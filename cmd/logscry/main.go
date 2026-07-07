// SPDX-License-Identifier: Apache-2.0

// Command logscry is a real-time, AI-assisted log/event triage CLI/TUI.
//
// M1: ingestion — read from stdin, run a subprocess (logscry -- ./app) and
// capture its stdout+stderr, or follow Docker container logs (--docker-all and
// friends). All sources fan into one channel; a temporary consumer prints each
// line prefixed by its source and stream. The pipeline, scoring, and TUI arrive
// in later milestones.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
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
	sources, err := buildSources(args)
	if err != nil {
		return err
	}

	lines := make(chan model.LogLine, 1024)
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

// stringList is a repeatable string flag (e.g. --docker-label k=v --docker-label x=y).
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// buildSources parses the CLI args and selects the ingest sources. Everything
// after a "--" separator is run as a subprocess (logscry -- ./app). Docker
// selection flags add a Docker source. If neither is selected, logscry reads
// from stdin. Docker and a subprocess can run together (logscry --docker-all --
// ./app).
func buildSources(args []string) ([]ingest.Source, error) {
	// Split at the first "--" before flag parsing: the stdlib flag package's own
	// "--" handling is ambiguous with a bare `logscry ./app`, and we require an
	// explicit "--" to treat trailing args as a subprocess.
	flagArgs, argv := splitArgs(args)

	fs := flag.NewFlagSet("logscry", flag.ContinueOnError)
	var (
		dockerAll   = fs.Bool("docker-all", false, "follow logs from all Docker containers")
		dockerName  = fs.String("docker-name", "", "follow Docker containers whose name matches this regexp")
		dockerTail  = fs.String("docker-tail", "100", "number of trailing log lines to fetch per container on attach")
		dockerLabel stringList
	)
	fs.Var(&dockerLabel, "docker-label", "follow Docker containers with this k=v label (repeatable, AND-combined)")
	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}

	var sources []ingest.Source
	if len(argv) > 0 {
		sources = append(sources, ingest.NewSubprocessSource(argv))
	}
	if *dockerAll || *dockerName != "" || len(dockerLabel) > 0 {
		src := ingest.NewDockerSource(ingest.DockerSelector{
			All:       *dockerAll,
			NameRegex: *dockerName,
			Label:     dockerLabel,
		})
		src.Tail = *dockerTail
		sources = append(sources, src)
	}
	if len(sources) == 0 {
		sources = append(sources, ingest.NewStdinSource())
	}
	return sources, nil
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

// streamLabel renders a Stream as a short lowercase label for the temporary
// consumer output.
func streamLabel(s model.Stream) string {
	if s == model.Stderr {
		return "stderr"
	}
	return "stdout"
}
