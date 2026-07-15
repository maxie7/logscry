# Contributing to logscry

Thanks for your interest. logscry is a small, focused tool; contributions that keep it
that way are the most welcome.

## Ground rules

- **Keep changes tightly scoped.** One concern per pull request. The design and scope
  live in [`logscry_RDI_v1.md`](logscry_RDI_v1.md); please open an issue before starting
  anything that expands the v1 scope.
- **Standard library first.** Add a dependency only when the RDI calls for it.
- **Match the surrounding code** — its naming, comment density, and idiom.

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

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org): `feat:`, `fix:`,
`chore:`, `test:`, `docs:`, `refactor:`.

## Licensing

logscry is [Apache-2.0](LICENSE). By contributing you agree your contribution is licensed
under the same terms. Every source file carries the SPDX header:

```go
// SPDX-License-Identifier: Apache-2.0
```
