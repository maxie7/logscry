// SPDX-License-Identifier: Apache-2.0

// Command logscry is a real-time, AI-assisted log/event triage CLI/TUI.
//
// M1: ingestion — read from stdin, run a subprocess (logscry -- ./app) and
// capture its stdout+stderr, or follow Docker container logs (--docker-all and
// friends). All sources fan into one channel.
//
// M2: the pipeline goroutine normalizes, templates, and deduplicates each line.
// It emits — it never prints — so this file picks the renderer: the Bubble Tea
// TUI, or plain lines when the TUI cannot or should not run (see tui.Resolve).
//
// M3: the scorer decides which of those events are worth escalating. There is no
// LLM yet, so --explain-dry-run is how the thresholds get calibrated: it surfaces
// what *would* have been escalated. The escalation channel that M4's worker pool
// will consume is left nil for now (see runPlain).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/maxie7/logscry/internal/config"
	"github.com/maxie7/logscry/internal/ingest"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
	"github.com/maxie7/logscry/internal/score"
	"github.com/maxie7/logscry/internal/tui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "logscry:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}
	sources, stdinOnly := sources(cfg)

	mode, ttyIn := tui.Resolve(cfg.Plain)
	if ttyIn != nil {
		defer func() { _ = ttyIn.Close() }()
	}

	// Reading logs from an interactive terminal means the stdin source and Bubble
	// Tea would both be consuming the same keystrokes. Rather than fight over them,
	// say what is missing. (--plain has no such conflict: it reads typed lines.)
	if mode == tui.ModeTUI && stdinOnly && ttyIn == nil {
		return errors.New("no log source: pipe logs in, run 'logscry -- ./app', or use --docker-all")
	}

	lines := make(chan model.LogLine, 1024)
	errs := make(chan error, 1)
	go func() {
		defer close(lines)
		defer close(errs)
		// Source errors end the run. A cancelled context (Ctrl-C) is an expected
		// stop, not an error to report. In TUI mode the error goes to the model —
		// printing it would corrupt the alternate screen.
		if err := ingest.Run(ctx, sources, lines); err != nil && ctx.Err() == nil {
			errs <- fmt.Errorf("ingest: %w", err)
		}
	}()

	// The scorer decides; nothing consumes its escalations yet. Handing it a real
	// channel with no reader on the other end would fill it after a handful of
	// escalations and record every one after that as a drop, corrupting the very
	// counts --explain-dry-run exists to calibrate. M4 gives it the worker pool;
	// until then the decision travels on the Event, which is what the renderers show.
	sc := score.New(cfg.Score, nil)

	if mode == tui.ModePlain {
		return runPlain(ctx, lines, errs, sc, cfg.ExplainDryRun)
	}
	return runTUI(ctx, lines, errs, ttyIn, sc, cfg.ExplainDryRun)
}

// sources builds the ingest sources from the resolved config, and reports whether
// stdin is the only one — which decides whether the TUI can have the terminal's
// keyboard to itself (see run).
func sources(cfg config.Config) ([]ingest.Source, bool) {
	var out []ingest.Source
	if len(cfg.Argv) > 0 {
		out = append(out, ingest.NewSubprocessSource(cfg.Argv))
	}
	if cfg.Docker.All || cfg.Docker.NameRegex != "" || len(cfg.Docker.Labels) > 0 {
		src := ingest.NewDockerSource(ingest.DockerSelector{
			All:       cfg.Docker.All,
			NameRegex: cfg.Docker.NameRegex,
			Label:     cfg.Docker.Labels,
		})
		src.Tail = cfg.Docker.Tail
		out = append(out, src)
	}
	if len(out) == 0 {
		return []ingest.Source{ingest.NewStdinSource()}, true
	}
	return out, false
}

// runTUI renders pipeline snapshots in Bubble Tea. The snapshot channel has
// capacity 1 and the pipeline sends into it without blocking, so the renderer
// always sees the latest state and never slows ingestion down.
func runTUI(ctx context.Context, lines <-chan model.LogLine, errs <-chan error, ttyIn *os.File, sc *score.Scorer, dryRun bool) error {
	snaps := make(chan pipeline.Snapshot, 1)
	go pipeline.Run(ctx, lines, pipeline.Options{Snapshots: snaps, Scorer: sc})
	return tui.Run(ctx, snaps, errs, ttyIn, tui.Options{ExplainDryRun: dryRun})
}

// runPlain is the non-TUI escape hatch: one line per event, straight to unbuffered
// stdout so it stays a real-time tail that pipes and CI can consume.
func runPlain(ctx context.Context, lines <-chan model.LogLine, errs <-chan error, sc *score.Scorer, dryRun bool) error {
	events := make(chan pipeline.Event, 1024)
	go pipeline.Run(ctx, lines, pipeline.Options{Events: events, Scorer: sc})

	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-errs:
			if !ok {
				errs = nil // closed: a nil channel never fires again
				continue
			}
			fmt.Fprintln(os.Stderr, "logscry:", err)
		case ev, ok := <-events:
			if !ok {
				// Every source finished (e.g. stdin EOF) and the pipeline closed the
				// events channel: shut down cleanly.
				return nil
			}
			// The running count makes dedup visible: repeated templates increment
			// in place.
			if _, err := fmt.Fprintf(os.Stdout, "[%s %s x%d] %s\n",
				ev.Line.Source, levelLabel(ev.Line.Level), ev.Count, ev.Pattern); err != nil {
				return err
			}
			if dryRun && ev.Escalate {
				if _, err := fmt.Fprintln(os.Stdout, wouldEscalate(ev)); err != nil {
					return err
				}
			}
		}
	}
}

// wouldEscalate renders an escalation that has nowhere to go yet. Without an LLM the
// scoring engine is otherwise invisible, and an invisible scoring engine cannot be
// tuned — so this line is the whole point of --explain-dry-run.
func wouldEscalate(ev pipeline.Event) string {
	return fmt.Sprintf("WOULD ESCALATE: %s | reasons: %s | score: %.2f",
		ev.Pattern, strings.Join(ev.Reasons, ", "), ev.Score)
}

// levelLabel renders a detected level for the plain-text consumer, using "-" when
// no level was detected so the columns stay aligned.
func levelLabel(level string) string {
	if level == "" {
		return "-"
	}
	return level
}
