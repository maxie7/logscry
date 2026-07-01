// SPDX-License-Identifier: Apache-2.0

// Command logscry is a real-time, AI-assisted log/event triage CLI/TUI.
//
// M0: minimal end-to-end path — read stdin, run each line through the pipeline
// pass-through, and print it to stdout, with graceful shutdown on SIGINT/SIGTERM.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/maxie7/logscry/internal/ingest"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "logscry:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	lines := make(chan model.LogLine, 1024)

	src := ingest.NewStdinSource()
	go func() {
		defer close(lines)
		// A read error on stdin ends the run; surface it on stderr but don't crash.
		if err := src.Lines(ctx, lines); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "logscry: ingest:", err)
		}
	}()

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			processed := pipeline.Process(line)
			if _, err := fmt.Fprintln(out, processed.Raw); err != nil {
				return err
			}
			// Flush per line so piped/streamed output appears in real time.
			if err := out.Flush(); err != nil {
				return err
			}
		}
	}
}
