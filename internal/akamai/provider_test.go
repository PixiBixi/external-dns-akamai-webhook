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
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/akamai/AkamaiOPEN-edgegrid-golang/v13/pkg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
)

// stub counts the calls each Edge DNS operation received. This provider is
// dominated by round trips, so the call count is the figure the tests assert;
// a duration measured against an in-memory stub would mean nothing.
type stub struct {
	mu sync.Mutex

	zones      []string
	recordSets map[string][]dns.RecordSet
	// failWith maps an operation name to the error it should return.
	failWith map[string]error

	listZones  int
	getRecords int
	creates    int
	updates    int
	deletes    int
}

func newStub(zones ...string) *stub {
	return &stub{zones: zones, recordSets: map[string][]dns.RecordSet{}, failWith: map[string]error{}}
}

func (s *stub) counts() (listZones, getRecords, creates, updates, deletes int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.listZones, s.getRecords, s.creates, s.updates, s.deletes
}

func (s *stub) ListZones(_ context.Context, _ dns.ListZonesRequest) (*dns.ZoneListResponse, error) {
	s.mu.Lock()
	s.listZones++
	s.mu.Unlock()
	if err := s.failWith["ListZones"]; err != nil {
		return nil, err
	}

	resp := &dns.ZoneListResponse{}
	for _, z := range s.zones {
		resp.Zones = append(resp.Zones, dns.ZoneResponse{Zone: z, ContractID: "ctr-1"})
	}

	return resp, nil
}

func (s *stub) GetRecordSets(_ context.Context, params dns.GetRecordSetsRequest) (*dns.GetRecordSetsResponse, error) {
	s.mu.Lock()
	s.getRecords++
	sets := s.recordSets[params.Zone]
	s.mu.Unlock()
	if err := s.failWith["GetRecordSets"]; err != nil {
		return nil, err
	}

	return &dns.GetRecordSetsResponse{RecordSets: sets}, nil
}

func (s *stub) CreateRecordSets(_ context.Context, _ dns.CreateRecordSetsRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creates++

	return s.failWith["CreateRecordSets"]
}

func (s *stub) UpdateRecord(_ context.Context, _ dns.UpdateRecordRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates++

	return s.failWith["UpdateRecord"]
}

func (s *stub) DeleteRecord(_ context.Context, _ dns.DeleteRecordRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++

	return s.failWith["DeleteRecord"]
}

func apiError(status int) error {
	return &dns.Error{StatusCode: status}
}

func newProvider(t *testing.T, s *stub, zones []string, cacheDuration time.Duration) *Provider {
	t.Helper()

	return New(s, Config{
		DomainFilter:      endpoint.NewDomainFilter(zones),
		ZoneCacheDuration: cacheDuration,
	})
}

func TestRecordsReadsEveryZone(t *testing.T) {
	s := newStub("a.example.com", "b.example.com")
	s.recordSets["a.example.com"] = []dns.RecordSet{
		{Name: "www.a.example.com", Type: endpoint.RecordTypeA, TTL: 300, Rdata: []string{"10.0.0.1"}},
	}
	s.recordSets["b.example.com"] = []dns.RecordSet{
		{Name: "www.b.example.com", Type: endpoint.RecordTypeA, TTL: 300, Rdata: []string{"10.0.0.2"}},
	}
	p := newProvider(t, s, []string{"example.com"}, 0)

	got, err := p.Records(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Indexed by zone, so the order does not depend on which listing finished first.
	assert.Equal(t, "www.a.example.com", got[0].DNSName)
	assert.Equal(t, "www.b.example.com", got[1].DNSName)

	listZones, getRecords, _, _, _ := s.counts()
	assert.Equal(t, 1, listZones)
	assert.Equal(t, 2, getRecords)
}

func TestRecordsSkipsUnsupportedTypesAndForeignNames(t *testing.T) {
	s := newStub("example.com")
	s.recordSets["example.com"] = []dns.RecordSet{
		{Name: "www.example.com", Type: endpoint.RecordTypeA, TTL: 300, Rdata: []string{"10.0.0.1"}},
		{Name: "example.com", Type: "SOA", TTL: 300, Rdata: []string{"ns1.akam.net."}},
		{Name: "www.elsewhere.org", Type: endpoint.RecordTypeA, TTL: 300, Rdata: []string{"10.0.0.9"}},
	}
	p := newProvider(t, s, []string{"example.com"}, 0)

	got, err := p.Records(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "www.example.com", got[0].DNSName)
}

// A zone that cannot be listed must fail the sync. Returning a partial view would
// have ExternalDNS believe those records do not exist.
func TestRecordsFailsWhenAZoneCannotBeListed(t *testing.T) {
	s := newStub("example.com")
	s.failWith["GetRecordSets"] = apiError(http.StatusInternalServerError)
	p := newProvider(t, s, []string{"example.com"}, 0)

	_, err := p.Records(t.Context())
	require.Error(t, err)
}

// The steady-state path: nothing to apply must cost nothing at all.
func TestApplyChangesEmptyIssuesNoCall(t *testing.T) {
	s := newStub("example.com")
	p := newProvider(t, s, []string{"example.com"}, 0)

	require.NoError(t, p.ApplyChanges(t.Context(), &plan.Changes{}))
	listZones, getRecords, creates, updates, deletes := s.counts()
	assert.Equal(t, 0, listZones)
	assert.Equal(t, 0, getRecords)
	assert.Equal(t, 0, creates)
	assert.Equal(t, 0, updates)
	assert.Equal(t, 0, deletes)
}

func TestApplyChangesBatchesCreatesPerZone(t *testing.T) {
	s := newStub("a.example.com", "b.example.com")
	p := newProvider(t, s, []string{"example.com"}, 0)

	changes := &plan.Changes{Create: []*endpoint.Endpoint{
		endpoint.NewEndpoint("one.a.example.com", endpoint.RecordTypeA, "10.0.0.1"),
		endpoint.NewEndpoint("two.a.example.com", endpoint.RecordTypeA, "10.0.0.2"),
		endpoint.NewEndpoint("one.b.example.com", endpoint.RecordTypeA, "10.0.0.3"),
	}}
	require.NoError(t, p.ApplyChanges(t.Context(), changes))

	// Three records, two zones, one call per zone.
	_, _, creates, _, _ := s.counts()
	assert.Equal(t, 2, creates)
}

// Deletes and updates are addressed by name and type, so nothing is read back.
func TestApplyChangesDoesNotReadRecordsBack(t *testing.T) {
	s := newStub("example.com")
	p := newProvider(t, s, []string{"example.com"}, 0)

	changes := &plan.Changes{
		Delete:    []*endpoint.Endpoint{endpoint.NewEndpoint("gone.example.com", endpoint.RecordTypeA, "10.0.0.1")},
		UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpoint("moved.example.com", endpoint.RecordTypeA, "10.0.0.2")},
	}
	require.NoError(t, p.ApplyChanges(t.Context(), changes))

	_, getRecords, _, updates, deletes := s.counts()
	assert.Equal(t, 1, deletes)
	assert.Equal(t, 1, updates)
	assert.Equal(t, 0, getRecords, "the record must not be read back before writing it")
}

