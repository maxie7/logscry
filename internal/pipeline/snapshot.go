// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"sort"
	"time"
)

const (
	// escalationsKept bounds the retained escalations. They are held apart from the
	// stream ring because they must outlive it: an escalation that scrolled past a
	// second ago is exactly the one the user is trying to read.
	escalationsKept = 20
	// defaultRingSize bounds the stream ring buffer: the TUI only ever shows a
	// tail, so memory must not grow with the log volume.
	defaultRingSize = 2000
	// defaultInterval is the snapshot cadence. Render rate is decoupled from event
	// rate: logs can arrive thousands/sec, but the view only needs ~10 fps.
	defaultInterval = 100 * time.Millisecond
	// rateWindow is the period over which lines/sec is averaged.
	rateWindow = time.Second
)

// TemplateSummary is one row of the aggregated view: a template and its running
// dedup state, copied by value out of the pipeline's state map.
type TemplateSummary struct {
	Hash      string
	Pattern   string
	Level     string // last level observed on a line of this template, "" if none
	Count     int
	FirstSeen time.Time
	LastSeen  time.Time
}

// Stats is the status-bar summary of the whole run.
type Stats struct {
	TotalLines      int
	UniqueTemplates int
	LinesPerSec     float64
	Sources         []string // every source seen so far, sorted

	// Escalations is how many events cleared every gate. Suppressed is how many were
	// worth escalating but lost to the rate limiter — shown so the UI can be honest
	// that the cost cap is currently hiding things, rather than silently hiding them.
	Escalations int
	Suppressed  int

	// The LLM stage, for the status bar: answered, waiting on the model, and failed.
	// Dropped is the escalations that never reached the pool at all because the queue
	// was full — the model falling behind the anomalies, which is worth admitting to.
	Explained     int
	Explaining    int
	ExplainFailed int
	Dropped       int
}

// Snapshot is an immutable view of the pipeline state at one instant. The
// pipeline goroutine deep-copies everything it puts here, so a consumer in
// another goroutine can read it without synchronization and without racing the
// pipeline's ongoing mutations.
type Snapshot struct {
	Lines     []Event           // recent processed events, oldest first
	Templates []TemplateSummary // sorted: count desc, then last seen desc
	Stats     Stats
	// Escalations are the most recent events that cleared every gate, oldest first.
	// The renderer pins these: at any real log rate an escalation scrolls out of the
	// stream in well under a second, which is no use to someone reading it.
	Escalations []Event
}

// collector accumulates the display state that the template map alone does not
// hold: the stream tail, the totals, and the per-template level. Like Pipeline
// it is goroutine-confined — only the pipeline goroutine touches it.
type collector struct {
	ring   []Event // fixed-size; wraps in place once full
	head   int     // next write index
	filled bool    // ring has wrapped at least once
	total  int

	// escalated retains the last escalations, oldest first, independently of the
	// stream ring.
	escalated []Event

	levels  map[string]string   // template hash -> last level seen
	sources map[string]struct{} // source name -> seen

	// Lines/sec is averaged over rateWindow rather than recomputed per tick, so
	// the number in the status bar is stable enough to read.
	rate       float64
	windowFrom time.Time
	windowN    int

	// dirty tracks whether any line arrived since the last snapshot, so an idle
	// stream builds no snapshots at all.
	dirty bool
}

func newCollector(size int) *collector {
	if size <= 0 {
		size = defaultRingSize
	}
	return &collector{
		ring:    make([]Event, size),
		levels:  make(map[string]string),
		sources: make(map[string]struct{}),
	}
}

// observe records one processed event. It is called for every line, so it stays
// O(1) — the expensive copying happens in snapshot, at the snapshot cadence.
func (c *collector) observe(ev Event, now time.Time) {
	c.ring[c.head] = ev
	c.head++
	if c.head == len(c.ring) {
		c.head = 0
		c.filled = true
	}
	c.total++
	if ev.Escalate {
		c.escalated = append(c.escalated, ev)
		if len(c.escalated) > escalationsKept {
			c.escalated = c.escalated[len(c.escalated)-escalationsKept:]
		}
	}
	if ev.Line.Level != "" {
		c.levels[ev.Hash] = ev.Line.Level
	}
	if ev.Line.Source != "" {
		c.sources[ev.Line.Source] = struct{}{}
	}
	if c.windowFrom.IsZero() {
		c.windowFrom = now
	}
	c.windowN++
	c.dirty = true
}

