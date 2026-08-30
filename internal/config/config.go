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

// Package config turns flags and environment into the runtime configuration.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/external-dns/endpoint"

	"github.com/PixiBixi/external-dns-akamai-webhook/internal/akamai"
)

// Config is everything the process needs to start.
type Config struct {
	Credentials akamai.Credentials
	Provider    akamai.Config

	ProviderAddr      string
	HealthAddr        string
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	MaxBodySize       int64

	LogLevel    string
	LogFormat   string
	ShowVersion bool
}

// Parse reads flags, falling back to the matching environment variable for any
// flag not given. Every flag --foo-bar has an env twin AKAMAI_WEBHOOK_FOO_BAR,
// except the credentials, which keep the AKAMAI_* names the Edge DNS tooling and
// the .edgerc convention already use.
func Parse(args []string) (Config, error) {
	var (
		cfg            Config
		domainFilter   stringList
		excludeDomains stringList
		contractIDs    stringList
	)

	fs := flag.NewFlagSet("external-dns-akamai-webhook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&cfg.Credentials.ServiceConsumerDomain, "akamai-serviceconsumerdomain", env("AKAMAI_SERVICECONSUMERDOMAIN", ""), "Edge DNS API host (with --akamai-client-token, --akamai-client-secret and --akamai-access-token; otherwise .edgerc is used)")
	fs.StringVar(&cfg.Credentials.ClientToken, "akamai-client-token", env("AKAMAI_CLIENT_TOKEN", ""), "Edge DNS API client token")
	fs.StringVar(&cfg.Credentials.ClientSecret, "akamai-client-secret", env("AKAMAI_CLIENT_SECRET", ""), "Edge DNS API client secret")
	fs.StringVar(&cfg.Credentials.AccessToken, "akamai-access-token", env("AKAMAI_ACCESS_TOKEN", ""), "Edge DNS API access token")
	fs.StringVar(&cfg.Credentials.EdgercPath, "akamai-edgerc-path", env("AKAMAI_EDGERC_PATH", ""), "Path to an .edgerc credentials file, used when the four explicit credential flags are not all set")
	fs.StringVar(&cfg.Credentials.EdgercSection, "akamai-edgerc-section", env("AKAMAI_EDGERC_SECTION", "default"), "Section to read from the .edgerc file")
	fs.StringVar(&cfg.Credentials.AccountKey, "akamai-account-key", env("AKAMAI_ACCOUNT_KEY", ""), "Account switch key, for managing another account")
	fs.IntVar(&cfg.Credentials.RequestLimit, "akamai-request-limit", envInt("AKAMAI_REQUEST_LIMIT", 0), "Cap on concurrent Edge DNS requests inside the SDK (0 keeps the SDK default)")

	fs.Var(&domainFilter, "domain-filter", "Zone to manage, repeatable. Anything outside is ignored")
	fs.Var(&excludeDomains, "exclude-domains", "Domain to exclude even when it matches --domain-filter, repeatable")
	fs.Var(&contractIDs, "akamai-contract-id", "Restrict the zone listing to this Akamai contract, repeatable")
	fs.DurationVar(&cfg.Provider.ZoneCacheDuration, "zones-cache-duration", envDuration("AKAMAI_WEBHOOK_ZONES_CACHE_DURATION", 0), "Reuse the zone listing for this long. 0 lists zones on every call. Keep it below the rate at which zones are added, or a new zone stays invisible until it expires")
	fs.BoolVar(&cfg.Provider.DryRun, "dry-run", envBool("AKAMAI_WEBHOOK_DRY_RUN", false), "Log the changes without writing them to Edge DNS")

	fs.StringVar(&cfg.ProviderAddr, "provider-addr", env("AKAMAI_WEBHOOK_PROVIDER_ADDR", "127.0.0.1:8888"), "Listen address for the webhook API. It is unauthenticated, so keep it on localhost")
	fs.StringVar(&cfg.HealthAddr, "health-addr", env("AKAMAI_WEBHOOK_HEALTH_ADDR", "0.0.0.0:8080"), "Listen address for /healthz and /metrics")
	fs.DurationVar(&cfg.ReadTimeout, "read-timeout", envDuration("AKAMAI_WEBHOOK_READ_TIMEOUT", 60*time.Second), "Maximum time to read a request")
	fs.DurationVar(&cfg.WriteTimeout, "write-timeout", envDuration("AKAMAI_WEBHOOK_WRITE_TIMEOUT", 60*time.Second), "Maximum time to write a response")
	fs.DurationVar(&cfg.ReadHeaderTimeout, "read-header-timeout", envDuration("AKAMAI_WEBHOOK_READ_HEADER_TIMEOUT", 5*time.Second), "Maximum time to read request headers")
	fs.DurationVar(&cfg.IdleTimeout, "idle-timeout", envDuration("AKAMAI_WEBHOOK_IDLE_TIMEOUT", 120*time.Second), "How long a keep-alive connection stays open while idle")
	fs.Int64Var(&cfg.MaxBodySize, "max-body-size", int64(envInt("AKAMAI_WEBHOOK_MAX_BODY_SIZE", 32<<20)), "Cap on request body bytes. 0 disables the cap")

	fs.StringVar(&cfg.LogLevel, "log-level", env("AKAMAI_WEBHOOK_LOG_LEVEL", "info"), "One of: panic, fatal, error, warn, info, debug, trace")
	fs.StringVar(&cfg.LogFormat, "log-format", env("AKAMAI_WEBHOOK_LOG_FORMAT", "text"), "One of: text, json")
	fs.BoolVar(&cfg.ShowVersion, "version", false, "Print the version and exit")

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parsing flags: %w", err)
	}
	if cfg.ShowVersion {
		return cfg, nil
	}

	cfg.Provider.DomainFilter = endpoint.NewDomainFilterWithExclusions(
		defaulted(domainFilter, "AKAMAI_WEBHOOK_DOMAIN_FILTER"),
		defaulted(excludeDomains, "AKAMAI_WEBHOOK_EXCLUDE_DOMAINS"),
	)
	cfg.Provider.ContractIDs = defaulted(contractIDs, "AKAMAI_WEBHOOK_CONTRACT_IDS")

	return cfg, cfg.validate()
}

