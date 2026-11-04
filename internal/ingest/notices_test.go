// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"errors"
	"testing"
)

// TestPostIsSendWithoutThePrefix: what the pipeline says is not ingest's to sign, and it goes the
// same non-blocking way (#126).
func TestPostIsSendWithoutThePrefix(t *testing.T) {
	out := make(chan error, 2)
	n := NewNotices(out, 2)
	n.Post(errors.New("pipeline: said"))
	n.Send(errors.New("said"))
	if len(out) != 2 {
		t.Fatalf("%d notices on the channel, want 2", len(out))
	}
	if got := (<-out).Error(); got != "pipeline: said" {
		t.Errorf("Post sent %q", got)
	}
	if got := (<-out).Error(); got != "ingest: said" {
		t.Errorf("Send sent %q", got)
	}
}

func TestPostDropsAndCountsWithoutRoom(t *testing.T) {
	out := make(chan error, 1)
	n := NewNotices(out, 1)
	n.Post(errors.New("first"))
	n.Post(errors.New("second")) // must not block
	if got := n.Dropped(); got != 1 {
		t.Errorf("Dropped() = %d, want 1", got)
	}
	if len(out) != 1 {
		t.Errorf("%d notices on the channel, want 1", len(out))
	}
}

// TestPostAfterStopSendsNothing: the pipeline outlives the sources, and errs closes behind them.
// A Post then must be silent, never a send on a closed channel.
func TestPostAfterStopSendsNothing(t *testing.T) {
	out := make(chan error, 1)
	n := NewNotices(out, 1)
	n.Stop()
	close(out)
	n.Post(errors.New("too late")) // would panic if it sent
}

func TestNilNoticesPostsNothing(t *testing.T) {
	var n *Notices
	n.Post(errors.New("nowhere"))
}
