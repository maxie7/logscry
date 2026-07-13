// SPDX-License-Identifier: Apache-2.0

// Package pipeline normalizes, templates, and deduplicates log lines. A single
// goroutine (see Run) owns the template state map, so the map needs no mutex —
// state is goroutine-confined per RDI §3.
package pipeline

import (
	"context"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// Event is one processed and scored log line, ready for display. It carries a value
// snapshot of the template's display state (not the live *model.Template) so the
// consumer, which runs in another goroutine, never races the pipeline goroutine's
// ongoing mutations of the state map.
type Event struct {
	Line      model.LogLine // normalized: Level and Message filled in
	Hash      string        // template signature hash (dedup key)
	Pattern   string        // masked template, e.g. "user <NUM> failed"
	Count     int           // running count for this template at emit time
	FirstSeen time.Time
	LastSeen  time.Time

	// Scoring (zero when the pipeline runs without a Scorer).
	Score    float64
	Escalate bool
	Reasons  []string // why it escalated; allocated once by the scorer, never mutated
}

// Pipeline holds the template dedup/count state and the scorer that reads it. Its
// methods are NOT safe for concurrent use: call them from a single goroutine only
// (see Run).
type Pipeline struct {
	templates map[string]*model.Template
	scorer    *score.Scorer // nil: no scoring, events carry a zero Score
}

// New returns an empty Pipeline scoring with sc, which may be nil.
func New(sc *score.Scorer) *Pipeline {
	return &Pipeline{templates: make(map[string]*model.Template), scorer: sc}
}

// Process runs one line through normalize -> template -> dedup/count -> score and
// returns the event to emit. now is the observation time (injected for testability).
func (p *Pipeline) Process(line model.LogLine, now time.Time) Event {
	line = Normalize(line)
	pattern, hash := Templatize(line.Message)
	tmpl, prevLastSeen := p.upsert(hash, pattern, now)

	ev := Event{
		Line:      line,
		Hash:      hash,
		Pattern:   pattern,
		Count:     tmpl.Count,
		FirstSeen: tmpl.FirstSeen,
		LastSeen:  tmpl.LastSeen,
	}
	if p.scorer != nil {
		res := p.scorer.Evaluate(line, tmpl, prevLastSeen, now)
		ev.Score, ev.Escalate, ev.Reasons = res.Score, res.Escalate, res.Reasons
	}
	return ev
}

// Stats returns the scorer's running escalation counters, or the zero value when the
// pipeline runs without a scorer.
func (p *Pipeline) Stats() score.Stats {
	if p.scorer == nil {
		return score.Stats{}
	}
	return p.scorer.Stats()
}

// upsert inserts or updates the template state for hash, returning the live entry and
// the LastSeen it held *before* this occurrence. That previous LastSeen is the gap the
// novelty cooloff is measured against, and this call is the last moment it exists.
//
// New templates start at Count 1; existing ones bump Count, advance LastSeen, and push
// now onto the bounded Recent ring.
func (p *Pipeline) upsert(hash, pattern string, now time.Time) (tmpl *model.Template, prevLastSeen time.Time) {
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
		return tmpl, time.Time{}
	}
	prevLastSeen = tmpl.LastSeen
	tmpl.Count++
	tmpl.LastSeen = now
	tmpl.Recent = pushRecent(tmpl.Recent, now)
	return tmpl, prevLastSeen
}

// pushRecent appends now to the recent ring, dropping the oldest entry in place
// once the buffer is full. Bounded at model.RecentCap with no reallocation on wrap.
func pushRecent(recent []time.Time, now time.Time) []time.Time {
	if len(recent) < model.RecentCap {
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
	// Scorer decides which events escalate. Nil to skip scoring entirely, in which
	// case every Event carries a zero Score and Escalate is never set.
	Scorer *score.Scorer
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

	p := New(opts.Scorer)
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
