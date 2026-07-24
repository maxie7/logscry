// SPDX-License-Identifier: Apache-2.0

// Package pipeline normalizes, templates, and deduplicates log lines. A single
// goroutine (see Run) owns the template state map, so the map needs no mutex —
// state is goroutine-confined per RDI §3.
package pipeline

import (
	"context"
	"time"

	"github.com/maxie7/logscry/internal/export"
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
	Queued   bool     // the escalation reached the LLM stage (vs dropped: channel full)
	Reasons  []string // why it escalated; allocated once by the scorer, never mutated

	// Explanation is the LLM's verdict, filled in on the escalations a Snapshot
	// carries. It is nil at the moment the event is processed and arrives seconds
	// later, so it is a value copy taken at snapshot time (see collector.snapshot) —
	// which is exactly what lets a pinned card update in place from "explaining…" to an
	// answer without anything mutating what the renderer already holds.
	Explanation *model.Explanation

	// Context is the raw lines immediately before the trigger, oldest first. It is the
	// evidence an expanded card shows: what was happening around the anomaly.
	//
	// It is set ONLY on escalations, and only on the copy the collector retains for the
	// renderer (see collector.observe) — never on the events in the stream ring, which
	// number in the thousands and would each be carrying a copy of their predecessors.
	Context []string
}

// Pipeline holds the template dedup/count state and the scorer that reads it. Its
// methods are NOT safe for concurrent use: call them from a single goroutine only
// (see Run).
type Pipeline struct {
	templates map[string]*model.Template
	scorer    *score.Scorer // nil: no scoring, events carry a zero Score

	// explain is whether an LLM stage is attached. Without one — a plain run, or
	// --explain-dry-run — an escalation gets no explanation state at all, so dry-run
	// renders exactly as it did before there was a model to call.
	explain bool

	// export appends flagged anomalies to a JSONL file, or is nil when --export is off —
	// which is the default, and a nil *export.Writer is inert, so nothing below branches on
	// it. Its methods are channel sends to a goroutine that owns the file: this goroutine
	// owns the template map and must never wait on a disk (RDI §3).
	export *export.Writer

	// LLM counters, maintained here rather than in the pool: the pipeline goroutine
	// already sees every escalation on the way out and every explanation on the way
	// back, so the numbers need no atomics and no coupling to the LLM package.
	explaining     int // escalated, waiting on the model
	explained      int // answered
	explainFailed  int // model unavailable, or the escalation never got queued
	explainDropped int // escalations that never reached the pool: the queue was full
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
		ev.Score, ev.Escalate, ev.Queued, ev.Reasons = res.Score, res.Escalate, res.Queued, res.Reasons
		if ev.Escalate {
			p.flagged(tmpl, ev, now)
		}
	}
	return ev
}

// flagged handles an event that has just crossed the threshold: it records the anomaly for
// the export file, then marks its explanation state.
//
// The export half runs in every mode — TUI, --plain, and dry-run — because this is the one
// place all three pass through, and it captures the anomaly's fields at THIS instant. That
// is the point-in-time semantics the file promises: a record describes the event "this
// template escalated", so its count and last-seen are the ones that were true when it did,
// not the ones that are true when a model finishes answering seconds later.
func (p *Pipeline) flagged(tmpl *model.Template, ev Event, now time.Time) {
	p.export.Flag(export.Flag{
		Hash:      tmpl.Hash,
		Pattern:   tmpl.Pattern,
		Level:     ev.Line.Level,
		Source:    ev.Line.Source,
		Count:     tmpl.Count,
		FirstSeen: tmpl.FirstSeen,
		LastSeen:  tmpl.LastSeen,
		Score:     ev.Score,
		Reasons:   ev.Reasons,
	})

	// No LLM stage attached (--explain-dry-run): nothing will ever explain this, so the
	// flag is terminal the moment it fires and the record can be completed right here. The
	// condition is deliberately "no explainer" rather than "the dry-run flag" — it is the
	// honest reason, and it keeps the queue-full branch below unreachable in dry-run, where
	// Queued is false only because the scorer emits to a nil channel (see score.emit).
	if !p.explain {
		p.export.WouldEscalate(tmpl.Hash, now)
		return
	}
	p.escalated(tmpl, ev.Queued, now)
}

// escalated marks the template's explanation state the instant the event fires, so the
// card appears immediately rather than after the model has thought about it for five
// seconds. A dropped escalation resolves right here instead: nothing is coming for it,
// and a card stuck at "explaining…" forever would be a lie.
func (p *Pipeline) escalated(tmpl *model.Template, queued bool, now time.Time) {
	if !queued {
		tmpl.Explanation = &model.Explanation{
			Hash:    tmpl.Hash,
			Pattern: tmpl.Pattern,
			State:   model.ExplainFailed,
			Err:     "escalation queue full — the model is slower than the anomalies",
			At:      now,
		}
		p.export.Resolve(*tmpl.Explanation)
		p.explainFailed++
		p.explainDropped++
		return
	}
	tmpl.Explanation = &model.Explanation{
		Hash:    tmpl.Hash,
		Pattern: tmpl.Pattern,
		State:   model.ExplainPending,
		At:      now,
	}
	p.explaining++
}

