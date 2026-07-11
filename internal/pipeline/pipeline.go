// SPDX-License-Identifier: Apache-2.0

// Package pipeline normalizes, templates, and deduplicates log lines. A single
// goroutine (see Run) owns the template state map, so the map needs no mutex —
// state is goroutine-confined per RDI §3.
package pipeline

import (
	"context"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// recentCap bounds the per-template ring buffer of recent occurrence timestamps
// used by burst detection (M3).
const recentCap = 64

// Event is one processed log line ready for display — and, in M3, already scored.
// It carries a value snapshot of the template's display state (not the live
// *model.Template) so the consumer, which runs in another goroutine, never races
// the pipeline goroutine's ongoing mutations of the state map.
type Event struct {
	Line      model.LogLine // normalized: Level and Message filled in
	Hash      string        // template signature hash (dedup key)
	Pattern   string        // masked template, e.g. "user <NUM> failed"
	Count     int           // running count for this template at emit time
	FirstSeen time.Time
	LastSeen  time.Time
	// M3 will add scoring fields (e.g. Score, Escalate) populated inside Process,
	// which has the live *model.Template — including its Recent ring — in scope.
}

// Pipeline holds the template dedup/count state. Its methods are NOT safe for
// concurrent use: call them from a single goroutine only (see Run).
type Pipeline struct {
	templates map[string]*model.Template
}

// New returns an empty Pipeline.
func New() *Pipeline {
	return &Pipeline{templates: make(map[string]*model.Template)}
}

// Process runs one line through normalize -> template -> dedup/count and returns
// the event to emit. now is the observation time (injected for testability).
//
// The M3 scoring step slots in here, after upsert and before the snapshot, where
// the live *model.Template (Count, Recent, FirstSeen) is available.
func (p *Pipeline) Process(line model.LogLine, now time.Time) Event {
	line = Normalize(line)
	pattern, hash := Templatize(line.Message)
	tmpl := p.upsert(hash, pattern, now)
	return Event{
		Line:      line,
		Hash:      hash,
		Pattern:   pattern,
		Count:     tmpl.Count,
		FirstSeen: tmpl.FirstSeen,
		LastSeen:  tmpl.LastSeen,
	}
}

// upsert inserts or updates the template state for hash, returning the live entry.
// New templates start at Count 1; existing ones bump Count, advance LastSeen, and
// push now onto the bounded Recent ring.
func (p *Pipeline) upsert(hash, pattern string, now time.Time) *model.Template {
	tmpl, ok := p.templates[hash]
	if !ok {
		tmpl = &model.Template{
			Hash:      hash,
			Pattern:   pattern,
			FirstSeen: now,
			LastSeen:  now,
			Count:     1,
			Recent:    []time.Time{now},
		}
		p.templates[hash] = tmpl
		return tmpl
	}
	tmpl.Count++
	tmpl.LastSeen = now
	tmpl.Recent = pushRecent(tmpl.Recent, now)
	return tmpl
}

// pushRecent appends now to the recent ring, dropping the oldest entry in place
// once the buffer is full. Bounded at recentCap with no reallocation on wrap.
func pushRecent(recent []time.Time, now time.Time) []time.Time {
	if len(recent) < recentCap {
		return append(recent, now)
	}
	copy(recent, recent[1:])
	recent[len(recent)-1] = now
	return recent
}

// Options selects what Run emits. Both channels are optional: plain mode wants
// the per-line Events, the TUI wants the periodic Snapshots, and neither consumer
// pays for the other.
type Options struct {
	// Events receives one Event per line, sent synchronously so the plain-text
	// consumer stays a real-time tail. Nil to skip.
	Events chan<- Event
	// Snapshots receives an immutable view of the pipeline state every Interval,
	// sent non-blockingly (see trySend) so a slow renderer can never stall
	// ingestion. Give it capacity 1: the consumer wants the latest state, not a
	// backlog. Nil to skip.
	Snapshots chan<- Snapshot
	// Interval is the snapshot cadence (default 100ms).
	Interval time.Duration
	// RingSize bounds the snapshot's stream tail (default 2000 events).
	RingSize int
}

// Run reads lines from in and processes each through a single goroutine-confined
// Pipeline, emitting per the Options. It returns when in is closed or ctx is
// cancelled; on the way out it emits a final snapshot and closes both channels,
// so consumers can drain and learn that the stream ended.
func Run(ctx context.Context, in <-chan model.LogLine, opts Options) {
	if opts.Events != nil {
		defer close(opts.Events)
	}
	if opts.Snapshots != nil {
		defer close(opts.Snapshots)
	}

	p := New()
	c := newCollector(opts.RingSize)

	// Without a snapshot consumer there is nothing to tick for; a nil channel
	// blocks forever in a select, which is exactly the "never fires" we want.
	var ticks <-chan time.Time
	if opts.Snapshots != nil {
		interval := opts.Interval
		if interval <= 0 {
			interval = defaultInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		ticks = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if c.dirty {
				trySend(opts.Snapshots, c.snapshot(p, time.Now()))
			}
		case line, ok := <-in:
			if !ok {
				// Every source finished. Push a last snapshot so the final counts are
				// never lost to a drop — blocking, since nothing follows it, but still
				// giving up on cancellation in case the consumer has already quit.
				if opts.Snapshots != nil {
					select {
					case opts.Snapshots <- c.snapshot(p, time.Now()):
					case <-ctx.Done():
					}
				}
				return
			}
			now := time.Now()
			ev := p.Process(line, now)
			if opts.Snapshots != nil {
				c.observe(ev, now)
			}
			if opts.Events != nil {
				select {
				case opts.Events <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}
