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
- [x] **Community health files** — #123. The community profile listed four missing items; three
      are added.
      - `SECURITY.md`. The latest release is supported, and fixes ship as patch releases.
        Reports go through GitHub private vulnerability reporting, with a synthetic
        reproduction and never real logs or credentials. Best effort: no SLA, no bounty.
        The scope is grounded in what the README promises:
        - in: a documented-masked value reaching the model endpoint, a partial mask included;
          a request despite `--explain-dry-run` or a failed mask; a release artifact that
          fails attestation or its checksum; a crafted line that crashes, hangs or executes;
        - out: real values in the TUI and the export; unmasked lines to a local model or with
          masking off; gaps the README already names.

        Until this, an anonymizer leak could only be reported in public, which disclosed it.
      - `.github/ISSUE_TEMPLATE/`: a bug form (version, source, command line, anonymizer
        setting, backend kind, expected and actual, and a warning against real log lines), a
        feature form, and `config.yml` keeping blank issues with a link to the advisory form.
      - `.github/pull_request_template.md`: one HTML comment with the closing-keyword rule,
        which #61's reopening came from.
      - README and CONTRIBUTING point at all three.

      Left out, with reasons:
      - **Code of conduct:** a single-maintainer project has no community spaces of its own
        to moderate yet.
      - **Accessibility statement:** not until there is real content to state for a TUI.

      Private vulnerability reporting has to be enabled in the repository settings
      (Settings → Security); without it the advisory link 404s.

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

