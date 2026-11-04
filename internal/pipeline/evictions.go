// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"sync"
	"sync/atomic"
)

// Evictions counts the templates a run evicted at the bound, and calls notify on the first. An
// eviction costs history rather than data, but it changes how a returning template is scored, so
// it is announced as it starts and not only counted at the end.
//
// It follows ingest.Truncations. The count is atomic because the pipeline goroutine can outlive
// the run that reads it (Ctrl-C returns while it is still mid-loop). A nil *Evictions counts
// nothing.
type Evictions struct {
	n      atomic.Uint64
	once   sync.Once
	notify func()
}

// NewEvictions returns a counter that calls notify on the first eviction. notify runs on the
// pipeline goroutine and must not block. nil notifies nothing.
func NewEvictions(notify func()) *Evictions { return &Evictions{notify: notify} }

// add counts one eviction.
func (e *Evictions) add() {
	if e == nil {
		return
	}
	e.n.Add(1)
	e.once.Do(func() {
		if e.notify != nil {
			e.notify()
		}
	})
}

// Count reports how many templates have been evicted. Safe to call at any time.
func (e *Evictions) Count() uint64 {
	if e == nil {
		return 0
	}
	return e.n.Load()
}
