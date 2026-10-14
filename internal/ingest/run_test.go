// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// failSource fails at once.
type failSource struct{ err error }

func (f failSource) Name() string { return "fail" }

func (f failSource) Lines(context.Context, chan<- model.LogLine) error { return f.err }

// liveSource emits one line per value on next, until ctx is cancelled. It then returns
// ctx's error, as a source stopped by a cancel may.
type liveSource struct{ next chan string }

func (l liveSource) Name() string { return "live" }

func (l liveSource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case raw := <-l.next:
			select {
			case out <- model.LogLine{Source: l.Name(), Raw: raw}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// reports records every error Run reports.
type reports struct {
	mu   sync.Mutex
	errs []error
	got  chan struct{} // one per report
}

func newReports() *reports { return &reports{got: make(chan struct{}, 16)} }

func (r *reports) report(err error) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
	r.got <- struct{}{}
}

func (r *reports) all() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

// TestRunReportsAFailureWhileOthersRun is decision 2 of #97: one source's error reaches
// the caller the moment that source ends, and the run goes on: the other source keeps
// delivering. The cancel that ends the run is not reported.
func TestRunReportsAFailureWhileOthersRun(t *testing.T) {
	boom := errors.New("boom")
	live := liveSource{next: make(chan string)}
	r := newReports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan model.LogLine, 4)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, []Source{failSource{boom}, live}, out, r.report) }()

	select {
	case <-r.got:
	case <-time.After(2 * time.Second):
		t.Fatal("the failure was not reported within 2s while the other source ran")
	}
	if got := r.all(); len(got) != 1 || !errors.Is(got[0], boom) {
		t.Fatalf("reported %v, want [boom]", got)
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned %v while a source was still running", err)
	default:
	}

	live.next <- "still here"
	select {
	case ll := <-out:
		if ll.Raw != "still here" {
			t.Errorf("delivered %q, want %q", ll.Raw, "still here")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the other source stopped delivering after the failure")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Errorf("Run = %v, want the joined errors, boom among them", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if got := r.all(); len(got) != 1 {
		t.Errorf("reported %v, want only boom: the cancel is not a failure", got)
	}
}

// TestRunReportsNothingOnCancel is decision 3 of #97: a source stopped by the cancel is not
// a failure, whatever error it returns, so Ctrl-C reports nothing.
func TestRunReportsNothingOnCancel(t *testing.T) {
	r := newReports()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	sources := []Source{liveSource{next: make(chan string)}, liveSource{next: make(chan string)}}
	go func() { done <- Run(ctx, sources, make(chan model.LogLine), r.report) }()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v, want the sources' own context.Canceled, joined", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if got := r.all(); len(got) != 0 {
		t.Errorf("reported %v on cancel, want nothing", got)
	}
}
