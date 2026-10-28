// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Notices carries what a source that is still running has to say, such as the Docker
// source losing and regaining its event stream (#100), to the run's background-notice
// channel. A source can otherwise speak only by returning (see Run's report), which ends
// it.
//
// Send never blocks. Neither reader of that channel drains it continuously: --plain reads it
// on the goroutine that writes stdout, which a paused pager stops, and the TUI on its event
// loop, which waits on the terminal. A source that waited for either would stall where the
// reader stalled, and a Docker source stalled mid-reconnect stops following Docker without a
// word, which is #97's failure. So a notice that finds no room is dropped and counted, and
// the caller reports the total at exit: a lost "reconnecting" line is better than a stalled
// reconnect.
//
// It fills at most room slots of the channel, so the one-shot sends that share it (each
// source's error, the journald, truncation and replay notices) still never block, as long as
// the channel has room for those besides.
//
// It reaches the sources through the context (see WithNotices), as Truncations does, because
// Source.Lines takes nothing else per run. A context carrying none sends nothing.
type Notices struct {
	out     chan<- error
	room    int
	dropped atomic.Uint64
	mu      sync.Mutex // serialises the room check with its send
	done    bool       // Stop has been called: nothing more is sent
}

// NewNotices returns a sender of notices into out that fills at most room of its slots.
func NewNotices(out chan<- error, room int) *Notices { return &Notices{out: out, room: room} }

type noticesKey struct{}

// WithNotices returns ctx carrying n, which a source sends its notices through.
func WithNotices(ctx context.Context, n *Notices) context.Context {
	return context.WithValue(ctx, noticesKey{}, n)
}

// noticesFrom returns the Notices ctx carries, or nil, whose methods do nothing.
func noticesFrom(ctx context.Context) *Notices {
	n, _ := ctx.Value(noticesKey{}).(*Notices)
	return n
}

// Send sends err, prefixed "ingest: " as a source's error is, or drops and counts it if the
// channel has no room for it. It never blocks.
func (n *Notices) Send(err error) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.done {
		return
	}
	if len(n.out) >= n.room {
		n.dropped.Add(1)
		return
	}
	select {
	case n.out <- fmt.Errorf("ingest: %w", err):
	default:
		n.dropped.Add(1)
	}
}

// Dropped reports how many notices found no room.
func (n *Notices) Dropped() uint64 {
	if n == nil {
		return 0
	}
	return n.dropped.Load()
}

// Stop ends sending: once it returns, Send sends nothing, so the channel can be closed.
func (n *Notices) Stop() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.done = true
	n.mu.Unlock()
}
