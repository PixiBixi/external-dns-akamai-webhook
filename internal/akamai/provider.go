/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package akamai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/akamai/AkamaiOPEN-edgegrid-golang/v13/pkg/dns"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"

	"github.com/PixiBixi/external-dns-akamai-webhook/internal/logsafe"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
	"sigs.k8s.io/external-dns/provider/blueprint"
)

// zoneListConcurrency bounds the parallel recordset listings issued by Records.
// Edge DNS enforces per-account API quotas, so this stays low on purpose.
const zoneListConcurrency = 4

// Config configures the provider.
type Config struct {
	DomainFilter *endpoint.DomainFilter
	// ContractIDs restricts the zone listing to these Akamai contracts.
	ContractIDs []string
	// ZoneCacheDuration reuses the zone listing between Records and ApplyChanges.
	// 0 disables the cache, which lists zones twice per sync.
	ZoneCacheDuration time.Duration
	DryRun            bool
}

// Provider serves Edge DNS records to ExternalDNS over the webhook API.
type Provider struct {
	provider.BaseProvider

	client       EdgeDNS
	domainFilter *endpoint.DomainFilter
	contractIDs  []string
	dryRun       bool
	zonesCache   *blueprint.ZoneCache[[]zone]
}

type zone struct {
	ContractID string
	Name       string
}

// New builds a provider around an Edge DNS client.
func New(client EdgeDNS, cfg Config) *Provider {
	duration := cfg.ZoneCacheDuration
	if duration < 0 {
		duration = defaultZoneCacheDuration
	}

	return &Provider{
		client:       client,
		domainFilter: cfg.DomainFilter,
		contractIDs:  cfg.ContractIDs,
		dryRun:       cfg.DryRun,
		zonesCache:   blueprint.NewZoneCache[[]zone](duration),
	}
}

// GetDomainFilter reports the zones this provider is willing to manage. ExternalDNS
// reads it during negotiation and stops sending endpoints outside it.
func (p *Provider) GetDomainFilter() endpoint.DomainFilterInterface {
	return p.domainFilter
}

// verb marks a log line as a dry run. Logging "creating recordset" and then not
// creating it makes the logs unreadable at the moment they matter most, which is
// when somebody is checking whether a run wrote anything.
func (p *Provider) verb(action string) string {
	if p.dryRun {
		return "dry run, would be " + action
	}

	return action
}

// Retryable reports whether ExternalDNS should retry after this error. Rate limits
// and upstream faults are worth another attempt; a rejected request or a bad
// credential is not, and retrying it only burns quota.
func (p *Provider) Retryable(err error) bool {
	return isRetryable(err)
}

