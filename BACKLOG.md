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
- [x] Bubble Tea layout: live stream (left) + flagged-event cards (right). Two-pane above
      100 columns; below it the panes stack, because 55/45 of 80 columns is two unusable
      40-column columns and the cards pane wraps prose
- [x] Empty state: the cards pane is ALWAYS present and says "No anomalies · watching N
      templates across M sources". Zero cards is the tool working, so the pane must read
      as confident, not unfinished — and the layout cannot jump when the first card lands
- [x] Card fields: severity badge, summary, cause, suggestion, count, first/last seen,
      source, relative time, escalation reasons + score, context lines from the ring buffer.
      All three states render: explaining… / explained / explanation unavailable (+ reason)
- [x] Keybindings: tab focus (focused pane is bordered), scroll, expand card, pause/resume,
      quit — with a footer that is honest about the keys available in the current focus
- [x] Scroll-lock on both panes: scrolled away → hold position and show "↑ scrolled — N new";
      at the home edge → keep following. The cards pane grows from the top, so its home edge
      is the newest card, and selection is keyed by hash so an arrival cannot move it
- [x] Shorten the subprocess source name to the base name (`proc:myapp`, not
      `proc:/home/user/code/.../myapp`): the full path crowds the stream out at ~100
      columns, and it will look bad in the M6 demo GIF
- [x] Surface the `--docker-tail` history limit (status bar). Nothing tells the user the
      default `100` is all the backlog they get, so an event further back silently never
      appears — during M3 testing this looked like a broken tool. The README half of this
      rides along with the M6 README task; call out `--docker-tail all` for full history
- [x] `--plain` non-TUI line mode <!-- pulled forward into M2: the TUI needs a non-TUI escape hatch to exist -->


## Epic M6 — Demo, README, go public
- [x] `examples/demo`: docker-compose + tiny buggy service (worker hits one FATAL fault
      ~18s in while the api service stays quiet). Two alpine services, one `docker
      compose up`; `RECORDING.md` + `docs/demo.tape` for the GIF; `examples/logscry.yaml`
      annotated config; `CONTRIBUTING.md`
- [x] Record demo GIF: trigger fault → card appears; quiet under normal traffic
      <!-- manual: run `vhs docs/demo.tape` per RECORDING.md; output to docs/demo.gif -->
- [x] README: usage, config, architecture (embed the mermaid diagram), demo GIF
      (placeholder wired to docs/demo.gif). Also documents the v1 known limitations,
      discharging the two M7 "rides along with the M6 README" riders
- [x] **Flip repo to PUBLIC**

## Epic M7 — Post-launch (after the repo is public)
Deferred work, not a v1 blocker. Nothing here gates M6.
- [x] `--version` flag: the binary could not report its own version — `logscry --version` failed
      with "flag provided but not defined: -version". Resolved hybrid: an `-X main.version` ldflags
      stamp (git-describe, injected by `make build`) overrides; else the module version from
      `debug.ReadBuildInfo()` (ignoring the "(devel)" placeholder); else "dev". ldflags alone is not
      enough — `go install .../logscry@latest` never runs the Makefile, so it would report "dev";
      BuildInfo is what makes that install path report the real tag. Checked before config validation,
      so it prints even with a missing or broken config
- [x] Anonymization flag: mask values before sending to a REMOTE backend, with a reversible map to
      de-anonymize the response (k8sgpt does the same). Default off. Local Ollama needs none, which is
      why this is not a v1 blocker — but it is the thing that makes a cloud provider acceptable.
      Reversible, type-tagged (`<IP_1>`/`<HOST_1>`/…), per-request in-memory map; masks at the LLM
      boundary only (pipeline/TUI keep real values), restores all three response fields, and FAILS
      CLOSED — a masker error skips the escalation rather than sending plaintext. Bare hostnames are
      masked only on private/infra suffixes (public TLDs would eat Go module paths in stack traces);
      a startup notice fires when a non-local `--llm-url` is used without masking
