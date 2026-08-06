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
//
// M8: --export appends one JSON object per flagged anomaly to a file, so a run can be
// consumed by another program instead of only read. The file is owned by its own goroutine
// (see internal/export); this file only opens it and taps the two places a terminal
// explanation is already consumed for display.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/maxie7/logscry/internal/config"
	"github.com/maxie7/logscry/internal/export"
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
	// --version reports and exits before any source, backend, or TUI is constructed.
	if cfg.Version {
		fmt.Println("logscry", versionString())
		return nil
	}
	// Opened before the terminal is taken over, so a bad --export path is a plain startup
	// error rather than something discovered from inside the alternate screen. The deferred
	// close is registered first and therefore runs LAST, after the TUI has restored the
	// terminal — which is what makes its warnings readable.
	exp, err := openExport(cfg)
	if err != nil {
		return err
	}
	defer closeExport(exp)

	sources, stdinOnly := sources(cfg)

	term := tui.Resolve(cfg.Plain)
	defer func() { _ = term.Close() }()

	// stdin as the only source, and stdin is the terminal the user is sitting at: no
	// logs are coming, and none ever will. Say what is missing rather than drawing an
	// empty screen forever. (--plain has no such problem: it reads typed lines.)
	if term.Mode == tui.ModeTUI && stdinOnly && tui.StdinIsTerminal() {
		return errors.New("no log source: pipe logs in, run 'logscry -- ./app', or use --docker-all")
	}

	lines := make(chan model.LogLine, 1024)
	// Capacity 2 so the journald notice below and a later source error can both be put
	// down without either sender blocking.
	errs := make(chan error, 2)
	if notice := journaldNotice(cfg); notice != nil {
		errs <- notice
	}
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

	// Fold multi-line events (stack traces, goroutine dumps) into one logical line
	// before templating, so a traceback becomes one template instead of one per frame.
	// A standalone goroutine owns the coalescer's buffer state, so the concurrency model
	// is unchanged. Timeout 0 disables it: lines pass straight through.
	grouped := lines
	if cfg.Group.Timeout > 0 {
		g := make(chan model.LogLine, 1024)
		go pipeline.Coalesce(ctx, lines, g, cfg.Group.Timeout)
		grouped = g
	}

	escalations, explanations := startLLM(ctx, cfg)
	sc := score.New(cfg.Score, escalations)

	if term.Mode == tui.ModePlain {
		// stderr survives in plain mode; in the TUI it would be wiped by the alternate
		// screen, so that path surfaces the same notice in the status bar (see runTUI).
		if host := remoteWarnHost(cfg); host != "" {
			fmt.Fprintf(os.Stderr, "logscry: raw log lines will be sent to %s; "+
				"pass --llm-anonymize to mask IPs, emails, tokens, and hostnames first\n", host)
		}
		return runPlain(ctx, grouped, errs, sc, escalations, explanations, exp, cfg.ExplainDryRun)
	}
	return runTUI(ctx, grouped, errs, term, sc, escalations, explanations, exp, cfg)
}

// openExport opens the JSONL export file, or returns nil when --export was not given. A nil
// *export.Writer is inert, so "disabled" needs no further branching anywhere.
func openExport(cfg config.Config) (*export.Writer, error) {
	if cfg.Export == "" {
		return nil, nil
	}
	w, err := export.Open(cfg.Export)
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	return w, nil
}

// closeExport drains and closes the export file, reporting anything it has to admit to.
//
// Both messages go to stderr rather than being swallowed: a tool whose whole job is to not
// miss anomalies must say so when it has missed one on the way to disk, and a file the
// consumer is about to parse deserves to be described honestly.
func closeExport(w *export.Writer) {
	if err := w.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "logscry: export:", err)
	}
	if n := w.Dropped(); n > 0 {
		fmt.Fprintf(os.Stderr, "logscry: export: %d record(s) dropped — the export file could not keep up\n", n)
	}
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

	// Default off ⇒ the exact unwrapped backend as before. With --llm-anonymize, the
	// decorator masks the outgoing payload and restores the answer at the LLM boundary,
	// leaving the pipeline, template state, and TUI on the real values (see internal/llm).
	var backend llm.Backend = llm.NewOpenAICompatible(cfg.LLM)
	if cfg.LLM.Anonymize {
		backend = llm.NewAnonymizing(backend, cfg.LLM.AnonymizeSuffixes...)
	}
	go llm.Run(ctx, backend, cfg.LLM, escalations, explanations)
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
	if journaldEnabled(cfg) {
		out = append(out, ingest.NewJournaldSource(cfg.Journald.Units, cfg.Journald.Priority))
	}
	if len(out) == 0 {
		return []ingest.Source{ingest.NewStdinSource()}, true
	}
	return out, false
}

