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

package akamai

import (
	"strings"
	"testing"

	"github.com/akamai/AkamaiOPEN-edgegrid-golang/v13/pkg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/provider"
)

func TestCleanTargets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rtype   string
		targets []string
		want    []string
	}{
		{
			name:    "A is left alone",
			rtype:   endpoint.RecordTypeA,
			targets: []string{"10.0.0.1"},
			want:    []string{"10.0.0.1"},
		},
		{
			name:    "CNAME loses its trailing dot",
			rtype:   endpoint.RecordTypeCNAME,
			targets: []string{"target.example.com."},
			want:    []string{"target.example.com"},
		},
		{
			name:    "SRV loses its trailing dot",
			rtype:   endpoint.RecordTypeSRV,
			targets: []string{"0 0 443 target.example.com."},
			want:    []string{"0 0 443 target.example.com"},
		},
		{
			name:    "TXT gets quoted",
			rtype:   endpoint.RecordTypeTXT,
			targets: []string{"heritage=external-dns"},
			want:    []string{`"heritage=external-dns"`},
		},
		{
			name:    "already quoted TXT is not double quoted",
			rtype:   endpoint.RecordTypeTXT,
			targets: []string{`"heritage=external-dns"`},
			want:    []string{`"heritage=external-dns"`},
		},
		{
			// The API mangles quotes embedded in an owner value, hence the backticks.
			name:    "quotes inside an owner value become backticks",
			rtype:   endpoint.RecordTypeTXT,
			targets: []string{`external-dns/owner="default",heritage=external-dns`},
			want:    []string{"\"external-dns/owner=`default`,heritage=external-dns\""},
		},
		{
			// Found by FuzzCleanTargetsTXT: before the owner guard was dropped this
			// came back as "a"b", a value escaping its own quoting.
			name:    "a quote outside an owner value is escaped too",
			rtype:   endpoint.RecordTypeTXT,
			targets: []string{`a"b`},
			want:    []string{"\"a`b\""},
		},
		{
			// See TestTxtRoundTripIsLossyOnATrailingQuote: Trim eats the closing
			// quote of a value that ends the string, before the substitution runs.
			name:    "a quote closing the string is eaten by the outer trim",
			rtype:   endpoint.RecordTypeTXT,
			targets: []string{`heritage=external-dns,external-dns/owner="default"`},
			want:    []string{"\"heritage=external-dns,external-dns/owner=`default\""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, cleanTargets(tc.rtype, tc.targets...))
		})
	}
}

// The TXT round trip is not lossless, and this test exists to pin that rather than
// hide it. strings.Trim strips quote characters from both ends, so an owner value
// that closes the string ("...owner="default"") loses its own closing quote before
// the backtick substitution ever runs. The behaviour is inherited verbatim from the
// in-tree provider: every TXT registry record already in Edge DNS was written this
// way, so "fixing" it would make ExternalDNS rewrite all of them on the next sync.
func TestTxtRoundTripIsLossyOnATrailingQuote(t *testing.T) {
	original := `heritage=external-dns,external-dns/owner="default"`

	cleaned := cleanTargets(endpoint.RecordTypeTXT, original)
	back := trimTxtRdata(cleaned, endpoint.RecordTypeTXT)

	assert.Equal(t, []string{`"heritage=external-dns,external-dns/owner="default"`}, back)
}

// A quote in the middle survives intact, which is the common case.
func TestTxtRoundTripKeepsAnEmbeddedQuote(t *testing.T) {
	original := `external-dns/owner="default",heritage=external-dns`

	cleaned := cleanTargets(endpoint.RecordTypeTXT, original)
	back := trimTxtRdata(cleaned, endpoint.RecordTypeTXT)

	assert.Equal(t, []string{`"` + original + `"`}, back)
}

func TestTrimTxtRdataLeavesOtherTypesAlone(t *testing.T) {
	rdata := []string{"back`tick"}
	assert.Equal(t, []string{"back`tick"}, trimTxtRdata(rdata, endpoint.RecordTypeA))
}

func TestTTLAsInt(t *testing.T) {
	for _, tc := range []struct {
		name string
		ttl  endpoint.TTL
		want int
	}{
		{name: "unset falls back to the default", ttl: 0, want: defaultTTL},
		{name: "negative falls back to the default", ttl: -1, want: defaultTTL},
		{name: "a real value is kept", ttl: 300, want: 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ttlAsInt(tc.ttl))
		})
	}
}

