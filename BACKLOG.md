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
- [x] TUI shows live templated stream with per-template counts
- [x] Bubble Tea: stream view + aggregated template table (`t`), pause (`p`), scroll, quit
- [x] Snapshot channel (cap 1, non-blocking send): render rate decoupled from event rate
- [x] TTY matrix: TUI on `/dev/tty` when stdin carries logs; auto-plain when piped or headless
- [x] Unit tests for templating

## Epic M3 — Scoring engine (the make-or-break)  `[private]`
- [x] Novelty signal (unseen, or unseen > cooloff) — muted during warmup, or the first
      seconds of a run would flood the user with "novel" routine templates
- [x] Burst signal — purely RELATIVE (k× the template's own baseline); no absolute floor,
      because "busy" is not "changed", and no baseline means no burst
- [x] Severity signal (stderr / `ERROR|FATAL|PANIC|CRITICAL`) — additive; `stderr + ERROR`
      = 0.9 sits deliberately under the 1.0 threshold, so routine chatter cannot escalate.
      Only fatal-class fires on its own
- [x] Escalation decision: score ≥ threshold AND not cached AND rate-limiter allows
- [x] Global rate limiter (token bucket, calls/min, configurable) — the cost cap, tested
      explicitly under a 10k-line flood
- [x] Explanation cache keyed by template hash
- [x] Global ring buffer for LLM context (last M lines)
- [x] Escalation channel: bounded, non-blocking, drops + counts when full (the M4 seam)
- [x] `--explain-dry-run`: surface would-be escalations instead of calling a model
      <!-- pulled into M3: it is how the thresholds get calibrated before an LLM exists -->
- [x] Config: flags + `--config logscry.yaml` + one defaults table (RDI §9)
      <!-- pulled into M3: a scoring engine whose numbers cannot be tuned is not tunable -->
- [x] Unit tests for scoring + escalation decision, including the "quiet system" property
      test: thousands of lines of routine traffic → zero escalations

## Epic M4 — LLM backend  `[private]`
- [x] `llm.Backend` interface (`Explain` / `Name`) + `ExplainRequest`/`ExplainResponse`
- [x] OpenAI-compatible backend (configurable base URL + model + key via env) — covers OpenAI / Groq / Ollama.
      JSON mode on by default, with a one-shot downgrade for servers that reject `response_format`
- [x] Async worker pool consuming the escalation channel; never blocks ingest. Results return on a
      channel to the pipeline goroutine, which owns the template map — so an explanation lands on
      its template with no lock anywhere (RDI §3)
- [x] Prompt assembly: trigger line + ring-buffer context + template; request structured JSON
- [x] Defensive parsing: fences, leading/trailing prose, truncation, no-JSON-at-all — degrade, never discard
- [x] Graceful degradation on LLM error (mark card "unavailable", keep tailing). Retry only transient
      (timeout/429/5xx), never 4xx — a bad key fails identically forever, and retrying it is a storm
- [x] Config: `--llm-workers/-timeout/-max-tokens/-temperature/-json-mode/-retries` + `logscry.yaml`; key env-only
- [x] `--explain-dry-run` builds no backend and no pool at all, so no request *can* be made
- [x] Unit tests incl. the cost guarantee end to end: 1000 escalating events → ≤ rate-limit HTTP calls,
      asserted against the fake provider's own request count

## Epic M5 — TUI polish  `[private]`
- [ ] Bubble Tea layout: live stream (left) + flagged-event cards (right)
- [ ] Card fields: severity, summary, cause, suggestion, count, first/last seen
- [ ] Keybindings: scroll, expand card, pause/resume, quit
- [ ] Shorten the subprocess source name to the base name (`proc:myapp`, not
      `proc:/home/maxie/code/.../myapp`): the full path crowds the stream out at ~100
      columns, and it will look bad in the M6 demo GIF
- [ ] Surface the `--docker-tail` history limit (status bar). Nothing tells the user the
      default `100` is all the backlog they get, so an event further back silently never
      appears — during M3 testing this looked like a broken tool. The README half of this
      rides along with the M6 README task; call out `--docker-tail all` for full history
- [x] `--plain` non-TUI line mode <!-- pulled forward into M2: the TUI needs a non-TUI escape hatch to exist -->


## Epic M6 — Demo, README, go public
- [ ] `examples/demo`: docker-compose + tiny buggy service (fails on a specific route)
- [ ] Record demo GIF: trigger fault → card appears; quiet under normal traffic
- [ ] README: usage, config, architecture (embed the mermaid diagram), demo GIF
- [ ] Clean up commit history if needed (squash / orphan for a clean public debut)
- [ ] **Flip repo to PUBLIC**
- [ ] Enable "Include private contributions" in GitHub settings (keep the green squares)

## Epic M7 — Post-launch (after the repo is public)
Deferred work, not a v1 blocker. Nothing here gates M6.
- [ ] Anonymization flag: mask values before sending to a REMOTE backend, with a reversible map to
      de-anonymize the response (k8sgpt does the same). Default off. Local Ollama needs none, which is
      why this is not a v1 blocker — but it is the thing that makes a cloud provider acceptable
- [ ] Streaming responses (the seam is `chatRequest.stream` + the decode in `call`); v1 is non-streaming
- [ ] Multiline / stack-trace grouping: fold a traceback into ONE template instead of one per frame.
      Found in M4 live testing: a Python FastAPI traceback exploded into ~40 separate templates, each
      a "novel" frame, which is exactly the noise this tool exists to suppress. v1 templates one line
      at a time by design; grouping needs a continuation heuristic (indent / no-timestamp / language
      cues) in `pipeline`. Name it as a known v1 limitation in the README (rides along with the M6
      README task)
- [ ] README: document `--llm-max-tokens` for **reasoning** models. Found in live testing: a thinking
      model spends the whole budget on its chain of thought and returns EMPTY content with
      `finish_reason: length`. The error now names the flag, but the default 300 is too low for such
      models and the README should say so (rides along with the M6 README task)
