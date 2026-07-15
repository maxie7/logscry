# logscry demo

A tiny, self-contained log stream that shows the whole point of logscry in under 30
seconds: **silent on noise, speaks on signal.**

Two Alpine containers emit a few readable lines per second:

- **`api`** — steady, healthy request traffic (routine `INFO` lines, the occasional
  `ERROR` on stderr). All of it stays *below* logscry's escalation threshold.
- **`worker`** — the same routine job traffic, until about 18 seconds in it hits one
  genuine fault (Postgres unreachable) and then keeps running.

logscry stays quiet on every routine line and escalates **only** the fault — sending
that one event to the LLM and rendering an explained card, while the healthy `api`
traffic scrolls past unremarked.

## Run it

Two terminals. First, start the stream:

```sh
docker compose -f examples/demo/docker-compose.yml up
```

Then, from the repo root (after `make build`), point logscry at it:

```sh
./bin/logscry --docker-all --llm-model gemma2:2b
```

Within ~30 seconds you get the money shot: healthy traffic on the left, one explained
**"connection refused: postgres:5432"** card on the right.

`--docker-all` auto-attaches to both containers (and to any new one that starts), so the
stream shows both `docker:api` and `docker:worker` sources interleaved.

### With a local LLM (Ollama)

The command above expects a local [Ollama](https://ollama.com) with `gemma2:2b` pulled:

```sh
ollama pull gemma2:2b
```

`gemma2:2b` gives clear, concise explanations and is small enough to run anywhere. Avoid
reasoning models (e.g. `qwen3`, `deepseek-r1`): they spend the token budget "thinking"
and return empty explanations.

### Without an LLM

You don't need a model to see the scoring engine work. `--explain-dry-run` shows what
*would* be escalated and makes no LLM calls at all:

```sh
./bin/logscry --docker-all --explain-dry-run
```

The fault line prints `WOULD ESCALATE`; the routine traffic produces nothing. That
contrast is the tool.

## Notes

- The containers are named `api` and `worker`. If you already run containers by those
  names, stop them first (or edit `container_name` in `docker-compose.yml`).
- Tear down when you're done: `docker compose -f examples/demo/docker-compose.yml down`.
- Tune the timing with the env vars in `docker-compose.yml`: `INTERVAL` (seconds between
  lines) and `FAULT_AFTER` (when the worker breaks).
