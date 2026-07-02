// SPDX-License-Identifier: Apache-2.0

// Command logscry is a real-time, AI-assisted log/event triage CLI/TUI.
//
// M1: ingestion — read from stdin, or run a subprocess (logscry -- ./app) and
// capture its stdout+stderr. All sources fan into one channel; a temporary
// consumer prints each line prefixed by its source and stream. The pipeline,
// scoring, and TUI arrive in later milestones.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/maxie7/logscry/internal/ingest"
	"github.com/maxie7/logscry/internal/model"
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
	lines := make(chan model.LogLine, 1024)

	sources := buildSources(args)
	go func() {
		defer close(lines)
		// Source errors end the run; surface them on stderr but don't crash. A
		// cancelled context (Ctrl-C) is an expected stop, not an error to report.
		if err := ingest.Run(ctx, sources, lines); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "logscry: ingest:", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				// Every source has finished (e.g. stdin EOF) and the fan-in
				// closed the channel: shut down cleanly.
				return nil
			}
			// TODO(M2): route through the pipeline instead of printing directly.
			// Write straight to os.Stdout (unbuffered) so each line appears the
			// instant it arrives — this is a real-time tail.
			if _, err := fmt.Fprintf(os.Stdout, "[%s %s] %s\n", line.Source, streamLabel(line.Stream), line.Raw); err != nil {
				return err
			}
		}
	}
}

// buildSources selects the ingest sources from the CLI args. Everything after a
// "--" separator is run as a subprocess (logscry -- ./app); otherwise logscry
// reads from stdin. Docker selection flags arrive in a later M1 task.
func buildSources(args []string) []ingest.Source {
	if argv := subprocessArgv(args); len(argv) > 0 {
		return []ingest.Source{ingest.NewSubprocessSource(argv)}
	}
	return []ingest.Source{ingest.NewStdinSource()}
}

// subprocessArgv returns the args following the first "--" separator, or nil if
// there is no separator (or nothing follows it).
func subprocessArgv(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return nil
}

// streamLabel renders a Stream as a short lowercase label for the temporary
// consumer output.
func streamLabel(s model.Stream) string {
	if s == model.Stderr {
		return "stderr"
	}
	return "stdout"
}