// attach lands an explanation on the template that asked for it, seconds after the fact.
// This runs on the pipeline goroutine — the only goroutine that ever writes the template
// map — which is the entire reason that map still needs no mutex (RDI §3).
//
// The pointer is REPLACED, never mutated: a value copy of the old one may already be in a
// Snapshot that the renderer is reading.
func (p *Pipeline) attach(ex model.Explanation) {
	// The export file's terminal-state hook for the TUI, where explanations are routed
	// through this goroutine. (--plain reads them itself and taps them there instead; see
	// main.runPlain.) It sits above the template lookup because completing a record depends
	// on the answer, not on the template still being displayable — and Resolve ignores a
	// PENDING update, so a streamed answer still writes exactly one line.
	p.export.Resolve(ex)

	tmpl, ok := p.templates[ex.Hash]
	if !ok {
		return // a template we no longer know about; nothing to show it on
	}
	// A streamed answer arrives as several pending updates before its terminal one, so the
	// counter must move only on the transition OUT of pending — decrementing on every
	// update would leave "explaining N" at zero while cards were still filling in.
	if tmpl.Explanation != nil && tmpl.Explanation.State == model.ExplainPending &&
		ex.State != model.ExplainPending {
		p.explaining--
	}
	tmpl.Explanation = &ex

	switch ex.State {
	case model.ExplainDone:
		p.explained++
	case model.ExplainFailed:
		p.explainFailed++
	case model.ExplainPending:
	}
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

	// Escalations is the channel the Scorer was built with — the LLM stage's input.
	// The pipeline goroutine owns the only sender (the scorer it drives), so it is
	// also the only thing that can safely close it, which it does when ingestion ends.
	// That close is the head of the shutdown chain: it lets the worker pool finish and
	// close Explanations, which lets Run finish. Nil when no LLM is attached.
	Escalations chan<- score.EscalationRequest
	// Explanations delivers the pool's answers back to the goroutine that owns the
	// template map. Nil to skip: a plain run prints them itself, and a dry run has none.
	Explanations <-chan model.Explanation

	// Export appends one JSONL record per flagged anomaly. Nil (the default, no --export)
	// disables it, and a nil writer's methods are no-ops, so the code path is otherwise
	// identical either way. The writes happen on the writer's own goroutine — this one must
	// never block on a disk.
	Export *export.Writer
}

// Run reads lines from in and processes each through a single goroutine-confined
// Pipeline, emitting per the Options. It returns when its inputs are exhausted or ctx
// is cancelled; on the way out it emits a final snapshot and closes its channels, so
// consumers can drain and learn that the stream ended.
//
// "Inputs exhausted" is the subtle part once an LLM is attached: ingestion ending does
// not mean the run is over, because explanations for the last escalations are still in
// flight. So closing in closes the escalation channel, and Run keeps going — still
// ticking snapshots — until the pool has drained and closed the explanation channel.
func Run(ctx context.Context, in <-chan model.LogLine, opts Options) {
	if opts.Events != nil {
		defer close(opts.Events)
	}
	if opts.Snapshots != nil {
		defer close(opts.Snapshots)
	}

	p := New(opts.Scorer)
	p.explain = opts.Escalations != nil
	p.export = opts.Export
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

	lines, explanations := in, opts.Explanations
	escalations, escalationsOpen := opts.Escalations, opts.Escalations != nil

	for lines != nil || explanations != nil {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if c.dirty {
				trySend(opts.Snapshots, c.snapshot(p, time.Now()))
			}
		case ex, ok := <-explanations:
			if !ok {
				explanations = nil // the pool has finished: nothing more can arrive
				continue
			}
			p.attach(ex)
			c.dirty = true
		case line, ok := <-lines:
			if !ok {
				// Every source finished, so the scorer will emit nothing further and the
				// LLM stage can be told to wind down. A nil channel then parks this case
				// forever while the last explanations come home.
				lines = nil
				if escalationsOpen {
					close(escalations)
					escalationsOpen = false
				}
				continue
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

	// Push a last snapshot so the final counts — and the last explanation to land — are
	// never lost to a drop. Blocking, since nothing follows it, but still giving up on
	// cancellation in case the consumer has already quit.
	if opts.Snapshots != nil {
		select {
		case opts.Snapshots <- c.snapshot(p, time.Now()):
		case <-ctx.Done():
		}
	}
}
