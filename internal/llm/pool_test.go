// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// fakeBackend is a Backend that answers however a test needs it to — instantly, slowly,
// or not at all — without any HTTP at all.
type fakeBackend struct {
	calls atomic.Int64
	// block, if non-nil, holds every call until it is closed or the context is
	// cancelled: a model that has stopped answering.
	block chan struct{}
	err   error
}

func (f *fakeBackend) Name() string { return "fake" }

func (f *fakeBackend) Explain(ctx context.Context, req ExplainRequest) (ExplainResponse, error) {
	f.calls.Add(1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ExplainResponse{}, ctx.Err()
		}
	}
	if f.err != nil {
		return ExplainResponse{}, f.err
	}
	return ExplainResponse{Summary: "explained " + req.Template}, nil
}

// escalation is a minimal request for the pool to chew on.
func escalation(hash, pattern string) score.EscalationRequest {
	return score.EscalationRequest{Hash: hash, Pattern: pattern, Count: 1}
}

// poolConfig is a Defaults() with the knobs a pool test cares about.
func poolConfig(workers int) Config {
	cfg := Defaults()
	cfg.Workers = workers
	return cfg
}

// TestPoolRoutesResultsByHash: the explanation arrives seconds after the escalation, on
// another goroutine, with nothing but the hash to say what it is about. If that routing
// is wrong, the answer lands on the wrong card — which is worse than no answer at all.
func TestPoolRoutesResultsByHash(t *testing.T) {
	in := make(chan score.EscalationRequest, 3)
	out := make(chan model.Explanation, 3)
	in <- escalation("aaa", "panic: nil map")
	in <- escalation("bbb", "connection refused")
	in <- escalation("ccc", "disk full")
	close(in)

	Run(context.Background(), &fakeBackend{}, poolConfig(2), in, out)

	got := make(map[string]model.Explanation)
	for ex := range out { // Run closed it, so this drains and stops
		got[ex.Hash] = ex
	}
	if len(got) != 3 {
		t.Fatalf("got %d explanations, want 3", len(got))
	}
	for hash, pattern := range map[string]string{"aaa": "panic: nil map", "bbb": "connection refused", "ccc": "disk full"} {
		ex := got[hash]
		if ex.State != model.ExplainDone {
			t.Errorf("hash %s: state = %v, want done", hash, ex.State)
		}
		if ex.Summary != "explained "+pattern {
			t.Errorf("hash %s: summary = %q, want the answer for %q", hash, ex.Summary, pattern)
		}
	}
}

// TestPoolMarksFailuresUnavailable: a model that is down produces a card that says so,
// not a missing card and not a crash.
func TestPoolMarksFailuresUnavailable(t *testing.T) {
	in := make(chan score.EscalationRequest, 1)
	out := make(chan model.Explanation, 1)
	in <- escalation("aaa", "panic: nil map")
	close(in)

	Run(context.Background(), &fakeBackend{err: errors.New("connection refused")}, poolConfig(1), in, out)

	ex := <-out
	if ex.State != model.ExplainFailed {
		t.Fatalf("state = %v, want failed", ex.State)
	}
	if ex.Err == "" {
		t.Error("a failed explanation carries no reason: the card would say nothing")
	}
	if ex.Hash != "aaa" {
		t.Errorf("hash = %q, want the template that asked", ex.Hash)
	}
}

// TestPoolNeverBlocksTheProducer is the whole point of the stage. With every worker stuck
// on a model that has stopped answering, the producer must still run at full speed — the
// bounded channel drops, exactly as the scorer intends, and ingestion never feels it.
func TestPoolNeverBlocksTheProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backend := &fakeBackend{block: make(chan struct{})}
	defer close(backend.block)

	const queue = 4
	in := make(chan score.EscalationRequest, queue)
	out := make(chan model.Explanation, 64)
	go Run(ctx, backend, poolConfig(2), in, out)

	// A scorer emits with exactly this non-blocking send (see score.emit): a full
	// channel drops rather than stalls.
	var sent, dropped int
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 1000 {
			select {
			case in <- escalation("h", "pattern"):
				sent++
			default:
				dropped++
			}
			_ = i
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the producer blocked behind a stuck model: ingestion would have stalled")
	}

	if dropped == 0 {
		t.Error("nothing was dropped against a wedged pool, so the channel is not bounded")
	}
	// The queue plus whatever the workers picked up: bounded, and nowhere near 1000.
	if sent > queue+poolConfig(2).Workers {
		t.Errorf("accepted %d escalations with every worker stuck, want at most %d",
			sent, queue+poolConfig(2).Workers)
	}
}

// TestPoolCancelsPromptly: Ctrl+C with requests in flight must not wait out the model.
// The deadline is the assertion — a regression fails the test rather than hanging the run.
func TestPoolCancelsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	backend := &fakeBackend{block: make(chan struct{})} // never released: only ctx can end it
	in := make(chan score.EscalationRequest, 2)
	out := make(chan model.Explanation, 2)
	in <- escalation("aaa", "panic")
	in <- escalation("bbb", "panic")

	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, backend, poolConfig(2), in, out)
	}()

	// Wait until the workers are actually inside the backend, so cancellation has
	// something in flight to abort.
	for backend.calls.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the pool did not return promptly on cancellation: Ctrl+C would hang")
	}
	if _, open := <-out; open {
		t.Error("the pool emitted a result for a cancelled request")
	}
}

// TestPoolStopsWhenInputCloses: ingestion ending winds the stage down, which is what lets
// the pipeline finish and the process exit cleanly.
func TestPoolStopsWhenInputCloses(t *testing.T) {
	in := make(chan score.EscalationRequest)
	out := make(chan model.Explanation, 1)
	close(in)

	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(context.Background(), &fakeBackend{}, poolConfig(2), in, out)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the pool did not exit when its input closed")
	}
	if _, open := <-out; open {
		t.Error("the pool did not close its output channel")
	}
}
