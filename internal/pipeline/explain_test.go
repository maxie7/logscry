// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// escalateAlways is a scoring config that escalates the first occurrence of anything, so
// a routing test can spend its attention on the routing rather than on tripping the
// scorer. The scorer's own behaviour is pinned down in internal/score.
func escalateAlways() score.Config {
	cfg := score.Defaults()
	cfg.Warmup = 0
	cfg.WarmupLines = 0
	return cfg
}

// waitFor drains snapshots until cond is satisfied, failing on a deadline rather than
// hanging the suite if the explanation never arrives.
func waitFor(t *testing.T, snaps <-chan Snapshot, what string, cond func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case snap, ok := <-snaps:
			if !ok {
				t.Fatalf("snapshots ended before %s", what)
			}
			if cond(snap) {
				return snap
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TestExplanationLandsOnItsTemplate is the M4 hand-off in one test: the escalation goes
// out on one channel, the answer comes back on another, seconds later and from another
// goroutine — and it must find the template that asked for it, with no lock anywhere on
// the state map. It also pins the pending state, which is what makes a card appear
// instantly rather than after the model has finished thinking.
func TestExplanationLandsOnItsTemplate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lines := make(chan model.LogLine, 4)
	snaps := make(chan Snapshot, 1)
	escalations := make(chan score.EscalationRequest, 4)
	explanations := make(chan model.Explanation, 4)

	go Run(ctx, lines, Options{
		Snapshots:    snaps,
		Interval:     time.Millisecond,
		Scorer:       score.New(escalateAlways(), escalations),
		Escalations:  escalations,
		Explanations: explanations,
	})

	lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "PANIC: nil map write in handler 42"}

	// It escalated, so the model has been asked — and the card says so immediately.
	pending := waitFor(t, snaps, "the escalation to appear as pending", func(s Snapshot) bool {
		return len(s.Escalations) == 1 && s.Escalations[0].Explanation != nil
	})
	ev := pending.Escalations[0]
	if ev.Explanation.State != model.ExplainPending {
		t.Fatalf("state = %v, want pending: the card must say it is being explained", ev.Explanation.State)
	}
	if !ev.Queued {
		t.Error("the escalation was not queued, but the channel had room")
	}
	if pending.Stats.Explaining != 1 {
		t.Errorf("Stats.Explaining = %d, want 1", pending.Stats.Explaining)
	}

	// The pool answers, keyed only by the hash it was given.
	req := <-escalations
	explanations <- model.Explanation{
		Hash:        req.Hash,
		Pattern:     req.Pattern,
		State:       model.ExplainDone,
		Summary:     "A handler wrote to a nil map.",
		LikelyCause: "The cache map is never initialised.",
		Suggestion:  "Make the map in NewServer.",
	}

	done := waitFor(t, snaps, "the explanation to land", func(s Snapshot) bool {
		return len(s.Escalations) == 1 && s.Escalations[0].Explanation != nil &&
			s.Escalations[0].Explanation.State == model.ExplainDone
	})

	// It landed on the SAME escalation — the pinned card updated in place, which is what
	// the user is watching — and on the right template.
	got := done.Escalations[0]
	if got.Hash != ev.Hash {
		t.Errorf("the explanation landed on hash %q, want %q", got.Hash, ev.Hash)
	}
	if got.Explanation.Summary != "A handler wrote to a nil map." {
		t.Errorf("summary = %q, want the model's answer", got.Explanation.Summary)
	}
	if done.Stats.Explained != 1 || done.Stats.Explaining != 0 {
		t.Errorf("stats = %d explained / %d explaining, want 1 / 0",
			done.Stats.Explained, done.Stats.Explaining)
	}
}