- [x] Streaming responses (the seam was `chatRequest.stream` + the decode in `call`); v1 was non-streaming.
      Default OFF: provider support for `stream` alongside `response_format` varies, and a regression in
      the explanation path is worse than a card that feels slower — a 400 drops streaming first, keeping
      json mode. Progressive display is a strict SUBSET of the existing parser (completed JSON fields
      only), so it can never drift from the final authoritative answer, and a half-written field, a fence
      or a preamble can never reach a card. Updates fire on field completion — ≤3 per escalation, no
      timer — and DROP rather than block, because a blocking send would let a busy renderer stall the
      read until the deadline fired and badge a good answer incomplete. A stream that dies is salvaged
      and marked `answer incomplete`; a tear after the model closed its JSON is not a tear at all;
      `finish_reason: length` stays a clean end and keeps its `--llm-max-tokens` diagnostic
- [x] Multiline / stack-trace grouping: fold a traceback into ONE template instead of one per frame.
      Found in M4 live testing: a Python FastAPI traceback exploded into ~40 separate templates, each
      a "novel" frame, which is exactly the noise this tool exists to suppress. v1 templates one line
      at a time by design; grouping needs a continuation heuristic (indent / no-timestamp / language
      cues) in `pipeline`. Name it as a known v1 limitation in the README (rides along with the M6
      README task)
- [x] README: document `--llm-max-tokens` for **reasoning** models. Found in live testing: a thinking
      model spends the whole budget on its chain of thought and returns EMPTY content with
      `finish_reason: length`. The error now names the flag, but the default 300 is too low for such
      models and the README should say so (rides along with the M6 README task)

## Epic M8 — Export & persistence  `[v0.6.0]`
- [x] `--export <path>`: JSONL dump of flagged anomalies. Until this, an anomaly was pixels — a TUI card
      or a `--plain` line — and nothing else could consume it, so logscry could be watched but not run
      inside a pipeline. RDI §2 permits exactly one form of persistence ("an optional JSONL dump of
      flagged events"), which is this and nothing more: no DB, no query layer, no rotation, one
      append-only file. Default off, and off means no file opened and no goroutine started.
      One line per flagged anomaly, written when its explanation reaches a TERMINAL state — the streaming
      landmine, since v0.5.0 delivers an answer as several pending updates first, and a file that took
      each of them would carry the same anomaly three times, each slightly more complete. A record is a
      POINT-IN-TIME snapshot: `count_at_flag` / `last_seen_at_flag` are named for the fact that they are
      the values at the instant the template crossed the threshold, not running totals. An anomaly IS the
      event "this escalated", and the explanation arriving seconds later explains that event, not the
      state of the world when the model finished; welding the two instants into one object would be a
      line that looks like one snapshot and is not. Both renderers capture at the same instant, so the
      file is a property of the run rather than of what happened to be watching it.
      Values are REAL: `--llm-anonymize` masks what goes to a remote model, and this file is local
      exactly like the terminal. Deliberately no raw trigger line — `pattern` is the masked signature,
      which is what lets the whole file go into a ticket or a CI artifact without auditing it first, and
      a record about a template that fired N times has no non-arbitrary "the" raw line anyway.
      `--explain-dry-run` writes too, marked `kind: "would_escalate"` / `state: "not_requested"`: that
      mode exists to calibrate thresholds, and a greppable, score-sortable, diffable file is the artifact
      it was missing. It still builds no backend, so the cost guarantee is untouched.
      Placement: the file is owned by its own goroutine behind a non-blocking send. The pipeline
      goroutine owns the template map and must never wait on a disk — the same rule that kept the LLM
      call off it. Line integrity is built rather than assumed: `write(2)` may return short with the
      bytes it did write already on disk, so each record goes out as one complete buffer through a loop
      over short writes, and a failed write is rolled back with `Truncate` to the last record boundary —
      that one record is lost, the file stays valid JSONL
