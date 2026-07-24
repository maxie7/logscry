// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// captureStdout swaps os.Stdout for a pipe and returns everything fn wrote to it. runPlain
// writes straight to the real stdout, which is the thing under test here.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w

	out := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()

	fn()
	_ = w.Close()
	os.Stdout = saved
	return <-out
}

// TestPlainSuppressesStreamedPartials is the --plain regression guard for streaming.
//
// A line-oriented consumer cannot rewrite a printed line, so a streamed answer must not
// print the same explanation three times, each slightly more complete. But the skip that
// achieves that must not take the ESCALATED marker with it — that line comes off the EVENTS
// channel, not the explanations channel, and losing it would gut --explain-dry-run and the
// "we are asking about this" marker without any streaming test noticing.
func TestPlainSuppressesStreamedPartials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lines := make(chan model.LogLine, 4)
	errs := make(chan error)
	escalations := make(chan score.EscalationRequest, 4)
	explanations := make(chan model.Explanation, 4)

	cfg := score.Defaults()
	cfg.Warmup, cfg.WarmupLines = 0, 0
	sc := score.New(cfg, escalations)

	got := captureStdout(t, func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = runPlain(ctx, lines, errs, sc, escalations, explanations, nil, false)
		}()

		lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Level: "PANIC",
			Raw: "PANIC: nil map write in handler 42"}

		select {
		case req := <-escalations:
			// Two progressive updates, then the terminal one — exactly what a streamed
			// answer delivers.
			explanations <- model.Explanation{Hash: req.Hash, Pattern: req.Pattern,
				State: model.ExplainPending, Summary: "A handler wrote to a nil map."}
			explanations <- model.Explanation{Hash: req.Hash, Pattern: req.Pattern,
				State: model.ExplainPending, Summary: "A handler wrote to a nil map.",
				LikelyCause: "The cache map is never initialised."}
			explanations <- model.Explanation{Hash: req.Hash, Pattern: req.Pattern,
				State:   model.ExplainDone,
				Summary: "A handler wrote to a nil map.", LikelyCause: "The cache map is never initialised.",
				Suggestion: "Make the map in NewServer."}
		case <-time.After(5 * time.Second):
			t.Error("the line never escalated")
		}

		close(lines)
		close(explanations)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runPlain did not return")
		}
	})

	if n := strings.Count(got, "ESCALATED:"); n != 1 {
		t.Errorf("printed %d ESCALATED lines, want 1 — the pending skip must not eat it:\n%s", n, got)
	}
	if n := strings.Count(got, "EXPLAINED:"); n != 1 {
		t.Errorf("printed %d EXPLAINED blocks, want 1 — partials must not print:\n%s", n, got)
	}
	if !strings.Contains(got, "check: Make the map in NewServer.") {
		t.Errorf("the terminal explanation is incomplete:\n%s", got)
	}
}

// TestPlainMarksTruncatedAnswers: a salvaged answer must say so in plain mode too, or a
// half-finished verdict reads exactly like a complete one.
func TestPlainMarksTruncatedAnswers(t *testing.T) {
	got := explained(model.Explanation{
		Pattern:   "panic: nil map write in handler <NUM>",
		State:     model.ExplainDone,
		Truncated: true,
		Summary:   "A handler wrote to a nil map.",
	})
	if !strings.Contains(got, "EXPLAINED (INCOMPLETE):") {
		t.Errorf("a truncated answer is not marked:\n%s", got)
	}
}
