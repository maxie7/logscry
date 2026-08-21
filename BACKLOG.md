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

## Epic M11 — journald source
- [x] Follow the SYSTEMD JOURNAL, so logscry can triage a Linux host and not just what runs on it.
      stdin, subprocess and Docker between them cover application logs; none of them covers the class
      of logs a box actually fails on — systemd units, the kernel, the host. This is the first of
      issue #10's three sources (NATS and Kafka are separate, later, and untouched here) and the first
      real test of the claim RDI §10 makes: that the `ingest` seam is clean enough for a new source to
      slot in without a rewrite. It is — a journald entry becomes an ordinary `model.LogLine` at the
      source and flows through the identical pipeline, scorer, templater, anonymizer, TUI and export.
      The `Source` interface did not move; journald is a new IMPLEMENTER, not a reshaped contract.
      `journalctl -f -o json` as a SUBPROCESS, not a cgo binding, and the release story is what decides
      it rather than taste. The alternative is `coreos/go-systemd/sdjournal`, which binds `libsystemd`
      through cgo — and M10 ships six targets cross-compiled at `CGO_ENABLED=0`, which is the line that
      makes the shipped binary genuinely static. Adopting cgo would cost the static linux build, both
      non-linux targets outright (there is no libsystemd to link against on either), and the absence of
      build tags anywhere in this package, all to avoid depending on a binary that is present on every
      host that HAS a journal to read. journald is Linux-only by nature, so the source being unusable
      elsewhere is correct rather than a gap — and because nothing platform-specific enters the Go, the
      whole feature needs no build tags at all. `-o json` and not `json-pretty`: compact output is one
      entry per line, which is exactly the newline-delimited shape `readLines` already handles.
      PRIORITY maps to a level — 0/1 FATAL, 2 CRITICAL, 3 ERROR, 4 WARN, 5/6 INFO, 7 DEBUG — and at 3
      and below to STDERR as well. emerg and alert are the crash class the fatal weight exists to
      interrupt for; crit is weighed as ERROR, serious without being an interrupt. The stream half is
      faithful rather than invented: systemd forwards a unit's stderr at priority 3 and its stdout at 6.
      It is also what keeps journald at PARITY with the other sources — without it an ERROR from a unit
      would score 0.6 where the byte-identical ERROR from a container scores 0.9, making one source
      quietly less alarming than another for no reason the user could see. It cannot over-fire either:
      `SeverityMax` still caps the stream+level sum, so severity alone never escalates. An absent or
      unparseable PRIORITY assigns NOTHING rather than guessing, since the guess would feed the scorer.
      `Normalize` now lets a level the SOURCE supplied outrank one detected from the text — the one edit
      outside `ingest`/`config`, and the piece that made the mapping reach scoring at all, since
      `Normalize` had always overwritten `Level` unconditionally. The precedence is the point: PRIORITY
      is recorded by systemd at the journal protocol level, while a leading `[INFO]` is a regex guess
      about prose, and they DO disagree — a unit logging "INFO: shutting down" at PRIORITY=3 is telling
      us through the channel that cannot be fooled. Message extraction still runs either way, so the
      line goes through the same format detection as any other and still gets its recognized prefix
      stripped; only the level is overridden. Genuinely additive: every source that predates journald
      leaves `Level` empty and reaches the unchanged detection path, which is pinned by its own
      regression test rather than assumed.
      The failure this feature is most likely to hit is SILENT, which is why it gets a probe of its own
      rather than only an error path. A user outside the `systemd-journal` group runs `--journald` and
      journalctl starts, follows, and streams perfectly happily — their own session's journal, and
      nothing from any system service. There is no error to report and no exit code to check, so logscry
      looks broken while doing exactly what it was told, which is the worst possible failure for a tool
      whose entire promise is noticing things. `SystemJournalWarning` OPENS the files journalctl would,
      rather than checking group membership: access is also granted by ACL — Debian and Ubuntu hand it
      to `adm` that way — so a membership check would warn users whose setup works. It stays SILENT when
      it cannot tell (no system journal files at all means volatile-only journald, a container, or
      forwarding elsewhere, none of which is a permission problem), because a warning that fires on a
      working setup is the noise this project is built to avoid. It rides the existing background-notice
      channel, which the TUI already renders in its status line and `--plain` already prints to stderr —
      so a soft warning reached both renderers without one line of TUI or pipeline change.
      Hard failures stay hard and NAMED. `exec.LookPath` runs before `Start`, so "not installed" is
      reported as itself instead of surfacing from inside `Start` as a generic exec failure. A non-zero
      exit carries the first line of journalctl's stderr, which is the only place the real cause ever
      appears ("Failed to open files in /var/log/journal: Permission denied", "No journal files were
      found") and which a bare `exit status 1` throws away — plus the exact `usermod` remedy when it
      reads as permission denied. That stderr is captured in a 4KB-capped buffer and NEVER emitted as
      log content: unlike `SubprocessSource`, which emits a child's stderr as lines because that IS the
      log, journalctl's stderr is diagnostics ABOUT the journal and feeding it to the pipeline would
      have the journal appear to say it.
      `mapLines` came out of `docker.go` first, as its own commit. `readLines` was already the shared
      line-buffering primitive, but the loop around it — read into an interim channel, rewrite each
      line, forward, give up on cancellation — existed only inside `readDockerStream`, and every source
      richer than "one line, one event" needs exactly it and differs only in the transform. Docker
      passes its timestamp parse, journald passes its JSON decode, and NATS/Kafka will pass theirs. The
      extraction was kept honest by refusing to touch the Docker tests: had one needed editing, the
      refactor would have changed behaviour rather than moved it.
      Flags mirror `--docker-*` rather than inventing a mode: `--journald`, `--journald-unit`
      (repeatable and OR-combined like journalctl's own `-u`, and enough on its own to select the
      source the way `--docker-name` is), and `--journald-priority` (a floor, which does NOT imply the
      source — it tunes one, exactly as `--docker-tail` does). Sources still compose: `--journald`
      alongside a subprocess or Docker fans into the one channel through `ingest.Run`. `-p` is omitted
      entirely at 7, since journalctl's least-severe priority already includes every entry and passing
      a no-op flag is a thing to have to explain.
      Left out as decisions rather than oversights: journal CURSORS and persistence, historical replay
      beyond what `-f` gives, a full PRIORITY→level syslog specification, `--journald-tail` (`-f`
      already shows the last entries before following, and a history knob is its own call), and any new
      module dependency — journalctl is a subprocess, not an import. Targets v0.8.0

## Fixes — post-release corrections

Not milestone work and not new capability: recalibrations and bug fixes found by USING the
tool, recorded here so the reasoning survives. Epic numbers stay reserved for features.

- [x] **Novelty is a BOOSTER, not a threshold-crosser** — found by dogfooding, and the same
      class of failure the burst detector was fixed for at M4, now in novelty. Four hours of
      `--journald` on a real working laptop produced `WOULD ESCALATE · 179 out of 2304 lines`,
      and nearly every one of them read `novel template (first seen) · score 1.00`. The cause
      was arithmetic rather than a bug: `Weights.Novelty` was 1.0, `Threshold` was 1.0, and
      `Evaluate` sums the signals — so ANY first-seen template crossed the line ALONE, with
      nothing else about it being remarkable. Novelty was designed for a stream with an
      established template set, where new means suspicious. On a real host — browser
      internals, NetworkManager, cron, docker veth churn, the kernel, VPN handshakes — new
      and harmless IS the steady state, and hundreds of distinct templates appear over a few
      hours, each novel exactly once.
      The fix applies the principle severity already used: ERROR and stderr RAISE a score but
      never cross the threshold by themselves, and only a fatal-class level fires alone.
      Novelty joins them. `Weights.Novelty` 1.0 → 0.45 and nothing else — threshold, burst,
      and every severity weight untouched. No structural change was needed or made, because
      the model already expresses "booster, not trigger" exactly as `weight < threshold`; the
      number was simply wrong. So the knobs survive intact (`--weight-novelty`, `--threshold`,
      the `score:` block), and dialling novelty back to 1.0 restores the old behaviour without
      a code change — pinned by a test, because "configurable" is a claim like any other.
      0.45 and not 0.4 or 0.5, which is where the severity weights either side of it decide
      the answer. 0.4 and 0.45 produce IDENTICAL escalate/quiet outcomes across the whole
      matrix, but 0.4 puts novel+ERROR (0.4 + 0.6) exactly ON the threshold, and a decision
      boundary that depends on float equality is one retune, config override, or change in
      summation order away from silently never firing — with no test able to see it coming.
      0.5 fails at the other end: it puts novel + WARN + stderr at exactly 1.00 and starts
      escalating new WARN-on-stderr templates, a large population on any source that writes
      everything to stderr, which is the same cry-wolf failure relocated rather than fixed.
      0.45 leaves margin on both sides — 0.95 is the quiet ceiling, 1.05 the first combination
      that fires. What still escalates is deliberately unchanged: a new ERROR (1.05), a new
      FATAL (1.45), FATAL/PANIC alone (1.00), and a burst (1.00). What goes quiet is exactly
      the 179: new, info-level, unbursting, unremarkable.
      Warmup COMPOSES with this rather than duplicating it, because the two act at different
      points. Warmup is a gate on the SIGNAL — for the first 30s/100 lines novelty contributes
      0, meaning "do not trust novelty at startup", which is what stops an attach-time backlog
      replay from being a flood. The weight bounds what the signal is WORTH once it is trusted,
      meaning "novelty is never the whole story". Sequentially: 0 during warmup, 0.45 after.
      Neither weakens the other and there is no double-counting.
      Novelty and burst, it turns out, can never fire on the same line — a property that
      predates this change and survives it. First-seen novelty needs `Count == 1` while a
      baseline needs 20 occurrences over a minute; cooloff novelty needs a 15m gap while a
      burst needs 10 occurrences inside 10s. So "new AND bursting" is not a combination the
      weights have to account for: a new template that later bursts escalates on the BURST, at
      the moment it bursts, which is the honest reason. Pinned by a test rather than left as a
      comment.
      The tests are the deliverable as much as the constant. `TestNoveltyAloneNeverEscalates`
      replays the exact scenario that produced the 179 — 300 distinct benign info-level
      first-seen templates — and asserts not just zero escalations but zero SUPPRESSED and zero
      CACHED, so the quiet is the score's doing and not the rate limiter's; it also asserts the
      novelty reason is still REPORTED, since a scorer that had merely stopped detecting
      novelty would pass a weaker version of the test and be a worse tool. `TestSignalMatrix`
      then enumerates all ten combinations with their exact scores, so any future weight change
      fails a test instead of silently shifting what interrupts people — the two calibration
      bugs this package has had were both invisible until someone ran it for hours.
      `TestThresholdIsInclusive` pins RDI §6's `score >= threshold` on the one combination that
      lands exactly on 1.00 (a fatal-class line), because flipping that to a strict comparison
      would silence crashes and nothing else would notice.
      Seven existing tests asserted "a plain, unremarkable novel line escalates" — precisely
      the behaviour being removed — and were updated to use a novel ERROR instead, which is
      what they were each really about. That is the behaviour change, stated rather than
      smuggled. `RDI §6` gained a calibration note so the spec stops disagreeing with the code.
      Out of scope, deliberately: issue #24 (journald PRIORITY). Worth naming because this
      change removes what was masking it — `Normalize` lets a source-supplied level override
      the text heuristic, so a unit logging `ERROR: ...` at PRIORITY=6 is recorded INFO on
      stdout and now scores 0.45 and stays quiet, where before it escalated at 1.00 on novelty
      alone. That escalation was an accident (novelty firing, not severity), but it was doing
      real work, and #24 is now load-bearing. Targets v0.8.1

- [x] **journald's DEFAULT priority is not a level** — #24, and the debt the entry above named
      when it took novelty away from these lines. systemd records a unit's captured stdout at
      `PRIORITY=6` whatever the text of the line says, so at 6 the priority is not severity at
      all: a service printing `ERROR: connection refused` and one printing `starting up` arrive
      byte-identically labelled. The source mapped 6 to INFO anyway, and `Normalize` gives a
      source-supplied level precedence over its own text detection, so the heuristic's ERROR was
      thrown away for the ONE case where the heuristic was the only thing that knew anything.
      The result: 0.45 on novelty alone, and silence. Severity scoring was blind to every
      service that merely PRINTS leveled text to stdout, which is most of the enterprise ones.

      The fix is one branch in the source and nothing else: `PRIORITY` is authoritative only
      where it is NOT the default, so at 6 the source sets no level and the existing
      `given != ""` test in `Normalize` falls through to detection on its own. Every other
      priority had to be set deliberately through the journal protocol and still wins. The
      general precedence rule is untouched — stdin, subprocess and Docker behave exactly as
      before, which `TestNormalizeDetectsLevelWhenTheSourceSuppliesNone` and
      `TestNormalizeSourceLevelWinsOverTheText` both still prove. No weight and no threshold
      moved: a first-seen text ERROR from a stdout service goes 0.45 → 1.05 and escalates, the
      same line from a known template sits at 0.60 and stays quiet. That is the whole point.

      `TestJournaldDefaultPriorityDefersToTheText` is the regression guard, deliberately in an
      external `ingest_test` package so it runs `decode` AND `Normalize` back to back — either
      half alone would have missed a bug that was a disagreement BETWEEN them. It covers the
      JSON payload path as well as the text one, since JSON-on-stdout is the larger real-world
      class. `TestJournaldNonDefaultPriorityStillWinsOverTheText` pins the other half.

      Out of scope, deliberately: the STREAM mapping (`PRIORITY <= 3` → stderr) is unchanged,
      because systemd does not distinguish captured stdout from captured stderr by priority and
      there is nothing there to recover. Priorities 5 (notice) and 7 (debug) stay authoritative
      and are pinned as tests rather than special-cased — both are non-default, so both were
      somebody's decision, and second-guessing those is exactly what this change chose not to
      do. And the text detector is ANCHORED to the start of a line, which this fix does not
      touch: a JVM/Spring console line carries its level mid-line, after the service's own
      timestamp, and is still not detected by any source. Its own issue, not smuggled in here.
      Targets v0.8.2

- [x] **A cluster is not a flood** — #32, and the third dogfooding recalibration. Six hours of
      `--journald --explain-dry-run` on the same working laptop: 5,631 lines, 1,831 templates,
      32 escalations, SIXTEEN of them burst-driven and not one an incident. GNOME compositor
      redraws (`Can't update stage views actor`, `Negative content width`), a CI runner's
      container churn across `init.scope`/`containerd`/`docker`, and a VPN client reacting to
      the veth pairs that churn creates. Every one was a cluster of ten or sixteen lines
      reported at 20×–340× baseline.

      The cause is the DENOMINATOR, not the weight. `baseline()` is a lifetime average,
      `Count / (now - FirstSeen)`, so a template appearing once every few minutes sits near
      0.005/s and ANY clustering of it produces an arbitrarily large multiple. Event-driven
      systems log in clusters by construction — a CI job starts ten containers inside one
      second, a compositor redraws and emits sixteen warnings — so the ratio path converts
      every regular-but-rare template into a trigger. Ten lines in ten seconds is not a flood.

      Demoting `Weights.Burst` the way #27 demoted novelty was considered and REJECTED, and the
      arithmetic is why: at 0.45 the recorded 1.20s become 0.65 and the 1.00s become 0.45, so
      the evidence clears — but the only surviving path is burst+ERROR at 1.05, which
      novel+ERROR already covers without burst. An INFO-level retry storm, the exact incident
      burst exists for, would score 0.45 and stay silent forever. A genuine flood IS
      self-sufficient evidence; what was broken is the definition of one.

      So the fix is `BurstMinCount` 10 → 25 and nothing else — one literal, no new knob, no
      function body touched. It is a GATE and not the absolute trigger deleted at M4: an OR
      trigger fires on "this system is busy", an AND gate only ever refuses. Read through the
      arithmetic it binds exactly when `baseline < BurstMinCount/(BurstMultiplier*BurstWindow)`,
      which at the defaults is 0.5/s — so it is the same statement as "do not trust a ratio
      measured against a baseline below half a line a second", and that is precisely why there
      is no separate baseline-floor knob: it would be this constraint expressed twice, in a
      less legible unit, with a second thing to keep in sync when the multiplier moves.
      Templates averaging 0.5/s or more are unaffected; the old value bound only below 0.2/s.

      25 and not a rounder neighbour: the admissible band is [17, 32] — below it the recorded
      noise survives, above it a spike the package already calls genuine dies. 25 sits inside
      with margin either side. Going higher buys nothing real: no constant can bound an
      unbounded cluster (a 64-way CI build lands 64 lines and clears any value), so paying a
      live detection for that defence is paying for nothing. That failure belongs to the
      baseline, not to this gate.

      The band is ENFORCED, not merely documented, which is the part worth keeping:
      `TestRecordedNoiseDoesNotBurst` fails at `BurstMinCount` 16 or lower and
      `TestGenuineSpikeBursts` fails at 33 or higher, verified by sweeping the constant across
      10/16/17/18/25/32/33 and running both. Reverting to 10 breaks a test; raising to 35 — the
      value this change first proposed — breaks a different one. Neither edge can be crossed
      silently again.

      But the two edges are NOT equivalent evidence, and saying otherwise would be the more
      comfortable lie. The lower edge is MEASURED: a real GNOME compositor clump of 16 from the
      six-hour run. The upper edge is the peak of a SYNTHETIC fixture — 32 is whatever
      `TestGenuineSpikeBursts` happened to be written with, asserted by its author and never
      validated against a real incident. No genuine flood has yet been observed on a real
      system, so "the smallest spike worth firing on" remains an assumption wearing a test's
      clothing. The band is one measurement and one guess. That asymmetry is another argument
      for #35: only replay can put a real number on the upper edge.

      `TestRecordedNoiseDoesNotBurst` replays all sixteen, each row named for the source it
      actually came from. Its file comment is deliberate and says the table is ONE ASSERTION
      REPEATED ELEVEN TIMES: the ratio gate passes in every recorded row, so only the count is
      load-bearing, and the row count must not be read as coverage. The real burden is on
      `TestFloodFloorIsExactlyPinned`, which is two-sided on purpose — a volume gate invites
      being set beyond what any real machine reaches, and THAT failure produces silence, which
      on a healthy host is indistinguishable from success. It derives both edges from the
      config handed to the scorer, never from a literal, so the pin follows the constant.
      `TestRetryStormEscalates` gives the number a shape: an INFO-level line carrying no
      severity weight at all, which is the case only burst can catch. No existing test was
      modified — that is what choosing inside the band bought.

      Out of scope, deliberately: this is a WORKAROUND and is documented as one in `RDI §6`.
      The root cause is that `baseline()` never forgets — a lifetime average has no decay, so a
      formerly rare template stays burst-prone forever and a formerly chatty one can never
      burst again. That is #37; any replacement (EWMA, windowed baseline) needs #35 replay to
      be evaluated honestly against real rate histories rather than synthetic fixtures. Also
      noted and not acted on: after this change `BurstMultiplier` is the ONLY control for
      templates at or above 0.5/s, and this run carries no evidence about that regime.
      Targets v0.8.3

- [ ] **`baseline()` never forgets** — #37, the root cause the entry above routes around: a
      lifetime average with no decay, so rate history is permanent in both directions — a
      formerly rare template stays burst-prone forever, a formerly chatty one can never burst
      again. Blocked on #35 (replay): neither direction can be evaluated against synthetic
      fixtures, since the failure is about real rate histories. Fixing it is also what would
      let the upper edge of #32's admissible band be measured rather than assumed.

- [x] **A card is a TEMPLATE, and its numbers are live** — #34, found by dogfooding, and a
      display bug rather than a scoring one: no weight, threshold or gate moved, and
      `git diff --stat internal/score/ internal/export/` is empty by design.
      A 4.5-hour `--journald --explain-dry-run` run put the anomaly pane and the aggregated
      pane in one screenshot and they disagreed. Three cards read `x2 · 1h ago`; the
      aggregated rows for the same templates read count 9, last seen 22 minutes earlier. An
      earlier six-hour run had a card at `x371` against a live count of 505. The cause is
      that a card was rendered from the `pipeline.Event` retained at the instant the
      template escalated and never refreshed, while the pane beside it was built from live
      template state — the same map, the same snapshot, two different answers.
      The fix is small because the mechanism already existed. `collector.escalations` was
      already re-stamping each retained escalation from `p.templates[hash]` on every
      snapshot — that is what makes a pinned card go from "explaining…" to an answer in
      place — and it had `tmpl.Count` and `tmpl.LastSeen` in hand and copied neither. No new
      channel, no new message type, no TUI plumbing.
      **Identity had to be settled first, and it is the real content of this change.** The
      pane drew one card per ESCALATION, not per template: that run's six JSONL records over
      three hashes rendered six cards. Making six cards live would have shown two rows per
      template both claiming count 9 and the same last-seen — worse than the frozen version,
      and the same failure as #28 one level down. A card is now a template, keyed by hash,
      and a re-escalation reuses the card it already has, moving it to the top of the pane
      because coming back is news.
      The decisive argument was not RDI §6, though §6 already said this and the explanation
      cache key already implied it. It was that the renderer was ALREADY written for one
      card per template and duplicate hashes were already breaking it. Three symptoms, all
      live on exactly this data, all closed here and none of them independently actionable
      afterwards, so they get no separate issue: (1) `renderCards` marks every card whose
      hash equals `selKey`, so both twins drew the selection marker and the pane showed two
      selections; (2) `expanded` is keyed by hash, so opening one twin opened the other;
      (3) `indexOfCard` returns the first match, so `selectBy` could not reach the older
      twin — the selection snapped back to the top and scroll-lock silently engaged, making
      the down-arrow look dead. The guard is `TestSnapshotNeverRepeatsAHash`, a named
      invariant at the COLLECTOR under a mixed workload, not in the TUI: after this change a
      duplicate hash is unreachable from the renderer, so a TUI test would have to build a
      snapshot the collector can no longer produce — a test of an impossible state.
      **What the second escalation's information costs, decided knowingly.** Three scalars
      on `model.Template` — `FlagCount`, `FirstFlagged`, `LastFlagged` — stamped in
      `Pipeline.flagged`, which every mode passes through. They live on the template and not
      on the retained event so a card that ages past `escalationsKept` and comes back does
      not claim to be firing for the first time. They describe the LATEST flag, so the
      earlier flags' reasons and scores are **discarded from memory**: once a template
      re-escalates its `why:` line shows the newest reason only, and the distinction between
      "first seen" and "returned after 1h41m of silence" is gone from the card. Recorded here
      so it is not rediscovered later as a bug. Keeping the freshest reason is the right
      call, and the full per-flag history is already in the JSONL export, which is what the
      export is for. A bounded `[]Escalation` of every flag was rejected as unbounded memory
      per template growing with run length — a template re-escalating on cooloff accumulates
      ~100 entries a day, none of which anyone reads — where everything else in v1 (ring
      caps, bounded channels) is bounded on purpose.
      **The export does not change and must not.** `count_at_flag` and `last_seen_at_flag`
      are correctly named and correctly frozen: a record describes a decision at a moment, so
      a second flag writes a SECOND record rather than editing the first. The card describes
      a template now. `TestExportStaysFrozenWhileTheCardMoves` asserts both halves side by
      side, which is the only reason they are one test — each is invisible from the other,
      and #34 was exactly the card half having quietly adopted the export's semantics.
      **A merged card must never lose an answer it already has**, which the merge would
      otherwise have regressed against RDI §7. Before it, a re-escalation left the old card
      showing its finished answer and put the pending state on a NEW card, so a failed second
      explain cost nothing; after it, a single card would have dropped a good answer for
      "explanation unavailable". So `Pipeline.escalated` carries the previous answer forward
      into the new pending explanation, and `attach` keeps it when a retry fails, recording
      only why the retry failed. `Explanation.At` is preserved across both, which is the
      whole of how the card knows the answer predates the newest flag — no new state and no
      flag to keep in sync. Collapsed, a failed retry says `retry failed` rather than
      `explanation unavailable`, which would be a card denying what it is showing; expanded,
      an `answer: from the flag at 10:00:00` line scopes the fields under it. The FILE still
      records the bare failure: a record describes one flag, and that flag produced no
      answer.
      **Display rules, both suppressed when they would carry no information.** The `⚑N` chip
      on the collapsed card and the `(N flags)` bracket in the pane title appear only when
      the flag count exceeds the card count — a badge present on every card is a badge nobody
      sees when it finally means something, and the bracket APPEARING is itself the signal
      that a template came back. Only the plural `flags` form exists, because the suppression
      makes `(1 flag)` unreachable. The title counts CARDS; the status bar's `esc` is
      untouched and keeps counting flags, which is what matches the JSONL export line for
      line. Two numbers measuring different things is fine as long as each is labelled.
      Also in the diff and stated rather than smuggled: `wrapField` pads its label column to
      8 instead of 7. The new `answer:` label is 7 characters and would have rendered with no
      gap before its value — which the existing `failed:` label had been doing all along, so
      this fixes that too and shifts every detail value right by one column.
      `escalationsKept` is unchanged at 20 but now bounds distinct flagged TEMPLATES rather
      than flags, so the pane retains twenty separate faults instead of twenty rows that
      might be seven faults over again. Its comment says so.
      Out of scope, deliberately: #28 (card grouping), which this unblocks without
      implementing — grouping needs cards addressable by a stable key and updatable from the
      stream, and per-template cards are that where per-escalation cards were not. Also
      untouched: the aggregated pane, which could mark flagged templates and does not.
      Targets v0.8.4

- [x] **The IPv6 mask ate C++ scope resolution operators** — found by dogfooding, and the
      most direct hit on the product yet: the card is the output, and the card was naming
      functions that do not exist. One hour of `--journald` on a work laptop escalated
      fifteen patterns, ten of which carried an `<IP>`, and NONE of those ten was an
      address. All were `::`. Two failure modes, the second much worse than the first:
      `CDBusNMHelper::GetDNSConfig` masked to `CDBusNMHelper<IP>GetDNSConfig`, and — because
      the match ate the hex characters adjacent to it — `CNSSCertStore::CNSSCertStore` masked
      to `CNSSCertStor<IP>NSSCertStore` and `CCertificateInfoTlv::Assign` to
      `CCertificateInfoTlv<IP>ssign`. The regex was not wrong by the letter: `::` and `::A`
      really are valid IPv6. It was wrong about the world it runs in.
      A later grep over the same capture turned up a shape the issue never recorded, and a
      worse one: the run after `::` is eaten WHOLE, up to four characters, so a Qt paint call
      lost two (`QPainter::begin` → `QPainter<IP>gin`) and a certificate store lost five
      across both sides (`…CertStore::addCert` → `…CertStor<IP>ert`). Same mechanism, more
      damage than the single-letter cases the fix was written against; both are test rows.
      The fix is two clauses on the IPv6 pattern, and they do different jobs. A boundary
      before the match (start of text, or a character outside `[0-9A-Za-z:.]`, consumed and
      re-emitted by `maskIP` because RE2 has no lookbehind) forbids a match that BEGINS
      inside a token — which ends the character-eating outright and rejects every
      `Class::Method`, since a qualified name always has identifier characters running up to
      its `::`. A minimum of TWO hex groups then covers what the boundary cannot: a `::` that
      legitimately sits at a boundary, as in `operator :: used`, `call ::AddRef`, or a message
      that BEGINS `Cafe::draw`. Nothing else moved — not the masker order, not `mustLongest`,
      not `ipv4Pat`, not the other five patterns.
      **The two-group rule is not corpus-fitting**, and the distinction matters because the
      capture was convenient. Every real address in the 612 records is fully expanded and
      every `::` in it is C++, so the corpus applies no pressure at the compressed end — it
      cannot tell "≥2 groups" from "require all eight". The rule comes from the collision
      structure instead: a qualified name is `identifier :: identifier`, exactly one `::` and
      no single `:` anywhere, while every IPv6 form with two or more groups needs either a
      single-colon separator or a group on each side of the `::` (and the boundary disposes of
      the second). Verified against compressed forms absent from the capture: `2001:db8::1`,
      `fe80::1`, `fe80::1%eth0`, `2001:db8::/32`, `::ffff:0:0`, `[2001:db8::1]:8080` all still
      mask. Where it does NOT generalise is exactly where the corpus is convenient — the
      one-group forms — which is why those are recorded as limitations rather than hidden.
      **Three deliberate losses, all fragmentation, none corruption**, in README "Known
      limitations" and pinned as `KNOWN_GAP` rows so widening the mask back is a visible diff:
      `fe80::/64` → `fe<NUM>::/<NUM>` (one group; indistinguishable from `Cafe::draw`, and
      absent from the capture); `::1` → `::<NUM>` (a C++ identifier cannot begin with a digit,
      so `::` plus an all-decimal group WOULD be a sound discriminator — deliberately not
      taken, because it widens the match for a shape no capture has measured; reopen it with
      evidence); and `peer:2001:db8::1` → `peer:<NUM>:db<NUM>::<NUM>`, an address glued onto
      the token before it, which would be the costliest of the three because the NUM fallback
      keeps the hex groups and those lines fragment per-address instead of collapsing — so it
      was measured before being accepted, and all 38 address-shaped tokens in the capture
      (a host running Docker, a VPN client and a desktop session) are preceded by a space,
      bracket or comma, none glued. Recorded as a loss no run has yet shown us paying. One
      residual false positive survives and cannot be fixed textually: hex-only segments of
      four characters or fewer on both sides (`dec::add`) ARE an address shape. Pinned as a
      test row rather than papered over.
      **Template hashes reshuffle once.** `hashTemplate` is a pure function of the pattern, so
      every template that carried a corrupted `<IP>` gets a new hash. No semantic content — the
      hash is an opaque dedup key — but a RUNNING instance loses continuity with its own
      history: the explanation cache misses on the new hashes and those templates read as novel
      exactly once (bounded by the same rate limiter as everything else), and JSONL exported
      before the change will not join by hash to exports after it.
      **#36's fragmentation figure was measured on a corpus this changes.** Ten of fifteen
      escalated patterns in that run carried a corrupted `<IP>`; their patterns and their hashes
      both move. #36 needs its numbers re-measured after this lands, or its conclusions carry a
      known error term.
      Out of scope and left alone: #31a (ANSI stripping — the capture contains zero escape
      sequences, so there is nothing to validate it against), #36's `<NUM>`-inside-tokens
      problem, and `internal/anonymize`, which carries a near-verbatim copy of the same IPv6
      alternation. That copy is now the durable finding: this change makes the two DIVERGE.
      Filed separately as #42.

