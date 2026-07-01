# logscry — project memory

## What this is
logscry is a single-binary, real-time, AI-assisted log/event triage CLI/TUI in Go.
It tails logs (stdin, subprocess, Docker), scores events for novelty / burst / severity,
and escalates **only** anomalies to an LLM. Guiding principle: silent on noise, speaks on signal.

**Source of truth:** the full spec lives in `logscry_RDI_v1.md`. Read it for architecture,
scope, module layout, and milestones before any non-trivial work. Do not exceed the v1
scope defined there (§2) without asking first.

## Tech & conventions
- Go, latest stable (pinned in `go.mod`). Module path: `github.com/maxie7/logscry`.
- Layout: `cmd/logscry`, `internal/{model,ingest,pipeline,score,llm,tui,config}`.
- Standard library first; add a dependency only when the RDI calls for it.
- Format with `gofmt`; keep `go vet` clean; lint with `golangci-lint`.
- Unit-test pure logic (templating, scoring) with the stdlib `testing` package.

## Workflow rules
- Use **plan mode** for any multi-file change; the approved plan is the spec.
  If reality diverges from the plan, stop and re-plan — do not improvise.
- Work one milestone/task at a time (see `BACKLOG.md`). Keep every change tightly scoped.
- Before finishing a task, run the verify loop: `gofmt`, `go vet`, `golangci-lint run`,
  `go test ./...`, `go build ./...` (the `/verify` skill does this).
- Commit with Conventional Commits: `feat:`, `fix:`, `chore:`, `test:`, `docs:`, `refactor:`.
- Repo is **PRIVATE until milestone M6**. Never commit secrets; API keys via env only.

## Definition of done (v1)
Single static binary; runs offline against a local Ollama; the demo in `examples/demo`
surfaces and explains a real fault while staying quiet under normal traffic.
