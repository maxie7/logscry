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

## Epic M9 — JSON-per-line templating  `[v0.7.0]`
- [x] Template a JSON line on its KEY STRUCTURE instead of running the text masker over it. Found while
      dogfooding logscry against MCP servers: they log nested JSON objects, one per physical line, and
      they carry no `level`/`msg` where RDI §5.1 looks — so normalization declined them, the message fell
      back to the whole raw JSON, and the line went through a masker built for `user 4821 failed`. Every
      string value became `<STR>`, keys included, and the signature degraded to brace soup
      (`{<STR>:{<STR>:<STR>,<STR>:{<STR>:{}},...}`). Visible in the AGGREGATED pane, and wrong twice over:
      unreadable, and broken for dedup in BOTH directions — two genuinely different events with the same
      shape collapsed into one template, while the same event logged with a different key order or nesting
      split into several. Not a crash; the masker followed its rules. But JSON-per-line is common now and
      deserves a real branch.
      Field NAMES are the stable, meaningful part of a structured event — they are what makes two lines
      "the same event" — so keys are kept literally and only values are masked, into the existing
      `<STR>/<NUM>/<BOOL>/<NULL>` vocabulary rather than a second one a reader would have to learn. Keys
      are SORTED before rendering: without that, key order alone still splits a template, which was half
      the original bug. The result reads like `{"level":<STR>,"msg":<STR>,"ts":<NUM>}` — a human can see
      what the event was. Top-level arrays take the branch too (keys inside their elements would otherwise
      be masked away) and render as their DISTINCT element shapes, because array LENGTH is a value-like
      property, not structure, and folding it in would split a template by how many items happened to be
      logged. Top-level scalars stay on the text path: a line that is a bare `42` is ordinary text.
      The one exception, and the whole reason this does not just move the dedup bug: the recognized
      message field is run through the TEXT masker and kept, not blanked. That value is the human payload
      and is where the variable parts live — masking it to `<STR>` would merge `{"msg":"disk full"}` with
      `{"msg":"connection refused"}` and leave novelty unable to ever fire on a JSON-logging app. The
      boundary is deliberately narrow: the text masker touches the message field and NOTHING else, since
      running it over arbitrary values would bring the soup straight back and couple key-structure
      templating to value-masking. Because the fragment is computed by the same function the plain-text
      path uses, it comes out byte-identical to that path's template — asserted by a test — while the full
      hashes still DIFFER: a structured event and an unstructured one are genuinely different events, and
      merging them would erase the wrapper, the sibling fields, and where severity came from. Ambiguity
      (both `msg` and `message`) or a non-string value falls back to a plain placeholder rather than
      guessing.
      Bounded by construction — depth cap 6, a shared 128-value budget, truncation deterministic because
      keys are sorted first — so a pathological producer degrades rather than hangs; and `encoding/json`
      rejects absurd nesting itself, which simply falls back to the text path. Two edges are pinned rather
      than left to chance: an empty object `{}` dedups ALL field-less heartbeat/ack lines from ALL sources
      into one template, which is accepted because they ARE structurally identical and a line with no
      fields carries nothing to tell it apart by; and a message value that is itself JSON-as-a-string is
      NOT re-parsed — it is text-masked like any other string, since recursing into message values is a
      different feature.
      §5.1 keeps working: level/message extraction holds its key sets and priority order and gains
      case-insensitive lookup (resolved deterministically, not by map iteration), so severity still fires
      on a JSON FATAL line; an object with no level field gets no severity signal, which is correct rather
      than a gap. Non-JSON lines reach the text masker byte for byte — its regexes and their
      TS→UUID→IP→HEX→NUM→STR order are untouched — and the anonymizer stays a separate masker, as it was
      always meant to be. Pretty-printed multi-line JSON stays out of scope: the coalescer's joined output
      does not parse, so it takes the text path

