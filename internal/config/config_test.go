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

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullCreds is the shortest argument list that passes validation.
func fullCreds() []string {
	return []string{
		"-akamai-serviceconsumerdomain=host.luna.akamaiapis.net",
		"-akamai-client-token=token",
		"-akamai-client-secret=secret",
		"-akamai-access-token=access",
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := Parse(fullCreds())
	require.NoError(t, err)

	// The webhook API is unauthenticated, so it must not default to all interfaces.
	assert.Equal(t, "127.0.0.1:8888", cfg.ProviderAddr)
	assert.Equal(t, "0.0.0.0:8080", cfg.HealthAddr)
	assert.Equal(t, "info", cfg.LogLevel)
	// Caching zones trades correctness for calls, so it is opt-in.
	assert.Equal(t, time.Duration(0), cfg.Provider.ZoneCacheDuration)
	assert.False(t, cfg.Provider.DryRun)
}

func TestRepeatableAndCommaSeparatedFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "repeated", args: []string{"-domain-filter=a.example.com", "-domain-filter=b.example.com"}},
		{name: "comma separated", args: []string{"-domain-filter=a.example.com,b.example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse(append(fullCreds(), tc.args...))
			require.NoError(t, err)

			assert.True(t, cfg.Provider.DomainFilter.Match("www.a.example.com"))
			assert.True(t, cfg.Provider.DomainFilter.Match("www.b.example.com"))
			assert.False(t, cfg.Provider.DomainFilter.Match("www.c.example.com"))
		})
	}
}

func TestExcludeDomains(t *testing.T) {
	cfg, err := Parse(append(fullCreds(),
		"-domain-filter=example.com",
		"-exclude-domains=internal.example.com",
	))
	require.NoError(t, err)

	assert.True(t, cfg.Provider.DomainFilter.Match("www.example.com"))
	assert.False(t, cfg.Provider.DomainFilter.Match("db.internal.example.com"))
}

func TestEnvironmentIsTheFallback(t *testing.T) {
	t.Setenv("AKAMAI_SERVICECONSUMERDOMAIN", "host.luna.akamaiapis.net")
	t.Setenv("AKAMAI_CLIENT_TOKEN", "token")
	t.Setenv("AKAMAI_CLIENT_SECRET", "secret")
	t.Setenv("AKAMAI_ACCESS_TOKEN", "access")
	t.Setenv("AKAMAI_WEBHOOK_DOMAIN_FILTER", "a.example.com,b.example.com")
	t.Setenv("AKAMAI_WEBHOOK_LOG_LEVEL", "debug")

	cfg, err := Parse(nil)
	require.NoError(t, err)

	assert.Equal(t, "token", cfg.Credentials.ClientToken)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.True(t, cfg.Provider.DomainFilter.Match("www.b.example.com"))
}

func TestFlagBeatsEnvironment(t *testing.T) {
	t.Setenv("AKAMAI_WEBHOOK_LOG_LEVEL", "debug")

	cfg, err := Parse(append(fullCreds(), "-log-level=warn"))
	require.NoError(t, err)
	assert.Equal(t, "warn", cfg.LogLevel)
}

// Partial credentials are the dangerous case: without this check the client falls
// back to .edgerc and quietly authenticates as somebody else.
func TestPartialCredentialsAreRejected(t *testing.T) {
	_, err := Parse([]string{
		"-akamai-serviceconsumerdomain=host.luna.akamaiapis.net",
		"-akamai-client-token=token",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set all of them")
}

func TestMissingCredentialsAreRejected(t *testing.T) {
	_, err := Parse(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Edge DNS credentials")
}

func TestEdgercPathAloneIsEnough(t *testing.T) {
	cfg, err := Parse([]string{"-akamai-edgerc-path=/etc/akamai/.edgerc"})
	require.NoError(t, err)

	assert.Equal(t, "/etc/akamai/.edgerc", cfg.Credentials.EdgercPath)
	assert.Equal(t, "default", cfg.Credentials.EdgercSection)
}

func TestBadLogFormatIsRejected(t *testing.T) {
	_, err := Parse(append(fullCreds(), "-log-format=xml"))
	require.Error(t, err)
}

func TestVersionSkipsValidation(t *testing.T) {
	cfg, err := Parse([]string{"-version"})
	require.NoError(t, err, "--version must work without credentials")
	assert.True(t, cfg.ShowVersion)
}

func TestUnknownFlagIsAnError(t *testing.T) {
	_, err := Parse([]string{"-nope"})
	require.Error(t, err)
}
