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
	"strconv"
	"time"

	"github.com/akamai/AkamaiOPEN-edgegrid-golang/v13/pkg/dns"
	"github.com/prometheus/client_golang/prometheus"
)

// This provider is dominated by Edge DNS round trips, so the call count per
// operation is the number to watch. It is also the only way to see the real
// volume of a deployment, which no benchmark against a stub can tell you.
var (
	apiCalls = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "external_dns_akamai",
			Name:      "api_calls_total",
			Help:      "Edge DNS API calls, by operation and outcome.",
		},
		[]string{"operation", "status"},
	)

	apiDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "external_dns_akamai",
			Name:      "api_call_duration_seconds",
			Help:      "Edge DNS API call latency, by operation.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"operation"},
	)
)

// operations is every Edge DNS call this provider makes.
var operations = []string{"ListZones", "GetRecordSets", "CreateRecordSets", "UpdateRecord", "DeleteRecord"}

func init() {
	prometheus.MustRegister(apiCalls, apiDuration)

	// A CounterVec exposes nothing until a label combination is observed, so a
	// fresh process serves an empty /metrics and rate() over it returns no data
	// rather than zero. Seed the success series so a dashboard has something to
	// draw from the first scrape. Error statuses stay unseeded: their values are
	// HTTP codes, and inventing series for codes that never happen is noise.
	for _, op := range operations {
		apiCalls.WithLabelValues(op, statusOK)
		apiDuration.WithLabelValues(op)
	}
}

const statusOK = "ok"

// instrumented counts and times every Edge DNS call.
type instrumented struct {
	inner EdgeDNS
}

// Instrument wraps a client so its calls land in the Prometheus registry.
func Instrument(client EdgeDNS) EdgeDNS {
	return &instrumented{inner: client}
}

// observe records one call. The status label is the HTTP status when the API
// answered, "ok" on success, and "error" when the call never got a response.
func observe(operation string, start time.Time, err error) {
	apiDuration.WithLabelValues(operation).Observe(time.Since(start).Seconds())

	status := statusOK
	if err != nil {
		status = "error"
		apiErr := &dns.Error{}
		if errors.As(err, &apiErr) && apiErr.StatusCode != 0 {
			status = strconv.Itoa(apiErr.StatusCode)
		}
	}
	apiCalls.WithLabelValues(operation, status).Inc()
}

func (i *instrumented) ListZones(ctx context.Context, params dns.ListZonesRequest) (*dns.ZoneListResponse, error) {
	start := time.Now()
	resp, err := i.inner.ListZones(ctx, params)
	observe("ListZones", start, err)

	return resp, err
}

func (i *instrumented) GetRecordSets(ctx context.Context, params dns.GetRecordSetsRequest) (*dns.GetRecordSetsResponse, error) {
	start := time.Now()
	resp, err := i.inner.GetRecordSets(ctx, params)
	observe("GetRecordSets", start, err)

	return resp, err
}

func (i *instrumented) CreateRecordSets(ctx context.Context, params dns.CreateRecordSetsRequest) error {
	start := time.Now()
	err := i.inner.CreateRecordSets(ctx, params)
	observe("CreateRecordSets", start, err)

	return err
}

func (i *instrumented) UpdateRecord(ctx context.Context, params dns.UpdateRecordRequest) error {
	start := time.Now()
	err := i.inner.UpdateRecord(ctx, params)
	observe("UpdateRecord", start, err)

	return err
}

func (i *instrumented) DeleteRecord(ctx context.Context, params dns.DeleteRecordRequest) error {
	start := time.Now()
	err := i.inner.DeleteRecord(ctx, params)
	// A delete of an absent record is the outcome we wanted, not a failure.
	if isNotFound(err) {
		observe("DeleteRecord", start, nil)
		return err
	}
	observe("DeleteRecord", start, err)

	return err
}