## Epic M12 — Replay  `[v0.10.0]`
- [x] **`--replay`: the clock is the feature** — #35, the instrument the scoring half of this
      project had been missing. Before it, every calibration decision was either a six-hour
      laptop run or a synthetic fixture asserted by its author (#32's upper band edge is
      "an assumption wearing a test's clothing" for exactly that reason), and the 612-record
      journald capture every recent calibration argument leaned on was already lost.
      `--replay <file>` plays a `journalctl -o json` capture through the same decode,
      coalescer, pipeline, scorer, templater and export as a live run, clocked by the
      capture's own timestamps.

      **The premise it was filed on was wrong, and the measurement is why replay matters more
      than it said.** Piping a capture through stdin was expected to make everything burst.
      It does the opposite. A 703-line synthetic capture spanning ten virtual minutes holds:
      - a novel ERROR at PRIORITY 3;
      - `INFO: shutting down` at PRIORITY 3 (the #24 precedence case);
      - an INFO retry storm on an established template;
      - a PRIORITY 1 OOM.

      Piped as `--plain --explain-dry-run`, it produced **zero** escalations:
      - every line was tagged `stdin` with no level, and the JSON envelope was templated as
        the message;
      - warmup (30s) never elapses inside one wall second, so novelty stays muted;
      - no template reaches `baseline()`'s 60s age, so nothing can burst;
      - even the priority-1 OOM stayed quiet.

      Through decode on the capture's clock, the same file gives **four**, one per fault. A
      silent wrong answer is indistinguishable from a quiet healthy host, which is worse than
      a loud one.

      **The seam was one line.** The scorer took time as an argument everywhere:
      - `Evaluate(…, now)`;
      - `RateLimiter.Allow(now)`;
      - the cache's `Hit/Mark(hash, now)`.

      `Process(line, now)` handed that one `now` to `upsert`, `Evaluate` and `flagged`. The
      only wall-clock read on the scoring path was `now := time.Now()` in `pipeline.Run`.
      `Options.SourceTime` replaces it with `sourceClock`, which is:
      - monotone, because the recent ring is append-ordered and `countSince` stops at the
        first older entry, so a rewind would shrink every burst window;
      - held where it is by an untimed line;
      - advanced only by the pipeline goroutine, so there is no lock and no second owner of
        time.

      A `Clock` interface was rejected because replay time belongs to each LINE, not to the
      moment: someone would have to advance it, and if the ingest goroutine did, that would be
      a second owner. A `func(LogLine) time.Time` field was rejected as the same power with an
      indirect call per line and one non-default implementation. **`git diff internal/score`
      is empty.**

      **The rate limiter answer.** It follows the capture's clock automatically (`decide`
      passes Evaluate's `now`), so a replay admits 10 escalations per CAPTURE minute. That is
      scoring fidelity: the replay flags what the live run would have flagged. It is NOT M4's
      cost guarantee, which is about real calls per real minute; at 1000× a capture-clock
      limiter in front of a real model would admit 10,000 real calls a minute. The two
      properties need two limiters. Replay needs neither, because it is dry-run only.

      **Dry-run only, as a decision.** Replay runs the scorer, not a model:
      - everything it was built for (#37, #32's upper edge, #36's re-measurement) lives in the
        scorer;
      - `--explain-dry-run` already builds no backend, so requiring it inherits the cost
        guarantee by construction;
      - a live model would trade away the determinism the instrument exists for;
      - a wall-clock `Explanation.At` beside a capture-clock `LastFlagged` would regress #34's
        "answer predates the newest flag" silently, and only in replay.

      The check reads the RESOLVED configuration, not the flag. `explain_dry_run: true` in a
      config file satisfies it, and `startLLM` refuses a replay on its own from the same
      fields. A guard and the mechanism it guards that consulted different objects would agree
      only until a second path set the value, which is #24's and #43's class.
      `TestReplayResolvesToNoLLMStage` runs resolution and stage construction back to back, and
      `TestReplayWithoutDryRunBuildsNoBackend` asserts zero requests reached a model endpoint,
      not merely an exit code. Live-LLM replay is #75, a display feature nothing
      blocked is waiting on.

      **Three determinism breakers, each measured and each fixed:**
      1. *The wall-clock read*, fixed by the seam.
      2. *The export writer dropping under load.* Six virtual hours with a distinct FATAL every
         5s gave 3,609 escalations. Across three replays of one input the file got 548, 561
         and 574 records, with over 6,000 queue messages dropped each time. `OpenBlocking`
         makes a replay's writer wait for the disk. Replay has no real-time contract to keep,
         and its file is the artifact that gets diffed. The blocked send escapes on `quit`, so
         it cannot outlive `Close`. The live writer still drops, and
         `TestDefaultWriterStillDrops` pins that.
      3. *The coalescer flushing in map order.* That turned out to be a live defect and landed
         separately first (#69, in Fixes below).

      The coalescer also needed a capture clock. At replay speed a header and its continuation
      arrive microseconds apart, so a wall timer would fold lines a live run split and would
      never fire. `CoalesceSourceTime` flushes, before each line at capture time T, every
      buffer whose deadline T has reached. It stamps each released line with the moment a
      live run would have PROCESSED it:
      - the next header's time, for a line flushed by that header;
      - its deadline, for an idle flush;
      - the last time seen, for the end-of-input flush.

      Live stamps at processing time, and a held line reaches the pipeline up to one group
      timeout after its entry, so exported times sit up to `--group-timeout` after the
      journal's (measured: 10:43:20.2 in the capture, 20.4 in the file). Fan-in still runs;
      with one source every stage has exactly one producer, and order is preserved end to end.

      **No scoring drift, proved in two strengths:**
      - `TestSourceTimeRunMatchesInjectedNow` is exact: `Run{SourceTime}` against a loop of
        `Process(line, line.Time)`, the contract every existing test uses, must agree on every
        event down to the reason strings byte for byte. The capture reaches every gate:
        escalated, suppressed, cached, burst and cooloff.
      - `TestReplayAtOneXMatchesWallClock` plays a six-second capture into today's program in
        real time (wall-clock coalescer and pipeline) and at max speed on the capture's clock.
        Both flag the same templates, in the same order, for the same kinds of reason, with the
        same counters: 7 escalated, 1 suppressed, 8 cached. The capture covers first-seen and
        cooloff novelty, a coalesced traceback, a burst, a FATAL and an empty token bucket.
        What is NOT compared is stated in the test: digits inside reasons (measured by
        whichever clock ran) and absolute timestamps. It passed 6 of 6 under `-race`.
      - `TestWallClockAtMaxSpeedDiverges` shows the comparison can fail. The live program fed
        the fixture at max speed through decode was predicted, not measured, to flag 1: the
        FATAL, which needs no warmup. **Measured: 1** (the OOM), against replay's 4. The
        number is logged, not asserted.

      **No template hash moves:** `template.go` and `normalize.go` are untouched, and
      `TestReplayHashesEqualLiveDecode` checks every fixture line against the live decode.
      **No existing test was modified;** every changed `_test.go` file only gained lines.

      The fixture is `internal/ingest/testdata/replay/basic.jsonl`: synthetic, generated by
      `gen.go`, 115 KB. `TestReplayFixturesArePublishable` holds everything in that directory
      to four fields (a real entry carries 31, including `_HOSTNAME`, `_MACHINE_ID` and
      `_CMDLINE`), RFC 5737 / RFC 3849 addresses, reserved host names, 128 KB and 1,000 lines.
      A sanitiser for real captures is #74.

      **Replay has NOT yet been validated against a real capture.** The fixture is synthetic,
      and synthetic fixtures are precisely what this instrument exists to replace. The first
      real use, a real multi-hour capture replayed for #37, is its validation. Keep captures
      outside the repository from now on; the README's recording section says so, because
      nothing did when the 612-record capture was lost.

      Found on the way and filed, not fixed:
      - #72: the prompt's "first seen N ago" subtracts a wall-clock `FirstSeen` from a
        source-time trigger, which is skewed or silently omitted on backlog lines. The prompt
        is the only place affected; it is named in README "Known limitations".
        Fixed, in Fixes below.
      - #71: export times carry the local zone (`Z` against `+09:00` for one instant).
        Fixed, in Fixes below.
      - #70: the live export's drop count reaches the user only at exit, as queue
        messages labelled records (a 2× overstatement), with exit status 0. (Done with #70,
        in v0.12.19.)
      - #73: `--record` for sources without their own clock.

      **What this unblocks, stated and not built:**
      - #37: replay a real capture under today's lifetime `baseline()` and under a candidate,
        then `diff` the two exports; each changed burst record is one decision.
      - #32's upper edge: sweep `--burst-min-count` over a capture holding a real incident;
        the largest value that still flags it is the measured edge.
      - #36: count distinct `template_hash` values against escalations on the post-#40
        corpus.

      Targets v0.10.0

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

- [x] **The burst baseline forgets** — #37, the root cause the entry above routed around.
      `baseline()` was `Count / (now − FirstSeen)`, a lifetime average, so rate history was
      permanent in both directions. A formerly rare template stayed burst-prone forever, and
      a formerly chatty one could never burst again.

      **The evidence is replay of real captures, the first use #35 was built for.**
      - **Controlled experiment.** On a real laptop (SYSLOG_IDENTIFIER `ab37`), one INFO
        template ran at 1/s for 2 h, 1/min for 1 h, then 3/s for 20 s, with a live
        `--journald --burst-multiplier 4` run alongside.
      - **Ground truth reproduced.** Replaying the recorded journal gave the live export record
        exactly on kind, template_hash, count_at_flag (7287) and reasons. That is the first
        validation of replay against a real capture.
      - **The miss, under the lifetime baseline (0.67/s at the incident):** at the default
        multiplier 5 the incident never fired. At 4.5 it fired at 31 lines.
      - **Ten whole boots** (Oct 1–12, about 40 h) were probed at multiplier 1.01. They held 9
        windows where a burst is possible at the 25 gate:
        - 8 GLib clusters (113×–830× a near-zero baseline). An always-rare template is
          near zero under any averaging, so these are #32/#36 evidence, not #37 evidence.
        - 1 logfmt artifact of #83.
        - Cutting each boot's first 5 minutes (the boot's own log storm, which a logscry
          started after login never sees) removes the logfmt window and leaves the 8.
        - So the natural data barely discriminates; the experiment does.

      **The fix.** The baseline is half-life-weighted:
      - A decayed occurrence count (`Template.DecayedCount`, advanced by the scorer on every
        line) is divided by the decayed exposure since FirstSeen, `τ·(1 − e^(−age/τ))`, with
        `τ = H/ln 2`.
      - A constant rate measures that rate at any age.
      - For age ≪ H it *is* `Count/age`, so a young template is scored as before.
      - `--baseline-half-life` (yaml `baseline_half_life`) sets H. **0 restores the lifetime
        average exactly**, the way raising novelty back to 1.0 restores #27's old behaviour.
      - A windowed baseline was not taken. `Recent` (2048 entries) spans 30 min only below
        1.1/s, and a hard window edge makes the baseline jump when a burst ages out.

      **H = 30 min, inside a band whose edges are NOT the same kind of evidence.**
      - **Data, upper edge: ≤ 80 min.** The ab37 incident fires at its onset (25 lines) only
        while its baseline is ≤ 0.5/s. On a real replay that holds up to 80 min (0.50/s);
        at 90 min the incident fires a line later.
      - **Data, lower edge: > ~35 s.** Below it, a real 25-line GLib cluster is absorbed into
        its own baseline: 25/τ must stay under the 0.5/s gate. On a real replay, H = 30 s
        loses all 16 GLib escalations, and the ab37 incident with them; 60 s keeps them.
      - **Policy, not measurement: ≥ ~26 min.** A template resuming at its *own* rate reads
        as a burst after about log₂5·H of silence. Holding that past the 1 h cache TTL, the
        existing rule for "worth explaining afresh", needs H ≳ 26 min. Nothing in the
        captures puts this edge there. It is a choice, borrowed from the cache's.
      - At 30 min, ab37 is caught at 0.26/s (10×, 1.9× above the multiplier) for any quiet
        ≥ 35 min. A 3/s template resuming after 60 or 65 min of silence stays quiet; after 70
        it fires. 45 min was the quieter alternative (resumption after about 104 min), but it
        would hold ab37 at only 6.7×.

      **What changed on real data** (v0.10.3 against the branch at 30 min; 21 replays: ten
      boots full and trimmed, plus the ab37 capture):
      - **ab37:** +1 record, count 7285, `2.5/s vs baseline 0.26/s (10×), 25 in the last 10s`.
      - **Natural captures:** no record added or removed, full or trimmed.
      - **GLib, 8 records** (Oct 8 20:19 and Oct 9 09:12): same count, same instant. Only the
        printed ratio changes (113× → 97×, 830× → 255×), because the cluster now weighs more
        than the old lines.
      - **logfmt, 1 record** (Oct 8 20:19, full only): count_at_flag 157 → 156, at the same
        microsecond. **The prediction that a decaying baseline would fire at the cluster's
        onset (118) was wrong at this H.** The template is about two minutes old there, far
        below the half-life, which is exactly the case where the formula reduces to the
        lifetime average.
      - `--baseline-half-life 0` reproduces v0.10.3's exports **byte for byte** on all 21
        replays, plus ab37 at multiplier 4. The escape hatch is the old scorer.
      - The probe count is unchanged: 9 full, 8 trimmed.

      **Tests.** Red first. Every history is generated in code, and no capture line is in
      the repository.
      - `TestFormerlyChattyTemplateBurstsAgain`: the ab37 schedule. It fails on main.
      - Two pins that also pass on main:
        `TestSameRateResumptionStaysQuietPastTheCacheTTL` and
        `TestClusterIsNotAbsorbedIntoItsOwnBaseline`.
      - Together the three bound H from both sides. Mutants confirm each edge: H = 20 min
        breaks the resumption test, 90 min and 0 break the onset test, and 30 s breaks the
        cluster test.
      - Added with the fix: `TestZeroHalfLifeIsTheLifetimeAverage`,
        `TestHalfLifeBaselineMeasuresASteadyRate`, `TestNegativeHalfLifeIsRejected`.

      **No existing test changed outcome.** The full suite ran under `-race` on `main` and on
      the branch: 848 results, then 856. The only differences are the 8 new tests and
      subtests; all 75 existing score results are unchanged.

      Seen and left alone, out of scope: #83 (logfmt lines collapse into one template), #36
      (one GLib event, four templates), #32's gate value.

      Found on the way and filed, not fixed: **#87**. Replay drops a journal entry
      whose MESSAGE is empty, after decode, while a live `--journald` run emits it as an empty
      line at the entry's priority. Every captured boot holds exactly one (kernel, priority
      3). Confirmed by a throwaway test that feeds the same entry through both paths (live 2
      lines, replay 1); it is in README "Known limitations" (Done with #87, in
      v0.12.16.). It does not touch this measurement: both legs of every A/B are replays,
      and the ab37 capture holds none.
      RDI §6 updated. Targets v0.11.0

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

- [x] **A logfmt line is templated on its keys, and its level is read where it sits** — #83,
      found while preparing data for #37. `time="…" level=error msg="…"` is how dockerd,
      containerd, logrus and slog's text handler write. It went down the text path, which
      failed it twice:
      - `strPat` masks every quoted string whole, so `msg="disk full"` and
        `msg="connection refused"` were both `msg=<STR>`. Every message of one level was one
        template. In one boot, 171 info lines from four daemons were a single template, and a
        cluster of that union escalated as a burst.
      - `level=` was read only at the start of a line, so a line beginning with `time=` got no
        level. journald records these at PRIORITY 6, and stdin, subprocess and Docker have
        nothing else, so `level=error` weighed nothing.

      Together these meant a new dockerd or containerd error could not escalate on its own.

      **What counts as logfmt.** The whole line has to parse as two or more `key=value` pairs
      separated by spaces or tabs. A value is either a quoted string, which must terminate, or
      a bare run with no `"` and no `=`. One key has to be a level or message key, from the
      sets normalize already trusts for JSON.
      - Prose fails at its first bare word, so `user=bob login failed`, the demo's
        `INFO: request method=GET …` and the replay fixture keep the text path.
      - The key requirement keeps out environment dumps (`HOME=/`) and audit records
        (`apparmor="DENIED" operation=…`).
      - A line break disqualifies an event. That one was found by measuring, not planned (see
        below).
      - On ten boots of one laptop (62,707 entries) the rule claims **2,437**: containerd 1,028,
        nvidia-ctk 946, dockerd 293, ollama 104, sh 66. Every one begins with `time=` and
        carries `level`, and no line that starts with a bare word is claimed.

      **The template** keeps the keys literally and in their order, joined by single spaces.
      - The message and `error` are unquoted, run through the text masker and re-quoted. That
        is M9's `maskedMessage` computation, so the fragment is byte-identical to the text
        path's template.
      - Every other value keeps its text-path rule: quoted becomes `<STR>`, bare is masked.
      - Keys are not sorted. A producer writes one order per call site, so sorting would only
        move hashes.
      - `error` is included because it is where the failure cause lives: both real dockerd
        errors share `msg="Failed to get event"`. Including it costs +2 templates across all
        ten boots. `err` is not included, because no captured line carries it.

      **The level** is read from the first level key present, wherever it sits, and only
      inside a logfmt line. Mid-line levels in prose stay #31's. A source-supplied level
      still wins.

      **Hash stability.** A non-logfmt line reaches `Templatize` over `normalizePlain`'s
      message byte for byte. Measured on every line of the ten boots and the ab37 capture
      (main against the branch):
      - 2,415 hashes moved, all on claimed lines;
      - **0** hashes moved on unclaimed lines;
      - 0 levels changed on unclaimed lines.

      The 22 claimed lines that did not move carry neither a quoted message nor a quoted error.

      **What changed on real data.** The A/B covers 21 replays at default settings: ten boots,
      full and with each boot's first 5 minutes cut, plus ab37. It runs main@3027afb against
      the branch. A third leg, v0.10.3 against the branch at `--baseline-half-life 0`, gave
      the same five changes, so none of them is #37's.

      The prediction was made from the weights before any replay. Every logfmt line is
      PRIORITY 6, which means stdout and no stderr weight. It predicted full +3 −1, trimmed
      +1 −0, ab37 0. **It held exactly.**
      - **Added, full:** boot Oct 10 13:44 at +103 s, dockerd `Failed to get event` twice.
        These are two templates, one per containerd namespace, each novel + ERROR = 1.05.
        Boot Oct 12 at +42 min, an ollama ERROR line, 1.05.
      - **Added, trimmed:** the ollama line only. The dockerd pair falls inside the cut.
      - **Removed, full:** boot Oct 8 20:19, the logfmt union burst #37 measured
        (`6.3/s vs baseline 1.26/s (5×)`, count 156). With the union split, no logfmt template
        reaches 25 lines in 10 s. It was already gone in the trimmed replay.
      - **Unchanged:** every other record is byte-identical (90 records). No INFO or WARN
        line escalates (0.45 and 0.65), so #27's rule holds.
      - **Templates:** +104 per full boot (+106 on the two boots with standalone ollama lines),
        +1 per trimmed boot (+3 on those two), ab37 +0.
      - **Levels gained, per line:** INFO 2,223, WARN 211, ERROR 3. Per event (after
        grouping): INFO 2,124, with WARN and ERROR unchanged. The difference is the line-break
        rule below.

      **The line-break rule, found by the replays.** The coalescer's Go-frame cue `\.go:\d+` is
      unanchored, so slog's `source=server.go:99` folds each ollama line into the one before
      it. In the captures that makes 11 events holding 99 of ollama's 104 logfmt lines. The
      first version counted `\n` as whitespace and read each group as one logfmt line with
      repeated keys: ambiguous message, and the first line's level. A grouped event is several
      physical lines, so it is now refused and keeps the text path. The 11 events are
      byte-identical to main. The coalescer defect itself is filed, not fixed (below).

      **The anonymizer.** The template is sent to the model, and it now carries message and
      error text that `<STR>` used to hide. That is #43's exposure in a new wrapper, so
      `TestLogfmtTemplatizedMaskingIsComplete` runs every templatized row as a logfmt msg
      value and as an error value, plus six rows with the secret against a quote.
      - 58 pass.
      - One row, run as msg and as error, is skipped. The home-dir rule takes the closing
        quote into `<USER_N>`. That over-masks rather than discloses, it predates this change,
        and raw lines and JSON templates do the same.
      - The raw-line suite needs no row: the trigger line was sent verbatim before and still is.

      **Tests.** Red first: `TestIssue83LogfmtLines`, the issue's four lines, failing on main
      on templates, levels and counts. Added with the fix:
      - `TestLogfmtRecognition`, `TestLogfmtTemplate`, `TestLogfmtLevel`;
      - `TestNonLogfmtLinesKeepTheirTemplate` (prose, demo and fixture shapes, a grouped
        event) and `TestFixtureLinesKeepTheirTemplate` (the replay fixture, line by line);
      - `TestLogfmtErrorEscalatesOnItsOwn`. Its matrix covers stdout and stderr: a new INFO
        scores 0.45, or 0.75 on stderr; a new WARN 0.65, or 0.95 on stderr (the loudest a
        merely-new line gets); a known ERROR 0.60; a new ERROR 1.05, or 1.35 on stderr;
      - a mid-line `level=error` row in `TestJournaldDefaultPriorityDefersToTheText`.

      Mutants confirm the edges. Masking the message whole again, dropping the key
      requirement, and allowing line breaks each turn tests red.

      **No existing test changed outcome.** The full suite ran under `-race` on main and on the
      branch: 856 results, then 997. Every difference is a new test or subtest, three of them
      skips: the existing one and the two home-dir quote rows.

      **What a user sees.** In exports, `template_hash` and `pattern` change for logfmt lines;
      no other line's hash moves. A logfmt line now shows its level, and its card shows its
      message.

      Seen and left alone, out of scope: #31 (plaintext levels after a prefix); #36, visible in
      the new templates (digit runs inside device names and library paths); #32, #84, #85 and #87; and
      the anonymizer's detectors.

      Found on the way and filed, not fixed, each confirmed by a throwaway test that was not
      committed:
      - **#89:** a line mentioning `file.go:N` is folded into the line before it,
        so an ERROR can disappear into an INFO event.
      - **#90:** the home-dir rule takes a closing quote into the username.
      - **#91:** `strPat` ignores backslash escapes, so `"user \"bob\" …"`
        templates as `<STR>bob\<STR>`.
      - **#92:** M9's JSON msg fragment has no templatized-completeness test. A
        throwaway run of all 24 rows passed, so this is a coverage gap, not a leak.

      The first three are in README "Known limitations". Targets v0.12.0

- [x] **A Go frame cue is a frame line, not any mention of `file.go:N`** — #89, found while
      building #83, whose line-break guard it forced. The coalescer's Go cue was `\.go:\d+`,
      unanchored and not gated on an active block. Any line citing a Go source position was
      folded into the line before it if it arrived within `--group-timeout` on the same
      stream. That caught slog's `source=server.go:99`, logrus's `file="main.go:42"` and
      `log`'s `Lshortfile` prefix (`main.go:42: …`). An ERROR straight after an INFO
      disappeared into the INFO event. On the text path the group then had no level, so the
      ERROR's severity and novelty were both lost.

      **The rule.** The cue now matches a whole line that holds nothing but a frame position.
      It is not gated.
      - The line is a path ending in `file.go:N`. The path holds no whitespace and no `=`, so
        a bare `source=x.go:N` is not a frame.
      - The runtime's ` +0x…` PC offset may follow; an inlined frame prints none.
      - So may the `fp= sp= pc=` that `GOTRACEBACK=system` appends.
      - Nothing else is allowed on the line.

      A real frame line is tab-indented and folds on indentation alone, so the cue only
      matters for frames whose indentation a producer or shipper stripped. No logscry
      ingest path strips it.

      **Why not gate it on `p.active`, like `goFrameRe`.**
      - Gating the old cue would not have fixed it. Every fold makes a block active, so a
        log line after a trace still joined the trace.
      - Gating the anchored cue costs real frames. A stripped file line after an idle call
        line (`main.main()` with no `panic:` above) folds today and would split.

      **What moved on real data.** The harness ran on the same 21 captures as #83: ten
      boots, the same ten with each boot's first 5 minutes cut, and ab37. It compared
      main@c1e638f with the branch on the source clock at the default 200 ms timeout.
      - **Full boots.** 68 events split back into the 321 lines they held:
        - ollama: 21 events, 109 lines, in the three boots that run it;
        - snapd: 37 events, 172 lines, all ten boots;
        - canonical-livepatchd: 10 events, 40 lines, all ten boots.
      - **Trimmed boots.** Ollama is unchanged at 21/109, because its lines fall after the
        cut. snapd splits 21 events holding 62 lines. livepatchd falls inside the cut.
      - **ab37.** Nothing moves.
      - **No event merged.** Every split event became single lines. No other source moved,
        and no line holding only a frame position was split.
      - **Why each line moved.** Every line in a split event cites a position, with one
        exception. snapd and livepatchd write the `file.go:N: msg` prefix. Ollama's 10 other
        lines are llama.cpp runner lines that headed a group of slog lines.
      - **Reconciling #83's count.** #83 measured "11 events holding 99 lines". Those 11 are
        the logfmt-headed events, holding 69 logfmt lines. The other 10 events are headed by
        a runner line and hold the remaining 30.

      **Logfmt and #83's guard.** All 104 of ollama's logfmt lines now arrive one per event,
      and every one is claimed:
      - levels: 103 INFO and 1 ERROR. The ERROR was already standalone and is unchanged.
        None of the 99 that were grouped is above INFO, so in this data no error was hidden.
        The defect is shown by the test, not by an escalation;
      - templates: 26, of which 25 are INFO and 1 is ERROR. No template has more than 10
        lines across the three boots or more than 2 in any 10 s window.

      On main, 11 multi-line events in each of the full and trimmed sets had a first line the
      logfmt path would claim. On the branch there are none, in all 21 captures. The guard
      is kept because a logfmt line can still carry a folded continuation, and a test pins
      that.

      **Replays, predicted first.** The prediction was written from the weights before any
      replay: **+0 −0 on all 21, every export byte-identical.**
      - Every event the fix creates is stdout, at INFO or no level, so it scores 0.45 at
        most.
      - The old group templates had no level either.
      - No new template comes near the burst gate of 25.
      - The surviving templates whose count moves are ollama runner lines, which have no
        level.
      - On the full boots, warmup ends on the same line at the same time, 12 or 13 events
        later by count.

      **It held.** All 21 exports are byte-identical (94 records). The `--plain` streams grow
      by exactly the split counts.

      **Templates.** Full boots gain +16 or +17 templates, and +31 to +34 on the three boots
      with ollama. Trimmed boots gain +4 or +5, and +19 to +21 with ollama. ab37 gains none.

      **Tests.**
      - Red first: `TestIssue89SourcePositionLines`. It runs slog, logrus and `Lshortfile`
        pairs 50 ms apart on one stream through both coalescers and fails on the event
        count. The levels it checks are INFO then ERROR, and none for `Lshortfile`, which
        is #31's.
      - Added with it, passing on main where the old rule was right:
        - `TestGoFrameLineShape`, the rule's rows;
        - `TestCoalesceGoFrameLines`. It checks that a real Go 1.26 goroutine dump folds
          into one event both tabbed and stripped, that a stripped file line folds after an
          idle call line, that a log line after a dump is its own event, and that a logfmt
          line with a folded frame keeps the text path.
      - Four mutants each turn a row red: deleting the cue, gating it unanchored, anchoring
        and gating it, and dropping `fp= sp= pc=`.

      **No existing test changed outcome.** The full suite ran under `-race`: 997 results on
      main, 1,027 on the branch. All 30 new results pass.

      **What a user sees.** A slog or logrus service's lines now show one per line in the
      stream pane and `--plain`. Each gets its own template and level, so a new ERROR from
      one can escalate on novelty + ERROR. README's #89 limitation is gone.

      Seen and left alone, out of scope:
      - #31: snapd's `WARNING:` after the `file.go:N:` prefix still gets no level;
      - #36;
      - the Python and Java cues.

      Found on the way and filed, not fixed. It was confirmed by a throwaway test that was
      not committed, and identically on main:
      - **#94:** Go dumps that write `goroutine N gp=… m=… [state]:`
        headers fragment. This covers `GOTRACEBACK=system` and `crash`, and every SIGQUIT
        dump. So do dumps that begin at the goroutine header, as `debug.Stack()` written on
        its own does. Measured on real Go 1.26 output: a system panic splits 87 lines into
        13 events, or 22 with the tabs stripped, and a SIGQUIT dump 116 lines into 38 or
        49. No capture contains one. It is in README "Known limitations". (Done with #94, in
        v0.12.18.) Targets v0.12.1

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

- [x] **A restarted container resumes after the last line read** — #80, found while fixing
      #72. On `die` the follower was cancelled. On `start` the same container ID was
      re-attached with `Tail: --docker-tail` and no `Since`. The json-file log survives a
      restart, so up to that many lines that were already counted arrived again within
      milliseconds. Measured on a daemon: five lines and one `docker restart` gave `x15` where
      10 were written. Computed, not observed: a template with an established rate can then
      cross the burst gate.

      **The daemon's read path, read in docker v28.5.2 rather than assumed.**
      - `since` is **inclusive**: the loggerutils forwarder skips a message only if it is
        `Before(since)`. The journald driver's `SeekRealtime` is inclusive too.
      - The forwarder stops filtering at the first message at or after `since`, so older lines
        can follow.
      - **Tail is applied before since**, to the combined stdout+stderr log.
      - Each stream is stamped by its own copier goroutine, so stamps are monotonic *within*
        a stream and not across the two.
      - The wire stamp always has nine fractional digits, so logscry has the exact nanosecond.

      **The fix (`internal/ingest/docker.go` only).**
      - Each follower returns a per-stream mark: the newest daemon stamp it delivered and how
        many lines it delivered at exactly that stamp. `Lines` keeps the mark per container ID.
      - It writes the mark in the same critical section that removes the follower, so `start`
        sees either a running follower or that follower's final mark.
      - A re-attach of a marked ID asks for `since` equal to the *earlier* stream's mark, with
        tail `all`. Each stream then drops lines stamped before its own mark, plus the first n
        stamped exactly at it.
      - The count exists because `since` is inclusive. Within a stream, the n lines already
        delivered are the first n at that stamp. A distinct line sharing the stamp is still
        delivered: a later one on the same stream, or one on the other stream.
      - The tail is `all` because a limit applied before `since` can only lose lines logged
        between the restart and the re-attach.
      - The mark advances only after a line is sent. A line cut off by the die is read again
        rather than lost.
      - A follower that delivered nothing leaves no mark, and the re-attach is a first attach.
        A mark carries forward across a silent restart.
      - No new lock on the per-line path: each stream goroutine owns its half of the mark, and
        the follower reads it after both have finished.
      - The first attach is unchanged.

      **Residuals.**
      - A daemon clock that steps backwards breaks per-stream monotonicity. Lines stamped
        before the mark are then dropped, as Docker's own `since` would drop them.
      - A line without a parseable stamp neither moves the mark nor is filtered. A real daemon
        never sends one.
      - Every re-attach makes the daemon decode the container's whole current log file. Only
        new lines cross the wire.
      - The marks map keeps one entry per container ID followed until logscry exits. Pruning
        would need `destroy`.
      - With `--docker-tail 0`, a container that delivered nothing before it restarted is
        re-attached like a first attach. The lines it logs between the restart and the
        re-attach are lost to re-attach latency. That is not history the user declined; it is
        kept because it is an edge of an edge.

      **Tests.** Red first. `docker_daemon_test.go` drives `Lines` end to end against a fake
      daemon: an httptest server that `DOCKER_HOST` points at, with no production seam. Its
      logs endpoint emulates the read path above and records each request's query, so the
      re-attach's `since` and `tail` are asserted as they reach the wire.
      - `TestDockerRestartDeliversEachLineOnce` (non-TTY and TTY) has #80's shape.
      - `TestDockerRestartBoundary`: two stdout lines at T, one stderr line just before it,
        then a new line at T on each stream.
      - `TestDockerRestartReadsPastTail`: `--docker-tail 3`, and five lines in the gap.
      - `TestDockerRestartCarriesTheInstantForward`: a silent restart, then another restart.
      - `TestDockerRestartAfterReadingNothing` passes on `main` too, and pins the fallback.
      - `TestReadDockerStreamMarksOnlyWhatItDelivered` was added with the fix, because it needs
        the new signature.
      - Three mutants were run, and the tests go red on each: a mark that advances before the
        send, dropping everything at the mark rather than the first n, and keeping the tail on
        a re-attach.
      - The fake re-sends `start` until the re-attach arrives, because of the race below.

      **No existing test changed outcome.** The full suite ran under `-race` on `main` and on
      the branch: 836 results, then 848. The only differences are the 12 new tests and
      subtests. The two existing `streamLogs` tests gained the mark argument, for compilation
      only.

      **Found on the way and filed, not fixed.** Both are in README "Known limitations", and
      both lose lines rather than duplicate them.
      - **#84:** stopping a non-TTY follower while a frame is mid-write into its
        stdout or stderr pipe leaves `stdcopy` blocked forever. Nothing closes the pipe's read
        end once its reader returns. So the follower never ends, and that container is never
        followed again. Confirmed by a throwaway test that was not committed: it did not return
        within 2 s in 3 of 3 runs, and the stack was in `io.(*pipe).write`.
      - **#85:** a `start` that arrives before the old follower has removed itself is
        ignored, and the restarted container is never followed. Confirmed against the fake
        daemon with `die` and `start` back to back: lost in 50 of 50 runs, and always followed
        with 200 ms between them. Not observed on a real daemon. Targets v0.10.4

- [x] **A follower stopped mid-write returns** — #84, found while fixing #80. A non-TTY
      container's stream is demuxed by `stdcopy.StdCopy` into two `io.Pipe`s, one per
      stream, each read by `readDockerStream`. Only the write ends were ever closed.

      **What went wrong.** A pipe write returns only once its reader has consumed every
      byte. When `die` cancelled the follower:
      - the reader blocked on a send returned, and left its read end open;
      - if the demuxer was writing into that pipe, the write never finished;
      - the other reader, parked waiting for input, ended only when the demuxer did.

      So `streamLogs` never returned, and the follower never stored its mark or removed
      itself. Every later `start` for that container was ignored for the rest of the run.
      The follower, its readers and the HTTP body leaked until exit. Exit itself was not
      blocked. TTY containers were unaffected, since they have no pipe.

      **The trigger is back-pressure, not frame size.** dockerd sends one frame per log
      message. Once the readers stop consuming, the demuxer's next write blocks whatever its
      size. A container that logs faster than logscry drains it, and then dies, is enough.
      The tests use one frame of 2,000 short lines only because it makes the hang certain:
      the readers hold at most about 4 KB of it. A single long line never hung, because the
      reader consumes a whole line before it sends.

      **The fix (`internal/ingest/docker.go` only).** Each reader closes its pipe's read end
      with its own result when it returns. On a cancel, things then unwind in order:
      1. The reader that stopped consuming returns `context.Canceled`.
      2. The demuxer's pending write fails with that error, and `StdCopy` returns it
         unchanged.
      3. Both write ends close.
      4. The other reader ends.
      5. `streamLogs` returns, and the follower stores its mark and removes itself.

      **The error.** The read end is closed with the reader's own result, so a cancel yields
      only cancellations. That is what a cancel with no write pending already returned, and
      `Lines` discards follower errors as before, so a `die` stays quiet. Closing with `nil`
      would have turned each such cancel into `io.ErrClosedPipe`.

      **EOF vs cancel.** A reader reaches EOF only after the demuxer has finished and closed
      both write ends. By then every byte of both streams has been consumed. So a reader
      closing its own pipe on EOF touches nothing still in use, and the other stream's
      remaining lines all arrive.

      **No reader returns early on its own error.** Every return path other than EOF and a
      cancel was checked:
      - `readLines` fails only when a pipe read fails. A pipe read fails only with the error
        its write end was closed with, and the demuxer closes both write ends at once when
        it finishes. So a demux error reaches both readers together. A throwaway test
        confirmed it: an unrecognized frame header surfaced three times and nothing hung.
      - An over-long line is no error, because `ReadString` grows its buffer. The same
        throwaway test delivered a 1 MiB line whole.
      - `readDockerStream` adds only the cancel at its send.

      **The mark (#80) is untouched.** It still advances only after a send, so the lines a
      stopped follower read but never delivered are sent again on the re-attach.

      **Tests.** Red first, each failing on an assertion within a bound and never hanging the
      run, in 5 of 5 runs:
      - `TestStreamLogsCancelIsQuiet`'s pending-write case fails at 2 s.
      - `TestDockerDieMidWriteIsFollowedAgain` fails at 5 s. Against the fake daemon, a
        3,000-line burst fills `out`, the container dies, then restarts. The test fails at
        once instead if the follower is never back-pressured.

      With the fix, these assert:
      - only cancellations are returned;
      - only the delivered line is marked;
      - the re-attach asks from the last delivered line's stamp;
      - every line arrives exactly once, in order.

      Passing on main too:
      - the cancel with no write pending;
      - `TestStreamLogsStdoutEndingFirstKeepsStderr`: two stdout frames, then 2,000 stderr
        frames, then EOF. Every line arrives and the end is a clean nil.

      **Flakiness and mutants.** Every new test passes 20 of 20 runs under `-race`. Four
      mutants each turn tests red:
      - closing with `nil`;
      - closing both read ends when either reader returns;
      - advancing the mark before the send;
      - no close at all.

      **No existing test changed outcome.** The full suite ran under `-race` on main and on
      the branch: 1,027 results, then 1,032. The only differences are the 5 new tests and
      subtests.

      **On a real daemon.** A manual A/B: a container whose burst outruns a stalled `--plain`
      is killed mid-burst, then started again 2 s later. The old binary printed NOT-FOLLOWED
      and the new one FOLLOWED.

      Seen and left alone, out of scope: #85, which shares this lifecycle. A `start` that
      arrives while the old follower is still unwinding is ignored as before. The difference
      is that the unwinding now ends. Targets v0.12.2

- [x] **A restart that lands during the old follower's unwind is followed** — #85, found
      while fixing #80. `Lines` handles a restart's `die` and its `start` back to back. On
      `die` it cancels the follower. The follower removes its own entry only once it has
      unwound, in the critical section that stores its mark. `start` returned early whenever
      an entry existed.

      So a `start` handled in between was dropped, and no later event re-sends it. The
      restarted container was never followed again, and its lines were lost for the rest of
      the run. Since #84 the old follower always exits, so the window was finite, but the
      loss was permanent. The fake daemon's `start()` used to re-send the event every 20 ms
      just to step around this.

      **The fix (`internal/ingest/docker.go` only).** The follower bookkeeping moves from
      `Lines`' closures into `followerSet`:
      - A `start` for a **stopped** follower, one `stop` was called on because a `die` was
        seen, is recorded on it. The latest start's name wins.
      - The exiting follower launches its successor in the same critical section that stores
        its mark and removes its entry.
      - `Lines` calls `start`, `stop` and `wait` where it called the closures and `wg.Wait`.

      The moved code keeps today's behaviour exactly, including what it never did (below). It
      is a type rather than closures so that a test can replace the follow function and hold
      a stopped follower in its unwind for as long as a case needs. A real follower's unwind
      cannot be steered from outside.

      **Decisions.**
      1. **Only a stopped follower takes a pending start.** A start for a running one is
         ignored, as before. That covers a duplicate event, or the overlap between
         subscribing and listing at startup. Events arrive in order, so a restart's `die` is
         always handled before its `start`.
      2. **The successor starts from the mark the old follower stores on exit.** Nothing is
         captured when the `start` arrives, so every line arrives exactly once across the
         restart.
      3. **`die`, `start`, `die`, all before the old follower exits.**
         - The pending start is kept. The second `stop` finds the old, already-cancelled
           follower and changes nothing.
         - The successor attaches to a container that is stopped again. dockerd v28.5.2
           creates a logger just for that read, so `follow` is false: it sends the history
           since the mark and ends the stream (`daemon/logs.go`, `getLogger`).
         - So the lines the container logged between the two dies are delivered once, and
           the successor exits on its own.
         - Dropping the pending start would leave those lines unread until some later
           `start`.
      4. **Shutdown.**
         - A successor is launched only while `Lines`' ctx is live, checked under the lock.
           If the cancel lands just after the check, the successor's context is already
           cancelled and it returns at once.
         - Its `wg.Add` comes before the exiting follower's `Done`, so the count never
           reaches zero in between, and a `wait` in progress covers the successor.
      5. **The successor takes the start event's name**, so a container renamed across its
         restart is sourced by the new name.

      **Tests.** Red first:
      - `TestDockerDieAndStartBackToBack` (TTY and not) sends `die` and `start` exactly once
        each. On main it failed at the 5 s bound waiting for the re-attach, in 40 of 40
        runs.
      - `TestDockerDuplicateStartIsIgnored` passes on main and pins decision 1 end to end.

      Added with the fix, because they need the new type:
      - `docker_followers_test.go` holds one test per decision.
      - Each drives `followerSet` with a follow that blocks until the test releases it.

      **The harness.** `fakeDaemon.start()` now sends one start event, then waits for the
      re-attach with its 5 s bound. Every existing docker test passes with it.

      **Flakiness and mutants.** Every new test passes 20 of 20 runs under `-race`. Five
      mutants each turn tests red:
      - a pending start accepted for a running follower turns both decision-1 tests red;
      - a successor started from the mark that existed when the start arrived turns red the
        back-to-back test (TTY and not), decisions 2 and 3, and one run of
        `TestDockerRestartBoundary`;
      - a successor launched after cancel turns the shutdown test red;
      - dropping the pending start on the second `die` turns decision 3 red;
      - launching with the old name turns decisions 3 and 5 red.

      **No existing test changed outcome.** The full suite ran under `-race` on main and on
      the branch: 1,032 results, then 1,047. The only differences are the 15 new tests and
      subtests.

      **On a real daemon.** This was a regression check, not the proof: 50 back-to-back
      `docker kill` and `docker start` rounds per binary, counting the restarts followed. Old
      binary: followed 50 of 50. New binary: followed 50 of 50. So the rewritten restart path
      follows every real restart, and the real daemon never reached the window. The window is
      the old follower's unwind, well under the time `docker start` takes. The fake-daemon
      test is the evidence for the fix.

      Seen and left alone, out of scope:
      - **The `fctx` of a follower that ends on its own is never cancelled.** When its stream
        reaches EOF before the `die`, `stop` later finds no entry. The context stays
        registered on `Lines`' until exit. A throwaway test measured about 116 bytes retained
        per such run, freed when the parent was cancelled. It is there since v0.1.0, and the
        moved code keeps it. Filed as #98 (Done with #98, in v0.12.17.).
      - **An events-stream error hangs `Lines` instead of returning.** It waits for its
        followers without cancelling them. A throwaway test against the fake daemon ended
        the events stream with a follower attached:
        - `Lines` had not returned 3 s later, and the follower kept delivering lines;
        - only the cancel returned `docker: events: EOF`, which `main` drops on a cancelled
          ctx;
        - meanwhile, starts and restarts go unseen.

        It is there since v0.1.0. Filed as #97. Targets v0.12.3

- [x] **An events-stream error is reported, and so is any source error beside a live one** —
      #97, found while working on #85. Two layers hid it.
      - **`DockerSource.Lines`:** on an events error with its ctx alive, it waited for its
        followers without stopping them. Followers ran under the caller's ctx, which only
        shutdown cancels, so the wait lasted until every followed container stopped.
        Meanwhile no start or die was seen, so new and restarted containers were not followed.
      - **`ingest.Run`:** it returned the sources' errors only once every source had ended,
        and `main` reported them only then, and not at all once ctx was cancelled.
        journald ends only on Ctrl-C, and so does a healthy Docker source. So **every** source
        error beside a live source stayed unseen for the whole run and was dropped on Ctrl-C:
        - a Docker client or `docker: list` error, or a bad `--docker-name` regex (config never
          compiles it);
        - journalctl missing, denied, or exiting;
        - a subprocess that fails to start or exits non-zero.

        Fixing only the first layer would have changed nothing the user sees beside journald.
        Confirmed on the old binary: `DOCKER_HOST=tcp://127.0.0.1:1 --docker-all -- sleep 5` and
        `--docker-name=( -- sleep 5` each printed their error only after 5 s, when `sleep`
        ended.

      **Decisions.**
      1. **On an events error the Docker source ends.** It cancels its followers, waits for
         them, and returns `docker: events: …`. That is a trade, not a free win. The
         containers already being followed stop streaming too. The error is shown at once.
         To follow Docker again you restart logscry, which reads each container's
         `--docker-tail` again on first attach.

         Reconnecting was costed and not built:
         - Re-subscribing with `since=<last event>` replays the gap from the daemon's event
           buffer. A re-list catches what that misses, and the marks make each re-attach
           exact.
         - Still lost: containers that start and die in a gap longer than that buffer.
         - It needs backoff and a give-up rule.
         - Above all, it needs a way for a source to say "reconnecting" without ending. A
           Source can only report by returning, so retrying would be silent, and silence was
           this issue's failure.

         Filed as #100.
      2. **A source's error is reported the moment that source ends, and the others keep
         running.** The run ends when every source has ended, as before. Ending the run on
         the first error was rejected for two reasons:
         - A subprocess's non-zero exit is a source error, and expected for a crashing target.
           Ending the run would stop following the journal or the containers at the moment
           `-- ./app` crashes.
         - A dropped events connection says nothing about journald's health.

         `Run` takes a `report func(error)`, called from the failing source's goroutine
         before `Run` returns, and never for what a cancel stopped. `main` sends each report
         into the error channel that `--plain` prints to stderr and the TUI shows in its
         status bar. That is the only path: `Run`'s joined return is read only to skip the
         replay notice after a failed replay, so nothing is reported twice. The channel holds
         one error per source plus the two notices, so no sender blocks. **The exit status
         stays 0**: a source's outcome, a watched app's crash included, is reported, not
         exit-coded. What the exit status should mean is the question #70 also raises.
         (Done with #70, in v0.12.19.)
      3. **Shutdown is unchanged.** On Ctrl-C nothing is reported. `Run` returns only after
         every source has. A pending re-attach (#85) is not launched once the source is
         ending, by the same check that stops it at shutdown. The marks are untouched.

      **Found on the way and fixed here: plain mode could lose an error at the end of a run.**
      `runPlain`'s loop left as soon as the events channel closed. An error still waiting in
      the error channel then lost a coin toss in the `select`. Per-source reports made that
      reachable: two sources failing together as the run ended lost one error in 1 of 50 runs
      (`--docker-all` with an unreachable daemon beside `-- sh -c 'exit 3'`). The old binary
      showed both in 50 of 50, as one joined message.

      `run` closes the error channel before the lines channel, so when events closes every
      error is already buffered. The loop now prints what is pending before it goes on. To
      pin that without timing, the loop is now `printPlain`, called by `runPlainAt` with the
      same behaviour.

      The old single send raced the same `select`. In principle it could be lost too: if the
      loop was still busy with the last event when the error arrived and events closed, both
      were ready at its next `select`. It never was in 50 runs of 20,000 lines then a failure.

      **Tests.** Red first, each failing on an assertion within its bound and never hanging
      the run, in 5 of 5 runs:
      - `TestDockerEventsErrorEndsTheSource` (TTY and not). The fake daemon's new
        `endEvents()` ends `/events` while a container's log stream stays up. Lines must
        return the error wrapping `io.EOF` within 2 s, and the follower must hang up. On
        main: "Lines had not returned 2s after the events stream ended".
      - `TestPlainReportsASourceErrorWhileOthersRun` runs `run` in `--plain`, with a fake
        `journalctl` on `PATH` that never exits beside `-- sh -c 'exit 3'`. The error must be
        on stderr within 2 s while the run goes on. After the cancel, stderr must hold exactly
        one `ingest:` line. On main it fails at the 2 s bound.

      Passing on main, pinning decision 2: `TestPlainSourceErrorExitStatus`. The error is
      printed exactly once, and `run` returns nil, so the exit status is 0.

      Added with the fix, because they need the new `report` parameter or `printPlain`:
      - `TestRunReportsAFailureWhileOthersRun`: a failure is reported within 2 s, `Run` has
        not returned, and the other source still delivers.
      - `TestRunReportsNothingOnCancel`.
      - `TestPlainPrintsEveryErrorAtTheEnd`: events already closed, and two errors waiting in
        a closed channel; each is printed exactly once. Without the drain it fails in 37 of
        50 runs.

      **Flakiness and mutants.** Every new test passes 20 of 20 runs under `-race`. Seven
      mutants each turn tests red:
      - the events branch without the cancel (the old code) turns the docker test red;
      - followers under the caller's ctx instead of Lines' own turns the docker test red;
      - reporting after `wg.Wait` turns both while-others-run tests red;
      - reporting without the ctx filter turns both Run tests red;
      - `main` without the report turns both `--plain` `run` tests red;
      - `main` also sending `Run`'s return turns the exit-status test red, in 50 of 50 runs.
        Before the drain it was caught in only about half of them;
      - no drain on events close turns the end-of-run test red.

      Two mutants stay green, so those two lines are unpinned:
      - **Cancel without the wait.** A cancelled follower exits within microseconds, so the
        window in which it could outlive `Lines` is out of a test's reach. The docker test
        closes `out` after Lines returns, so a straggler would panic sending on it; none did.
      - **No ctx check in the events branch.** On a cancel, `ctx.Done()` is ready before the
        events goroutine forwards `ctx.Err()`, so the select takes the shutdown branch. Over
        20 runs no existing `stop()` saw the error.

      **No existing test changed outcome.** The full suite ran under `-race` on main and on
      the branch: 1,047 results, then 1,055. The only differences are the 8 new tests and
      subtests. The three existing `Run` calls gained a nil `report`, for compilation only.

      **On a real daemon.** A manual A/B against a real daemon: a proxy in front of the socket
      cut only the events connection while an `alpine` container that logs twice a second was
      followed. The proxy cut exactly one events connection in every run. Each binary ran
      Docker alone, and Docker beside a subprocess printing a heartbeat.
      - **Old binary.** No error after the break, and the container's lines kept flowing, so
        the run looked healthy. A restart of the container was not followed. Alone, the
        error appeared only once the restart ended the followed stream. Beside the
        heartbeat, it never appeared.
      - **New binary.** The error at once, `logscry: ingest: docker: events: unexpected EOF`,
        and the container's lines stopped. Alone, logscry then exited, since its only source
        had ended. Beside the heartbeat, the heartbeat kept running.
      - Every run exited 0.

      A real break reads as `unexpected EOF`, where the fake daemon's clean end reads as
      `EOF`. Both arrive through the same events branch.

      Seen and left alone, out of scope:
      - **#98 is unchanged.** A follower's context is now a child of Lines' own. It is freed
        when the Docker source ends, but it still accumulates during the run.
      - **#70 is unchanged**, and the exit status stays 0 there too. (Done with #70, in
        v0.12.19.)
      - **A Docker list error leaves the events subscription open until exit.** Lines
        subscribes to events before listing. When the list fails, it returns, but the events
        goroutine and its connection stay until the run's ctx is cancelled, because
        `cli.Close` closes only idle connections. A throwaway test against a fake daemon
        whose list answers 500 confirmed it in 3 of 3 runs: still open 2 s after Lines
        returned, closed on cancel. It has no functional effect, and needs an events request
        that succeeds beside a list that fails. Filed under #98, which now covers both
        (Done with #98, in v0.12.17.).
      - **The TUI status bar shows only the newest error**, so a later one hides the Docker
        error. That is by design.

      Targets v0.12.4

- [x] **An underscore in a hostname is masked across it** — #66, found while sweeping #61 and
      pinned there as two open assertions. `--llm-anonymize` had no `_` in any host class, so
      every host rule stopped at the first one and sent the rest to the configured model
      endpoint:
      - `http://my_host:8080/x` → `http://<HOST_1>_host:8080/x`;
      - `postgres://u:p@db_1.acme.com/app` sent `_1.acme.com`, the whole domain;
      - `mongodb://host1,host_2/db` sent `_2` (9c);
      - `peer db_primary.corp.internal down` → `peer db_primary.<HOST_1> down` (9b's `\b` cannot
        start after a `_`);
      - `http://_svc.acme.com/x` was sent whole, since 9a's group cannot start on a `_`;
      - the email rule had the same gap in its domain class, so `user@my_host.example.com` and an
        ssh-style `deploy@build_01.example.com` matched nothing and were sent whole.

      The Template field behaved identically. `Mask` returned nil and `residue()` returned `""`:
      9a, 9b and 9c are `verify:false`, and no declared secret in the completeness table carried a
      `_`. v0.4.0 → v0.12.4, **thirty tagged releases** (`git tag --contains 2da31be | wc -l`
      → 30).

      **The cause is #61's fifth kind, its second instance.** The host class was smaller than the
      production: RFC 3986 puts `_` in `unreserved`, legal anywhere in a reg-name, and logs carry
      it in container names, Kafka brokers and internal hosts.

      **One correction to the issue as filed.** It said `env_db.corp.internal` masks
      `db.corp.internal` today. It masked `corp.internal` and sent `env_db.`, because no `\b`
      precedes `db` there either. Measured on main, as were the other shapes.

      **The fix (`internal/anonymize/anonymize.go` only).** Four classes widen by one character
      each: `hostRun` (9a, and 9c's group and first-member context), 9b's `label` and `inner`, and
      the email domain class.

      **`listEnd` deliberately does not widen, and that was found while planning.** It is the one
      place where admitting `_` would narrow. 9c's group can stop at a terminator only where the
      full member fails, which is a member in scheme position. On `mongodb://h1,h2_x://y`, `_`
      as a terminator lets the group fall back to `h2` and mask it, as before. As a host character
      no shorter member ends legally, 9c declines, and `h2` is sent. The mutant measured it:
      `mongodb://<HOST_1>,h2_x://y`. It is pinned as a byte-identical row.

      **Decisions.**
      1. **Nothing narrows, proven differentially.** The corpus was collected with an uncommitted
         hook in `Mask` and `residue`, from every input the anonymize and llm tests feed them,
         plus the raw and templatized form of every templatized row: 597 distinct inputs. Old
         and new masking were compared position by position through `byToken`:
         - 41 outputs changed;
         - **0 narrowed**, 0 renumbered only, 0 changed whether `Mask` errors;
         - every newly masked run contains a `_`;
         - the only pre-existing inputs among the 41 are the two open assertions; nothing else
           moved.
      2. **A label may begin and end with `_`.** DNS service labels begin with one (`_dmarc`,
         `_sip._tcp`), and refusing it would keep sending it. Refusing a trailing one buys nothing.
         The hyphen rule is unchanged.
      3. **The over-masks are four, enumerated in `TestAcceptedOverMasking`.** An over-mask from
         `_` needs a `_` to enter a group it used to end:
         - 9b's `\b` now starts before a word run glued to a host by `_`. That covers an
           identifier prefix (`env_db.corp.internal`) and a snake_case file name on a private
           suffix (`app_settings.local`, untouched before);
         - a leading-`_` label that is not a host is one to 9b: an mDNS service type,
           `_ipp._tcp.local`;
         - 9a's and 9c's group can end on a trailing `_` (`http://acme.com_`);
         - a systemd template unit whose instance carries a `_` reads as an address
           (`worker@job_runner.service`).

         **The plan said three, and the capture run found the fourth.** See the frequency
         paragraph below.
      4. **The email domain class widens, with its own rows.** Its gap sent the whole value, user
         included. A URL's userinfo is detector 4's before email runs, so the widening reaches
         bare address shapes only. Email is verify-eligible, so a `_`-domain address that ever
         survives masking now mutes the escalation: `residue` returns `EMAIL` where it returned
         `""`.
      5. **Both paths.** Templatized rows go through the real `Templatize`, where `my_host_2`
         becomes `my_host_<NUM>`, and through the logfmt wrapper. Completeness rows sit after
         the canonical ones, so `completenessSecretFor` is unchanged.

      **The systemd template unit was already an address, and is pinned now for the first
      time.** `unit@instance.service` matches the email rule whenever the instance fits the domain
      class: `backup@nightly.service` → `<EMAIL_1>` on main and before. Nothing tested it and nothing
      wrote it down. It is an **accepted over-mask that costs triage context**: the model is told
      an address started, not which unit did. It is recorded, not fixed and not filed here. #66's
      cell only extends it to a `_` instance.

      **Frequency, measured.** Run over the ten boot captures and the ab37 capture on this laptop
      (70,783 records), raw and through `Templatize`, old against new, counts only.

      **Prediction, recorded before the run:** under 100 records, nearly all 9b on `.local`; 9a
      0–5; email 0. **It did not hold.** Results:
      - **On this laptop's captures #66 changed zero host masks.** 9a, 9b and 9c changed
        nothing.
      - **Its real-world effect here was the email cell only:** 378 records (0.53%), raw and
        template alike.
      - All 378 were newly masked text, sent in the clear before.
      - None of it was an address or a host: three distinct systemd template unit names, the
        fourth over-mask cell above.
      - Nothing narrowed, and nothing was renumbered only.

      So the leak this fixes did not occur in this corpus, and the cost it adds did. Nothing from
      the captures is in the repo: every test value is invented, and the tests were committed
      before the captures were opened.

      **Tests.** Red first:
      - `TestIssue66UnderscoreInHostname`, with exact outputs, a check that no fragment the old
        output sent survives, and a round trip. It carries:
        - the issue's shapes, the decision-2 labels, and the email shapes;
        - five Template-field rows;
        - six byte-identical rows: `THE_HOST_IS db.corp.internal`, the scheme-position member,
          no scheme, a bare public host, and two identifiers;
        - the residue assertion.
      - Five completeness rows, four templatized rows (eight more through the logfmt wrapper),
        four over-mask rows, and the old leftovers fed back to `TestPlaceholdersAreInert`.
      - Two completeness rows also turned `TestNoDetectorGroupLandsOnOurOwnOutput` red on main,
        because masking produced no placeholder at all: that was the leak.

      Main failed 45 result lines in 5 of 5 runs. The checks are deterministic. One fragment check
      was wrong as first written: `_2` occurs inside `<HOST_2>`. It was corrected in the red
      commit before the fix.

      A later commit adds the two systemd-unit rows and the ssh-style row. On old code the
      `_` rows fail and the plain-unit row passes, because that over-mask is inherited.

      **Flakiness and mutants.** Every new and touched test passes 20 of 20 runs under `-race`.
      Seven mutants each turn tests red:
      - `_` only in `hostRun` turns the 9b rows red;
      - `_` only in 9b turns the 9a/9c rows red;
      - a leading `_` refused turns `_dmarc`, `_sip._tcp` and the mDNS row red;
      - a trailing `_` refused turns `db_.corp.internal` red;
      - the email domain not widened turns the email rows red;
      - `listEnd` admitting `_` turns the scheme-position row red, the narrowing;
      - `_` only in 9b's `inner` turns the leading and trailing rows red.

      **No existing test changed outcome.** The full suite ran under `-race` on main and on the
      branch: 1,055 results, then 1,102. The two open #66 assertions left
      `TestIssue61MultiHostAuthority`, and 49 new results came in.

      **Wire check.** One run per binary against a capturing endpoint (`--plain --llm-anonymize
      --warmup-lines 0`). A `[FATAL]` line carried `http://my_host:8080/x`, with #57's
      `s3://AKIA…EXAMPLE.prod@x@bucket` in its context. `_host`: 2 before (trigger line and
      template), 0 after. `.prod` fires in both, so the grep is not vacuous.

      **`residue`.** The host rules stay `verify:false`, so there is no new proviso on the README's
      re-scan paragraph from them. Email is the one verify-eligible rule that widened.

      `internal/pipeline` does not appear in the diff. README:
      - the Known-limitations bullet goes;
      - the #61 paragraph's pointer and the audit sentence move to past tense;
      - a new past-tense paragraph carries the shapes, span, costs and the measured frequency.

      The pre-v0.9.2 table row `svc_user@10.0.0.5` sent `_user` is this class, the host rule's
      group stopping at a `_`. It is left as it is: it describes those releases truthfully, and
      since #55 detector 4 masks the whole userinfo first, so the shape has been fully masked
      since `v0.9.2` (`TestIssue55PasswordlessUserinfo`).

      **Found on the way, not fixed: a `_` glued before a scheme hides the URL from 9a.**
      `x_https://acme.com/x` and `__https://acme.com__` send the host on the raw line. 9a's anchor
      needs a `\b` before the scheme, and `_` is a word character. Detector 4 has no anchor, so a
      credential there is still masked. Confirmed with a throwaway test.

      It is the scheme anchor, not the host class, so it is #52's class: the anchor fails to
      recognise a URL, there because of a digit and here because of a `_`. It is offered as a
      comment on #52 and named under #52 in the README audit list. Seen, out of scope: #65's `~`,
      sub-delim and pct-encoded cells; #67; #54. Targets v0.12.5

- [x] **A tag on a later member of a host list no longer ends the list** — #67, filed by #61 as
      a class with four pinned open assertions, one per tag that can reach a later member. Since
      #61, `--llm-anonymize` masked a multi-host authority as two placeholders: the first member,
      and hosts 2..n as one group (9c). That group excludes `<`, the inertness invariant every
      target group carries. So a tag minted on member k ≥ 2 before 9c ran ended the group there,
      and every member after it went to the configured model endpoint:
      - `mongodb://host1:27017,10.0.0.5:27017,host3:27017/db` sent `host3`;
      - `mongodb://h1,2001:db8::1,host3/db` and `mongodb://h1,[2001:db8::1]:27017,host3:27017/db`
        sent `host3`;
      - `mongodb://h1,AKIAIOSFODNN7EXAMPLE,host3/db` sent `host3` (theoretical: a member that is a
        key);
      - `mongodb://10.0.0.5,10.0.0.6,host3/db` sent `host3`: two addresses first.

      `Mask` returned nil and `residue()` returned `""`: 9c is `verify:false`, and the leftover is not
      a shape it matches on its own. `v0.9.4` → `v0.12.5`; before `v0.9.4` every member after the
      first was sent regardless, so the class is a remainder of #61 and strictly narrower than what
      it replaced.

      **One correction to the issue as filed.** It said the class was live on the raw path only,
      because on the Template field an address collapses to the pipeline's `<IP>`, which the
      tolerant host run steps over. That holds for an unbracketed address. A bracketed one becomes
      `[<IP>]` and a key `<TOKEN_n>`, and 9c could start on neither. So
      `mongodb://h<NUM>,[<IP>]:<NUM>,host<NUM>:<NUM>/db` and the AKIA row sent `host<NUM>` on the
      Template field too. Measured on main.

      **The fix (`internal/anonymize/anonymize.go` only) is the issue's option 2.** 9c's group is one
      member. Its context steps over every member already tagged, a `taggedMember` shared with the
      first-member context, whichever rule minted the tag. `Mask` re-applies 9c until a pass
      changes nothing. Each pass takes the first raw member after a run of tags, so it re-anchors on
      the last tag in the list, 8's or 2's as readily as its own. The example now comes back
      `<HOST_1>:27017,<IP_1>:27017,<HOST_2>:27017/db`. #54's pre-pass was not needed: here the inner
      tag is right, and the remedy is to re-anchor on it rather than undo it.

      **Decisions.**
      1. **Termination, argued and pinned.** A changing pass masks a raw run directly after a comma,
         and that comma stays followed by `<` for good: no rule rewrites a tag, and neither a tag nor
         the run it replaced contains a comma. So changing passes are bounded by the commas, which per
         list is the members after the first. One pass masks the next member of **every** list on
         the line, so the bound is the longest list, not the sum. Two 65-member lists on one line
         settle in 64 passes, and a 40-member list beside a CSV tail masks as each would alone (both
         pinned).
      2. **A hard cap of 64 changing passes, and it fails closed.** It is reachable on purpose. Each
         pass rescans the whole line, so an unbounded loop is O(cells × length). A path-less URL
         followed by 4,200 comma cells (8 KB) took **7.9 s** per `Mask` uncapped, against 4.8 ms on
         main, and anyone who can write a log line can produce that. 64 is above MongoDB's 50-member
         replica-set limit.
         - At the cap, `Mask` returns no string and the error `anonymize: a host list with more than
           64 members after the first was not masked`.
         - Measured with the cap, main against new: a 3-member replica set 127 µs → 121 µs; a 64 KB
           line with 64 cells 62 ms → 220 ms; the 8 KB line 4.8 ms → 27 ms; a 64 KB line of 33k
           cells 38 ms → 223 ms. The gate was 1 s or 20× main, and the worst case is 5.8×.
      3. **The change is local to 9c.**
         - `apply` is unchanged and still one pass.
         - `detector` gains a `fixpoint` flag, and exactly one rule, 9c, carries it in both the
           default set and with extra suffixes (pinned).
         - Old and new chains with 9c removed are byte-identical on every differential input.
      4. **One placeholder per member, deliberately.** The three-member rows of the #61 battery, three
         list completeness rows (now one marked span per member), two templatized rows (×3 with the
         logfmt wrapper) and the CSV over-mask row were rewritten.
         - The interior-ports over-mask, #61's first named cost, is retired, since every port is
           visible now; its row moved into the new test.
         - The gain is pinned: a list member alone in a later line gets its list placeholder, where
           before the list's tail was one value and the member minted a new one.
      5. **The path-less-URL-then-CSV over-mask** covers exactly the characters it covered before. Each
         cell is now its own placeholder, so equal cells share one, and a cell with a character
         outside the host class ends the run where it always did. Past 64 cells the escalation is
         skipped (decision 2).

      **Why fail closed at the cap, and not main's shape (the rest of the list as one placeholder).**
      Main's shape is the defect. A group spanning several members still cannot cross a tag, so
      falling back to it would leak every member after a tag on exactly the lists that hit the cap,
      #67's class restored where it is hardest to see. Making the fallback cross tags would put a
      target group on our own output, which the inertness invariant forbids: a nested mapping, and
      a `residue` that can fire on its own output. It would also be a second masking path that runs
      only on pathological input, which no real log exercises and so nobody would notice rotting. The
      package already has a rule for "cannot mask this confidently": don't send it, which is what a
      `residue` hit does. The cost is one skipped explanation on a line with more than 64 members
      after the first. No capture line came near it.

      **How a cap-hit reaches the user: exactly as a `residue` mute does.** Both are `Mask` errors.
      `maskErr` marks them fatal, and the escalation becomes an `ExplainFailed` with the reason. That
      reason shows on the card ("explanation unavailable: anonymization failed, escalation skipped:
      …"), in the status bar's `✗` counter, in the export record (`state: unavailable` with the
      `error`), and on the `--plain` line. No channel was added, and `residue` mutes are not silent,
      so there is no gap to record. `TestHostListCapFailsClosedLikeResidue` pins the road with the
      real anonymizer.

      **No narrowing, shown differentially.** The corpus came from uncommitted hooks in `Mask` and
      `residue`, run under the anonymize and llm tests of both main and the branch, plus the
      templatized form of every input: 1,750 distinct inputs. Old and new masking were compared by
      per-byte coverage bitmaps rebuilt from `byToken`. A newly uncovered run that is exactly a
      member separator (`(:port)?,+`, port digits possibly `<NUM>`) counts as layout, since it was
      inside one placeholder before. Results:
      - **0 narrowed**, 0 mixed;
      - 1,301 unchanged;
      - 365 layout only: lists of three or more members;
      - 73 widened: later-member tags;
      - 4 the bitmap cannot rebuild, because the input already carries a literal placeholder. They
        are the old leftovers fed to `residue`, and all four widen (`host3` newly masked);
      - 7 changed whether `Mask` errors, all of them the cap tests' own inputs;
      - 9c-removed identity on all 1,750.

      Restricted to what main's own tests feed (884): 846 unchanged, 28 layout, 6 widened (the four
      open rows, plus the bracketed and AKIA Template rows), the 4 unrebuildable, 0 narrowed and
      0 error changes.

      **Frequency, measured.** The ten boot captures and the ab37 capture, 70,783 records, raw and
      through `Templatize`, old against new, counts only. **Prediction, recorded before the run:**
      0 changed records and 0 cap hits. **It held:** 0 and 0. As with #43 and #66, there is no
      multi-host authority in this corpus. Nothing from the captures is in the repo.

      **Tests.** Red first:
      - `TestIssue67LaterMemberTag` has exact outputs, every member absent, and a round trip:
        - the four former open rows;
        - two consecutive addresses, addresses alternating with names, two addresses first, and
          adjacent tags as a later member;
        - an eight-member list, the retired interior-ports row, and two lists on one line;
        - four Template-field rows through the real `Templatize`;
        - address-last and address-first rows, byte-identical on main;
        - the recurrence row, and the `residue` claim.
      - The layout rewrites above, and 9c's output fed back to `TestPlaceholdersAreInert`.

      Main failed 33 subtests, all by assertion. Main's own test file run against the new code fails
      exactly the decision-4 rows and the four open rows, and nothing else. After the fix:
      - `TestHostListFixpoint`: one rule iterates; lists of 2 to 65 members terminate below the cap
        in three shapes; the cap counts the longest list; a list beside a CSV tail; past the cap,
        `Mask` fails closed with a reason naming the host list.
      - `TestHostListCapFailsClosedLikeResidue` in `internal/llm`.

      These two use the new flag and cap, so they come after the fix rather than in the red commit.

      **Flakiness and mutants.** Every new and touched test passes 20 of 20 runs under `-race`. Eight
      mutants each turn tests red:
      - no iteration turns 28 subtests and the llm test red: every multi-member and later-tag row,
        the list completeness and templatized rows, the CSV rows and the cap rows;
      - main's multi-member group, iterated, turns 27 subtests and the llm test red;
      - re-anchoring only on `<HOST_n>` turns 11 red: every later-tag row, raw and template, and the
        address-alternating termination row;
      - the cap returning what it has ("send the rest") turns the fail-closed row and the llm test
        red;
      - the cap off by one turns the 65-member termination rows and the longest-list row red;
      - no bracketed later member turns the bracketed rows red, raw and template;
      - the group without `listEnd` turns the comma-separated-URL and scheme-position rows red;
      - a single tag instead of adjacent tags turns both adjacent-tag rows red.

      Main's 9c itself is the red commit.

      **The full suite** ran under `-race` on main and on the branch: 1,102 results, then 1,127.
      - **No existing result changed outcome.**
      - Five left: the four open #67 rows, and the interior-ports row, which moved.
      - 30 new results came in.

      **Wire check.** One run per binary against a capturing endpoint (`--plain --llm-anonymize
      --warmup-lines 0`). A `[FATAL]` line carried
      `mongodb://host1:27017,10.0.0.5:27017,host3:27017/db`, with #57's
      `s3://AKIA…EXAMPLE.prod@x@bucket` in its context.
      - `host3`: 1 before (trigger line), 0 after.
      - `.prod` fires in both, so the grep is not vacuous.
      - On the wire, the new trigger reads `<HOST_1>:27017,<IP_1>:27017,<HOST_2>:27017/db`.

      **`residue`.** Nothing new: 9c stays `verify:false`, and the old leftover still reads as clean
      (pinned).

      `internal/pipeline` does not appear in the diff. README:
      - the #67 audit bullet goes;
      - the audit sentence and the #61 paragraph's layout and #67 sentences move to past tense;
      - the "two placeholders" cost sentence moves to past tense;
      - a new past-tense paragraph carries the shapes, span, costs and the measured frequency.

      **Found on the way, not fixed: a MIXED later member still ends the list.**
      `mongodb://h1,10.0.0.5.nip.io,host3/db` sends `.nip.io` and `host3`, because by the time 9c runs
      the member is `<IP_1>.nip.io`, a tag with text glued to it, and that is neither a tag nor a host
      run. Confirmed with a throwaway test. A #57-shaped key (`h1,AKIA…EXAMPLE.prod,host3`) does the
      same, and so does an adjacent-tags member on the Template field, where a `<NUM>` follows the
      tags. It is #54's class, an inner tag that is wrong because it sits inside a host, so it is
      offered as a comment on #54 and named under #54 in the README audit list; #54's pre-pass would
      close it. Seen, out of scope: #54's own cells, #57, #59 with a list, #65. Targets v0.12.6

- [x] **An IPv4 address inside a hostname is masked as part of the host** — #54, the half of #48
      that ordering could not reach, filed by #48 with the impossibility as a result. Detector 8
      tagged an address on its own before any host rule ran, and every host class excludes `<`, so
      an address that was a LABEL inside a name blocked the host rules and the rest of the name went
      to the configured model endpoint:
      - `https://10.0.0.5.nip.io/x` → `https://<IP_1>.nip.io/x`;
      - `worker-10.0.0.5.corp.internal` → `worker-<IP_1>.<HOST_1>`;
      - `https://api.10.0.0.5.nip.io/x` → `https://<HOST_1><IP_1>.nip.io/x`;
      - `peer db.10.0.0.5.corp.internal down` sent `db.`;
      - `mongodb://h1,10.0.0.5.nip.io,host3/db` sent `.nip.io` and `host3` (the issue's comment, from
        #67's sweep).

      `Mask` returned nil and `residue()` returned `""`: the host rules are `verify:false`, and the
      leftover is not a shape any of them matches. v0.4.0 → v0.12.6, **thirty-two tagged releases**
      (`git tag --contains 2da31be | wc -l` → 32).

      **One correction to the issue as filed.** It treated the class as raw-path only, as #48's was.
      On the Template field the address is the pipeline's `<IP>`, which the tolerant host runs step
      over in a URL and on a private suffix, but a bare name on a public suffix is no host rule's
      business: `resolve <IP>.nip.io failed` and `peer api.<IP>.nip.io down` sent the labels around
      `<IP>`. Measured on main.

      **The mechanism, chosen from the cells.** Two were compared before any code, and neither closed
      every cell as stated:
      - **The issue's pre-pass** defines a bare authority as `(label\.)+private-suffix`, so `resolve
        10.0.0.5.nip.io failed` is not one and stays open on both paths. Making it one is the rule
        below with a span machinery around it: spans carried through every `apply` while the string
        is rewritten, the userinfo kept outside them, and `residue` made span-aware.
      - **A post-pass gluing our `<IP_n>` to its neighbours** closes every raw cell and misses the
        Template ones, where the address is not our tag. It would also put a target group on our own
        output, mint from `Restore(run)` to avoid a nested mapping, and orphan `<IP_1>`, leaving a
        numbering gap. Given the raw-address branch that `completenessSecretFor` needs anyway, its
        tag-reading half closes nothing more: 7 is the only other rule that mints an IP tag, and `:`
        ends a host.

      **The fix (`internal/anonymize/anonymize.go` only) is the glue read on raw text one step
      earlier: a new host rule, 9d, between 7 and 8.** It matches an address, 8's own pattern or the
      pipeline's `<IP>`, joined to hostname labels across `.` or `-`. Each side starts and ends on a
      label character, at least one side carries a letter outside the address, and the run sits
      between non-label characters as context. It is not a reordering, so #48's result stands: 9d is
      more specific than 8 on exactly the span they share and absent everywhere else, and every
      address 8 masked whole before, it still does. `apply`, `residue`, `Restore` and 9c are
      unchanged; 8 and 9d share `ipv4Pattern`.

      **Decisions.**
      1. **Placement after 4, 4b and 4c, measured.** `authorityTail` excludes `<`, so with 9d first,
         4 declined on `postgres://u:p@10.0.0.5.nip.io/db` and only the password was masked.
      2. **The tag is HOST and the value is the whole name.** Restore gives back `10.0.0.5.nip.io`;
         the name recurs under one placeholder across a request, as a list member and in a URL; the
         address alone elsewhere is a different value with its own `<IP_n>`. All pinned.
      3. **`residue` needs no change.** 9d is `verify:false` like every host rule. The old leftover
         still reads as clean, and a raw address left inside a name is still detector 8's and mutes
         the escalation; both are pinned.
      4. **9c is untouched and the cap still counts what #67 pinned.** 9d runs before 9a and 9c, so a
         fused member reaches 9c as one tag and the list re-anchors on it at no cost in passes. A new
         termination shape alternates `10.0.0.i.nip.io` with names up to 65 members. A fused member
         numbers before the members around it (`<HOST_2>,<HOST_1>,<HOST_3>`).
      5. **The letter rule.** A DNS name's top label is alphabetic, and nothing cheaper tells a name
         from an address beside a number, so `10.0.0.5.443` (BSD netstat's address.port),
         `10.0.0.5-20` and `10.0.0.5-10.0.0.9` stay `<IP_n>`. Pinned byte-identical, raw and
         templatized, with the address that is the authority, `:8080`, `/24`, a sentence dot, prose,
         #67's address-as-member and a partial octet.

      **What it costs, pinned in `TestAcceptedOverMasking`.** Text glued to an address by `.` or `-`
      that carries a letter is a host now. Each row was `<IP_1>` beside text in the clear and is one
      `<HOST_1>`: a file name (`dump-10.0.0.5.log`), a missing space after a sentence dot, a word
      before the address, a version with a pre-release tag (`1.2.3.4-beta`, `<IP_1>-beta` on main),
      and a reverse-DNS name. On the Template field, where the pipeline does not validate octets,
      `worker-10.0.0.256` becomes `worker-<IP>` and is masked whole too.

      **Pinned, not targeted.** An access key as a host label, the sweep's "theoretical" cell, keeps
      main's `https://<TOKEN_1>.prod.acme.com/x`; the same key as a later list member is #57's shape
      and still sends `.prod` and `host3`.

      **No narrowing, shown differentially.** The corpus came from uncommitted hooks in `Mask` and
      `residue`, carried by `-overlay` copies of the source so nothing was edited or stashed, and run
      under the anonymize and llm tests of both main and the branch. The templatized form of every
      input was added: 2,066 distinct inputs. Old and new masking were compared by per-byte coverage
      bitmaps rebuilt from `byToken`. Results:
      - **0 narrowed**, 0 mixed, 0 error changes, 0 unrebuildable;
      - 1,896 unchanged;
      - 104 widened: the new tests' inputs;
      - 66 layout only: templatized host lists in which the fused member now numbers first.

      Restricted to the 994 inputs main's own tests feed: 993 unchanged, and the one widened is the
      old open row.

      **Frequency, measured.** The ten boot captures and the ab37 capture, 70,783 records, raw and
      through `Templatize`, old against new, counts only. **Prediction, recorded before the run:** at
      most 20 changed records, 0 narrowed, 0 error changes, and over-mask shapes rather than a closed
      leak. **It held:** 10 records changed, raw and templatized alike, all widened, and every one a
      reverse-DNS name. That is a cost category, not a leak closed: main had sent only the
      `.in-addr.arpa` beside a masked address. It is the only category, and its row is invented.
      Nothing narrowed, and there was no list effect. Nothing from the captures is in the repo.

      **Tests.** Red first:
      - `TestIssue54IPv4LabelInHostname`, carrying:
        - exact outputs for 17 closed shapes, raw and through the real `Templatize`, with every
          fragment main sent absent and a round trip;
        - 11 byte-identical rows on both paths, plus the partial octet on the raw line;
        - the two pinned key cells;
        - the recurrence row and both `residue` claims.
      - The #48 test's open row went, and three things were added:
        - three completeness rows and three templatized rows (the URL one green on main, as #48's
          was), each also run through the logfmt wrapper;
        - five over-mask rows;
        - #54's old outputs fed to `TestPlaceholdersAreInert`.
      - The fixpoint shape.

      Main failed 33 subtests, all by assertion. A later commit adds a sentence-dot row with text
      after the dot. At the end of the text, a rule that ate the dot ties on length with one that
      leaves it, and a mutant survived the first row.

      **Flakiness and mutants.** The new and touched tests pass 20 of 20 runs under `-race`. Ten
      mutants each turn tests red:
      - 9d removed turns 34 subtests red, every closed, completeness, templatized, over-mask and
        fixpoint row;
      - the right-hand run allowed to end on `.`, in the letter alternative only, turns the
        dot-then-space row red; in both alternatives it turns both sentence-dot rows red;
      - `:` admitted to the label class turns the port rows red, raw, template and completeness;
      - the letter rule dropped turns the netstat and both range rows red;
      - no `<IP>` turns 13 red: the Template rows, logfmt included, and the templatized-coverage
        test;
      - no leading context turns the partial-octet row (`https://x10.0.0.5.nip.io/x`) red;
      - no trailing context turns the raw partial-octet check red;
      - 9d before detector 4 turns the credentialed-DSN row red;
      - our own tag accepted as the address turns `TestPlaceholdersAreInert`, the three open #57 rows
        in the #55 and #58 tests, and both pinned key rows red.

      **The full suite** ran under `-race` on main and on the branch: 1,127 results, then 1,176.
      - **No existing result changed outcome.**
      - One left: the open #54 row.
      - 50 new results came in.

      **Wire check.** One run per binary against a capturing endpoint (`--plain --llm-anonymize
      --warmup-lines 0`). A `[FATAL]` line carried `https://10.0.0.5.nip.io/x`, with #57's
      `s3://AKIA…EXAMPLE.prod@x@bucket` in its context.
      - `.nip.io`: 1 before (the trigger line), 0 after.
      - `.prod` fires in both, so the grep is not vacuous.
      - On the wire, the new trigger reads `https://<HOST_1>/x` and the template `https://<HOST_2>/x`.
        That is the existing raw/template two-placeholder cost.

      `internal/pipeline` does not appear in the diff. README:
      - the #54 audit bullet is struck, and its impossibility argument kept as history;
      - the later-member key cell moves to #57's bullet;
      - the audit sentence gains #54;
      - a new past-tense paragraph carries the shapes, span, costs and the measured frequency.

      "Known limitations" never named #54, so nothing there moved. Seen, out of scope: #57, #59, #52,
      #65. Targets v0.12.7

- [x] **A URL is recognised whatever is glued before or inside its scheme** — #52, found in the #46
      wire capture and left by #43, whose comment from #66's sweep added the raw-line half. 9a and 9c
      find a URL by its scheme, `\b[a-z][a-z0-9+.-]*://`, and each carried its own copy of it. The
      anchor is CONTEXT, outside the group, so #43's placeholder tolerance never reached it, and the
      host went to the configured model endpoint two ways:
      - **Template field.** A scheme with a digit templatizes to `s<NUM>://`, and both rules went
        quiet: `url=s3://AKIA…:secret@bucket.example.com/x` →
        `url=s<NUM>://<TOKEN_1>:<TOKEN_2>@bucket.example.com/x`; `h2://api.acme.com/x` sent the host;
        `s3://h1,h2,h3/x` sent every member (9c).
      - **Raw line.** `\b` fails before a scheme glued to `_`, a word character:
        `x_https://acme.com/x`, `__https://acme.com__` and `svc_https://acme.com/x` were sent whole.
        So was `2https://acme.com/x`, on the raw line only; in its template, `<NUM>https://`, `\b`
        holds after the `>`, so the two paths disagreed.

      Credentials were never affected: detector 4 anchors on `://` alone. `Mask` returned nil and
      `residue()` returned `""`: 9a and 9c are `verify:false`. v0.4.0 → v0.12.7, **thirty-three tagged
      releases** (`git tag --contains 2da31be | wc -l` → 33).

      **One correction to the issue as filed.** It named `k8s://` with `s3://`. On main `k8s://`
      was masked in the template, by accident: after `k<NUM>` the anchor restarted at the `s` and
      read the scheme as `s`. It reads as `k<NUM>s` now, and the output is the same. It is pinned as
      byte-identical, with the accident named.

      **The fix (`internal/anonymize/anonymize.go` only).** One `schemeAnchor`, used by both rules:
      a letter, then scheme characters or pipeline placeholders, then `://`.

      **Decisions.**
      1. **No leading assertion, argued.** Leftmost-longest already starts a scheme at the earliest
         letter whose run reaches `://`, so a letter before a scheme is part of it: `xhttps://` is
         scheme `xhttps`, as it always was, and is pinned. What `\b` refused was only a start after a
         `_` or after a digit no earlier letter reaches, and both leaked. So nothing that may precede
         a scheme is refused, and the anchor is `(?:^|[^a-z])` in effect. `foo_bar://` reads as scheme
         `bar`; it is not a legal scheme either way, and masking the host behind it is the safe
         reading. Pinned: `(…)`, `"…"`, `<…>`, start of text, and two prose rows with scheme-like
         words and no `://`. The leading letter needs no tolerance, because the pipeline never
         replaces a lone letter.
      2. **One definition.** `TestSchemeAnchorIsShared`: exactly two rules compile `schemeAnchor`
         (9a, and 9c iterating), and no rule spells the scheme class out on its own, in the default
         set or with extra suffixes. It needs the const, so it follows the fix in its own commit.
      3. **The issue's generalisation is a companion test,
         `TestEveryDetectorsContextSurvivesTemplating`.** For every completeness row a detector's
         group lands on, each CONTEXT position (inside the match, outside the group) is replaced
         by a digit:
         - if the detector stops matching, that position is proven digit-free;
         - if it still matches, the row is templatized for real and the detector must still find
           its secret.

         **Results:**
         - On main it is red on exactly 9a and 9c, naming every row.
         - It proves 3a's "Bearer " (7 positions), 10's `/home/` and `/Users/` (13, with a new
           `/Users/` completeness row) and 9d's edges (6) digit-free.
         - 4, 4b and 4c are tolerant in theirs.

         It is a companion and not an extension of `TestEveryUncollapsedDetectorHasATemplatizedRow`.
         That test is about the GROUP and exempts values the pipeline collapses; context damage does
         not depend on collapse, and this one reads every row. Group literals (`eyJ`, `AKIA`, `sk-`,
         9b's suffixes) stay the other test's business.
      4. **The 3a cell is not reached, measured.** `Bearer https://acme.com/x` →
         `Bearer <TOKEN_1>://acme.com/x` on both paths: 3a takes the scheme as the token, and no
         scheme letter is left. The damage is OUR tag, not the pipeline's, so it is interference,
         not tolerance. It is pinned, and recorded as a cell of the authority audit, 65.

      **What it costs.** No new over-mask category. Every new match is a `scheme://` the old anchor
      refused, and the host behind it is masked. One existing row moved, and it had deferred to this
      issue in its own comment: #66's member-in-scheme-position row,
      `mongodb://h1,h2_x://y` → `mongodb://<HOST_1>,<HOST_3>_x://<HOST_2>`. `y` is masked now, and
      `h2` stays masked, which is what the row guards.

      **No narrowing, shown differentially.** The corpus came from uncommitted hooks in `Mask` and
      `residue`, carried by `-overlay` copies of the source, and run under the anonymize and llm tests
      of both main and the branch. The templatized form of every input was added: 2,179 distinct
      inputs. Old and new masking were compared by per-byte coverage bitmaps rebuilt from `byToken`.
      Results:
      - **0 narrowed**, 0 mixed, 0 error changes, 0 layout-only;
      - 2,144 unchanged;
      - 34 widened: the new tests' inputs;
      - 1 the bitmap cannot rebuild: #66's old output, which `Mask` feeds to `residue`; its `y` is
        masked now.

      Restricted to the 1,236 inputs main's own tests feed: 1,234 unchanged, the #66 row widened, and
      the same unrebuildable leftover.

      **Frequency, measured.** The ten boot captures and the ab37 capture, 70,783 records, raw and
      through `Templatize`, old against new, counts only, classified in the harness by shape. Three
      categories were defined: digit in the scheme, `_` before it, digit before it. **Prediction,
      recorded before the run:** 0 to 5 changed records, 0 narrowed. **It held:** 0 changed records,
      so no category appeared and no category row was due. Nothing from the captures is in the repo.

      **Tests.** Red first:
      - `TestIssue52SchemeAnchor`:
        - exact outputs for 13 closed shapes, raw and through the real `Templatize`, with every
          fragment main sent absent and a round trip;
        - 12 byte-identical rows on both paths;
        - the pinned 3a cell and the `residue` claim.
      - `TestEveryDetectorsContextSurvivesTemplating`.
      - Three templatized rows with a digit scheme (URL, credential, host list), each also run
        through the logfmt wrapper.
      - A digit-scheme and a `/Users/` completeness row.
      - The new outputs fed to `TestPlaceholdersAreInert`.
      - #66's row rewritten.

      Main failed 23 subtests and the context test, all by assertion.

      **Flakiness and mutants.** Every new and touched test passes 20 of 20 runs under `-race`. Five
      mutants each turn tests red:
      - tolerance on 9a only, so 9c keeps its old anchor: both digit-scheme host-list rows (on their
        template path), the list's templatized row and its two logfmt runs, the context test and the
        shared-anchor test;
      - `\b` kept as the leading boundary: the six `_`- and digit-glued rows and #66's row;
      - the scheme run not tolerant: 16 subtests, every digit-scheme row and templatized row, and
        the context test;
      - a boundary that refuses a digit, `(?:^|[^a-z0-9])`: the digit-before-the-scheme row;
      - 9c with a drifted copy of the anchor, tolerant of `<NUM>` only: the shared-anchor test
        alone, since the rows only ever carry `<NUM>`.

      **The full suite** ran under `-race` on main and on the branch: 1,177 results, then 1,217.
      - **No existing result changed outcome.** #66's row passes before and after under its
        rewritten expectation.
      - 40 new results came in.

      **Wire check.** One run per binary against a capturing endpoint (`--plain --llm-anonymize
      --warmup-lines 0`). A `[FATAL]` line carried `upload to s3://bucket.example.com/key failed`,
      with #57's `s3://AKIA…EXAMPLE.prod@x@bucket` in its context.
      - `bucket.example.com`: 1 before (the template field), 0 after.
      - `.prod` fires in both, so the grep is not vacuous.
      - The template now reads `s<NUM>://<HOST_1>/key`, the trigger line's placeholder, so the model
        sees one host recur.

      **`residue`.** Nothing new: 9a and 9c stay `verify:false`, and the old leftover still reads as
      clean (pinned).

      `internal/pipeline` does not appear in the diff. README:
      - the #52 audit bullet is struck;
      - the audit sentence gains #52;
      - a new past-tense paragraph carries the shapes, span, decisions and measured frequency.

      **Found on the way, not fixed: a configured bare-host suffix with a digit in it.**
      `--llm-anonymize-suffix k8s` masks `db.k8s` on the raw line, but the template carries
      `db.k<NUM>s` and 9b no longer matches it, so the name is sent there. It is this issue's
      lesson on 9b's suffix literal, which sits inside the group, so neither the context test nor
      the coverage test sees it: the coverage test reads only the built-in suffixes, none of which
      has a digit. Confirmed with a throwaway test. It is reachable through configuration and no
      open issue covers it, so it is filed as #105 and named under README "Known limitations".
      Seen, out of scope: #57, #59, #65's sweep. Targets v0.12.8

- [x] **A host on a configured suffix is masked in the pre-masked template** — #105, found in the work on
      #52 and filed from it. `--llm-anonymize-suffix` extends 9b, and each value was spliced
      in verbatim (`escapeAll` → `QuoteMeta`). The labels before a suffix were tolerant since #43;
      the suffix was not. It is #52's lesson on a literal INSIDE the group, which is why neither
      #52's context test nor the uncollapsed test saw it: the one reads context, the other reads
      only the built-in suffixes. `New("k8s")`: `peer db.k8s down` → `peer <HOST_1> down` raw,
      and `peer db.k<NUM>s down` sent whole on the Template field. `Mask` returned nil; 9b is
      `verify:false`. v0.4.0 → v0.12.8, **thirty-four tagged releases** (`git tag --contains
      2da31be | wc -l` → 34).

      **Two corrections to the issue as filed, both measured with the real `Templatize`.**
      - **It is not only digits.** NUM takes a sign and a decimal tail with it (`eu-1` → `eu<NUM>`,
        `v1.2` → `v<NUM>`), HEX rewrites a suffix with no digit at all (`deadbeef`, `0xcafe` →
        `<HEX>`), and a suffix beginning with a digit loses the dot before it after a label ending
        in one (`db1.2corp`, `db-1.2corp` → `db<NUM>corp`).
      - **`corp1`, the shape config_test.go uses, was not a leak.** The built-in `corp` read
        `db.corp<NUM>` as a host on `corp` and left the `<NUM>` beside the tag, so no host text was
        sent. It is pinned as moved: one `<HOST_1>` now.

      **The fix (`internal/anonymize/anonymize.go`, and `internal/config` for the dots).**
      `bareHostPattern` reads each suffix as written AND as `pipeline.Templatize` rewrites it.

      **Decisions.**
      1. **Derived, not hand-written.** A hand-written tolerance needs copies of UUID, HEX with its
         letter rule, and NUM, in the pipeline's order: #42's class, a pattern duplicated across
         two packages with no shared source. So anonymize imports pipeline. There is no cycle:
         pipeline imports model, export and score, none of which imports anonymize or llm. The
         direction is llm → anonymize → pipeline, the consumer of the Template field reading the
         producer's handwriting. The masking stays separate, as the package comment requires.

         What can touch a suffix-shaped string: NUM and HEX, as above; UUID only inside a suffix
         that contains one; IPv4 only across numeric labels, which is 9d's on both paths; TS, IPv6
         and STR need ':' or quotes. Derivation covers all of them without the list.

         `Templatize(S)` alone gives what it gives in a host, because 9b demands a boundary after
         the suffix and a NUM that takes a trailing `.5` keeps the prefix. The one context effect
         is the dot BEFORE the suffix. It is measured on a probe, `Templatize("x9." + S)`. If the
         dot is gone, the rest becomes a branch `labels* label inner* <NUM> rest`, added only when
         the rest carries a letter (9d's rule), so an all-digit suffix cannot make every word glued
         to a number a host.
      2. **A form ending in a placeholder is closed by `\B`.** After '>' a `\b` demands a word
         character next, the opposite of what the raw line asks after `corp1`. `\B` holds exactly
         where the next character is not a word character, or the text ends. It is zero-width, so
         9b keeps group 0. Pinned: end of text, a space, a dot, a port, another placeholder; and a
         suffix glued to a letter or '_' is no host on either path. The merge branch's boundary is
         read on `<NUM>` + rest, so the letter guard is the only thing that refuses an empty rest
         (a mutant found the boundary refusing it by accident first).
      3. **The built-ins are byte-identical.** Every built-in, and a configured suffix the pipeline
         leaves alone, adds no form. `TestBareHostPatternUnchanged` rebuilds main's construction in
         the test and compares the source for `baseDetectors`, `New()`, `New("acmecloud")` and
         `New("acmecloud", "my-cloud")`.
      4. **Config: dots stripped, empty refused.** Measured on main with throwaway tests:
         - `/` and whitespace are matched literally: no leak;
         - `<NUM>` never matches after `\b`, and a tag-shaped suffix at worst nests a mapping, which
           `Restore` resolves by rule (#50): no leak;
         - `""` was an empty alternative and masked every dotted prefix
           (`java.lang.NullPointerException` → `<HOST_1>NullPointerException`);
         - **`.acme` LEAKED**: `\.acme` needs `db..acme`, so every host on it was sent on both
           paths, with no warning, and the README writes the built-ins as `.internal`. `acme.`
           matched only before a word character.

         So `config.Load` now trims leading and trailing dots from each resolved suffix, flag or
         file alike, and refuses one that is empty after trimming, with an error naming
         `--llm-anonymize-suffix` and `llm.anonymize_suffixes`. Case is left alone; 9b is `(?i)`.

      **What it costs.** Pinned in `TestAcceptedOverMasking`, all on the template and only under a
      configured suffix the pipeline rewrites. The placeholder stands for any run rewritten the same
      way:
      - `db.acme12` is a host under `acme1`;
      - `db.k88s` is a host under `k8s`;
      - any `word.<HEX>` is a host under `deadbeef`;
      - `x12corp` is a host under `2corp`.
      The template's host also gets its own placeholder: its masked form, `db.k<NUM>s`, differs
      from the raw line's `db.k8s`.

      **No narrowing, shown differentially.** Hooks in `Mask` and `residue`, carried by `-overlay`
      copies of the source, collected every input the anonymize, llm and config tests feed, on
      main's tests and the branch's; each input's templatized form was added: 2,364 distinct
      inputs. Main's `anonymize.go` was overlaid as a sibling package and compared by per-byte
      coverage from `byToken`, under `New()` and under `New("k8s", "corp1", "eu-1", "v1.2", "az2",
      "deadbeef", "2corp")`:
      - default Mapper: 2,364 unchanged;
      - extra-suffix Mapper: 2,297 unchanged, **67 widened**, all of them the new tests'
        templatized inputs; **0 narrowed**, 0 mixed, 0 error changes, 0 layout-only, 0 that the
        bitmap cannot rebuild.

      Restricted to the 2,177 inputs main's own tests feed: all unchanged, under both Mappers.

      **Frequency.** The built-ins are untouched, so the default Mapper cannot change, and one run
      shows it: the ten boot captures and the ab37 capture, 70,783 records, raw and through
      `Templatize`, old against new, counts only: **0 changed**, 0 error changes. No configured
      suffix was taken from capture content, so there is no category to report. Nothing from the
      captures is in the repo.

      **Tests.** Red first:
      - `TestIssue105ConfiguredSuffix`:
        - 16 closed rows, exact outputs raw and through the real `Templatize`, every fragment main
          sent absent, a round trip; one logfmt msg row;
        - the `corp1` moved row;
        - 20 byte-identical rows: every built-in under `New()` and `New("k8s")`, `acmecloud`, a URL
          host, three glued rows and an all-digit suffix.
      - `TestBareHostPatternUnchanged`.
      - `TestConfiguredSuffixesSurviveTemplating`: seven suffix shapes (digit inside, at the end,
        hyphen-digit, dotted digit, all hex, leading digit, multi-label) in five positions, each
        raw, templatized and inside a logfmt msg, derived from marked rows.
      - Four `TestAcceptedOverMasking` rows; the new outputs fed to `TestPlaceholdersAreInert`
        under a rule set with configured suffixes.
      - Config: `TestAnonymizeSuffixNormalised` (flag and file), `TestAnonymizeSuffixEmptyRejected`
        (flag and file), and `TestAnonymizeSuffixReachesTheMasker`, which runs `config.Load` into
        `anonymize.New` on both paths.

      `TestConfiguredSuffixesSurviveTemplating` is a third test, not an extension of the
      uncollapsed test or of #52's context test. Both iterate the default rule set, and the
      configuration is the dimension this class lives in.

      Main failed 57 subtests and the three config tests, all by assertion; the guards passed.

      **Flakiness and mutants.** Every new and touched test passes 20 of 20 runs under `-race`.
      Eight mutants each turn tests red:
      - a bare digit run only (`\d+` → `<NUM>` by hand): 20, every hyphen, dotted and hex row;
      - a boundary per suffix, behaviour-equivalent on the built-ins: the unchanged-pattern test;
      - `\b` kept after a trailing placeholder: 37, every row ending in one, and the glued rows;
      - no assertion after a trailing placeholder: the three glued rows;
      - no merge branch: 4, the dot-eaten rows;
      - a rewritten form only when the suffix has a digit: 7, every hex row;
      - the merge without its letter guard: the all-digit row;
      - config without dot stripping: the three config tests.

      **The full suite** ran under `-race` on main and on the branch: 1,217 results, then 1,300.
      No existing result changed outcome; 83 new results came in.

      **Wire check.** One run per binary against a capturing endpoint (`--plain --llm-anonymize
      --llm-anonymize-suffix k8s --warmup-lines 0`). A `[FATAL] peer db.k8s down` line, with #57's
      `s3://AKIA…EXAMPLE.prod@x@bucket` in its context.
      - `db.k`: 1 before (the template field), 0 after.
      - `.prod` fires in both, so the grep is not vacuous.
      - The template now reads `peer <HOST_2> down`.

      `internal/pipeline` does not appear in the diff. README: the #105 Known-limitations bullet
      is gone, and a past-tense paragraph carries the shapes, span, cost and the dotted-suffix
      change. The flag's help text and README's usage line say nothing about digits and stay.
      Seen, out of scope: #57, #59, #65's sweep. Targets v0.12.9

- [x] **A key recognised inside a larger value masks the whole value** — #57, row 17 of the #55
      sweep, with the later-member cell its comment moved from #54. Detectors 1, 2 and 3b (JWT,
      AKIA, `sk-`) run before every credential and host rule, so a key that was only PART of a value
      was tagged on its own, mid-value. Every later group excludes `<`, the inertness invariant, so
      the remainder was sent:
      - `s3://AKIA…EXAMPLE.prod:secret@bucket` → `s3://<TOKEN_1>.prod:<TOKEN_2>@<HOST_1>`, and the
        colon-less and raw-`@` forms the same way;
      - a remainder before the key (`prod.AKIA…@bucket`) or in the password (`user:AKIA….prod@`,
        `user:AKIA…:extra@`, `user:pa:AKIA…@`);
      - a key as a host label: `https://AKIA….prod.acme.com/x` sent the domain, as did
        `https://api.AKIA….acme.com/x` and the same host behind `u:p@`;
      - a key with text beside it as a list member: `mongodb://h1,AKIA….prod,host3/db` sent `.prod`
        and `host3`, and as the first member it sent the rest of the list.

      `Mask` returned nil and `residue()` returned `""`: the remainder is not a shape any detector
      matches. v0.4.0 → v0.12.9, **thirty-five tagged releases** (`git tag --contains 2da31be | wc -l`
      → 35); the list member since v0.9.4, before which no later member was masked at all.

      **Where the grammar allows a glued remainder, measured per key.** AKIA and `sk-`: any non-word
      userinfo or host character on either side. On the raw line a `_` or a letter defeats the key's
      `\b`, so no key is seen and detector 4 masks the whole userinfo. On the Template field the
      mangled form carries no closing `\b`, so a `_` or a lowercase letter after the key is glue
      there too: `s3://sk-…XYZ_prod@b` sent `_prod` on the template only. JWT: `-` and `_` are
      base64url and run on into the third segment; `.`, `~` and `!` are glue.

      **The mechanism, compared on the cells before any code.**
      - **A, the issue's candidate**: `group int` becomes a group list. It closes the userinfo cells
        only with companion rules reading our tag as context, one per side per position. The host
        cells need host rules with groups too, and the label stays split (`<TOKEN_1><HOST_1>`, the
        HOST a partial name). It changes `apply`, `residue`, `completenessSecretFor` and every test
        that reads `d.group`. Its one real gain is that `residue` would see the class.
      - **B, #54's lesson**: rules more specific than the key detectors, run before them. It
        closed every cell, raw and templatized, in a prototype measured before the plan, with no
        contract change.
      - **C**: a post-pass gluing our tag to its neighbours closes raw cells only, since on the
        template the key is mangled, not our tag, and #54 rejected it for nesting and a numbering
        gap. A key with a stricter boundary narrows: a key before a sentence dot would go unmasked.
        The authority pre-pass stays rejected, since B does not fail.

      **The fix (`internal/anonymize/anonymize.go` only) is B, as two rules.** Two are needed, not
      one: the userinfo is a TOKEN and the host is a HOST, and a detector carries one tag. Both run
      first, out of sequence, under 6's and 9d's numbering convention.
      - **4d** is detector 4's group and `authorityTail`, matched only when a half carries a key
        that is not the whole half. The halves split at the first `:`, as 4b/4c split them. Glued
        means a unit before the key in its half, or directly after it a character the key could not
        have continued with. That exclusion stops the mangled template form (`AKIAIOSFODNN<NUM>`
        before `EXAMPLE`) reading as glued when the key is a whole half. TOKEN, `verify:true`.
      - **9e** is one host run with a glued key in it, after one consumed `://`, `@` or `,`, with
        no consumed right edge: leftmost-longest makes the run maximal anyway, and a consumed edge
        would eat the comma the next member needs. HOST, `verify:false`.
      - The three key patterns are hoisted into named values that 1, 2, 3b, 4d and 9e all read. Every
        existing detector compiles byte-identically (compared source by source with main).
      - `apply`, `residue`, `Restore`, `applyToFixpoint` and the `detector` struct are unchanged.

      **Decisions.**
      1. **Placement, pinned.** Both rules run before every key detector, and 4d before 9e, so a
         userinfo is a TOKEN before a host rule can read it (`s3://user@AKIA….prod@bucket` →
         `<TOKEN_1>@<HOST_1>`). `TestGluedKeyRulesReadTheKeyDetectors` holds both: each key
         detector's source sits inside exactly one TOKEN rule and one HOST rule, both earlier.
      2. **Tag and Restore.** The userinfo is one TOKEN and Restore gives it back whole. A key-label
         host is HOST and the whole name is the value: containment, as for #48's UUID and #54's
         address. TOKEN would tell the model a host is a credential, behind `u:p@` too. The fused
         value recurs under one placeholder, and the key alone elsewhere keeps its own TOKEN.
         **`residue`**: a raw glued userinfo mutes (4d); a raw key-label host, fused member or key
         in prose mutes through detector 2, unchanged; the old leftovers still read as clean. All
         pinned.
      3. **9c's fixpoint and cap, untouched.** 9e masks every fused member in one pass before 9a
         and 9c, so a fused member reaches 9c as a whole tag and the list re-anchors on it at no
         cost in passes: `mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db`, 9d's layout. Two fused members
         side by side each match. A termination shape alternates `AKIA%016d.prod` with names up to
         65 members.

      **Byte-identical with main, raw and templatized:** a key that is a whole half, member or host;
      a key alone, behind `Bearer`, in a URL query; a JWT with a `-` tail; a `_` before the key; a
      userinfo with no key; and a key glued to text in PROSE, `key AKIA….prod used` →
      `key <TOKEN_1>.prod used`. Outside a userinfo or an authority, the text beside a key is not
      part of a value this package protects.

      **What it costs, pinned in `TestAcceptedOverMasking`.**
      - A glued userinfo is one token, so the colon and "this half is a key" are inside it, #55's
        traded-signal class.
      - A key in a hostname is tagged HOST (`TestTypeTags`).
      - 9e does not check that it is inside an authority, so a key glued to labels right after a
        comma or an `@` in prose reads as a host: `keys=a,AKIA….prod` → `keys=a,<HOST_1>`, and
        `mail ops@AKIA….prod.example.com` → `mail ops@<HOST_1>`, where main sent the domain.
      - Inherited, not new: behind a password-less userinfo, a key-label host makes detector 4
        decline (`authorityTail` admits no `<`), and 9a's fallback tags the username HOST, as on main.

      **Known residual, pinned, not filed.** A key as a label of a BARE host on a private suffix:
      `peer worker-AKIA….corp.internal down` → `peer worker-<TOKEN_1>.<HOST_1> down`, sending
      `worker-`. Reaching it would mean reading 9b's configurable suffixes, and #105's rewritten
      forms, in a rule that runs before the key detectors. It is as theoretical as #54's key-label
      cell was.

      **No narrowing, shown differentially.** Hooks in `Mask` and `residue`, carried by `-overlay`
      copies of the source, collected every input the anonymize, llm and config tests feed, on main
      and on the branch; each input's templatized form was added, for 2,629 distinct inputs. Main's
      `anonymize.go` was overlaid as a sibling package, and the two were compared by per-byte coverage
      rebuilt from `byToken`, under `New()` and an extra-suffix Mapper alike:
      - **0 narrowed**, 0 mixed, 0 error changes;
      - 1,786 unchanged;
      - 205 widened, the new tests' inputs;
      - 8 layout only: the delimiting `@` is now outside the placeholders, where the email rule had
        swallowed `.prod@h1.acme.com` under EMAIL (#61's old row and the two templatized rows, each
        also inside the logfmt wrapper);
      - 623 the bitmap cannot rebuild, because the input already carries a literal placeholder;
        every one masks byte-identically;
      - 7 errored on both, the cap tests' inputs.

      Restricted to the 2,364 inputs main's own tests feed: 1,747 unchanged, 10 widened (the five
      moved cells, raw and templatized), 2 layout (#61's row), 598 unrebuildable and identical,
      7 both-error, 0 narrowed.

      **Frequency, measured.** The ten boot captures and the ab37 capture, 70,783 records, raw and
      through `Templatize`, old against new, counts only. **Prediction, recorded in the plan before
      the run:** 0 changed records, 0 error changes. **It held:** 0 and 0, and no record carries a
      key shape at all, so there is no category to report. Nothing from the captures is in the repo.

      **Tests.** Red first:
      - `TestIssue57PartlyRecognisedValue`:
        - 30 closed rows, exact outputs raw and through the real `Templatize`, every fragment main
          sent absent, a round trip;
        - 14 byte-identical rows and the pinned residual;
        - the recurrence row and the `residue` claims.
      - The two open #57 rows of the #55 test, the one in the #58 test and the two pinned key rows of
        the #54 test moved in. So did #61's "#57-shaped first member", whose slot now holds a key
        followed by an address, so member 1's adjacent-tags context stays covered.
      - Five completeness rows, four templatized rows (each also run in the logfmt wrapper), three
        over-mask rows, a `TestTypeTags` row, eight outputs fed to `TestPlaceholdersAreInert`, the
        fixpoint shape, and `TestGluedKeyRulesReadTheKeyDetectors`, written without the new
        identifiers so the red commit builds.

      Main failed 52 subtests and two tests without subtests, all by assertion; the guards passed.
      Main's own test file run against the new code fails exactly the six moved rows, plus
      `TestEveryDetectorHasACompletenessRow`, which asks for rows for the two new rules.

      **Flakiness and mutants.** Every new and touched test passes 20 of 20 runs under `-race`.
      Twelve mutants each turn tests red:
      - both rules off: 52 subtests, every closed, completeness, templatized, over-mask and fixpoint
        row, plus the type-tag and shared-key tests;
      - 4d after the key detectors: 24, every userinfo row and the shared-key test;
      - 9e after the key detectors: 22, every host row, the fixpoint shape and the shared-key test;
      - 9e before 4d: 18, every userinfo row whose glued key sits in the username, where 9e reads
        the run after `://` or a raw `@` as a host, plus #61's row and the recurrence row;
      - 9e's group admitting `/`: 10, every key-label row, raw, template and logfmt;
      - 9e's group admitting `@`: the password-less row and the JWT `-` tail row;
      - 4d's class admitting `/`: the URL-query row;
      - 4d without the per-key exclusion: 18, every whole-half key row on the template, #52's
        credential rows and the #46 templatized rows;
      - 9e without it: 10, the whole-key rows on the template -- as a half, a member and a host,
        where `AKIAIOSFODNN<NUM>` before `EXAMPLE` reads as glued -- the JWT `-` tail, #67's
        templatized key row and #52's credential rows;
      - 4d without its password branch: the four password-side rows and the password completeness
        row;
      - 9e without `,`: 11, every list-member row and the fixpoint shape;
      - 9e consuming a right edge: the adjacent-members row.

      **The full suite** ran under `-race` on main and on the branch: 1,300 results, then 1,364.
      - **No existing result changed outcome.**
      - Six left: the moved rows.
      - 70 new results came in.

      **Wire check.** One run per binary against a capturing endpoint (`--plain --llm-anonymize
      --warmup-lines 0`). A `[FATAL]` line carried `s3://AKIA…EXAMPLE.prod:secret@bucket`, with
      #59's `postgres://user:p/ss@db` in its context as the new open control, since `.prod` is now
      closed.
      - `.prod`: 2 before (the trigger line and the masked template), 0 after.
      - `p/ss` fires once in both, so the grep is not vacuous.
      - On the wire, the new trigger reads `url=s3://<TOKEN_1>@<HOST_1>`.

      `internal/pipeline` does not appear in the diff. README: the #57 bullet is struck and marked
      closed, the audit sentence gains #57, and a new past-tense paragraph carries the shapes, span,
      costs, the residual and the frequency.

      **Found on the way, not fixed.**
      - **A key glued to `_` or a letter is not recognised at all on the raw line, and is SENT
        WHOLE.** `key AKIAIOSFODNN7EXAMPLE_prod used` and `key sk-…XYZ_prod used` reach the model
        unmasked, and `residue()` reads them as clean: the key's closing `\b` fails, and detectors 2
        and 3b match nothing. The template masks the key and sends `_prod`. Confirmed with a
        throwaway test. No open issue covers it, so it is filed as #108.
      - **9d misses the second of two adjacent fused list members** on the raw line, because its
        consumed trailing edge eats the shared comma:
        `mongodb://h1,10.0.0.5.nip.io,10.0.0.6.nip.io,host4/db` sends `.nip.io` and `host4`.
        Confirmed with a throwaway test. It is an authority cell, offered as a comment on #65.

      Seen, out of scope: #59, #65's sweep, #90, #51. Targets v0.12.10

- [x] **A key glued to `_` or a letter is masked, not sent whole** — #108, found while verifying
      #57. Detectors 1, 2 and 3b (JWT, AWS, `sk-`) end a key with `\b` on both edges, and `_` and
      letters are word characters, so a key glued to one matched nothing on the raw line and was
      SENT WHOLE:
      - `key AKIAIOSFODNN7EXAMPLE_prod used`, `key AKIA…EXAMPLEx`, `key ASIA…_prod used` and
        `key sk-…XYZ_prod used`, the key with text glued after it;
      - `key x_AKIA… used`, `key idAKIA… used`, `my_sk-…` and `token_eyJ…`, the key with text
        glued before it, and both sides at once;
      - two keys glued by one `_`.

      `Mask` returned nil and `residue()` returned `""`: the re-scan runs the same detectors, so it
      had no pattern to look with, #55's reason. On the Template field the mangled form has no
      closing `\b`, so a key with text glued AFTER it was masked there and only the text was sent;
      text glued BEFORE it leaked on both paths, and so did the second of two glued keys.
      v0.4.0 → v0.12.10, **thirty-six tagged releases** (`git tag --contains 2da31be | wc -l` →
      36).

      **The mechanism, compared on the cells before any code.**
      - **A, move the edges inside 1, 2 and 3b.** It narrows main in three shapes, each masked whole
        today only BECAUSE the key detectors do not see the key. Tagged first, the key blocks every
        later group at our `<`, the anti-correlation recorded for #51:
        - `hdr Bearer AKIA…_prod` → `Bearer <TOKEN_1>_prod` (2 runs before 3a);
        - `x_AKIA…@example.com` → `x<TOKEN_1>@example.com`, the audit table's theoretical
          "1/2/3b → 5" cell reached from a shape main masks;
        - `worker_AKIA….corp.internal` → `worker<TOKEN_1>.<HOST_1>`.

        RE2 also has no lookbehind and those detectors must keep group 0
        (`TestGluedKeyRulesReadTheKeyDetectors` finds them by it), so a leading glue character
        could only be consumed into the token.
      - **B, rule 11**: one more detector per key, appended AFTER detector 10. Each one is
        `(?:^|[^<lead>])(<loose key>)`, group 1, TOKEN, `verify:true`. It runs after every rule that
        could own a value around a key, so it only reads raw text no rule claimed. No earlier
        rule's input changes and rule 11 only adds placeholders, so nothing narrows and nothing
        moves BY CONSTRUCTION. B was taken.

      **The fix (`internal/anonymize/anonymize.go` only).**
      - A key's parts are hoisted into named values: `awsPrefix`, `awsChar`, `skPrefix`, `skChar`,
        `jwtBody`. `jwtKey`, `awsKey` and `skKey` are rebuilt from them, so a key is one grammar read
        with two edge policies that cannot drift. **Strict** (`\b` on both edges) is a key that
        stands alone in its value: 1, 2, 3b, 4d and 9e. **Loose** is a key glued to a word
        character in text nothing else claimed: rule 11.
      - `keyShapes` gains `loose` and `lead` beside `pat` and `cont`. A key's own alphabet ends
        it, and rule 11 has no trailing assertion: leftmost-longest makes the run maximal.
      - Every existing detector compiles byte-identically. Its source, tag, group, verify, fixpoint
        and order were dumped and compared with main's under `New()` and an extra-suffix Mapper:
        only the three rule-11 detectors are new. `apply`, `residue`, `Restore` and the `detector`
        struct are unchanged.

      **Decisions, each pinned by a test.**
      1. **Boundaries from each key's own grammar, on both edges.**
         - AWS: the run is `[0-9A-Z]`, so lowercase, `_` and punctuation end it. Before the prefix,
           anything outside `[0-9A-Z]` is glue: `_` and lowercase are, while `XAKIA…` and `7AKIA…`
           are a longer run and not a key. Unchanged on the raw line; the template masks `7AKIA…`
           by a NUM accident (`<NUM>AKIA…`), as on main.
         - `sk-`: `_` is glue, a letter or digit is not, so `task-…`, `ask-…` and `2sk-…` stay
           unmasked. `sk-proj-…` (#51) is unchanged.
         - JWT: `-` and `_` continue the third segment as before. Before `eyJ`, only `_` is glue,
           so `xeyJ…` stays.
         - The lead classes come from one rule: a key's own letters and digits make a longer run,
           and `_` and `-` are separators.
      2. **A run longer than a key is masked whole.** `AKIAIOSFODNN7EXAMPLEPROD` → `<TOKEN_1>`.
         Loose AWS is `{16,}`, so the raw line now agrees with the template, whose mangled form
         was always unbounded. This is in rule 11 only: in strict detector 2 it would hit the
         email narrowing above.
      3. **`residue` sees exactly what the detectors see.** Rule 11 is verify-eligible:
         - 11 strings carrying a raw glued key read TOKEN, in prose, a query string, behind
           `Bearer` and in a userinfo;
         - the old leftovers read clean (`key <TOKEN_1>_prod used`).

         So an escalation carrying such a key would mute.
      4. **4d and 9e: `cont` and `pat` do not change.** Every #57 row is byte-identical and
         `TestGluedKeyRulesReadTheKeyDetectors` stays green: rule 11 has group 1, and its source
         does not contain the strict source.
      5. **The prose remainder stays prose.** `key <TOKEN_1>_prod used` and `key x_<TOKEN_1> used`,
         on both paths.

      **What it costs, pinned in `TestAcceptedOverMasking`.** `region ASIAPACIFICDATACENTER01 up`
      → `region <TOKEN_1> up`, and a lowercase word glued before such a run,
      `southeastASIAPACIFICDATACENTER01` → `southeast<TOKEN_1>`. The template already masked the
      first one on main.

      **No narrowing, shown differentially.** The inputs are every string the anonymize, llm and
      config tests feed `Mask` and `residue`, on main and on the branch, plus each one's
      templatized form: 2,744 distinct inputs. Main's `anonymize.go` was overlaid as a sibling
      package, and per-byte coverage was rebuilt from `byToken` under `New()` and an extra-suffix
      Mapper (`k8s`, `acme1`, `eu-1`, `v1.2`, `deadbeef`, `2corp`). Both Mappers gave the same
      result:
      - 0 narrowed, 0 mixed, 0 layout-only, 0 error changes;
      - 32 widened, every one a glued-key input of the new tests;
      - main's own 2,624 inputs all unchanged.

      **Frequency.** The 70,783 records (ten boots and ab37) were run raw and templatized, old
      against new, under both Mappers: **0 changed, 0 error changes**, as predicted. No record
      carries an AWS or JWT prefix, and the 396 with `sk-` inside a word are untouched.

      **Tests.**
      - `TestIssue108KeyGluedToAWordCharacter`, raw and through the real `pipeline.Templatize`:
        - 14 closed rows, with every fragment main sent checked absent and a round trip;
        - 20 byte-identical rows: the must-stays above, the three narrowing shapes, a bearer key
          with `_` before it and with `_` or `.` after it, a key as a host label, a list member and
          in a userinfo;
        - a recurrence subtest and the `residue` claims.
      - Five completeness rows, four templatized rows (three leading `_`, one control), two
        over-mask rows, one `TestTypeTags` row and seven new outputs fed to
        `TestPlaceholdersAreInert`.
      - Red on main: 31 subtests plus 2 tests without subtests, all by assertion, with the guards
        byte-identical and green.

      **Mutants, each with what it turned red.**
      - (a) `\b` kept on rule 11's trailing edge: 12. Eight #108 rows, completeness
        `aws-key/glued-underscore` and `sk-key/glued`, `TestNoDetectorGroupLandsOnOurOwnOutput`,
        `TestTypeTags`.
      - (b) letters admitted before `sk-`: 2, `task` and `ask`.
      - (c) the decision-2 run refused, capped at 16: 4. Both over-mask rows, the longer-run row,
        completeness `aws-key/run`.
      - (d) rule 11 `verify:false`, so residue reads the old patterns: the `residue` claims.
      - (e) rule 11 at 1, 2 and 3b's position: 10. The eight narrowing rows (bearer `_` before and
        after, email `_` before and after, bare private host, host label, list member,
        userinfo), and #57's "sk- key, underscore after (template only)" and "underscore before
        the key".
      - (f) AWS lead refusing lowercase: 4. The lowercase-before row, completeness
        `aws-key/glued-letter`, the lowercase over-mask row, `TestNoDetectorGroupLandsOnOurOwnOutput`.
      - (g) no leading context: 6. `ask`, `task`, a digit before AWS and before `sk-`, a letter
        before a JWT, an uppercase letter before AWS.
      - (h) JWT lead equal to `cont`, refusing `_`: 6. The JWT `_`-before row and its completeness,
        templatized and both logfmt rows, `TestNoDetectorGroupLandsOnOurOwnOutput`.
      - (i) a drifted AWS prefix without ASIA: 3. The ASIA row and both over-mask rows.
      - (j) trailing context consumed: the two-keys row.

      **The full suite** ran under `-race` on main and on the branch: 1,374 results, then 1,429.
      **No existing result changed outcome** and none left; 55 came in. Main's test file passes
      against the new code. The new and touched tests pass under `-race -count=20`.

      **Wire check.** One run per binary against a capturing endpoint (`--plain --llm-anonymize
      --warmup-lines 0`). A `[FATAL] key AKIAIOSFODNN7EXAMPLE_prod used` line, with #59's
      `postgres://user:p/ss@db` in its context as the open control.
      - `AKIAIOSFODNN7EXAMPLE`: 1 before (the trigger line), 0 after. Both fields now read
        `key <TOKEN_1>_prod used`.
      - `p/ss` fires once in both, so the grep is not vacuous.

      `internal/pipeline` does not appear in the diff. README: a new past-tense paragraph carries
      the shapes, span, costs and frequency, and the audit sentence gains #108. #108 had no
      Known-limitations entry to remove.

      **Found on the way, not fixed.**
      - **A REMAINDER glued after a masked AWS key or JWT behind `Bearer` is sent.** It is
        topology, #57's class, not the key: the key is masked. 1 and 2 run before 3a and tag only
        the key, so `hdr Bearer AKIA…EXAMPLE.prod expired` → `hdr Bearer <TOKEN_1>.prod expired`
        on both paths. `~` and `-` glue the same way, and `_` does on the template. A JWT sends
        what follows `.` or `~`. `sk-` is not affected, because 3a runs before 3b. `residue()`
        reads it clean. Main and the branch agree on every cell, so it predates #108 and is
        untouched by it. The audit table's "1/2/3b → 3a harmless" row is wrong for a glued key,
        and a row is added below it. Confirmed with a throwaway test. No open issue covers it, so
        it is filed as #110 and named in README's Known limitations.

      Seen, out of scope: #51, #59, #65's sweep. Targets v0.12.11

- [x] **A bearer value with a key glued inside it is masked whole** — #110, found while verifying
      #108. 3a takes the whole value after `bearer\s+` as one TOKEN, but it ran AFTER 1 (JWT) and 2
      (AWS), and its group excludes `<` (inertness). A key that was only PART of a bearer value was
      tagged alone and 3a could not claim the rest, so the text glued to it was SENT. Topology,
      #57's class, not the key:
      - `hdr Bearer AKIA…EXAMPLE.prod expired` → `hdr Bearer <TOKEN_1>.prod expired`, and `~prod`,
        `-prod` and ASIA the same, on both paths;
      - `_prod` and a lowercase letter after the key, on the template only, where the mangled key
        has no closing `\b`;
      - a JWT followed by `.` or `~`;
      - `Bearer prod.AKIA….x`, which sent `.x`. `Bearer prod.AKIA…` sent nothing but split one
        value into two tokens (`<TOKEN_2><TOKEN_1>`).

      `residue()` read every leftover as clean. `sk-` was never affected, because 3a runs before
      3b. v0.4.0 → v0.12.11, **thirty-seven tagged releases** (`git tag --contains 2da31be | wc -l`
      → 37; v0.4.0's own anonymizer, run in a throwaway overlay, sends `.prod` and `~prod` on both
      paths).

      **The mechanism, measured on the cells and the full differential before any code.** Both
      candidates close every cell and narrow nothing, because a bearer value holds no whitespace,
      `:`, `@` or `,`, so no later rule ever spanned one, and the whole-value TOKEN covers the key's.
      - **A, move 3a before 1 and 2.** It mints EVERY bearer value before 1 and 2 mint keys
        elsewhere on the line, so a line with a bearer token and a JWT or AWS key elsewhere swaps
        its placeholder numbers: `hdr Bearer abc123… key AKIA… used` goes from
        `<TOKEN_2> … <TOKEN_1>` to `<TOKEN_1> … <TOKEN_2>`. Over the differential: 8 layout-only.
      - **B, rule 3c, #57's remedy.** One rule before 1 and 2, built from `keyShapes` and
        `glued` like 4d and 9e, that matches only a bearer value with a strict JWT or AWS key
        glued inside it and masks the whole value as TOKEN. Absent everywhere else. Over the
        differential: 2 layout-only, both the `prod.AKIA…` cell. B was taken: 0 narrowed and the
        fewest byte changes outside the cells.

      **The fix (`internal/anonymize/anonymize.go` only).**
      - 3a's class is hoisted into `bearerChars`/`bearerChar`. 3a's source is byte-identical.
      - `gluedBearer`: `\b(?i:bearer)\s+(` + `glued` over the unit `bearerChar` or `pipelinePH`
        + `)`. The case-fold is scoped to the word, so a key's case is never folded. The "after"
        class is the bearer class minus the key's `cont`, through `hostCharsBut`.
      - It reads `keyShapes[:2]`, JWT and AWS. `glued` gains a `shapes` parameter; 4d and 9e pass
        `keyShapes`.
      - Detector 3c, `{tagToken, mustLongest(gluedBearer), 1, true, false}`, sits after 9e and
        before 1. It is disjoint from 4d and 9e: their spans carry no whitespace, and a bearer
        value carries none of their anchors.
      - Every existing detector compiles byte-identically: source, tag, group, verify, fixpoint and
        order dumped against main's under `New()` and an extra-suffix Mapper. Only 3c is new, at
        index 2. `apply`, `residue`, `Restore` and the `detector` struct are unchanged.

      **Decisions, each pinned by a test.**
      1. **Mechanism B**, pinned by three mixed-line byte-identical rows A would renumber: a
         bearer token beside an AWS key, `Bearer sk-….prod` beside an AWS key, and
         `bearer akia….prod` beside an AWS key, which also pins that only the word is case-folded.
      2. **Tag and Restore.** The whole bearer value is one TOKEN, and `Restore` gives it back
         whole. The same key alone elsewhere on the line gets its own placeholder, as for #57's
         userinfo. Pinned by a recurrence subtest on one Mapper.
      3. **`residue`.** 3c is `verify:true`, as every credential rule. Three raw leftovers read
         TOKEN, raw and templatized and the JWT form. Four old leftovers read clean:
         `<TOKEN_1>.prod`, `<TOKEN_1>_prod`, `<TOKEN_1>~prod` and `<TOKEN_2><TOKEN_1>`.
      4. **The audit table is corrected.** "1/2/3b → 3a harmless" holds only where the key IS the
         whole value; the glued row is now **DEFECT (#110), FIXED**.

      **The guard.** `TestGluedKeyRulesReadTheKeyDetectors` counted the readers of each key. It now
      names them per key (JWT and AWS: 3c, 4d, 9e; `sk-`: 4d, 9e), so a fourth reader fails naming
      itself, not by a count. The check that every reader runs before every key detector stays.

      **Costs, pinned in `TestAcceptedOverMasking`.** A sentence dot right after a bearer key joins
      the value: `auth Bearer AKIA…EXAMPLE. retry` → `auth Bearer <TOKEN_1> retry`. RFC 6750's
      b64token admits `.`, and 3a already reads a non-key bearer token that way. A pays it too.

      **No narrowing, shown differentially.** Every input the anonymize, llm and config tests feed
      `Mask` and `residue`, on main and on the branch, plus their templatized forms: 2,814 inputs,
      per-byte coverage compared under `New()` and an extra-suffix Mapper, with the same result
      under both:
      - 0 narrowed, 0 mixed, 0 error changes;
      - 30 widened, every one a bearer value with a glued JWT or AWS key, the sentence dot
        included;
      - 2 layout-only, the `prod.AKIA…` cell raw and templatized;
      - main's own 2,744 inputs: 3 widened, #108's bearer rows (`.prod` raw and templatized,
        `_prod` templatized), and the rest unchanged.

      **Frequency.** The 70,783 records (ten boots and ab37) were run raw and templatized, old
      against new, under both Mappers: **0 changed, 0 error changes**, as predicted. No record
      contains `bearer`.

      **Tests.**
      - `TestIssue110KeyGluedInsideABearerValue`, raw and through the real `pipeline.Templatize`:
        - 14 closed rows, each with its exact output, every fragment main sent checked absent, and
          a round trip: AWS with `.`, `_`, `~`, `-` and a letter after, ASIA, JWT with `.` and `~`,
          `Authorization: Bearer`, lowercase and uppercase `bearer`, text before the key, text on
          both sides, a JWT after "bearer" in prose;
        - 12 byte-identical rows: `Bearer` followed by a whole key (AKIA, JWT, `sk-`),
          `Bearer sk-….prod`, `Bearer` with no key, `Bearer x_AKIA…`, "Bearer" in a query string,
          "bearer" as a userinfo, a JWT after "bearer" in prose and the three mixed rows;
        - the recurrence subtest and the `residue` claims.
      - Two completeness rows, two templatized rows (also run inside the logfmt wrapper), one
        over-mask row and five outputs and old leftovers fed to `TestPlaceholdersAreInert`.
      - #108's two bearer rows, "'_' after the key" and "'.' after the key", pinned #110's leak
        itself in #108's byte-identical table. They moved here as closed rows and the comment there
        says so. Every other #57 and #108 row is untouched.
      - Red on main: 24 subtests plus the guard, all by assertion. No new identifier is named, so
        the red commit compiles on main.

      **Mutants, each with what it turned red.**
      - (a) 3c removed, the mechanism off: 25. All 14 closed rows and the recurrence subtest,
        the sentence-dot row, the guard, both completeness rows, both templatized rows and their
        four logfmt forms.
      - (b) 3c after 2, the rule after the key detectors: the same 25.
      - (c) 3c also reading `sk-`: 2. The guard and the mixed `sk-` row.
      - (d) `bearer` case-sensitive: 24. Every closed row spelled `Bearer`, `BEARER` or
        `Authorization: Bearer`, the recurrence, sentence-dot, completeness, templatized and logfmt
        rows, `TestEveryDetectorHasACompletenessRow` and the guard. The lowercase and prose rows
        stay green, as they must.
      - (e) `(?i)` over the whole pattern: 2. The guard and the mixed lowercase-`akia` row.
      - (f) the leading-glue alternative dropped: 1, "text before the key".
      - (g) the "after" class not excluding `cont`: 0, equivalent as predicted. The key's own `\b`
        already ends it where its alphabet ends, so the value taken is the same.
      - (h) 3c `verify:false`: 0, equivalent as predicted. 1 and 2 already mute any raw key on the
        re-scan.
      - (i) mechanism A instead: 4. The guard and the three mixed rows.

      **The full suite** ran under `-race` on main and on the branch: 1,419 results, then 1,454.
      **No existing result changed outcome.** Two left (the moved #108 rows) and 37 came in. The
      new and touched tests pass under `-race -count=20`.

      **Wire check.** One run per binary against a capturing endpoint (`--plain --llm-anonymize
      --warmup-lines 0`). A `[FATAL] hdr Authorization: Bearer AKIAIOSFODNN7EXAMPLE.prod expired`
      line, with #59's `postgres://user:p/ss@db` in its context as the open control.
      - `.prod`: 2 before, 0 after. `AKIAIOSFODNN7EXAMPLE`: 0 in both.
      - `p/ss` fires once in both, so the grep is not vacuous.

      `internal/pipeline` does not appear in the diff. README: the Known-limitations entry goes, a
      past-tense paragraph carries the shapes, span, costs and frequency, and the audit sentence
      gains #110.

      **Found on the way:** nothing new.

      Seen, out of scope: #51, #59, #65's sweep. Targets v0.12.12

- [x] **A home-dir username ends before a closing quote or bracket** — #90, found while building
      #83. Detector 10's username class stopped at `/`, whitespace, `:`, `<` and `>`, and at nothing
      a log writes AFTER a value. So a name at the end of a quoted or bracketed value took the closer
      into its placeholder:
      - `msg="cannot read /home/deploy2prod" module=x` → `…/home/<USER_1> module=x`;
      - `{"path":"/home/bob"}` → `{"path":"/home/<USER_1>`;
      - `open("/home/bob")` → `open("/home/<USER_1>`.

      Nothing was disclosed and `Restore` round-tripped, but the model was sent a broken payload, and
      an answer naming `<USER_1>` restored with the stray characters. Raw lines (trigger and context)
      and the JSON and logfmt templates, which re-quote the message; on the text-path template only
      the non-quote closers, since `strPat` folds a quoted string into `<STR>`. v0.4.0 → v0.12.12,
      **thirty-eight tagged releases** (`git tag --contains 2da31be | wc -l` → 38; its class
      `[^/<>\s:]+` already admitted every closer). The logfmt suite had pinned it as a skipped row
      since #83.

      **The fix (`internal/anonymize/anonymize.go` only).** The body keeps today's class, and the
      name's LAST unit may not be a closer: `homeUser` is a run of `homeChar` that ends on a `homeEnd`,
      either of them a pipeline placeholder. No trailing context; leftmost-longest makes the name the
      longest such run, so a run of closers of any length stays outside. Every other detector compiles
      byte-identically (source, tag, group, verify, fixpoint and order dumped against main's under
      `New()` and an extra-suffix Mapper; only 10's source moved). `apply`, `residue`, `Restore` and
      the `detector` struct are unchanged.

      **Decisions, each pinned by a test.**
      1. **The trailing set** is `"` `'` `` ` `` `)` `]` `}` `,` `;` `.` `\`. Quotes and the backtick
         close a value in logfmt, JSON, `%q`, Python reprs and Markdown; brackets close a call, a list
         or an object; a comma and a semicolon end a field or a clause; the dot ends a sentence, as 9d
         keeps it outside an address. The backslash was added at step 0, measured rather than planned:
         inside a quoted value it escapes the closing quote (`{"msg":"open(\"/home/bob\")"}`), and
         without it the name ended on it and `<USER_1>` restored to `bob\`. Left out: `!` and `?`, which
         no formatter writes after a value, and `-`, `_` and `$`, which are legal in a username.
      2. **No narrowing but closers.** A closer INSIDE a name stays masked (`/home/o'brien/x`,
         `/home/a)b/x`, `/Users/a,b/x`), and a leading one stays inside (`/home/'bob'` →
         `/home/<USER_1>'`).
      3. **Both paths:** raw, the JSON and logfmt templates, the text template for the closers it
         keeps, and #43's tolerant end (`/home/maxie<NUM>"`, `(see /home/maxie<NUM>)`).
      4. **Restore.** Every row round-trips, `<USER_1>` never restores to a name ending in a closer,
         and a model answer naming it restores the name alone; the name bare elsewhere is the same
         placeholder. `residue()` is unchanged in kind: a raw name before a closer still reads USER.

      **Costs, pinned.** The rule has no trailing context, so a name whose LAST character is a closer
      loses it before a `/` too (`/home/bob./x` → `/home/<USER_1>./x`), and a "name" made only of
      closers is none (`/home/../etc`, `/home/./x`, left as they are). Only closers leave a
      placeholder. Removing either cost needs a second rule, since one group cannot carry the `/`
      context; the captures gate below decided it was not needed.

      **No narrowing, shown differentially.** Every input the anonymize, llm and config tests feed
      `Mask` and `residue`, on main and on the branch, plus their `Templatize` and `TemplatizeLine`
      forms: 3,036 inputs, per-byte coverage under `New()` and an extra-suffix Mapper, the same result
      under both:
      - 48 narrowed, every byte in an allowed class: 86 bytes are trailing-set characters after the new
        name's end and inside the old one, and 3 are an old name made only of closers that the new rule
        does not match (`..` and `.`);
      - 0 narrowed elsewhere, 0 widened, 0 mixed, 0 layout-only, 0 error changes;
      - main's own 2,888 inputs all unchanged; the 48 are the new tests'.

      **Frequency.** The 70,783 records (ten boots and ab37) were run raw and through `TemplatizeLine`,
      old against new, under both Mappers. Predicted: 1 to 100 changed, mostly `"`, `'` and `.`, 0 in
      "other", 0 in either cost cell, 0 error changes. Measured: **15 changed, every one a name before
      `]`**, 0 other, 0 error changes. The count, the "other" bucket, the gate and the errors held; the
      mix did not, it is all `]`. The gate: 0 records carry a closer-ended name before a `/`, and 0 a
      name made only of closers.

      **Tests.**
      - `TestIssue90HomeDirClosingPunctuation`, raw and through the real `TemplatizeLine`:
        - 23 closed rows: the issue's three lines, a JSON msg, one row per closer, an escaped quote on a
          raw JSON and a raw logfmt line, runs of closers (`x=["/home/bob"]), y` raw, as a raw logfmt
          line, and `(see [/home/bob]);.` through the logfmt template), the leading quote, both tolerant
          ends, and a JSON escape glued to the name;
        - each with its exact output, a round trip, the answer-side check and the new output re-masked
          unchanged with a clean `residue()`;
        - a raw-only row, `x="(see [/home/bob]);." y=1`, which is not logfmt (no level or message key),
          so its template is `<STR>`;
        - 10 byte-identical rows, the three cost rows, the answer-side and recurrence subtest, and the
          `residue` claim.
      - The logfmt suite's skipped `quote-edge/home-dir-last` runs again, as msg and as error, beside two
        new quote-edge rows (a tolerant end, and the run `]);.` before the wrapper's quote).
      - Two completeness rows (a closing quote, a sentence dot) and two templatized rows (a closing
        paren, a tolerant end), which also run in the logfmt wrapper.
      - Red on main: 42 subtests, all by assertion. Guard functions byte-identical.

      **Mutants, each with what it turned red.**
      - (a) closers dropped from the whole class: 6. The four closer-inside rows, the leading quote, the
        glued JSON escape.
      - (b) the trailing set without `"`: 24. Every `"` row, raw, JSON and logfmt, the answer side,
        the raw-only run, every logfmt-suite row (the wrapper closes each with a quote) and the
        closing-quote completeness row.
      - (c) tolerance lost at the end: 10. The tolerant-end rows on every path, and the trailing-digit,
        UUID and leading-quote rows, whose templates end on a pipeline placeholder.
      - (d) `.` dropped: 10. The sentence-dot rows, the dot runs and all three cost rows.
      - (e) each other closer dropped alone: `'` 2, backtick 1, `)` 16, `]` 8, `}` 5, `,` 3, `;` 5,
        backslash 3 (the escaped-quote rows and the raw logfmt run). Each turns its own row red.
      - (f) main's group restored: 40, every closed and cost row.
      - (g) one closer only, read two ways. (g1) the name, then one optional closer inside it: 38. (g2)
        exactly one trailing closer stripped (a throwaway second rule): 19, among them
        `run_of_four_closers`, every other run row (raw logfmt, logfmt template, raw-only, the logfmt
        suite's `closer-run`) and both escaped-quote rows: each ends on two closers or more.

      **The full suite** ran under `-race` on main and on the branch: 1,454 results, then 1,505. **The only
      changed outcomes are the two un-skipped rows, skip → pass**; none left, 51 came in. The new and
      touched tests pass under `-race -count=20`.

      **No wire check.** Nothing is disclosed before or after the fix, so there is no fragment to grep for
      on the wire.

      README: the Known-limitations entry goes.

      **Found on the way:** nothing new.

      Seen, out of scope:
      - **The same shape in the host rules.** 9a's, 9c's and 9e's host run admits a trailing `.` and
        `-`, so `see https://acme.com.` masks the sentence dot into `<HOST_1>`, and so do a list member
        (`mongodb://h1,h2.acme.com.`) and a key-labelled host. Confirmed with a throwaway test. Restore
        round-trips, so nothing is sent or lost, and no record of the 70,783 carries it (0 for each
        rule). Not filed. 3a's trailing `.` is already pinned as #110's cost.
      - **A JSON escape glued to a name.** `{"msg":"cannot read /home/bob\n"}` keeps `bob\n` as one name,
        as on main: `n` is a name character. Pinned, no behaviour change, not filed.
      - #59, #65's sweep, #51.

      Targets v0.12.13

- [x] **An sk- key with '-' or '_' in its tail is masked, not sent whole** — #51, filed from the
      #46 audit. Detector 3b wanted sixteen unbroken letters and digits straight after `sk-`, and
      every current key format puts a `-` or a `_` in the tail before that:
      - OpenAI's `sk-proj-…`, `sk-svcacct-…` and `sk-admin-…`;
      - Anthropic's `sk-ant-api03-…` and `sk-ant-admin01-…`;
      - a classic key with a `_` inside (`sk-AbCdEfGh_IjKl…`).

      Each was sent whole, raw (trigger and context) and on the template, and `residue()` read the
      line as clean: `key sk-proj-… rejected`, `OPENAI_API_KEY=sk-proj-…`. Only `Bearer sk-proj-…`
      was masked, by 3a. On the template a classic key whose tail starts with a digit was sent too:
      NUM takes the `-` as a sign, `sk-7AbC…` becomes `sk<NUM>AbC…`, and no `sk-` was left to anchor
      on. v0.4.0 → v0.12.13, **thirty-nine tagged releases** (`git tag --contains 2da31be | wc -l` →
      39).

      **The fix (`internal/anonymize/anonymize.go` only).** The tail is a run of the key's alphabet,
      letters, digits, `-` and `_`, carrying a WITNESS, because a kebab-case name that starts with a
      literal `sk-` word is the same alphabet:
      - sixteen unbroken letters and digits anywhere in it, the classic key's own run allowed after a
        vendor prefix (`sk-or-v1-<hex>`);
      - or an uppercase letter, with the whole tail sixteen long: a base62 or base64url body. Names on
        k8s, Docker, PyPI and npm are lowercase by rule. RE2 has no intersection, so `withUpper` is
        sixteen branches keyed on where the first uppercase letter falls.

      On the template the pipeline breaks both runs and a placeholder is the evidence, as for
      `mangled`: main's mangled form, then the rest of the run across separators, or a run holding a
      placeholder and an uppercase letter (a placeholder alone is not enough once `-` is admitted:
      `sk-worker-pool-7f9c` templatizes to `sk-worker-pool<NUM>f<NUM>c`). After `sk<NUM>` the witness is
      a letter of each case or a sixteen-run, not a floor: the placeholder stands for an unknown count
      of digits, and it is also every `sk-1`. No alternative carries a trailing assertion; each is
      maximal over the alphabet, so a key ends where its alphabet does, and that alphabet is now the sk
      key's `cont` in `keyShapes`, for 4d and 9e. Rule 11 reads the same `skLoose`. Every other
      detector compiles byte-identically. `apply`, `residue`, `Restore`, the `detector` struct and
      the order are unchanged.

      **Decisions, each pinned by a test.**
      1. **A generic tail with witnesses, no prefix list.** A prefix list goes stale, which is how #51
         happened, and misses a random tail with `_` or `-` in it.
      2. **No over-masking.** `sk-learn-compatible-estimator`, `sk-worker-pool-7f9c` (no witness),
         `task-…`, `ask-…`, `disk-…` even with a long mixed-case tail (`\b` and rule 11's lead),
         `Accept-Language: sk-SK`, `sk-Ab_Cd` and a fifteen-long uppercase tail (the floor), a fifteen-run
         after a separator, and after `sk<NUM>`: `sk-1 failed`, `region sk-2`, `sk-2XL`. Raw and
         templatized.
      3. **Edges.** `.` `,` `"` `'` `)` `]` `}` `:` stay outside the placeholder, one row each plus a JSON
         value: #90's class is not reintroduced. **A `-` or `_` at the very end is the key's**: a
         base64url body can end on either, and no formatter writes them as a closer. So text glued
         after an sk- key by either is the key's too, and #108's `key sk-…XYZ_prod used` becomes
         `key <TOKEN_1> used`. Glue before the key is still prose (`key x_<TOKEN_1> used`).
      4. **The mangled form is widened**, and the templatized, logfmt (msg and error) and quote-edge
         suites gain sk-proj, svcacct, Anthropic, `_`-inside, digit-first, env-var and sentence-dot rows;
         #51's test runs a JSON msg and a logfmt msg through the real `TemplatizeLine`.
      5. **One row per reader of `skKey`.**
         - 4d `s3://sk-proj-….prod@bucket`, 9e host label, and 3a `Bearer sk-proj-…` and `Bearer
           sk-proj-….prod` are byte-identical with main. 3c still reads only JWT and AWS.
         - Rule 11's `key x_sk-proj-… used` is closed.
         - **Layout only:** the #46 cell `postgres://sk-proj-…:password@localhost/db` was
           `<TOKEN_1>@<HOST_1>` and is `<TOKEN_1>:<TOKEN_2>@<HOST_1>`, #46's design for a key half (no
           secret byte unmasked; only the `:` leaves the placeholder). So is its mirror, and the 9e list
           member is `<HOST_2>,<HOST_1>,<HOST_3>`, #57's numbering for a classic key. A home directory
           that is such a key is `<TOKEN_1>`, not `<USER_1>`.
      6. **Restore and residue.** Every row round-trips, its output re-masks unchanged with a clean
         `residue()`, and a raw key of each format now reads TOKEN. The key in prose and in
         `OPENAI_API_KEY=` is one placeholder.

      **Residuals, pinned** (the two narrowing cells chosen at planning). A key the detectors now see
      blocks the rule that owned the value around it, the anti-correlation the audit recorded:
      `mail sk-proj-…@example.com` → `mail <TOKEN_1>@example.com` (main: `<EMAIL_1>`), and
      `peer worker-sk-proj-….corp.internal down` → `peer worker-<TOKEN_1>.<HOST_1> down` (main:
      `<HOST_1>`). And on the template a lowercase body with its digits collapsed carries neither
      witness: `key sk-proj-abcdefghij<NUM> used` is sent, as on main; the raw line is closed.

      **Costs, pinned.** A mixed-case identifier sixteen long after a literal `sk-` is a key
      (`deploy sk-MyService-Deployment` → `deploy <TOKEN_1>`), and a run of sixteen letters takes the
      words after it (`sk-internationalization-svc` → `<TOKEN_1>`; main left `-svc` beside it). On the
      template a word after `sk-v<NUM>` is absorbed (`sk-v2-worker-pool`), and mixed case after `sk<NUM>`
      is a key (`size sk-2-AbCd`); the raw line is unchanged in both.

      **The differential.** Every input the anonymize, llm and config tests feed `Mask` (recorded),
      every string literal in those tests (main's and the branch's), and their `Templatize` and
      `TemplatizeLine` forms: 9,354 form-inputs, per-byte coverage under `New()` and an extra-suffix
      Mapper, the same result under both:
      - 9,117 unchanged, 0 mixed, 0 error changes;
      - 197 widened, every run classified: 128 runs are the new sk- key shapes, 34 the `sk<NUM>`
        digit-first form (3 of them the `sk-2-AbCd` cost), 19 `_`/`-` glue after a key joining it, 7 the
        template's first form running on through a `_`, 9 the two raw costs and the `sk-v<NUM>` one;
      - 16 narrowed: the two pinned residual cells (8) and the `:` of the #46 cell and its mirror (8);
      - 4 tag-only (the home directory) and 4 layout-only (the 9e member);
      - 16 that the harness cannot align (fail-closed errors, and inputs carrying our own placeholder
        syntax), identical on both sides.

      **Frequency.** The 70,783 records (ten boots and ab37; 310 have a non-string MESSAGE) raw,
      through `Templatize` and through `TemplatizeLine`, old against new, under both Mappers. Predicted
      per witness: 0 changed by the sixteen-run, 0 by the uppercase witness, 0 by the `sk<NUM>` form, a
      handful of standalone `\bsk-` records at most, 0 error changes. Measured: **0 changed** under each
      witness and both Mappers, 0 error changes, and **0 records with a standalone `\bsk-`** (the 396 with
      `sk-` have it inside a word) and 0 with an `sk<NUM>` on either template. The three small laptop and
      home captures (1,643 records) agree: 0 changed. The over-masking cost is measured at zero on this
      corpus, not assumed.

      **Wire check.** A capturing endpoint, `--plain --llm-anonymize --warmup-lines 0`, v0.12.13 built
      from 1bd653f against the branch, with an sk-proj line, an `OPENAI_API_KEY=sk-proj-…` line, an
      `sk-ant-api03-…` line, a digit-first classic key and `postgres://user:p/ss@db` in the context. Key
      fragments in the request bodies on v0.12.13: 7 for each sk-proj fragment, 3 for each Anthropic
      one, and 1 for the digit-first key (its `sk<NUM>` template field); **0** for every one on the
      branch. `p/ss` is sent by both, the open #59 control.

      **Tests.**
      - `TestIssue51SkKeyWithASeparatorInItsTail`, raw and through the real `TemplatizeLine`: 31 closed
        rows (each format, both witnesses at their floors, the digit-first form raw, logfmt and JSON,
        env vars, JSON and logfmt wrappers, one row per closer, a trailing `-` and `_`, lowercase glue
        after, rule 11), each with its exact output, the fragments main sent checked absent, a round
        trip, and the output re-masked unchanged with a clean `residue()`; 5 byte-identical reader rows,
        4 layout rows, 3 residuals, 2 template costs, 12 must-stays raw and templatized, recurrence, and
        the `residue` claims.
      - New rows in the completeness (8), templatized (9), quote-edge (2), type-tag, inert (6) and both
        over-masking tables, and two raw costs in `TestAcceptedOverMasking`.
      - Changed rows, each with a comment: #108's two `_`-after sk rows, completeness `sk-key/glued` and
        templatized `sk-key/glued-underscore-before` (the `_prod` is the key's), and #108's `sk-proj` same
        row, which moved into #51's test.
      - Red on main: every closed, layout, residual and cost row, by assertion. Guard functions
        byte-identical. The full suite under `-race`: 1,505 results on main, 1,588 on the branch, 0
        failing on either; no existing test changed outcome. New and touched tests pass under
        `-race -count=20`.

      **Mutants, each with what it turned red.**
      - (a) the unbroken-run class restored: 72 subtests, `TestTypeTags` and
        `TestNoDetectorGroupLandsOnOurOwnOutput`.
      - (b) `_` dropped: 57, among them both #108 rows and every Anthropic and `_`-inside row.
      - (c) `-` dropped: 60, every vendor-prefix row and both raw costs.
      - (d1) the uppercase floor lowered to 2: `sk-SK` in both over-masking tests, `sk-Ab_Cd`, the
        fifteen-long tail, `sk-2XL` and the `sk-2-AbCd` cost row.
      - (d2) the run witness lowered to 4: `sk-learn-compatible-estimator`, `sk-worker-pool-7f9c` (raw and
        template), the fifteen-run, the fifteen-long tail, the lowercase residual and both template
        costs.
      - (e) the mangled form left unwidened: 59, among them #57's two sk rows.
      - (f) no witness (any sixteen of the alphabet): #51's must-stays and both over-masking tests.
      - (g) the run witness dropped: 7, the hex vendor rows, every lowercase sixteen-run and the
        sixteen-letter cost.
      - (h) sk `cont` left alphanumeric: 2, the #46 layout rows (4d then takes the value whole again).
      - (i) the template's first form not run across separators: 5, the lowercase `_prod` rows and the
        userinfo `url=s3://sk-…xyz_prod@…` row, whose template went to `<TOKEN_1><EMAIL_1>`.
      - (j) the `sk<NUM>` form dropped: 14, every digit-first row.
      - (k) its witness dropped: `sk-1 failed`, `region sk-2`, `sk-2XL`. (k2) one case allowed: `sk-2XL`.

      **Cost in time.** Matching is unchanged (every detector timed over a whole boot's records, main
      against branch, within noise; `Mask` on a typical payload within noise). Compiling is not: `New()`
      with extra host suffixes, which recompiles the chain, went from about 2.0 ms to 4.9 ms. Production
      builds one Mapper per escalation, capped by `--rate-limit`, and the default Mapper reuses the
      compiled chain.

      README: the Known-limitations entry goes, and a #114 entry comes in (below).

      **Found on the way.** Stripe (`sk_live_`, `rk_live_`, `sk_test_`), GitHub (`ghp_`, `github_pat_`),
      Slack (`xoxb-`, `xoxp-`) and Google (`AIza…`) secrets match no detector and are sent whole, raw and
      on the template, on main and on the branch. Not #51's class: 3b reads `sk-` with a hyphen. Filed as
      #114 and named in README's Known limitations (Done with #114, in v0.12.15.).

      Seen, out of scope:
      - **A bare `KEY=value`.** `OPENAI_API_KEY=<a value that is not an sk- key>` is sent; no rule reads
        the variable name. Confirmed with a throwaway test, reported, not filed.
      - #59, #49, #42, #65's sweep, #91, #92.

      Targets v0.12.14

- [x] **Stripe, GitHub, Slack and Google secrets are masked, not sent whole** — #114, found on the
      way to #51. No detector had a vendor format's shape (3b reads `sk-` with a hyphen only), so each
      of these was sent whole, raw (trigger and context) and on the template, and `residue()` read the
      line as clean:
      - Stripe `sk_live_…`, `sk_test_…`, `rk_live_…`, `rk_test_…` and the webhook signing secret
        `whsec_…`;
      - GitHub `ghp_…`, `gho_…`, `ghu_…`, `ghs_…`, `ghr_…` and the fine-grained `github_pat_…`;
      - Slack `xoxb-…`, `xoxp-…`, `xoxa-…`, `xoxr-…`, `xoxs-…` and the app-level `xapp-…`;
      - Google `AIza…`.

      The leak was confined to text no rule claims: prose, a URL query value (`…/geocode/json?…&key=AIza…`,
      the commonest real placement of a Google key), a JSON value, an env var. Every rule that owns a
      value around such a key already masked it whole on main, because their classes admit the key's
      alphabet: a userinfo half (4), a host or a list member (9a, 9c), a bare private host (9b), an email
      local part (5), a home directory (10), a bearer value (3a). v0.4.0 → v0.12.14, **forty tagged
      releases** (`git tag --contains 2da31be | wc -l` → 40).

      **The fix (`internal/anonymize/anonymize.go` only).** Rule 12 is one verify-eligible TOKEN rule
      per format family, appended after rule 11 through `looseKey` (the character before the prefix is
      context, outside `A-Za-z0-9`). Each raw body is the format's alphabet from its shortest published
      length up, with no upper bound and no trailing assertion, so a key ends where its alphabet does:

      | Family | Prefix | Raw body | Template floor |
      |---|---|---|---|
      | Stripe | `sk_`/`rk_` × `live_`/`test_` | 24+ base62 (test and legacy keys are 24; current ones ~99) | 8 units |
      | | `whsec_` | 32+ base62 (the Stripe CLI's is 64 hex) | 8 units |
      | GitHub classic | `ghp_` `gho_` `ghu_` `ghs_` `ghr_` | 36+ base62 (30 random + a 6-character checksum) | 12 units |
      | GitHub fine-grained | `github_pat_` | 82+ of base62 and `_` (published 22 `_` 59; the split is not relied on) | 24 units |
      | Slack | `xoxb-` `xoxp-` `xoxa-` `xoxr-` `xoxs-` `xapp-` | a digit first, then `-`-separated segments carrying an unbroken run of 16 | after `<NUM>`, a letter of each case or a 16-run |
      | Google | `AIza` | 35+ base64url (published: exactly 35) | 12 units |

      On the template every digit run is `<NUM>`, and a pipeline placeholder is the evidence, as in
      `mangled`, over a floor in UNITS (a class character or a whole placeholder): `mangledFloor` is two
      branches that never match fewer than n units and match every placeholder-carrying run of 2n−2 or
      more. The floors are measured, not guessed: over 200,000 random bodies per format the fewest units
      left were 14 (Stripe 24), 22 (`whsec_` base62), 13 (`whsec_` hex), 27 (`ghp_`), 69 (`github_pat_`)
      and 25 (`AIza`), and every one of those templates was masked whole against the compiled rules,
      raw too. Slack's leading `-` and first numeric segment become one `<NUM>` (NUM takes the `-` as a
      sign), so after `xoxb<NUM>` the witness is #51's `sk<NUM>` one, factored into `caseOrRun`;
      `skDigitFirst` compiles byte-identically. Where I am unsure of a length (Slack per-type segments,
      the fine-grained split, a future longer Stripe key), the floor is the shortest published form and
      the run is unbounded above.

      **Decisions, each pinned by a test.**
      1. **Run last, one rule per family.** At 3b's slot a vendor rule would mint first and block the
         value rules above at our `<`, #46's and #51's anti-correlation: the email domain and the
         labels before a bare host's key label would be sent, and #46's cell would change layout. Run
         after rule 11 it reads only text no rule claimed, so every rule before it sees main's input,
         main's chain is a byte-identical prefix (a source/tag/group/verify dump of both chains), and
         nothing can narrow. Per family rather than combined, so the completeness, templatized and
         context guards hold for each format, not for one row of a combined rule; GitHub is two
         families because its alphabets and floors differ.
      2. **No over-masking**, raw and templatized, and what keeps each: the floor (`sk_test_helper`,
         `sk_live_mode`, `whsec_rotated`, `ghp_cache_dir`, `AIzaTest`, one short of each raw floor in
         letters); the alphabet (`sk_test_create_customer_with_card_1`, where `_` ends the body); the
         template floor (`sk_test_run42`, `whsec_v2_rotation`, `ghp_v2`, `AIza_test_1`,
         `github_pat_rotation_policy_v2_enabled`, short digit-bearing tails); the Slack witness
         (`xoxb token missing`, `the xoxb- prefix`, `set xoxb-1 retries`, a kebab-case date-and-words
         name after `xoxb-`, and a 24-letter word after `xoxb-`, which only the digit-first rule keeps); and the lead
         (`disk_test_…`, `ask_live_…`, `xghp_…`, `mygithub_pat_…`, `bAIza…`, a letter before `xoxb-…`).
      3. **Edges, as #57 and #108 decided them.** `.` `,` `"` `'` `)` `]` `}` `:` stay outside the
         placeholder, one row per closer per family. Glue BEFORE the prefix (`_` `-` `=` `:`) is prose:
         `token_<TOKEN_1>`. AFTER the key the format's alphabet decides: `_prod` and `-prod` stay prose
         after a Stripe or `ghp_` key; `_` is the key's for `github_pat_` and Google, `-` for Slack and
         Google.
      4. **The template.** Every family's prose, env var, logfmt and JSON msg, JSON value (`<STR>`) and
         query-value placements are closed through the real `TemplatizeLine`, and each family has a
         templatized, a logfmt (msg and error) and a quote-edge row.
      5. **Readers: none.** The shapes are not keyShapes. That list exists so that 4d, 9e and 3c can see
         a key inside a larger value before a strict detector tags it; no strict detector tags a vendor
         key, so 4, 9a and 9c already take such a value whole. Joining it would duplicate them, run them
         earlier and compile each shape about eight times. `TestGluedKeyRulesReadTheKeyDetectors` now
         holds each family to exactly one detector, a verify-eligible TOKEN rule with group 1 among the
         chain's last, one per family, whose shape no other detector carries. One byte-identical row per
         would-be reader and per family: 4d's position `s3://<key>.prod@bucket`, 9e's host label and
         list member, 3c's `Bearer <key>.prod`, the email, the bare private host, the home directory and
         #46's cell `postgres://<key>:password@localhost/db` → `postgres://<TOKEN_1>@<HOST_1>/db`.
      6. **Restore and residue.** Every closed row round-trips; its output re-masks unchanged with a
         clean `residue()`; no key body survives; a raw key of each format now reads TOKEN where main
         read `""`; the key in prose and in its env var is one placeholder.

      **Residuals, pinned.** The raw line is closed in every placement. On the template a Slack user or
      app token's secret segment is already `<HEX>`, so nothing of it is left and the template is main's
      (`xoxp<NUM><NUM><NUM>-<HEX>`). But with a word character glued right after an `xoxp-…` key, the
      hex segment has no `\b` to end on, NUM takes its digits, and the template sends its letters a–f,
      as main does. Not widened here; filed with the formats below as #116.

      **Costs, pinned.** Raw: a camelCase identifier at least as long as a raw floor after a vendor
      prefix is a key (`sk_test_` and a 31-letter camelCase test name → `<TOKEN_1>`). Template:
      a tail between the template floor and the raw floor that carries a digit is a key there
      (`sk_test_Abc123defghij`, `whsec_Abc123defghij`, `ghp_` + 17, `github_pat_` + 27, `AIza` + 30), and
      so is mixed case after `xoxb<NUM>` (`size xoxb-12-Ab`); the raw line is unchanged in each.

      **The differential.** Every input any package's tests feed `Mask` (recorded through an overlay
      hook; 2,118 distinct, 1,409 of them from main's own tests) and every string literal in the
      repository's tests (5,013), raw, through `Templatize` and through `TemplatizeLine`, per-byte
      coverage under `New()` and the extra-suffix Mapper (`k8s acme1 eu-1 v1.2 deadbeef 2corp`):
      - main's own inputs: 6,920 cells, **all identical** (24 fail closed on both sides);
      - the literals: 28,454 cells, all identical (every vendor value in the tests is split at its prefix);
      - the branch's inputs: 9,778 cells, 8,262 identical, **1,492 widened, 0 narrowed, 0 layout-only,
        0 error changes**. Every widened run starts at a vendor prefix and is classified: 422 runs a test
        key raw, 10 a key with the glue its alphabet takes, 1,012 a test key templated, 42 the pinned
        template costs, 6 the pinned raw cost.
      - main's test file run against the branch's code fails only `TestEveryDetectorHasACompletenessRow`,
        naming the five new detectors.

      **Frequency.** The 70,783 records (ten boots and ab37) and the three small laptop and home
      captures (1,643), each record's MESSAGE and its whole JSON line, raw, through `Templatize` and
      through `TemplatizeLine`, old against new, under both Mappers. Predicted before the run: 0 changed,
      0 error changes, 0 records with a standalone vendor prefix. Measured: **0 changed** in 217,058
      cells (54,090 message and 155,120 line cells; 3,348 and 4,500 in the small captures), so 0 per
      family and per rule, 0 error changes, and **0 records with a vendor prefix anywhere**. The
      prediction held; the over-masking cost is measured at zero on this corpus, not assumed.

      **Wire check.** A capturing endpoint, `--plain --llm-anonymize --warmup 0 --warmup-lines 0`,
      v0.12.14 built from 9feb036 against the branch, one `[FATAL]` line per family (Stripe `sk_live_…`
      in prose, `whsec_…`, `ghp_…`, `github_pat_…`, `xoxb-…`, `xoxp-…`, and a Google key as the geocode
      query value) and `postgres://user:p/ss@db` as the open #59 control. Eight requests each. Key
      fragments in the request bodies on v0.12.14: every family's raw fragment sent (8, 7, 6, 5, 4, 3 and
      2 times, the context ring carrying each line into the later requests) and every template fragment
      once (`xoxp-…`'s template is `<HEX>` on both); **0** for every one on the branch. `p/ss` is sent
      twice by both.

      **Tests.**
      - `TestIssue114VendorSecretFormats`, raw and through the real `TemplatizeLine`, over ten keys (three
        Stripe, three GitHub, three Slack, one Google): 20 closed contexts per key (prose, env var, logfmt
        and JSON msg, JSON value, the Google, Stripe and GitHub query values, four glue-before rows, one
        row per closer), each with its exact output, the fragments main sent checked absent, a round trip
        and the output re-masked unchanged with a clean `residue()`; 14 glue-after rows; 11
        byte-identical reader rows per key; 3 residuals; 7 template costs; 37 must-stays; recurrence and
        `residue()` per key.
      - New rows in the completeness (17), templatized (13), quote-edge (5), type-tag (5), inert (11) and
        both over-masking tables, and a raw cost in `TestAcceptedOverMasking`. No existing row changed.
      - Red on main: 8 tests by assertion (every closed, glue-after, cost, residual and recurrence row);
        no reader row and no must-stay fails there. Guard functions byte-identical; the test commit is
        additions only. The full suite under `-race`: 1,591 passing and 1 skipped on main, 2,003 and 1 on
        the branch, 0 failing on either; no existing test changed outcome. New and touched tests pass
        under `-race -count=20`.

      **Mutants, each with what it turned red** (38, all killed).
      - Prefix dropped: `rk_`, the test mode, `whsec_`, `ghs_`, `github_pat_`, `xoxp`, `xapp` and `AIza`,
        each its family's closed rows and recurrence; `github_pat_` and `AIza` also the reader guard and
        `TestEveryDetectorHasACompletenessRow`.
      - Raw floor lowered (Stripe 24→6, `whsec_` 32→7, `ghp_` 36→5, `github_pat_` 82→20, Google 35→4,
        Slack's run 16→4): the must-stays and the over-masking tests.
      - Template floor n→1, per family: the short digit-bearing must-stays (`sk_test_run42`,
        `whsec_v2_rotation`, `ghp_v2`, `github_pat_…_v2_enabled`, `AIza_test_1`).
      - Template form dropped, per family: the templatized, logfmt and closed template rows, and
        `TestEveryUncollapsedDetectorHasATemplatizedRow` for `ghp_`, `github_pat_`, Google and Slack.
      - Slack: digit-first dropped (the 24-letter word after `xoxb-`), the raw witness dropped (the
        kebab-case must-stays), the template witness dropped (`xoxb<NUM>` rows and the inert list).
      - The lead admitting letters, per family: `disk_test_…` and `ask_live_…`, `xghp_…`, `mygithub_pat_…`,
        `bAIza…`, and a letter before `xoxb-…`.
      - Rule 12 not appended: every #114 row and the reader guard. `verify:false`: the reader guard and the
        `residue()` rows. Rule 12 at 3b's slot: the reader guard and the byte-identical reader rows.
        The shapes added to keyShapes: the same. One combined rule: the reader guard.

      **Cost in time.** Main against the branch, medians of eight runs: `buildDetectors(nil)` 4.86 →
      5.60 ms (×1.15), `New()` with extra host suffixes 4.97 → 5.74 ms (×1.16), and `Mask` over a
      typical seven-line payload (trigger, template, five context lines) with a fresh Mapper 0.86 →
      1.04 ms (×1.21), five more scans per string. Production builds one Mapper per escalation, capped by
      `--rate-limit`, and the default Mapper reuses the compiled chain.

      README: the Known-limitations entry goes.

      **Found on the way.** Other vendors' secrets match no detector either, confirmed with a throwaway
      test and invented values, on main and on the branch: AWS secret access keys, Twilio `SK…`/`AC…`,
      SendGrid `SG.…`, npm `npm_…`, GitLab `glpat-…`, Hugging Face `hf_…`, Shopify `shpat_…` and PyPI
      `pypi-…` are sent whole on the raw line and minus their digits on the template (a Hugging Face
      token has none to lose; a Twilio `AC…` SID is `<HEX>` there). In a Slack incoming-webhook URL
      (`hooks.slack.com/services/T…/B…/…`) or a Discord webhook URL the host is masked and the secret
      path segment is sent. Not #114's class as a whole: the AWS secret has no prefix, only a variable
      name or a neighbouring key id to know it by, and a webhook's secret is in the URL path, which no
      rule reads. Filed as #116 with the `xoxp-…_prod` template residual above, and named in README's
      Known limitations.

      Seen, out of scope: a bare `KEY=value`, #59, #49, #42, #65's sweep, #91, #92 and
      `internal/pipeline` were not touched.

      Targets v0.12.15

- [x] **Replay keeps a journal entry whose message is empty, as a live run does** — #87, found
      on the way in #37. `ReplaySource.Lines` skipped a line whose `Raw` was empty ("not a journal
      entry; journalctl never writes one"), but the check ran after `replayDecode`. It dropped two
      things it could not tell apart: a blank line in the capture file, the skip it meant, and a real
      entry with `MESSAGE` `""`, which a live `--journald` run emits as an empty line at its priority
      (priority 3: an empty `ERROR` line on stderr). Replay promises the live run's answer (#35), and
      every captured boot holds exactly one such entry (kernel, priority 3), so a replay scored one
      line fewer than the run it recorded.

      **Fix.** The skip runs on the raw capture line, before decode. `mapLines`' transform cannot drop
      a line, so its read/emit loop is written out in `replay.go` as `replayLines`, as
      `readDockerStream`'s is. With `model.Stdout` and `replayDecode` substituted, `mapLines`' body
      and `replayLines`' differ by the skip alone (checked by diff), so the goroutine, the
      cancellation and the error return are `mapLines`' own. `\r\n` alone is still a blank line; a
      whitespace-only line is decoded and emitted, as before. The live path, `decode`, `mapLines` and
      `readLines` are untouched.

      Both paths dropping the line was considered and rejected. The pipeline never drops an empty
      line, and the coalescer reads a blank line as a continuation of an open group (Go's blank line
      between `panic:` and the goroutine dump), so dropping blanks live would change coalesced
      templates.

      **Tests.**
      - `TestReplayMatchesLiveJournald`: the same capture, through the live source with a stand-in
        `journalctl` and through `ReplaySource`, gives the same lines (raw, level, stream, source and
        time); the empty entry comes out as an empty `ERROR` line on stderr from `journald:kernel`.
        Red on main by assertion (2 lines, live 3).
      - `TestReplaySkipsBlankCaptureLines`: a blank line, and one holding only `\r`, replay as
        nothing. It passes on main and on the branch.
      - The full suite under `-race`: 2,003 passing and 1 skipped on main, 2,005 and 1 on the branch,
        0 failing on either; no existing test changed outcome. The new tests pass under
        `-race -count=20`.

      **Captures.** The ten journald boots replayed at max speed through the pipeline with default
      settings, main against the branch. Predicted before the run: one more line per boot and no change
      in escalations, since the entry sits about a second into each capture and the replay clock is the
      capture's, so warmup still mutes novelty and stderr + `ERROR` is 0.90, under the threshold. Both
      held on every boot: 63,007 → 63,017 lines (+1 each; each boot now emits one line per entry in
      its file), 59 → 59 escalations (+0 each).

      README: the Known-limitations entry goes, and so does the sentence in the replay section
      that pointed to it.

      Targets v0.12.16

- [x] **A Docker follower's context, and a failed list's events subscription, are released** —
      #98, found while fixing #85 and extended while fixing #97. Two retentions, neither with a
      functional effect:
      - Each follower runs under `context.WithCancel` on `Lines`' context, and its cancel func was
        held only in the follower's entry. One whose log stream ends before its `die` (the
        container stops, the daemon ends the stream, the `die` arrives a moment later) deletes its
        own entry, so the later `stop` found nothing and the cancel func was never called. The
        child stayed registered until the source ended: about 116 bytes per such run, growing with
        restarts. There since v0.1.0.
      - `Lines` subscribed to events and listed containers on the caller's ctx. When the list
        failed, it returned `docker: list: …`, but the events goroutine and its HTTP connection
        stayed open until the run's ctx ended, since `cli.Close` closes only idle connections.

      **Fix.** The follower goroutine cancels its own context once `follow` returns, however the
      follower ended. A `stop` that lands before the entry is removed cancels again, a no-op. The
      mark hand-off, the restart path and `stop` are unchanged, and a successor's context is its
      own child of the set's, so it is untouched. `Events` and `ContainerList` now run under
      `lctx`, which `Lines` cancels on every return, so a list error closes the events request
      with it. The shutdown and events-error paths keep their behaviour: `lctx` is cancelled only
      by the caller's ctx, by the events-error branch after it has received the error, or by the
      return itself, so no cancellation of `lctx` can come first and read as `docker: events`.
      `internal/ingest/docker.go` only.

      **Tests.**
      - `TestFollowerSetReleasesEveryFollowersContext`, through the fake follow function: after
        the follower exits, the context it was handed is cancelled, while the set's own is still
        live. It covers a follower that ends on its own and a successor that ends on its own (both
        red on main by assertion), and a stopped follower as the control.
      - `TestDockerListErrorClosesTheEventsSubscription`, against the fake daemon with its list
        answering 500: `Lines` returns the list error, and the events request closes on the server
        side without the caller's ctx being cancelled. Red on main by assertion (still open 2 s
        after `Lines` returned). The fake daemon gains a failing list and a signal for an events
        stream the client hung up on.
      - Reverting either half alone turns only its own test red. `ContainerList` under `lctx` is
        not pinned by a test of its own; it returns before `Lines` goes on either way.
      - The full suite under `-race`: 2,005 passing and 1 skipped on main, 2,010 and 1 on the
        branch, 0 failing on either; no existing test changed outcome. The new tests, and every
        Docker and follower-set test, pass under `-race -count=20`.

      No Docker captures exist, so there is nothing to replay. README: no change; it does not
      document either retention.

      Targets v0.12.17

- [x] **Every dump the Go runtime writes is one event** — #94, found while fixing #89. Since
      Go 1.23, `GOTRACEBACK=system` and `crash`, and every SIGQUIT dump, write each goroutine
      header as `goroutine 1 gp=0x… m=0 mp=0x… [running]:`. The coalescer split such a dump at
      every goroutine, and each piece became its own "novel" template. It also split a dump that
      begins at the goroutine header, as `debug.Stack()` written on its own does.

      **Why it split.** The issue named three causes. The runtime source showed two more for
      SIGQUIT:
      - the header cue was `^goroutine \d+ \[`, which the `gp=` form does not match;
      - a dump starting at the header opened no block, so its first un-indented call line
        (`runtime/debug.Stack()`) started a new event, since `goFrameRe` is gated on an active
        block;
      - with the indentation stripped, assembly frames (`asm_amd64.s:1771 +0x1 fp=…`) matched
        nothing, since the frame cue was `.go`-only;
      - the `PC=0x… m=0 sigcode=0` line written after `SIGQUIT: quit` matched nothing;
      - neither did the register dump at the end (`rax    0xca` … `gs     0x0`, 21 lines on
        amd64).

      **The cues.** All of them are in `coalesce.go`.
      - **Goroutine header**, both forms: `goroutine N`, then only `key=value` fields, then the
        bracketed state, and the line ends at `]:`. It continues a dump and opens one
        (`traceStartRe`).
      - **Assembly frame**: the `.go` frame's whole-line shape with a `.s` file, and the ` +0x`
        offset required. Assembly is never inlined, so a real asm frame always prints one, and a
        bare `boot.s:12` is not a frame.
      - **Signal PC line**: `PC=0x… m=N sigcode=N`, optionally ` addr=0x…`. It is ungated. The
        signal's name line (`SIGQUIT: quit`) is a plain header that this line folds into, which
        makes the block active. No regex reads `SIGQUIT` text, so prose that mentions it opens
        nothing.
      - **Register line**: `name<spaces>0xHEX` and nothing else. It is gated on an active block,
        like `goFrameRe`, because the shape is too generic to stand alone.
      - Openers: `panic:` and `fatal error:` already existed. The goroutine header is the only
        new one.

      **A behaviour change.** The header cue used to be open-ended. It now ends at `]:`, so a
      bracketed line such as `goroutine 5 [worker] started` no longer folds into a dump before
      it. In the ten boots and ab37 (70,783 lines), 0 lines match the old open-ended cue, so
      nothing in them moves.

      **Not changed.** Two dumps that each *start* at a goroutine header, such as two
      `debug.Stack()` writes inside the group timeout, still fold into one. They cannot be told
      apart from one dump with two goroutines, and the header continued a block on main too.
      This is not pinned by a test.

      **`GOTRACEBACK=crash` is one event, like `system`.** After the dump, the runtime re-raises
      SIGABRT with the default handler, which prints nothing. The plan expected a second
      `SIGABRT: abort` dump, and the real output showed there isn't one.

      **Fixtures.** `internal/pipeline/testdata/godump/{system,crash,sigquit,stack}.txt` are the
      stderr of a real Go 1.26.4 binary. It is the existing `goroutineDump` program: an
      index-out-of-range panic beside one parked worker goroutine, the same worker plus a SIGQUIT
      the program sends itself, and `debug.Stack()` written alone. The build path is replaced
      with `/build/app`, GOROOT with `/usr/local/go`, and every address with a deterministic
      fake of the same width. The `GOTRACEBACK=all` row is the existing inline `goroutineDump`.

      | dump | lines | events on main, tabbed / stripped | on the branch |
      |---|---|---|---|
      | panic, `GOTRACEBACK=all` | 13 | 1 / 1 | 1 / 1 |
      | panic, `GOTRACEBACK=system` | 97 | 15 / 25 | 1 / 1 |
      | panic, `GOTRACEBACK=crash` | 97 | 15 / 25 | 1 / 1 |
      | SIGQUIT | 120 | 38 / 48 | 1 / 1 |
      | `debug.Stack()` written alone | 5 | 2 / 2 | 1 / 1 |

      The fixture program has one more goroutine than the issue's dumps (87 → 13 / 22 and
      116 → 38 / 49), hence the different counts.

      **Tests.** Red first.
      - `TestGoDumpsFoldIntoOneEvent`: each dump above, tabbed and stripped, is one event holding
        every line. So does a system panic followed by a SIGQUIT dump, which is two events.
      - `TestGoDumpCueShapes`: each cue line by line, on an idle header and in an active block.
      - `TestNotPartOfAGoDump` covers lines that stay their own event:
        - `goroutine 5 started`, after a dump and as a header followed by `retry(3) scheduled`;
        - the bracketed `goroutine 5 [worker] started`;
        - SIGQUIT in prose;
        - a bare `/src/boot.s:12` and lines ending in `.s:12`;
        - two `GOTRACEBACK=all` panics back to back, tabbed and stripped;
        - an `ERROR:` line and #89's slog line, after each fixture, tabbed and stripped.

        All of these pass on main except the bracketed row, which is the behaviour change.
      - Mutants, each applied alone:
        - header cue reverted: system, crash, SIGQUIT, back-to-back, the `gp=` shape rows and
          the bracketed rows go red;
        - header dropped from `traceStartRe`: `debug.Stack()` ×2;
        - `.s` cue removed: the stripped system, crash, SIGQUIT and back-to-back rows, and the
          `.s` shape rows;
        - header cue loosened to `^goroutine \d+\b`: both `goroutine 5 started` rows and the
          bracketed rows;
        - PC cue removed: SIGQUIT ×2, back-to-back ×2 and the PC shape rows;
        - register cue removed: SIGQUIT ×2, back-to-back ×2 and the register shape rows;
        - `]:$` dropped: the bracketed rows.
      - The full suite under `-race`: 2,010 passing and 1 skipped on main, 2,078 and 1 on the
        branch. No existing test changed outcome. The new tests pass under `-race -count=20`.

      **Captures, predicted first.** The prediction: +0 events, +0 templates, +0 escalations on
      the ten journald boots and ab37, with exports byte-identical. No goroutine header of either
      form appears in them. The new cues are whole-line runtime shapes, and the register cue
      only applies inside a block. **It held.** Main and branch match on all 11 at the default
      200 ms timeout on the source clock: 58,405 events from 70,783 lines and 60 escalations, and
      distinct templates match capture by capture. All 11 exports are byte-identical. No capture
      line matches any of the new cues.

      README: the #94 Known-limitations entry goes, and the grouping paragraph names the `.s`
      frame.

      Targets v0.12.18

- [x] **Lost export records are counted, announced, exit-coded and recorded** — #70, found
      while working on #35. The live export writer loses a record rather than stall ingestion
      when its 1024-slot queue is full. The only report was one stderr line at exit. It counted
      queue messages (two per record) but said "record(s)", the run exited 0, and the file gave
      no sign that it was incomplete. The issue measured 3,061 records lost, reported as
      "6102 record(s) dropped". Making the live writer blocking stays out of scope (RDI §3), and
      replay's blocking writer is unchanged.

      **Found while reading, and fixed here: a record could mix the halves of two
      escalations.** The writer joined a flag and its resolution by template hash only. When one
      half was dropped, its partner still went in, waited unmatched, and was joined to the
      *next* escalation of the same template. The file then held one record with one
      escalation's `count_at_flag` and another's explanation, and nothing in it could tell. This
      is a correctness fix, not just a counting one.
      `TestDroppedFlagNeverPairsWithALaterEscalation` and
      `TestDroppedResolutionNeverPairsWithALaterEscalation` show it on main, one test per
      dropped half: the second escalation's record carries `count_at_flag 2` with the first
      answer's `explanation.at`, and `count_at_flag 1` with the second's.

      **Decisions.**
      1. **Records, not messages, and the pairing.** `send` gives each pair a sequence number
         under a mutex. It records which half arrived first and whether it was queued, in
         `pairs`, one entry per template. The arrival rules:
         - the first half of a pair takes a new number; if the queue is full, the record is
           lost at that moment, once;
         - the other half of a lost pair is dropped on arrival, **even with room in the
           queue**, so no orphan half reaches the writer;
         - the other half of a queued pair takes the same number; if *it* is the half that
           can't be queued, the record is lost then, and its first half is already in the
           writer;
         - a half from the same side as the waiting one starts a new pair, as the newer flag
           replaced the older on main.

         The writer joins only halves with equal numbers. A waiting half with a different
         number is stale and is discarded when the next half for that template arrives. So
         the first half of a record whose resolution was lost is never written, and never
         joins a later one. `Dropped()` keeps its name and counts records.

         **`pairs` stays bounded.** A flag gets exactly one resolution on every path but one:
         - dry-run resolves it at once (`WouldEscalate`);
         - a full LLM queue resolves it at once as failed;
         - a queued request gets exactly one terminal answer from the pool, explained or
           unavailable, an anonymization failure included; partial answers are not
           resolutions;
         - a cache hit makes no flag.

         The one exception is an answer still pending when the run ends. Its entry is left
         behind until exit, at most one per template, and the next escalation of that template
         replaces it. `TestALeftBehindHalfIsReplaced` pins this: 100 templates flagged three
         times unanswered hold 100 entries, and one answered escalation each leaves none, with
         every record the new one.
      2. **Shown live, through the existing mechanisms.**
         - `--plain`: the output loop checks `Dropped()` once per iteration. The first time it
           moves, one stderr line: `logscry: export: records are being lost — the export file
           is not keeping up; the total is reported at exit`. A loss follows a flag, which an
           event to this loop always follows, or a resolution this loop makes itself.
         - TUI: `Options.ExportLost` is read on every snapshot, and snapshots come on a ticker,
           so an idle stream still sees it. The first move adds a background notice. A status
           field ` · export lost N` in the protected counters segment shows the running count
           and is hidden at 0.
         - At exit: `export: N record(s) lost — the export file could not keep up`.
      3. **Exit status 1 when records were lost, in `--plain` and the TUI.** The codes in use
         were 0 (the run finished, source errors and Ctrl-C included, per #97) and 1 (`run`
         returned an error: a bad flag or config, `-h`, an unopenable `--export`, no log
         source, a TUI failure). A loss is now one of the reasons for 1, through the same path.
         `closeExport` returns it, and a deferred assignment in `run` makes it the error when
         `run` has none of its own. Otherwise it is printed beside that error, and the status
         is 1 either way. The TUI exits 1 too, because the TUI is chosen automatically at a
         terminal. Were the status renderer-specific, the same `logscry --export f -- ./app`
         would leave the same incomplete file and exit 1 in CI but 0 at a terminal. A write
         error with nothing lost is still printed and not exit-coded. README gains an
         "Exit status" section.
      4. **The file describes itself.** When N > 0 its last line is
         `{"kind":"export_incomplete","records_lost":N,"at":"<RFC3339 UTC>"}`.
         - `records_lost` names its unit because a unit confusion was the bug, like
           `count_at_flag`.
         - `at` places the marker among other runs' records in an appended file.
         - It is a kind of its own, not a zero-filled anomaly that a count would include.
         - It is written only when N > 0, so a complete file is byte-identical to main's.

         The readers of export files in the repo are the export, pipeline, cmd and replay
         tests and README's jq examples. The tests never see it, because they run without loss
         or through the blocking writer. Both jq examples (`select(.kind == "anomaly")` and
         `select(.explanation.state == "explained")`) skip it, and a plain line count includes
         it, which README says.

      **Close is bounded on the live writer, by progress and not by time.** The writer bumps a
      counter after every message it handles and every write it attempts. Close gives up after
      10 s in which the counter did not move. That is about 1000× a healthy fsync, and above
      the few-second ext4 writeback stalls, so a slow but moving disk drains fully and a stuck
      one costs 10 s at exit instead of a hang.
      - On giving up, the records still queued are counted lost. The record being written is
        not counted. The writer writes nothing more, so a record counted lost cannot appear
        later.
      - The trailer is skipped. stderr says the export is incomplete, how many records were
        still queued, and that the trailing record could not be written. The exit status is 1.
      - If the drain completes, the trailer has its own 2 s bound.
      - Replay's blocking writer keeps its unbounded drain, which is #35's no-drop promise.
      - A writer stuck *inside* a write can leave a last line cut short, with no newline and no
        one to roll it back. One stuck in fsync leaves it whole but unsynced. README says to
        discard a final line that doesn't parse after an incomplete export.

      **Tests.** Red first, failing on main by assertion. Inert hooks let them compile there:
      the `stall` and `trailerWait` fields, `export.NewWriter` (the one exported constructor
      over a sink, used by `cmd`'s tests through the `openLiveExport` var), the `tuiOptions`
      helper and `tui.Options.ExportLost`.
      - `internal/export`:
        - `TestLostCountsRecords`: written plus lost is the 3,000 records sent. On main, 513 +
          4,974 = 5,487.
        - The two mixed-halves tests above.
        - `TestTrailerRecordsTheLoss`: exactly three keys, `records_lost == Dropped()`, `at` in
          UTC within the test's window.
        - `TestCloseGivesUpOnAStalledDisk`: hangs on main.
        - `TestTrailerWriteIsBounded`.
        - Guards that pass on main: `TestCompleteFileIsUnchanged` (byte-identical),
          `TestCloseDrainsASlowButMovingDisk` (300 records at 2 ms each against a 200 ms
          bound) and `TestBlockingCloseStillWaits`.
        - `TestALeftBehindHalfIsReplaced`, added with the fix because it reads the new
          bookkeeping.
      - `internal/tui`: `TestExportLossNotice` (once), `TestExportLostStatusField` (hidden at
        0, kept at width 80 beside a long source list), and the guard
        `TestNoExportNoLossField`.
      - `cmd/logscry`:
        - `TestPlainExportLossEndToEnd`: 2,000 distinct FATALs in dry-run onto a stalled disk.
          The live line appears once; `run` returns the record count; the file's last line is
          the trailer with that count; records plus lost is every escalation.
        - The guard `TestPlainNoExportLoss`: no line, `run` returns nil, records only.
        - `TestTUIOptionsCarryExportLoss`.
      - The full suite under `-race`: 2,078 passing and 1 skipped on main, 2,094 and 1 on the
        branch. No existing test changed outcome. The new tests pass under `-race -count=20`.

      **The issue's measurement, repeated.** A distinct FATAL every 5 s over 6 virtual hours
      (4,320 lines), default config, `pipeline.Run` on the source clock in dry-run, writing
      through the live writer to an ext4 file. Three runs on each side:

      | | escalations | records in the file | lost, as reported | file + lost |
      |---|---|---|---|---|
      | main | 3,609 | 569 / 577 / 564 | 6,076 / 6,048 / 6,072 (messages) | ≠ 3,609 |
      | branch | 3,609 | 578 / 571 / 575 | 3,031 / 3,038 / 3,034 (records) | 3,609 in all three |

      On the branch the live count, the exit-time count and the trailer's `records_lost` agree
      in every run, and Close returns no error.

      Seen and left alone, out of scope: a second Ctrl-C during a slow exit is swallowed by
      `signal.NotifyContext`, so it does not cut the 10 s bound short.

      README: the #70 Known-limitations entry goes. The Export section documents the live line,
      the notice, the status field, the `export_incomplete` kind, the bounded exit and the
      partial last line, and a new "Exit status" section lists 0 and 1.

      Targets v0.12.19

- [x] **A line is cut at 64 KiB, and the rest discarded without being held** — #124, found
      while writing SECURITY.md, whose crafted-line item covers it. `readLines` read with
      `bufio.Reader.ReadString('\n')`, which holds a line whole until its newline or EOF, and every
      line source goes through it: stdin, both subprocess streams, journald (`mapLines`), Docker
      (`readDockerStream`) and replay (`replayLines`). Nothing downstream bounded a line. The
      coalescer's 64 KiB cap stops folding but does not cut, and the prompt's 1000-rune cut
      limits only what reaches the model. While the pipeline templated a huge line, it handled
      nothing else. The issue measured 64 MiB → 360 MiB RSS and 13.2 s, and 256 MiB → 1.37 GiB and
      52.4 s. A huge line *with* a newline is the same defect. **Present since v0.1.0:**
      `ReadString` arrived with the first stdin and subprocess sources (8177235), and v0.1.0 is
      the first tag that contains it.

      **Decisions.**
      1. **The cap is `ingest.MaxLineBytes`, 64 KiB, with no flag.** It applies to a line's text,
         what `Raw` holds, without its `\n` or trailing `\r`s, so `len(Raw) <= MaxLineBytes` holds
         for every line from every source. Why this size:
         - **It equals `maxCoalesceBytes`.** One physical line never exceeds the coalescer's own
           unit, and a coalesced event stays under about 128 KiB.
         - **It sits above what the producers write.** journald splits a stdout line at
           `LineMax` (48K by default), and `journalctl -o json` without `--all` prints any field
           over 4096 bytes as `null`. Docker splits a message at 16 KiB. Only stdin and
           subprocess have no producer-side bound.
         - **It sits far above the captures.** Measured over the 10 boots and ab37: 70,783
           entries, counts and lengths only. The prediction, written down first: longest line
           4–10 KiB, none over any candidate cap, under 10 null MESSAGEs. The maximum and the
           count over the cap held; the null MESSAGEs did not, since there were none.

           | set | `_TRANSPORT` | entries | longest line (B) | over 64 KiB |
           |---|---|---|---|---|
           | boots | stdout | 26,361 | 4,239 | 0 |
           | boots | journal | 17,480 | 1,637 | 0 |
           | boots | kernel | 14,019 | 1,143 | 0 |
           | boots | syslog | 5,097 | 2,039 | 0 |
           | boots | driver | 60 | 1,518 | 0 |
           | ab37 | stdout | 7,391 | 1,386 | 0 |
           | ab37 | journal | 195 | 1,530 | 0 |
           | ab37 | syslog | 168 | 1,813 | 0 |
           | ab37 | kernel | 12 | 812 | 0 |

         - **Larger would multiply into what a crafted stream can make logscry hold.** Every line
           in flight can be held at the cap: the `lines`, `grouped` and plain `events` channels
           (1024 each) and the 2000-event stream ring, which every mode builds. At 1 MiB that is
           gigabytes, and about 200 ms of templating per line.
         - **The old "large JSON log records survive" comment.** It was about `bufio.Scanner`,
           whose default limit is also 64 KiB: past it, Scanner *fails and ends the stream*. Here a
           line up to 64 KiB arrives intact, and past it the stream goes on. A JSON record over
           64 KiB is templated as text, because its cut head no longer parses.
           `TestReadLinesSurvivesLongLine` now holds a line of exactly `MaxLineBytes` (Scanner's
           failure point) instead of 512 KiB. It is the one existing test whose input changed.
         - **No flag.** Every user would pay the stream cost of a higher cap, and nothing in the
           data asks for one.
      2. **Past the cap, the head is kept and the rest discarded up to the newline.** Splitting
         was rejected:
         - each later chunk starts mid-token, so it is a novel template;
         - a 64 MiB line would become 1,024 events of one template, a burst that escalates;
         - every byte would still be templated.

         Truncating bounds the pipeline's work per line and keeps what identifies the line,
         which sits at its front: timestamp, level, source. At the cut:
         - **UTF-8.** A character the cut would split is dropped whole, which is why the notice
           says "at most".
         - **Line endings.** A line is over the cap only if a byte other than `\r` follows it
           before the `\n` or EOF. So `cap + "\r\n"` and `cap + "\r\r\n"` are intact and not
           counted, while `cap + "\rx\n"` is cut. That is today's `TrimRight(line, "\r\n")`.
      3. **What a cut line becomes, per source** (a test for each):
         - **stdin.** Its head.
         - **subprocess.** stdout and stderr are cut separately, each counted.
         - **Docker.** The timestamp is at the front, so it survives and is parsed. It counts
           toward the 64 KiB, so the message keeps the cap less the stamp. The mark advances as
           for any stamped line.
         - **journald.** The cut entry no longer parses, so `decode` passes it through raw, as it
           does any undecodable line: source `journald`, no level, the receipt time. That is
           acceptable, because losing the line would be worse, and because with fields nulled
           past 4096 bytes an entry would need about 16 maximal fields to reach 64 KiB.
         - **replay.** The cut entry is replayed raw and has no timestamp. Past the first line it
           is counted in `Untimed()` and in the replay notice. **Known edge:** a cut *first*
           entry makes replay refuse the whole capture with `errUntimedCapture`, whose message
           says the entry carries no timestamp when it was in fact cut. It is pinned as is by
           `TestReplayRefusesACaptureWhoseFirstEntryIsCut`. Reaching it takes a first entry over
           64 KiB, which needs a capture made with `--all` or a hand-built file; README records
           with plain `-o json`. The truncation notice prints beside the refusal. On main the
           same entry replayed whole.
      4. **Shown once, counted, exit status unchanged, no marker.**
         - **The count.** `ingest.Truncations` holds it and is attached to the run's context
           (`WithTruncations`), because `Source.Lines` takes nothing else per run, so no source
           or constructor changed. It calls `notify` on the first cut, at the moment the cap is
           crossed, so a line that never ends is announced while it is being discarded.
         - **The notice.** It goes down the background-notice channel: on stderr with `--plain`,
           in the status line in the TUI.

           `logscry: ingest: a line over 64 KiB was cut to at most its first 64 KiB and the rest
           discarded; the total is reported at exit`

           `errs` gains a slot for it.
         - **At exit.** `logscry: ingest: N line(s) over 64 KiB were truncated`.
         - **`Stop`.** `mapLines`, `replayLines` and `readDockerStream` return on a cancel
           without waiting for their reader, so a reader can still cut a line after `errs` has
           closed. `Stop` runs before the close, under the same lock as `notify`.
         - **No exit code.** It differs from #70: an export loss leaves a file that is missing
           records, and status 1 is how a job learns its artifact is wrong. A cut line was still
           read, templated, scored and exported, so every output is complete. Like the prompt's
           1000-rune cut and a failing source (#97), it is a property of the input. Exit-coding it
           would fail a CI job for piping a log with one base64 blob in it.
         - **No marker in `Raw`.** A marker would be indistinguishable from text the producer
           wrote, and would be templated, sent to the model and exported as if logged. Template
           hashes: a line at or under the cap is byte-identical to main and keeps its hash, and
           only lines over 64 KiB move.
      5. **Bounded by construction.**
         - **The reader.** It reads with `ReadSlice` through bufio's 4 KiB buffer. Text past one
           buffer is appended to a reused slice that never grows past the cap. Past the cap,
           bytes are only scanned for a non-`\r`, never stored.
         - **Cancellation.** ctx is checked after every full buffer, so a cancel mid-discard
           returns at once.
         - **No spinning.** Each full buffer is consumed, so a newline-less stream makes
           progress, and a reader that returns nothing gets bufio's `io.ErrNoProgress`.
         - **Memory per stream.** The buffer plus at most 64 KiB.

      **Measurements.** Every prediction was written down before its run.
      - **The issue's harness, rebuilt.** One run of `a` with no newline,
        `--plain --explain-dry-run`, peak RSS from `/usr/bin/time`:

        | | 64 MiB | 256 MiB |
        |---|---|---|
        | main | 358.6 MiB, 13.15 s | 1,403.5 MiB, 50.79 s |
        | branch | 14.4 MiB, 0.06 s | 14.2 MiB, 0.14 s |
        | reader alone, main | 141.2 MiB | 533.3 MiB |
        | reader alone, branch | 10.5 MiB | 10.6 MiB |

        The prediction was under 25 MiB and under 1 s for the binary, and under 15 MiB for the
        reader alone. It held.
      - **The residual bound for a crafted stream.** Identical 1 MiB lines (one template), each
        cut to 64 KiB, through the binary in plain dry-run:

        | lines | peak RSS | wall |
        |---|---|---|
        | 2,000 | 154.9 MiB | 22.4 s |
        | 20,000 | 157.1 MiB | 230.5 s |

        It plateaus and does not grow with the input. The prediction was 150–400 MiB and
        300–800 MiB with a ratio under 2.5×. The plateau held; the 20,000-line range was too
        high. So about 160 MiB is the worst case for a stream of maximum-length lines **of one
        template**. A stream of *distinct* templates is not bounded by this change, because
        the template map is never pruned (found here; see the end of this entry).
      - **Replay of all 11 captures, main vs branch.** Predicted zero change, since no line is
        over the cap. It held:
        - events: 58,405 on both;
        - escalations: 60 on both;
        - templates: identical counts per capture;
        - per-event template hashes: 0 differing.
      - **Allocations on ordinary lines.** `BenchmarkReadLines`, median of 5:

        | line | main | branch |
        |---|---|---|
        | 64 B | 1 alloc, 80 B | 1 alloc, 64 B |
        | 256 B | 1 alloc, 288 B | 1 alloc, 256 B |
        | 1 KiB | 1 alloc, 1,152 B | 1 alloc, 1,024 B |
        | 4 KiB | 3 allocs, 8,984 B | 1 alloc, 4,096 B |
        | 16 KiB | 7 allocs, 34,960 B | 1 alloc, 16,384 B |

        Time per line is equal or lower at every size: 16 KiB went from 14.1 µs to 6.9 µs.

      **Tests.** All of them fail on main by assertion. Input is generated lazily by an
      `io.Reader`, so no test allocates its input. The red commit adds `MaxLineBytes` and an
      inert `Truncations` so the tests compile on main.
      - **`TestReadLinesCapsALine`.** Exactly the cap, cap+1, CRLF after and straddling the cap,
        `\r\r\n`, a `\r` that text follows, and 2-, 3- and 4-byte runes straddling the cap at
        every offset or ending exactly at it.
      - **`TestReadLinesHugeLineIsBoundedInMemory`.** A 64 MiB line, then `next`, intact.
        TotalAlloc is measured around the `readLines` call alone, with the test not parallel,
        against a 16 MiB bound. Main: 135,980,016 bytes. Branch: 322,160 bytes in each of 20 runs
        under `-race` (min = max).
      - **The rest:**
        - `TestReadLinesCapsALineAtEOF`;
        - `TestReadLinesStopsDiscardingOnCancel`: within 2 s, at most 64 KiB read after the
          cancel; main reads 8 MiB more;
        - `TestTruncationsNotifyOnce`;
        - `TestTruncationsStopEndsNotification`;
        - one per source: stdin, subprocess, Docker, journald, replay, and the replay edge
          above;
        - `TestPlainReportsTruncationOnce`: the notice once, the total at exit, `run` returns
          nil.
      - **The guard** `TestReadLinesEndsOnAReaderThatMakesNoProgress` passes on main too.
      - **The full suite under `-race`.** 2,094 passing and 1 skipped on main; 2,126 and 1 on the
        branch. No existing test changed outcome. The new tests pass under `-race -count=20`.

      **Seen and left alone, out of scope:**
      - **`internal/llm/sse.go`** reads the model endpoint's response with the same unbounded
        `ReadString`. It is the same class across a different trust boundary: an endpoint the
        operator configured.
      - **Untouched:** the coalescer, the prompt cut, #91, #121, and the masker order and
        patterns.

      **Found on the way, not addressed here:**
      - **The template map is never pruned** (#126). A stream of distinct templates grows memory
        without bound: measured at about 3.5 MiB per 10k short lines and about 629 MiB per 10k
        lines at the cap, linear, with no plateau.
      - **A journald MESSAGE over 4096 bytes arrives as the raw JSON entry** (#127).
        `journalctl -o json` without `--all` prints it as `null`, so the entry has no unit, no
        priority and the receipt time. Confirmed with one synthetic 5,000-byte entry.

      Targets v0.12.20

- [x] **A double-quoted string reads backslash escapes** — #91, found while building #83.
      `strPat` was `"[^"]*"|'[^']*'`, and it knew nothing of escapes:
      - `config "user \"bob\" logged in" applied` templated as `config <STR>bob\<STR> applied`;
      - the alice line templated as `config <STR>alice\<STR> applied`;
      - `["!", "\"", "#"]` mis-paired into `[<STR>, <STR><STR>#\"]`.

      That was one template per value, with the text between the escapes left in the template.
      It affects the plain-text path only. JSON (M9) and logfmt (#83) decode their quoting
      before the text masker sees a value. **Present since v0.1.0:** the pattern is unchanged
      since the first templating commit (78fcb70), and v0.1.0 is the first tag that contains
      it.

      **The rule.**
      `strPat = (?:strBoundary strEscaped) | strPlain | strSingle`, where:
      - `strEscaped` is `"(?:[^"\\]|\\.)*"`, the issue's candidate;
      - `strBoundary` is `(?:^|[^\\'"])`;
      - `strPlain` and `strSingle` are the old two alternatives, unchanged.

      The boundary belongs to `strEscaped` alone. The grouping makes that explicit, and the
      comment says so, so it is not "fixed" into applying to all three. RE2 has no lookbehind,
      so the boundary character is consumed and `maskSTR` re-emits it, the shape `maskIP` uses
      for IPv6 (#33).

      What it buys: a template differs from the old pattern's only where a quote follows an odd
      run of backslashes.
      - Without one, `strEscaped` ends where `"[^"]*"` ends. `\\"` closes under both.
      - A backslash before a line break fails `strEscaped`, so `strPlain` takes it as before.
      - A match opened one character early is re-emitted unchanged.

      **Decisions, each a test row.** The direction of each trade is given.
      - **Escaped backslash before the close**, `open "C:\\temp\\" failed`. It closes there,
        as before: `open <STR> failed`. No trade.
      - **A literal trailing backslash with a later quote**, `copy "C:\logs\" rotated "x"`.
        It is now `copy <STR>x"`, where it was `<STR> rotated <STR>`. **Accepted, toward
        merging:** the string runs on to the next quote, so lines differing in between merge,
        and `x` stays in the template. Nothing in the text tells a literal backslash from an
        escape. It takes a quoted Windows path on a Linux host with another quote later on
        the line, and the captures hold none.
      - **Unterminated, ending in `\"`**, `read "partial\"`. `strEscaped` cannot close, so
        `strPlain` gives today's `read <STR>`. The unguarded candidate would match nothing
        there and leave the value in the template, one template per value. The fallback also
        covers lines #124 cut mid-string.
      - **An escaped quote outside any string**, `key=\"bob\" said "hi"`. It stays
        `key=\<STR> said <STR>`. Unguarded, the candidate opens at the escaped quote and runs
        on: `key=\<STR>hi"`, merging distinct events. The captures had no line of this
        shape, so the guard rests on the argument and on its mutant, not on data.
      - **A double-quoted string right after an apostrophe**, `'"x"'`. It stays `<STR>`. The
        boundary class excludes `'`, so a match opened one character early cannot take
        precedence over `strSingle`.
      - **Adjacent strings**, `"a""b\"c"`. It stays `<STR><STR>c"`, today's mis-pairing.
        **Residual:** the first string's match has consumed the quote before the second
        string, so no boundary character is left for it, and the second string is
        `strPlain`'s. The boundary was not widened. Widening would not reach this case, and
        admitting `"` would let a match open on a quote that `maskSTR` then swallows.
      - **The single-quote twin is not taken.** `say 'it\'s' now` stays `say <STR>s' now`.
        Measured on the ten boots, the twin changed no line beyond the one `strEscaped`
        already moves. Apostrophes in prose already pair across words under `'[^']*'`, so a
        twin would change prose pairing on no evidence.

      **Measured.** The corpus is the ten boots, with `boot-2026-10-12-1134` filtered of its
      `ab37*` entries: 58,172 lines (4,845 entries filtered). Predictions were written down
      before each run.
      - **Per-line template diff, the #83 method.** Every entry goes through
        `TemplatizeLine(Normalize(decode(raw)))`, old against new:
        - **the rule:** 1 line moved, an escaped quote inside a closed string, on the stdout
          transport. Predicted: exactly 1, that category. **Held.**
        - **the unguarded candidate and the twin:** each differs from the rule on 0 lines.
          Predicted: twin 0, held; candidate ≥ 1, held at 1.
        - **lines carrying an escaped quote:** 45. The other 44 keep their template: they
          are JSON or logfmt lines, or carry the escaped quote outside a string.
      - **Templates and escalations.** Replay, `--explain-dry-run`, main binary against the
        branch binary, plus the replay harness for templates:
        - templates: 27,865 on both, +0 on every boot;
        - escalations: 59 on both, equal per boot;
        - events: 1 of 45,796 differs, the moved line.

        Predicted: 0 and 0. **Held.**
      - **The anonymizer, unedited.** Main and branch give the same 237 passing results, by
        name, for these three:
        - `TestTemplatizedMaskingIsComplete`;
        - `TestEveryUncollapsedDetectorHasATemplatizedRow`;
        - `TestLogfmtTemplatizedMaskingIsComplete`.

      **Tests.**
      - **`TestStrEscapes`.** The issue's lines, the alice twin sharing one hash, the array,
        and the trailing-backslash row fail on main by assertion. The other six rows above
        pass on main and pin what stays.
      - **`TestStrMaskMovesOnlyEscapedQuotes`.** It compares 20,000 strings from a fixed seed
        against the old pattern, asserting that a template may differ only where a quote
        follows an odd run of backslashes. The alphabet includes `\n`, `é` and `<NUM>`, and
        no escape pairs are removed, so `\\"` is in the inputs. 97 of the 20,000 differ, all
        with an escaped quote.
      - **Mutants.** Each turns tests red:
        - the unguarded candidate;
        - no fallback;
        - a boundary admitting `'`;
        - the twin;
        - the boundary in front of all three;
        - a `strEscaped` that reads `\\"` as an escape. Only the property guard catches this
          one.
      - **The full suite under `-race`.** 2,126 passing and 1 skipped on main; 2,138 and 1 on
        the branch. No existing test changed outcome. The new tests pass under
        `-race -count=20`.

      **What a user sees.** In exports, `template_hash` and `pattern` change only for a
      plain-text line whose quoted string carries an escaped quote. Such a line is now one
      template whatever the value.

      Seen and left alone, out of scope: #92, #36, #31, #49 and #42, and the anonymizer's
      detectors. The masker order, and every pattern other than STR's, are byte-identical.

      README: the #91 Known-limitations entry goes.

      Targets v0.12.21

- [x] **A journald message over 4096 bytes keeps its unit, level and time** — #127, found
      while measuring #124. `args()` ran `journalctl -f -o json --no-pager` without `--all`, and
      without it journalctl prints any field over 4096 bytes as `null` (journalctl(1), "json").
      `decode` read nothing from a null MESSAGE and passed the whole entry through. The entry
      was tagged `journald` instead of `journald:<unit>`, got no level from PRIORITY and no
      stderr tag, and kept the receipt time. In a replay it was untimed, and as the **first**
      entry it got the capture refused with `errUntimedCapture`, whose message says the entry
      has no timestamp when it has one. Captures recorded with plain `-o json`, as README
      advised, hold the same nulls. **Present since v0.8.0:** `git log -L` on `args()` shows one
      commit, 42e9fe9 (#25), and v0.8.0 is the first tag that contains it. The replay side dates
      from v0.10.0, when replay started reusing `decode`.

      **The threshold.** journalctl(1) documents 4096 bytes. systemd v255's `logs-show.c`
      (`update_json_data`) nulls a field when `strlen(name) + 1 + size >= 4096` and
      `OUTPUT_SHOW_ALL` is unset. Measured, it is bracketed and not pinned: on the current boot
      the largest `name=value` printed without `--all` was 3,604 bytes, and the smallest nulled
      was 5,008, the #124 probe. Nothing in the code depends on the exact value, because `--all`
      removes it, so no probe was spent pinning it, and the marker names no number.

      **Decisions.**
      1. **`--all` in `args()`.** In `-o json` it changes only the size→null rule.
         `update_json_data` picks between a string and an array of bytes with
         `utf8_is_printable` either way, and `journalText` reads both. "blob data" belongs to the
         other output modes. Measured on the current boot, the
         prediction written down first:

         | measure | prediction | result |
         |---|---|---|
         | entries whose content differs with/without `--all` | 1–50 | 1 |
         | fields null without `--all`, by name | MESSAGE 1, `_CMDLINE` 0–50, others 0 | MESSAGE 1, nothing else |
         | differences other than null→value | 0 | 0 |
         | longest line with `--all` | 5.5–20 KiB | 5,646 B |
         | lines over 64 KiB with `--all` | 0 | 0 |

         The prediction held except for `_CMDLINE`, where 0 sat at the edge of the range. One
         surprise: all 4,666 lines differ *as text*. That is not `--all`'s doing. journalctl's
         key order varies between runs, and two runs without `--all` differ on all 4,666 too.
         Content was compared parsed, paired by `__CURSOR`. The startup backlog is unchanged:
         `-n 10` gives 10 lines both ways, and `-f` over a short window gave 10–11 lines either
         way, depending on traffic.

         **The residual: an entry whose line passes 64 KiB.** Before, its big fields were null
         and its line small. Now #124 cuts it, the head no longer parses, and `decode` passes it
         through raw: `journald`, no level, the receipt time, untimed in a replay. Accepted:
         - it is what that entry got before, and the truncation notice now announces it;
         - doing without `--all` would cost every entry between 4 and 64 KiB;
         - the boot measured has no line over 5,646 bytes.

         Which transports can produce one:
         - **stdout:** not as plain text, since `LineMax` (48K) splits it, but a binary or
           heavily escaped line can (a non-UTF-8 byte is up to four characters of JSON);
         - **journal protocol and syslog:** these have no split;
         - **kernel and driver:** these stay far below. Docker splits at 16 KiB.

         `--output-fields` was considered for shortening lines and rejected: MESSAGE alone can
         pass 64 KiB, and it would change every live line. The cap itself is out of scope.
      2. **A null MESSAGE is decoded, with logscry's marker as Raw.** When `MESSAGE` is present
         and null, `decode` keeps unit, priority, level, stream and time, and `Raw` is
         `messageNotRecorded`:
         `logscry: message not recorded (journalctl -o json prints a long field as null without --all)`.
         Live never produces one now, so this serves captures made without `--all`. Why not
         keep the raw fallback:
         - it left the entry untimed, or got the capture refused;
         - it hid an error-class entry's ERROR, which a live run with `--all` would flag;
         - its `Raw` was journald's metadata (host, machine ID, command line, cursor) posing as
           a message.

         **#124's objection to a marker** was that a marker sits inside text the producer wrote
         and cannot be told apart from it. Here there is no producer text. What remains is a
         reader mistaking the line for one the unit logged, and the `logscry: ` prefix answers
         that. A test pins the prefix, that text detection gives the marker no level, and that
         the anonymizer returns it byte-identical. The marker never reaches a model: replay is
         dry-run only, and live has no nulls.

         **Templating:** the hash is computed from the pattern alone, so all such entries
         share **one template across units**, not one per unit. The raw fallback grouped them
         by key shape instead.

         **Scoring:**
         - the first entry is novel;
         - at PRIORITY ≤3 it carries ERROR and stderr, so it can escalate, as live would;
         - at 6 the marker carries no level.

         The cost of one shared template: a second unit's lost message inside the first one's
         cooloff is suppressed. Accepted on 0 null MESSAGEs in 58,172 entries.

         **Held:**
         - an absent MESSAGE still passes through raw;
         - so does a MESSAGE that is neither text nor null;
         - no other field's null handling changed;
         - `journalText` still rejects null, through a `jsonNull` helper the two share.
      3. **Recording advice is `journalctl -o json --all`.** That covers README's Replay example
         and its text, the `--journald` known limitation, the mermaid label, the journald
         section and `examples/logscry.yaml`. The `--replay` flag help, the flag table and the
         `errUntimedCapture` text describe the format, which `--all` does not change, and stay.
      4. **README Known limitations.** The #127 entry is rewritten into the two residuals:
         - a capture made without `--all`, which replays the marker;
         - an entry over 64 KiB.

      **Against the real journalctl.**
      - **The #124 probe** (PRIORITY 6, stdout transport, no unit, so it is named by its
        identifier). Extracted both ways and replayed. Every prediction held:

        | | without `--all` | with `--all` |
        |---|---|---|
        | main | refused, `errUntimedCapture` | `journald:logscry-probe`, entry time, Raw 5,000 |
        | branch | `journald:logscry-probe`, entry time, Raw = marker (92) | same as main |

      - **One live probe,** `systemd-cat -t logscry-probe -p err`, 5,000 bytes starting
        `logscry-probe-127 `, followed by main and the branch at once. Both predictions held:
        - **main:** `journald`, no level, stdout, the receipt time (0.44 s after the entry's own),
          Raw 616 bytes (the whole entry);
        - **branch:** `journald:logscry-probe`, ERROR, stderr, the entry's own time, Raw 5,000.

        The captures MANIFEST notes it beside the #124 probe. The identifier filter returns
        exactly the two.
      - **Regression over the ten boots,** `ab37*` and `logscry-probe` filtered: 58,172 entries,
        0 null MESSAGEs. The prediction was zero change, and it held:
        - per-line template hashes: 0 differing;
        - distinct templates per boot: identical;
        - escalations: 59 on both.

      **Tests.** The red commit adds the inert constant so the tests compile.
      - **Red on main by assertion:**
        - `TestJournaldArgs`: all four rows gain `--all`;
        - `TestJournaldNullMessageKeepsTheEntrysMetadata`: PRIORITY 3 and 6;
        - `TestJournaldReadsALongMessageInFull`: a stand-in journalctl that nulls the field
          unless given `--all`;
        - `TestReplayKeepsANullMessageEntrysTime`: first and later.
      - **Guards, green on main:**
        - `TestJournaldNotRecordedMarkerIsLogscrysOwn`;
        - `TestJournaldNonTextMessageDecodesAsBefore`: a 5,000-byte byte-array MESSAGE
          decodes in full; an array of strings and a number pass through.
      - **Existing test changed:** the null row of `TestJournaldPassesThroughWhatItCannotDecode`
        moved out, since that entry is decoded now.
      - **The full suite under `-race`:** 2,138 passing and 1 skipped on main, 2,147 and 1 on
        the branch. The new tests pass under `-race -count=20`.

      **Seen and left alone, out of scope:**
      - #126, #121, #31, #36;
      - the 64 KiB cap;
      - `internal/llm/sse.go`;
      - `internal/anonymize`;
      - a field journalctl prints as an array of values, because the entry repeats it. That
        MESSAGE still passes through raw, which is `decode`'s documented behaviour for what it
        cannot read. 0 occurrences in the 58,172 boot entries and the 4,666 of the current boot.

      Targets v0.12.22

- [x] **Each answer is matched to its own flag, not to its template** — #121. With
      `--cache-ttl` shorter than the model's answer time, a template can flag again while its
      first flag is unanswered. Everything that matched an answer to a
      flag used the template hash, which the two flags share. Three symptoms, one cause:
      1. **The export.** The writer's `pairs`, `flags` and `orphans` were keyed by hash:
         - the second flag replaced the first;
         - the first answer was written with the second flag's `count_at_flag` and
           `last_seen_at_flag`;
         - the second answer wrote nothing.

         The issue's reproduction (TTL 1 s, the same PANIC line at t0 and t0+2 s, answers in
         order) gave one record with `count_at_flag` 2 and the first answer's summary.
         **Present since v0.6.0:** the hash-keyed join is in the first export commit
         (5c69018), and `--cache-ttl` has existed since v0.1.0 (fc45bf3).
      2. **The card.** `attach` put any answer on the template's one explanation:
         - an older answer arriving while the newer flag was pending ended "explaining…";
         - in reverse order it replaced the newer answer;
         - an older flag's partials overwrote the newer flag's.

         **Present since v0.1.0** (7e2489f, `attach`).
      3. **The "explaining" counter.** It went down only when the template's explanation left
         pending, so the second terminal answer found it already terminal and the counter
         stuck at 1. **Present since v0.5.0** (132dd8b).

      None of the three is reachable at defaults: a 1 h TTL against a 30 s timeout with one
      retry.

      **Decisions.**
      1. **A flag has an identity, `model.FlagID` (uint64).** The scorer mints it in
         `Evaluate`, right after `decide` passes, counting from 1. Why there:
         - every flag passes that point, in every mode;
         - the request leaves for the model from inside `Evaluate`, before the pipeline sees
           the flag;
         - the scorer is owned by one goroutine, so a counter needs no lock;
         - the values are ordered, which the card rule needs;
         - a run's IDs repeat from run to run.

         It is carried unchanged by the request, by `llm.explain`'s single `Explanation` and
         every copy of it (each partial, the answer, every failure), by the queue-full
         `Resolve`, and by the export's `Flag` and `WouldEscalate`.

         **The JSONL gains no field.** A record already is one flag (M8), and two of one
         template differ in `count_at_flag` and `last_seen_at_flag`. A `flag_id` restarts at
         1 every run while one file appends many runs, so it would look like a key and not be
         one. In the README's `diff before after` workflow, one changed flag would also shift
         every later record's ID.
      2. **The export joins on (hash, flag).** Every flag gets one record, joined to its own
         answer or failure, in any order. #70's rules now hold per flag: the other half of a
         lost record is dropped on arrival, and a flag unanswered at the run's end is never
         written and not counted lost.

         **The bound moves from per template to per flag in flight**, and it holds only if
         every queued flag's terminal answer is delivered. Checked path by path:
         - the worker returns without sending only when `ctx` is done (pool.go, after
           `explain` and in the send's `select`);
         - its send of a terminal answer blocks, and only a partial goes through the dropping
           `trySend`;
         - `pipeline.Run` and `printPlain` stop reading answers only on `ctx.Done`, or in
           `--plain` on a stdout write error, which ends the run;
         - `Resolve` ignores only pending updates.

         So outside a run's end no terminal answer is lost. The entries are bounded by the
         escalation queue, the workers and the answers waiting to be read.
      3. **The card rule.** The card's state always belongs to its template's newest flag
         (`Template.LastFlagID`). An older flag's final answer is shown only while no newer
         flag has produced any answer text, labelled as from its own flag. An older flag's
         partial updates never reach the card. Showing it rather than ignoring it is #34's
         carry-forward arriving late; RDI §6 asks for it ("the previous one stays visible,
         marked as belonging to the earlier flag"). Its `At` predates `LastFlagged`, so the
         card says "answer from the flag at …". Its state and error stay the newest flag's.
      4. **The counter goes up once per queued flag** (`escalated`; a queue-full drop and dry
         run never reach it) **and down once per terminal answer** (`attach`). So it equals
         the flags in flight.
      5. **An answer without its flag is a bug, and tests catch it.** Every non-test
         construction copies the request's flag:
         - `llm.explain` builds the one `Explanation`, and success, a 4xx, a timeout, retries
           exhausted, the anonymizer failing closed and a salvaged stream are all a
           `(resp, err)` from `Explain` mapped onto it;
         - partials copy it;
         - `pipeline` builds the queue-full failure and the card's pending state from
           `LastFlagID`;
         - shutdown sends nothing.

         In `attach`, flag 0 for a flagged template is counted in `Stats.Unidentified`, which
         nothing renders and the run-level tests assert is 0. It is shown as the newest
         answer, as before IDs existed, so a bug costs the card its ordering, not its answer.
         I chose a counter over a test-only assertion so production never panics.
      6. **Unchanged:**
         - the cache stays keyed by hash, because "escalated recently" is per template by
           design (RDI §6) and it is marked when the flag is decided, not by any answer;
         - the escalation decision and the rate limiter;
         - the per-card keys: collector, snapshot, TUI selection and expansion, which are
           per template (#34).

      **Measurements.** The ten boots, `ab37*` and `logscry-probe` filtered, were replayed
      `--explain-dry-run --export`, main against branch. The prediction was byte-identical
      exports, no new field, and a synchronous dry-run resolution. It held:

      | TTL | records main | records branch | kinds | files differing | diff on kind, hash, count, reasons |
      |---|---|---|---|---|---|
      | default (1 h) | 59 | 59 | all `would_escalate` | 0 of 10 | 0 lines |
      | 1 s | 61 | 61 | all `would_escalate` | 0 of 10 | 0 lines |

      Replay is dry-run only, so it cannot reach the answered path; the tests below do.
      **Model calls:**
      - `TestCostCapHoldsUnderFlood`, `TestRateLimitCapsLLMCalls` and
        `TestStreamCostGuarantee` pass unedited;
      - the reproduction makes 2 requests with `Escalated` 2, on main and the branch.

      **Tests.**
      - **Red on main by assertion:**
        - `TestReescalationBeforeTheFirstAnswerWritesTwoRecords`: the reproduction, with
          rows for in order (with partials), reversed, the first failing, and the second
          never answered. The dry-run row is a guard.
        - `TestALostFlagNeverTakesAnotherFlagsRecord`: on main the first record vanished
          uncounted.
        - `TestAnOlderAnswerNeverDisplacesANewerFlag`: four rows.
        - `TestExplainingCountsFlagsInFlight`: a queue-full drop, a failure, out-of-order
          answers and shutdown.
        - `TestEveryTerminalPathCarriesTheRequestsFlag`: the real pool and backend over
          `httptest`, with shutdown as a guard.
      - **With the fix:** `TestAnAnswerWithoutItsFlagIsCountedNotDropped`, the tripwire.
      - **Existing tests changed:**
        - `TestALeftBehindHalfIsReplaced` is replaced by
          `TestAnUnansweredFlagNeverJoinsALaterOne`. It pinned the newer-flag-replaces-the-
          older bound, the mechanism of this defect, and would have passed only because its
          flags were all 0.
        - Fixtures change mechanically: 8 `WouldEscalate` calls pass 0, 16 answers built from
          a request copy its flag, and one direct `flagged` call passes flag 1.
      - **The full suite under `-race`:** 2,147 passing and 1 skipped on main, 2,169 and 1 on
        the branch. The new tests pass under `-race -count=20`.

      **Seen and left alone, out of scope:** #126, #36, #28, the cache TTL default and
      `internal/anonymize`.

      Targets v0.12.23

- [x] **The two IPv6 patterns stay two, and one table compares them** — #42. The template mask
      (`ipv6Pat`, internal/pipeline) and the anonymizer's detector 7 (`ipv6Pattern`) were two
      copies of one matcher with nothing connecting them. #33 narrowed the first; #41 changed how
      the second is compiled. A person editing one could reasonably believe they were done.

      **The decision: two patterns, on purpose.** Of the three candidates:
      - **One shared source was impossible.** The two fail in opposite directions. One pattern
        has to pick a side. The pipeline's side sends `::1`, `fe80::/64` and
        `peer:2001:db8::1` to the model. The anonymizer's side corrupts templates again and moves
        every #33 hash. Each of those is a `mustDiffer` row now, not an argument.
      - **A common core was rejected.** The only text both could share without rewriting the
        pipeline's source is the hex group. Anything larger would mean rewriting the pipeline
        alternation around the anonymizer's needs. That puts hashes at risk for no change in
        behaviour, and makes the anonymizer follow any later pipeline edit silently, which its
        package comment forbids.
      - **Drift is made visible instead.** `internal/anonymize/ipv6_compare_test.go` runs the
        real `pipeline.Templatize` (how `bareHostPattern` already reads the pipeline, #105) and
        `New().Mask` over one hand-written table. It asserts both outputs exactly and DERIVES
        whether the two took the same span: the anonymizer's `<IP_n>` read as `<IP>`, then
        templatized, must equal the pipeline's pattern. Every `mustDiffer` row is named for its
        reason. An arity guard runs every elision of two or more groups (33 classes) plus the
        full form, which no hand-written row covers one by one. There is no new export and no
        import cycle: anonymize already imports pipeline. Both pattern sites now say what the
        other is, why they differ, and where the table is.

      **No pattern changes.** The anonymizer's C++
      over-redaction (`CDBusNMHelper<IP_1>GetDNSConfig`, `CNSSCertStor<IP_1>NSSCertStore`) is a
      prompt cost and not a leak, and it stays:
      - **No rule separates it from an address.** In `CNSSCertStore::CNSSCertStore`, `e::C` IS a
        valid address glued to a word character on both sides. A narrowing that refused it was
        planned and rejected: it would have stopped masking `id_fe80::/64` and `id_fe80::a`.
        That is an address glued onto the token before it, sent to the model in exchange for
        prompt quality. Every group carries bits.
      - **Refusing a bare `::`, which carries no bits, was measured and not taken.** It removes
        16 of 907 scope-operator matches on raw lines and 6 of 886 on templates (3 of the 5
        distinct templates that carry one), 0 address-shaped matches, and 3 of the table's 13 C++
        shapes plus part of a fourth. But "only bare-`::` bytes change" is not true in general.
        On a run of three colons the match starts one colon later, and
        `x :::1:2:3:4:5:6:7:8 y` sends `8`. The captures hold no such run, so a differential
        over them would have passed. A probe found it.
      - **A context-dependent variant fails closed.** Refusing `::` only when it is glued on both
        sides leaves `Log<IP_1>::init` after `::Dec` is masked, the re-scan reads that `::` as an
        address, and the escalation is skipped.

      **Predictions, then results.**
      - Pipeline, per-line hash diff over the ten boots (58,172 lines, `ab37*` and
        `logscry-probe` filtered), main vs branch. Predicted 0, measured **0**. `internal/pipeline`
        changes only in a comment; the #33 tests pass unedited.
      - Anonymizer, differential through `New()` and through `New("lab")`, on raw lines and
        templates. Predicted 0 bytes. Measured **0 of 232,688** capture records and **0 of 294**
        corpus records.
      - Bare-`::` share of scope-operator matches. Predicted about 40%, measured 1.8% by volume:
        **wrong**. Two high-frequency templates carry a group on one side. By distinct template it
        is 3 of 5.
      - Table shapes the bare refusal would fix. Predicted 3 full and 1 partial of 13. Measured
        the same.
      - Address-shaped matches it would remove, and shifted matches. Predicted 0 and 0. Measured
        0 and 0 on the captures, and wrong in general (the run of three colons above).
      - Wire check. Each binary ran against a capturing endpoint (`--plain --llm-anonymize
        --warmup-lines 0`) on 17 `[FATAL]` lines, one per leak-direction form plus two C++ lines,
        with #59's `postgres://user:p/ss@db` in the context.
        - **0** whole addresses and **0** address groups were sent by either binary.
        - `p/ss` was sent in 17 of 17 requests on both, so the grep is not vacuous.
        - Both send `CDBusNMHelper<IP_n>GetDNSConfig` and `CNSSCertStor<IP_1>NSSCertStore`.
        - The request bodies are byte-identical.

      **Tests.** The table, the arity guard, and 19 completeness rows for the leak-direction forms
      (loopback, one-group prefix, zone id, glued by `:`, `=` and `_`, eight groups bare and glued,
      upper case). All pass on main: this is a guard, not a red/green pair. `go test -race ./...`:
      2,169 passing and 1 skipped on main, 2,262 and 1 on the branch, and no existing result
      changed outcome. The new tests pass under `-race -count=20`.

      **Mutants, and what each turned red:**
      - Refusing the bare `::`: 6 table rows (the four bare-`::` cost rows, the three-level row
        and one #134 row).
      - Dropping `(?i)`: the four upper-case completeness rows, 10 table rows and the C++ row of
        `TestAcceptedOverMasking`.
      - Dropping the one-group trailing form: three completeness rows and 4 table rows.
      - Compiling detector 7 leftmost-first: #41's tests, the arity guard (21 classes), 15 table
        rows.
      - Porting the pipeline pattern into the anonymizer: 7 completeness rows and 21 table rows.
      - On the pipeline side, every mutant reddens the table as well as its own test:
        - dropping the boundary;
        - admitting `:` into it;
        - re-admitting a one-group form at either end.

        Compiling the IP masker leftmost-first is caught only by the arity guard (15 classes).
        The #33 tests stay green under it.
      - #49's seam is visible. A v4-mapped alternative in either pattern turns exactly one row
        red: the KNOWN_GAP v4-mapped row.

      **Found on the way, not fixed.** An address that begins with `::` and is glued on by `:` is
      sent: `peer:::ffff:0:0` goes as `peer<IP_1>:ffff:0:0`. A match ending in `::` takes the
      address's first colon, and `residue()` reads the rest as clean. It dates from v0.4.0 and is
      the same under leftmost-first. It is pinned as two KNOWN_GAP rows in the table and filed as
      #134, carrying the measurements above and the three-colon constraint. The wire check found a
      second: on the Template field, the hex letters of an address the template mask leaves
      uncollapsed are sent (`db<NUM>`, `fe<NUM>`), since #33's narrowing in v0.8.5. Same on main
      and on the branch; #135.

      Seen, out of scope: #49, #59, #65, #116, #36, the detector order.

      No release (no behaviour change).

- [x] **A `::`-led address glued on by `:` is masked whole up to a boundary** — #134, found in
      #42. **Since v0.4.0 (2da31be), 49 tags.**

      **Cause.** Detector 7 is leftmost-longest, so it takes the earliest start. In a `:::` run, a
      match ending in `::` starts one colon early:
      - the bare `::` after any token;
      - `(h:){1,7}:` after a token ending in a hex character.

      It takes the first colon of the address's own `::`. The rest (`:ffff:0:0`, `:a:b:c`) has one
      leading colon and no `::`. No alternative matches it, and `residue()` reads it as clean
      (#41's blindness). `peer:::ffff:0:0 down` sent `ffff:0:0`. `src:::a:b:c lost` sent `a:b:c`.
      Netstat's `:::22` sent `:22`. `x :::1:2:3:4:5:6:7:8 y` sent one colon. That colon was #42's
      reason not to delete the bare `::`, so the bare `::` stays.

      **The rule: 7a, a new chain entry immediately before 7.** The rest of the chain order is
      unchanged.
      ```
      7a   := (?i) ( lead tail ) END            END is context, not masked
      lead := ( h{1,4} (":" h{1,4})* )? ":{3,}"  raw hex only
      tail := ( U{1,4} (":" U{1,4})* )?         U = hex | a pipeline placeholder (#43)
      END  := [\s , ; ) ] } > " ' / %] | "." (\s | $) | $
      ```
      - **The left end is 7's.** With up to seven groups before the run, alt 2's start is the
        same as 7a's. With eight or more, alt 1 starts earlier, which is why `lead` is unbounded
        in count. Where `lead` cannot reach back, a `::` remainder is re-masked by 7
        (`a::b:::c ` → `<IP_2><IP_1> `).
      - **The parse is forced.** Between the run and END there are only single-colon groups. So
        the match does not depend on alternation order, and the Longest-off mutant on 7a alone
        turns nothing red.
      - **The right end is bounded.** That is what keeps every later value whole. With no END,
        7a takes `:::AIza…`'s hex prefix (rule 12 is cut), `:::ab://`'s scheme (9a),
        `:::1.db.corp.internal`'s first label (9b), and the first octet of
        `:::ffff:192.168.1.1` (8). 7a matches only where a character that no later value
        contains follows. Everywhere else 7 and the rest run exactly as on v0.12.23.
      - **END was widened one candidate at a time, each against the gate.**
        - `'/'` went in. The home-path value starts AT the `/`, and END is not consumed, so
          the user name after it is still the home rule's. The URL-path, userinfo and
          prefix-length probes (`postgres://u:::1/x@db/app`, `http://:::1/x`,
          `route:::1/home/bob/x`) pass the gate.
        - `'%'` went in. The userinfo classes admit `%`, but a userinfo there would be inside
          the address's own span, and the probes (`postgres://u:::1%x@db/app`, `u:::1%40x@h`)
          stay as they were.
        - A terminal `.` went in: a `.` followed by whitespace or the end, as a two-character
          context.

        Strictly more, gate corpus at 1,000 cores: 537 at the base END, 657 with `/`, 791 with
        `%`, 834 with the terminal `.`. No candidate failed. A bare `.` does fail: the
        gate turns red. On v0.12.23 the `.` of `peer:::1.db.corp.internal` was masked inside a
        host, and with a bare `.` it is sent (`peer<IP_1>.<HOST_1>`). On
        `peer:::ffff:192.168.1.1` the bare `.` sends `.168.1.1`.
      - **#49's seam.** 7a's source is `lead + tail + END` consts. #49 adds one alternative,
        `(?:0{1,4}:)?ffff:` + `ipv4Pattern`, beside `tail` in the composition line. `lead`, `tail`,
        END and the chain entry are untouched. Checked in a scratch copy:
        - the probe `peer:::ffff:192.168.1.1 down` gives `peer<IP_1> down`;
        - the gate stays green;
        - exactly one row turns red, the RESIDUAL #49 row.

        Glued v4-mapped needs the same right-boundary treatment.

      **The superset gate, asserted at the chain level.** `TestGluedRunMasksASupersetOfMain`
      runs `baseDetectors` and `buildDetectors({"lab"})` against the same chains as v0.12.23
      built them: 7a removed, and 7 on its v0.12.23 pattern, frozen in the test file as the
      reference (not an equality pin, because #49 changes `ipv6Pattern` on purpose). For every
      input, the masked byte positions come from the output plus `byToken`, and the test fails if
      those do not rebuild the input. It asserts three things:
      - the bytes masked now include every byte v0.12.23 masked;
      - an input without `:::` comes back byte for byte as before;
      - an input v0.12.23 masked is not refused now.

      Its corpus is every completeness, templatized, comparison-table and right-boundary row,
      #134's three over-mask rows, 20 probes (each END character next to a value a later rule reads), and
      a seeded generator (PCG 134): colon runs 1–4, hex 0–8, `.`, `/`, `%`, `[`, `]`, letters,
      digits, `_`, space, glued to a word, to a hex-ending word, after `=` and unglued. All of it
      runs raw and templatized. 250 cores keep it to 18 s under `-race`. 2,532 inputs, 179 mask
      strictly more. This replaces #42's "only bare-`::` bytes change", the property that failed.

      **Shapes.**
      - **Masked whole now:** `peer:::ffff:0:0`, `src:::a:b:c` (→ `sr<IP_1>`; the `c` was taken
        on v0.12.23 too), `peer:::1`, `x=:::1`, `x:::1:2:3:4:5:6:7:8`, upper case, netstat
        `:::22`, before `/128`, a home path, `%lo` and a sentence dot.
      - **Unchanged and asserted equal to v0.12.23** (`TestIssue134RightBoundary`):
        - a key, a hex scheme, a hex-led scheme, `ts:::INFO:::msg`, an S3 ARN;
        - `:::1.example.com`, `:::1.db.corp.internal` (the host stays `<HOST_1>`), `:::1.2.3`;
        - RESIDUAL #49 `peer:::ffff:192.168.1.1`;
        - KNOWN_GAP #137 rows for `-`, `_`, a letter and a dot inside a word.
      - **`:::*`.** Its address `::` is masked on both, and `<IP_n>:*` stays (`*` is not END).
      - **Comparison table.** The two #134 KNOWN_GAP rows become "leak direction" rows in
        `ipv6MustDiffer`: the anonymizer now masks whole and the pipeline leaves them uncollapsed.
        The four bare-`::` cost rows lose "see #134", because the bare `::` stays.
        `internal/pipeline` is untouched.

      **What it costs, pinned in `TestAcceptedOverMasking`:**
      - the gluing colon goes into the placeholder;
      - netstat's `:::22` loses its port (`::` then `:22` also reads as `:` then `::22`, and no
        textual rule separates them);
      - `xs ::: ys` is one placeholder.

      **What it does not reach: the rest of the class, #137.** Followed by `-`, `_`, a
      letter, a dot inside a word, or other punctuation (`*`, `!`, `?`, `&`, `=`, `#`, `+`, `@`,
      `:`, `~`, `|`, `$`, `^`, `` ` ``, `(`, `[`, `{`, `<`), a glued address is still sent, the
      same as v0.12.23. 22 neighbours were confirmed identical on both. None of them can simply
      be END, because each can continue a value a later rule owns: a host label, a key, a dotted
      quad, the field separator, a userinfo or query. The shapes #134's text lists are all
      masked whole.

      **Predictions, then results.**
      - `go test -race ./...` on main: predicted 2,262 passing and 1 skipped, **held**.
      - Same on the branch: predicted 2,299 and 1, **held**. No existing result changed outcome:
        6 table rows were renamed and 43 names are new.
      - Gate strictly more at the base END: predicted 250–450, measured **537**, **missed**.
      - Captures differential, the ten boots (58,172 lines, `ab37*` and `logscry-probe`
        filtered, raw and `TemplatizeLine`, `New()` and `New("lab")`): predicted 0 changed and
        0 less of 232,688. **Held**: 0 changed, 0 less, 0 more, 0 errors on either side, 0
        holding `:::`.
      - Residue, clean on v0.12.23 and refused on the branch: predicted 0, **0** on the corpus
        and the captures.
      - Template hashes over the boots: predicted 0, **0 of 58,172** moved.
      - Corpus differential at 4,000 cores (32,612 inputs per mapper): predicted 0 less, **held**.
        - `New()`: 3,036 strictly more. I predicted about 11,000 by scaling from the wrong base,
          so this **missed**.
        - `New("lab")`: identical, 3,036 strictly more and 0 less.
        - Predicted every changed output to mask strictly more: **missed**. 2,494 mask the same
          bytes in fewer placeholders, a colon run that v0.12.23 split as `<IP_1><IP_1>` and that
          is now one.
      - Wire check. Each binary ran against a capturing endpoint (`--plain --llm-anonymize
        --warmup-lines 0`) on 7 `[FATAL]` lines, with #59's `postgres://user:p/ss@db` before
        them. **Held**:
        - main sent 6 distinct tails (`ffff:0:0`, `a:b:c`, `:1` twice, `:22`, `FFFF:0:0`);
        - the branch sent 0;
        - `[::]:22` and `:::*`'s address were masked on both;
        - `p/ss` was sent in 7 of 7 requests on both.
      - `-race -count=20` on the new tests: green.

      **Mutants, and what each turned red:**
      - **`:{3,}` → `::`, 7a without its colon run.** 23 red: the gate (both mappers), 13
        completeness rows, the templatized row and its two logfmt forms, 2 table rows and 3
        over-mask rows. **Held.**
      - **Bare `::` deleted from 7, the #42 trap.** 23 red: the gate, 5 bare-cost table rows,
        the `[::]:22` and netstat completeness rows, the netstat over-mask row, 12
        right-boundary rows, and `TestNoDetectorGroupLandsOnOurOwnOutput`. The last two were not
        predicted.
      - **`Longest()` off on 7a only.** 0 red, **held**.
      - **`Longest()` off in `mustLongest`.** 68 red: #41, the arity guard (21 classes), 15
        table rows, the #57/#58/#61/#67/#105 rows and the C++ over-mask rows. None of 7a's rows.
      - **7a after 7.** 21 red, all of 7a's rows; the gate stays green. The order is what
        carries the change.
      - **A bare `.` in END.** 8 red: the gate, 5 right-boundary rows, and
        `TestEveryDetectorsContextSurvivesTemplating`, which was not predicted.
      - **END removed.** 17 red: the gate, 13 right-boundary rows, and the netstat completeness
        and over-mask rows.
      - **Without `/`, `%` or the terminal `.`.** 2, 1 and 2 red, each its own completeness
        rows, gate green. **Held.**

      **Tests.**
      - **Red on v0.12.23:** 13 completeness rows (the `[::]:22` control passes), one
        templatized row and its two logfmt forms, 2 table rows, 3 over-mask rows.
      - **Passing on both:** the gate (trivially on v0.12.23) and `TestIssue134RightBoundary`.
      - **Existing tests changed:** `TestAcceptedOverMasking` also runs #134's rows, which sit in
        the package var `gluedRunOverMaskingRows` so the gate can read them. Its own cases stay
        where they were.

      **Seen and left alone, out of scope:** #49, #135, #59, #65, #116, #36, the C++
      over-redaction, the detector order, every other detector, `internal/pipeline`.

      Targets v0.12.24

- [x] **`Restore` resolves a nested mapping by rule, not by map order** — #50, filed from the
      rejected widening of detector 4 in #46. `Restore` ranged over `m.byToken`, a Go map, and
      ran one `strings.ReplaceAll` per token, with no fixpoint. If a mapping's VALUE contained
      another mapping's TOKEN, the answer depended on which order the runtime walked the map.
      **Latent:** no detector produces a nested mapping, because every target group excludes
      `<` and `>`. So nothing changes for any input `Mask` can produce today, and there is no
      Known limitations bullet to remove.

      **Neither of the issue's fixes was taken as written.** Fix 1 (make nesting impossible and
      test it) had already landed with #46: `TestNoDetectorGroupLandsOnOurOwnOutput` and
      `TestRestoreHasNoNestedMapping`, whose failure message said the nesting "resolves by
      chance (see #50)". Landing it would have closed nothing. Fix 2 as written (one pass with an
      alternation over all tokens) is order-independent but deterministically WRONG on nesting.
      It substitutes `<TOKEN_2>` with `<TOKEN_1>:hunter2` and never re-reads what it
      substituted, so it fails the guard test the same issue proposes, on every run.

      **The fix: one pass over `ourPH`, expanding recursively.** Each match is looked up in
      `byToken`, and its value is expanded the same way. A token not in the map stays verbatim,
      at the top level and inside a value. The pattern is compiled once, at package level.
      Because the scan is one pass, a substituted value is never re-read together with the text
      beside it. That closes a second order-dependence the issue did not name: `TOKEN_1 → "<IP"`
      in front of a literal `_1>` used to become `IP_1`'s value whenever `TOKEN_1` was visited
      first.

      **Termination.** The expansion path is a STACK of the tokens being expanded. A token is
      popped when its expansion returns, so a token repeated inside one value, or reached
      twice through a diamond, resolves fully. Re-entering a token still on the stack is a
      cycle. The stack holds distinct tokens, so depth is bounded by `len(byToken)` and no
      acyclic nesting can reach the bound. A numeric depth counter would give the same outputs,
      but it detects a cycle only after unrolling it, which costs exponential work when values
      branch. **At a cycle the OUTERMOST token is emitted verbatim.** A cyclic mapping has no
      value, and a partial unrolling would be an invented string shown as a real one. That is
      exactly what `Restore`'s "better a stray `<IP_9>` than a wrong value" forbids.

      **What it depends on now.** `Restore` finds tokens by `ourPH`, so a tag outside `[A-Z]+`
      would mint a token it never restores. A future `IPV6` tag would do that.
      `TestEveryTagMintsARestorableToken` reads the tags from the real detector tables, not
      from a list written in the test, so adding such a tag turns it red.

      **#46's argument loses one ground and keeps the other.** #46 rejected widening detector 4
      on two grounds. The second was that nesting resolves nondeterministically, and this
      removes it. The first was that no target group may land on our own tags, which is the
      inertness invariant. That one is untouched and is sufficient on its own. No detector
      changed, and #46 is not revisited. `TestRestoreHasNoNestedMapping` keeps its name and its
      assertions; its comment and failure message now say what it guards: `Mask` never
      produces a nested mapping. Two package comments that described the old single-pass
      dependency are corrected.

      **Tests.** Red first. `TestRestoreIsOrderIndependent` builds the nested pair directly in
      `byToken` and restores it 1000 times with a fresh `Mapper` each run. On the old code it was
      wrong in **861–882 of 1000** across four runs, and the neighbour-combination case was
      wrong at the same rate. `TestRestoreResolvesByRule` pins these cases:
      - two- and three-level nesting;
      - unknown tokens;
      - a repeated token;
      - a glued token;
      - a known token twice inside one value;
      - a diamond;
      - a two-token cycle, a self-cycle, and a token whose value reaches a cycle.

      The repeated-in-a-value rows nest one level deeper on purpose. A leaf value is returned
      before it is pushed, so leaves alone cannot tell a stack from a visited-set. A mutant
      that never pops was run to confirm both rows go red. Full suite under `-race` on `main`
      and on the branch: 833 results, then 836. The only differences are the three new tests;
      no existing test changed outcome. README unchanged: it does not describe `Restore`'s
      mechanics. Targets v0.10.3

- [x] **The prompt's "first seen" age is source time on both sides** — #72, filed while
      building replay (#35). `firstSeenSuffix` computed `Trigger.Time − FirstSeen`. The
      trigger is the SOURCE's clock (journald's `__REALTIME_TIMESTAMP`, Docker's timestamp
      prefix; the read time on stdin and subprocess). `FirstSeen` is the pipeline's clock at
      upsert. On a backlog the source time is minutes or hours older than the pipeline's
      clock, so the age went negative and the `< 0` guard silently dropped the phrase. The
      prompt is the only reader: no card, `--plain` line or export value is computed this way.

      **The issue's candidate fixed the wrong side.** It proposed pipeline time on both sides
      (`LastSeen − FirstSeen`), which is consistent and meaningless on a backlog. The cell
      that decided it is reachable at the defaults: Normalize strips a `LEVEL:` prefix from
      the message and the template hashes the message, so `ERROR: db unreachable` ×5 then
      `FATAL: db unreachable` is ONE template. The FATAL escalates alone at Count 6. In a
      `--docker-tail` backlog all six arrive within milliseconds. The candidate would have
      told the model `Occurrences: 6 (first seen 0s ago)` about lines its source spread over
      an hour, turning "silently omitted" into "confidently misleading". The fix puts SOURCE
      time on both sides instead, which the trigger already carries.
      `model.Template.EarliestLineTime` is the running MIN of `LogLine.Time` over the
      occurrences read, maintained in `upsert`. It is a min and not the first because source
      time is not monotone across containers and backlogs, and a zero time contributes
      nothing. It replaces `FirstSeen` on `EscalationRequest` and `ExplainRequest`, where only
      the prompt read it; a pipeline-clock `FirstSeen` left beside it unread would invite #72
      back. `upsert` runs before `Evaluate` and `emit` sends that same line, so the trigger is
      inside the min and one source cannot produce a negative age. The `< 0` check stays as a
      guard for templates fed by sources on different clocks. `FirstSeen`/`LastSeen` keep the
      pipeline's clock and no stamp moved. `internal/score` changes only the request struct
      and the emit literal, and no template hash moves.

      **A first occurrence carries no age, by rule.** Its age is 0 by construction, and
      "Occurrences: 1" already says it. Live it was already omitted, by accident: the trigger
      is stamped microseconds before the pipeline's clock, so the old age was slightly
      negative. Novel templates are most escalations, so the commonest prompt is
      byte-identical.

      **Every cell, computed from the code.** Source (stdin/subprocess, journald, Docker) ×
      live/backlog × Count 1/>1. Count 1 is omitted everywhere, before by accident and now by
      rule. For live Count > 1 the old age was right to within ingest latency and is unchanged.
      Two cells change:
      - **Backlog Count > 1** went from omitted to the source spread. It can escalate at the
        defaults through the ERROR→FATAL shape above. Warmup mutes novelty in a backlog, and a
        millisecond backlog has no baseline, so burst cannot fire.
      - **Live after a backlog**, and **a container attached mid-run whose tail holds old
        lines**, went from understated to the true spread.
      What remains is named in README "Known limitations":
      - it is a lower bound, bounded by what logscry read;
      - stdin's source time is read time;
      - one template from sources on different clocks is off by their skew;
      - the card and the prompt now hold two different "first seen"s, on purpose.
      Replay never builds a prompt, since `--replay` requires `--explain-dry-run`.

      **Red first, through the chain as well as the unit.** `pool.explain` copies the request
      field by field, and a zero time omits the suffix, which is indistinguishable from the
      bug. So two tests drive real `pipeline.Process` → scorer `emit` → `llm.Run` → a
      capturing backend and assert the rendered user prompt, one of them through the
      anonymizing decorator: `TestBacklogEscalationCarriesSourceAge` and
      `TestBacklogAgeSurvivesAnonymizer`. Both assert exactly one escalation at Count 6, so
      that a lost template merge cannot pass vacuously. Also:
      - `TestFirstSeenSuffix` pins the unit;
      - `TestEarliestLineTimeIsRunningMin` feeds out-of-order source times and checks that
        `FirstSeen`, `LastSeen` and the hash do not move;
      - `TestFirstOccurrencePromptHasNoAge` has a production-shaped row (trigger 3µs before
        the pipeline's clock) that is green before and after, and an equal-clocks row that
        pins the rule.
      `ExplainRequest.EarliestLineTime` and the template field were added in the red commit,
      zero-valued and unused, so that the tests failed on assertions rather than on
      compilation.

      **No existing test changed outcome.** The full suite ran under `-race` on `main` and on
      the branch: 820 results, then 833. The only differences are the 13 new tests and
      subtests. Two existing tests were edited for compilation only: `upsert` gained the
      source-time argument, and a request literal lost `FirstSeen`.

      Found on the way and filed, not fixed: a restarted Docker container is re-attached with
      `--docker-tail` and no `Since`, so lines already counted arrive again within
      milliseconds (#80; README "Known limitations"). Confirmed on a daemon: five lines and
      one restart gave `x15` where 10 were written. Computed from the code, not observed: a
      template with an established rate can then cross the burst gate, so a restart can raise
      a false burst. RDI §3 and §7 updated for the new field. Targets
      v0.10.2

- [x] **Export times are written in UTC** — #71, filed while building replay (#35) and fixed
      before #37 so that every calibration artifact is born in the final format. Every time
      the exporter receives is in `time.Local`: journald's `time.UnixMicro` and the pipeline's
      `time.Now()` both are. `encoding/json` spells a time in its own zone, so one instant was
      `"2026-08-29T10:40:00.5Z"` under `TZ=UTC` and `"2026-08-29T19:40:00.5+09:00"` under
      `TZ=Asia/Tokyo`. A replay export was byte-identical run to run on one machine and NOT
      across two machines in different zones, and a replay export is exactly the artifact #37
      diffs.

      **One seam, verified rather than assumed.** `Record` holds three times: `first_seen`,
      `last_seen_at_flag` and `explanation.at`. Every line is built by `record()`, and both
      sources of `explanation.at` reach it: `Resolve` through `explanationOf`, and
      `WouldEscalate`'s `at` (the pipeline's `now`) directly. `record()` now calls `.UTC()` on
      all three, and nothing else changed. No source and no pipeline stamp moved, so the TUI and
      `--plain` keep showing local time. **This is a visible format change:** a consumer
      matching `+09:00` sees `Z` instead. The instants are unchanged.

      **Red first, at two levels.** `TestRecordTimesAreWrittenInUTC` feeds all three fields in a
      fixed +09:00 zone, through both the anomaly path and the would_escalate path. It asserts
      that each spells `Z` and parses back to the instant it was given. It uses
      `time.FixedZone`, not `time.Local`, because `time.Local` is process-global and
      initialised once, so nothing a test sets reaches it. The existing schema test could not
      see the bug, because its inputs were already UTC.

      `TestReplayExportIsZoneIndependent` tests the issue's own claim. It re-executes the test
      binary over the replay fixture under `TZ=UTC` and `TZ=Asia/Tokyo` and compares the two
      exports byte for byte. It guards against a vacuous pass in three ways:
      - each child reports the offset it actually ran at, so a TZ that failed to load fails the
        test instead of comparing two runs in one zone;
      - a child that exits non-zero is reported with its output, not as "files differ";
      - the child test SKIPS when run directly instead of passing having done nothing.

      `time/tzdata` is imported by that test file only, so the zone loads on a host with no
      zoneinfo.

      **No existing test changed outcome.** The full suite ran under `-race` on `main` and on
      the branch, each under `TZ=UTC` and `TZ=Asia/Tokyo`. It was 815 tests with no failure in
      any of the four runs, and the only differences were the new tests. No existing test's
      outcome depends on whether it runs at UTC or at +09:00.

      The times are not claimed to be lexically sortable. `time.MarshalJSON` trims trailing
      fractional zeros, so `…:05Z` sorts after `…:05.5Z`. Fixed-width fractions would be a
      different format change, and nobody has asked for one. RDI unaffected: it does not specify
      the export's time format. Targets v0.10.1

- [x] **Buffered streams flush in the order they arrived** — #69, a LIVE-path defect
      found while building replay (#35) and deliberately kept out of it: nobody looking for a
      coalescer bug would look under a replay entry. `pipeline.Coalesce` holds one pending line
      per (source, stream) key in a map, and two paths flush several of them at once — the
      end-of-input flush and one firing of the idle timer — by RANGING OVER THAT MAP. Go
      randomises map order, so the lines left in whatever order the hash seed picked. Measured
      before the fix: four buffered streams flushed at end of input, 200 runs, **four distinct
      output orders**; the new test with five streams fails on both paths at once. v0.2.0 →
      v0.9.4, twenty tagged releases (`git tag --contains 846bd24 | wc -l` → 20).
      It is harmless with one source and bites the moment there are several, which since v0.8.0
      is every `--journald` run: journald tags one source per unit (`journald:<unit>`), Docker
      one per container. The order lines reach the pipeline is the order the stream pane shows,
      `--plain` prints, and the scorer's context ring records — so the "preceding lines" an
      escalation carries to the model could differ between two runs over identical input. It can
      reach a DECISION in one narrow case: the rate limiter is global, so when two lines that both
      clear the threshold are flushed together and one token is left, which of them escalates
      followed the hash seed. Per-template state (count, recent ring, cooloff) is untouched by
      interleaving across templates, and no recorded run shows the narrow case happening.
      The fix numbers each header as it arrives and flushes in (idle deadline, arrival) order on
      the timer path and arrival order at end of input — `inOrder`, one helper both paths call.
      It only fixes the order among buffers flushed in the SAME instant, which was random;
      nothing that was deterministic changes, no coalescing heuristic moved, `Coalesce`'s
      signature is untouched, and every existing `TestCoalesce*` (all single-key) passed
      unmodified. `TestCoalesceFlushOrderIsArrivalOrder` pins both paths, 200 and 50 runs.
      Replay needs this — a byte-identical `--export` across two replays of one multi-unit
      capture is impossible while the coalescer shuffles — but it lands first, as its own change.
      Targets v0.9.5

- [x] **Hosts 2..n of a multi-host authority are masked** — #61, found and filed by #58's sweep
      and pinned there as two assertions that the gap was open. `--llm-anonymize` masked
      `mongodb://user:pass@host1:27017,host2:27017,host3:27017/db?replicaSet=rs0` to
      `<TOKEN_1>@<HOST_1>:27017,host2:27017,host3:27017/db…` and sent `host2` and `host3` to the
      configured model endpoint — measured on the wire before the fix, in the trigger line AND in
      the masked template of the same request; `mysql://user:pass@host1,host2/db` sent `host2`.
      v0.4.0 → v0.9.3, **seventeen tagged releases** (`git tag --contains 2da31be | wc -l` → 17).
      **The cause is a fifth kind, and the first instance of it.** Not an under-matching pattern
      (#41), not a value another component rewrote first (#43), not a detector blocked by another's
      tag (#46/#48/#54/#57), not a shape no rule described (#55). Detector 9a matched exactly what
      it was written for — one host, class `[a-z0-9.-]` — and stopped where its author thought the
      value ended, at the port colon and the comma. **Its notion of the value was smaller than the
      value.** `residue` could not see it: both host rules are `verify:false`, and had they been
      eligible the leftover (`host2:27017,host3`) is not a shape any of them matches on its own —
      #41's structural reason from the re-scan's side. The completeness rows could not see it
      either: they ask whether the group landed on the DECLARED secret, and it did. **What would
      have caught it** is the #55-style sweep of the AUTHORITY production (`host = IP-literal /
      IPv4address / reg-name`, the port, the driver multi-host extension), which was never done —
      only the userinfo production was swept — with a `net/url`-as-oracle test as its mechanised
      half (every row with `://` must have HOST/IP placeholders covering `url.Parse(row).Host`; it
      does not consult the detector, so class 5 does not defeat it by construction). Filed as
      **#65**.
      **The fix is a companion rule, 9c, and NOT a wider class on 9a — measured, not preferred,
      and it overturned the working hypothesis.** The hypothesis was to let 9a's group span the
      whole authority. Two forms were built and run over the whole battery: a negated class
      `[^/\s<>@]` bounded at `/`, whitespace, `>` and end of text (with and without `?#`), and
      the exact grammar `host[:port](,host[:port])*` with the trailing port outside — which is
      what `net/url` calls the Host of that URI (`Hostname()` of the replica-set URI is
      `host1:27017,host2:27017,host3`, `Port()` `27017`). Both close every list shape and both
      OPEN a leak on a shape masked today: `peers=https://a.com,https://b.com` is
      `<HOST_1>,https://<HOST_2>` and becomes `<HOST_1>//b.com` / `<HOST_1>://b.com`. RFC 3986
      §3.2.2 puts `,` in sub-delims, so `a.com,https` is a legal reg-name — `net/url` reads that
      string as host `a.com,https`, path `//b.com` — and RE2 has no lookahead to refuse the member
      that is the next URL's scheme. Three ways out, all rejected: accepting the regression
      (forbidden: masked today); a REQUIRED trailing context on 9a rejecting `://` (a terminator on
      a first-pass rule narrows what it already masks — every position it does not name becomes a
      no-match, e.g. `https://a.com://b.com` masks `a.com` today and would not); relaxing 9a's
      anchor to a bare `://` so the orphaned URL re-anchors (closes #52 incidentally by DROPPING
      the anchor rather than making it tolerant — out of scope, not done). The negated form also
      minted `<HOST_n>` for a lone `[` on a bracketed IPv6 host and absorbed every port. **The
      durable result:** disambiguating trailing context is affordable only on a rule whose
      failure is the status quo. 9c reads the first member's tag as context, as 4b/4c read a
      credential half — the fifth rule to read one of our tags on purpose — and carries the `://`
      rejection safely. It runs after 9a and **before 9b and 6**, both placements measured: after
      9b, `h1.acme.com,h2.corp.internal,h3.acme.com` sent `h3.acme.com` (9b's own tag on the
      second member blocked the group); after 6, `host1,<uuid>.acme.com` sent `.acme.com`.
      **Options 2 and 3 were rejected on shape, not on disclosure, and each has one real merit.**
      All three close the same leaks; they differ in OUTPUT SHAPE. Option 3 — two capture groups
      on 9a — is strictly dominated: more blast radius (`detector.group` becoming a list, with
      `apply`, `residue`, `completenessSecretFor` and two tests following) AND less complete, since
      9a's group cannot start on `<IP_n>` and an address-first list stays open under it. That an
      address-first list defeats option 3 is evidence about #54 and is recorded in #67.
      Its merit: one placeholder for the whole list on the primary rule. Option 2 — 9c with a
      one-member group, re-applied by `apply` to a fixpoint — is rejected as **unmeasured, not as
      wrong**: it adds a rule that reads its own output, iterated to quiescence, which brings a
      termination obligation the inertness invariant does not currently carry. Its merit, measured
      under option 1 so it is stated exactly: a repeated host gets the SAME token in the list and
      in an adjacent field only when the two spans are identical, and under option 1 the list's
      tail maps to `host2:27017,host3` while `https://host2/x` in a context line maps `host2`
      alone — a different `<HOST_n>`, so the recurrence signal across fields is lost for members
      2..n. It is the named follow-up, and it would also close #67 (below), which the
      pre-pass #54 names would not need to.
      **The first member's tag set was derived by walking the chain, not sampled.** Before 9c
      runs, a tag can land on member 1 from 7 and 8 (an address literal, bracketed or not:
      `mongodb://user:pass@10.0.0.5:27017,host2:27017/db` → `<TOKEN_1>@<IP_1>:27017,<HOST_1>:27017/db`),
      from 9a (the ordinary case), from 1/2/3b only when a host literally IS a key
      (`mongodb://AKIAIOSFODNN7EXAMPLE,host2/db` → `<TOKEN_1>,<HOST_1>/db`), and from 5 only through
      #57's shape, where two tags sit adjacent with no comma between
      (`s3://AKIA….prod@h1.acme.com,host2/db` → `<TOKEN_1><EMAIL_1>,<HOST_1>/db`, which the first
      draft leaked). The #46 path by which the email rule swallowed `pass@h1.acme.com` is gone
      since #55/#58 — measured, not recalled: `postgres://sk-…:hunter2@h1.acme.com,host2/db` →
      `<TOKEN_1>:<TOKEN_2>@<HOST_1>,<HOST_2>/db`. #59's shape with a list
      (`postgres://user:p/ss@h1.acme.com,host2/db` → `<HOST_1>:p/<EMAIL_1>,host2/db`) stays open and
      is #59's: the authority ended at the raw `/`, so `host2` is PATH to the RFC and to `net/url`
      alike. 3a is impossible (it needs whitespace, and none can precede member 1 inside an
      authority); 4/4b/4c mint on the userinfo, never on a member — 4 can SWALLOW the list when
      the query carries an `@` (`mongodb://host1,host2?x=a@b` → `<TOKEN_1>@<HOST_1>`, #58's pinned
      over-mask, nothing leaks); 9b, 6 and 10 run after 9c. So the context admits one or more
      adjacent tags of ours, a bracketed tag — ours, or the pipeline's `<IP>` on the Template
      field, which the first draft missed and which leaked `host<NUM>` there — or a raw host run.
      The raw branch is unreachable in the chain (9a has taken every raw first member this anchor
      can reach, and declines only on `<` and `[`, where the raw branch cannot start either) and
      exists for the two single-detector probes, `completenessSecretFor` and the
      templatized-coverage test, exactly as 4b/4c's context does. Every excluded placement is
      unreachable, not unobserved.
      **What anchors 9c to an authority.** `scheme://` followed, with no whitespace anywhere, by
      an optional userinfo run, member 1, one or more commas and a host run. So a bracketed
      address in prose is never reached — `[10.0.0.5] connected, retrying`, `[10.0.0.5],retrying`,
      `peer [10.0.0.5] , retrying`, two bracketed addresses with no scheme and no `@`, a scheme
      earlier on the line, an `@` earlier on the line, `10.0.0.5,retrying`: all byte-identical bar
      the address, raw and templatized, and all pinned as rows. The one URL-shaped string,
      `https://x@[2001:db8::1],retrying`, tags `retrying` and is pinned as an over-mask.
      **Boundary shapes, measured through the real `Templatize` as well.** No credential
      (`<HOST_1>,<HOST_2>/db`); a list terminated by `?` or `#` — the group class is POSITIVE, so
      those end it by construction, which is why #58's `?#` decision about a NEGATED userinfo
      class was not imported by analogy; a single member with a trailing comma (unchanged,
      `<HOST_1>,/db`); an empty member — `h1,,h2` sent `h2` in the first draft, and libpq accepts
      an empty list item ("selects the default"), so the separator is `,+`; mixed case —
      `mongodb://host1,Host2.ACME.com,host3/db` → `<HOST_1>,<HOST_2>/db`, because Go folds the
      negated `listEnd` class under `(?i)` (measured character by character: `A` and `Z` do not
      terminate, `:` `/` `?` `,` do), which mattered because an uppercase terminator would have
      truncated a member with no test looking for it. Two members that templatize alike come back
      as the same placeholder (`host<NUM>,host<NUM>` → `<HOST_1>,<HOST_1>`), correctly.
      **What it costs, pinned in `TestAcceptedOverMasking`.** Hosts 2..n are ONE placeholder, so
      the ports BETWEEN members are inside it while the first and last stay visible:
      `host1:27017,host2:27018,host3:27019` → `<HOST_1>:27017,<HOST_2>:27019`. And a path-less URL
      directly followed by a comma and CSV cells reads as a host list: `https://api.acme.com,200,12ms`
      → `https://<HOST_1>,<HOST_2>`. **That row is a new kind of cost in this package, and it is
      named so the next over-mask decision does not cite the old asymmetry unexamined.** Every
      over-mask before it cost a placeholder in a restatement that is already lossy; this one costs
      the PAYLOAD the model reasons about — a status code and a duration are exactly what an
      explanation turns on. The over-mask-rather-than-under-mask asymmetry still holds and the row
      stays, but "over-masking is cheap" is now two claims — cheap to Restore, and cheap to the
      explanation — and only the first is structural. The boundary is narrow: a comma with NO space
      directly after a path-less URL. Measured beside it: `tried https://api.acme.com, then gave
      up`, `url: https://api.acme.com: refused`, `https://api.acme.com/orders,200` (a path before
      the comma), and `bootstrap=host1:9092,host2:9092` (no scheme) are all untouched.
      **What stays open is a CLASS, filed as one — #67.** A tag minted on a LATER member
      truncates 9c's group there, because the group excludes `<` (the inertness invariant), and
      every member after it is sent. Derived by the same walk: 7 and 8 reach a later member and
      are the ordinary way in (`mongodb://host1:27017,10.0.0.5:27017,host3:27017/db` sends `host3`;
      an IPv6 literal bracketed or not does the same); 1, 2 and 3b reach it only when a member
      literally IS a key (`mongodb://h1,AKIAIOSFODNN7EXAMPLE,host3/db` sends `host3`); 5 only through
      #57's shape, since any `@` inside an authority is detector 4's delimiter first
      (`mongodb://h1,bob@acme.com,host3/db` → `<TOKEN_1>@<HOST_1>,<HOST_2>/db`, nothing leaks); 3a
      needs whitespace and 10 needs a `/`, and both end the authority; 6 and 9b run after 9c. One
      pinned open assertion per reachable tag, so a cell is not a defect nobody reads — row 17 of
      the #55 sweep taught that. On the Template field the address members collapse to `<IP>`,
      which the tolerant class steps over, so the class is live on the raw path (#48's finding
      again). **Same class as #54 by cause, a sibling by remedy, argued:** #54 is an inner
      detector's tag inside a host value where a linear chain cannot say what the span is PART OF
      — that is exactly this. But in #54's two cells the inner tag is WRONG (an address that is a
      label inside a host) and only a pre-pass identifying the authority first can right it; here
      the inner tag is RIGHT (an address in a list is an address, `<HOST_1>,<IP_1>,<HOST_2>` is the
      wanted output) and a companion re-applied to a fixpoint would re-anchor on it, no pre-pass
      needed. So the remedy is option 2 above, not #54's. **#58's entry named the #54 pre-pass as
      this issue's candidate; it was not needed**, because the list's members are peers of the
      first host rather than parts of it, and a companion can see a peer through the tag the first
      one left behind. The pre-pass would still be the remedy for #54's own cells.
      **Found, not fixed, and it is a LIVE single-host leak — #66.** `http://my_host:8080/x`
      sends `_host:8080` with only `my` masked. `_` is legal in an RFC 3986 reg-name and ordinary
      in Docker container names, Kafka broker names and internal hosts, and it is absent from the
      host class, so 9a and 9c both stop at it. A single-host URL is a far more common shape than
      a multi-host DSN. **Its frequency relative to #61 is UNMEASURED**: the capture is not on this
      machine, so deferring it is a decision taken without data rather than a conclusion, and it
      is in README "Known limitations" for that reason — a disclosure defect present only here
      would be documentation asserting the absence of a bug. Pinned as two open assertions.
      **The corpus: no data.** The 612-record journald capture is not on this machine; per #43 it
      contains zero lines with an `@`, so zero credentialed DSNs, and whether it holds a
      credential-less multi-host authority could not be checked. The leak's frequency and the CSV
      over-mask's cost are both recorded as costs no run has shown us paying.
      **`residue` — nothing.** No new proviso on the README's re-scan paragraph: the re-scan was
      never in a position to see this, and closing it changes nothing about what it can see. The
      README sentence "not one that matches a value only partly" gains "not one whose notion of the
      value is smaller than the value", which is #41's blindness stated from the pattern's side.
      **Tests, red first.** `TestIssue61MultiHostAuthority` with exact outputs for 26 closed shapes
      on both paths and, beside every exact span, a property assertion that no declared member
      survives literally — absence of known strings, not a survival threshold, so that a later move
      to per-member placeholders rewrites `want` and nothing else; fifteen byte-identical negatives
      including the comma-URL list and the seven bracketed-prose shapes; seven open assertions
      (four #67 cells, #59 with a list, two #66). The two #58 assertions that
      pinned the leak flipped; four completeness rows flush at both ends; two templatized rows;
      9c's own output fed back to `TestPlaceholdersAreInert`; four over-mask rows. Red before the
      fix: 1 + 5 + 2 + 26 + 4. `TestNoDetectorGroupLandsOnOurOwnOutput`,
      `TestRestoreHasNoNestedMapping`, `TestPlaceholdersAreInert`, `TestMaskingIsComplete`,
      `TestEveryDetectorHasACompletenessRow` and `TestEveryUncollapsedDetectorHasATemplatizedRow`
      green throughout; 9c's group excludes `<` and `>`; without the templatized row the last of
      those goes red naming detector 11, which is why the rows land in the first commit.
      **Wire test, one run, two greps** on the same captured POST body (python `http.server`,
      `--plain --llm-anonymize --warmup-lines 0`, a `[FATAL]` line carrying the replica-set URI
      with #57's `s3://AKIA…EXAMPLE.prod@x@bucket` in its context — the level must be bracketed or
      `level=`, a bare `FATAL` does not parse and nothing escalates): `host2`/`host3` → 2 before, 0
      after; `.prod` → fires in both, as a release whose honesty is executable needs it to; the
      truncation signature `,host` → 0 after.
      `internal/pipeline` does not appear in the diff: this package runs at the LLM boundary and
      never feeds `hashTemplate`, so no template hashes move. README corrected at seven sites — the
      "what it masks" sentence, the #58 paragraph's "before and after" sentence, the audit
      paragraph, the #61 bullet struck, the two-placeholders sentence, the re-scan sentence, a new
      past-tense paragraph with the conditional table and the port asymmetry — plus #67
      beside #59 and #66 under "Known limitations". Released as `v0.9.4`.

- [x] **A raw `@` inside a userinfo is masked to the last `@`** — #58, row 18 of the #55
      sweep. `--llm-anonymize` masked `postgres://user:p@ss@db` up to the FIRST `@`, 9a read the
      `ss` behind it as an authority and tagged it `<HOST_n>`, and the real host went to the
      configured model endpoint in the clear; a raw `@` in the USERNAME (`us@er:pass@db`) sent
      the password and the host. v0.4.0 → v0.9.2, **sixteen tagged releases**, counted rather
      than derived: `git tag --contains 2da31be | wc -l` → 16, the same fifteen as #55 plus
      v0.9.2. Established from the full line history (`git log -L394,394:` on the detector-4
      line, four states: `2da31be`, `0ee8e38` #41, `4a6fc68` #43, `96abd93` #55), every one of
      which excluded `@` from every userinfo class.
      **The cause is a delimiter chosen by position rather than by rule.** Every userinfo class
      excluded `@` because "that is the delimiter they anchor on", which makes the FIRST `@`
      structural. RFC 3986 §3.2 makes the LAST one structural — userinfo may not carry a raw
      `@`, so a string with two has one legal reading — and `net/url` already implements it:
      measured again for this fix, `user="user" pass="p@ss" host="db"`, no error, and `p@s@s`
      likewise. "Last `@` wins" is the standard library's behaviour, not a heuristic.
      **The fix is not the one-character change the issue proposed, and the cell that forced
      that is #46's.** Admitting `@` into the classes and nothing else was measured over the
      whole corpus and closes every plain shape — but on `us@er:sk-live…@db` (raw `@` in the
      username, secret password) detector 4 matched the PREFIX `us@`, because the `<TOKEN_1>`
      blocks the longer parse, and 9a reading to the last `@` then sent `er` in the clear where
      it had been mis-tagged as a host. A host leak traded for a username-fragment leak is the
      wrong direction, and `residue` reads that output as clean. So detector 4 carries the
      second half of the RFC's rule as trailing CONTEXT, `authorityTail`: after the delimiting
      `@` comes `host[:port]` with no further `@`, then `/`, whitespace, `>` or the end. On the
      blocked shape that fails, detector 4 declines, and 4c — written for exactly a userinfo one
      of whose halves we already masked — takes the half whole. The parse stays unique, which
      the `mustLongest` note now argues: the tail admits no `@`, so exactly one `@` lets the
      whole pattern match. `<` is deliberately not a terminator: at detector 4's position a `<`
      after the host can only be a tag 1–3b minted inside the userinfo, which means the `@`
      just crossed was a raw one. 4b, 4c and 9a's context admit `@` too — the fourth rule that
      reads a userinfo must agree with the three that capture it on which `@` delimits — and
      carry no tail, since a tag after a raw `@` in THEIR half is a partly-recognised half, #57.
      **Bounding at `?` and `#` was the working hypothesis and it was rejected on output.** The
      same RFC paragraph ends the authority at `/`, `?` or `#`; the class excludes `/` and admits
      the other two, and applying half a rule is how this grammar accumulated its gaps. Measured:
      bounding retires the `?q=a@b` accepted over-mask row with the host still covered by 9a —
      and reopens a leak, `postgres://user:p?ss@db` going from fully masked to
      `<HOST_1>:p?ss@db`, password fragment and host both sent, on a shape the #55 comment
      already names as "illegal and happens". It is a straight trade in the disclosure
      direction and does not ship. **Why the symmetry misleads**: `net/url` cuts fragment and
      query BEFORE resolving the `@`, so it errors on a raw `?`, `#` and `/` identically
      (`invalid port ":p" after host`, all three). Those are one class — no reliable parse —
      and it is #59's. Bounding at `?#` is the `/`-exclusion extended to its siblings, i.e. part
      of #59's remedy whatever that is, and it is named here and stopped at rather than folded
      in. The `?q=a@b` row is therefore LEFT AS IT IS, byte-identical before and after, with the
      reason in its comment.
      **What admitting `@` costs, both pinned in `TestAcceptedOverMasking`.** A query carrying
      two `@`s (`?q=a@b&r=c@d`) is absorbed one parameter further — `&r=c@d` used to stay in
      the clear. A credentialed URL whose QUERY carries an `@` (`user:pass@db?x=a@b`) absorbs
      `db?x=a` into the credential token, with `b` tagged as the host. Nothing that was masked
      becomes unmasked and `Restore` is exact. Prose is unaffected: `/` and whitespace still
      bound the run — two DSNs on one line, a URL beside an email, `<postgres://u:p@db>`,
      `"postgres://u:p@db"`, a Go module path with `@v1.0.0`, all byte-identical.
      **The conditional, measured on the old code rather than reasoned.** With a raw `@` in the
      PASSWORD: bare host and loopback sent the host; a dotted host was rescued by the email
      detector (`ss@db.acme.com` as `<EMAIL_n>`); an address literal sent nothing but tagged the
      password's tail as a host. With a raw `@` in the USERNAME: bare host and address literal
      sent the PASSWORD (`<TOKEN_1>@<HOST_1>:pass@<IP_1>`); a dotted host rescued it as
      `<EMAIL_n>`. The README carries the table.
      **`residue` — half.** Detector 4 is verify-eligible and now describes the whole raw-`@`
      userinfo, so one that survives masking WHOLE mutes the escalation; that was true before
      too, since the old rule matched a prefix of it. What failed was the LEFTOVER
      (`<HOST_1>@db`), which is #41's blindness, and the fix removes the leftover rather than
      teaching the re-scan to see it. No new proviso on the README's re-scan paragraph.
      **The #57 shapes with a raw `@` move, and which fragment leaks is stated.**
      `s3://AKIA….prod@x@bucket` used to send `.prod` AND `@bucket` with `x` mis-tagged as the
      host; it now masks the host and sends `.prod@x`. Still #57, pinned as an open assertion
      on the fragment present in both states.
      **The multi-host shapes the sweep table did not have, and the finding they produced.**
      `mongodb://user:pass@host1:27017,host2:27017,host3:27017/db?replicaSet=rs0` and
      `mysql://user:pass@host1,host2/db` were run before and after: the credential span is
      right and the host list is NOT swallowed into it — but **hosts 2..n are sent, before and
      after**, because 9a's group class `[a-z0-9.-]` stops at the port colon and the comma, and
      9b rescues only a private suffix (`h2.corp.internal` masked; `h2.acme.com` and `host2`
      sent). Undocumented anywhere — README, this file, the code — and **a fourth category of
      cause**, worth naming because the other three each took several instances to notice: not
      an under-matching pattern (#41), not another component rewriting the value (#43), not
      another detector blocking it (#46/#48/#54/#57), not a shape no rule described (#55). The
      detector matched exactly what it was written for and stopped where its author thought the
      value ended; **its notion of the value is smaller than the value.** Neither `residue` nor
      the completeness tests can see that class, because both ask whether the group landed on
      the declared secret — and it did. Filed as **#61** with the replica-set URI as the
      example; pinned as an open assertion and as a literal `host2` in the multi-host
      completeness row, both red when it is fixed. The candidate is the #54 authority pre-pass,
      which this is the second issue to land on.
      **Tests.** `TestIssue58RawAtInUserinfo` with exact outputs for every position the `@` can
      occupy, both #46 cells with a raw `@` in the fallback's half, and the replica-set URI;
      seven completeness rows flush at both ends; two templatized rows through the real
      `Templatize`; the old truncated output fed back to `TestPlaceholdersAreInert`. Red before
      the fix: 12 + 7 + 1 + 2 rows. `TestNoDetectorGroupLandsOnOurOwnOutput`,
      `TestRestoreHasNoNestedMapping` and `TestPlaceholdersAreInert` green throughout; every
      widened class still excludes `<` and `>`.
      `internal/pipeline` does not appear in the diff: this package runs at the LLM boundary and
      never feeds `hashTemplate`, so no template hashes move. README corrected at four sites:
      the #58 bullet struck, the "what it masks" sentence, the "three more" audit sentence — now
      "and closing the second of those found one of a kind none of the sweeps had a category
      for" — and the new past-tense paragraph with the conditional table. Released as
      `v0.9.3`.

- [x] **A username with no password is still a credential** — #55, the third gap found in one
      grammar and the one that closed it as a class. `--llm-anonymize` sent `appuser` from
      `postgres://appuser@db:5432/app`, v0.4.0 → v0.9.1, fifteen tagged releases.
      **The cause is a grammar written as delimiters instead of as a production.** RFC 3986
      §3.2.1 gives `userinfo = *( unreserved / pct-encoded / sub-delims / ":" )` — the colon is a
      MEMBER of the production, not a separator between two of them. Detectors 4, 4b and 4c were
      all written as two runs with a literal `:` between them, which describes the same value in a
      way that leaves three of its arrangements unreachable, and all three were found separately:
      the empty username (the Redis DSN, closed alongside #46), the empty password (same patch),
      and no colon at all — this one, with nothing to anchor on. 9a's userinfo run is CONTEXT that
      steps over the value to reach the host rather than a group that captures it, because `apply`
      writes only the target group and `residue` tests only the target group, so the one rule that
      READS a password-less userinfo masks nothing. `Mask` returned nil, `residue()` returned `""`,
      the escalation was SENT.
      **`residue` could not have caught it, and that is a NEW proviso rather than #46's.** The
      re-scan is the detectors run again, so it is blind to a SHAPE NO DETECTOR DESCRIBES by
      construction — not, as with #41/#46, because a leftover no longer matches. The README's
      re-scan paragraph had one proviso and now has two; that sentence has been corrected twice
      and this is the second time.
      **The fix MERGES rather than adds, and the number three is argued.** A colon-less fourth
      rule was the obvious fix. Measured against the whole existing corpus, raw and templatized,
      it closes EXACTLY the same leaks as widening detector 4's class to admit the colon — so the
      choice was rule-set shape and not coverage, and when coverage is equal the smaller rule set
      wins. A grammar covered by four overlapping patterns keyed on which delimiters happen to be
      present is how the first three gaps happened; a fifth arrangement as a fifth pattern would
      entrench the shape that has already failed three times. Detector 4 is now one group over the
      whole userinfo and the colon is an ordinary character in it.
      **The merge stops at three rules and the wall is `detector{re, group, tag}`.** 4b and 4c
      mask the half left behind when the other was pre-masked, and that half is on the LEFT for
      one and the RIGHT for the other; one group and one tag per detector means a single pattern
      would need two capture positions. Stated in the code as a constraint: merged as far as the
      struct allows, not nearly merged. This is the second of three encounters with that same
      constraint against this same grammar — see the third below.
      **The conditional was measured, and #55 AS FILED understated it.** The issue said a dotted
      host rescues the username as an `<EMAIL_n>` and an address literal rescues it as a
      `<HOST_n>`. Both rescues are PARTIAL: each holds only while the username lies inside the
      RESCUING detector's alphabet, and both alphabets are narrower than the userinfo production.
      One sub-delim defeats the email rescue — `us!er@db.acme.com` sent the `!`; one underscore
      defeats the host rescue — `svc_user@10.0.0.5` sent `_user`. So no authority shape was safe,
      and the correction runs in the dangerous direction rather than the convenient one.
      **The over-masking trade was decided, not absorbed, and it is not purely a cost.**
      `https://api.acme.com?q=a@b` now reads as a userinfo. Measured: BEFORE the fix that input
      sent `api.acme.com` in the clear, because 9a is leftmost-longest and reads the query as
      userinfo CONTEXT, masking only the `b` behind it. So the widening CLOSES a host leak as well
      as costing an over-mask, and the package's over-mask-rather-than-under-mask asymmetry is the
      second argument rather than the only one. The brief predicted the existing
      `url query absorbed as credentials` row would move; it does not — that input has a colon and
      detector 4 has always owned it, byte-identical before and after. The colon-LESS sibling is
      what the widening costs, and it joins the table beside it.
      **Two shapes checked rather than assumed, because the colon stopped being structural.** A
      multi-colon userinfo (`user:pass:extra@`) spans byte-identically before and after — detector
      4's PASSWORD class already admitted `:`, so only the FIRST colon was ever structural, and
      only as a required delimiter. Percent-encoding needs no handling and never did: the classes
      are NEGATED, so `%40` is three characters none of which is `@`. Nothing relied on that and
      nothing tested it; two completeness rows now do.
      **What it costs is a signal on the way out, pinned in `TestAcceptedOverMasking`.** A colon
      beside an ABSENT half is captured with the half that is there, so `redis://:<TOKEN_1>@`
      becomes `redis://<TOKEN_1>@` — `Restore` is exact, but the leading colon used to tell the
      model the missing half was the USERNAME. `://:@host` is louder: a lone delimiter now mints a
      placeholder, announcing a credential where there is none. Both moved rows are in that table
      deliberately rather than arriving as a side effect of the merge.
      `internal/pipeline` does not appear in the diff: this package runs at the LLM boundary and
      never feeds `hashTemplate`, so no template hashes move.
      **The sweep is the deliverable, and it filed three issues rather than three table cells.**
      The whole userinfo production was enumerated — username present/absent, password
      present/absent, colon present/absent, either half pre-masked, pct-encoding and sub-delims —
      and every negative was RUN except three that are impossible to write and were computed from
      the production: a password with no colon (unexpressible), whitespace/`<`/`>`/`/` inside a
      half (not in the production), and a userinfo with no scheme (not a URI at all). The full
      table is below. Three shapes are open and each is filed, because a defect in a table cell is
      a defect nobody reads — row 17 sat in one and took a different investigation to surface:
      a half only PARTLY recognised, a raw `@`, and a raw `/`.

      | # | shape | today | verdict |
      |---|---|---|---|
      | 1 | `user@` — no colon | `appuser` sent | **FIXED (#55)** |
      | 2–4 | `user:pass@`, `:pass@`, `user:@` | masked | correct; 3 and 4 move the span |
      | 5 | `:@` | no match | now masks a lone delimiter |
      | 6 | `@` — empty userinfo | no match | nothing to leak |
      | 7–10 | either or both halves pre-masked | masked by 4b/4c | #46, correct |
      | 11–14 | pct-encoded, sub-delims | masked | works; now tested |
      | 15–16 | multi-colon | masked, span unchanged | only the first colon was ever structural |
      | 17 | a half recognised only in PART | `.prod` sent, residue clean | **#57, now FIXED** |
      | 18 | raw `@` in a half | the TRUE HOST sent | **FIXED (#58)** |
      | 19 | raw `/` in a half | host and remainder sent | **OPEN — #59** |
      | 20–22 | no scheme; unexpressible shapes | — | computed, not run |

      **Row 17 is not a leftover of #55 and is filed on its own terms, as #57.** An earlier detector
      recognises only part of a half, minting a tag mid-value; every credential group excludes
      `<`, so all three rules are blocked, and the remainder can lie on EITHER side of the tag.
      That is not two rules — it is none, and it is the THIRD encounter with the one-group-one-tag
      constraint against this grammar. Decisive measurement:
      `s3://AKIA…EXAMPLE.prod:secret@bucket` leaks too, so **#46's own fix does not cover it** —
      an uncovered remainder of #46 found after #46 was called closed. The candidate named in the
      draft is a second group or a second tag on `detector` rather than another pattern.
      **Rows 18 and 19 share a cause and NOT a remedy, so they are two issues, #58 and #59.** Same distinction
      the audit drew when #48 and #54 were split. Both are a delimiter appearing raw inside a half,
      and both are RFC-illegal — but `net/url` PARSES `postgres://user:p@ss@db` correctly, taking
      the last `@` as the RFC requires, so that one is not unparseable and the candidate is to
      admit `@` and let leftmost-longest reach the last one. `net/url` ERRORS on
      `postgres://user:p/ss@db`, because the authority ends at the first `/` and the parse the
      author meant and the parse the grammar mandates disagree with nothing in the text to
      separate them. There the honest candidate is to FAIL CLOSED rather than mask in part, which
      would be the first place in this package where the answer is to mask less and refuse: a
      partial mask that leaks a host is worse than a skipped escalation. **#58's fix added a
      measurement to this split**: `net/url` errors on a raw `?` and `#` exactly as it does on a
      raw `/` (it cuts query and fragment before resolving the `@`), so those two are row 19's
      class and not row 18's — see the #58 entry above.

- [x] **A UUID in a hostname is part of the host, not a value beside it** — #48, the first of the
      four the audit left open. `--llm-anonymize` sent a hostname in part or in full whenever one
      of its labels was a UUID: `https://<uuid>.blob.core.windows.net/x` sent the whole domain
      with nothing behind it, `https://api.<uuid>.acme.com/` sent `.acme.com`, and
      `worker-<uuid>.corp.internal` sent `worker-`. v0.4.0 → v0.9.0.
      **The cause is the interference class, and the fix is ordering — the tool that did NOT work
      for #46.** Detector 6 ran before both host detectors, so a UUID label minted a `<UUID_n>`
      inside their span; every host class excludes `<` and `>`, so 9a (whose group is required
      immediately after `://`) failed to match AT ALL on a leading label, truncated on a middle
      one, and 9b's `\b` re-anchored past the tag and took only the private suffix. Both host
      detectors are `verify: false`, so `residue` reported clean and the escalation was SENT.
      **Live on the RAW path only, which is most of a payload.** `Trigger.Raw`, `Trigger.Source`
      and every context line reach `Mask` as raw text; the `Template` field does not, because the
      pipeline collapses a UUID to `<UUID>` — one of the six untagged names `pipelinePH` matches —
      so the tolerant host group has always stepped over it there. A wire capture shows both in
      one payload: the trigger line leaking and the same line's masked template clean.
      `templatizedRows` carries a row that is green BEFORE and AFTER for exactly this reason.
      **Why reordering is legitimate here and was rejected for #46**, which is the part that will
      not survive in anyone's head. Most-specific-first governs detectors that COMPETE FOR THE
      SAME SPAN — an email contains a host, an IP is digits-and-dots a host regex would grab —
      and in each such pair the more specific detector must run first. Detector 6 is in no such
      relation with the host detectors: a UUID standing alone is unreachable by both, since 9a
      needs a `://` and 9b needs a dotted name on a private suffix, and a UUID has neither a
      scheme nor a dot. The only text they contend for is a UUID INSIDE a host, and there the host
      is the right answer. **Containment, not competition, and the container wins** — so the move
      APPLIES the stated principle rather than breaking it, and the package comment now says so.
      For #46 the blockers were genuinely more specific and had to precede detector 4; order was
      the wrong tool. The prediction recorded in that entry — "it remains a candidate for the
      UUID-vs-host pair below" — was right, and this is why.
      **The tag changes and that was argued, not noted.** Such a UUID now comes back `<HOST_n>`
      instead of `<UUID_n>`. The choice was never `<UUID_n>` against `<HOST_n>`; it was "`<UUID_n>`
      plus a leaked domain" against "`<HOST_n>` and nothing leaked". `HOST` is also the truer
      thing to tell the model — it is a host that recurs, not an identifier that recurs. Pinned on
      BOTH sides, because with detector 6 at the back of the chain every other detector now gets
      first refusal on a UUID and 9a's group class admits every character a UUID contains:
      `TestTypeTags` pins the change, and `TestUUIDOutsideAHostKeepsItsOwnTag` pins with exact
      outputs that a bare UUID, a UUID in a URL path and a UUID in a `/home/` path all still come
      back `<UUID_n>`. Detector 6 sits between 9b and 10 rather than at the end for the last of
      those: ahead of the home-dir detector, `/home/<uuid>/` stays `<UUID_n>` and not `<USER_n>`.
      **Stated plainly: this is a PARTIAL fix and the rest is refiled, not left in a comment.**
      The audit filed three rows as one defect because they share a cause. They do not share a
      remedy. An IPv4 literal in a host label does exactly the same thing and ordering CANNOT
      resolve it, because the two IPv4 cases want opposite orders — `https://10.0.0.5/x` needs
      detector 8 to win (the address IS the authority and the `IP` tag is the product) while
      `https://10.0.0.5.nip.io/x` needs 9a to win (the address is a label INSIDE the authority) —
      and no linear chain gives both, since the right answer depends on what the matched span is
      PART OF. That is a result, and it is what rules out another reordering and points at the
      structural candidate: a pre-pass identifying the authority span before the inner detectors
      run. Refiled as its own class under **#54**, together with a shape the sweep added to
      the table: `worker-10.0.0.5.corp.internal` sends `worker-` and has **no `://` to anchor on**,
      so the companion-detector approach that reads our own tags as context — the 4b/4c shape —
      closes only one of the two and was rejected for that reason rather than on cost.
      **The sweep, as computed results.** Which detectors can mint a tag inside a host detector's
      GROUP span? Beyond 6 (fixed) and 8 (refiled): detectors 1, 2 and 3b are structurally
      identical — their alphabets are subsets of the host classes — but require a hostname label
      that literally IS a JWT, an `AKIA…` key or an `sk-` key, so they stand where `1/2/3b → 5`
      does, theoretical. Detector 5 masks the value WHOLE under the wrong tag, harmless. Detector
      3a reaches 9a's ANCHOR rather than its group, by masking a scheme when an empty `Bearer`
      precedes a URL — that is **#52's** cause, a tolerance gap on an anchor, not this class.
      **Detectors 7 and 10 are impossible, and that is computed rather than unswept**: 7 needs a
      `:` and 10 needs a `/`, and each of those characters is one that TERMINATES a host.
      **One find was NOT in this class and is filed separately** rather than buried in an
      interference issue where nobody would look for it: a userinfo with no password is invisible
      to every credential detector, because 4, 4b and 4c are all anchored on the `:` and 9a's
      userinfo run is context that steps over the value rather than a group that captures it. So
      `postgres://appuser@db:5432/app` sends `appuser`. Same family as #46 and the empty-half gap
      — the credential grammar not covering a userinfo shape that exists in the wild — and
      different from both in which half is missing. v0.4.0 → v0.9.0. **#55**.
      `internal/pipeline` does not appear in the diff: this package runs at the LLM boundary and
      never feeds `hashTemplate`, so no template hashes move. README corrected at three sites, two
      of which this find contradicted — "the credential cases are fixed" and "none involving
      credential material" were both true of the interference audit and both false once the
      password-less userinfo turned up.

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
      | 1/2/3b → 3a | `Bearer AKIAIOSFODNN7EXAMPLE` | nothing | **harmless where the key IS the whole value**; already pinned by `TestPlaceholdersAreInert`. A key glued to text inside the value is the next row |
      | 1/2 → 3a | `Bearer AKIA…EXAMPLE.prod` | `.prod` | **DEFECT (#110), FIXED**, by a rule run before the key detectors (3c) that masks the whole bearer value. The text glued to the key, not the key; 3b runs after 3a and was never affected |
      | 1/2/3b → 5 | `eyJ….eyJ….sig@example.com` | the domain | **theoretical**; nothing puts a JWT in an email local part |
      | 6 → 9a | `https://550e8400-….blob.core.windows.net/x` | the whole domain | **DEFECT, FIXED** (#48) |
      | 6 → 9a | `https://api.550e8400-….acme.com/` | `.acme.com` | **DEFECT, FIXED** (#48) |
      | 6 → 9b | `worker-550e8400-….corp.internal` | `worker-` | **DEFECT, FIXED** (#48) |
      | 8 → 9a | `https://10.0.0.5.nip.io/x` | `.nip.io` | **DEFECT** (#54), **FIXED**. Same cause, DIFFERENT remedy: a more specific rule, not an order |
      | 8 → 9b | `worker-10.0.0.5.corp.internal` | `worker-` | **DEFECT** (#54), **FIXED**. New in the #48 sweep; not in the original table |
      | 1/2/3b → 4, 4b, 4c | `s3://AKIA…EXAMPLE.prod:secret@bucket` | `.prod` | **DEFECT** (#57), **FIXED**. A key inside a half, not a half: a rule run before the key detectors |
      | 1/2/3b → 9a, 9c | `mongodb://h1,AKIA…EXAMPLE.prod,host3/db` | `.prod`, `host3` | **DEFECT** (#57), **FIXED**, the same way; a bare private-suffix host is a pinned residual |
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
      land before this does (Done with #51, in v0.12.14.);
      and detector 9a's scheme anchor (**#52**), which #43 left
      non-tolerant while widening the host group beside it, so `s3://` templatizes to
      `s<NUM>://` and the host goes unmasked on the `Template` field. That last one was found in
      the #46 wire capture rather than by a test, and the lesson generalises: **a detector's
      tolerance has to cover its ANCHORS, not only its capture** — which is also why the
      coverage test that should have caught it did not, since it checks a detector's GROUP and
      the scheme is context. (Done with #52, in v0.12.8, together with a test that checks
      every detector's context.)
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
