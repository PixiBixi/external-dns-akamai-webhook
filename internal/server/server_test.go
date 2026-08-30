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

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
	"sigs.k8s.io/external-dns/provider/webhook/api"
)

type fakeProvider struct {
	provider.BaseProvider

	records      []*endpoint.Endpoint
	recordsErr   error
	applyErr     error
	applied      *plan.Changes
	retryable    bool
	domainFilter *endpoint.DomainFilter
	sawContext   context.Context //nolint:containedctx // captured to assert propagation
}

func (f *fakeProvider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	f.sawContext = ctx
	return f.records, f.recordsErr
}

func (f *fakeProvider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	f.sawContext = ctx
	f.applied = changes

	return f.applyErr
}

func (f *fakeProvider) GetDomainFilter() endpoint.DomainFilterInterface {
	return f.domainFilter
}

func (f *fakeProvider) Retryable(error) bool { return f.retryable }

func newTestServer(p provider.Provider) *Server {
	return New(Options{Provider: p, MaxBodySize: 1 << 20})
}

func do(t *testing.T, s *Server, method, target string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	// What the ExternalDNS client sends: Accept on the reads, Content-Type on the
	// writes. See internal/server/mediatype.go.
	if method == http.MethodGet {
		req.Header.Set("Accept", api.MediaTypeFormatAndVersion)
	} else {
		req.Header.Set(api.ContentTypeHeader, api.MediaTypeFormatAndVersion)
	}
	rec := httptest.NewRecorder()
	s.provider.Handler.ServeHTTP(rec, req)

	return rec
}

func TestNegotiateReportsTheDomainFilter(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	rec := do(t, newTestServer(f), http.MethodGet, "/", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, api.MediaTypeFormatAndVersion, rec.Header().Get(api.ContentTypeHeader))

	// ExternalDNS decodes this straight into an endpoint.DomainFilter.
	var got endpoint.DomainFilter
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.True(t, got.Match("www.example.com"))
}

func TestGetRecords(t *testing.T) {
	f := &fakeProvider{
		domainFilter: endpoint.NewDomainFilter([]string{"example.com"}),
		records: []*endpoint.Endpoint{
			endpoint.NewEndpoint("www.example.com", endpoint.RecordTypeA, "10.0.0.1"),
		},
	}
	rec := do(t, newTestServer(f), http.MethodGet, api.UrlRecords, "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, api.MediaTypeFormatAndVersion, rec.Header().Get(api.ContentTypeHeader))

	var got []*endpoint.Endpoint
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "www.example.com", got[0].DNSName)
}

// ExternalDNS treats 204 as the success case for ApplyChanges.
func TestApplyChangesAnswers204(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}

	changes := plan.Changes{Create: []*endpoint.Endpoint{
		endpoint.NewEndpoint("www.example.com", endpoint.RecordTypeA, "10.0.0.1"),
	}}
	body, err := json.Marshal(changes)
	require.NoError(t, err)

	rec := do(t, newTestServer(f), http.MethodPost, api.UrlRecords, string(body))

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, f.applied)
	assert.Len(t, f.applied.Create, 1)
}

func TestAdjustEndpoints(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}

	body, err := json.Marshal([]*endpoint.Endpoint{
		endpoint.NewEndpoint("www.example.com", endpoint.RecordTypeA, "10.0.0.1"),
	})
	require.NoError(t, err)

	rec := do(t, newTestServer(f), http.MethodPost, api.UrlAdjustEndpoints, string(body))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, api.MediaTypeFormatAndVersion, rec.Header().Get(api.ContentTypeHeader))
}

// The reason these handlers are ours: ExternalDNS retries 5xx and only 5xx, so a
// permanent failure answered with 500 is retried until the sync deadline.
func TestProviderErrorsMapToTheRightStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		retryable  bool
		wantStatus int
	}{
		{name: "transient failures are retried", retryable: true, wantStatus: http.StatusInternalServerError},
		{name: "permanent failures are not", retryable: false, wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeProvider{
				domainFilter: endpoint.NewDomainFilter([]string{"example.com"}),
				recordsErr:   errors.New("boom"),
				retryable:    tc.retryable,
			}
			rec := do(t, newTestServer(f), http.MethodGet, api.UrlRecords, "")

			assert.Equal(t, tc.wantStatus, rec.Code)
			// ExternalDNS drains the body to reuse the connection, so it is never empty.
			assert.NotEmpty(t, rec.Body.Bytes())
		})
	}
}

func TestMalformedBodyIsRejectedWithout500(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	rec := do(t, newTestServer(f), http.MethodPost, api.UrlRecords, "{not json")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, f.applied, "a body that will not decode must not reach the provider")
}

func TestOversizedBodyIsRejectedWith413(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	s := New(Options{Provider: f, MaxBodySize: 16})

	rec := do(t, s, http.MethodPost, api.UrlRecords, `{"Create":[`+strings.Repeat(" ", 128)+`]}`)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestUnsupportedMethodIsNotAServerError(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	rec := do(t, newTestServer(f), http.MethodDelete, api.UrlRecords, "")

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// api.StartHTTPApi calls the provider with context.Background(). Ours passes the
// request context, so a cancelled sync stops the outbound Edge DNS calls too.
func TestRequestContextReachesTheProvider(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	s := newTestServer(f)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, api.UrlRecords, nil)
	req.Header.Set("Accept", api.MediaTypeFormatAndVersion)
	s.provider.Handler.ServeHTTP(httptest.NewRecorder(), req)

	require.NotNil(t, f.sawContext)
	cancel()
	assert.Error(t, f.sawContext.Err(), "the handler passed the request context, not a fresh one")
}

func TestHealthAndMetrics(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	s := newTestServer(f)

	for _, path := range []string{"/healthz", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.health.Handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	// Port 0 lets the kernel pick, so the test never collides with a busy port.
	s := New(Options{Provider: f, ProviderAddr: "127.0.0.1:0", HealthAddr: "127.0.0.1:0"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	cancel()
	assert.NoError(t, <-done)
}
