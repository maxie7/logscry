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
// M3: the scorer decides which of those events are worth escalating, gated by a rate
// limiter and an explanation cache. --explain-dry-run surfaces what *would* have been
// escalated, which is how the thresholds get calibrated.
//
// M4: the escalation channel finally has a reader. A worker pool calls an
// OpenAI-compatible model asynchronously and sends the explanations back to the
// pipeline goroutine, which owns the template state and attaches them there. A slow or
// dead model can therefore never stall the tail — and in dry-run no LLM stage is built
// at all, so no request can be made (see startLLM).
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
	"github.com/maxie7/logscry/internal/llm"
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

	// stdin as the only source, and stdin is the terminal the user is sitting at: no
	// logs are coming, and none ever will. Say what is missing rather than drawing an
	// empty screen forever. (--plain has no such problem: it reads typed lines.)
	if mode == tui.ModeTUI && stdinOnly && tui.StdinIsTerminal() {
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

	escalations, explanations := startLLM(ctx, cfg)
	sc := score.New(cfg.Score, escalations)

	if mode == tui.ModePlain {
		return runPlain(ctx, lines, errs, sc, escalations, explanations, cfg.ExplainDryRun)
	}
	return runTUI(ctx, lines, errs, ttyIn, sc, escalations, explanations, cfg.ExplainDryRun)
}

// startLLM builds the LLM stage: the escalation channel the scorer emits on, and the
// explanation channel the worker pool answers on.
//
// In --explain-dry-run it returns nil channels and starts nothing. That is the point:
// the mode's guarantee is not "we skip the call", it is that there is no backend and no
// pool in the process at all, so no request CAN be made. A nil escalation channel also
// keeps the drop counter honest — a real channel with no reader would fill up and record
// every later escalation as a drop, corrupting the very numbers dry-run exists to
// calibrate.
func startLLM(ctx context.Context, cfg config.Config) (chan score.EscalationRequest, chan model.Explanation) {
	if cfg.ExplainDryRun {
		return nil, nil
	}

	escalations := make(chan score.EscalationRequest, cfg.LLM.Queue)
	// Room for every escalation that could still be in flight, so a worker finishing
	// after its consumer has gone can always put its answer down and exit rather than
	// blocking on the way out.
	explanations := make(chan model.Explanation, cfg.LLM.Queue+cfg.LLM.Workers)

	go llm.Run(ctx, llm.NewOpenAICompatible(cfg.LLM), cfg.LLM, escalations, explanations)
	return escalations, explanations
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
//
// The explanations go to the pipeline goroutine, not to the TUI: it owns the template
// state, so it is the one that attaches an answer to the template that asked for it.
// The TUI then sees it appear in the next snapshot, and never touches pipeline state.
func runTUI(ctx context.Context, lines <-chan model.LogLine, errs <-chan error, ttyIn *os.File,
	sc *score.Scorer, escalations chan score.EscalationRequest, explanations chan model.Explanation, dryRun bool,
) error {
	snaps := make(chan pipeline.Snapshot, 1)
	go pipeline.Run(ctx, lines, pipeline.Options{
		Snapshots:    snaps,
		Scorer:       sc,
		Escalations:  escalations,
		Explanations: explanations,
	})
	return tui.Run(ctx, snaps, errs, ttyIn, tui.Options{
		ExplainDryRun: dryRun,
		Explain:       escalations != nil,
	})
}

// runPlain is the non-TUI escape hatch: one line per event, straight to unbuffered
// stdout so it stays a real-time tail that pipes and CI can consume.
//
// It reads the explanations itself rather than routing them through the pipeline: plain
// mode has no card to update in place, so an explanation is simply another line to print
// when it arrives, a few seconds after the escalation it belongs to.
func runPlain(ctx context.Context, lines <-chan model.LogLine, errs <-chan error,
	sc *score.Scorer, escalations chan score.EscalationRequest, explanations chan model.Explanation, dryRun bool,
) error {
	events := make(chan pipeline.Event, 1024)
	go pipeline.Run(ctx, lines, pipeline.Options{Events: events, Scorer: sc, Escalations: escalations})

	for events != nil || explanations != nil {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-errs:
			if !ok {
				errs = nil // closed: a nil channel never fires again
				continue
			}
			fmt.Fprintln(os.Stderr, "logscry:", err)
		case ex, ok := <-explanations:
			if !ok {
				explanations = nil // the pool has drained: no answers are outstanding
				continue
			}
			if _, err := fmt.Fprint(os.Stdout, explained(ex)); err != nil {
				return err
			}
		case ev, ok := <-events:
			if !ok {
				// Every source finished (e.g. stdin EOF) and the pipeline closed the
				// events channel. Keep going until the last explanation lands.
				events = nil
				continue
			}
			// The running count makes dedup visible: repeated templates increment
			// in place.
			if _, err := fmt.Fprintf(os.Stdout, "[%s %s x%d] %s\n",
				ev.Line.Source, levelLabel(ev.Line.Level), ev.Count, ev.Pattern); err != nil {
				return err
			}
			if ev.Escalate {
				if _, err := fmt.Fprintln(os.Stdout, escalated(ev, dryRun)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// escalated renders the escalation itself, the moment it fires.
//
// In dry-run this line is the whole point of the mode: without an LLM the scoring engine
// is invisible, and an invisible scoring engine cannot be tuned. With an LLM attached it
// is the "we are asking about this" marker that the explanation later answers — unless
// the queue was full, in which case nothing is coming and it says so.
func escalated(ev pipeline.Event, dryRun bool) string {
	verb := "ESCALATED"
	switch {
	case dryRun:
		verb = "WOULD ESCALATE"
	case !ev.Queued:
		verb = "ESCALATION DROPPED (queue full)"
	}
	return fmt.Sprintf("%s: %s | reasons: %s | score: %.2f",
		verb, ev.Pattern, strings.Join(ev.Reasons, ", "), ev.Score)
}

// explained renders an answer that has come back from the model, seconds after the
// escalation it belongs to scrolled past.
func explained(ex model.Explanation) string {
	if ex.State == model.ExplainFailed {
		return fmt.Sprintf("EXPLANATION UNAVAILABLE: %s | %s\n", ex.Pattern, ex.Err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "EXPLAINED: %s\n", ex.Pattern)
	fmt.Fprintf(&b, "  what:  %s\n", ex.Summary)
	if ex.LikelyCause != "" {
		fmt.Fprintf(&b, "  cause: %s\n", ex.LikelyCause)
	}
	if ex.Suggestion != "" {
		fmt.Fprintf(&b, "  check: %s\n", ex.Suggestion)
	}
	return b.String()
}

// levelLabel renders a detected level for the plain-text consumer, using "-" when
// no level was detected so the columns stay aligned.
func levelLabel(level string) string {
	if level == "" {
		return "-"
	}
	return level
}
