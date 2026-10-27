# Contributing to logscry

Thanks for your interest. logscry is a small, focused tool; contributions that keep it
that way are the most welcome.

## Ground rules

- **Keep changes tightly scoped.** One concern per pull request. The design and scope
  live in [`logscry_RDI_v1.md`](logscry_RDI_v1.md); please open an issue before starting
  anything that expands the v1 scope.
- **Standard library first.** Add a dependency only when the RDI calls for it.
- **Match the surrounding code** — its naming, comment density, and idiom.

## Issues and pull requests

Bug reports and feature requests have issue forms that ask for what a report needs: for a bug,
the version, the source, the full command line and the anonymizer setting. Reproduce a bug with
synthetic lines, never real logs. Blank issues stay open for anything else. A security problem,
an anonymizer leak above all, goes through the private form described in
[`SECURITY.md`](SECURITY.md), never an issue. The pull request template is one comment, the rule
for closing keywords: `Closes #N` once, for the issue the PR closes, and every other number bare.

## Before you open a PR

Run the full verify loop from a clean tree; all of it must be green:

```sh
gofmt -l .          # must print nothing
go vet ./...
golangci-lint run
go test ./...
SUPERSET_GATES=$(sed -n "s/^ *SUPERSET_GATES: '\(.*\)'$/\1/p" .github/workflows/ci.yml)
go test -race -skip "$SUPERSET_GATES" ./...
go build ./...
```

`make build`, `make test`, `make lint`, and `make fmt` wrap the common ones.

The two test commands are CI's two test steps, and together they are the full check. `go test
./...` runs every test, the anonymizer's superset gates included, as CI's coverage step does. The
race step skips the gates by name: they are pure. Each gate and its two subtests run in parallel,
every input gets fresh Mappers, and the corpus a gate built is only read, so the race detector can
find nothing in them. A bare `go test -race ./...` runs them anyway and exceeds `go test`'s default
10-minute timeout per package: `internal/anonymize` stops with a timeout panic, not an assertion.
For anyone who wants the full race run anyway, `go test -race -timeout 90m ./...` takes about 45
minutes on 4 cores. A new superset gate must follow their name pattern, `Test…MasksASupersetOf…`,
and be added to `SUPERSET_GATES` in [`.github/workflows/ci.yml`](.github/workflows/ci.yml); CI
fails until it is.

CI also runs govulncheck in a job of its own, on every push and pull request and weekly. It is
not part of the local check. It fails only on a vulnerability that the code reaches and that has
a fix we can take. One with no fixed version only warns. When the job is red over the standard
library, the code under review is not the cause: a new Go patch release has shipped security
fixes. Raise `go.mod`'s toolchain line to it, `go mod edit -toolchain=go1.26.N`, in a change of
its own. CI and the release workflow both fail when the Go they run differs from that line.

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org): `feat:`, `fix:`,
`chore:`, `test:`, `docs:`, `refactor:`.

## Licensing

logscry is [Apache-2.0](LICENSE). By contributing you agree your contribution is licensed
under the same terms. Every source file carries the SPDX header:

```go
// SPDX-License-Identifier: Apache-2.0
```
