# Recording the demo GIF

This is the one manual step in the demo pipeline. Everything around it — the compose
stream, the README, the recording script — is already in the repo. Follow this to
produce `docs/demo.gif`, which the README embeds.

The recommended tool is **[vhs](https://github.com/charmbracelet/vhs)**: it scripts a
terminal recording from a plain-text `.tape` file and renders a GIF in its own headless
terminal, so the result is reproducible and pixel-consistent. A ready-to-run tape lives
at [`docs/demo.tape`](docs/demo.tape).

## Prerequisites

- **vhs** — `brew install vhs`, or see the vhs README for other platforms. It also
  needs `ttyd` and `ffmpeg`, which its install instructions cover.
- **Docker** running (the demo compose needs it).
- **Ollama** running locally with the demo model pulled:
  ```sh
  ollama pull gemma2:2b
  ```

### Which model

Use **`gemma2:2b`**. It is small, fast, and gives clear, concise explanations that fit
the card's three fields (summary / cause / suggestion).

**Do not use a reasoning model** (e.g. `qwen3`, `deepseek-r1`). They spend the token
budget on an internal chain of thought and then return empty content
(`finish_reason: length`), so the card lands as "explanation unavailable". If you want a
different model, pick a plain instruct model and, if its answers get cut off, raise
`--llm-max-tokens`.

## The easy path: run the tape

From the **repo root**:

```sh
vhs docs/demo.tape
```

That builds logscry, starts the demo compose in the background, records logscry catching
the worker's one fault while the `api` traffic stays quiet, expands the card, quits, and
tears the compose down — writing `docs/demo.gif`.

The tape is tuned for `FontSize 15` at `1200×720` (~100 readable columns). If your
machine explains slower or faster than `gemma2:2b` on a typical laptop, adjust the two
`Sleep` lines around "the fault escalates" so the card is fully rendered before the
`Tab`/`Enter` expand step. The fault timing itself is set by `FAULT_AFTER` in
`examples/demo/docker-compose.yml`.

> vhs renders its own terminal, so the "use a real terminal" caveat below does **not**
> apply to the vhs path — that is the main reason to prefer it.

## Manual path (asciinema + agg)

If you'd rather record by hand — for example to narrate the timing yourself — use
[asciinema](https://asciinema.org) to capture and [agg](https://github.com/asciinema/agg)
to convert to GIF.

**Record in a real terminal (iTerm2, Alacritty, kitty, GNOME Terminal, …), not an IDE's
embedded terminal.** IDE terminals report odd sizes and swallow escape sequences, and
the resulting GIF is usually a mess.

Terminal setup:

- Size the window to **~100–120 columns** (the two-pane layout needs ≥100 columns; below
  that logscry stacks the panes).
- Font size **~15–16px** so the text is legible in a GIF.

Then, in two terminals:

```sh
# Terminal 1 — start the demo stream
docker compose -f examples/demo/docker-compose.yml up

# Terminal 2 — record logscry
asciinema rec demo.cast \
  --command "./bin/logscry --docker-name '^(api|worker)$' --llm-model gemma2:2b"
```

Timing while recording:

1. Start logscry (the `asciinema rec` command above launches it).
2. Watch the routine traffic scroll — nothing escalates.
3. ~18s after the compose came up, the worker's `FATAL` lands and, a few seconds later,
   the explained card appears on the right.
4. Press **`Tab`** to focus the cards pane, then **`Enter`** to expand the card (cause,
   suggestion, and context lines).
5. Let it sit for a few seconds, then press **`q`** to quit — this ends the recording.
6. `docker compose -f examples/demo/docker-compose.yml down`.

Convert to GIF:

```sh
agg demo.cast docs/demo.gif
```

## Result

Either path produces `docs/demo.gif`. Commit it — the README already points at it:

```md
![logscry demo](docs/demo.gif)
```
