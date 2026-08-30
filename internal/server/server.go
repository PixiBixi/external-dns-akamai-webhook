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

// Package server exposes a DNS provider over the ExternalDNS webhook HTTP API.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
	"sigs.k8s.io/external-dns/provider/webhook/api"
)

// Retryable is implemented by providers that can tell a transient failure from a
// permanent one. ExternalDNS retries 5xx and only 5xx, so a provider that cannot
// make the distinction has every bad credential retried until the sync deadline.
type Retryable interface {
	Retryable(err error) bool
}

// Options configures the two listeners.
type Options struct {
	Provider provider.Provider
	// ProviderAddr serves the webhook API. Bind it to localhost: it is unauthenticated.
	ProviderAddr string
	// HealthAddr serves /healthz and /metrics, and is safe to expose in the pod.
	HealthAddr        string
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	// MaxBodySize caps request bodies. 0 or less disables the cap.
	MaxBodySize int64
}

// Server owns the provider and health listeners.
type Server struct {
	opts     Options
	provider http.Server
	health   http.Server
}

// New wires both listeners without starting them.
//
// The handlers are ours rather than api.StartHTTPApi's, for three reasons that
// matter in production: that implementation calls the provider with
// context.Background() so a cancelled request never reaches Edge DNS, it answers
// every provider error with 500 so ExternalDNS retries permanent failures, and it
// writes an empty body on error, which the webhook guide asks providers not to do.
// The wire format still comes from api's own constants, so it cannot drift.
func New(opts Options) *Server {
	s := &Server{opts: opts}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleNegotiate)
	mux.HandleFunc("GET "+api.UrlRecords, s.handleGetRecords)
	mux.HandleFunc("POST "+api.UrlRecords, s.handleApplyChanges)
	mux.HandleFunc("POST "+api.UrlAdjustEndpoints, s.handleAdjustEndpoints)

	s.provider = http.Server{
		Addr:              opts.ProviderAddr,
		Handler:           mux,
		ReadTimeout:       opts.ReadTimeout,
		WriteTimeout:      opts.WriteTimeout,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		IdleTimeout:       opts.IdleTimeout,
	}

	healthMux := http.NewServeMux()
	healthMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	healthMux.Handle("GET /metrics", promhttp.Handler())

	s.health = http.Server{
		Addr:              opts.HealthAddr,
		Handler:           healthMux,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
	}

	return s
}

// Run serves until ctx is cancelled, then drains both listeners.
func (s *Server) Run(ctx context.Context) error {
	errs := make(chan error, 2)
	go func() { errs <- listen("provider", &s.provider) }()
	go func() { errs <- listen("health", &s.health) }()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	return errors.Join(s.provider.Shutdown(shutdownCtx), s.health.Shutdown(shutdownCtx))
}

func listen(name string, srv *http.Server) error {
	log.Infof("%s listener on %s", name, srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// handleNegotiate answers the initialization request with the domain filter.
func (s *Server) handleNegotiate(w http.ResponseWriter, req *http.Request) {
	if !checkAccept(w, req) {
		return
	}
	w.Header().Set(api.ContentTypeHeader, api.MediaTypeFormatAndVersion)
	if err := json.NewEncoder(w).Encode(s.opts.Provider.GetDomainFilter()); err != nil {
		log.Errorf("encoding the domain filter: %v", err)
	}
}

func (s *Server) handleGetRecords(w http.ResponseWriter, req *http.Request) {
	if !checkAccept(w, req) {
		return
	}

	records, err := s.opts.Provider.Records(req.Context())
	if err != nil {
		s.fail(w, "reading records", err)
		return
	}

	w.Header().Set(api.ContentTypeHeader, api.MediaTypeFormatAndVersion)
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(records); err != nil {
		log.Errorf("encoding records: %v", err)
	}
}

func (s *Server) handleApplyChanges(w http.ResponseWriter, req *http.Request) {
	if !checkContentType(w, req) {
		return
	}

	var changes plan.Changes
	if !s.decode(w, req, &changes) {
		return
	}

	if err := s.opts.Provider.ApplyChanges(req.Context(), &changes); err != nil {
		s.fail(w, "applying changes", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdjustEndpoints(w http.ResponseWriter, req *http.Request) {
	if !checkContentType(w, req) {
		return
	}

	var endpoints []*endpoint.Endpoint
	if !s.decode(w, req, &endpoints) {
		return
	}

	adjusted, err := s.opts.Provider.AdjustEndpoints(endpoints)
	if err != nil {
		s.fail(w, "adjusting endpoints", err)
		return
	}

	w.Header().Set(api.ContentTypeHeader, api.MediaTypeFormatAndVersion)
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(adjusted); err != nil {
		log.Errorf("encoding adjusted endpoints: %v", err)
	}
}

// decode reads a capped JSON body, answering the client itself on failure.
func (s *Server) decode(w http.ResponseWriter, req *http.Request, into any) bool {
	body := req.Body
	if s.opts.MaxBodySize > 0 {
		body = http.MaxBytesReader(w, req.Body, s.opts.MaxBodySize)
	}
	if err := json.NewDecoder(body).Decode(into); err != nil {
		status := http.StatusBadRequest
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			status = http.StatusRequestEntityTooLarge
		}
		log.Errorf("decoding the request body: %v", err)
		writeError(w, status, "malformed request body")
		return false
	}

	return true
}

// fail maps a provider error to a status code ExternalDNS will act on: 5xx for
// something worth retrying, 4xx for something that will fail again identically.
func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	log.Errorf("%s: %v", what, err)

	status := http.StatusInternalServerError
	if r, ok := s.opts.Provider.(Retryable); ok && !r.Retryable(err) {
		status = http.StatusBadRequest
		recordPermanentFailure(what)
	}
	writeError(w, status, what+" failed")
}

// writeError always writes a body: ExternalDNS drains it to reuse the connection.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set(api.ContentTypeHeader, "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

var permanentFailures = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "external_dns_akamai",
		Name:      "permanent_failures_total",
		Help:      "Provider failures reported to ExternalDNS as non-retryable.",
	},
	[]string{"operation"},
)

func init() {
	prometheus.MustRegister(permanentFailures)

	// Seeded so /metrics is not empty before the first failure. See the same
	// reasoning in internal/akamai/metrics.go.
	for _, op := range []string{"reading records", "applying changes", "adjusting endpoints"} {
		permanentFailures.WithLabelValues(op)
	}
}

func recordPermanentFailure(operation string) {
	permanentFailures.WithLabelValues(operation).Inc()
}
