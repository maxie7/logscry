<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.svg">
    <img src="docs/logo-light.svg" alt="logscry" width="440">
  </picture>
</p>

# logscry

<p align="center">
  <img src="https://img.shields.io/github/go-mod/go-version/maxie7/logscry" alt="Go version">
  <img src="https://img.shields.io/github/license/maxie7/logscry" alt="License: Apache-2.0">
  <a href="https://github.com/maxie7/logscry/releases/latest"><img src="https://img.shields.io/github/v/release/maxie7/logscry" alt="Latest release"></a>
  <a href="https://github.com/maxie7/logscry/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/maxie7/logscry/ci.yml?label=CI" alt="CI status"></a>
  <a href="https://codecov.io/gh/maxie7/logscry"><img src="https://img.shields.io/codecov/c/github/maxie7/logscry" alt="Coverage"></a>
  <img src="https://img.shields.io/github/last-commit/maxie7/logscry" alt="Last commit">
  <img src="https://img.shields.io/github/languages/code-size/maxie7/logscry" alt="Code size">
</p>

**Real-time, AI-assisted log triage. Silent on noise, speaks on signal.**

logscry tails a log or event stream as it happens, does cheap local scoring to tell
routine noise from genuine anomalies, and escalates **only** the anomalies to an LLM for
a short, structured explanation — while staying quiet on everything else.

## Demo

![logscry demo](docs/demo.gif)

> _The GIF is added separately — see [RECORDING.md](RECORDING.md). To watch it live in
> ~30 seconds, run the [demo compose](examples/demo)._

Healthy traffic scrolls on the left. One real fault — a worker that can't reach Postgres
— escalates and lands as an explained card on the right, while the routine chatter around
it stays unremarked.

## What it is

logscry is a single-binary CLI/TUI for **dev-time log triage**. Point it at a running
local stack — a piped log file, a subprocess, or your Docker containers — and it watches
the stream in real time. Most log lines are routine; logscry collapses them into
templates and scores them locally. When something is genuinely novel, bursting, or
fatal, it sends that one event (with surrounding context) to an LLM and shows you a
concise **what happened / likely cause / what to check** card.

It is **not** an "LLM observability" product (those monitor AI applications — prompts,
tokens, output quality). It is the inverse: an LLM used as a tool to make *any* system's
logs understandable.

## How it's different

- **Real-time / streaming-native** — a live tail that flags anomalies as they appear, not
  a scan-on-demand report you run after the fact.
- **Source-agnostic** — stdin, a subprocess, Docker containers, or the systemd journal
  (NATS/Kafka later). Not tied to Kubernetes.
