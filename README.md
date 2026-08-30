# external-dns-akamai-webhook

An [ExternalDNS](https://github.com/kubernetes-sigs/external-dns) webhook provider for Akamai Edge DNS.

ExternalDNS dropped its in-tree Akamai provider in [#6485](https://github.com/kubernetes-sigs/external-dns/pull/6485). This is that provider, ported to the current Edge DNS SDK and run out of tree, which is the [supported way](https://kubernetes-sigs.github.io/external-dns/latest/docs/tutorials/webhook-provider/) to add a provider now.

## Install

Run it as a sidecar of the ExternalDNS pod. It listens on `127.0.0.1:8888` and is not reachable from outside the pod.

```bash
kubectl create namespace external-dns

kubectl -n external-dns create secret generic akamai-edgedns \
  --from-literal=AKAMAI_SERVICECONSUMERDOMAIN=<host>.luna.akamaiapis.net \
  --from-literal=AKAMAI_CLIENT_TOKEN=<token> \
  --from-literal=AKAMAI_CLIENT_SECRET=<secret> \
  --from-literal=AKAMAI_ACCESS_TOKEN=<token>
```

With the ExternalDNS Helm chart:

```bash
helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/
helm upgrade --install external-dns external-dns/external-dns \
  -n external-dns -f deploy/values.yaml
```

Without Helm: edit the domain filter and image tag in [`deploy/manifests.yaml`](deploy/manifests.yaml), then `kubectl apply -f`.

## Credentials

Either set the four credential flags, or point at an `.edgerc` file. Mixing them is rejected at start-up: a partial set would silently fall back to `.edgerc` and authenticate as somebody else.

| Flag | Environment | Description |
| --- | --- | --- |
| `--akamai-serviceconsumerdomain` | `AKAMAI_SERVICECONSUMERDOMAIN` | Edge DNS API host |
| `--akamai-client-token` | `AKAMAI_CLIENT_TOKEN` | Client token |
| `--akamai-client-secret` | `AKAMAI_CLIENT_SECRET` | Client secret |
| `--akamai-access-token` | `AKAMAI_ACCESS_TOKEN` | Access token |
| `--akamai-edgerc-path` | `AKAMAI_EDGERC_PATH` | Path to an `.edgerc` file, used when the four above are not all set |
| `--akamai-edgerc-section` | `AKAMAI_EDGERC_SECTION` | Section of that file (default `default`) |
| `--akamai-account-key` | `AKAMAI_ACCOUNT_KEY` | Account switch key |

## Flags

| Flag | Environment | Default | Description |
| --- | --- | --- | --- |
| `--domain-filter` | `AKAMAI_WEBHOOK_DOMAIN_FILTER` | | Zone to manage. Repeatable, or comma-separated |
| `--exclude-domains` | `AKAMAI_WEBHOOK_EXCLUDE_DOMAINS` | | Domain to exclude. Repeatable |
| `--akamai-contract-id` | `AKAMAI_WEBHOOK_CONTRACT_IDS` | | Restrict the zone listing to this contract. Repeatable |
| `--zones-cache-duration` | `AKAMAI_WEBHOOK_ZONES_CACHE_DURATION` | `0s` | Reuse the zone listing for this long. `0s` lists on every call |
| `--akamai-request-limit` | `AKAMAI_REQUEST_LIMIT` | SDK default | Cap on concurrent Edge DNS requests |
| `--dry-run` | `AKAMAI_WEBHOOK_DRY_RUN` | `false` | Log the changes without writing them |
| `--provider-addr` | `AKAMAI_WEBHOOK_PROVIDER_ADDR` | `127.0.0.1:8888` | Webhook API listen address |
| `--health-addr` | `AKAMAI_WEBHOOK_HEALTH_ADDR` | `0.0.0.0:8080` | `/healthz` and `/metrics` listen address |
| `--read-timeout` | `AKAMAI_WEBHOOK_READ_TIMEOUT` | `60s` | Maximum time to read a request |
| `--write-timeout` | `AKAMAI_WEBHOOK_WRITE_TIMEOUT` | `60s` | Maximum time to write a response |
| `--read-header-timeout` | `AKAMAI_WEBHOOK_READ_HEADER_TIMEOUT` | `5s` | Maximum time to read request headers |
| `--idle-timeout` | `AKAMAI_WEBHOOK_IDLE_TIMEOUT` | `120s` | Keep-alive idle timeout |
| `--max-body-size` | `AKAMAI_WEBHOOK_MAX_BODY_SIZE` | `33554432` | Request body cap in bytes. `0` disables it |
| `--log-level` | `AKAMAI_WEBHOOK_LOG_LEVEL` | `info` | `panic`, `fatal`, `error`, `warn`, `info`, `debug`, `trace` |
| `--log-format` | `AKAMAI_WEBHOOK_LOG_FORMAT` | `text` | `text` or `json` |
| `--version` | | | Print the version and exit |

A flag always beats its environment variable.

`--zones-cache-duration` must stay below the rate at which zones are added to the account. A zone created after the last refresh is invisible to ExternalDNS until the cache expires.

## Records

| Supported | Not supported |
| --- | --- |
| A, AAAA, CNAME, TXT, MX, NS, SRV, PTR, NAPTR, DNAME | Weighted, latency and geolocation routing |

TTL comes from the endpoint, falling back to 600 seconds.

## Endpoints

| Port | Path | Purpose |
| --- | --- | --- |
| 8888 | `/`, `/records`, `/adjustendpoints` | Webhook API, localhost only |
| 8080 | `/healthz` | Liveness and readiness |
| 8080 | `/metrics` | Prometheus |

## Metrics

| Metric | Type | Labels |
| --- | --- | --- |
| `external_dns_akamai_api_calls_total` | counter | `operation`, `status` |
| `external_dns_akamai_api_call_duration_seconds` | histogram | `operation` |
| `external_dns_akamai_permanent_failures_total` | counter | `operation` |

`status` is the HTTP status when Edge DNS answered, `ok` on success, `error` when the call got no response.

## Development

```bash
make test      # go test -race ./...
make lint      # golangci-lint run
make build     # local binary
make image     # local container image, from the Dockerfile (releases use ko)
make snapshot  # full goreleaser dry run, nothing published
```

Releases go through goreleaser: `svu` derives the version from the Conventional Commits since the last tag, and the run publishes archives for Linux and macOS, a multi-arch image on `ghcr.io` built by [ko](https://ko.build) with no Dockerfile involved, SBOMs, a cosign signature on the manifest list and a build provenance attestation.

## License

Apache 2.0. The Edge DNS provider logic in `internal/akamai/` derives from the in-tree provider in kubernetes-sigs/external-dns and keeps its original copyright header. See [NOTICE](NOTICE).
