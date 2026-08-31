# Operations

## Deploying

The binary runs as a sidecar of the ExternalDNS pod, listening on
`127.0.0.1:8888`. Two supported paths, both in the [README](../README.md#install):

- the ExternalDNS Helm chart with [`deploy/values.yaml`](../deploy/values.yaml)
- raw manifests with [`deploy/manifests.yaml`](../deploy/manifests.yaml), after
  editing the domain filter and the image tag

Credentials go in a Secret and reach the process as environment variables. The
process never logs them, and no code path reads `.env` style files.

Configuration is flags first, with an environment twin for every flag: `--foo-bar`
falls back to `AKAMAI_WEBHOOK_FOO_BAR`. The credentials are the exception and keep
the `AKAMAI_*` names the Edge DNS tooling and the `.edgerc` convention already use.
The full tables live in the README ([credentials](../README.md#credentials),
[flags](../README.md#flags)) and are not duplicated here.

Two validations run before the listeners start, both in
`internal/config/config.go`:

- `--log-format` must be `text` or `json`
- the four explicit credential flags are all-or-nothing. Setting one, two or three
  is an error, because a partial set would silently fall back to `.edgerc` and
  authenticate as somebody else.

## Observability

`/healthz` on the health port answers 200 unconditionally: it reports that the
process is serving, not that Edge DNS is reachable. Wiring it to Edge DNS would
restart the pod during an Akamai outage, which fixes nothing.

Metrics exposed by this process, all on `/metrics` of the health port:

| Metric | Type | Labels | Read it for |
| --- | --- | --- | --- |
| `external_dns_akamai_api_calls_total` | counter | `operation`, `status` | Real API volume and the HTTP status mix. `status` is the code when Edge DNS answered, `ok` on success, `error` when the call got no response |
| `external_dns_akamai_api_call_duration_seconds` | histogram | `operation` | Edge DNS latency per operation |
| `external_dns_akamai_permanent_failures_total` | counter | `operation` | Failures reported to ExternalDNS as non-retryable, so the ones about to restart it |

The two signals worth alerting on together:

- `external_dns_akamai_permanent_failures_total` increasing means this process just
  told ExternalDNS to give up, and the controller is about to exit. Treat any
  sustained increase as a configuration or credential problem.
- `external_dns_controller_consecutive_soft_errors`, exposed by ExternalDNS itself,
  means the retry path is working but not succeeding. It is the metric that catches
  a transient failure that stopped being transient.

Both come out of the classification described in
[Error classification](architecture.md#error-classification-the-contract-that-keeps-externaldns-alive).

`--dry-run` logs every intended write and performs none. The log lines read "dry
run, would be creating recordset", so a dry run is never mistaken for a real one in
a scrollback.

## Testing

```bash
make test   # go test -race ./...
make lint   # golangci-lint run, config in .golangci.yml
make cover  # coverage report in the browser
```

CI runs `go mod verify`, `go build ./...` and `go test -race ./...` on
`ubuntu-24.04` (`.github/workflows/ci.yml`), with golangci-lint pinned to an exact
version in a separate workflow. Every action is pinned to a SHA with the version in
a trailing comment, and `zizmor` lints the workflows themselves.

What the test files guard:

| File | Guards |
| --- | --- |
| `internal/akamai/provider_test.go` | Call counts (this provider is round trips, so counts are the assertion), the retry classification table, dry run, the zone cache, delete idempotence |
| `internal/akamai/convert_test.go` | The TTL, TXT and trailing dot rules |
| `internal/server/server_test.go` | Status mapping including retryable to 500 and permanent to 400, body caps, malformed bodies |
| `internal/server/mediatype_test.go` | 406 versus 415, wildcards, version parameters |
| `internal/config/config_test.go` | Flag and environment precedence, the credential validation |
| `internal/logsafe/logsafe_test.go` | Line break stripping |

There is also a read-only live suite behind a build tag, which needs real
credentials and burns real API quota:

```bash
go test -tags=integration -v ./internal/akamai/... -run TestLive
```

It reads `~/.edgerc`, or `AKAMAI_EDGERC_PATH` and `AKAMAI_EDGERC_SECTION`, skips
when no file is there, and never writes: no create, no update, no delete.

Any change to the retry classification or to the HTTP status mapping needs a test
first. Those two are the paths where a wrong answer takes the controller down rather
than returning a wrong result.

## Releases

Releases go through goreleaser, driven by Conventional Commits: `svu` derives the
version from the commits since the last tag, and the run publishes archives for
Linux and macOS, a multi-arch image on `ghcr.io` built by [ko](https://ko.build)
with no Dockerfile involved, SBOMs, a cosign signature on the manifest list and a
build provenance attestation. `make image` builds from the `Dockerfile` instead, for
local use only.

Signatures are keyless, so there is no public key to fetch: the identity is the
workflow that published. The verification commands, including the trap that the
identity stays pinned to the ref the release ran on rather than the tag, are in the
[README](../README.md#verifying-a-release).

Commit scopes follow the packages: `fix(akamai)`, `fix(log)`, `feat`, `docs`,
`build(release)`, `ci`. PR titles are validated by a workflow.
