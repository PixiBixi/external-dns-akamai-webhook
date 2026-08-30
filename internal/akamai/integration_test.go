//go:build integration

/*
Copyright 2026 Jeremy Delgado.

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

// Read-only checks against a live Edge DNS account. Behind a build tag because
// they need real credentials and burn real API quota:
//
//	go test -tags=integration -v ./internal/akamai/... -run TestLive
//
// Credentials come from ~/.edgerc, or AKAMAI_EDGERC_PATH and
// AKAMAI_EDGERC_SECTION. Nothing here writes: no create, no update, no delete.
package akamai

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sigs.k8s.io/external-dns/endpoint"
)

func liveClient(t *testing.T) EdgeDNS {
	t.Helper()

	path := os.Getenv("AKAMAI_EDGERC_PATH")
	if path == "" {
		path = os.Getenv("HOME") + "/.edgerc"
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no edgerc at %s", path)
	}

	section := os.Getenv("AKAMAI_EDGERC_SECTION")
	if section == "" {
		section = "default"
	}

	client, err := NewClient(Credentials{EdgercPath: path, EdgercSection: section}, "external-dns-akamai-webhook/integration-test")
	require.NoError(t, err, "building the client from %s section %q", path, section)

	return client
}

// TestLiveZonesFromEnv covers the other half of buildConfig. Supplying the four
// credentials explicitly does not read .edgerc at all: it assembles an
// edgegrid.Config by hand, including MaxBody and HeaderToSign, and that is the
// path the shipped manifests use since they pull the values from a Secret.
// Without this the documented default would be the untested branch.
func TestLiveZonesFromEnv(t *testing.T) {
	creds := Credentials{
		ServiceConsumerDomain: os.Getenv("AKAMAI_SERVICECONSUMERDOMAIN"),
		ClientToken:           os.Getenv("AKAMAI_CLIENT_TOKEN"),
		ClientSecret:          os.Getenv("AKAMAI_CLIENT_SECRET"),
		AccessToken:           os.Getenv("AKAMAI_ACCESS_TOKEN"),
	}
	if !creds.complete() {
		t.Skip("set the four AKAMAI_* credential variables to exercise this path")
	}

	client, err := NewClient(creds, "external-dns-akamai-webhook/integration-test")
	require.NoError(t, err)

	p := New(client, Config{DomainFilter: &endpoint.DomainFilter{}})
	zones, err := p.zones(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, zones, "the explicit credentials authenticated but returned no zone")

	t.Logf("%d primary zones through explicit credentials", len(zones))
}

// TestLiveZones answers the questions no stub can: whether the credentials
// resolve, whether ShowAll really returns everything, and what the response
// metadata says about paging.
func TestLiveZones(t *testing.T) {
	p := New(liveClient(t), Config{DomainFilter: &endpoint.DomainFilter{}})

	start := time.Now()
	zones, err := p.zones(t.Context())
	require.NoError(t, err)

	t.Logf("%d primary zones in %s", len(zones), time.Since(start).Round(time.Millisecond))
	for i, z := range zones {
		if i >= 10 {
			t.Logf("  ... and %d more", len(zones)-10)
			break
		}
		t.Logf("  %-45s contract=%s", z.Name, z.ContractID)
	}
}

// TestLiveRecords reads one zone end to end, which is what exercises the
// conversion against real recordsets rather than the ones a stub hands back.
// Set AKAMAI_TEST_ZONE to pick the zone; without it the test skips.
func TestLiveRecords(t *testing.T) {
	zone := os.Getenv("AKAMAI_TEST_ZONE")
	if zone == "" {
		t.Skip("set AKAMAI_TEST_ZONE to read a zone")
	}

	p := New(liveClient(t), Config{DomainFilter: endpoint.NewDomainFilter([]string{zone})})

	start := time.Now()
	endpoints, err := p.Records(t.Context())
	require.NoError(t, err)

	t.Logf("%d endpoints in %s", len(endpoints), time.Since(start).Round(time.Millisecond))
	byType := map[string]int{}
	for _, ep := range endpoints {
		byType[ep.RecordType]++
	}
	t.Logf("by type: %v", byType)
	for i, ep := range endpoints {
		if i >= 15 {
			t.Logf("  ... and %d more", len(endpoints)-15)
			break
		}
		t.Logf("  %-45s %-6s ttl=%-6d %v", ep.DNSName, ep.RecordType, ep.RecordTTL, ep.Targets)
	}
}
