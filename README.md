# logscry

**Silent on noise, speaks on signal.**

`logscry` is a single-binary, real-time, AI-assisted log/event triage CLI/TUI. It
watches log/event streams **as they happen** and uses an LLM to **explain only the
events worth explaining** — surfacing genuine anomalies live, while staying quiet on
routine noise.

It is *not* an "LLM observability" product (those monitor AI applications — prompts,
tokens, output quality). It is the inverse: an LLM used as a tool to make *any*
system's logs understandable.

## Why it's different

- **Real-time / streaming-native** — a live tail that flags anomalies as they appear,
  not a scan-on-demand report.
- **Source-agnostic** — stdout/files and Docker containers in v1; NATS/Kafka/journald
  later. Not tied to Kubernetes.
- **Dev-time, local, single binary** — point it at your running local stack while
  developing. No cluster, no SaaS; data can stay local (runs offline against Ollama).

The interesting engineering is the pipeline: you cannot call an LLM on every log line
in real time (cost, latency, noise), so `logscry` does cheap local pre-processing
(normalize → template → dedup → novelty/burst/severity scoring) and escalates **only**
novel/anomalous events to the LLM, on a bounded token budget.

## Status

Early development. Current milestone: **M0 — compiling skeleton**. The end-to-end path
today is minimal (stdin → pass-through → stdout); ingestion, templating, scoring, the
LLM backend, and the TUI land in M1–M5. See `logscry_RDI_v1.md` for the full spec and
`BACKLOG.md` for the milestone plan.

## Usage

```sh
# build
make build

# M0: pass-through tail
echo "hello" | ./bin/logscry
```

_Full usage (subprocess, Docker, TUI, config) coming as milestones land._

## Demo

_Demo (coming soon)._

## License

[Apache-2.0](LICENSE). See also [NOTICE](NOTICE).
