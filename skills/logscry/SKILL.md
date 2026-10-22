---
name: logscry
description: Triage logs with logscry — find the few lines worth attention in a command's output, Docker containers, the systemd journal, a log file or a journald capture, and optionally get an LLM explanation of each. Use when asked what went wrong in some logs, to watch a service for anomalies, or to calibrate logscry's thresholds.
---

# Triage logs with logscry

logscry reads a log stream, collapses it into templates (`user 4821 failed` and
`user 9933 failed` are one template, `user <NUM> failed`), scores each line for novelty,
burst and severity, and flags only what crosses a threshold. It is built to stay
**silent on routine noise**: zero flags is a normal, meaningful result, not a failure.

Requires the `logscry` binary on `PATH` (`logscry --version`). If it is missing, ask the
user before installing: a release archive from
https://github.com/maxie7/logscry/releases/latest, or
`go install github.com/maxie7/logscry/cmd/logscry@latest`.

## Rules

1. **Always run with `--plain --export <file>.jsonl` and read the file, not stdout.**
   The JSONL export is the stable, documented contract; the stdout lines are for humans.
   Without `--plain` logscry falls back to plain output by itself when stdout is not a
   terminal, but say it explicitly. Use a fresh file per run: the export is appended to,
   never truncated.
2. **Start with `--explain-dry-run`.** It needs no model, builds no LLM client at all, and
   sends nothing anywhere. It tells you *what* is anomalous; add a model only when the user
   wants explanations.
3. **Bound every run that follows a live source.** `--docker-*` and `--journald` never end
   on their own. Wrap them in `timeout` (e.g. `timeout 120 logscry …`). logscry exits
   cleanly on SIGTERM and finishes writing the export; `timeout` itself then exits `124`,
   which is expected, not an error.
4. **Never put an API key on the command line or in a config file.** The key is read from
   `LOGSCRY_API_KEY` only.
5. **Never send logs to a remote model without asking the user first**, and then use
   `--llm-anonymize` (see below).

## Pick the source

| Logs are… | Command | Ends when |
|---|---|---|
| produced by a command | `logscry --plain --explain-dry-run --export a.jsonl -- ./app --flag` | the command exits |
| in Docker | `timeout 120 logscry --plain --explain-dry-run --export a.jsonl --docker-all` | timeout |
| specific containers | `--docker-name '^api'` or `--docker-label k=v` instead of `--docker-all` | timeout |
| in the systemd journal (Linux) | `timeout 120 logscry --plain --explain-dry-run --export a.jsonl --journald` | timeout |
| specific units | `--journald-unit nginx --journald-unit sshd` (add `--journald-priority 4` for warning and above) | timeout |
| a journald capture | `logscry --replay capture.jsonl --explain-dry-run --plain --export a.jsonl` | end of file |
| a plain-text log file | `logscry --plain --explain-dry-run --warmup 0 --warmup-lines 0 --export a.jsonl < app.log` | end of file |

Notes:

- `--docker-tail` (default `100`) is all the history a container gives on attach; use
  `--docker-tail all` to see older lines.
- With `--journald`, a stderr notice that the system journal is unreadable means the user
  is not in the `systemd-journal` (or `adm`) group and only their own session is visible.
  Report it rather than concluding the host is quiet.
- `--replay` reads only `journalctl -o json` captures (record with
  `journalctl -o json --all …`), plays them on their own timestamps, and requires
  `--explain-dry-run`. It is the right way to analyse a recorded journal: the scorer sees
  the real timing.

### A static text file is the weakest case — say so

Piped through stdin, every line of a file arrives within about a second. So:

- **burst** cannot fire: it needs a template with at least a minute of history;
- **novelty** is muted during warmup (the first 30 s and 100 lines) unless you pass
  `--warmup 0 --warmup-lines 0`, as in the table above;
- what remains is **severity**: a FATAL/PANIC line flags on its own, a *new* ERROR template
  flags (novelty 0.45 + error 0.6 ≥ 1.0), a known ERROR or a WARN does not.

That is useful for "what broke in this log", not for "what is unusual in this log". If the
file is a journald export, use `--replay` instead.

### Level detection

A level is recognised from `[ERROR]`, `ERROR:`, `level=error` at the start of the line,
from a `level`/`severity` field in a JSON line, or from journald `PRIORITY`. A level after
a timestamp or other prefix (`2026-10-22T10:00:00Z ERROR …`), or a bare word without a
colon (`FATAL panic …`), is **not** detected, so those lines get no severity weight. If the
logs look like that and nothing flags, mention it.