// TestStreamedPartialsKeepTheCardPending: a streamed answer arrives as several PENDING
// updates before its terminal one, and the "explaining N" counter must move only on the
// transition out of pending.
//
// Decrementing on every update — which is what the counter did before streaming existed,
// because a second pending update could not happen — would drive it to zero, and then
// negative, while cards were still visibly filling in.
func TestStreamedPartialsKeepTheCardPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lines := make(chan model.LogLine, 4)
	snaps := make(chan Snapshot, 1)
	escalations := make(chan score.EscalationRequest, 4)
	explanations := make(chan model.Explanation, 4)

	go Run(ctx, lines, Options{
		Snapshots:    snaps,
		Interval:     time.Millisecond,
		Scorer:       score.New(escalateAlways(), escalations),
		Escalations:  escalations,
		Explanations: explanations,
	})

	lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "PANIC: nil map write in handler 42"}
	req := <-escalations

	// Two progressive updates, as the summary and then the cause complete.
	explanations <- model.Explanation{
		Hash: req.Hash, Pattern: req.Pattern, State: model.ExplainPending,
		Summary: "A handler wrote to a nil map.",
	}
	partial := waitFor(t, snaps, "the streamed summary to appear", func(s Snapshot) bool {
		return len(s.Escalations) == 1 && s.Escalations[0].Explanation != nil &&
			s.Escalations[0].Explanation.Summary != ""
	})
	if got := partial.Escalations[0].Explanation.State; got != model.ExplainPending {
		t.Errorf("state = %v, want pending: the model has not finished", got)
	}
	if partial.Stats.Explaining != 1 {
		t.Errorf("Stats.Explaining = %d after a partial, want 1", partial.Stats.Explaining)
	}
	if partial.Stats.Explained != 0 {
		t.Errorf("Stats.Explained = %d after a partial, want 0", partial.Stats.Explained)
	}

	explanations <- model.Explanation{
		Hash: req.Hash, Pattern: req.Pattern, State: model.ExplainPending,
		Summary: "A handler wrote to a nil map.", LikelyCause: "The cache map is never initialised.",
	}
	second := waitFor(t, snaps, "the streamed cause to appear", func(s Snapshot) bool {
		return len(s.Escalations) == 1 && s.Escalations[0].Explanation != nil &&
			s.Escalations[0].Explanation.LikelyCause != ""
	})
	if second.Stats.Explaining != 1 {
		t.Errorf("Stats.Explaining = %d after two partials, want 1", second.Stats.Explaining)
	}

	// Only the terminal update moves the counters.
	explanations <- model.Explanation{
		Hash: req.Hash, Pattern: req.Pattern, State: model.ExplainDone,
		Summary: "A handler wrote to a nil map.", LikelyCause: "The cache map is never initialised.",
		Suggestion: "Make the map in NewServer.",
	}
	done := waitFor(t, snaps, "the terminal explanation", func(s Snapshot) bool {
		return len(s.Escalations) == 1 && s.Escalations[0].Explanation != nil &&
			s.Escalations[0].Explanation.State == model.ExplainDone
	})
	if done.Stats.Explaining != 0 || done.Stats.Explained != 1 {
		t.Errorf("stats = %d explaining / %d explained, want 0 / 1",
			done.Stats.Explaining, done.Stats.Explained)
	}
}

// TestExplanationForUnknownTemplateIsIgnored: a result for a template the pipeline does
// not know about is dropped, not a nil-map panic that takes the tail with it.
func TestExplanationForUnknownTemplateIsIgnored(t *testing.T) {
	p := New(nil)
	p.attach(model.Explanation{Hash: "nobody-has-this-hash", State: model.ExplainDone})
	if p.explained != 0 {
		t.Errorf("explained = %d, want 0: an orphan explanation was counted", p.explained)
	}
}