// Records returns every supported record in the configured zones.
func (p *Provider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	zones, err := p.zones(ctx)
	if err != nil {
		return nil, err
	}
	if len(zones) == 0 {
		log.Warn("no zones matched the domain filter and contract IDs")
		return nil, nil
	}

	// The round trips dominate; the decoding does not. Results are indexed by zone
	// so the output does not depend on which listing finishes first.
	perZone := make([][]*endpoint.Endpoint, len(zones))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(zoneListConcurrency)
	for i, z := range zones {
		group.Go(func() error {
			eps, err := p.zoneEndpoints(groupCtx, z.Name)
			if err != nil {
				return fmt.Errorf("listing recordsets in zone %s: %w", z.Name, err)
			}
			perZone[i] = eps
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}

	var endpoints []*endpoint.Endpoint
	for _, eps := range perZone {
		endpoints = append(endpoints, eps...)
	}
	log.Debugf("read %d endpoints across %d zones", len(endpoints), len(zones))

	return endpoints, nil
}

func (p *Provider) zoneEndpoints(ctx context.Context, zoneName string) ([]*endpoint.Endpoint, error) {
	resp, err := p.client.GetRecordSets(ctx, dns.GetRecordSetsRequest{
		Zone:      zoneName,
		QueryArgs: &dns.RecordSetQueryArgs{ShowAll: true},
	})
	if err != nil {
		return nil, err
	}

	var endpoints []*endpoint.Endpoint
	for _, rs := range resp.RecordSets {
		if !p.domainFilter.Match(rs.Name) {
			continue
		}
		if ep, ok := recordSetToEndpoint(rs); ok {
			endpoints = append(endpoints, ep)
		}
	}

	return endpoints, nil
}

// ApplyChanges writes the plan to Edge DNS.
func (p *Provider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	// Nothing to do means nothing to fetch. In steady state this is the common
	// path and it must not cost a zone listing.
	if !changes.HasChanges() {
		log.Debug("no changes to apply")
		return nil
	}

	zones, err := p.zones(ctx)
	if err != nil {
		return err
	}
	zoneMap := provider.ZoneIDName{}
	for _, z := range zones {
		zoneMap[z.Name] = z.Name
	}

	if err := p.create(ctx, zoneMap, changes.Create); err != nil {
		return err
	}
	if err := p.delete(ctx, zoneMap, changes.Delete); err != nil {
		return err
	}

	return p.update(ctx, zoneMap, changes.UpdateNew)
}

func (p *Provider) create(ctx context.Context, zoneMap provider.ZoneIDName, endpoints []*endpoint.Endpoint) error {
	for zoneName, eps := range changesByZone(zoneMap, p.domainFilter, endpoints) {
		sets := &dns.RecordSets{RecordSets: make([]dns.RecordSet, 0, len(eps))}
		for _, ep := range eps {
			sets.RecordSets = append(sets.RecordSets, newRecordSet(
				ep.DNSName,
				ep.RecordType,
				ttlAsInt(ep.RecordTTL),
				cleanTargets(ep.RecordType, ep.Targets...),
			))
			log.WithFields(log.Fields{"zone": logsafe.String(zoneName), "record": logsafe.String(ep.DNSName), "type": logsafe.String(ep.RecordType)}).Info(p.verb("creating recordset"))
		}

		if p.dryRun {
			continue
		}
		// Edge DNS creates a whole batch per zone in one call.
		if err := p.client.CreateRecordSets(ctx, dns.CreateRecordSetsRequest{
			RecordSets: sets,
			Zone:       zoneName,
			RecLock:    []bool{true},
		}); err != nil {
			return fmt.Errorf("creating %d recordsets in zone %s: %w", len(sets.RecordSets), zoneName, err)
		}
	}

	return nil
}

func (p *Provider) delete(ctx context.Context, zoneMap provider.ZoneIDName, endpoints []*endpoint.Endpoint) error {
	for _, ep := range endpoints {
		zoneName, ok := zoneFor(zoneMap, p.domainFilter, ep)
		if !ok {
			log.Debugf("skipping deletion of %s %s: outside the configured zones", logsafe.String(ep.RecordType), logsafe.String(ep.DNSName))
			continue
		}
		log.WithFields(log.Fields{"zone": logsafe.String(zoneName), "record": logsafe.String(ep.DNSName), "type": logsafe.String(ep.RecordType)}).Info(p.verb("deleting recordset"))

		if p.dryRun {
			continue
		}
		// DELETE addresses the record by name and type, so there is nothing to read
		// back first. A record that is already gone is the outcome we wanted.
		err := p.client.DeleteRecord(ctx, dns.DeleteRecordRequest{
			Zone:       zoneName,
			Name:       strings.TrimSuffix(ep.DNSName, "."),
			RecordType: ep.RecordType,
			RecLock:    []bool{true},
		})
		if err != nil {
			if isNotFound(err) {
				log.Debugf("%s %s was already absent", logsafe.String(ep.RecordType), logsafe.String(ep.DNSName))
				continue
			}
			return fmt.Errorf("deleting %s %s in zone %s: %w", ep.RecordType, ep.DNSName, zoneName, err)
		}
	}

	return nil
}

func (p *Provider) update(ctx context.Context, zoneMap provider.ZoneIDName, endpoints []*endpoint.Endpoint) error {
	for _, ep := range endpoints {
		zoneName, ok := zoneFor(zoneMap, p.domainFilter, ep)
		if !ok {
			log.Debugf("skipping update of %s %s: outside the configured zones", logsafe.String(ep.RecordType), logsafe.String(ep.DNSName))
			continue
		}
		log.WithFields(log.Fields{"zone": logsafe.String(zoneName), "record": logsafe.String(ep.DNSName), "type": logsafe.String(ep.RecordType)}).Info(p.verb("updating recordset"))

		if p.dryRun {
			continue
		}
		// PUT replaces the recordset addressed by name and type, and both the TTL and
		// the rdata come from the endpoint, so reading the record back buys nothing.
		//
		// Not UpdateRecordSets: it maps to PUT /zones/{zone}/recordsets, which the
		// Edge DNS reference defines as "Replaces all record sets that currently
		// exist with the list provided". Batching updates through it deletes every
		// record the batch omits. Do NOT turn this loop into one call.
		ttl := ttlAsInt(ep.RecordTTL)
		err := p.client.UpdateRecord(ctx, dns.UpdateRecordRequest{
			Record: &dns.RecordBody{
				Name:       strings.TrimSuffix(ep.DNSName, "."),
				RecordType: ep.RecordType,
				TTL:        &ttl,
				Target:     cleanTargets(ep.RecordType, ep.Targets...),
			},
			Zone:    zoneName,
			RecLock: []bool{true},
		})
		if err != nil {
			return fmt.Errorf("updating %s %s in zone %s: %w", ep.RecordType, ep.DNSName, zoneName, err)
		}
	}

	return nil
}

// zones lists the primary zones matching the domain filter, from cache when one is
// configured and still fresh.
func (p *Provider) zones(ctx context.Context) ([]zone, error) {
	if !p.zonesCache.Expired() {
		return p.zonesCache.Get(), nil
	}

	req := dns.ListZonesRequest{Types: "primary", ShowAll: true}
	if len(p.contractIDs) > 0 {
		req.ContractIDs = strings.Join(p.contractIDs, ",")
	}
	resp, err := p.client.ListZones(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("listing Edge DNS zones: %w", err)
	}

	zones := make([]zone, 0, len(resp.Zones))
	for i := range resp.Zones {
		if z := &resp.Zones[i]; p.domainFilter.Match(z.Zone) {
			zones = append(zones, zone{ContractID: z.ContractID, Name: z.Zone})
		}
	}
	p.zonesCache.Reset(zones)

	return zones, nil
}