- **Local-first** — runs offline against a local [Ollama](https://ollama.com); your logs
  stay on your machine. No cluster, no SaaS, no account.

The closest comparable, [k8sgpt](https://github.com/k8sgpt-ai/k8sgpt), proves the demand
for AI-explained errors but is Kubernetes-specific and scan-based. logscry deliberately
takes the real-time, source-agnostic, dev-time lane it leaves open.

## Installation

**Download a binary** — no Go toolchain needed. Grab the archive for your OS and
architecture from the [latest release](https://github.com/maxie7/logscry/releases/latest)
and put `logscry` on your `PATH`:

```sh
VERSION=0.8.0   # the version on the releases page, without the leading v
ARCHIVE=logscry_${VERSION}_linux_amd64.tar.gz   # or darwin_arm64, linux_arm64, …

curl -sSLO "https://github.com/maxie7/logscry/releases/download/v${VERSION}/${ARCHIVE}"
tar xzf "$ARCHIVE" logscry
chmod +x logscry
./logscry --version
```

Windows archives are `.zip`; macOS and Linux are `.tar.gz`. Every release also carries a
`checksums.txt`. Optionally confirm a download really came from this repo's release
pipeline — the archives ship with keyless GitHub build provenance:
`gh attestation verify "$ARCHIVE" --repo maxie7/logscry`.

**With Go** — if you already have a toolchain:

```sh
go install github.com/maxie7/logscry/cmd/logscry@latest
```

**From source** — for contributors, or to build a specific commit:

```sh
git clone https://github.com/maxie7/logscry.git && cd logscry
make build          # -> ./bin/logscry
```

## Quick start

The examples below use `./bin/logscry` (the from-source path); a downloaded or `go
install`ed binary is just `logscry`.

```sh
# build the single binary -> ./bin/logscry
make build

# follow all Docker containers, auto-attaching to new ones
./bin/logscry --docker-all

# run a program and watch its stdout+stderr
./bin/logscry -- ./myapp

# tail a log file (keys come from /dev/tty, so stdin stays free for logs)
tail -f /var/log/app.log | ./bin/logscry

# follow the systemd journal (Linux; see the note below on journal access)
./bin/logscry --journald

# ...or just some units, at warning and above
./bin/logscry --journald-unit nginx --journald-unit sshd --journald-priority 4

# see what WOULD escalate without calling an LLM (no model needed)
./bin/logscry --docker-all --explain-dry-run
```

Explanations use a local Ollama by default (`http://localhost:11434/v1`). Pull a model
and go:

```sh
ollama pull gemma2:2b
./bin/logscry --docker-all --llm-model gemma2:2b
```

Point it at any OpenAI-compatible endpoint (OpenAI, Groq, …) with `--llm-url` and
`--llm-model`; the API key comes from the environment, never a flag or the config file:

```sh
export LOGSCRY_API_KEY=sk-...
./bin/logscry --docker-all \
  --llm-url https://api.openai.com/v1 --llm-model gpt-4o-mini
```

In the TUI: `Tab` moves focus between the two panes, `Enter` expands the selected card,
`t` toggles the live stream against the aggregated template table, `p` pauses rendering
while ingestion continues, and `q` quits. logscry falls back to plain line output on its
own when stdout is not a terminal, so piping and redirecting never see escape codes
(force it with `--plain`).

The fastest way to see all of this is the **[demo](examples/demo)**: one
`docker compose up` and one `logscry --docker-all`.

## How it works

```mermaid
flowchart TD
    subgraph SRC["Sources"]
        A1["stdin<br/>e.g. tail -f file"]
        A2["subprocess<br/>logscry -- ./app"]
        A3["Docker logs<br/>ContainerLogs + Events<br/>stdcopy demux"]
        A4["journald<br/>journalctl -f -o json<br/>PRIORITY to level"]
    end

    subgraph CORE["Pipeline core — single goroutine, owns state"]
        NORM["normalize<br/>detect JSON, level, message"]
        TMPL["template<br/>mask variables to a signature"]
        DEDUP["dedup + count<br/>template state map"]
        SCORE["score engine<br/>novelty / burst / severity"]
        DECIDE{"escalate?"}
        NORM --> TMPL --> DEDUP --> SCORE --> DECIDE
    end

    subgraph LLMS["LLM stage — async, never blocks ingest"]
        ESCQ["escalation channel"]
        POOL["LLM worker pool"]
        BACKEND["Backend<br/>OpenAI-compatible / Ollama"]
        EXPL["explanation<br/>summary / cause / suggestion"]
        ESCQ --> POOL --> BACKEND --> EXPL
    end

    subgraph TUIS["TUI — Bubble Tea"]
        TSTATE["TUI state"]
        RENDER["render"]
        PANE1["live stream pane"]
        PANE2["flagged-event cards"]
        TSTATE --> RENDER
        RENDER --> PANE1
        RENDER --> PANE2
    end

    ING["ingest goroutines<br/>one per source"]
    RING[("global ring buffer<br/>last M lines")]
    RL["global rate limiter<br/>token bucket"]
    CACHE[("explanation cache<br/>by template hash")]

    A1 --> ING
    A2 --> ING
    A3 --> ING
    A4 --> ING
    ING -->|chan LogLine| NORM
    NORM -. feeds context .-> RING
    RL -. gate .-> DECIDE
    CACHE -. gate .-> DECIDE
    DECIDE -->|no| TSTATE
    DECIDE -->|yes| ESCQ
    RING -. context .-> POOL
    EXPL --> TSTATE
```

You can't call an LLM on every log line in real time — cost, latency, and noise all
forbid it. The interesting part is the pipeline that avoids doing so:

1. **Normalize** each line (JSON, logfmt or plain text) into a level and a message.
2. **Template** it — mask the variable parts (`<NUM>`, `<IP>`, `<UUID>`, …) down to a
   signature, so `user 4821 failed` and `user 9933 failed` become one pattern. A JSON or
   logfmt line is templated on its keys: field names stay, the message (and logfmt's
   `error`) is masked like any other text, and other values become placeholders, so
   `level=error msg="disk full"` and `level=error msg="connection refused"` stay two
   templates.
3. **Dedup and count** by that signature, tracking first/last seen and recent rate.
4. **Score** it from independent signals — **novelty** (unseen, or unseen past a
   cooloff), **burst** (rate spiking above the template's own baseline), and **severity**
   (stderr / `ERROR|FATAL|PANIC|CRITICAL`).

Most of those signals are **boosters, not triggers**: they are weighted *below* the
threshold, so they raise a score without crossing it on their own. Only a burst and a
fatal-class level fire by themselves. That distinction is the whole calibration:

| | score | escalates? |
|---|---|---|
| A template nobody has seen before | 0.45 | no — a working host emits new benign templates all day |
| Routine `stderr` + `ERROR` chatter | 0.90 | no |
| A new template, at `WARN`, on stderr | 0.95 | no — the loudest a merely-*new* line gets |
| **A new template at `ERROR`** | **1.05** | **yes** — a new *failure* is the real signal |
| 25+ of one template in 10s, at 5× its own baseline | 1.00 | yes — a *change* in rate |
| `FATAL` / `PANIC` | 1.00 | yes — even during warmup |

A burst needs the count as well as the ratio. A template that appears every few minutes
has a baseline near zero, so *any* clustering of it reads as a huge multiple — and
event-driven systems log in clusters by construction: a CI job starts ten containers
inside a second. Ten lines in ten seconds is not a flood. At the default multiplier the
count gate is the same statement as *"don't trust a ratio measured against a baseline
below 0.5/s"*; templates busier than that are unaffected by it.

The baseline forgets: each occurrence counts half as much after `--baseline-half-life`
(default `30m`). A template that was chatty for hours and then went quiet can burst again
when it comes back at a multiple of its *recent* rate. A lifetime average never let it.
A template resuming at its own former rate is not a burst unless it was silent for about
70 minutes. A template younger than the half-life is scored exactly as by a lifetime
average, and `--baseline-half-life 0` restores that average everywhere.

Every number is a knob (`--weight-novelty`, `--threshold`, `--burst-min-count`, or the
`score:` block in `logscry.yaml`); nothing is hardcoded. Turn novelty back up to `1.0` if
you want every unseen template escalated on sight.

Only events at or above the threshold escalate — and even then only if they aren't
already explained (an **explanation cache** keyed by template hash) and a **global rate
limiter** allows it. That token bucket caps LLM calls per minute *regardless of log
volume*, so cost is bounded no matter how loud the stream gets. The LLM stage is an async
worker pool: a slow or dead model degrades one card to "explanation unavailable" and
never stalls the tail.

The design bias is deliberate: **fewer, higher-confidence escalations.** A tool that
cries wolf gets uninstalled after a day.

## Configuration

Everything common is a flag; `--config logscry.yaml` covers the full set; the one secret
(`LOGSCRY_API_KEY`) is environment-only. Precedence is defaults < file < flags.

Key flags:

| Flag | Default | What it does |
|---|---|---|
| `--docker-all` | off | Follow all Docker containers, auto-attaching to new ones |
| `--docker-name <re>` | — | Follow containers whose name matches a regexp |
| `--docker-tail <n>` | `100` | Lines of history fetched per container on first attach (`all` for everything); a restarted container resumes after the last line read |
| `--journald` | off | Follow the systemd journal (Linux; see below) |
| `--journald-unit <u>` | — | Follow only these units (repeatable, OR-combined); implies `--journald` |
| `--journald-priority <n>` | `7` | Priority floor: `0` emerg … `7` debug (`3` = errors and worse) |
| `--llm-url <url>` | local Ollama | OpenAI-compatible base URL |
| `--llm-model <name>` | `gemma2:2b` | Model to ask for explanations (`ollama pull gemma2:2b`) |
| `--llm-max-tokens <n>` | `300` | Cap on tokens per explanation |
| `--threshold <f>` | `1.0` | Escalate at or above this score |
| `--weight-novelty <f>` | `0.45` | Weight of a never-seen template — below the threshold, so new alone stays quiet |
| `--burst-min-count <n>` | `25` | Fewer than this many occurrences in the burst window is never a burst, whatever the ratio |
| `--baseline-half-life <d>` | `30m` | How fast a template's baseline forgets: an occurrence counts half as much after this long (`0`: lifetime average) |
| `--rate-limit <n>` | `10` | Global cap on LLM calls per minute (the cost cap) |
| `--explain-dry-run` | off | Show what *would* escalate; build no LLM stage at all |
| `--export <path>` | off | Append one JSON object per flagged anomaly to a file (see below) |
| `--replay <file>` | — | Replay a recorded `journalctl -o json` capture on its own clock; needs `--explain-dry-run` (see below) |
| `--replay-speed <s>` | `max` | Replay pacing: `max`, or a multiple of real time such as `1x` or `10x` |
| `--llm-anonymize` | off | Mask sensitive values before sending to the LLM (see below) |
| `--llm-stream` | off | Fill card fields in as the model completes them (see below) |
| `--plain` | auto | Plain line output instead of the TUI |
| `--version` | — | Print the version and exit |

`--explain-dry-run` is the way to calibrate thresholds and to run in CI: it surfaces
every would-be escalation and, crucially, builds no backend and no worker pool — so no
request *can* be made.

**Reasoning models need more `--llm-max-tokens`.** A thinking model (e.g. `qwen3`,
`deepseek-r1`) can spend the whole default 300-token budget on its chain of thought and
return **empty** content with `finish_reason: length` — the escalation shows as
unavailable rather than explained. Raise `--llm-max-tokens` (say `1024`) for such models.
Non-reasoning instruct models like the default `gemma2:2b` are fine at 300.

A full annotated config lives at [`examples/logscry.yaml`](examples/logscry.yaml). Run
`./bin/logscry -h` for every flag.

### journald (`--journald`, Linux only)

`--journald` follows the systemd journal, so logscry can triage the host itself — units,
the kernel, the boot — and not only what runs inside a container. It works by running
`journalctl -f -o json` and decoding one entry per line, which is why the binary stays
pure Go and `CGO_ENABLED=0`: binding libsystemd would need cgo and would break the
cross-compiled darwin/windows builds. It needs `journalctl` on `PATH`, and journald
exists only on Linux.

Journal `PRIORITY` becomes the level severity scoring uses, so a unit that records a real
priority is scored honestly even when its text says nothing resembling a level token:

| PRIORITY | 0 emerg | 1 alert | 2 crit | 3 err | 4 warning | 5 notice | 6 info | 7 debug |
|---|---|---|---|---|---|---|---|---|
| level | `FATAL` | `FATAL` | `CRITICAL` | `ERROR` | `WARN` | `INFO` | *from the text* | `DEBUG` |

Entries at priority 3 and below are also tagged as **stderr**, matching how systemd
forwards a unit's stderr at 3 and its stdout at 6 — the same severity weight a container
line on stderr picks up. Because the journal's priority is structured metadata, it wins
over any level token in the message text: a unit that prints `INFO: shutting down` but
was recorded at priority 3 scores as the error it is.

Priority 6 is the exception, and it is the reason the column above says *from the text*.
Only services speaking the journal protocol set a priority themselves; a service that
merely prints to stdout has systemd record **everything** it says at 6, `ERROR:` lines
included. At 6 the journal is telling us where a line came from, not how bad it is — so
that is the one priority logscry does not treat as a level, falling back to reading the
message instead. Everywhere else the priority still wins.

> **What reading the message does and does not recover.** The fallback finds a level at
> the *start* of a line — `ERROR: ...`, `[ERROR] ...`, `level=error ...` — and, under any of
> `level`, `lvl`, `severity`, or `log.level`, inside a JSON payload or anywhere in a logfmt
> line (`time="…" level=error msg="…"`, the shape dockerd, containerd and Go's slog write). A
> line counts as logfmt only if all of it is `key=value` pairs. It does **not** find a level
> in the middle of prose, which is the usual console format for a JVM or Spring
> service (`2026-01-15 10:23:45.123 ERROR 12345 --- [http-nio-8080] com.example.Foo : ...`)
> — the level sits after the service's own timestamp and is missed. Configure such a
> service to log JSON and it is read correctly. Levels at priority **5** (notice) and **7**
> (debug) are also left alone: both are non-default, so both are taken at their word, and a
> unit logging at either still loses a level its text reports.

Lines are tagged `journald:<unit>` (`journald:nginx`, `journald:kernel`), and `--journald`
composes with the other sources — `logscry --journald --docker-all` follows both.

> **Journal access.** Reading the **system** journal requires membership of the
> `systemd-journal` group, or root. Without it `journalctl` still runs and still
> streams — but only *your own session's* logs, with nothing from any system service.
> Nothing errors; you just see far less than you expected. Grant access once with:
>
> ```sh
> sudo usermod -aG systemd-journal $USER    # then log out and back in
> ```
>
> logscry checks for this at startup and says so in the status bar (or on stderr with
> `--plain`) when it detects a reduced view, but the check stays quiet when it cannot
> tell — so if `--journald` looks unexpectedly thin, this is the first thing to check.

### Streaming (`--llm-stream`)

By default logscry waits for the whole answer and the card flips from "explaining…" to
explained in one step. `--llm-stream` asks the provider for the answer as it is generated,
so each card field appears the moment the model finishes writing it — usually the summary
first, then the cause and the check.

**What it does not do: make the model faster.** The final explanation is identical either
way, parsed by the same code; only the moment fields appear changes. What you get is a
summary a few seconds earlier and a visible sign that a slow model is producing something
rather than hanging.

Two honest caveats. Intermediate updates depend on the model actually emitting the JSON
object it was asked for: a model that answers in prose has no completed *fields* to show, so
the card simply stays "explaining…" until the end (the final answer is unaffected). And if
the stream dies partway, logscry keeps whatever fields arrived and marks the card **`answer
incomplete`** — a half-finished verdict is worth reading, but not worth mistaking for the
model's final word.

`--plain` prints only the finished answer: a line-oriented consumer cannot rewrite a line it
has already printed.

It is off by default because support for streaming alongside `response_format` varies
between OpenAI-compatible servers. If yours rejects the combination logscry notices, drops
streaming, and retries without it, so the explanation still arrives.

### Anonymization (`--llm-anonymize`)

By default logscry sends the escalated line and its surrounding context to the model
**verbatim**. Against the default **local Ollama that is fine — nothing leaves the
machine, and this flag is unnecessary.** The moment `--llm-url` points at a remote
provider (OpenAI, Groq, …) those raw lines leave your network; logscry prints a one-line
notice at startup when that is the case.

`--llm-anonymize` masks the outgoing payload and restores the model's answer on the way
back, so the terminal and the cards always show the real values while the provider sees a
type-tagged placeholder (`<IP_1>`, `<HOST_2>`) in place of every value logscry recognizes.
The tag is kept on purpose: the model still needs to know it is reasoning about *an IP* or
*a host that recurs*. The mapping is per-request and in memory only — never written to
disk, never reused.

What it masks: IPv4/IPv6, email addresses, hosts inside URLs and connection strings (any
domain, and every member of a multi-host list such as a replica set), bare hostnames on **private/infra suffixes** (`.internal`, `.local`, `.svc`,
`.lan`, `.corp`, …; extend with `--llm-anonymize-suffix`), UUIDs, known-shape secrets
(JWTs, `AKIA…` keys, `Bearer`/`sk-` tokens, and a URL userinfo in any of its shapes —
`user:pass@`, `user@`, `:pass@`, with or without a raw `@` inside it), and the username in
`/home/<user>` and `/Users/<user>` paths.

A value that is *part of* a hostname is masked as part of that host, under `<HOST_n>`, rather than
under its own tag: `https://550e8400-….blob.core.windows.net` comes back as one `<HOST_n>` and not
as `<UUID_n>` followed by a domain. The whole authority is one value, and that is what the model
should see recur. If masking a payload fails, that escalation is
**skipped** (the card says so) rather than sent in the clear. What that check can and cannot see
is worth stating exactly, because the previous wording here was wrong rather than merely vague.
It re-scans the masked text, so it catches a value a detector missed entirely — including one the
pipeline's template mask has already rewritten, which every detector now recognizes in both forms
— **provided two things: that some detector describes the value's shape at all, and that no other
detector has already rewritten part of its span.** It cannot catch a value masked only in part,
because the leftover no longer has the shape the detector looks for; and for the same structural
reason it cannot catch a value whose detector was blocked by a placeholder minted inside its span.
The first proviso is the one #55 added, and it is the flatter of the two: the re-scan is the
detectors, run again, so a *shape no detector describes* is invisible to it by construction
rather than by accident. A password-less userinfo was exactly that, and the re-scan reported
clean on every one of them for fifteen releases. Completeness is enforced by the detectors' own
tests, not at runtime.

**Compressed IPv6 was masked only in part before v0.8.6.** From v0.4.0 — the release
that introduced `--llm-anonymize` — through v0.8.5, an address written with `::` was masked
up to the `::` and the remainder was sent to the configured model endpoint as literal text.
Eleven tagged releases carry that behaviour. Expanded addresses and all IPv4 were never
affected, and nothing left the machine if the endpoint was local. If you pointed
`--llm-anonymize` at a remote provider on any of those releases, that provider received the
tail of every compressed IPv6 address in the escalated lines.

**The pipeline's template was sent in the clear before v0.8.7.** An escalation carries
both the raw line and the pipeline's template for it, and that template has already had numbers
and IDs masked (`<NUM>`, `<IP>`, `<HEX>`). Nine of the twelve detectors therefore have to
recognize a value in that damaged form, and from v0.4.0 — the release that introduced
`--llm-anonymize` — through v0.8.6, only four of them did. The other five matched up to the
first placeholder and sent the rest. In one request,
`postgres://svcuser:hunter2@db01.corp.internal/prod` went out as
`postgres://<TOKEN_1>@<HOST_1>/prod` in the trigger line and as
`postgres://svcuser:hunter<NUM>@<HOST_2><NUM>.<HOST_3>/prod` in the field beside it — the same
mapper, classifying the credential and then sending it. Twelve tagged releases carry that
behaviour. Two kinds of value were affected, and they are worth keeping apart: **credentials
and email addresses** (a `user:pass@` pair; any address with a digit anywhere in it, so
`user01@corp.example.com` and `bob@db01.example.com` went out whole while a digitless address
masked correctly), and **internal topology** (a domain — `api-gw7.prod.acme.com` sent
`.prod.acme.com`; a host label — `db01.corp.internal` sent `db`; an environment name out of a
path — `/home/deploy2prod` sent `prod`). Nothing left the machine on a local Ollama; if you
pointed `--llm-anonymize` at a remote provider, that provider received those values for every
escalated line whose template contained one.

**Half of every URL credential was sent in the clear before v0.9.0.** The credential
detector masks `user:pass` as one value, and the secret detectors that run before it — JWT,
`AKIA…`, `sk-…` — mask theirs first. So a URL whose username *or* password is itself a recognised
secret got a placeholder minted inside the credential detector's span, and since that detector's
pattern excludes `<` by design, it stopped matching entirely and the **other half** went to the
configured model endpoint as literal text. From v0.4.0 — the release that introduced
`--llm-anonymize` — through v0.8.7. Thirteen tagged releases carry it.

Which half leaked depended on which half was recognisable, and whether anything leaked at all
depended on what followed the `@` — because in each case a *different* detector happened to grab
the value under the wrong tag:

| after the `@` | secret username | secret password |
|---|---|---|
| `@db.acme.com` | password masked by accident, as `<EMAIL_n>` | **username sent** |
| `@localhost`, `@redis`, `@db` | **password sent** | **username sent** |
| `@10.0.0.5`, `@[2001:db8::1]` | **password sent** | username masked by accident, as `<HOST_n>` |

No authority shape was safe on both sides, so "conditional" here narrows the description and not
the severity — and a check against a fully-qualified host shows nothing wrong, which is why this
took three attempts to find. `postgres://sk-live…:hunter2@localhost:5432/app` sent `hunter2`;
`postgres://appuser:sk-live…@db.acme.com` sent `appuser`; `s3://AKIA…:secret@bucket` sent the
secret access key.

**A credential with an empty half was sent in the clear before v0.9.0.** A separate defect
with the same fix, kept separate because the cause is different: the credential detector required
at least one character on each side of the colon. `redis://:password@host` has no username — Redis
had none before 6.0 — so the ordinary Redis DSN matched no credential detector at all. Same span,
v0.4.0 → v0.8.7, and the same accidental rescue on a host with a dotted alphabetic suffix.

**A username with no password was sent in the clear before `v0.9.2`.** The third gap in
the same grammar, and the one with no colon anywhere to anchor on. The credential detectors were
all written around a literal `:` between two runs, and the URL-host detector's userinfo run is
*context* that steps over the value to reach the host rather than a group that captures it — so
`postgres://appuser@db:5432/app` fell through the whole chain and sent `appuser`. Masking
returned no error and the fail-closed re-scan reported clean, because no pattern described the
shape to look for it with. v0.4.0 → v0.9.1, fifteen tagged releases.

Whether the username actually left the process depended on the authority, and this is #46's
table inverted — **with both accidental rescues only partial**, which the issue as filed did not
say:

| after the `@` | password-less username |
|---|---|
| `@db`, `@redis`, `@rabbit`, `@localhost` | **sent whole** — the ordinary container and compose DSN |
| `@db.acme.com`, `@cache.acme.internal` | masked by accident as `<EMAIL_n>`, glued to the host — but **only while the username stays inside the email detector's local-part class**. One sub-delim defeats it: `us!er@db.acme.com` sent the `!` |
| `@10.0.0.5`, `@[2001:db8::1]` | masked by accident as `<HOST_n>` — but **only while the username stays inside the host detector's class**. One underscore defeats it: `svc_user@10.0.0.5` sent `_user` |

So no authority shape was safe, which is again why "conditional" narrows the description and not
the severity — and a reproduction against a fully-qualified host and a tidy username shows
nothing wrong.

**The fix merges rather than adds**, and that is worth one line here because it changes what the
package masks in two places you can see. The credential detector is now a single group over the
whole userinfo, with the colon an ordinary member of it (RFC 3986 §3.2.1), so a fourth rule for
the colon-less arrangement was not needed. Two consequences: a colon beside an *absent* half is
now captured with the half that is there, so `redis://:password@host` masks to
`redis://<TOKEN_1>@<HOST_1>` rather than `redis://:<TOKEN_1>@<HOST_1>` — the model loses the
hint that the missing half was the username; and a URL whose query contains an `@` with no
colon is now read as a credential. The second is not only a cost: before the fix
`https://api.acme.com?q=a@b` sent **the host** in the clear, so the widening closes a leak as
well as over-masking.

**A raw `@` inside a URL userinfo sent the real host before `v0.9.3`.** Every credential
rule excluded `@` on the reasoning that `@` is the delimiter, so `postgres://user:p@ss@db` was
masked up to the *first* one: the `ss` behind it was re-read as an authority and tagged
`<HOST_n>`, and the real host `db` went to the configured model endpoint in the clear. A raw `@`
in the *username* was worse — `postgres://us@er:pass@db` sent the password and the host. A raw
`@` in a userinfo is illegal (RFC 3986 wants `%40`) and ordinary, because people put `@` in
passwords and paste DSNs into logs unencoded; it is not ambiguous, because the RFC makes the
*last* `@` the delimiter and Go's `net/url` already parses it that way. v0.4.0 → v0.9.2, sixteen
tagged releases. Whether anything left the process again depended on the authority:

| after the last `@` | raw `@` in the password | raw `@` in the username |
|---|---|---|
| `@db`, `@redis`, `@localhost` | **host sent**; the password's tail tagged as the host | **password and host sent** |
| `@db.acme.com` | masked by accident as `<EMAIL_n>`, glued to the host | masked by accident as `<EMAIL_n>` |
| `@10.0.0.5` | nothing sent; the password's tail tagged as the host | **password sent** |

The fix writes the RFC's rule down in full — the delimiting `@` is the one after which no further
`@` appears before the authority ends — rather than only admitting `@` into the class, because
admitting it alone traded one leak for another: with a secret password (`us@er:sk-live…@db`) the
credential rule matched the prefix `us@` and the fragment `er` went out where it had been
mis-tagged before. Two things were checked rather than assumed. The same RFC paragraph also ends
the authority at `?` and `#`, and bounding the userinfo there — which would have retired the
`?q=a@b` over-mask above — was measured and **rejected**, because it reopens a leak:
`postgres://user:p?ss@db` is fully masked today and would send `p?ss` and `db`. `net/url` errors
on a raw `?`, `#` and `/` in a password identically, so those two belong to #59's class and not
this one. And a replica-set URI (`mongodb://user:pass@host1:27017,host2:27017/db`) kept its
credential and its first host masked, before and after — but its second host was sent, before and
after; that was #61, closed in `v0.9.4`, and it has its own paragraph below.

**Hosts 2..n of a multi-host connection string were sent in the clear before `v0.9.4`.**
`mongodb://user:pass@host1:27017,host2:27017,host3:27017/db?replicaSet=rs0` masked the credential
and `host1` and sent `host2` and `host3` — in the trigger line *and* in the masked template of the
same request; `mysql://user:pass@host1,host2/db` sent `host2`. Replica-set and failover URIs are
ordinary: MongoDB, MySQL, libpq and Sentinel all write the host list this way. v0.4.0 → v0.9.3,
seventeen tagged releases. This was a **fifth kind of cause** and the first instance of it: not an
under-matching pattern (#41), not a value another component rewrote (#43), not a detector blocked by
another's tag (#46, #48, #54, #57), not a shape no rule described (#55). The URL-host rule matched
exactly what it was written for — one host — and stopped where its author thought the value ended,
at the port colon and the comma; its notion of the value was smaller than the value. Neither the
fail-closed re-scan nor the completeness tests could see that, because both ask whether the rule's
group landed on the declared secret, and it did. Whether a later host actually left the process
depended on its suffix:

| a host after the first | before `v0.9.4` |
|---|---|
| `h2.corp.internal` (a private suffix) | rescued by the bare-host rule, under its own tag |
| `h2.acme.com`, `host2` (public suffix, bare name) | **sent** |
| `10.0.0.5`, `[2001:db8::1]` as the *first* member | masked as an address — and then blocked every name after it |

The fix is a **companion rule** rather than a wider host rule, and that was measured rather than
preferred: widening the host rule to span the list closes every shape above and *opens* a leak on a
shape masked today, a comma-separated list of URLs (`peers=https://a.com,https://b.com` would send
`b.com`, because a comma member that is the next URL's scheme is a legal hostname character
sequence and no pattern can refuse it without trailing context, which a first-pass rule cannot
afford). The companion reads the first member's placeholder as context. Until `v0.12.6` it took
hosts 2..n as **one** placeholder, so a list of any length came back as two,
`<HOST_1>:27017,<HOST_2>:27017`, with the ports between members inside the second while the first and
last stayed visible; since then every member is its own placeholder (#67, two paragraphs down). What it
costs, pinned: a path-less URL directly followed by a comma and CSV cells
(`https://api.acme.com,200,12ms`) now reads as a host list and the cells are masked — a comma *with a
space* after it, a path before it, or no scheme on the line are all untouched. What stayed open at
`v0.9.4`: an address literal (or a key-shaped member) as a *later* member still blocked every member
after it, which was #67, closed in `v0.12.6`; and an underscore in a hostname, found while
sweeping this one, was #66, closed in `v0.12.5` (next paragraph).

**An underscore in a hostname was sent in the clear before `v0.12.5`.** No host rule
admitted `_`, which RFC 3986 allows anywhere in a hostname and which logs carry in container
names, broker names and internal hosts, so every host rule stopped at the first one:
`http://my_host:8080/x` went out as `http://<HOST_1>_host:8080/x`, `postgres://u:p@db_1.acme.com/app`
sent `_1.acme.com`, `peer db_primary.corp.internal down` sent `db_primary.`, and a host that
*begins* with `_` (`http://_svc.acme.com/x`) was sent whole. The email rule had the same gap, and
there the whole value went out, user included: `user@my_host.example.com`, or an ssh-style
`deploy@build_01.example.com`. v0.4.0 → v0.12.4, thirty tagged releases. A label may now begin or
end with `_`, so a DNS service name (`_dmarc.corp.internal`) masks whole. What it costs, pinned:
an identifier glued to a private host by `_` (`env_db.corp.internal`) and a snake_case file name
on a private suffix (`app_settings.local`) now mask as hosts, an mDNS service type
(`_ipp._tcp.local`) is a host, a trailing `_` after a URL host goes into the placeholder, and a
systemd template unit whose instance carries a `_` (`worker@job_runner.service`) reads as an
address — as one without (`backup@nightly.service`) always has, which costs the model the name of
the unit that started. Measured over one laptop's journal, 70,783 records: no host mask changed;
the only effect was that last cell, on 378 records.

**A host list sent every member after an address in it before `v0.12.6`.** The `v0.9.4` fix
masked hosts 2..n as one placeholder, and that placeholder's rule cannot step over another
placeholder, so a tag minted on a *later* member ended it there:
`mongodb://host1:27017,10.0.0.5:27017,host3:27017/db` masked `host1` and the address and sent
`host3`. An IPv6 literal, bracketed or not, did the same, and so would a member that literally is a
key. The bracketed and key-shaped cases leaked on the masked template too. `v0.9.4` → `v0.12.5`;
before `v0.9.4` every member after the first was sent regardless. The rule now takes **one member at
a time** and is re-applied until nothing changes, re-anchoring each time on the last placeholder in
the list whichever rule minted it, so the example comes back as
`<HOST_1>:27017,<IP_1>:27017,<HOST_2>:27017/db`. Every member is its own placeholder, every port is
visible, and a member that recurs elsewhere in the request is seen to recur. What it costs, pinned:
the comma cells after a path-less URL above are now one placeholder per cell, so equal cells read as
equal; and a list or a run of cells with **more than 64 members after the first** is not masked at
all — the explanation is skipped, as when the fail-closed check fires, with the reason on the card.
64 is above MongoDB's 50-member replica-set limit; the bound exists because each pass rescans the
line, and without it an 8 KB line of comma cells took 7.9 s. Measured over the same 70,783 records:
nothing changed.

**An IPv4 address inside a hostname sent the rest of the name before `v0.12.7`.** The address
rule ran before every host rule, so an address that was a *label* in a name was masked on its own
and the host rules could not step over its placeholder: `https://10.0.0.5.nip.io/x` went out as
`https://<IP_1>.nip.io/x`, `worker-10.0.0.5.corp.internal` sent `worker-`,
`https://api.10.0.0.5.nip.io/x` sent `.nip.io`, and as a later member of a host list
(`mongodb://h1,10.0.0.5.nip.io,host3/db`) it ended the list and sent `.nip.io` and `host3`. The
pre-masked template was not safe either: a bare name on a public suffix (`resolve 10.0.0.5.nip.io
failed`) sent `.nip.io` there too. v0.4.0 → v0.12.6, thirty-two tagged releases. Running the host
rules first was never the answer, because `https://10.0.0.5/x` must keep coming back `<IP_1>`, and
no order of the chain gives both. The fix is a more specific rule, not a new order: one that
matches only an address joined to hostname labels by `.` or `-`, so an address that stands alone
is masked as an address exactly as before, ports, prefix lengths, sentence dots and list members
included. Each name is one placeholder, and it recurs under that placeholder across the request.
What it costs, pinned: text glued to an address by `.` or `-` that carries a letter is a host now,
so a file name (`dump-10.0.0.5.log`), a missing space after a sentence dot, a word before the
address and a version with a pre-release tag (`1.2.3.4-beta`, already read as an address) come
back as one host placeholder. A reverse-DNS name (`5.0.0.10.in-addr.arpa`) is masked whole. An
address beside digits only (`10.0.0.5.443`, `10.0.0.5-20`) is still an address. Measured over the
same 70,783 records: ten records changed, raw and templatized alike, every one of them a reverse-DNS
name; nothing narrowed.

**A URL's host was sent when its scheme carried a digit or was glued to `_` before
`v0.12.8`.** The URL-host and host-list rules find a URL by its scheme, and that part of the
pattern never learned to read the pipeline's placeholders the way the host part did. So on the
pre-masked template a scheme with a digit, `s3://` or `h2://`, became `s<NUM>://` and the rules went
quiet: `url=s3://AKIA…:secret@bucket.example.com/x` reached the model as
`s<NUM>://<TOKEN_1>:<TOKEN_2>@bucket.example.com/x`, and `s3://h1,h2,h3/x` sent every member. On
the raw line the scheme had to start at a word boundary, so a URL glued to a word by `_`
(`x_https://acme.com/x`, markdown bold `__https://acme.com__`) or to a digit (`2https://acme.com/x`)
was sent whole. Credentials were never affected; the credential rules anchor on `://` alone.
v0.4.0 → v0.12.7, thirty-three tagged releases. Both rules now share one scheme definition that
steps over a placeholder inside the scheme and asks nothing of what precedes it: a letter before a
scheme is still part of it (`xhttps://` reads as it always did), and what the old boundary refused
was only a start after `_` or a digit, both of which leaked. Every detector's surrounding pattern is
now checked against the pipeline's handwriting too, not only the value it captures. One shape is
not reached: an empty `Bearer ` directly before a URL turns the scheme into a token and the host is
still sent (recorded under #65). Measured over the same 70,783 records: nothing changed.

**A host on a suffix you configured was sent in the pre-masked template before
`v0.12.9`, wherever the pipeline had rewritten the suffix.** The bare-host rule took each
`--llm-anonymize-suffix` value as written, so on the template, where the pipeline had already
rewritten it, the rule went quiet: with `--llm-anonymize-suffix k8s`, `peer db.k8s down` was masked
on the raw line and reached the model as `peer db.k<NUM>s down` in the template. It was wider than
a digit: `eu-1` and `v1.2` became `eu<NUM>` and `v<NUM>`, `deadbeef` and `0xcafe` became `<HEX>`
with no digit at all, and a suffix starting with a digit lost the dot before it (`db1.2corp` became
`db<NUM>corp`). The raw line, the built-in suffixes and hosts inside URLs were never affected, and a
configured suffix that begins with a built-in one (`corp1`) was masked by the built-in by accident.
v0.4.0 → v0.12.8, thirty-four tagged releases. The rule now reads each configured suffix both as
written and as the pipeline rewrites it, asking the pipeline rather than keeping a copy of its
patterns; the built-in rule is byte-identical. What it costs is on the template only: the
placeholder stands for any run the pipeline rewrites the same way, so under `k8s` a `db.k88s` is a
host there though not on the raw line, and the template's host gets a placeholder of its own,
because its masked form differs from the raw line's. Changed with it, because it leaked too: a
suffix written with a dot, `.acme` or `acme.`, never matched (this README writes the built-ins as
`.internal`), so every host on it was sent on both paths. The dots are now stripped, and an empty
suffix is refused at startup, where it used to mask every dotted prefix. Measured over the same
70,783 records: nothing changed.

**A key with text beside it inside one value sent that text before `v0.12.10`.** The JWT,
AWS-key and `sk-` rules run before every credential and host rule, so a key that was only *part* of
a value was masked on its own and the rules after it could not step over its placeholder:
`s3://AKIA…EXAMPLE.prod:secret@bucket` went out as `s3://<TOKEN_1>.prod:<TOKEN_2>@<HOST_1>`, sending
`.prod`, and so did the colon-less form and a remainder before the key (`prod.AKIA…@bucket`) or in
the password. A key as a hostname label sent the domain (`https://AKIA…EXAMPLE.prod.acme.com/x`),
and as a later member of a host list (`mongodb://h1,AKIA…EXAMPLE.prod,host3/db`) it ended the list
and sent `.prod` and `host3`. The pre-masked template leaked the same way, and on it alone an `sk-`
key followed by `_` sent the `_` and what came after it. The fail-closed check reported clean every
time. v0.4.0 → v0.12.9, thirty-five tagged releases; the list member since `v0.9.4`, before which no
later member was masked at all. Running the credential rule first was never the answer, because a
key that *is* a whole half must keep its own placeholder (#46). The fix is two more specific rules
that run first, each matching only a key glued to other text inside one value: a userinfo with such
a key is one credential placeholder, and a hostname with one is one host placeholder, as an address
or a UUID inside a name is. A key that stands alone — a whole half, a whole list member, a whole
host, after `Bearer`, in prose — is masked exactly as before, and so is a key glued to text in
prose, which keeps its own placeholder with the text beside it. What it costs, pinned: such a
userinfo no longer shows its colon, so the model is not told which half carried the key; a key
inside a hostname is tagged as a host; and a key glued to text right after a comma or an `@` reads
as a host even outside a URL (`keys=a,AKIA…EXAMPLE.prod`). A key as a label of a bare host on a
private suffix (`worker-AKIA…EXAMPLE.corp.internal`) is not reached and still sends `worker-`; it is
as theoretical as the key-label cell was. Measured over the same 70,783 records: nothing changed.

**A key glued to `_` or a letter was sent whole before `v0.12.11`.** The JWT, AWS-key and
`sk-` rules ended a key at a word boundary on both sides, and `_` and letters are word characters,
so on the raw line a key glued to one was not seen at all: `key AKIA…EXAMPLE_prod used`,
`key sk-…XYZ_prod used`, `key x_AKIA…EXAMPLE used`, `key idAKIA…EXAMPLE used` and `token_eyJ…`
reached the model with the key itself in them, and the fail-closed check reported clean, because
no rule it re-ran could see the key either. The pre-masked template masked a key with text glued
*after* it and sent only the text, so the two paths disagreed; a key with text glued *before* it
was sent on both. v0.4.0 → v0.12.10, thirty-six tagged releases. The fix is one more rule per key,
run last, that ends a key where its own alphabet ends rather than at a word boundary: an AWS key
at the first character outside `[0-9A-Z]`, an `sk-` key at the first outside `[A-Za-z0-9]`, a JWT
where it always did. What is glued to the key stays prose beside its placeholder
(`key <TOKEN_1>_prod used`), as for any key in prose, and the fail-closed check re-runs the new
rule too, so a glued key that ever survived masking would mute the request. Running it last is
what keeps everything else as it was: given the key first, the bearer, email and private-host rules
would stop at its placeholder and send the glue, where they mask the whole value today. So a key
alone, after `Bearer`, in an email, a URL or a host list masks exactly as before, and a word that
only contains a prefix (`task-…`, `XAKIA…`) is still not a key. What it costs, pinned: an
uppercase run that starts like an AWS key and is longer than one is masked whole
(`region ASIAPACIFICDATACENTER01 up` → `region <TOKEN_1> up`), after a lowercase word too, as the
template always masked it. Measured over the same 70,783 records: nothing changed. None carries an
AWS or JWT prefix, and the 396 that contain `sk-` inside a word are untouched.

**Text glued to a key behind `Bearer` was sent before `v0.12.12`.** The bearer rule masks the
whole value after `Bearer` as one token, but it ran after the JWT and AWS-key rules, and a
placeholder stops it. So a key that was only part of a bearer value was masked alone and the text
glued to it was sent: `hdr Bearer AKIA…EXAMPLE.prod expired` reached the model as
`hdr Bearer <TOKEN_1>.prod expired`. A `~` or `-` glued the same way on both paths, and a `_` or a
lowercase letter did on the pre-masked template. A JWT sent what followed a `.` or `~` after its
signature, and a key with text on both sides sent the text after it. The fail-closed check read
the remainder as clean. An `sk-` key was never affected, because the bearer rule runs before it.
v0.4.0 → v0.12.11, thirty-seven tagged releases. The fix is one more rule, run before the key
rules, that matches only a bearer value with a JWT or an AWS key glued inside it and masks the
whole value as one token: `hdr Bearer <TOKEN_1> expired`, on both paths and in any case of
`Bearer`. Moving the bearer rule ahead of the key rules would have closed the same cells, but it
would also have renumbered the placeholders on every line that carries a bearer token and a key
elsewhere. The new rule is absent everywhere else, so a key that is the whole bearer value, an
`sk-` key, a bearer value with no key and every other line mask exactly as before. The same key
alone elsewhere on the line keeps its own placeholder. What it costs, pinned: a sentence dot right
after a bearer key joins the value (`auth Bearer AKIA…EXAMPLE. retry` →
`auth Bearer <TOKEN_1> retry`), as the bearer rule already reads a dot in any other bearer token.
Measured over the same 70,783 records: nothing changed. None contains `bearer`.

**What the audit found and did not close.** v0.9.0 is a minor release because the package was
audited systematically for the first time rather than because of the count above: every
detector whose pattern spans a composite value was checked against every earlier detector that
can mint a placeholder inside that span. The *interference* credential cases are fixed. **Four
were open at release time**, none of them involving credential material — a claim that has now
been corrected twice by later sweeps rather than once, so it should be read as a statement about
what the interference audit examined and not about what the package leaked. The sweep run while
closing the first of them found a credential gap the audit had no reason to look at (#55, closed
in `v0.9.2`); the sweep run while closing *that* found three more, listed last below; and closing
the second of *those* (#58, closed in `v0.9.3`) found one of a kind none of the sweeps had
a category for (#61, closed in `v0.9.4`); and the sweep that closed *that* found a class it
could not close then, #67, closed in `v0.12.6`, and a single-host leak, #66, closed in `v0.12.5`.
Of the audit's own four, the half of #48 that ordering could not reach, #54, closed in
`v0.12.7`, and the anchor gap filed beside them, #52, in `v0.12.8`; of #55's three, the
partly-recognised half, #57, in `v0.12.10`; and the glued key the work on it found unseen on
the raw line, #108, in `v0.12.11`; and the bearer remainder the work on *that* found, #110,
in `v0.12.12`.
Each time the audit's own count was right and its scope was narrower than the sentence sounded.

- ~~**#48** — a UUID inside a hostname silences both host detectors.~~ **Closed in
  `v0.9.1`.** A UUID used as a hostname label is now masked as part of the host. The audit
  filed this and the IPv4 row below as one defect because they share a cause; fixing it showed
  they do not share a *remedy*, so the count above is right about how many were open and wrong
  about how many issues they were.
- ~~**#54** — the half of #48 that ordering cannot reach.~~ **Closed in `v0.12.7`.** See the
  paragraph above. It was refiled as its own class because an inner detector minted a placeholder
  inside a host detector's span and **no ordering resolves it**: the right answer depends on what
  the matched span is part of. `https://10.0.0.5.nip.io/x` sent `.nip.io` and
  `worker-10.0.0.5.corp.internal` sent `worker-`, while `https://10.0.0.5/x` must keep masking as
  `<IP_n>`, and no linear chain gives both. That result stands. The candidate was structural, a
  pre-pass identifying the authority first; what closed it is a rule more specific than the
  address rule on exactly the span they share, an address joined to hostname labels, which no
  ordering argument rules out.
- **#49** — an IPv4-mapped IPv6 address masks only as far as its first octet:
  `::ffff:192.168.1.1` sends `.168.1.1`.
- ~~**#52** — a URL scheme containing a digit hides the host from the URL-host detector in the
  pre-masked template.~~ **Closed in `v0.12.8`.** See the paragraph above. `s3://…@bucket`
  sent `bucket`. It was not an interference defect but a tolerance gap left over from #43, listed
  here because it leaked a host. The same anchor missed a URL glued to a word by `_`, on the raw
  line too (`x_https://acme.com/x`, `__https://acme.com__`), because the scheme had to start at a
  word boundary and `_` is a word character.
- ~~**#55** — a username with no password is not a credential to any detector.~~ **Closed in
  `v0.9.2`.** See the paragraph above. The sweep that closed it enumerated the whole
  userinfo production and found three more gaps in it, which are the three below: they share a
  family with #46 rather than with #55, because in each the grammar describes the value correctly
  and something else stops the rule reaching it.
- ~~**#57** — a userinfo half that a detector recognised only in *part* leaves an
  unreachable remainder.~~ **Closed in `v0.12.10`.** See the paragraph above.
  `s3://AKIA…EXAMPLE.prod:secret@bucket` sent `.prod`, so **#46's own fix did not cover it**: an
  uncovered remainder of #46 rather than a leftover of #55. The tag minted mid-value blocked every
  credential rule, and the remainder could lie on either side of it, so no pattern run *after* the
  key was tagged could reach it. The candidate was structural — a second group or a second tag on
  the detector type — and what closed it was not: two rules that run *before* the key rules and
  match only a key glued to other text inside one value, #54's remedy rather than a new struct. The
  same key as a later member of a host list (`mongodb://h1,AKIA…EXAMPLE.prod,host3/db`), which sent
  `.prod` and `host3`, is closed with it.
- ~~**#58** — a raw `@` in a password truncates the mask and sends the real host.~~ **Closed in
  `v0.9.3`.** See the paragraph above. The candidate was not as small as the issue said:
  admitting `@` alone traded a host leak for a username-fragment leak on one shape, so the rule
  carries the RFC's full disambiguation rather than half of it.
- ~~**#61** — hosts 2..n of a multi-host connection string are sent in the clear.~~ **Closed in
  `v0.9.4`.** See the paragraph above. The host rule's notion of the value — `host[:port]` —
  was smaller than the value, `host[:port](,host[:port])*`; the rest of the list is a companion
  rule now, because widening the host rule itself was measured to open a leak on a comma-separated
  list of URLs.
- **#59** — **a raw `/` in a password: partial mask, host sent, and no reliable parse.**
  `postgres://user:p/ss@db` masks the *username* as a host and sends both `p/ss` and `db`.
  Unlike the `@` case there is no correct parse to widen towards — `net/url` errors on it, and
  the RFC says the authority ends at the first `/`, so the string does not mean what its author
  meant. The honest candidate here is to **fail closed** rather than mask in part: a partial mask
  that leaks a host is worse than a skipped escalation.

The full interference table, including the pairs that turned out to be harmless and why, is in
`BACKLOG.md`.

One value can still end up with two placeholders — `<HOST_1>` from the raw line and `<HOST_2>`
from the pre-masked template — which slightly weakens the "the model sees one host recur"
signal. Cosmetic, and now the only cost of its kind: until `v0.12.6` a host list also came back
as two placeholders — the first member and the rest — so a member that recurred elsewhere in the
payload was not seen to recur.

**This is best-effort risk reduction, not a guarantee.** Free-text log messages can
contain anything, and logscry only masks what it recognizes. Bare **public** hostnames in
prose (`could not resolve db.acme.com`) are deliberately left alone — masking every dotted
name would eat Go module paths and stack frames for no privacy gain — so a public hostname
you consider sensitive may still be sent. The fail-closed check catches a detector that
misses a value entirely, not one that matches a value only partly, not one whose notion of the
value is smaller than the value, and not unknown data.
Treat this as a way to lower exposure to a remote provider, and do not send logs you cannot
afford to send.

### Export (`--export <path>`)

Everything above puts anomalies on a screen. `--export anomalies.jsonl` puts them in a
file, one JSON object per line, so another program can have them:

```console
$ logscry --plain --export anomalies.jsonl -- ./myapp
$ jq -r 'select(.explanation.state == "explained") | .pattern' anomalies.jsonl
connection refused to <IP>:<NUM>
```

It works the same in the TUI and in `--plain`, and the file is appended to, never
truncated — a second run adds to the first. Without the flag no file is opened at all.

**One line per flagged anomaly, written when its explanation resolves.** An anomaly gets
its line when it reaches a terminal state: explained, explanation-unavailable, or
incomplete. Progressive `--llm-stream` updates never produce a line, and a template that
keeps recurring does *not* get a second one — it only bumps a counter in memory.

**The numbers are as-of-flag-time, not final totals.** `count_at_flag` and
`last_seen_at_flag` are what they were at the instant the anomaly crossed the threshold,
which is what the record is *about*. If that template has fired two hundred more times
since, the file will not say so — the keys are named the way they are so nothing pretends
otherwise.

#### Schema

Every key is always present (no `omitempty`), so a consumer can index without checking.

| Field | Type | Meaning |
|---|---|---|
| `kind` | string | `anomaly`, or `would_escalate` in `--explain-dry-run` |
| `template_hash` | string | The dedup key: the template's signature hash |
| `pattern` | string | The masked signature, e.g. `connection refused to <IP>:<NUM>` |
| `level` | string | Level of the line that fired, `""` if none was detected |
| `source` | string | e.g. `docker:api`, `stdin`, `proc:myapp`, `journald:nginx` |
| `count_at_flag` | number | Occurrences of this template **when it was flagged** |
| `first_seen` | string | RFC3339 in UTC (`…Z`); the template's first occurrence |
| `last_seen_at_flag` | string | RFC3339 in UTC (`…Z`); its last occurrence **when it was flagged** |
| `score` | number | The escalation score |
| `reasons` | array | Why it escalated, e.g. `["novel template (first seen)", "level ERROR"]` |
| `explanation.state` | string | `explained`, `unavailable`, or `not_requested` (dry-run) |
| `explanation.summary` | string | One-line "what happened" |
| `explanation.likely_cause` | string | |
| `explanation.suggestion` | string | What to check or try |
| `explanation.truncated` | bool | `true` when the answer was salvaged from a stream that died — real, but short |
| `explanation.error` | string | Why the state is `unavailable`; `""` otherwise |
| `explanation.at` | string | RFC3339 in UTC (`…Z`); when that state was reached |

**`template_hash` and `pattern` changed for logfmt lines in `v0.12.0`.** Before it, a line
like `time="…" level=error msg="disk full"` was recorded as `time=<STR> level=error msg=<STR>`.
Now the message is kept, masked like any other text: `time=<STR> level=error msg="disk full"`.
A record from an earlier version will not match a `v0.12.0` record of the same event by hash.
No other line's hash moved.

**In `--explain-dry-run` the file records what *would* have escalated.** That mode calls no
model — it builds no backend at all — so those records carry `kind: "would_escalate"`,
`state: "not_requested"`, and no summary, cause, or suggestion. They are the calibration
artifact: sort by `score`, diff two threshold settings, without a model in the loop.
`jq 'select(.kind == "anomaly")'` keeps them out of anything counting real anomalies.

**The values are real, even with `--llm-anonymize`.** That flag masks what goes to a
*remote model*; it is not a redaction of logscry's own output. The terminal keeps the real
addresses and hostnames, and this file is just as local, so it keeps them too. The one
thing the record does not carry is a raw log line: `pattern` is the masked signature, which
is what lets the whole file go into a ticket or a CI artifact without auditing it first.

**Durability.** Each record is written and synced as it resolves, in full, so a run killed
mid-flight leaves a file whose every line still parses. If a write fails part-way the
partial bytes are rolled back and that one record is lost rather than the file's validity;
logscry says so on stderr on the way out.

### Replay (`--replay <file>`)

Calibrating the scorer used to mean one of two things: running logscry on a real machine
for six hours, or writing a synthetic fixture and trusting its author. `--replay` is the
third option. It plays a recorded capture through the same decode, grouping, templating,
scoring and export as a live run. Time comes from the capture, not from the wall clock:

```console
$ journalctl -o json --since "2026-08-29 09:00" --until "2026-08-29 15:00" > capture.jsonl
$ logscry --replay capture.jsonl --explain-dry-run --plain --export before.jsonl
$ logscry --replay capture.jsonl --explain-dry-run --plain --export after.jsonl --burst-min-count 30
$ diff before.jsonl after.jsonl
```

**The clock is what makes it a replay.** Piping the same file into stdin does not work.
Every line arrives within one wall-clock second, so warmup never ends, no template gets
old enough to have a baseline, and the file is never read as journal entries. Measured on
the bundled fixture: stdin produces **0** escalations and `--replay` produces the **4** the
capture contains.

With `--replay`:
- warmup, cooloff, burst windows, baselines, the rate limiter and the explanation cache all
  run on the capture's timestamps;
- ten virtual minutes replay in about 25 ms;
- the limiter still admits 10 escalations per *capture* minute, so a replay flags what the
  live run would have flagged.

**It replays journald captures.** Record one with `journalctl -o json`: a time range as
above, a unit with `-u`, or `journalctl -f -o json | tee capture.jsonl` to record while
following. Entries go through the same decode as `--journald`, so `PRIORITY` still sets the
level. A file with no timestamps is refused, not replayed with a frozen clock. Plain text,
subprocess output and `docker logs` carry no timestamps and cannot be replayed yet
(#73).

**Keep your captures, outside the repository.** A capture is the only way to ask a new
scoring question about an old incident. A 612-record journald capture that every
calibration discussion in this project leaned on was lost because nothing said to keep it.
Store captures somewhere durable, never commit a real one (they hold host names, machine
IDs and command lines), and file the path next to the issue it informed.

**Replay is dry-run only, because replay runs the scorer, not a model.** Without
`--explain-dry-run`, `--replay` refuses to start. Dry-run builds no LLM backend, so a
replay at `1000x` cannot send a thousand times the rate-limited number of requests. It also
keeps the output deterministic. A dry-run set in your config file counts. `--replay` cannot
be combined with any live source.

**The export is deterministic.** Two replays of one capture with one configuration write
byte-identical `--export` files, and the test suite asserts it. That is what makes a `diff`
between two threshold settings mean something. In a replay the export writer waits for the
disk instead of dropping records. Timestamps in the file are capture time as a live run
would have stamped it: a line held by multi-line grouping is stamped up to
`--group-timeout` later than its journal entry. They are written in UTC whatever the
machine's time zone, so replay exports of one capture are byte-identical across machines in
different zones too (#71); a live export is not deterministic even on one machine. Fractional
seconds drop trailing zeros (`…:05Z`, `…:05.5Z`), so compare the times as times, not as
strings.

**`--replay-speed`** only paces the lines. `max` (the default) is for calibration. `1x`
replays in real time, which is for watching the TUI's cards appear when they would have.
Speed never changes what is flagged.

**Validated against a real capture once.** While fixing #37, a controlled experiment ran
on a real laptop for three hours, with a live `--journald` run beside it. Replaying the
recorded journal with the same binary and flags gave the live run's export record exactly:
kind, template, count at flag and reasons. That is one escalation on one host, so it is
evidence, not a proof. The bundled fixture (`internal/ingest/testdata/replay/basic.jsonl`)
is still synthetic. One known divergence is listed under Known limitations: a journal
entry with an empty message.

## Known limitations (v1)

- **Multi-line grouping is heuristic.** logscry folds stack traces and goroutine dumps
  into one event before templating — Python, Java, and Go traces each collapse to a
  single template, so a traceback no longer explodes into dozens of "novel" frames. The
  heuristic (indentation, frame markers, language cues) errs toward under-grouping, so an
  unusual continuation style may still split. A buffered event is flushed after
  `--group-timeout` (`group.timeout`, default `200ms`) of idle; set it to `0` to disable
  grouping entirely. A Go frame cue is a line that holds nothing but a source position: a
  path ending in `file.go:N`, optionally followed by the runtime's ` +0x…` offset and the
  `fp= sp= pc=` that `GOTRACEBACK=system` appends. Such a line continues the event before it
  even with its indentation stripped. A line that cites a position among other text is an
  ordinary log line, so slog's `source=server.go:99`, logrus's `file="main.go:42"` and
  `log`'s `Lshortfile` prefix stay one event per line.
- **A Go dump written with `gp=` goroutine headers arrives as many events.** Since Go 1.23,
  a dump under `GOTRACEBACK=system` or `crash`, and every SIGQUIT dump, writes each
  goroutine header as `goroutine 1 gp=0x… m=0 mp=0x… [running]:`. The coalescer does not
  recognize that header, so the dump is split at every goroutine and each piece is
  templated as its own event. Confirmed on real Go 1.26 output: a `GOTRACEBACK=system` panic
  of 87 lines arrives as 13 events, and a SIGQUIT dump of 116 lines as 38 (22 and 49 if a
  shipper strips the indentation). The event carrying the `panic:` line holds only the lines
  before the first goroutine header. A `debug.Stack()` written on its own splits in two. A
  panic under the default `GOTRACEBACK` (`single`, or `all`) is not affected and still
  arrives as one event. This predates v0.12.1, which does not change it. None of the ten
  recorded boots contains such a dump. Filed as #94.
- **`--journald` needs `journalctl`, and journal access is a permission thing.** The
  source is Linux-only and shells out to `journalctl`, so it fails at startup on a host
  without it. More often it does something subtler: a user outside the `systemd-journal`
  group gets a *working* follow of their own session only, with no system-service logs
  and no error to explain why. See [journald](#journald---journald-linux-only) for the
  one-line fix. There is no cursor: `-f` picks up from roughly now, so an event
  further back won't appear until it recurs. Record with `journalctl -o json` and use
  [`--replay`](#replay---replay-file) to score history.
- **Replay drops a journal entry whose message is empty, and a live run does not.** A live
  `--journald` run emits such an entry as an empty line, at the entry's priority (an empty
  kernel message at priority 3 becomes an empty `ERROR` line on stderr). `--replay` skips it,
  so a replay can score one line fewer than the live run it recorded. That is enough to
  differ by one escalation if the empty line would have escalated. Every boot of the laptop
  measured for #37 logged exactly one: kernel, priority 3. Confirmed by a test that feeds
  the same entry through both paths, not observed as a changed escalation. Filed as
  #87.
- **Three IPv6 shapes are deliberately not masked in the pipeline's `<IP>` template mask.**
  The template mask requires a boundary before the match and at least two hex groups, because
  without that a C++ scope resolution operator reads as an address — `CNSSCertStore::CNSSCertStore` used to template as
  `CNSSCertStor<IP>NSSCertStore`, naming a function that does not exist. Three real shapes pay
  for that: single-group prefixes (`fe80::/64` templates as `fe<NUM>::/<NUM>`), the bare
  loopback (`::1` → `::<NUM>`), and an address glued straight onto the token before it
  (`peer:2001:db8::1` → `peer:<NUM>:db<NUM>::<NUM>`, which fragments per address rather than
  collapsing). All three mask *less* than ideal; none corrupts an identifier, which is the
  trade being made. The third was measured rather than assumed: in a 612-record journald
  capture from a host running Docker, a VPN client and a desktop session, all 38
  address-shaped tokens were preceded by a space, bracket or comma and none was glued to the
  token before it — so that gap is one no run has yet shown us paying, rather than a known
  cost we accepted. Expanded addresses, compressed addresses after a space or bracket,
  bracketed `[addr]:port`, CIDR suffixes, and all IPv4 are unaffected. **This is the template
  mask only.** The `--llm-anonymize` anonymizer is a separate detector set and masks all three
  shapes in full — it must, because there under-masking is disclosure rather than
  fragmentation. It does **not** mask an IPv4-mapped address in full: `::ffff:192.168.1.1` masks
  as far as `::ffff:192`, which is a legal hex group, and the remaining three octets are sent.
  That is a gap in the anonymizer's own pattern (#49), filed rather than fixed here because the
  fix belongs to the same constant #42 is about. The two IPv6 patterns diverged in #40 and #41; #42
  decides whether they stay two.
- **A quoted value containing an escaped quote templates badly on the plain-text path.** The
  `<STR>` mask does not read backslash escapes, so `config "user \"bob\" logged in"` templates
  as `config <STR>bob\<STR>`: one template per value, with the text between the escapes left
  in the template. JSON and logfmt lines decode their quoting and are not affected. Filed as #91.
- **There are two "first seen"s, on two clocks, and they can disagree.** The card's `first`
  time and `--export`'s `first_seen` are **logscry's** clock: when logscry first processed
  the template. The age the model is told ("first seen N ago") is the **source's** clock: the
  trigger line's own timestamp minus the earliest one logscry has read for that template. On
  a backlog these diverge by design. Six lines a container logged over the last hour arrive
  in milliseconds on attach, so the model may say "for the past hour" while the card's
  `first` time is the moment logscry attached, two minutes ago. That is not a bug: the card
  says when logscry saw it; the model is told how long the source has been logging it. The
  age is a **lower bound**. It covers only what logscry read (`--docker-tail` history, the
  entries `journalctl -f` prints first), and it is left out for a first occurrence. On stdin
  and subprocess the source time *is* the read time, so a file piped in reports how long the
  read took. A template seen from sources with different clocks (a remote `DOCKER_HOST`
  beside local journald) is off by their skew. Before #72 the age subtracted one clock from
  the other.
- **If Docker's event stream breaks, logscry stops following Docker.** logscry learns about
  container starts, restarts and stops from the daemon's event stream. If that stream ends or
  fails, the Docker source ends: the containers already being followed stop streaming too,
  and the error is shown at once, on stderr with `--plain` and in the status bar in the TUI.
  Other sources (`--journald`, a subprocess) keep running. logscry does not reconnect: restart
  it to follow Docker again, which reads each container's `--docker-tail` lines again on first
  attach. A plausible trigger is a remote `DOCKER_HOST` behind a proxy or SSH tunnel that
  closes the idle events connection. Reconnecting is filed as #100.
- **Export drops are reported only at exit, and overstated.** In a live run the export
  writer drops a record rather than stall ingestion when its queue is full, which needs a
  disk stalled for tens of minutes at the default rate limit. The only report is one
  stderr line when logscry exits, the exit status is still 0, and the count is of queue
  messages (two per record), so it can overstate the records lost by up to 2×. Filed
  as #70. Replay does not drop.
- **`--docker-tail` defaults to 100 lines** of history per container on first attach. An
  event further back than that won't appear until it recurs — use `--docker-tail all` for
  the full backlog. A restarted container is not read from its tail again: logscry resumes
  after the last line it read. One edge: a container that delivered nothing before it
  restarted is re-attached like a first attach, so with `--docker-tail 0` the lines it logs
  between the restart and the re-attach (milliseconds) are lost.
- **Reasoning models need a higher `--llm-max-tokens`.** A thinking model (e.g. `qwen3`,
  `deepseek-r1`) can spend the whole default budget on its chain of thought and return
  empty content with `finish_reason: length`. Prefer a plain instruct model such as
  `gemma2:2b`, or raise the cap (see [Configuration](#configuration)).

## License

[Apache-2.0](LICENSE). See also [NOTICE](NOTICE).

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).