// Deleting a record that is already absent is the outcome we wanted.
func TestApplyChangesTolerates404OnDelete(t *testing.T) {
	s := newStub("example.com")
	s.failWith["DeleteRecord"] = apiError(http.StatusNotFound)
	p := newProvider(t, s, []string{"example.com"}, 0)

	changes := &plan.Changes{
		Delete: []*endpoint.Endpoint{endpoint.NewEndpoint("gone.example.com", endpoint.RecordTypeA, "10.0.0.1")},
	}
	assert.NoError(t, p.ApplyChanges(t.Context(), changes))
}

func TestApplyChangesSkipsEndpointsOutsideTheZones(t *testing.T) {
	s := newStub("example.com")
	p := newProvider(t, s, []string{"example.com"}, 0)

	changes := &plan.Changes{Create: []*endpoint.Endpoint{
		endpoint.NewEndpoint("www.elsewhere.org", endpoint.RecordTypeA, "10.0.0.1"),
	}}
	require.NoError(t, p.ApplyChanges(t.Context(), changes))

	_, _, creates, _, _ := s.counts()
	assert.Equal(t, 0, creates)
}

func TestDryRunWritesNothing(t *testing.T) {
	s := newStub("example.com")
	p := New(s, Config{DomainFilter: endpoint.NewDomainFilter([]string{"example.com"}), DryRun: true})

	changes := &plan.Changes{
		Create:    []*endpoint.Endpoint{endpoint.NewEndpoint("new.example.com", endpoint.RecordTypeA, "10.0.0.1")},
		Delete:    []*endpoint.Endpoint{endpoint.NewEndpoint("old.example.com", endpoint.RecordTypeA, "10.0.0.2")},
		UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpoint("mod.example.com", endpoint.RecordTypeA, "10.0.0.3")},
	}
	require.NoError(t, p.ApplyChanges(t.Context(), changes))

	_, _, creates, updates, deletes := s.counts()
	assert.Equal(t, 0, creates)
	assert.Equal(t, 0, updates)
	assert.Equal(t, 0, deletes)
}

// The zone cache exists to spare the second listing of a sync. With it off, both
// Records and ApplyChanges pay for their own.
func TestZoneCache(t *testing.T) {
	for _, tc := range []struct {
		name          string
		duration      time.Duration
		wantListZones int
	}{
		{name: "enabled", duration: time.Hour, wantListZones: 1},
		{name: "disabled", duration: 0, wantListZones: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub("example.com")
			p := newProvider(t, s, []string{"example.com"}, tc.duration)

			_, err := p.Records(t.Context())
			require.NoError(t, err)
			changes := &plan.Changes{Create: []*endpoint.Endpoint{
				endpoint.NewEndpoint("www.example.com", endpoint.RecordTypeA, "10.0.0.1"),
			}}
			require.NoError(t, p.ApplyChanges(t.Context(), changes))

			listZones, _, _, _, _ := s.counts()
			assert.Equal(t, tc.wantListZones, listZones)
		})
	}
}

func TestGetDomainFilterIsReported(t *testing.T) {
	s := newStub("example.com")
	p := newProvider(t, s, []string{"example.com"}, 0)

	assert.True(t, p.GetDomainFilter().Match("www.example.com"))
	assert.False(t, p.GetDomainFilter().Match("www.elsewhere.org"))
}

// ExternalDNS retries 5xx and only 5xx. Anything permanent must not be retried,
// or a bad credential burns quota until the sync deadline.
func TestRetryable(t *testing.T) {
	p := newProvider(t, newStub(), []string{"example.com"}, 0)

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "rate limited", err: apiError(http.StatusTooManyRequests), want: true},
		{name: "upstream fault", err: apiError(http.StatusBadGateway), want: true},
		{name: "transport failure", err: errors.New("connection reset by peer"), want: true},
		{name: "unauthorized", err: apiError(http.StatusUnauthorized), want: false},
		{name: "rejected request", err: apiError(http.StatusBadRequest), want: false},
		{name: "not found", err: apiError(http.StatusNotFound), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, p.Retryable(tc.err))
		})
	}
}
