# logscry — Backlog (Epics → tasks)

Epics map 1:1 to the milestones in `logscry_RDI_v1.md` (§13). Work top to bottom.
Repo stays private through M5; flip to public at M6.

## Epic M0 — Compiling skeleton  `[private]`
- [x] `go mod init github.com/maxie7/logscry`; add Go `.gitignore`
- [x] `internal/model`: `LogLine`, `Template`, `Stream` types (§3)
- [x] Package stubs with interfaces + `// TODO(Mn)` markers: `ingest`, `pipeline`, `score`, `llm`, `tui`, `config`
- [x] `cmd/logscry/main.go`: stdin → pass-through → stdout; context-based graceful shutdown (SIGINT/SIGTERM)
- [x] `Makefile`: `build`, `run`, `test`, `lint`, `cross`
- [x] `.github/workflows/ci.yml`: build + `go vet` + `golangci-lint` + `go test`
- [x] Apache-2.0 `LICENSE` + `NOTICE`
- [x] `README.md` skeleton: positioning + usage placeholder + "Demo (coming soon)"
- [x] **DoD:** `make build` passes; `echo hello | ./bin/logscry` prints the line

## Epic M1 — Ingestion  `[private]`
- [x] `ingest.Source` interface (`Lines(ctx, out)` / `Name()`)
- [x] stdin source
- [x] subprocess source (`exec.Command`, capture stdout+stderr, tag `Stream`)
- [x] Docker source: `ContainerLogs` (Follow, Timestamps), parse RFC3339Nano
- [x] Docker: `stdcopy.StdCopy` demux when no TTY; raw when TTY (detect via `ContainerInspect`)
- [x] Docker: auto-attach/detach via `client.Events` on start/die
- [x] Selection flags: `--docker-all` / `--docker-label` / `--docker-name`
- [x] Fan-in all sources into one `chan LogLine`

## Epic M2 — Pipeline & templating  `[private]`
- [x] Normalize: detect JSON vs text; extract level + message
- [x] Templating: mask `NUM/HEX/UUID/IP/STR` (ordered, compiled regexes) → signature + hash
- [x] Template state map: firstSeen / lastSeen / count / recent ring buffer
- [ ] TUI shows live templated stream with per-template counts <!-- next session; plain-text consumer stands in for now -->
- [x] Unit tests for templating

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
