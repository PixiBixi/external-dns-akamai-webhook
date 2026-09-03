# Contributing

Bug reports, feature requests and pull requests are welcome.

## Reporting

- **Bugs and feature requests**: open a [GitHub issue][issues]. For a bug, include the
  webhook version (`external-dns-akamai-webhook --version`), the ExternalDNS version, the
  flags the webhook was started with, and the relevant log lines at `--log-level=debug`.
- **Vulnerabilities**: do not open a public issue. Follow [SECURITY.md](SECURITY.md), which
  routes reports through GitHub's private security advisory form.

## Getting started

Go 1.27 or later, per `go.mod`. No other tooling is needed to build and test.

```bash
make build     # local binary
make test      # go test -race ./...
make lint      # golangci-lint run
make cover     # tests plus an HTML coverage report
make image     # local container image from the Dockerfile (releases use ko)
make snapshot  # full goreleaser dry run, nothing published
```

`make lint` needs [golangci-lint][golangci] on your `PATH`; the CI pins the version in
`.github/workflows/lint.yml`.

## What a pull request needs

Run `make test` and `make lint` before pushing. Both run in CI anyway, and both must be
green to merge.

**Tests.** Behaviour changes need a test. The Edge DNS client is an interface
(`internal/akamai.EdgeDNS`), so a stub replaces it without any network: see
`internal/akamai/provider_test.go`. Tests that talk to a real Akamai account live in
`integration_test.go` behind a build tag and are not part of `make test`.

**Code standards.** `.golangci.yml` is the reference, `gosec` and `errorlint` included.
Formatting is `goimports`; the `Go format` workflow posts a suggested diff on the PR rather
than failing, so you can apply it from the review UI.

**Comments.** Explain the decision and why it must not be undone, not the mechanics. The
codebase leans on this in a few places where the obvious refactor is a bug: see the comment
above the `UpdateRecord` loop in `internal/akamai/provider.go`.

**Documentation.** Update the README when a flag, a metric or an observable behaviour
changes. The reasoning behind a design belongs in [openwiki/](openwiki/), not in prose
under a README table.

## Commits and pull request titles

The project follows [Conventional Commits][cc]. Pull requests are squashed, and the squash
commit takes the **pull request title**, so it is the title that must be conventional. The
`Conventional Commits` check enforces it.

```text
fix(akamai): escape every quote embedded in TXT rdata
feat(server): add a readiness gate on the first zone listing
ci(fuzz): run the fuzz targets on every push
```

The scope names the area touched. Those already in the history: `akamai`, `server`, `ci`,
`log`, `release`, `deps`, `docker`, `fuzz`, `go-format`, `markdownlint`, `hardening`,
`codeowners`, `security`, `wiki`. Keep one scope per commit rather than bundling unrelated
changes.

`main` requires linear history, so rebase on it rather than merging it into your branch.

## What CI runs

| Workflow | Checks |
| --- | --- |
| `ci.yml` | build, `go test -race`, and a 60s fuzz run per target |
| `lint.yml` | golangci-lint |
| `go-format.yml` | goimports, as review suggestions |
| `codeql.yml` | CodeQL static analysis |
| `govulncheck.yml` | known vulnerabilities in reachable dependency code |
| `dependency-review.yml` | new dependencies, blocking on high severity and copyleft licences |
| `github-actions.yml` | zizmor, against the workflows themselves |
| `markdownlint.yml` | remark-lint on Markdown |
| `validate-pr-title.yml` | Conventional Commits on the PR title |

## Fuzzing

The parsers that see untrusted input have native Go fuzz targets. `go test` replays their
seed corpus; to look for new inputs:

```bash
go test ./internal/server/ -run FuzzNegotiate -fuzz FuzzNegotiate -fuzztime 60s
go test ./internal/akamai/ -run FuzzCleanTargetsTXT -fuzz FuzzCleanTargetsTXT -fuzztime 60s
```

A crasher is written under `testdata/fuzz/`. Commit it with the fix: it becomes a
regression test.

## Licence

Contributions are accepted under the [Apache 2.0 licence](LICENSE). Parts of
`internal/akamai` derive from the ExternalDNS in-tree provider and keep the copyright header
recorded in [NOTICE](NOTICE); leave those headers in place.

[issues]: https://github.com/PixiBixi/external-dns-akamai-webhook/issues
[golangci]: https://golangci-lint.run/welcome/install/
[cc]: https://www.conventionalcommits.org/en/v1.0.0/
