// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"slices"
	"sort"
	"time"
)

const (
	// escalationsKept bounds the retained escalations. They are held apart from the
	// stream ring because they must outlive it: an escalation that scrolled past a
	// second ago is exactly the one the user is trying to read.
	//
	// It bounds distinct flagged TEMPLATES, not flags: a template that escalates again
	// reuses the entry it already has (see collector.retain), so the pane retains twenty
	// separate faults rather than twenty rows that might be seven faults over again.
	escalationsKept = 20
	// cardContext is how many preceding raw lines an escalation carries for its card.
	// Enough to show what was happening around the anomaly, few enough that the whole
	// retained set stays trivial: escalationsKept × cardContext lines.
	cardContext = 3
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
		// The context rides on the retained copy only. ev was already written to the
		// ring above without it, so the thousands of ordinary events in the ring stay
		// exactly as cheap as they were.
		ev.Context = c.preceding(cardContext)
		c.retain(ev)
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

// retain records an escalation for the cards pane, keyed by TEMPLATE. A template that
// escalates a second time — which the explanation cache is designed to allow once its TTL
// expires — replaces the entry it already has rather than adding a second one beside it
// (issue #34).
//
// One card per template is not a display preference: the renderer keys its selection, its
// expand/collapse map and its index lookup by hash, so two entries sharing one would put
// two selection markers on screen and make the older of the pair unreachable with the
// arrow keys. See TestSnapshotNeverRepeatsAHash.
//
// The newest escalation wins the entry outright: its trigger line, its reasons, its score
// and its context lines are the freshest evidence, and a card is about what is happening
// now. The flag history is not lost with the old entry — it lives on the template (see
// model.Template.FlagCount) and is stamped back on at snapshot time.
//
// Removing and re-appending is what makes a returning template rise to the top of the
// pane, which is the display order coming back is worth.
func (c *collector) retain(ev Event) {
	for i, prev := range c.escalated {
		if prev.Hash == ev.Hash {
			c.escalated = append(c.escalated[:i], c.escalated[i+1:]...)
			break
		}
	}
	c.escalated = append(c.escalated, ev)
	if len(c.escalated) > escalationsKept {
		c.escalated = c.escalated[len(c.escalated)-escalationsKept:]
	}
}

// preceding returns the raw text of the n lines before the one just written, oldest
// first. It reads backwards through the ring from the newest entry but one — the newest
// IS the trigger, and a card does not need to quote the line it is already about.
//
// It returns fewer than n lines early in a run, and never reaches back past a wrap.
func (c *collector) preceding(n int) []string {
	if n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	// c.head already points past the trigger, so head-2 is the line before it. The i <
	// len(c.ring) bound is what stops a walk from lapping the ring and quoting the
	// trigger back as its own context.
	for i := 1; i <= n && i < len(c.ring); i++ {
		idx := c.head - 1 - i
		if idx < 0 {
			if !c.filled {
				break // nothing was ever written there
			}
			idx += len(c.ring) // wrapped: the older lines live at the end of the array
		}
		out = append(out, c.ring[idx].Line.Raw)
	}
	slices.Reverse(out) // oldest first, as the card reads them
	return out
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

// escalations copies out the retained escalations, each re-stamped from the template's
// CURRENT state rather than the state it had at the instant it fired.
//
// This is what makes a pinned card update in place. Everything a card says about a
// template — how often it has occurred, when it was last seen, how many times it has been
// flagged, and what the model eventually said — happens after the event has been rendered,
// on a different goroutine, with no way to reach back into the Event the renderer holds.
// So nothing tries to: the retained escalations are re-stamped here on every snapshot, and
// a card goes from "x1 · 1h ago · explaining…" to "x9 · just now" with an answer simply
// because the next snapshot read the live template. Everything is copied by value, so the
// renderer holds nothing the pipeline can later touch.
//
// The event's own fields are deliberately NOT all refreshed: Line, Reasons, Score and
// Context describe the escalation that produced the card and stay as they were. The
// numbers are about the template; the evidence is about the moment.
func (c *collector) escalations(p *Pipeline) []Event {
	out := make([]Event, 0, len(c.escalated))
	for _, ev := range c.escalated {
		if tmpl, ok := p.templates[ev.Hash]; ok {
			ev.Count, ev.FirstSeen, ev.LastSeen = tmpl.Count, tmpl.FirstSeen, tmpl.LastSeen
			ev.FlagCount, ev.FirstFlagged, ev.LastFlagged = tmpl.FlagCount, tmpl.FirstFlagged, tmpl.LastFlagged
			if tmpl.Explanation != nil {
				ex := *tmpl.Explanation
				ev.Explanation = &ex
			}
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