## Epic M10 — Release automation
- [x] Attach PREBUILT BINARIES to every GitHub Release, so getting logscry stops requiring a Go toolchain.
      Until now the only two ways in were `go install` and building from source — both of which ask someone
      who merely wants to RUN the tool to install Go first. That is a strange toll for a project whose whole
      pitch is "single static binary", and it is the last step of the loop the Makefile fix started: that fix
      made a release SOURCE ARCHIVE buildable outside a git checkout; this removes the need to build at all.
      Download one file, `chmod +x`, run.
      GoReleaser rather than a hand-rolled build matrix. A matrix workflow is six `GOOS/GOARCH` lines and then
      an ever-growing tail of the things GoReleaser already does correctly — archive naming, per-OS zip vs
      tar.gz, checksums, uploading to the right release, LICENSE/NOTICE inclusion — each of which is a place
      to be subtly wrong once a year at tag time, which is exactly when nobody wants to debug YAML. Config
      lives in `.goreleaser.yaml` (schema `version: 2`, plural `formats:` — the singular `format:` is
      deprecated) and can be exercised locally with `goreleaser check` and `goreleaser release --snapshot
      --clean`, which builds every target into `./dist` and touches nothing on GitHub. Six targets:
      linux/darwin/windows x amd64/arm64. windows/arm64 is kept rather than skipped — the plain cross product
      covers it and cgo is off, so EXCLUDING it would be the thing that costs an extra rule. `CGO_ENABLED=0`
      is what makes the shipped binary genuinely static instead of quietly coupled to the libc of whatever
      runner built it.
      The load-bearing line is the ldflag: `-X main.version={{.Tag}}`. `main.version` is the exact symbol
      `cmd/logscry/version.go` declares and `resolveVersion()` prefers over BuildInfo — the same symbol the
      Makefile stamps, so the two paths agree rather than compete. Get that path wrong and NOTHING fails
      loudly: the build succeeds, the release publishes, and `logscry --version` prints an empty string —
      which is precisely the failure a downloadable binary exists to prevent, since a downloaded binary has
      no git checkout to fall back on and BuildInfo's module version is absent for a non-`go install` build.
      `{{.Tag}}` and not `{{.Version}}`: the tag already carries the `v`, so the binary reports the tag
      verbatim instead of depending on GoReleaser stripping exactly one leading `v` and this config gluing it
      back on — the tag is the source of truth, and the reported version matches `go install` (BuildInfo →
      `v0.8.0`) and `make build` (git describe) in style. The tradeoff, accepted: under `--snapshot` there is
      no new tag, so a dry-run binary reports the PREVIOUS tag rather than a snapshot string. That is a
      property of local dry runs only, and it still proves the thing the dry run is for — that the symbol
      path resolves and `--version` is non-empty.
      A separate `release.yml` triggered by `v*` TAG PUSHES ONLY, never merged into `ci.yml`. This job has
      `contents: write` and publishes artifacts; the reason it is its own file is so that permission can
      never be reachable from a branch push or a pull request. It uses the built-in `GITHUB_TOKEN` — no PAT
      to mint, scope, or rotate — with `fetch-depth: 0`, without which GoReleaser sees no tag and has nothing
      to release. A `go test ./...` before-hook means a release cannot be cut from failing code; deliberately
      not `-race`, since CI already runs the race detector on every push and repeating it would only make tag
      time slower.
      Binaries also carry KEYLESS BUILD PROVENANCE (GitHub/Sigstore via OIDC, `id-token: write`). This is not
      the code signing that stayed out of scope — there is no key to generate, store, or rotate, and no
      secret in the repo. It closes a gap that `checksums.txt` does not: a checksum proves a download was not
      corrupted in transit, but it is published alongside the artifact and says nothing about WHO produced
      the file it describes, so it is no help at all against someone with write access swapping both. The
      attestation binds these exact bytes to this workflow, this commit, this repo, verifiable with
      `gh attestation verify <file> --repo maxie7/logscry`. Handing out unattested executables would sit
      badly with a tool that markets itself on keeping your logs local and ships an anonymizer. The subjects
      are the ARCHIVES plus `checksums.txt` — one attestation per artifact, so verification works on the file
      a user actually downloaded rather than making it a two-file exercise. Checksums stay: integrity and
      provenance are complementary, not substitutes.
      Untouched on purpose: `ci.yml`, the Makefile and its version resolution (local builds keep working
      exactly as before; GoReleaser is a PARALLEL path, not a replacement), and every line of Go — the
      ldflags-override branch `resolveVersion()` already had is the entire integration surface. Left out as
      separate decisions rather than oversights: package managers (Homebrew/apt/scoop), Docker image
      publishing, cosign/GPG signing, and SBOM generation