// journaldEnabled reports whether to follow the systemd journal. Naming a unit is enough
// on its own, the way --docker-name and --docker-label imply Docker without --docker-all;
// --journald-priority alone is not, the way --docker-tail alone is not — it tunes a
// source rather than asking for one.
func journaldEnabled(cfg config.Config) bool {
	return cfg.Journald.Enabled || len(cfg.Journald.Units) > 0
}

// journaldNotice reports a journal the user can only partially see, or nil.
//
// This is the failure --journald is most likely to hit and the only one that arrives
// without an error: journalctl runs fine for a user outside the systemd-journal group,
// it just follows their own session instead of the system, so the tool looks broken
// while working exactly as instructed. It is not an error and must not end the run — it
// goes down the background-notice channel, which the TUI shows in its status line and
// --plain prints to stderr.
func journaldNotice(cfg config.Config) error {
	if !journaldEnabled(cfg) {
		return nil
	}
	if w := ingest.SystemJournalWarning(); w != "" {
		return errors.New(w)
	}
	return nil
}

// runTUI renders pipeline snapshots in Bubble Tea. The snapshot channel has
// capacity 1 and the pipeline sends into it without blocking, so the renderer
// always sees the latest state and never slows ingestion down.
//
// The explanations go to the pipeline goroutine, not to the TUI: it owns the template
// state, so it is the one that attaches an answer to the template that asked for it.
// The TUI then sees it appear in the next snapshot, and never touches pipeline state.
func runTUI(ctx context.Context, lines <-chan model.LogLine, errs <-chan error, term tui.Terminal,
	sc *score.Scorer, escalations chan score.EscalationRequest, explanations chan model.Explanation,
	exp *export.Writer, cfg config.Config,
) error {
	snaps := make(chan pipeline.Snapshot, 1)
	go pipeline.Run(ctx, lines, pipeline.Options{
		Snapshots:    snaps,
		Scorer:       sc,
		Escalations:  escalations,
		Explanations: explanations,
		Export:       exp,
	})
	return term.Run(ctx, snaps, errs, tui.Options{
		ExplainDryRun:  cfg.ExplainDryRun,
		Explain:        escalations != nil,
		DockerTail:     cfg.Docker.Tail,
		RemoteWarnHost: remoteWarnHost(cfg),
	})
}

// remoteWarnHost returns the host logscry is about to stream raw logs to, or "" when there
// is nothing to warn about: masking is on, the endpoint is local, or dry-run builds no
// backend and so sends nothing at all.
func remoteWarnHost(cfg config.Config) string {
	if cfg.ExplainDryRun || cfg.LLM.Anonymize {
		return ""
	}
	host, remote := remoteHost(cfg.LLM.BaseURL)
	if !remote {
		return ""
	}
	return host
}

// remoteHost reports a base URL's host and whether it is off-box. A URL we cannot parse is
// treated as local: staying quiet beats warning wrongly about a host we could not read.
func remoteHost(baseURL string) (string, bool) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return "", false
	}
	host := u.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0", "":
		return host, false
	}
	if strings.HasSuffix(host, ".localhost") {
		return host, false
	}
	return host, true
}

// runPlain is the non-TUI escape hatch: one line per event, straight to unbuffered
// stdout so it stays a real-time tail that pipes and CI can consume.
//
// It reads the explanations itself rather than routing them through the pipeline: plain
// mode has no card to update in place, so an explanation is simply another line to print
// when it arrives, a few seconds after the escalation it belongs to.
func runPlain(ctx context.Context, lines <-chan model.LogLine, errs <-chan error,
	sc *score.Scorer, escalations chan score.EscalationRequest, explanations chan model.Explanation,
	exp *export.Writer, dryRun bool,
) error {
	events := make(chan pipeline.Event, 1024)
	go pipeline.Run(ctx, lines, pipeline.Options{
		Events: events, Scorer: sc, Escalations: escalations, Export: exp,
	})

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
			// Progressive updates are a TUI affordance: plain mode has no card to rewrite,
			// so a streamed answer would print the same explanation three times, each a
			// little more complete. Only the terminal result is a line worth emitting.
			if ex.State == model.ExplainPending {
				continue
			}
			// The export file's terminal-state hook for plain mode, deliberately on the far
			// side of the skip above: whatever reaches here is the answer, and the file gets
			// exactly one line per anomaly for the same reason stdout gets one block.
			exp.Resolve(ex)
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
	if ex.Truncated {
		// The stream died before the model finished. The fields below are real but the
		// answer is short, and saying so is the difference between a partial verdict and
		// one someone acts on believing it was complete.
		fmt.Fprintf(&b, "EXPLAINED (INCOMPLETE): %s\n", ex.Pattern)
	} else {
		fmt.Fprintf(&b, "EXPLAINED: %s\n", ex.Pattern)
	}
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