func TestNewRecordSetTrimsTheTrailingDot(t *testing.T) {
	rs := newRecordSet("www.example.com.", endpoint.RecordTypeA, 300, []string{"10.0.0.1"})

	assert.Equal(t, "www.example.com", rs.Name)
	assert.Equal(t, 300, rs.TTL)
}

func TestRecordSetToEndpoint(t *testing.T) {
	ep, ok := recordSetToEndpoint(dns.RecordSet{
		Name:  "www.example.com",
		Type:  endpoint.RecordTypeA,
		TTL:   300,
		Rdata: []string{"10.0.0.1", "10.0.0.2"},
	})
	require.True(t, ok)
	assert.Equal(t, "www.example.com", ep.DNSName)
	assert.Equal(t, endpoint.TTL(300), ep.RecordTTL)
	assert.Equal(t, endpoint.Targets{"10.0.0.1", "10.0.0.2"}, ep.Targets)

	_, ok = recordSetToEndpoint(dns.RecordSet{Name: "example.com", Type: "SOA"})
	assert.False(t, ok, "SOA is not a type ExternalDNS manages")
}

func TestChangesByZone(t *testing.T) {
	zoneMap := provider.ZoneIDName{"a.example.com": "a.example.com", "b.example.com": "b.example.com"}
	endpoints := []*endpoint.Endpoint{
		endpoint.NewEndpoint("one.a.example.com", endpoint.RecordTypeA, "10.0.0.1"),
		endpoint.NewEndpoint("two.a.example.com", endpoint.RecordTypeA, "10.0.0.2"),
		endpoint.NewEndpoint("one.b.example.com", endpoint.RecordTypeA, "10.0.0.3"),
		endpoint.NewEndpoint("orphan.elsewhere.org", endpoint.RecordTypeA, "10.0.0.4"),
	}

	got := changesByZone(zoneMap, &endpoint.DomainFilter{}, endpoints)

	require.Len(t, got, 2, "the orphan has nowhere to go and is dropped")
	assert.Len(t, got["a.example.com"], 2)
	assert.Len(t, got["b.example.com"], 1)
}

func TestChangesByZoneHonoursExclusions(t *testing.T) {
	zoneMap := provider.ZoneIDName{"example.com": "example.com"}
	filter := endpoint.NewDomainFilterWithExclusions(
		[]string{"example.com"},
		[]string{"secret.example.com"},
	)
	endpoints := []*endpoint.Endpoint{
		endpoint.NewEndpoint("www.example.com", endpoint.RecordTypeA, "10.0.0.1"),
		endpoint.NewEndpoint("api.secret.example.com", endpoint.RecordTypeA, "10.0.0.2"),
	}

	got := changesByZone(zoneMap, filter, endpoints)

	require.Len(t, got["example.com"], 1, "the excluded name sits inside a managed zone but must not be written")
	assert.Equal(t, "www.example.com", got["example.com"][0].DNSName)
}

func TestChangesByZoneWithoutFilterKeepsEverything(t *testing.T) {
	zoneMap := provider.ZoneIDName{"example.com": "example.com"}
	endpoints := []*endpoint.Endpoint{
		endpoint.NewEndpoint("www.example.com", endpoint.RecordTypeA, "10.0.0.1"),
	}

	assert.Len(t, changesByZone(zoneMap, &endpoint.DomainFilter{}, endpoints)["example.com"], 1)
	assert.Len(t, changesByZone(zoneMap, nil, endpoints)["example.com"], 1)
}

// FuzzCleanTargetsTXT checks that TXT rdata leaves cleanTargets as one well-formed
// quoted string. Targets reach this function from the ApplyChanges body, so a
// value that breaks out of its own quoting would be rdata we did not intend to
// write.
func FuzzCleanTargetsTXT(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain",
		"\"quoted\"",
		"heritage=external-dns,external-dns/owner=default",
		"heritage=external-dns,external-dns/owner=\"default\"",
		"a\"b",
		"owner\"",
		"\"",
		"\"\"",
		"a`b",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, target string) {
		got := cleanTargets(endpoint.RecordTypeTXT, target)[0]

		if len(got) < 2 || got[0] != '"' || got[len(got)-1] != '"' {
			t.Fatalf("cleanTargets(TXT, %q) = %q, not a quoted string", target, got)
		}
		if inner := got[1 : len(got)-1]; strings.Contains(inner, "\"") {
			t.Fatalf("cleanTargets(TXT, %q) = %q, the value escapes its own quoting", target, got)
		}
	})
}