## Read the results

Each line of the export is one flagged anomaly. Every key is always present.

```sh
jq -c '{kind, level, source, count_at_flag, score, reasons, pattern}' a.jsonl
jq -r 'select(.kind=="anomaly" and .explanation.state=="explained")
       | "\(.pattern)\n  what:  \(.explanation.summary)\n  cause: \(.explanation.likely_cause)\n  check: \(.explanation.suggestion)"' a.jsonl
jq -s 'map(select(.kind!="export_incomplete")) | sort_by(-.score) | .[:10]' a.jsonl
```

| Field | Meaning |
|---|---|
| `kind` | `anomaly` (a model was asked), `would_escalate` (dry run), `export_incomplete` (see below) |
| `pattern` | the template, with values masked: `connection refused to <IP>:<NUM>` |
| `level`, `source` | e.g. `FATAL`, `docker:api`, `proc:myapp`, `journald:nginx`, `stdin` |
| `count_at_flag` | occurrences **when it was flagged**, not a final total |
| `first_seen`, `last_seen_at_flag` | RFC 3339, UTC |
| `score`, `reasons` | why it fired: `["novel template (first seen)", "level ERROR"]`, a burst rate, … |
| `explanation.state` | `explained`, `unavailable` (see `.error`), `not_requested` (dry run) |
| `explanation.summary` / `likely_cause` / `suggestion` | the model's answer |
| `explanation.truncated` | the answer was cut short; treat it as partial |

- An empty file after a dry run means nothing crossed the threshold. Report that plainly.
- A template is flagged once and then only counted. It is flagged again (a second record)
  only after `--cache-ttl` (default 1 h).
- A last line `{"kind":"export_incomplete","records_lost":N,…}` and exit status `1` mean the
  disk could not keep up and N anomalies are missing. Say so.
- The export holds the masked `pattern`, never a raw log line, but values in the
  explanation text are real.

Exit status: `0` for a finished run (including Ctrl-C/SIGTERM, and a monitored command that
itself failed — that failure is printed on stderr); `1` for a bad flag, config or source, or
lost export records.

## Explanations from a model

Drop `--explain-dry-run` and logscry asks a model about each flagged anomaly, at most
`--rate-limit` (default 10) calls a minute, once per template. Answers arrive seconds after
the flag; on a finite source logscry waits for them before exiting.

**Local model (default, nothing leaves the machine):** an OpenAI-compatible endpoint at
`http://localhost:11434/v1` (Ollama), model `gemma2:2b`. Check it is up with
`curl -s localhost:11434/api/tags`. Pick another with `--llm-model`.

**Remote model:** only with the user's consent.

```sh
export LOGSCRY_API_KEY=...        # never a flag
logscry --plain --export a.jsonl --llm-anonymize \
  --llm-url https://api.openai.com/v1 --llm-model gpt-4o-mini -- ./app
```

- `--llm-anonymize` masks IPs, emails, hostnames in URLs and on private suffixes
  (`.internal`, `.local`, `.corp`, …; add more with `--llm-anonymize-suffix`), UUIDs,
  known-shape secrets and URL credentials, replacing each with a typed placeholder
  (`<IP_1>`, `<HOST_2>`) and restoring the answer locally. It is a best-effort masker, not
  a guarantee: read "Known limitations" in the README before promising the user anything.
  If masking fails, that escalation is skipped rather than sent in the clear.
- Without it, logscry prints `raw log lines will be sent to <host>` on stderr. Treat that
  line as a stop sign.
- A reasoning model can spend the whole `--llm-max-tokens` budget (default 300) thinking
  and return nothing; raise the cap or use a non-reasoning model.
- `explanation.state == "unavailable"` with an `.error` such as `connection refused` means
  the model was unreachable; the anomaly itself is still valid.

## Tuning, only if the user asks

Defaults are calibrated to stay quiet. To see more, lower `--threshold` (default `1.0`) or
`--burst-multiplier` (default `5`). For calibration, run the same `--replay` capture twice
with different settings and `diff` the two exports — replay exports are byte-for-byte
deterministic. `logscry --help` lists every flag; a `logscry.yaml` passed with `--config`
holds the same settings.

## Reporting back

Lead with the count ("3 anomalies in 12,400 lines across 4 containers", or "nothing
flagged"). Then each anomaly: pattern, level, source, how often, why it fired, and the
explanation if there is one. Quote patterns, not raw lines. Do not invent causes the
export does not support.