- [x] **A partial match is a leak, not a near miss** — #41, the disclosure defect in the one
      package whose job is preventing disclosure. `--llm-anonymize` sent part of every
      COMPRESSED IPv6 address to the configured model endpoint in the clear:
      `peer 2001:db8::dead:beef down` masked to `peer <IP_1>dead:beef down`, and
      `peer fe80::1` to `peer <IP_1>1`. `Mask` returned a nil error, `residue()` returned
      `""`, no escalation was skipped, and the card looked normal.
      The cause is one call. `ipv6Pattern` was compiled with plain `regexp.MustCompile`, so
      Go's default leftmost-FIRST matching took the first alternative that succeeded at the
      earliest position — `(?:[0-9a-f]{1,4}:){1,7}:`, which stops at the `::` — over the
      alternative that would have taken the whole address. `internal/pipeline` compiles the
      same alternation through `mustLongest` for exactly this reason and its doc comment
      names the failure; this package never had the call. Not a regression: `git log -S
      "Longest()" -- internal/anonymize/` is empty, and `git show
      v0.4.0:internal/anonymize/anonymize.go` carries the identical nine alternatives. It
      dates from the introduction of `--llm-anonymize` in **v0.4.0** and shipped in **eleven**
      tagged releases (v0.4.0 → v0.8.5, `git tag --contains 2da31be`), all with prebuilt
      binaries. Severity is bounded — the recipient is an endpoint the operator configured,
      nothing left the machine on a local Ollama, and no credential material is involved —
      but the README's promise was unqualified and was not being met.
      **The fail-closed re-scan could not have caught this, and not because it is written
      badly.** The leftover of a partial match is BY CONSTRUCTION a shape the detector no
      longer matches: `dead:beef` has no `::` and fewer than eight groups, so the very
      detector that produced it reads it as clean. `residue()` can catch a detector that
      missed a value ENTIRELY; it cannot catch one that matched a value INCOMPLETELY. The old
      comment asserted the opposite ("by construction it can only catch what the detectors
      already match"), so the comment was corrected rather than merely fixed underneath.
      **`TestRoundTrip` was green the whole time, on top of the live leak.** Its inputs
      already included `peer fe80::1 unreachable`. `Mask` produced `peer <IP_1>1` and
      `Restore` mapped `<IP_1>` back to `fe80::`, reassembling the original byte-for-byte —
      the round trip succeeded BECAUSE of the leftover. That is the same shape of blindness as
      `residue()`: the check cannot see the failure because the failure preserves the property
      the check tests. The test is kept, with a comment naming what it cannot see.
      **The fix is applied to all twelve detectors, not to the one known to need it.** A
      single `mustLongest` compile site replaces twelve inline `regexp.MustCompile` calls, so
      the mode cannot be forgotten when a detector is added. Which detectors need it is not a
      property inspection can keep true: IPv4 is also an alternation and is saved today only
      incidentally, by its literal `.` separators and its trailing `\b`. Submatch cost checked
      rather than assumed — `ipv6Pattern` has no capture groups at all, and Go's
      leftmost-longest changes which parse wins only where a LONGER whole match exists by
      another route, which none of the four submatch detectors (Bearer, URL credentials, URL
      host, home dir) admits.
      **Tests assert completeness as a PROPERTY, because this bug was precisely a case nobody
      thought of.** `TestMaskingIsComplete` rows declare only inputs — prefix, secret, suffix
      — and the expected output is derived: the secret must come back as exactly one
      placeholder with the surrounding text untouched. A substring-survival threshold was
      considered and rejected: `fe80::1` leaks a SINGLE character, which no defensible
      threshold catches. The anchors also prove the detector did not overreach, which is the
      hazard `Longest()` creates, so several rows put a port or a path flush against the
      secret. `TestEveryDetectorHasACompletenessRow` requires every detector's target group to
      land exactly on some row's declared secret, so a detector added without a row goes red.
      **Two accepted over-masks, pinned as expected outputs in `TestAcceptedOverMasking`.**
      Unbracketed `2001:db8::dead:beef:443` now masks whole, port included — `:443` is a valid
      final hex group and RFC 3986 wants the brackets for exactly this ambiguity; bracketed,
      the mask still stops at `]`. And `CNSSCertStore::CNSSCertStore` masks `e::C` where it
      used to mask `e::`, one character more of #33's collision. Both are the cheap direction
      here: this package's asymmetry is the REVERSE of the pipeline's — matching too much
      costs one placeholder in a restatement that is already lossy, matching too little is
      disclosure.
      **#33's narrowing was deliberately NOT ported.** Its boundary and two-group rules would
      drop `::1`, `fe80::/64` and an address glued onto the preceding token — fragmentation in
      the pipeline, a leak here. The pattern's SHAPE is untouched; only how it is compiled
      changed. `internal/pipeline` does not appear in the diff. The two IPv6 patterns have now
      diverged in both directions (#40 narrowed one, #41 widened the other), which is #42's
      material, not this change's. README "Known limitations" was corrected to name which mask
      it describes: before this change both copies behaved the same way, so the unqualified
      sentence was true; this change is what makes it false.
      No template hashes move: this package runs at the LLM boundary and does not feed
      `hashTemplate`. Released as v0.8.6 (the README note was written against a pending tag).

- [x] **A templatized secret is still a secret** — #43, the second disclosure defect in the
      package whose job is preventing disclosure, found while wire-testing the #41 fix. One
      request, two adjacent fields, the same `Mapper`:
      `Trigger line: FATAL db postgres://<TOKEN_1>@<HOST_1>/prod lost` beside
      `Masked template: FATAL db postgres://svcuser:hunter<NUM>@<HOST_2><NUM>.<HOST_3>/prod lost`.
      It classified `svcuser:hunter2` as a credential and then sent it in the field next to the
      one it masked it out of.
      The cause is a coupling, not a pattern. `ExplainRequest.Template` is the one field that
      does not arrive as raw log text: `internal/llm/pool.go` builds it from the pipeline's
      template, so a value reaches the detectors shape-broken — `hunter2` as `hunter<NUM>`,
      `db01` as `db<NUM>`. Every detector class excludes `<` and `>` so placeholders stay inert,
      which also means a value the templatizer has already rewritten no longer matches. Four
      detectors were given a placeholder-tolerant form at `2da31be`; five were left out of the
      same decision and have leaked since. Dates from v0.4.0, twelve tagged releases.
      **The durable finding is the rule, not the two detectors the issue opened with: THE GAPS
      ARE EXACTLY THE DETECTORS WHOSE VALUES THE PIPELINE DOES NOT ALREADY COLLAPSE.** UUID,
      IPv4 and IPv6 are safe by construction — they arrive as `<UUID>` and `<IP>`, one
      placeholder, nothing left to recognize. All nine others receive a damaged but not
      collapsed value. The audit that produced the issue named two of the five; running the real
      patterns named five, which is what the rule predicts and is why the fix is all five.
      **Two kinds of disclosure, deliberately not reported as one number.** Detectors 4 and 5
      leak credentials and personal addresses — 5 is the likelier to fire, since a digit
      anywhere on either side of the `@` is enough and corporate addresses have digits. 9a, 9b
      and 10 leak internal topology: a domain (`.prod.acme.com`, and in a URL there is no second
      detector behind it — a public suffix is not on the bare-host allowlist), a host label
      (`db`), an environment name out of a path (`prod`). No credential material in the second
      group.
      **Two claims in the issue were wrong and are corrected rather than quietly contradicted.**
      Bare host does not "leak its first label": it leaks everything up to and including the
      label carrying the placeholder, so `db.eu1.corp.internal` leaks `db.eu`. And the home
      directory does leak plaintext — "no plaintext leak, only a fragmented placeholder" holds
      only for a TRAILING digit (`/home/maxie7` loses nothing), while a digit in the MIDDLE
      splits the value and sends the tail (`/home/deploy2prod` → `prod`). That asymmetry is the
      shape of the whole bug, so every test row carries a middle-digit case; a table written
      from convenient examples passes on examples chosen to pass.
      **The rule is enforced by a test, not by a comment, because #36 will change masking.**
      `TestEveryUncollapsedDetectorHasATemplatizedRow` asks the REAL templatizer what each
      detector's canonical secret looks like on arrival: collapsed to a lone pipeline
      placeholder means the anonymizer never sees a value and no row is required; anything else
      must have a row whose span the templatizer actually damaged. The exemptions are re-derived
      from the two real components every run, so when #36 moves what the templatizer collapses,
      a detector that is safe today goes red here without anyone touching this package.
      `TestTemplatizedMaskingIsComplete` runs the same composition and derives its expectation
      instead of hand-writing it — marks ride through `Templatize` with the text, and two
      premise checks refuse to proceed if templating perturbed them. A test that hand-writes
      what it thinks the templatizer produces can agree with itself forever while the two
      components drift apart, which is how these five were left out at birth.
      **The email detector's leading `\b` goes, and what that costs is enumerated rather than
      sampled.** A match begins either with a local-part-class character or with a pipeline
      placeholder. Beginning with a WORD character (letter, digit, `_`), `\b` could only fail
      where the character before it is also a word character — itself in the class, so leftmost
      matching would have started there and the span is identical either way. The excluded set
      is therefore exactly the four NON-word members of the class in leading position (`.`,
      `%`, `+`, `-`), each now absorbed into the placeholder and each pinned as its own row in
      `TestAcceptedOverMasking`, plus a local part the pipeline collapsed WHOLE
      (`<HEX>@corp.example.com` from a hashed address), which `'<'` being a non-word character
      made unmatchable — sending the entire address, domain included. That last one is a leak
      rather than a cost and is what decides the change; it is a row in `templatizedRows`, red
      with the assertion and green without.
      A CAPTURE WOULD HAVE ADDED NOTHING TO THAT CLAIM, and the 612-record journald capture was
      run and could not have: it contains zero lines with an `@` in them, so both forms match
      nothing and the delta is empty for want of data. The claim is about which first characters
      the pattern admits — finite, exhaustive over the class, checked against the real engine in
      leftmost-longest mode — not about real-world frequency. What stays UNMEASURED is only the
      frequency question: how often a real log line carries an address whose local part begins
      with `.`, `%`, `+` or `-`, and therefore how often the absorbed character is paid for. No
      capture we hold can answer it; measuring it needs a capture from a mail-adjacent source
      (an SMTP relay, a notification or paging service) where addresses appear at all, counting
      what share of address matches begin with one of the four. Same treatment as #33's
      colon-glued IPv6 gap: a cost no run has yet shown us paying, rather than one measured and
      accepted.
      **The two failure modes widening creates are guarded, both as tests.**
      `TestPlaceholdersAreInert` gains the adversarial strings for the five, which — unlike the
      four widened at `2da31be` — carry no distinctive secret prefix to lean on; the inertness
      argument is now in two parts (a class cannot ENTER a placeholder, and `pipelinePH` demands
      `>` where our own tags have `_`, so one cannot be stepped over either). The email detector
      needs both: `_` and `.` are in its local-part class, so a run can start inside `TOKEN_1`
      and only the `>` before the `@` stops it. Over-matching is guarded by keeping a port or a
      path flush against every value, the hazard `Longest()` created on every detector in #41.
      **`residue` starts catching this class, which narrows the note #41 just widened.** The
      re-scan could not have caught it before, for #41's reason: the detector that produced the
      leftover did not match the leftover. Now that the verify-eligible detectors read the
      templatized form, a value that survives in that form IS caught and the escalation is
      skipped. Both host detectors stay `verify: false` — flipping them would not have caught
      this class either (`db<NUM>.<HOST_1>` is not a shape the pattern matches), and for the
      bare-host detector missing a value entirely is the DESIGNED behaviour, so verify-eligible
      would fail closed on every escalation carrying `api.example.com`.
      **The Template field is kept rather than dropped.** Dropping it would delete the whole
      leak surface — it is the only pre-templatized field — but it is the only thing that tells
      the model WHICH tokens vary, it is the referent of `Occurrences: N`, it is a key-structure
      signature rather than a masked message for JSON lines, and RDI §7 pins it. A disclosure
      fix is not the place to change the request shape, and dropping it would leave the
      composition of the two maskers untested, which is the condition that produced the bug.
      `internal/pipeline` does not appear in the diff and no template hashes move: this package
      runs at the LLM boundary and never feeds `hashTemplate`. README was corrected at two
      sites — one of them asserted that secrets were recognized in both forms and called the
      cost "cosmetic, not a leak", which was wrong on the day it was written rather than made
      wrong by this change. Filed separately while auditing: a URL whose USERNAME is itself a
      recognised secret leaves its password unmasked, independent of templating — #46, and it turned
      out to be the reported half of a symmetric defect. Closed below.

- [x] **A credential's other half is still a credential** — #46, and the audit that came with
      it. `--llm-anonymize` sent half of every URL credential to the configured model endpoint
      in the clear whenever the other half was itself a recognised secret.
      The cause is the ordering working exactly as designed. Detector 4's target group spans
      `user:pass` as ONE value, which is what makes it readable as a credential rather than as
      two opaque runs. Detectors 1–3b — JWT, `AKIA…`, `sk-…` — are the more specific rules and
      run first, so a secret on EITHER side of the colon mints a `<TOKEN_n>` inside detector 4's
      span. Its classes exclude `<` and `>` by design (that exclusion is the inertness
      invariant), and with no second `://` to restart at the detector does not match AT ALL. The
      surviving half ships as literal text. `Mask` returned a nil error, `residue()` returned
      `""`, no escalation was skipped, and the card looked normal.
      **Symmetric, and the issue as filed was not.** It was filed as "a secret username leaves
      the password unmasked". Running the patterns showed the mirror: a secret PASSWORD leaves
      the USERNAME unmasked, `postgres://appuser:sk-live…@db` sending `appuser`. Whichever half
      was not recognisable is the half that goes. Fixing only the reported direction would have
      closed half the defect and looked complete.
      **Conditional on the authority shape, and every "rescue" is a different detector taking
      the value under the wrong tag.** With a dotted alphabetic host the EMAIL detector swallows
      `hunter2@db.acme.com` as one `<EMAIL_n>`; with an address host, detectors 7/8 mask the host
      before 9a runs, which destroys 9a's long parse and leaves it falling back onto the userinfo
      and masking the username as `<HOST_n>`. So:
      `@db.acme.com` password rescued / username sent; `@localhost` neither rescued;
      `@10.0.0.5` password sent / username rescued. **No shape is safe on both sides**, which is
      why the conditional narrows the description and not the severity — and why a reproduction
      against a fully-qualified host shows nothing wrong. That is how this survived #41 and #43,
      both of which read this detector closely.
      Dates from `2da31be` (v0.4.0), thirteen tagged releases (v0.4.0 → v0.8.7). Established
      from the FULL line history, `git log -p --follow` narrowed to the detector-4 hunk, after
      `git log -S'://('` returned one commit and was wrong: `-S` counts OCCURRENCES of a string,
      so the two later edits that rewrote the line without changing that count (#41 wrapping it
      in `mustLongest`, #43 widening both classes) were invisible to it. The properties that
      carry the defect — `<>` excluded, `+` on each half — are present in all three states, so
      the span holds; but the method that could not have shown otherwise is not evidence.
      **Two defects, one patch, kept apart in the writing.** The `*` quantifiers also close a
      gap that is not interference at all: detector 4 required at least one character on each
      side of the colon, so `redis://:password@host` — the ordinary Redis DSN, Redis having had
      no username before 6.0 — matched no credential detector at all. That much held on every
      host shape, but the DISCLOSURE did not, and the distinction is the same one #46 turned on:
      the email detector took `password@cache.acme.internal` whole whenever the authority ended
      in a dotted alphabetic suffix, `.internal` included, so only a bare host, a loopback or an
      address literal actually shipped the password. This entry said "on any host shape" in
      draft, which was the plan's claim and was wrong. Same v0.4.0 span, different cause, its own
      tests and its own README paragraph.
      **The fix adds two rules rather than loosening one.** 4b masks the password when the
      username was pre-masked, 4c the username when the password was; a single rule cannot mask
      two spans. Neither fires after detector 4 succeeded (no `:` left in the userinfo) nor after
      the other (the masked half begins with `<`). Both groups exclude `<>` exactly as detector
      4's do; only the opposite half, which is CONTEXT, admits one of our tags.
      **Widening detector 4 was rejected twice over, and the second reason is the durable one.**
      It attacks the inertness invariant, which is arguable. It also NESTS, which is not:
      `Restore` iterates `m.byToken` — a Go map — and substitutes ONCE, with no fixpoint, so a
      mapping whose value contains another mapping's token resolves correctly or does not
      depending on iteration order. Measured with a directly constructed nested pair: wrong in
      **175 of 200** restores in one process, and the rate is a function of the keys' hashes
      rather than a coin flip (Go randomises the starting cell offset; two keys in adjacent cells
      of one bucket give 7/8), so it varies per process — a test that is green four runs in five.
      Latent today because nothing nests; nothing prevents it either — filed as **#50**, with
      `TestRestoreHasNoNestedMapping` landing here to enforce the precondition this argument
      rests on, since a rejection resting on an unenforced property is not a rejection.
      **Reordering fixes nothing here and the reason is worth writing down.** The blockers are
      the more specific detectors and must precede detector 4: running 4 first would mask
      `sk-live…:password` as one opaque credential and lose that the username is a live API key.
      Order is the wrong tool for this pair. It remains a candidate for the UUID-vs-host pair
      below (**#48**), which is one reason that one is filed rather than folded in.
      **The invariant is sharpened, not weakened.** The package comment claimed no detector may
      MATCH a placeholder we minted. What `apply` and `residue` depend on is that no detector's
      TARGET GROUP may land on one — `apply` writes only the group, `residue` tests only whether
      the group captured. The fallbacks read a tag as context and are safe under the narrow
      claim, which is now the stated one and is mechanised by
      `TestNoDetectorGroupLandsOnOurOwnOutput` against the real masked output of every
      completeness row, checking group/placeholder OVERLAP rather than "matched somewhere in a
      string containing a placeholder" — the loose form reports residual plaintext beside a tag,
      which is a coverage question and a different test's.
      **`Mask`'s honest-scope note was WRONG, not incomplete.** It said the re-scan catches a
      value a detector missed ENTIRELY. #46 is exactly that and it did not catch it. The note now
      carries the precondition it always needed — provided no earlier detector rewrote part of
      that value's span — and `residue` starts catching this class, since the fallbacks are
      verify-eligible and their context admits our tags. Same narrowing move #43 made, one class
      further.

- [ ] **The detector-on-detector interference audit** — the deliverable #46 came with, recorded
      here because the negative results are the part that will not survive in anyone's head.
      #41 (a detector matched a value INCOMPLETELY), #43 (another COMPONENT rewrote it first) and
      #46 (another DETECTOR rewrote it first) are one blindness with three causes, each found by
      accident while verifying the previous one. The bounded question: for every detector whose
      pattern spans a COMPOSITE value, which earlier detectors can mint a placeholder inside that
      span, and what happens? Finite — thirteen detectors in fixed order — and decidable
      mechanically, because every target group excludes `<` and `>`, so a placeholder inside a
      later detector's span truncates or blocks it. The one designed exception is detector 9a's
      userinfo run `[^/@\s]*@`, which admits `<>` on purpose to step over a masked credential.
      | interferer → blocked | example | sent | verdict |
      |---|---|---|---|
      | 1/2/3b → 4 (either half) | `postgres://sk-live…:hunter2@localhost` | the other half | **DEFECT, fixed** (#46) |
      | 3a → 4 | — | — | **impossible**: 3a needs `bearer` + `\s+`, and the userinfo class excludes whitespace |
      | 4 → 5, 4 → 9a | `postgres://u:p@db.acme.com` | nothing | **harmless — designed**; this is why 4 precedes both |
      | 5 → 9a, 5 → 10 | `/Users/bob@corp.com/…` | nothing | **harmless**; the value is masked whole, as EMAIL |
      | 7/8 → 9a | `https://10.0.0.5:8080/x` | nothing | **harmless**; 9a is blocked but the whole authority WAS the address |
      | 6 → 10, 1/2/3b → 10 | `/home/550e8400-…/app.log` | nothing | **harmless**; blocking detector 10 costs a tag, never a value |
      | 1/2/3b → 3a | `Bearer AKIAIOSFODNN7EXAMPLE` | nothing | **harmless**; already pinned by `TestPlaceholdersAreInert` |
      | 1/2/3b → 5 | `eyJ….eyJ….sig@example.com` | the domain | **theoretical**; nothing puts a JWT in an email local part |
      | 6 → 9a | `https://550e8400-….blob.core.windows.net/x` | the whole domain | **DEFECT, filed #48** |
      | 8 → 9a | `https://10.0.0.5.nip.io/x` | `.nip.io` | **DEFECT, filed #48** (same cause) |
      | 6 → 9b | `worker-550e8400-….corp.internal` | `worker-` | **DEFECT, filed #48** (same cause) |
      | 7 → 8 | `::ffff:192.168.1.1` | `.168.1.1` | **DEFECT, filed #49** |
      **Detector 10 has no defects at all**, and that is a computed result rather than an
      untested area: every interferer that can reach a `/home/` username masks it entirely.
      **Detector 5's blockers are all theoretical** — it is the composite with the most paths
      into its span and none corresponds to a log line anyone will write. **3a → 4 is
      structurally impossible**, not merely unobserved.
      Three gaps the same sweep surfaced that are NOT detector-on-detector, recorded so the next
      person does not re-derive them: `redis://:password@host` (empty half — fixed here, above);
      `sk-proj-…`, the current OpenAI key format, which detector 3b does not match at all
      (**#51**) and which is anti-correlated with #46 — a key 3b cannot see cannot block
      detector 4, so fixing 3b alone would INTRODUCE this leak for those keys, and #51 must not
      land before this does; and detector 9a's scheme anchor (**#52**), which #43 left
      non-tolerant while widening the host group beside it, so `s3://` templatizes to
      `s<NUM>://` and the host goes unmasked on the `Template` field. That last one was found in
      the #46 wire capture rather than by a test, and the lesson generalises: **a detector's
      tolerance has to cover its ANCHORS, not only its capture** — which is also why the
      coverage test that should have caught it did not, since it checks a detector's GROUP and
      the scheme is context.
      `internal/pipeline` does not appear in the diff and no template hashes move: this package
      runs at the LLM boundary and never feeds `hashTemplate`. README corrected at three sites,
      one of which asserted that the fail-closed re-scan catches a value a detector missed
      entirely — wrong on the day it was written, and the sentence this defect walked straight
      through. Released as **v0.9.0** — a MINOR bump, and the reason is the audit rather than the
      defect count: the package was swept systematically for the first time, the full
      interference table published, one defect closed and four filed (#48, #49, #51, #52, plus
      #50 from the rejected fix). A patch release would have
      described this as one more fix in a series, which is the framing the series is trying to
      escape.
