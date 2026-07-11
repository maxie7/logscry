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
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/maxie7/logscry/internal/ingest"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
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

// options is the resolved command line.
type options struct {
	sources []ingest.Source
	// stdinOnly reports that the only source is stdin, which decides whether the
	// TUI can have the terminal's keyboard to itself (see run).
	stdinOnly bool
	plain     bool
}

func run(ctx context.Context, args []string) error {
	opts, err := parseArgs(args)
	if err != nil {
		return err
	}

	mode, ttyIn := tui.Resolve(opts.plain)
	if ttyIn != nil {
		defer func() { _ = ttyIn.Close() }()
	}

	// Reading logs from an interactive terminal means the stdin source and Bubble
	// Tea would both be consuming the same keystrokes. Rather than fight over them,
	// say what is missing. (--plain has no such conflict: it reads typed lines.)
	if mode == tui.ModeTUI && opts.stdinOnly && ttyIn == nil {
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
		if err := ingest.Run(ctx, opts.sources, lines); err != nil && ctx.Err() == nil {
			errs <- fmt.Errorf("ingest: %w", err)
		}
	}()

	if mode == tui.ModePlain {
		return runPlain(ctx, lines, errs)
	}
	return runTUI(ctx, lines, errs, ttyIn)
}

// runTUI renders pipeline snapshots in Bubble Tea. The snapshot channel has
// capacity 1 and the pipeline sends into it without blocking, so the renderer
// always sees the latest state and never slows ingestion down.
func runTUI(ctx context.Context, lines <-chan model.LogLine, errs <-chan error, ttyIn *os.File) error {
	snaps := make(chan pipeline.Snapshot, 1)
	go pipeline.Run(ctx, lines, pipeline.Options{Snapshots: snaps})
	return tui.Run(ctx, snaps, errs, ttyIn)
}

// runPlain is the non-TUI escape hatch: one line per event, straight to unbuffered
// stdout so it stays a real-time tail that pipes and CI can consume.
func runPlain(ctx context.Context, lines <-chan model.LogLine, errs <-chan error) error {
	events := make(chan pipeline.Event, 1024)
	go pipeline.Run(ctx, lines, pipeline.Options{Events: events})

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
		}
	}
}

// levelLabel renders a detected level for the plain-text consumer, using "-" when
// no level was detected so the columns stay aligned.
func levelLabel(level string) string {
	if level == "" {
		return "-"
	}
	return level
}

// stringList is a repeatable string flag (e.g. --docker-label k=v --docker-label x=y).
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parseArgs parses the CLI args, selecting the ingest sources and the render mode.
// Everything after a "--" separator is run as a subprocess (logscry -- ./app).
// Docker selection flags add a Docker source. If neither is selected, logscry reads
// from stdin. Docker and a subprocess can run together (logscry --docker-all --
// ./app).
func parseArgs(args []string) (options, error) {
	// Split at the first "--" before flag parsing: the stdlib flag package's own
	// "--" handling is ambiguous with a bare `logscry ./app`, and we require an
	// explicit "--" to treat trailing args as a subprocess.
	flagArgs, argv := splitArgs(args)

	fs := flag.NewFlagSet("logscry", flag.ContinueOnError)
	var (
		plain       = fs.Bool("plain", false, "plain line output instead of the TUI")
		dockerAll   = fs.Bool("docker-all", false, "follow logs from all Docker containers")
		dockerName  = fs.String("docker-name", "", "follow Docker containers whose name matches this regexp")
		dockerTail  = fs.String("docker-tail", "100", "number of trailing log lines to fetch per container on attach")
		dockerLabel stringList
	)
	fs.Var(&dockerLabel, "docker-label", "follow Docker containers with this k=v label (repeatable, AND-combined)")
	if err := fs.Parse(flagArgs); err != nil {
		return options{}, err
	}

	opts := options{plain: *plain}
	if len(argv) > 0 {
		opts.sources = append(opts.sources, ingest.NewSubprocessSource(argv))
	}
	if *dockerAll || *dockerName != "" || len(dockerLabel) > 0 {
		src := ingest.NewDockerSource(ingest.DockerSelector{
			All:       *dockerAll,
			NameRegex: *dockerName,
			Label:     dockerLabel,
		})
		src.Tail = *dockerTail
		opts.sources = append(opts.sources, src)
	}
	if len(opts.sources) == 0 {
		opts.sources = append(opts.sources, ingest.NewStdinSource())
		opts.stdinOnly = true
	}
	return opts, nil
}

// splitArgs partitions args at the first "--" separator: everything before is
// returned as flag args, everything after as the subprocess argv (nil when there
// is no separator).
func splitArgs(args []string) (flagArgs, argv []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}
