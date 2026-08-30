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
	"strings"

	"github.com/akamai/AkamaiOPEN-edgegrid-golang/v13/pkg/dns"
	log "github.com/sirupsen/logrus"

	"github.com/PixiBixi/external-dns-akamai-webhook/internal/logsafe"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/provider"
)

const (
	// defaultTTL is what Edge DNS gets when an endpoint carries no TTL.
	defaultTTL = 600
	maxUint    = ^uint(0)
	maxInt     = int(maxUint >> 1)
)

// newRecordSet builds an Edge DNS recordset from already-cleaned targets.
func newRecordSet(dnsName, recordType string, ttl int, targets []string) dns.RecordSet {
	return dns.RecordSet{
		Name:  strings.TrimSuffix(dnsName, "."),
		Rdata: targets,
		Type:  recordType,
		TTL:   ttl,
	}
}

// cleanTargets preps recordset rdata for Edge DNS.
//
// CNAME and SRV rdata must not carry the trailing dot. TXT rdata must be quoted,
// and the API mangles quotes embedded in an owner value, so those are swapped for
// backticks on the way in and swapped back by trimTxtRdata on the way out.
func cleanTargets(rtype string, targets ...string) []string {
	switch rtype {
	case endpoint.RecordTypeCNAME, endpoint.RecordTypeSRV:
		for idx, target := range targets {
			targets[idx] = strings.TrimSuffix(target, ".")
		}
	case endpoint.RecordTypeTXT:
		for idx, target := range targets {
			target = strings.Trim(target, "\"")
			if strings.Contains(target, "owner") && strings.Contains(target, "\"") {
				target = strings.ReplaceAll(target, "\"", "`")
			}
			targets[idx] = "\"" + target + "\""
		}
	}

	return targets
}

// trimTxtRdata reverses cleanTargets' backtick substitution on received TXT rdata.
func trimTxtRdata(rdata []string, rtype string) []string {
	if rtype == endpoint.RecordTypeTXT {
		for idx, d := range rdata {
			if strings.Contains(d, "`") {
				rdata[idx] = strings.ReplaceAll(d, "`", "\"")
			}
		}
	}

	return rdata
}

// ttlAsInt clamps an endpoint TTL into the range Edge DNS accepts, falling back
// to defaultTTL when the endpoint carries none.
func ttlAsInt(src endpoint.TTL) int {
	ttl := defaultTTL
	if src > 0 && int64(src) <= int64(maxInt) {
		ttl = int(src)
	}

	return ttl
}

// recordSetToEndpoint converts a recordset, or reports false when the record type
// is one ExternalDNS does not manage.
func recordSetToEndpoint(rs dns.RecordSet) (*endpoint.Endpoint, bool) {
	if !provider.SupportedRecordType(rs.Type) {
		log.Debugf("skipping %s %s: record type not supported", logsafe.String(rs.Type), logsafe.String(rs.Name))
		return nil, false
	}

	return endpoint.NewEndpointWithTTL(
		rs.Name,
		rs.Type,
		endpoint.TTL(rs.TTL),
		trimTxtRdata(rs.Rdata, rs.Type)...,
	), true
}

// changesByZone groups endpoints by the zone that contains them. Endpoints that
// match no zone are dropped, since there is nowhere to write them.
func changesByZone(zoneMap provider.ZoneIDName, endpoints []*endpoint.Endpoint) map[string][]*endpoint.Endpoint {
	byZone := make(map[string][]*endpoint.Endpoint, len(zoneMap))
	for _, ep := range endpoints {
		zone, _ := zoneMap.FindZone(ep.DNSName)
		if zone == "" {
			log.Debugf("skipping %s %s: outside the configured zones", logsafe.String(ep.RecordType), logsafe.String(ep.DNSName))
			continue
		}
		byZone[zone] = append(byZone[zone], ep)
	}

	return byZone
}