// TestDroppedEscalationIsNotLeftPending: when the escalation channel is full the request
// never reaches a worker, so nothing is ever coming back for it. The card has to say that
// — a spinner that spins forever is worse than an honest failure.
func TestDroppedEscalationIsNotLeftPending(t *testing.T) {
	full := make(chan score.EscalationRequest) // unbuffered, nobody reading: every send drops
	p := New(score.New(escalateAlways(), full))
	p.explain = true

	now := time.Now()
	ev := p.Process(model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "PANIC: nil map write"}, now)
	if !ev.Escalate {
		t.Fatal("the line did not escalate, so there is nothing to test")
	}
	if ev.Queued {
		t.Fatal("the escalation reported as queued, but nothing was reading the channel")
	}

	ex := p.templates[ev.Hash].Explanation
	if ex == nil || ex.State != model.ExplainFailed {
		t.Fatalf("explanation = %+v, want a failed state: nothing is coming for a dropped escalation", ex)
	}
	if ex.Err == "" {
		t.Error("the failure carries no reason")
	}
	if p.explaining != 0 {
		t.Errorf("explaining = %d, want 0: a dropped escalation is not in flight", p.explaining)
	}
	if p.explainDropped != 1 {
		t.Errorf("dropped = %d, want 1", p.explainDropped)
	}
}

// TestDryRunSetsNoExplanationState: with no LLM attached, an escalation is a decision and
// nothing more. Nothing may render as "explaining…" when nobody is explaining anything.
func TestDryRunSetsNoExplanationState(t *testing.T) {
	p := New(score.New(escalateAlways(), nil)) // no escalation channel: the dry-run wiring

	ev := p.Process(model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "PANIC: nil map write"}, time.Now())
	if !ev.Escalate {
		t.Fatal("the line did not escalate, so there is nothing to test")
	}
	if ex := p.templates[ev.Hash].Explanation; ex != nil {
		t.Errorf("explanation = %+v, want none: --explain-dry-run calls no model", ex)
	}
}

// TestRunWaitsForExplanationsAfterEOF: ingestion ending is not the run ending. The last
// escalations are still with the model, and dropping their answers on the floor would
// mean the most interesting thing in the log never gets explained.
func TestRunWaitsForExplanationsAfterEOF(t *testing.T) {
	lines := make(chan model.LogLine, 1)
	snaps := make(chan Snapshot, 1)
	escalations := make(chan score.EscalationRequest, 1)
	explanations := make(chan model.Explanation, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(context.Background(), lines, Options{
			Snapshots:    snaps,
			Interval:     time.Millisecond,
			Scorer:       score.New(escalateAlways(), escalations),
			Escalations:  escalations,
			Explanations: explanations,
		})
	}()

	// Drain like the renderer does. Run's parting snapshot is a blocking send — the final
	// counts are too important to drop — so a test that only reads at the end would wedge
	// it there and learn nothing.
	final := make(chan Snapshot, 1)
	go func() {
		var last Snapshot
		for snap := range snaps {
			last = snap
		}
		final <- last
	}()

	lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "PANIC: nil map write"}
	close(lines) // EOF, with the explanation still outstanding

	// Closing the line channel must close the escalation channel — that is what tells
	// the worker pool to wind down.
	req, ok := <-escalations
	if !ok {
		t.Fatal("the escalation channel closed before the escalation was read")
	}
	if _, open := <-escalations; open {
		t.Fatal("the escalation channel stayed open after ingestion ended: the pool would never exit")
	}

	// Run must still be going, waiting for the answer.
	select {
	case <-done:
		t.Fatal("Run returned at EOF and abandoned an explanation that was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	explanations <- model.Explanation{Hash: req.Hash, State: model.ExplainDone, Summary: "the last word"}
	close(explanations)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return once the last explanation had landed")
	}

	// The final snapshot carries it: an answer that arrived after EOF is still an answer.
	last := <-final
	if len(last.Escalations) != 1 || last.Escalations[0].Explanation == nil ||
		last.Escalations[0].Explanation.Summary != "the last word" {
		t.Errorf("the final snapshot lost the last explanation: %+v", last.Escalations)
	}
}