// snapshot builds an immutable copy of the current state. p is read but not
// mutated; both it and c must be owned by the calling goroutine.
func (c *collector) snapshot(p *Pipeline, now time.Time) Snapshot {
	c.dirty = false
	if elapsed := now.Sub(c.windowFrom); !c.windowFrom.IsZero() && elapsed >= rateWindow {
		c.rate = float64(c.windowN) / elapsed.Seconds()
		c.windowFrom, c.windowN = now, 0
	}
	scored := p.Stats()
	return Snapshot{
		Lines:       c.lines(),
		Templates:   c.summaries(p),
		Escalations: c.escalations(p),
		Stats: Stats{
			TotalLines:      c.total,
			UniqueTemplates: len(p.templates),
			LinesPerSec:     c.rate,
			Sources:         c.sourceNames(),
			Escalations:     scored.Escalated,
			Suppressed:      scored.Suppressed,
			Explained:       p.explained,
			Explaining:      p.explaining,
			ExplainFailed:   p.explainFailed,
			Dropped:         p.explainDropped,
		},
	}
}

// escalations copies out the retained escalations, each carrying the template's CURRENT
// explanation rather than the nothing it had when it fired.
//
// This is what makes a pinned card update in place. The explanation arrives seconds after
// the event, on a different goroutine, with no way to reach back into an Event that has
// already been rendered — so nothing tries to. The escalations are re-stamped from live
// template state on every snapshot instead, and a card goes from "explaining…" to an
// answer simply because the next snapshot found one there. The explanation is copied by
// value, so the renderer holds nothing the pipeline can later touch.
func (c *collector) escalations(p *Pipeline) []Event {
	out := make([]Event, 0, len(c.escalated))
	for _, ev := range c.escalated {
		if tmpl, ok := p.templates[ev.Hash]; ok && tmpl.Explanation != nil {
			ex := *tmpl.Explanation
			ev.Explanation = &ex
		}
		out = append(out, ev)
	}
	return out
}

// lines copies the ring out in chronological order, oldest first.
func (c *collector) lines() []Event {
	if !c.filled {
		return append([]Event(nil), c.ring[:c.head]...)
	}
	out := make([]Event, 0, len(c.ring))
	out = append(out, c.ring[c.head:]...) // oldest half: from head to the end
	out = append(out, c.ring[:c.head]...) // newest half: wrapped around
	return out
}

// summaries copies the template state map into sorted rows for the aggregated
// view. Sorting here keeps the renderer dumb: the snapshot arrives ready to draw.
func (c *collector) summaries(p *Pipeline) []TemplateSummary {
	out := make([]TemplateSummary, 0, len(p.templates))
	for hash, tmpl := range p.templates {
		out = append(out, TemplateSummary{
			Hash:      hash,
			Pattern:   tmpl.Pattern,
			Level:     c.levels[hash],
			Count:     tmpl.Count,
			FirstSeen: tmpl.FirstSeen,
			LastSeen:  tmpl.LastSeen,
		})
	}
	sortSummaries(out)
	return out
}

// sortSummaries orders the aggregated view: loudest template first, most recent
// breaking ties, then hash so the order never flickers between snapshots.
func sortSummaries(s []TemplateSummary) {
	sort.Slice(s, func(i, j int) bool {
		a, b := s[i], s[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if !a.LastSeen.Equal(b.LastSeen) {
			return a.LastSeen.After(b.LastSeen)
		}
		return a.Hash < b.Hash
	})
}

func (c *collector) sourceNames() []string {
	out := make([]string, 0, len(c.sources))
	for name := range c.sources {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// trySend delivers a snapshot only if the consumer has taken the previous one,
// dropping it otherwise. This is the guarantee that a slow renderer can never
// apply backpressure to ingestion: with a capacity-1 channel, the consumer always
// sees the latest state and never a backlog. It reports whether the send happened.
func trySend(ch chan<- Snapshot, s Snapshot) bool {
	select {
	case ch <- s:
		return true
	default:
		return false
	}
}
