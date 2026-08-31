# Quickstart

`external-dns-akamai-webhook` is a single Go binary that speaks the
[ExternalDNS webhook provider API](https://kubernetes-sigs.github.io/external-dns/latest/docs/tutorials/webhook-provider/)
on one side and the Akamai Edge DNS API on the other. It runs as a sidecar in the
ExternalDNS pod and turns the plan ExternalDNS computes into Edge DNS recordsets.

It exists because ExternalDNS removed its in-tree Akamai provider
([kubernetes-sigs/external-dns#6485](https://github.com/kubernetes-sigs/external-dns/pull/6485)).
This is that provider, ported to the current Edge DNS SDK
(`github.com/akamai/AkamaiOPEN-edgegrid-golang/v13`) and run out of tree, which is
the supported way to add a provider now. The whole repository is that port plus its
supply chain: one commit (`b64e167`) brought the provider in, and most of the
history since is release, CI and hardening work.

## Mental model

ExternalDNS keeps everything stateful: the watch on Kubernetes resources, the
reconciliation interval, the plan, and the TXT registry that records ownership.
This process is stateless and owns only three answers:

1. which zones am I willing to manage (`GET /`, the negotiation call)
2. what records exist in them (`GET /records`)
3. apply this plan (`POST /records`)

Everything else it does is translation between an ExternalDNS `endpoint.Endpoint`
and an Edge DNS recordset, plus one judgement call that matters more than it looks:
deciding whether a failure is worth retrying. See
[Error classification](architecture.md#error-classification-the-contract-that-keeps-externaldns-alive).

## Layout

| Path | What lives there |
| --- | --- |
| `main.go` | Flag parsing, logger setup, signal handling, wiring. 90 lines, no logic |
| `internal/config/` | Flags and their environment twins, plus start-up validation |
| `internal/server/` | The two HTTP listeners, the webhook API handlers, media type negotiation |
| `internal/akamai/` | The provider, the Edge DNS client, error classification, conversion, metrics |
| `internal/logsafe/` | Strips line breaks from attacker-controlled values that reach a log line |
| `deploy/` | Helm values for the ExternalDNS chart, and raw manifests for the Helm-less path |
| `.github/workflows/` | CI, lint, CodeQL, govulncheck, scorecard, dependency review, release |

## Request flow

```
ExternalDNS controller (same pod)
  |
  |  GET /                 -> handleNegotiate    -> Provider.GetDomainFilter()
  |  GET /records          -> handleGetRecords   -> Provider.Records()
  |  POST /records         -> handleApplyChanges -> Provider.ApplyChanges()
  |  POST /adjustendpoints -> handleAdjustEndpoints
  v
127.0.0.1:8888  (webhook API, unauthenticated, localhost only)
0.0.0.0:8080    (/healthz and /metrics, safe to expose in the pod)
  |
  v
internal/akamai.Provider -> Instrument(client) -> Edge DNS API (EdgeGrid signed)
```

`Records()` lists the zones once, then fans out the recordset listings with a
concurrency limit of 4 (`internal/akamai/provider.go`, `zoneListConcurrency`).
`ApplyChanges()` returns immediately when the plan is empty, so a steady-state sync
costs no zone listing at all.

## Run it

Deployment, credentials and the full flag tables live in the
[README](../README.md). The short version:

```bash
make test      # go test -race ./...
make lint      # golangci-lint run
make build     # local binary
```

Credentials are either the four explicit flags or an `.edgerc` file, never a mix:
a partial set is rejected at start-up because it would silently fall back to
`.edgerc` and authenticate as somebody else (`internal/config/config.go`,
`Config.validate`).

## Things to know before changing anything

- **A permanent error restarts ExternalDNS.** The HTTP status this server returns
  decides whether the controller retries or dies. Read
  [Error classification](architecture.md#error-classification-the-contract-that-keeps-externaldns-alive)
  before touching `isRetryable` or `Server.fail`.
- **Never batch record updates.** `UpdateRecordSets` maps to a PUT that replaces
  every recordset in the zone, so batching deletes everything the batch omits. The
  loop in `Provider.update` is deliberate.
- **The webhook API is unauthenticated.** It stays bound to localhost. Only
  `/healthz` and `/metrics` listen on the pod address.
- **Record names come from the request body**, so they are attacker controlled as
  far as this process is concerned. Anything reaching a log line goes through
  `internal/logsafe`.

## Next

- [Architecture](architecture.md): the request path, the error contract, the zone
  handling, and the conversion traps the Edge DNS API imposes.
- [Operations](operations.md): deploying, what to watch, how to test, how a release
  is built and verified, and a change checklist per area.
