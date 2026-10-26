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
go test -race ./...
go build ./...
```

`make build`, `make test`, `make lint`, and `make fmt` wrap the common ones.

Both test commands above run every test. CI differs in one place: the anonymizer's superset
gates, pure single-goroutine tests that take minutes under the race detector, run in full in its
coverage step and are skipped by name in its race step. A new superset gate must follow their
name pattern, `Test…MasksASupersetOf…`, and be added to `SUPERSET_GATES` in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml); CI fails until it is.

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org): `feat:`, `fix:`,
`chore:`, `test:`, `docs:`, `refactor:`.

## Licensing

logscry is [Apache-2.0](LICENSE). By contributing you agree your contribution is licensed
under the same terms. Every source file carries the SPDX header:

```go
// SPDX-License-Identifier: Apache-2.0
```
