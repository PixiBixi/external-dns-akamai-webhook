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
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/akamai/AkamaiOPEN-edgegrid-golang/v13/pkg/dns"
	"github.com/akamai/AkamaiOPEN-edgegrid-golang/v13/pkg/edgegrid"
	"github.com/akamai/AkamaiOPEN-edgegrid-golang/v13/pkg/session"
)

// EdgeDNS is the slice of the Edge DNS API this provider uses. It is a subset of
// dns.DNS, so a live client satisfies it as is, and a stub can replace it in tests.
type EdgeDNS interface {
	ListZones(ctx context.Context, params dns.ListZonesRequest) (*dns.ZoneListResponse, error)
	GetRecordSets(ctx context.Context, params dns.GetRecordSetsRequest) (*dns.GetRecordSetsResponse, error)
	CreateRecordSets(ctx context.Context, params dns.CreateRecordSetsRequest) error
	UpdateRecord(ctx context.Context, params dns.UpdateRecordRequest) error
	DeleteRecord(ctx context.Context, params dns.DeleteRecordRequest) error
}

// Credentials holds the Edge DNS authentication inputs. Supplying the four explicit
// fields takes precedence; leaving any of them empty falls back to the .edgerc file
// and the AKAMAI_* environment.
type Credentials struct {
	ServiceConsumerDomain string
	ClientToken           string
	ClientSecret          string
	AccessToken           string
	EdgercPath            string
	EdgercSection         string
	AccountKey            string
	MaxBody               int
	// RequestLimit caps in-flight requests inside the SDK. 0 leaves the SDK default.
	RequestLimit int
}

func (c Credentials) complete() bool {
	return c.ServiceConsumerDomain != "" && c.ClientToken != "" && c.ClientSecret != "" && c.AccessToken != ""
}

// NewClient builds a live Edge DNS client from the given credentials.
func NewClient(creds Credentials, userAgent string) (EdgeDNS, error) {
	cfg, err := buildConfig(creds)
	if err != nil {
		return nil, err
	}

	sess, err := session.New(
		session.WithSigner(cfg),
		session.WithUserAgent(userAgent),
		session.WithRequestLimit(cfg.RequestLimit),
		session.WithRetries(session.NewRetryConfig()),
	)
	if err != nil {
		return nil, fmt.Errorf("building the Edge DNS session: %w", err)
	}

	return dns.Client(sess), nil
}

// buildConfig resolves credentials, preferring the explicit fields and falling back
// to .edgerc plus the AKAMAI_* environment. The two sources are not mixed: a partial
// explicit configuration is treated as no configuration at all.
func buildConfig(creds Credentials) (*edgegrid.Config, error) {
	if !creds.complete() {
		cfg, err := edgegrid.New(
			edgegrid.WithFile(creds.EdgercPath),
			edgegrid.WithSection(creds.EdgercSection),
			edgegrid.WithEnv(true),
		)
		if err != nil {
			return nil, fmt.Errorf("reading Edge DNS credentials from %q section %q: %w", creds.EdgercPath, creds.EdgercSection, err)
		}
		cfg.HeaderToSign = append(cfg.HeaderToSign, headerToSign)
		applyOverrides(cfg, creds)
		return cfg, nil
	}

	cfg := &edgegrid.Config{
		Host:         creds.ServiceConsumerDomain,
		ClientToken:  creds.ClientToken,
		ClientSecret: creds.ClientSecret,
		AccessToken:  creds.AccessToken,
		MaxBody:      edgegrid.MaxBodySize,
		HeaderToSign: []string{headerToSign},
	}
	applyOverrides(cfg, creds)

	return cfg, nil
}

func applyOverrides(cfg *edgegrid.Config, creds Credentials) {
	if creds.MaxBody > 0 {
		cfg.MaxBody = creds.MaxBody
	}
	if creds.AccountKey != "" {
		cfg.AccountKey = creds.AccountKey
	}
	if creds.RequestLimit > 0 {
		cfg.RequestLimit = creds.RequestLimit
	}
}

const headerToSign = "X-External-DNS"

// isNotFound reports whether the Edge DNS API answered 404.
func isNotFound(err error) bool {
	apiErr := &dns.Error{}
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// isRetryable reports whether an error is worth a 5xx to ExternalDNS, which retries
// those and only those. Rate limiting and upstream faults are transient; a rejected
// request or a bad credential is not, and retrying it only burns quota.
func isRetryable(err error) bool {
	apiErr := &dns.Error{}
	if !errors.As(err, &apiErr) {
		// Transport-level failures: connection reset, timeout, DNS. Worth a retry.
		return true
	}

	return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= http.StatusInternalServerError
}

// defaultZoneCacheDuration is deliberately 0: caching zones trades correctness for
// calls, so an operator opts in explicitly.
const defaultZoneCacheDuration = time.Duration(0)
