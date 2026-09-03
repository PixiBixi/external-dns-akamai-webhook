# Architecture

Three packages carry the behavior: `internal/server` owns the HTTP contract with
ExternalDNS, `internal/akamai` owns the Edge DNS contract, and `internal/config`
turns flags and environment into the struct both of them are built from.
`main.go` only wires them together.

## The two listeners

`internal/server/server.go` starts two `http.Server`s and returns as soon as either
one fails:

- **provider** (`127.0.0.1:8888` by default) serves the webhook API. It is
  unauthenticated by design, which is why the default binds to localhost. Changing
  that default is a security decision, not a convenience one.
- **health** (`0.0.0.0:8080`) serves `/healthz` and `/metrics`, and is the only
  listener meant to be reachable from the pod network.

Shutdown drains both with a 15 second timeout on `SIGTERM`, the signal Kubernetes
sends, so an in-flight `ApplyChanges` is not cut mid-zone during a rollout.

### Why the handlers are hand-written

ExternalDNS ships `provider/webhook/api.StartHTTPApi`, and this repository
deliberately does not use it. The reasons are in the `New` doc comment and each one
is a production bug:

1. it calls the provider with `context.Background()`, so a cancelled request keeps
   talking to Edge DNS
2. it answers every provider error with 500, so ExternalDNS retries permanent
   failures until the sync deadline
3. it writes an empty body on error, which the webhook guide asks providers not to do

The wire format itself still comes from `api`'s constants (`api.UrlRecords`,
`api.MediaTypeFormatAndVersion`), so it cannot drift from the upstream client.

### Media type negotiation

`internal/server/mediatype.go` guards the routes: `Accept` on the GETs,
`Content-Type` on the POSTs. That split is not symmetry for its own sake, it is what
the ExternalDNS client actually sends: no `Accept` at all on `POST /records`.
Requiring both would reject a conformant client.

A missing header is a 406, a header naming something else is a 415. An unversioned
media type is accepted as version 1, which is what the protocol has meant since
there has only ever been one version.

## Error classification, the contract that keeps ExternalDNS alive

This is the highest-consequence logic in the repository. A misclassification does
not corrupt DNS, it kills the controller.

The chain, end to end:

```
Edge DNS answers an error
  -> akamai.isRetryable(err)                 internal/akamai/client.go
  -> Provider.Retryable(err)                 internal/akamai/provider.go
  -> Server.fail()                           internal/server/server.go
       retryable     -> HTTP 500
       not retryable -> HTTP 400 + external_dns_akamai_permanent_failures_total
  -> ExternalDNS webhook client              provider/webhook/webhook.go
       isRetryableError(status) is [500, 510] only
       500 -> provider.NewSoftError(err)
       400 -> a plain error
  -> ExternalDNS controller                  controller/controller.go, Run()
       SoftError -> log at error level, bump the consecutive soft error gauge,
                    retry at the next interval
       anything else -> return, which main turns into log.Fatal, which restarts
                        the pod
```

So: **5xx keeps ExternalDNS running, 4xx restarts it.** There is no flag on the
ExternalDNS side to tolerate N hard failures; `provider.SoftError` is the only
mechanism.

What `isRetryable` returns true for:

- a transport-level failure with no `dns.Error` at all (connection reset, timeout,
  DNS resolution): worth another attempt
- HTTP 429, and any 5xx from Edge DNS
- any error whose Akamai problem type ends in `resource-impl/forward-origin-error`,
  **whatever the status code**

That last rule exists because Akamai reports a gateway that could not reach the
Edge DNS origin as a 404 carrying
`https://problems.luna.akamaiapis.net/-/resource-impl/forward-origin-error`.
Nothing is actually missing. Classified on the status alone it becomes a 400, and a
30 second Akamai fault turns into a fatal error and a CrashLoopBackOff during which
no DNS is reconciled. Keep the rule this narrow: a wrong `host` in the `.edgerc` or
a contract that does not exist also answers 404, and those must stay loud,
permanent failures rather than an endless soft-error loop.

`isNotFound` excludes the same problem type, and for the same reason from the other
direction: `Provider.delete` treats a 404 as "the record was already gone, which is
the outcome we wanted" and moves on. A gateway fault read that way would report a
delete that never happened, and `internal/akamai/metrics.go` would count it as a
success.

Everything else, including 401 and 403, is permanent. Retrying a bad credential only
burns Edge DNS quota until the sync deadline.

## Zones

`Provider.zones` lists primary zones with `ShowAll`, optionally restricted to the
contracts from `--akamai-contract-id`, then filters the result through the
ExternalDNS domain filter.

The zone cache (`--zones-cache-duration`) defaults to 0, meaning no cache, and that
default is deliberate: a cached listing trades correctness for API calls, because a
zone added during the window stays invisible until it expires. With the cache off, a
sync that has changes to write lists zones twice, once in `Records` and once in
`ApplyChanges`.

