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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/provider/webhook/api"
)

func TestNegotiateMediaType(t *testing.T) {
	for _, tc := range []struct {
		name           string
		header         string
		wantOK         bool
		wantBadVersion string
	}{
		{name: "exactly what the client sends", header: api.MediaTypeFormatAndVersion, wantOK: true},
		{name: "unversioned is treated as version 1", header: mediaTypeBase, wantOK: true},
		{name: "quoted version", header: mediaTypeBase + `;version="1"`, wantOK: true},
		{name: "spaces around the parameter", header: mediaTypeBase + " ; version=1", wantOK: true},
		{name: "one entry among several", header: "application/json, " + api.MediaTypeFormatAndVersion, wantOK: true},
		{name: "a future version is refused", header: mediaTypeBase + ";version=2", wantBadVersion: "2"},
		{name: "a client accepting anything is conformant", header: "*/*", wantOK: true},
		{name: "application wildcard", header: "application/*", wantOK: true},
		{name: "plain json is not the protocol", header: "application/json"},
		{name: "empty", header: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, badVersion := negotiate(tc.header, true)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantBadVersion, badVersion)
		})
	}
}

// A wildcard says what a client will take, never what a body contains.
func TestWildcardIsAcceptOnly(t *testing.T) {
	ok, _ := negotiate("*/*", true)
	assert.True(t, ok, "Accept: */* is conformant")

	ok, _ = negotiate("*/*", false)
	assert.False(t, ok, "Content-Type: */* names nothing")
}

func TestMissingHeaderIs406(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	s := newTestServer(f)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, api.UrlRecords, nil)
	rec := httptest.NewRecorder()
	s.provider.Handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotAcceptable, rec.Code)
}

func TestUnsupportedVersionIs415(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	s := newTestServer(f)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, api.UrlRecords, nil)
	req.Header.Set(acceptHeader, mediaTypeBase+";version=99")
	rec := httptest.NewRecorder()
	s.provider.Handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
	assert.Contains(t, rec.Body.String(), "99")
}

// POST /records is the one route the ExternalDNS client sends no Accept on.
// Requiring it there would reject a conformant client.
func TestApplyChangesNeedsNoAcceptHeader(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	s := newTestServer(f)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, api.UrlRecords, strings.NewReader(`{}`))
	req.Header.Set(api.ContentTypeHeader, api.MediaTypeFormatAndVersion)
	rec := httptest.NewRecorder()
	s.provider.Handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestApplyChangesRejectsAForeignContentType(t *testing.T) {
	f := &fakeProvider{domainFilter: endpoint.NewDomainFilter([]string{"example.com"})}
	s := newTestServer(f)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, api.UrlRecords, strings.NewReader(`{}`))
	req.Header.Set(api.ContentTypeHeader, "application/json")
	rec := httptest.NewRecorder()
	s.provider.Handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
	assert.Nil(t, f.applied)
}

// FuzzNegotiate drives the Accept and Content-Type parser with arbitrary bytes.
// Both headers arrive unauthenticated on the provider socket and are parsed before
// anything else, so this is the first code an unexpected input reaches.
func FuzzNegotiate(f *testing.F) {
	for _, seed := range []string{
		"",
		"*/*",
		"application/*",
		api.MediaTypeFormatAndVersion,
		mediaTypeBase,
		mediaTypeBase + ";version=1",
		mediaTypeBase + ";version=\"1\"",
		mediaTypeBase + ";version=2",
		mediaTypeBase + ";charset=utf-8;version=1",
		"text/html, " + mediaTypeBase + ";version=1",
		";;;;",
		"=;=;=",
		mediaTypeBase + ";version=",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, header string) {
		wide, _ := negotiate(header, true)
		narrow, unsupported := negotiate(header, false)

		// Allowing the wildcard can only widen what is accepted. If this ever
		// inverts, a body labelled "*/*" would be parsed as the webhook format.
		if narrow && !wide {
			t.Fatalf("negotiate(%q) accepted without the wildcard but rejected with it", header)
		}
		// A reported version is echoed into the 415 body, so it must come from the
		// header rather than be synthesised.
		if unsupported != "" && !strings.Contains(header, unsupported) {
			t.Fatalf("negotiate(%q) reported version %q that is not in the header", header, unsupported)
		}
		// The rejection path must never name the version this provider speaks, or
		// the operator reads "unsupported version 1" while the server speaks 1.
		if !narrow && unsupported == supportedVersion {
			t.Fatalf("negotiate(%q) rejected the header while reporting the supported version", header)
		}
	})
}
