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
process is serving, not that Edge DNS is reachable.

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
`ubuntu-24.04` (`.github/workflows/ci.yml`), plus a `fuzz` job that runs each target
for 60 seconds and uploads any crasher from `internal/**/testdata/fuzz/` as an
artifact so the input can be replayed locally instead of re-found. golangci-lint is
pinned to an exact version in a separate workflow. Two further gates answer on the pull request as
review suggestions rather than a plain failure: `go-format.yml` runs goimports
over every `.go` file, and `markdownlint.yml` lints the Markdown, this wiki
included.

Every workflow follows one hardening shape and a new one is expected to copy it:
actions pinned to a SHA with the version in a trailing comment,
`persist-credentials: false` on checkout, `permissions: {}` at the workflow level
with the writes declared on the job that needs them, and
`step-security/harden-runner` in audit mode as the first step, which records the
outbound calls the job makes. `zizmor` lints the workflows themselves and
Scorecard scores the result. The `Dockerfile` base images are pinned by digest for
the same reason.

What the test files guard:

| File | Guards |
| --- | --- |
| `internal/akamai/provider_test.go` | Call counts (this provider is round trips, so counts are the assertion), the retry classification table, dry run, the zone cache, delete idempotence |
| `internal/akamai/convert_test.go` | The TTL, TXT and trailing dot rules |
| `internal/server/server_test.go` | Status mapping including retryable to 500 and permanent to 400, body caps, malformed bodies |
| `internal/server/mediatype_test.go` | 406 versus 415, wildcards, version parameters |
| `internal/config/config_test.go` | Flag and environment precedence, the credential validation |
| `internal/logsafe/logsafe_test.go` | Line break stripping |

Two fuzz targets cover the parsers that read untrusted input. Their seed corpus runs
as part of `make test`; only `-fuzz` looks for new inputs, and `-fuzz` takes one
target at a time:

```bash
go test ./internal/server/ -run FuzzNegotiate -fuzz FuzzNegotiate -fuzztime 60s
go test ./internal/akamai/ -run FuzzCleanTargetsTXT -fuzz FuzzCleanTargetsTXT -fuzztime 60s
```

`FuzzNegotiate` asserts two properties of `negotiate`: allowing the wildcard can only
widen what is accepted, never invert, and the version echoed into a 415 body is always
a substring of the header rather than something synthesised.
`FuzzCleanTargetsTXT` asserts the invariant that makes TXT quoting safe: the result is
quoted exactly once and carries no interior quote, whatever the input.

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

The Linux binary is UPX-compressed (`.goreleaser.yml`), macOS is not. UPX decompresses
the whole binary on every exec, a cost a long-lived server pays once at start-up and a
one-shot CLI would pay on every run, so keep the `goos` restriction if the artifact
list ever grows.

### Release notes, and the filter that silently did nothing

The notes on each GitHub release are generated by goreleaser from the commit range
since the previous tag. Nothing is committed: `dist/CHANGELOG.md` is build output
that `--clean` throws away, and the real destination is the release body assembled
in goreleaser's release pipe as header, then changelog, then footer.

Two settings in `.goreleaser.yml` exist for a reason and should not be reverted.

`abbrev: -1` drops the commit SHA from each entry. goreleaser returns an empty
string for that value rather than truncating it, so the line keeps only the human
part. A list of 40-character hashes reads as a `git log` transcript, which is what
release notes are explicitly not meant to be.

The exclude patterns allow an optional scope:

```yaml
exclude:
  - '^docs(\([^)]*\))?:'
```

The earlier form was `'^docs:'`, and it never matched anything. Conventional Commits
make the scope optional, every commit in this repository carries one, and `^docs:`
tested against `docs(wiki):` simply misses. Every type meant to be filtered had been
leaking into the notes since the config was written. The failure is silent: nothing
errors, the notes just quietly contain commits nobody wanted. If you add a type to
that list, keep the `(\([^)]*\))?` group.

`ci` is filtered alongside `docs`, `test` and `chore`, because the list exists to
help a user decide whether to upgrade and a CI change tells them nothing.

Snapshot builds skip the changelog pipe entirely, so `goreleaser release --snapshot`
produces no `CHANGELOG.md` and cannot be used to preview these notes. The first
proof of a change here is the next real tag.

Signatures are keyless, so there is no public key to fetch: the identity is the
workflow that published. The verification commands, including the trap that the
identity stays pinned to the ref the release ran on rather than the tag, are in the
[README](../README.md#verifying-a-release).

Commit scopes follow the packages: `fix(akamai)`, `fix(log)`, `feat`, `docs`,
`build(release)`, `ci`. PR titles are validated by a workflow. The contributor-facing
version of all of this, including what a pull request has to carry, is in
[CONTRIBUTING.md](../CONTRIBUTING.md).

Only the latest release is patched, and vulnerabilities go through GitHub's
private advisory form rather than a public issue
([SECURITY.md](../SECURITY.md)).