func (c Config) validate() error {
	if c.LogFormat != "text" && c.LogFormat != "json" {
		return fmt.Errorf("--log-format must be text or json, got %q", c.LogFormat)
	}
	// Partial credentials are the failure mode worth catching early: the client
	// would silently fall back to .edgerc and authenticate as somebody else.
	given := 0
	for _, v := range []string{
		c.Credentials.ServiceConsumerDomain,
		c.Credentials.ClientToken,
		c.Credentials.ClientSecret,
		c.Credentials.AccessToken,
	} {
		if v != "" {
			given++
		}
	}
	if given != 0 && given != 4 {
		return fmt.Errorf("%d of the 4 Edge DNS credential flags are set: set all of them, or none and use --akamai-edgerc-path", given)
	}
	if given == 0 && c.Credentials.EdgercPath == "" && os.Getenv("AKAMAI_HOST") == "" {
		return errors.New("no Edge DNS credentials: set the 4 credential flags, --akamai-edgerc-path, or the AKAMAI_* environment")
	}

	return nil
}

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	for part := range strings.SplitSeq(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*s = append(*s, part)
		}
	}

	return nil
}

// defaulted falls back to a comma-separated environment variable when the flag
// was not given.
func defaulted(flagValue stringList, envName string) []string {
	if len(flagValue) > 0 {
		return flagValue
	}
	var out stringList
	_ = out.Set(os.Getenv(envName))

	return out
}

func env(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}

	return fallback
}

func envInt(name string, fallback int) int {
	if v, ok := os.LookupEnv(name); ok {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}

	return fallback
}

func envBool(name string, fallback bool) bool {
	if v, ok := os.LookupEnv(name); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}

	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(name); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}

	return fallback
}
