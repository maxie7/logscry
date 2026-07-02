// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/model"
)

// collect drains up to want lines from a source running in the background, so a
// slow/blocking send never deadlocks the test.
func collect(t *testing.T, run func(ctx context.Context, out chan<- model.LogLine) error) []model.LogLine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan model.LogLine, 64)
	done := make(chan error, 1)
	go func() { done <- run(ctx, out) }()

	var got []model.LogLine
	for {
		select {
		case ll := <-out:
			got = append(got, ll)
		case err := <-done:
			if err != nil {
				t.Fatalf("source returned error: %v", err)
			}
			// Drain anything buffered after the source finished.
			for {
				select {
				case ll := <-out:
					got = append(got, ll)
				default:
					return got
				}
			}
		}
	}
}

func TestReadLinesSplitsAndKeepsFinalUnterminatedLine(t *testing.T) {
	input := "alpha\nbeta\ngamma" // no trailing newline on the last line
	got := collect(t, func(ctx context.Context, out chan<- model.LogLine) error {
		return readLines(ctx, strings.NewReader(input), "test", model.Stdout, out)
	})

	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Raw != w {
			t.Errorf("line %d = %q, want %q", i, got[i].Raw, w)
		}
		if got[i].Source != "test" || got[i].Stream != model.Stdout {
			t.Errorf("line %d tagged source=%q stream=%v", i, got[i].Source, got[i].Stream)
		}
	}
}

func TestReadLinesSurvivesLongLine(t *testing.T) {
	// Well beyond bufio.Scanner's 64KB default cap — this is the M1 watch-out.
	long := strings.Repeat("x", 512*1024)
	input := long + "\n"
	got := collect(t, func(ctx context.Context, out chan<- model.LogLine) error {
		return readLines(ctx, strings.NewReader(input), "test", model.Stdout, out)
	})

	if len(got) != 1 {
		t.Fatalf("got %d lines, want 1", len(got))
	}
	if got[0].Raw != long {
		t.Fatalf("long line truncated: got %d bytes, want %d", len(got[0].Raw), len(long))
	}
}

func TestSubprocessSourceCapturesBothStreams(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	src := NewSubprocessSource([]string{"sh", "-c", "echo out; echo err 1>&2"})
	got := collect(t, src.Lines)

	var sawOut, sawErr bool
	for _, ll := range got {
		switch {
		case ll.Raw == "out" && ll.Stream == model.Stdout:
			sawOut = true
		case ll.Raw == "err" && ll.Stream == model.Stderr:
			sawErr = true
		}
		if ll.Source != "proc:sh" {
			t.Errorf("unexpected source %q", ll.Source)
		}
	}
	if !sawOut {
		t.Errorf("did not capture stdout line; got %+v", got)
	}
	if !sawErr {
		t.Errorf("did not capture stderr line; got %+v", got)
	}
}

// fakeSource emits a fixed set of lines then returns.
type fakeSource struct {
	name  string
	lines []string
}

func (f fakeSource) Name() string { return f.name }

func (f fakeSource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	for _, l := range f.lines {
		select {
		case out <- model.LogLine{Source: f.name, Raw: l}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestRunFansInAllSources(t *testing.T) {
	sources := []Source{
		fakeSource{name: "a", lines: []string{"a1", "a2"}},
		fakeSource{name: "b", lines: []string{"b1"}},
	}

	ctx := context.Background()
	out := make(chan model.LogLine, 16)
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, sources, out) }()

	counts := map[string]int{}
	for i := 0; i < 3; i++ {
		ll := <-out
		counts[ll.Source]++
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if counts["a"] != 2 || counts["b"] != 1 {
		t.Fatalf("unexpected fan-in counts: %v", counts)
	}
}
