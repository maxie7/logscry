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

// Run reads lines from in, processes each through a single goroutine-confined
// Pipeline, and emits Events on out. It returns when in is closed or ctx is
// cancelled, closing out on the way so the consumer can drain and stop.
func Run(ctx context.Context, in <-chan model.LogLine, out chan<- Event) {
	defer close(out)
	p := New()
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-in:
			if !ok {
				return
			}
			ev := p.Process(line, time.Now())
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}
}