`Records` fans the per-zone recordset listings out through an `errgroup` capped at
`zoneListConcurrency = 4`, and writes results into a slice indexed by zone so the
output does not depend on which listing finishes first. The cap is low on purpose:
Edge DNS enforces per-account API quotas.

## Writes

`ApplyChanges` returns before doing anything when the plan is empty, which is the
common path in steady state and must not cost a zone listing.

| Operation | Call | Shape |
| --- | --- | --- |
| create | `CreateRecordSets` | one call per zone, whole batch at once |
| update | `UpdateRecord` | one call per record, never batched |
| delete | `DeleteRecord` | one call per record, 404 tolerated as already absent |

The update loop carries the repository's loudest comment, and it is worth repeating
here. `UpdateRecordSets` maps to `PUT /zones/{zone}/recordsets`, which the Edge DNS
reference defines as "Replaces all record sets that currently exist with the list
provided". Batching updates through it deletes every record the batch omits. Do not
turn that loop into one call.

Every write path resolves its zone through `zoneFor` (`internal/akamai/convert.go`),
which returns a zone only when `ZoneIDName.FindZone` matches **and** the domain filter
matches. The filter is re-checked here rather than trusted from `Records`, because
`ApplyChanges` arrives over a socket: a name the operator excluded but which sits
inside a managed zone would otherwise reach Edge DNS on the strength of `FindZone`
alone. That check is what keeps `--domain-filter` a write boundary and not just a
read filter, so do not remove it from `create`, `update`, `delete` or `changesByZone`.

Neither the update nor the delete path reads the record back first: both address the
record by name and type, and the endpoint carries both the TTL and the rdata, so a
read would buy nothing. `internal/akamai/provider_test.go` asserts the read count is
zero, which is how that stays true.

Dry run (`--dry-run`) is enforced inside each write method, after the log line, and
`Provider.verb` rewrites the log to "dry run, would be creating recordset". Logging
"creating recordset" and then not creating it makes the logs unreadable at the exact
moment somebody is checking whether a run wrote anything.

## Conversion traps

`internal/akamai/convert.go` is small and every function in it encodes an Edge DNS
quirk:

- **TTL**: an endpoint with no TTL gets `defaultTTL = 600`. Values are clamped into
  the range Edge DNS accepts.
- **CNAME and SRV** rdata must not carry the trailing dot, so `cleanTargets` strips
  it.
- **TXT** rdata must be quoted, and the API mangles an embedded quote, so
  `cleanTargets` swaps **every** interior quote for a backtick and `trimTxtRdata`
  swaps them back on the way out. Unconditionally: a target the substitution skips
  gets wrapped into `"a"b"` and escapes its own quoting. `FuzzCleanTargetsTXT` is
  what holds that property. The registry TXT records ExternalDNS writes are exactly
  the values affected, so breaking this breaks ownership.
- Record types ExternalDNS does not manage are skipped on read, not errored.
- `changesByZone` drops endpoints with no matching zone or no domain filter match,
  because there is nowhere to write them and nothing that authorises writing them.

## Metrics and logs

`internal/akamai/metrics.go` wraps the whole `EdgeDNS` interface
(`Instrument(client)`) rather than instrumenting call sites, so no call can be added
without being counted. This provider is dominated by round trips, so the call count
per operation is the number to watch, and it is the only way to see the real volume
of a deployment.

Success series are seeded at registration time. A `CounterVec` exposes nothing until
a label combination is observed, so a fresh process would serve an empty `/metrics`
and `rate()` over it would return no data rather than zero. Error statuses stay
unseeded on purpose: their values are HTTP codes, and inventing series for codes that
never happen is noise.

`internal/logsafe` strips `\n` and `\r` from record names, types and error messages
before they reach a log line. The replacement is unconditional: a guard that returns
the input untouched leaves a flow where the value reaches the log unchanged, so
neither static analysis nor a reader can call it sanitized.

## Where to start for a given change

| Change | Start at | Watch out for |
| --- | --- | --- |
| A new flag | `internal/config/config.go`, then the README tables | Every flag needs its `AKAMAI_WEBHOOK_*` env twin; credentials keep the `AKAMAI_*` names |
| Error handling | `internal/akamai/client.go` | The 4xx/5xx contract above. A test in `TestRetryable` is mandatory |
| A new record type or rdata shape | `internal/akamai/convert.go` | TXT backticks and the trailing dot rules |
| A new Edge DNS call | `EdgeDNS` interface in `client.go` | Add it to `operations` in `metrics.go` and to the stub in `provider_test.go` |
| The HTTP surface | `internal/server/` | Use `api`'s constants, never a literal path or media type |
