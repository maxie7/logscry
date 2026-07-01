# logscry — Backlog (Epics → tasks)

Epics map 1:1 to the milestones in `logscry_RDI_v1.md` (§13). Work top to bottom.
Repo stays private through M5; flip to public at M6.

## Epic M0 — Compiling skeleton  `[private]`
- [ ] `go mod init github.com/maxie7/logscry`; add Go `.gitignore`
- [ ] `internal/model`: `LogLine`, `Template`, `Stream` types (§3)
- [ ] Package stubs with interfaces + `// TODO(Mn)` markers: `ingest`, `pipeline`, `score`, `llm`, `tui`, `config`
- [ ] `cmd/logscry/main.go`: stdin → pass-through → stdout; context-based graceful shutdown (SIGINT/SIGTERM)
- [ ] `Makefile`: `build`, `run`, `test`, `lint`, `cross`
- [ ] `.github/workflows/ci.yml`: build + `go vet` + `golangci-lint` + `go test`
- [ ] Apache-2.0 `LICENSE` + `NOTICE`
- [ ] `README.md` skeleton: positioning + usage placeholder + "Demo (coming soon)"
- [ ] **DoD:** `make build` passes; `echo hello | ./bin/logscry` prints the line

## Epic M1 — Ingestion  `[private]`
- [ ] `ingest.Source` interface (`Lines(ctx, out)` / `Name()`)
- [ ] stdin source
- [ ] subprocess source (`exec.Command`, capture stdout+stderr, tag `Stream`)
- [ ] Docker source: `ContainerLogs` (Follow, Timestamps), parse RFC3339Nano
- [ ] Docker: `stdcopy.StdCopy` demux when no TTY; raw when TTY (detect via `ContainerInspect`)
- [ ] Docker: auto-attach/detach via `client.Events` on start/die
- [ ] Selection flags: `--docker-all` / `--docker-label` / `--docker-name`
- [ ] Fan-in all sources into one `chan LogLine`

## Epic M2 — Pipeline & templating  `[private]`
- [ ] Normalize: detect JSON vs text; extract level + message
- [ ] Templating: mask `NUM/HEX/UUID/IP/STR` (ordered, compiled regexes) → signature + hash
- [ ] Template state map: firstSeen / lastSeen / count / recent ring buffer
- [ ] TUI shows live templated stream with per-template counts
- [ ] Unit tests for templating

## Epic M3 — Scoring engine (the make-or-break)  `[private]`
- [ ] Novelty signal (unseen, or unseen > cooloff)
- [ ] Burst signal (sliding-window count vs threshold / baseline)
- [ ] Severity signal (stderr / `ERROR|FATAL|PANIC|CRITICAL`)
- [ ] Escalation decision: score ≥ threshold AND not cached AND rate-limiter allows
- [ ] Global rate limiter (token bucket, calls/min, configurable)
- [ ] Explanation cache keyed by template hash
- [ ] Global ring buffer for LLM context (last M lines)
- [ ] Unit tests for scoring + escalation decision

## Epic M4 — LLM backend  `[private]`
- [ ] `llm.Backend` interface (`Explain` / `Name`) + `ExplainRequest`/`ExplainResponse`
- [ ] OpenAI-compatible backend (configurable base URL + model + key via env) — covers OpenAI / Groq / Ollama
- [ ] Async worker pool consuming the escalation channel; never blocks ingest
- [ ] Prompt assembly: trigger line + ring-buffer context + template; request structured JSON
- [ ] Graceful degradation on LLM error (mark card "unavailable", keep tailing)
- [ ] (stretch) anonymization flag: mask values before remote send, reversible map

## Epic M5 — TUI polish  `[private]`
- [ ] Bubble Tea layout: live stream (left) + flagged-event cards (right)
- [ ] Card fields: severity, summary, cause, suggestion, count, first/last seen
- [ ] Keybindings: scroll, expand card, pause/resume, quit
- [ ] `--plain` non-TUI line mode

## Epic M6 — Demo, README, go public
- [ ] `examples/demo`: docker-compose + tiny buggy service (fails on a specific route)
- [ ] Record demo GIF: trigger fault → card appears; quiet under normal traffic
- [ ] README: usage, config, architecture (embed the mermaid diagram), demo GIF
- [ ] Clean up commit history if needed (squash / orphan for a clean public debut)
- [ ] **Flip repo to PUBLIC**
- [ ] Enable "Include private contributions" in GitHub settings (keep the green squares)
