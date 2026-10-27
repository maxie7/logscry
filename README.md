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

### From an AI coding agent

[`skills/logscry/SKILL.md`](skills/logscry/SKILL.md) teaches an agent (Claude Code, Cursor,
Codex and others that read the `SKILL.md` format) to run logscry itself: pick the source,
start with `--explain-dry-run`, bound a follow with `timeout`, read the `--export` file
instead of the screen, and ask before sending anything to a remote model. Copy the
`skills/logscry` folder into your agent's skills directory, for Claude Code
`~/.claude/skills/` or a project's `.claude/skills/`.

## How it works

```mermaid
flowchart TD
    subgraph SRC["Sources"]
        A1["stdin<br/>e.g. tail -f file"]
        A2["subprocess<br/>logscry -- ./app"]
        A3["Docker logs<br/>ContainerLogs + Events<br/>stdcopy demux"]
        A4["journald<br/>journalctl -f -o json --all<br/>PRIORITY to level"]
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

**A line is at most 64 KiB.** Every source reads its lines through the same reader. A longer
line keeps its first 64 KiB, less a character the cut would split, so it is cut to *at most*
its first 64 KiB. The rest of it, up to its newline, is read and discarded without being held,
so one runaway line costs neither memory nor pipeline time. The cut line is then processed like
any other, by its head; nothing marks it in the text. The first time a line is cut, logscry says
so once: on stderr with `--plain`, in the status bar in the TUI. The total is printed at exit,
and the exit status is not affected. A Docker line's timestamp counts toward its 64 KiB. A
journald entry is cut before it is decoded, so a cut entry is no longer JSON and shows as its
raw text, with no unit or priority, and in a replay with no timestamp. No real capture has come
close: the longest of 70,783 journal entries was 4.2 KiB.

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
`journalctl -f -o json --all` and decoding one entry per line, which is why the binary stays
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
back, so the terminal and the cards show what the log wrote while the provider sees a
type-tagged placeholder (`<IP_1>`, `<HOST_2>`) in place of every value logscry recognizes. A
placeholder the log itself carries as text is never given to a value, so it reaches the provider
and the card as written; before `v0.12.28` it could be.
The tag is kept on purpose: the model still needs to know it is reasoning about *an IP* or
*a host that recurs*. The mapping is per-request and in memory only — never written to
disk, never reused.

What it masks: IPv4/IPv6, email addresses, hosts inside URLs and connection strings (any
domain, and every member of a multi-host list such as a replica set), bare hostnames on **private/infra suffixes** (`.internal`, `.local`, `.svc`,
`.lan`, `.corp`, …; extend with `--llm-anonymize-suffix`), UUIDs, known-shape secrets
(JWTs, `AKIA…` keys, `Bearer`/`sk-` tokens, and a URL userinfo in any of its shapes —
`user:pass@`, `user@`, `:pass@`, with or without a raw `@` inside it, and with a raw `/` inside it
wherever the text can tell it from a path), vendor tokens by their published prefix, and the
username in `/home/<user>` and `/Users/<user>` paths. The vendor tokens:

- since `v0.12.15`: Stripe `sk_live_…`, `sk_test_…`, `rk_live_…`, `rk_test_…` and `whsec_…`;
  GitHub `ghp_…`, `gho_…`, `ghu_…`, `ghs_…`, `ghr_…` and `github_pat_…`; Slack `xoxb-…`, `xoxp-…`,
  `xoxa-…`, `xoxr-…`, `xoxs-…` and `xapp-…`; Google `AIza…`;
- since `v0.12.29`: Twilio `SK…` and `AC…` SIDs; SendGrid `SG.…`; npm `npm_…`; GitLab `glpat-…`
  and every other prefix GitLab documents (`gloas-`, `gldt-`, `glrt-`, `glcbt-`, …); Hugging Face
  `hf_…`; Shopify `shpat_…`, `shpca_…`, `shppa_…` and `shpss_…`; PyPI `pypi-…`.

Since `v0.12.29` it also masks an AWS secret access key written after its name
(`AWS_SECRET_ACCESS_KEY=…`, `aws_secret_access_key = …`, `"SecretAccessKey": "…"`,
`--aws-secret-access-key …`) or after its access key id, and the path of a Slack or Discord webhook
URL (`hooks.slack.com/services/…`, `discord.com/api/webhooks/…`), whose host is masked as any
URL's. A URL, a connection string, an email or a path is masked whether its `/`, `:` and `@` are
written raw or JSON-escaped: `\/`, `\u002f`, `\u003a`, `\u0040`,
and `\\/` for a string nested in another.

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

**A `::`-led address glued on by `:` was sent before `v0.12.24`.** In a `:::` run the IPv6
rule's earliest match ended in `::` (the bare `::`, or `c::` after a token ending in a hex
character) and so took the first colon of the address's own `::`. What was left had one leading
colon and no `::`, so no rule matched it and the fail-closed check read it as clean:
`peer:::ffff:0:0 down` reached the model as `peer<IP_1>:ffff:0:0 down`, which sends the whole
address, and `src:::a:b:c lost` as `sr<IP_1>:a:b:c lost`. A netstat line sent its port
(`:::22` as `<IP_1>:22`). v0.4.0 → v0.12.23, forty-nine tagged releases. The fix is one more rule,
run just before the IPv6 rule, that starts where that rule starts in a `:::` run and takes the
whole run and the hex groups after it. It matches only when a boundary follows the groups: a
space, closing punctuation, `/`, `%`, a sentence dot, or the end of the line. The boundary is not
masked, so `peer:::ffff:0:0 down` goes as `peer<IP_1> down`, on both paths and in either case.
A rule that took the groups up to anything would have cut a value the rules after it own: a host
in `:::1.db.corp.internal`, a key, a URL scheme, `ts:::INFO:::msg`, an ARN's `s3:::bucket`. Those
lines mask exactly as before. What it costs, pinned: the gluing colon goes into the placeholder,
a netstat port after the unspecified address goes with it (`:::22` → `<IP_1>`, because `:::22`
also reads as `:` before `::22`), and a `:::` operator is one placeholder. **This does not close
the class.** When anything else follows the groups, such as `-`, `_`, a letter, a dot inside a
word, or other punctuation, the address is still sent, exactly as before. That remainder is #137,
below. The glued IPv4-mapped form (`peer:::ffff:192.168.1.1`) was #49's, and is masked whole since
`v0.12.25` (next paragraph). The new rule was checked against the chain as v0.12.23 built it,
over every hand-written row and 32,000 generated inputs, with and without an extra host suffix. The
bytes it masks include every byte v0.12.23 masked, for every input. Measured over ten journald boots
(58,172 lines, raw and templated, 232,688 records): nothing changed. None holds a `:::` run.

**An IPv6 address ending in a dotted quad was sent in part before `v0.12.25`.** The last 32
bits of any IPv6 address may be written as an IPv4 address, and the IPv6 rule had no such form. It
took the groups and the first octet, which is a legal hex group, and stopped at the dot. The three
octets left over are no IPv4 address, so no rule matched them and the fail-closed check read the
line as clean: `peer ::ffff:10.0.0.5 down` reached the model as `peer <IP_1>.0.0.5 down`. The same
went for `::10.0.0.5`, `::ffff:0:10.0.0.5`, `64:ff9b::10.0.0.5`, upper case and the bracketed
`[::ffff:10.0.0.5]:443`. The full form `0:0:0:0:0:ffff:10.0.0.5` sent its groups instead, the IPv4
rule taking the quad alone, and glued on by `:` (`peer:::ffff:10.0.0.5`) it sent `:ffff:`. v0.4.0 →
v0.12.24, fifty tagged releases. The fix is one more rule, run just before the IPv6 rule, for every
form RFC 3986 allows: an address with a `::` or six groups, then a dotted quad, matched only when
the same boundary as the rule above follows. The `:::` rule takes the same right end, so the glued
form is masked whole too. A wider IPv6 pattern was measured and not taken: ending at the quad with
nothing after it cuts a value a later rule reads through the next character (`::ffff:10.0.0.5.6`
would send `.6`). What it costs, pinned: an unbracketed port goes into the placeholder
(`::ffff:10.0.0.5:443` → `<IP_1>`; after a dotted quad no hex group can follow, so it is not
ambiguous, and it is lost all the same), the hex character of a token written straight before the
address goes with it, as for every IPv6 address, and a prefix longer than an address is one value.
When `-`, `_`, a letter, a dot inside a word or other punctuation follows the quad, the address is
sent as before: that is #137, below. On the Template field most of these forms arrive as
`<IP>.<NUM>.<NUM>` with nothing of the value left; the full form keeps its groups there, which is
#135. Checked against the chain as `v0.12.24` built it, over every hand-written row, the dotted-quad
probes, prefixes of six to twelve groups and 1,000 generated lines, each raw and templatized,
with and without an extra host suffix: the bytes it masks include every byte `v0.12.24` masked,
for every input. Measured over the same ten journald boots: nothing changed. None holds an
address ending in a dotted quad.

**A raw `/` in a URL userinfo sent the password and the host before `v0.12.26`.** This was a
credential leak. RFC 3986 ends the authority at the first `/`, so no credential rule reached the
`@` after one, and the URL-host rule masked the username as if it were the host:
`postgres://user:p/ss@db/app` reached the model as `postgres://<HOST_1>:p/ss@db/app`, which sends the
password and the real host, and the fail-closed check read the line as clean. v0.4.0 → v0.12.25,
fifty-one tagged releases. What was sent depended on the authority:

| after the last `@` | raw `/` in the password | raw `/` in the username |
|---|---|---|
| `@db` | **password and host sent** | **the username's tail, the password and the host sent** |
| `@db.acme.com` | the password up to the `/` sent; the rest masked by accident as `<EMAIL_n>` | the username's tail sent; the rest masked by accident as `<EMAIL_n>` |
| `@10.0.0.5:5432` | **password sent**; the address masked | **the username's tail and the password sent**; the address masked |
| an empty username, `redis://:p/ss@redis:6379/0` | **the whole URI sent** | — |

The fix is one more rule, run first, that masks the authority whole as one token, from after `://`
through the host and port the author meant: `postgres://user:p/ss@db/app` goes as
`postgres://<TOKEN_1>/app`, on both paths, and a query or a closing quote after the host stays
outside the token. No parse is chosen, because none can be: `net/url` reads that string as the host
`user:p` and the path `/ss@db`, its author meant a userinfo and a host, and nothing in the text
separates the two. The token covers what is sensitive under either reading. Failing closed on the
shape was the candidate the issue named, and it was measured against this and not taken: it
discloses no less, and legal URIs carry the same shape, so each of those would have skipped a whole
escalation where this costs one placeholder. The rule matches only when the authority up to its first
`/` cannot be a `host[:port]` (a `:` whose right side is not a port), or when a `/` comes before the
`:` of `user:pass@`. Every other URI masks exactly as before. What it costs, pinned: the host has no
`<HOST_n>` of its own inside the token, so it is not seen to recur (leaving it outside was measured
to send the `@` that a dotted host's accidental email mask had covered); a legal URI of the second
shape is one token through its host (`docker://quay.io/ns/img:1.2@sha256:abc123` →
`docker://<TOKEN_1>`, `https://h/a:b@c`); so is a `/` and an `@` in a query after a port; and a
later `@` with no space before it extends the token, across JSON fields too, as the credential rule
already does for a raw `@`. **#59 is closed for every shape the text can identify. Four shapes are
sent by design**, because each one is also a legal URI that the text cannot tell apart from it. They
are credential leaks all the same, and they are listed under
[Known limitations](#known-limitations-v1). Checked against the chain as `v0.12.25` built it, over
every hand-written row and 500 generated URIs, each raw and templatized, with and without an extra
host suffix: the bytes it masks include every byte `v0.12.25` masked, for every input. Measured over
the same ten journald boots: nothing changed. None holds a `/` and then an `@` after a `://`.

**A JSON-escaped URL sent its credentials and host before `v0.12.27`.** This was a credential
leak, published as GHSA-p47p-xrhj-3x7p. A JSON encoder that escapes `/` writes a connection string
as `postgres:\/\/user:pass@host\/db`. PHP's `json_encode` does this by default, and so does any
encoder for a string nested in another (`\\/`). Every URL rule anchored on a literal `://` and
stopped at a literal `/`, so no rule matched. The username, the password and the host went to the
model, and the fail-closed check read the line as clean. The same held for `\u002f` and `\u002F`,
and for an encoder that also escapes `:` and `@` (`\u003a`, `\u0040`). v0.4.0 → v0.12.26,
fifty-two tagged releases.

What was sent depended on the host:

| after the `@` | sent on the trigger, the source and the context lines |
|---|---|
| `@db` | **the username, the password and the host** |
| `@db.acme.com`, `@db.corp.internal` | **the username**; the rest masked by accident as `<EMAIL_n>` |
| `@10.0.0.5` | **the username and the password**; the address masked |

The Template field sent the same on a plain-text line and on a logfmt bare value, where the escapes
reach it as written, and on a logfmt message, which reaches it re-quoted as `\\/`. In a JSON line,
a single level of escaping in the message field reaches the Template field decoded and was masked
there. Two levels arrive as `\\/` and were sent. Every other JSON field, and a quoted value in prose,
reaches it as `<STR>`.

The fix teaches no rule the escapes. The `://` anchor is in eight rules, and `/` is a boundary in
more. Instead, a line with an escaped `/`, `:` or `@` is masked twice: once as written, exactly as
before, and once as a view with those escapes resolved. The view resolves:

- `\/`;
- the `\u` escapes of `/`, `:` and `@`;
- any run of backslashes before one of those;
- the pipeline's form of `\u002f` on the Template field, `\u<NUM>f`.

Every value either pass masks is masked in the line as written, overlapping ones as one value.
`connect postgres:\/\/user:pass@db\/app failed` goes as `connect postgres:\/\/<TOKEN_1>@<HOST_1>\/app
failed`. The model never sees the view; the placeholder stands for the bytes the log wrote, and
Restore returns them escaped. Masking the view alone would have masked less than before on three
shapes, because v0.12.26 read an escape's own characters as part of a value: the `\` of
`user:1234\/5@db`, `us\u002fer` as a username, `u002fx` as an email's local part. The union keeps
every byte it masked. A line with no escape is masked exactly as before.

What it costs, pinned:

- `\/` is read everywhere, not only inside a JSON string. A path that escapes its slashes is read
  as a path: `copy C:\/Users\/bob\/x failed` goes as `copy C:\/Users\/<USER_1>\/x failed`.
- `\u<NUM>f` also stands for other characters whose escape has three digits and an `f`, such as
  the Cyrillic `п` in Python's ASCII-only JSON. Those are read as `/`, which can only mask more.
- Where the email rule had masked the `@` and the host by accident (a dotted or private host), the
  credential, the `@` and the host are one `<TOKEN_n>`. The host has no tag of its own there.
- Where v0.12.26 read an escape's letters as part of a value (`\u002fappuser@db.acme.com`), those
  letters stay inside it, and the escape's backslash is left beside the placeholder.
- One new way to skip an escalation, until `v0.12.28`: a line with an escape that also carried one
  of logscry's own placeholders as text, the same one a pass then minted for a value, was refused
  rather than sent. Such a line is sent now, with the placeholder kept as text (below).

Two escapes are not read, and they are listed under
[Known limitations](#known-limitations-v1). Checked against `v0.12.26`'s `Mask`, frozen in a test,
over every hand-written row, the earlier gates' corpus in six escaped forms, and 1,000 generated
JSON lines, each raw and templatized, with and without an extra host suffix: the bytes it masks
include every byte `v0.12.26` masked, for every input. Measured over the same ten journald boots:
nothing changed. None holds an escaped `/`, `:` or `@`.

**A line carrying one of logscry's own placeholders as text was read back with the wrong value
before `v0.12.28`.** This was a correctness defect, not a leak: nothing more reached the model
endpoint. The placeholder counter never looked at the text, so a line that already said `<HOST_1>`
had `<HOST_1>` minted for a real value too. `login as <HOST_1> from db.corp.internal` went as
`login as <HOST_1> from <HOST_1>`.
- **The model** was told one host recurs where the log named two things.
- **The card** showed the real value in the literal's place: `login as db.corp.internal from
  db.corp.internal`.

Explain masks the trigger, the source, the template and each context line through one mapping, so
a `<HOST_1>` in a context line took the trigger's host the same way. The Template field carries
such text only from a JSON key; everywhere else the pipeline makes its digits `<NUM>`.
v0.4.0 → v0.12.27, fifty-three tagged releases. From `v0.12.27`, a line with a JSON escape and
such a collision was refused rather than sent.

Every placeholder a request carries as text is now reserved before anything in it is masked, and
the counter skips it: that line goes as `login as <HOST_1> from <HOST_2>`, and the card shows
`login as <HOST_1> from db.corp.internal`. Restore already returned a placeholder it never issued
as the text it was. If a mapping is ever told about such text after it minted the same placeholder,
the request is refused rather than read back wrongly. Checked against `v0.12.27`, frozen in a test,
over every earlier gate's corpus and 1,000 generated lines that carry placeholders in every
position and escaped form: the same bytes are masked as before, for every input. Only the
numbering moves, and only on a line that carries a placeholder. Over the same ten journald boots
nothing changed, and none carries one.

**Vendor tokens, an AWS secret access key and a webhook URL's path were sent whole before
`v0.12.29`.** This was a credential leak. No rule had the shape of a Twilio, SendGrid, npm,
GitLab, Hugging Face, Shopify or PyPI token, of an AWS secret access key, or of the path of a Slack or
Discord webhook URL. Each went to the model endpoint, and the fail-closed check read the line as
clean. v0.4.0 → v0.12.28, fifty-four tagged releases.

| what | sent on the trigger, the source and the context lines | sent on the Template field |
|---|---|---|
| Twilio `SK…` SID | **the SID** | its letters |
| Twilio `AC…` SID | **the SID** | its letters when glued to a word; nothing alone (`<HEX>`) |
| SendGrid `SG.…` | **the key** | its letters, `_`, `-` and `.` |
| npm `npm_…` | **the token** | its letters |
| GitLab `glpat-…` and the other prefixes | **the token** | its letters, `_`, `-` and `.` |
| Hugging Face `hf_…` | **the token** | its letters: **the token**, when it has no digit |
| Shopify `shpat_…`, `shpca_…`, `shppa_…`, `shpss_…` | **the token** | its letters |
| PyPI `pypi-…` | **the token** | its letters, `_` and `-` |
| AWS secret access key, beside its name or its key id | **the key** | its letters, `/` and `+` |
| Slack webhook URL | **the team id, the bot id and the secret**; the host masked | the same, but their digits |
| Discord webhook URL | **the webhook id and token**; the host masked | the token, but its digits |
| Slack user token glued to a word (`xoxp-…_prod`) | masked since `v0.12.15` | its hex letters |

On a JSON line only the message field reaches the Template field as text; every other value is
`<STR>` there.

The fix adds rules. Of the old ones only the Slack family changes, by one more way to read a glued
user token on the Template field.
- Seven vendor families join the vendor-token rule of `v0.12.15`. Each is read from the vendor's
  published prefix, alphabet and shortest length, and where a vendor publishes no format, from the
  structure of observed tokens. GitLab's body is inferred (below).
- Each family is read on the Template field too, where digits are `<NUM>`: a run of its alphabet
  with a placeholder in it, above a floor. A Slack user token glued to a word gets the same.
- An AWS secret access key has no prefix, so context is its only evidence. Two shapes are read:
  - forty or more characters of its alphabet after `aws_secret_access_key` in its env, INI, YAML,
    JSON and command-line spellings;
  - exactly forty after its access key id.
- A webhook URL's host and the documented path prefix are the evidence for the path. Every segment
  after the prefix, ids included, is masked as one `<TOKEN_n>`. The host is masked as before:
  `post https://hooks.slack.com/services/…` goes as `post https://<HOST_1>/services/<TOKEN_1>`.

Every new rule but the webhook rule runs after every older rule, so the older rules see the line they
saw before. The webhook rule runs just before the URL-host rule, while the host is still raw; the
rules after it see the path masked.

What it costs, pinned:

- A long enough word after a prefix is a token: a kebab-case name of twenty characters after a
  GitLab prefix, a camelCase word of thirty-four after `hf_`.
- On the Template field a short run with a digit after a prefix is a token:
  - three units after `SK`, `AC` or a Shopify prefix;
  - seven after a GitLab prefix;
  - twelve after `SG.`, `npm_` or `hf_`;
  - twenty-four after `pypi-AgE`.

  A hex run longer than a SID after `SK`, and an uppercase hash glued after `AC`, are SIDs there.
- Forty characters of the AWS alphabet after any `<TOKEN_n>` are a key, because the rule cannot tell
  which secret the placeholder stands for: a SHA-1 after a GitHub token is masked.
- After an AWS secret's name:
  - a value of forty or more characters is a key, whatever it is;
  - on the Template field, so is a value one character off forty, beside the name or the key id.
- Every path segment after a webhook prefix is part of the token. That includes Discord's `/slack`
  and `/github` routes, and a private name inside the path (`…/db.corp.internal`), which is no longer
  a `<HOST_n>` of its own.

What stays sent is listed under [Known limitations](#known-limitations-v1). The check: the chain as
`v0.12.28` built it, frozen in a test, over every earlier gate's corpus, every new row (raw,
templatized and in five escaped forms) and 4,000 generated lines. For every input, the bytes masked
include every byte `v0.12.28` masked. In that corpus, ninety-one generated lines per mapper that
`v0.12.28` sent are refused now. Each carried one of these secrets packed against another value,
where the masking pass could not reach it and the re-scan found it. Nothing is sent where
`v0.12.28` sent the secret. Measured over the same ten journald boots: nothing changed, and none
carries one of these formats.

**Part of a vendor key or an AWS secret access key was sent before `TODO-VERSION`.** This was a
credential leak, partial: GHSA-8qq6-vx8j-rx37. The vendor-token rule and the AWS secret rule run
last, and stop at a placeholder. When an earlier rule had already masked a piece inside a key, they
masked at most one side of that piece, and the rest of the key went to the model endpoint. The
fail-closed check read the remainder as clean. Affected: Stripe, GitHub classic and fine-grained,
Slack and Google keys from `v0.12.15`; the families `v0.12.29` added and the AWS secret access key
in `v0.12.29` only. Twilio SIDs were never split.

The pieces that were masked first, measured over every earlier rule:

- an `sk-` key with a run of sixteen inside a dashed or underscored body (`AIza…-sk-…`);
- on the Template field, a digit, a prefix and a digit (`…4sk7…`, `…5xoxb7…`), which the pipeline
  writes `<NUM>sk<NUM>`, read as an `sk` or Slack key;
- another family's key inside a key's body, an access key id, a private host suffix (`.lan`,
  `.svc`), a JWT, a UUID and a home directory.

On random keys, raw lines were not affected, and on the Template field 3 to 21 keys in 80,000 per
family were split (none for Shopify). Every family but Twilio was split when the body held one of
those pieces, raw or on the Template field.

The fix reads the vendor-token rule and the AWS secret rule once more, each alone, on the text as
the log wrote it, and masks the union with what the other rules masked: a key with a piece masked
inside it is one `<TOKEN_n>`. No rule moved and none changed. The AWS secret is read after its key
id itself rather than after the key id's placeholder, which also closes a secret sent after an id
longer than a real one whose text an earlier rule had masked under its own tag (found on
the way, and part of the same advisory).

What it costs, pinned:

- the key absorbs what the masked piece ran into: `…_prod` after a Stripe key on the Template
  field goes with the key;
- a value that overlaps a key the other rules split, such as a UUID inside it, is part of the
  key's `<TOKEN_n>`; a key inside a host or an email that was masked whole stays that value's;
- five lines of the check's corpus that were sent with part of a key are now refused whole, the
  safe direction. Each is an AWS secret's value on the Template field whose key is now one
  `<TOKEN_n>` followed by a space and a long run of the AWS alphabet. The fail-closed check reads
  that as a secret after a placeholder (forty characters after any `<TOKEN_n>`, a cost of the fix
  above). The masking itself cannot mask that run: it reads the text as the log wrote it, where
  the run follows raw text, and only its own output puts a placeholder there.

The check: the same chain with the extra reading off, over every earlier gate's corpus, every new
row and 2,000 generated keys with a piece inside, each raw, templatized and in five escaped forms.
For every input, the bytes masked include every byte the chain alone masked, and no byte of a key
is sent. Measured over the same ten journald boots: nothing changed.

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
in `v0.12.12`; and the last of #55's three, the raw `/`, #59, in `v0.12.26`, for every shape
the text can identify.
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
- **#137** — an IPv6 address that the two boundary-bounded rules own is still sent when something
  other than a boundary follows it. Glued to the token before it by `:`, an address that begins with
  `::` goes in part: `peer:::ffff:0:0-y` sends `ffff:0:0-y`. An address ending in a dotted quad goes
  in part glued or not: `peer ::ffff:10.0.0.5-x` sends `.0.0.5-x`, and `::ffff:10.0.0.5.example`
  sends `.0.0.5.example`. The same goes for `_`, a letter, a dot inside a word, and other
  punctuation. This is the rest of the classes `v0.12.24` and `v0.12.25` mask only up to a
  space, closing punctuation, `/`, `%`, a sentence dot or the end of the line. Not affected: the
  same addresses followed by one of those, and every other IPv6 address.
- **#135** — on the template, an IPv6 address the template mask leaves uncollapsed sends its hex
  letters: `peer:2001:db8::1` sends `db`, and `route fe80::/64` sends `fe`. So does the full form of
  an address ending in a dotted quad, whose groups the template mask leaves beside an `<IP>`
  (`2001:db8:1:2:3:4:10.0.0.5` sends `db`; `0:0:0:0:0:ffff:10.0.0.5` sends `ffff`). Not
  affected: the raw line, where these shapes are masked whole, and every address the template mask
  collapses to `<IP>`.
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
- ~~**#59** — a raw `/` in a password: partial mask, host sent, and no reliable parse.~~ **Closed
  in `v0.12.26`**, for every shape the text can identify. See the paragraph above.
  `postgres://user:p/ss@db` masked the *username* as a host and sent both `p/ss` and `db`. There
  is still no correct parse to widen towards, and the remedy does not pick one: the authority is
  masked whole, which covers what is sensitive under either reading. The candidate named here,
  failing closed, was measured and not taken: it disclosed no less, and it would have skipped legal
  URIs that carry the same shape. Four shapes that are also legal URIs are sent by design; they are
  listed under [Known limitations](#known-limitations-v1).

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

A value that reaches the model although this section says it is masked is a vulnerability:
report it privately, as [SECURITY.md](SECURITY.md) describes, not in a public issue.

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
keeps recurring does *not* get a second one — it only bumps a counter in memory. A template
that escalates *again*, once `--cache-ttl` has passed, gets a second record of its own, with
its own counts and its own explanation, even when the model has not yet answered the first.

**The numbers are as-of-flag-time, not final totals.** `count_at_flag` and
`last_seen_at_flag` are what they were at the instant the anomaly crossed the threshold,
which is what the record is *about*. If that template has fired two hundred more times
since, the file will not say so — the keys are named the way they are so nothing pretends
otherwise.

#### Schema

Every key of a kind is always present (no `omitempty`), so a consumer can index without
checking. A record of an anomaly has these keys:

| Field | Type | Meaning |
|---|---|---|
| `kind` | string | `anomaly`, or `would_escalate` in `--explain-dry-run` (`export_incomplete` is below) |
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

#### When the file cannot keep up

A live run never waits for the export file: ingestion matters more. The writer has a queue
of its own, and when a disk stalls for long enough to fill it (tens of minutes at the
default rate limit), the records that do not fit are lost. Replay is the exception: it
waits for the disk and loses nothing. Losing a record is never silent:

- **While the run is live.** With `--plain`, one line on stderr the first time it happens:
  `logscry: export: records are being lost — the export file is not keeping up; the total
  is reported at exit`. In the TUI, the same news as a background notice, and a status-bar
  field ` · export lost N` with the running count (absent while nothing is lost).
- **At exit.** `logscry: export: N record(s) lost — the export file could not keep up`, and
  the exit status is 1 (see [Exit status](#exit-status)), in `--plain` and the TUI alike.
- **In the file.** The last line says how many records the file is missing:

  ```json
  {"kind":"export_incomplete","records_lost":412,"at":"2026-08-29T15:00:04Z"}
  ```

  | Field | Type | Meaning |
  |---|---|---|
  | `kind` | string | `export_incomplete` |
  | `records_lost` | number | Records this run lost; the same number as the exit-time line |
  | `at` | string | RFC3339 in UTC (`…Z`); when the run wrote it, which places it among the records of other runs appended to the same file |

  It is written only when a record was lost, so a complete file holds its records and
  nothing else, byte for byte what it held before this kind existed. `select(.kind == "anomaly")` and `select(.explanation.state == "explained")`
  skip it; a plain line count (`wc -l`) includes it.

A count of lost records is a count of whole records. A record is an anomaly and its
explanation: when one is lost, so is the other, and a record never pairs the flag of one
escalation with the explanation of another.

**Exit is bounded when the disk is stuck.** At exit a live run drains its queue to the file
for as long as the file keeps accepting writes, however slowly. It gives up only when the
file has accepted nothing for **10 s**. The records still queued then are counted as lost,
the trailing record is not written, and stderr says so: `logscry: export: incomplete — the
export file accepted no write for 10s; gave up with N record(s) still queued; the trailing
export_incomplete record could not be written`. When the drain finishes but the trailing
record itself takes more than 2 s, stderr says that it could not be written. Replay waits
as long as it takes.

**After an incomplete export, discard a final line that doesn't parse.** When logscry gives
up, the record it was writing at that moment may be missing, complete, or cut short: a disk
stuck part-way through a write leaves a last line without its newline, which no one is left
to roll back. Every line before it is whole.

### Replay (`--replay <file>`)

Calibrating the scorer used to mean one of two things: running logscry on a real machine
for six hours, or writing a synthetic fixture and trusting its author. `--replay` is the
third option. It plays a recorded capture through the same decode, grouping, templating,
scoring and export as a live run. Time comes from the capture, not from the wall clock:

```console
$ journalctl -o json --all --since "2026-08-29 09:00" --until "2026-08-29 15:00" > capture.jsonl
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

**It replays journald captures.** Record one with `journalctl -o json --all`: a time range
as above, a unit with `-u`, or `journalctl -f -o json --all | tee capture.jsonl` to record
while following. Without `--all`, journalctl records any field over 4096 bytes as `null`, so
a long message is lost from the capture (see [Known limitations](#known-limitations-v1)).
Entries go through the same decode as `--journald`, so `PRIORITY` still sets the
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
is still synthetic.

### Exit status

| Status | When |
|---|---|
| `0` | The run finished: its sources ended, or it was stopped with Ctrl-C or `SIGTERM`. A source that failed along the way (`-- ./app` exiting non-zero, a broken Docker events stream) is reported on stderr or in the status bar, not in the status, and so is a line cut at 64 KiB (see [How it works](#how-it-works)). |
| `1` | logscry could not run as asked (a bad flag or config file, `-h`, no log source, an export file it could not open, a TUI that failed), **or** the export lost records (see [When the file cannot keep up](#when-the-file-cannot-keep-up)). |

The status is the same in `--plain` and the TUI: it describes the run and the file, not the
renderer, so a job checking it sees an incomplete export whichever way the output went.

## Known limitations (v1)

- **Multi-line grouping is heuristic.** logscry folds stack traces and goroutine dumps
  into one event before templating — Python, Java, and Go traces each collapse to a
  single template, so a traceback no longer explodes into dozens of "novel" frames. The
  heuristic (indentation, frame markers, language cues) errs toward under-grouping, so an
  unusual continuation style may still split. A buffered event is flushed after
  `--group-timeout` (`group.timeout`, default `200ms`) of idle; set it to `0` to disable
  grouping entirely. A Go frame cue is a line that holds nothing but a source position: a
  path ending in `file.go:N`, optionally followed by the runtime's ` +0x…` offset and the
  `fp= sp= pc=` that `GOTRACEBACK=system` appends, or an assembly frame's path ending in
  `file.s:N`, which always carries the offset. Such a line continues the event before it
  even with its indentation stripped. A line that cites a position among other text is an
  ordinary log line, so slog's `source=server.go:99`, logrus's `file="main.go:42"` and
  `log`'s `Lshortfile` prefix stay one event per line.
- **`--journald` needs `journalctl`, and journal access is a permission thing.** The
  source is Linux-only and shells out to `journalctl`, so it fails at startup on a host
  without it. More often it does something subtler: a user outside the `systemd-journal`
  group gets a *working* follow of their own session only, with no system-service logs
  and no error to explain why. See [journald](#journald---journald-linux-only) for the
  one-line fix. There is no cursor: `-f` picks up from roughly now, so an event
  further back won't appear until it recurs. Record with `journalctl -o json --all` and use
  [`--replay`](#replay---replay-file) to score history.
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
  shapes in full on the raw trigger and context lines — it must, because there under-masking is
  disclosure rather than fragmentation. On the Template field it does not: by the time the
  anonymizer reads it, the pipeline has turned these shapes' digits into `<NUM>`, and the hex
  letters left behind are sent (#135). An address ending in a dotted quad (`::ffff:192.168.1.1`)
  is masked whole on the raw lines since `v0.12.25`, by a rule of its own beside the
  anonymizer's IPv6 pattern rather than inside it; until then it was masked only as far as
  `::ffff:192` and the remaining three octets were sent. On the Template field it arrives as
  `<IP>.<NUM>.<NUM>`, with nothing of the value left. The two IPv6 patterns stay two on purpose
  (#42), because each fails in the other's cheap direction;
  `internal/anonymize/ipv6_compare_test.go` runs both over one table and names every place they
  differ.
- **With `--llm-anonymize`, a C++ scope operator is redacted as an IPv6 address.**
  `CDBusNMHelper::GetDNSConfig failed` reaches the model as
  `CDBusNMHelper<IP_1>GetDNSConfig failed`. Where the name has hex letters next to the `::`, the
  placeholder takes them too: `CNSSCertStore::CNSSCertStore` goes as
  `CNSSCertStor<IP_1>NSSCertStore`. The card is not affected, because `Restore` puts the real name
  back. The cost lands on the model, which is asked about a symbol that does not exist and may
  explain the fault worse. This is a deliberate trade in the disclosure direction, and a sharper
  pattern cannot undo it: `e::C` inside `CNSSCertStore::CNSSCertStore` is a valid IPv6 address
  glued to a word character on both sides, so any rule that refuses it also refuses an address
  written the same way, and would send it. The template mask refuses these shapes, because there
  an over-match corrupts the card; here an under-match is disclosure. Decided in #42.
- **#137 — with `--llm-anonymize`, an address that begins with `::` and is glued to the
  token before it by `:`, or that ends in a dotted quad, is still sent when a word character or
  other punctuation follows it.**
  `peer:::ffff:0:0-y down` reaches the model as `peer<IP_1>:ffff:0:0-y down`, which sends the
  whole address. A match ending in `::` starts one colon early and takes the first colon of the
  address's own `::`, and what is left is a shape no rule matches, so the re-scan reads it as
  clean. Since v0.4.0. `v0.12.24` masks the address whole when a boundary follows it: a space,
  closing punctuation, `/`, `%`, a sentence dot, or the end of the line. It does not mask it when
  `-`, `_`, a letter, a dot inside a word, or other punctuation follows, because each of those can
  continue a value a later rule owns (a hostname, a key, a field separator). `v0.12.25` masks an
  address ending in a dotted quad whole on the same terms, glued or not, so it stops at the same
  characters: `peer ::ffff:10.0.0.5-x down` reaches the model as `peer <IP_1>.0.0.5-x down`, and
  a fifth octet, a dotted name or an address glued on by `:` after the quad leave it to the rules as
  they were. Affected: a `::`-led address written straight after a `:`, or an address ending in a
  dotted quad, followed by one of those characters. Not affected: every other address, and the same
  addresses followed by a boundary.
- **#135 — with `--llm-anonymize`, the Template field sends the hex letters of an IPv6 address the
  template mask fragments.** The three shapes the template mask leaves uncollapsed (a one-group
  prefix, the bare loopback, an address glued onto the token before it), and the full form of an
  address ending in a dotted quad, reach the anonymizer with their digits already `<NUM>`, and it
  cannot recognise an address in what is left:
  `peer:2001:db8::1` is sent as `peer:<NUM>:db<NUM><IP_1><NUM>` (sending `db`), and
  `route fe80::/64` as `route fe<NUM><IP_1>/<NUM>` (sending `fe`). Since v0.8.5. Affected: the
  Template field only. Not affected: the raw trigger and context lines, where all three shapes are
  masked whole, and every address the template mask collapses to `<IP>`.
- **With `--llm-anonymize`, four shapes of a raw `/` in a URL userinfo are still sent, by
  design.** These are credential leaks. Since `v0.12.26` the authority is masked whole when the
  text shows that the `/` is inside a userinfo (#59). In these four shapes it cannot: each is also a
  legal URI, a host and a path, and masking it would mean masking every URL path that holds an `@`.
  They are sent exactly as before, since v0.4.0, and the fail-closed check reads them as clean:

  | shape | reaches the model as | sent | the legal URI it cannot be told from |
  |---|---|---|---|
  | a password that is all digits before the `/`: `postgres://user:1234/5@db` | `postgres://<HOST_1>:1234/5@db` | the password and the host | host `user`, port `1234`, path `/5@db` |
  | a username with a `/` and no password: `postgres://us/er@db` | `postgres://<HOST_1>/er@db` | the username's tail and the host | host `us`, path `/er@db`, as in `https://h/users/bob@corp.com` |
  | an `@` before the `/`: `postgres://user:p@s/s@db` | `postgres://<TOKEN_1>@<HOST_1>/s@db` | the password's tail and the host | userinfo `user:p`, host `s`, path `/s@db` |
  | a password that starts with `/`: `postgres://user:/ss@db` | `postgres://<HOST_1>:/ss@db` | the password and the host | host `user`, an empty port, path `/ss@db` |

  Percent-encode the `/` as `%2F`, as RFC 3986 requires, and each of the four is masked whole:
  `postgres://user:1234%2F5@db` goes as `postgres://<TOKEN_1>@<HOST_1>`. Affected: these four
  shapes. Not affected: every other raw `/` in a userinfo, and a percent-encoded `/`.
- **With `--llm-anonymize`, an escaped `:` or `@` is not read on the Template field of a
  plain-text or logfmt line.** These are credential leaks. Since `v0.12.27` a URL whose `/`, `:`
  or `@` is JSON-escaped is masked as if it were written raw (GHSA-p47p-xrhj-3x7p). On those lines
  the pipeline's template mask rewrites `\u003a` and `\u0040` to `\u<NUM>a` and `\u<NUM>`.
  `\u<NUM>` is also what it makes of `\u0022`, `\u0027` and `\u0026`, the quote, apostrophe and
  ampersand .NET escapes by default, so reading it as `@` would be a guess.
  `dsn=postgres:\/\/user\u003apass\u0040db\/app` sends the password and the host on the Template
  field. Affected: the Template field of a plain-text or logfmt line whose userinfo escapes `:` or
  `@`. Not affected: the raw trigger, source and context lines, where it is masked, and a JSON
  line, whose message field the pipeline decodes before templating.
- **With `--llm-anonymize`, a `\u` escape of any other URL character is not read.** No known
  encoder writes one. A host whose dots are written `\u002e` is masked up to its first one, and the
  rest is sent: `https:\/\/api\u002eacme\u002ecom\/v1` sends `acme` and `com`. The same goes for a
  userinfo that escapes a character other than `/`, `:` and `@`. Since v0.4.0. Not affected:
  `\/`, and the `\u` escapes of `/`, `:` and `@`.
- **With `--llm-anonymize`, an AWS secret access key with nothing beside it to identify it is
  sent.** These are credential leaks. The key has no prefix: forty characters of base64, which a bare
  run cannot tell from a hash or an identifier. Since `v0.12.29` it is masked after its name or
  after its access key id. It is sent:
  - alone, or beside anything else (`key …`);
  - after a name outside the list: `secret_key=`, `AWS_SECRET_KEY=`, Terraform's `secret_key`;
  - beside its key id when the field is not exactly forty characters, or when something other than
    a comma, `;`, `:`, `|` or whitespace separates them.

  The session token of temporary credentials (`AWS_SESSION_TOKEN=…`) is sent too. Since v0.4.0.
- **With `--llm-anonymize`, GitLab tokens are masked by an inferred shape.** GitLab documents the
  prefixes, and a minimum of twenty characters for a personal access token, but not the body's
  alphabet or length for the others. A body shorter than twenty characters is sent.
  `_gitlab_session=` is a session cookie, not a token, and is not masked.
- **With `--llm-anonymize`, only Twilio's SIDs are masked.** A Twilio Auth Token and an API key's
  secret have no prefix: thirty-two hex characters, sent as any hash is. A SID with a hex letter
  glued straight after it (`SK…deadline`) is a hex run longer than a SID, and is sent on the raw
  line. Since v0.4.0.
- **With `--llm-anonymize`, three kinds of webhook URL are sent.** Their path goes in the clear:
  - Slack's workflow and trigger webhooks (`hooks.slack.com/workflows/…`, `…/triggers/…`);
  - GovSlack's `hooks.slack-gov.com`;
  - a webhook URL percent-encoded inside another (`redirect=https%3A%2F%2Fhooks…`).

  A webhook path run straight into another URL's scheme (`…/services/T…/B…/<secret>x_https://…`)
  sends its last segment, which carries the scheme the URL-host rule reads. Since v0.4.0.
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
- **`--docker-tail` defaults to 100 lines** of history per container on first attach. An
  event further back than that won't appear until it recurs — use `--docker-tail all` for
  the full backlog. A restarted container is not read from its tail again: logscry resumes
  after the last line it read. One edge: a container that delivered nothing before it
  restarted is re-attached like a first attach, so with `--docker-tail 0` the lines it logs
  between the restart and the re-attach (milliseconds) are lost.
- **A stream of distinct templates grows memory without bound.** logscry keeps every template
  it has seen, with its masked pattern, for the whole run, and never drops one. Measured: about
  3.5 MiB per 10,000 distinct templates of short lines, and about 629 MiB per 10,000 at the
  64 KiB line cap, growing linearly. More lines of templates already seen cost nothing more: a
  stream of one template repeated at the cap levels off at about 160 MiB. An ordinary host stays
  far from this; the real journal captures replayed for the line cap produced at most about
  3,100 templates each, over hours. Filed as #126.
- **A capture recorded without `--all` has lost its long journald messages.** Without
  `--all`, `journalctl -o json` writes any field over 4096 bytes as `null`, and the message is
  not in the file at all. That limit is journalctl(1)'s; systemd's `logs-show.c` applies it to
  the field's name, `=` and value together (`>= 4096`), and on a real boot a 3,604-byte field
  was printed and a 5,008-byte one written as `null`. `--replay` keeps the rest of such an entry, its `journald:<unit>`
  tag, the level from its priority and its own time, and shows
  `logscry: message not recorded (journalctl -o json prints a long field as null without --all)`
  in place of the text. The `logscry:` prefix marks the line as logscry's, not the unit's. All
  such entries share that one template, whatever their unit. Re-record with
  `journalctl -o json --all` to keep the messages. `--journald` itself runs with `--all` and is
  not affected.
- **A journald entry whose JSON line passes 64 KiB is shown as its raw JSON head.** Like any
  line, it is cut at 64 KiB and announced (see [How it works](#how-it-works)). The cut entry is
  no longer JSON, so it is tagged `journald`, gets no level from its priority, and carries the
  time logscry read it; in a replay it is counted untimed. A unit's stdout cannot reach that
  as plain text, since journald splits a stdout line at 48K, but a binary or heavily escaped
  one can: JSON writes a non-UTF-8 byte as up to four characters. A message sent through the
  journal protocol or syslog has no such split. On the one boot measured with `--all`, 4,666
  entries, the longest line was 5,646 bytes.
- **Reasoning models need a higher `--llm-max-tokens`.** A thinking model (e.g. `qwen3`,
  `deepseek-r1`) can spend the whole default budget on its chain of thought and return
  empty content with `finish_reason: length`. Prefer a plain instruct model such as
  `gemma2:2b`, or raise the cap (see [Configuration](#configuration)).

## Security

Report a vulnerability privately, never in a public issue: above all, a value that reaches the
model although `--llm-anonymize` is documented to mask it. [SECURITY.md](SECURITY.md) says how,
what is in scope, and what to leave out of the report.

## License

[Apache-2.0](LICENSE). See also [NOTICE](NOTICE).

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).
