# AGENTS.md

Guidance for coding agents working in this repository.

## What this is

An ExternalDNS webhook provider for Akamai Edge DNS: the in-tree provider ExternalDNS removed in kubernetes-sigs/external-dns#6485, ported to `AkamaiOPEN-edgegrid-golang/v14` and run out of tree as a sidecar of the ExternalDNS pod. Go 1.27, single binary, stateless: ExternalDNS owns the plan and the TXT registry, this process only answers `GET /`, `GET /records` and `POST /records`.

## Commands

```bash
make build     # local binary
make test      # go test -race ./... (also replays the fuzz seed corpus)
make lint      # golangci-lint run, config .golangci.yml
make cover     # tests plus an HTML coverage report
make image     # local image from the Dockerfile (releases use ko, not this)
make snapshot  # goreleaser dry run, nothing published

go test -race -run TestRetryable ./internal/akamai/   # single test

# Fuzzing, one target per invocation
go test ./internal/server/ -run FuzzNegotiate -fuzz FuzzNegotiate -fuzztime 60s
go test ./internal/akamai/ -run FuzzCleanTargetsTXT -fuzz FuzzCleanTargetsTXT -fuzztime 60s

# Live read-only suite: needs ~/.edgerc (or AKAMAI_EDGERC_PATH), burns real API quota
go test -tags=integration -v ./internal/akamai/... -run TestLive
```

A fuzz crasher lands under `testdata/fuzz/`: commit it with the fix, it becomes a regression test.

## Architecture

`main.go` only wires. `internal/config` turns flags and env into a struct, `internal/server` owns the HTTP contract with ExternalDNS, `internal/akamai` owns the Edge DNS contract, `internal/logsafe` sanitizes attacker-controlled values before logging.

```text
ExternalDNS (same pod) -> 127.0.0.1:8888 webhook API (unauthenticated)
                       -> internal/server handlers -> akamai.Provider
                       -> Instrument(EdgeDNS client) -> Edge DNS API
0.0.0.0:8080 serves /healthz and /metrics only
```

The handlers are hand-written instead of ExternalDNS's `api.StartHTTPApi` (which drops the request context and answers every error with 500), but paths and media types still come from `api`'s constants: never write them as literals.

## Invariants that must not be broken

- **5xx keeps ExternalDNS running, 4xx restarts it.** `isRetryable` (`internal/akamai/client.go`) decides, `Server.fail` (`internal/server/server.go`) maps it. Akamai's `forward-origin-error` 404 is retryable whatever its status. Any change here needs a case in `TestRetryable` first.
- **Never batch record updates.** `UpdateRecordSets` is a PUT that replaces every recordset in the zone. The per-record loop in `Provider.update` is deliberate.
- **`--domain-filter` is a write boundary.** `zoneFor` (`internal/akamai/convert.go`) re-checks the filter on every write path; do not remove it from `create`, `update`, `delete` or `changesByZone`.
- **The webhook API stays on localhost.** Changing the `--provider-addr` default is a security decision.
- **TXT quoting**: `cleanTargets` swaps every interior quote for a backtick, unconditionally. `FuzzCleanTargetsTXT` holds that property, and the ExternalDNS registry records depend on it.
- **Credentials are all-or-nothing**: the four explicit flags or an `.edgerc`, never a mix (`Config.validate`).
- Updates and deletes never read the record back; `provider_test.go` asserts the read count is zero.
- A new Edge DNS call goes into the `EdgeDNS` interface, `operations` in `metrics.go`, and the stub in `provider_test.go`.
- A new flag needs its `AKAMAI_WEBHOOK_*` env twin (credentials keep `AKAMAI_*`) and a row in the README tables.

## Conventions

- Conventional Commits, one scope per commit. PRs are squashed and the **PR title** becomes the commit, so the title must be conventional (enforced by `validate-pr-title.yml`). Existing scopes: `akamai`, `server`, `ci`, `log`, `release`, `deps`, `docker`, `fuzz`, `go-format`, `markdownlint`, `hardening`, `codeowners`, `security`, `wiki`.
- `main` requires linear history: rebase, never merge.
- Every push to `main` releases: `svu` derives the version from the commits, only `feat` and `fix` cut one. Details in `openwiki/operations.md`.
- Formatting is `goimports`. Keep the copyright headers in `internal/akamai` (see `NOTICE`).
- README documents flags, metrics and behavior; design rationale goes in `openwiki/`.
- New workflows copy the existing hardening shape: SHA-pinned actions, `persist-credentials: false`, `permissions: {}` at workflow level, `harden-runner` first.

## OpenWiki

This repository has documentation located in the /openwiki directory.

Start here:
- [OpenWiki quickstart](openwiki/quickstart.md)

OpenWiki includes repository overview, architecture notes, workflows, domain concepts, operations, integrations, testing guidance, and source maps.

When working in this repository, read the OpenWiki quickstart first, then follow its links to the relevant architecture, workflow, domain, operation, and testing notes.
