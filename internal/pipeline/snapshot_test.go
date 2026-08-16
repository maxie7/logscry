// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// TestTrySendDropsWhenFull is the guarantee that the TUI can never stall ingestion:
// with the consumer's slot already taken, the send drops rather than blocking.
func TestTrySendDropsWhenFull(t *testing.T) {
	ch := make(chan Snapshot, 1)

	if !trySend(ch, Snapshot{Stats: Stats{TotalLines: 1}}) {
		t.Fatal("first send into an empty channel was dropped")
	}

	// The channel is full and nobody is reading. A blocking send would deadlock the
	// pipeline goroutine here; trySend must return instead.
	done := make(chan bool, 1)
	go func() { done <- trySend(ch, Snapshot{Stats: Stats{TotalLines: 2}}) }()
	select {
	case sent := <-done:
		if sent {
			t.Error("send into a full channel reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("trySend blocked on a full channel")
	}

	// The dropped snapshot must not have displaced the queued one.
	if got := <-ch; got.Stats.TotalLines != 1 {
		t.Errorf("queued snapshot TotalLines = %d, want 1", got.Stats.TotalLines)
	}
}

// TestRingBufferBoundsAndWraps checks the stream tail stays bounded and keeps the
// newest events in chronological order after wrapping.
func TestRingBufferBoundsAndWraps(t *testing.T) {
	const size = 4
	c := newCollector(size)
	p := New(nil)
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

	const total = size + 3 // wrap past the end
	for i := range total {
		now := base.Add(time.Duration(i) * time.Second)
		ev := p.Process(model.LogLine{Source: "stdin", Raw: patternForIndex(i)}, now)
		c.observe(ev, now)
	}

	snap := c.snapshot(p, base.Add(total*time.Second))
	if len(snap.Lines) != size {
		t.Fatalf("ring holds %d events, want %d (bounded)", len(snap.Lines), size)
	}
	// The oldest 3 are gone; what remains is events 3..6, oldest first.
	for i, ev := range snap.Lines {
		want := patternForIndex(total - size + i)
		if ev.Line.Raw != want {
			t.Errorf("Lines[%d].Raw = %q, want %q (newest %d, in order)", i, ev.Line.Raw, want, size)
		}
	}
	if snap.Stats.TotalLines != total {
		t.Errorf("TotalLines = %d, want %d (counts every line, not just the retained ones)",
			snap.Stats.TotalLines, total)
	}
}

// patternForIndex gives each line in the ring test a distinct template.
func patternForIndex(i int) string {
	return string(rune('a'+i)) + " failed"
}

// TestSnapshotEmptyRing covers the not-yet-wrapped case: only the written slots
// are copied out, not the zero values behind them.
func TestSnapshotEmptyRing(t *testing.T) {
	c := newCollector(8)
	p := New(nil)
	now := time.Now()
	c.observe(p.Process(model.LogLine{Source: "stdin", Raw: "only line"}, now), now)

	snap := c.snapshot(p, now)
	if len(snap.Lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(snap.Lines))
	}
	if snap.Lines[0].Pattern != "only line" {
		t.Errorf("Pattern = %q, want %q", snap.Lines[0].Pattern, "only line")
	}
}

// TestTemplateSummariesSortOrder locks the aggregated view's order: loudest first,
// most recent breaking ties.
func TestTemplateSummariesSortOrder(t *testing.T) {
	t0 := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	s := []TemplateSummary{
		{Hash: "quiet", Count: 1, LastSeen: t0.Add(3 * time.Second)},
		{Hash: "tied-old", Count: 5, LastSeen: t0.Add(time.Second)},
		{Hash: "loud", Count: 50, LastSeen: t0},
		{Hash: "tied-new", Count: 5, LastSeen: t0.Add(2 * time.Second)},
	}

	sortSummaries(s)

	want := []string{"loud", "tied-new", "tied-old", "quiet"}
	for i, hash := range want {
		if s[i].Hash != hash {
			t.Errorf("position %d = %q, want %q (order: %v)", i, s[i].Hash, hash, hashes(s))
			break
		}
	}
}

func hashes(s []TemplateSummary) []string {
	out := make([]string, len(s))
	for i, t := range s {
		out[i] = t.Hash
	}
	return out
}

// TestSummariesCarryLevelAndCounts checks the aggregated rows are built from the
// template map plus the last level seen per template.
func TestSummariesCarryLevelAndCounts(t *testing.T) {
	c := newCollector(16)
	p := New(nil)
	now := time.Now()
	for range 3 {
		c.observe(p.Process(model.LogLine{Source: "docker:api", Raw: "[ERROR] disk 1 full"}, now), now)
	}

	snap := c.snapshot(p, now)
	if len(snap.Templates) != 1 {
		t.Fatalf("got %d templates, want 1 (three lines collapse into one)", len(snap.Templates))
	}
	got := snap.Templates[0]
	if got.Count != 3 {
		t.Errorf("Count = %d, want 3", got.Count)
	}
	if got.Level != "ERROR" {
		t.Errorf("Level = %q, want ERROR", got.Level)
	}
	if snap.Stats.UniqueTemplates != 1 || snap.Stats.TotalLines != 3 {
		t.Errorf("Stats = %d templates / %d lines, want 1 / 3",
			snap.Stats.UniqueTemplates, snap.Stats.TotalLines)
	}
	if len(snap.Stats.Sources) != 1 || snap.Stats.Sources[0] != "docker:api" {
		t.Errorf("Sources = %v, want [docker:api]", snap.Stats.Sources)
	}
}

// TestRunEmitsSnapshotsAndCloses exercises the goroutine path in TUI mode: a final
// snapshot carries the finished counts, and the channel closes so the TUI learns
// the stream ended.
func TestRunEmitsSnapshotsAndCloses(t *testing.T) {
	in := make(chan model.LogLine, 3)
	in <- model.LogLine{Source: "stdin", Raw: "user 1 failed"}
	in <- model.LogLine{Source: "stdin", Raw: "user 2 failed"}
	in <- model.LogLine{Source: "stdin", Raw: "disk full"}
	close(in)

	snaps := make(chan Snapshot, 1)
	go Run(context.Background(), in, Options{Snapshots: snaps, Interval: time.Millisecond})

	// Snapshots may be coalesced or dropped by design, so assert on the last one:
	// it is the only delivery the pipeline guarantees.
	var last Snapshot
	got := false
	for snap := range snaps { // Run closes snaps when in drains
		last, got = snap, true
	}
	if !got {
		t.Fatal("no snapshot was delivered")
	}
	if last.Stats.TotalLines != 3 {
		t.Errorf("TotalLines = %d, want 3", last.Stats.TotalLines)
	}
	if last.Stats.UniqueTemplates != 2 {
		t.Errorf("UniqueTemplates = %d, want 2 (the two user lines share a template)",
			last.Stats.UniqueTemplates)
	}
	if len(last.Templates) == 0 || last.Templates[0].Count != 2 {
		t.Errorf("loudest template = %v, want the x2 user template first", last.Templates)
	}
	if len(last.Lines) != 3 {
		t.Errorf("Lines = %d, want 3", len(last.Lines))
	}
}

// TestRunWithoutSnapshotsStillEmitsEvents guards the plain-mode path: no snapshot
// consumer, no ticker, events unchanged.
func TestRunWithoutSnapshotsStillEmitsEvents(t *testing.T) {
	in := make(chan model.LogLine, 1)
	in <- model.LogLine{Source: "stdin", Raw: "user 1 failed"}
	close(in)

	events := make(chan Event, 1)
	go Run(context.Background(), in, Options{Events: events})

	var n int
	for range events {
		n++
	}
	if n != 1 {
		t.Errorf("got %d events, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// Card state (issue #34): a card is a TEMPLATE, and its numbers are live.
// ---------------------------------------------------------------------------

// escalatingLine is the deterministic repro from issue #34: on stdin it scores
// novelty 0.45 + ERROR 0.6 = 1.05 the first time and escalates, and every repeat is a
// known template at 0.60 and stays quiet. That is the whole bug in two lines of input.
const escalatingLine = "ERROR: connection refused to backend"

// cardRun drives a scored Pipeline and its collector together, the way Run does, so the
// card tests exercise the real escalation path rather than a hand-built Snapshot. A card
// that is correct only when the test constructs it is not a card that is correct.
type cardRun struct {
	p *Pipeline
	c *collector
}

// newCardRun wires the dry-run shape: a real scorer with warmup off, and no LLM stage, so
// no explanation state gets in the way of the counts these tests are about.
func newCardRun(ring int) *cardRun {
	return &cardRun{p: New(score.New(escalateAlways(), nil)), c: newCollector(ring)}
}

func (r *cardRun) feed(raw string, now time.Time) Event {
	ev := r.p.Process(model.LogLine{Source: "stdin", Raw: raw}, now)
	r.c.observe(ev, now)
	return ev
}

// mustEscalate feeds a line that the test's premise says must fire, and reports the score
// and reasons when it does not — a silent "want 1, got 0" here is almost always the
// scorer's defaults having moved, not the card code being wrong.
func (r *cardRun) mustEscalate(t *testing.T, raw string, now time.Time) Event {
	t.Helper()
	ev := r.feed(raw, now)
	if !ev.Escalate {
		t.Fatalf("setup: %q at %s did not escalate (score %.2f, reasons %v)",
			raw, now.Format(time.TimeOnly), ev.Score, ev.Reasons)
	}
	return ev
}

func (r *cardRun) snap(now time.Time) Snapshot { return r.c.snapshot(r.p, now) }

// cardBase is the wall-clock the card tests hang off: the hour issue #34's evidence run
// produced its first escalation.
var cardBase = time.Date(2026, 8, 14, 11, 9, 31, 0, time.UTC)

// TestCardTracksTheLiveCount is issue #34 itself. The card was rendered from the Event
// retained at the flag instant and never refreshed, so it said x1 while the aggregated
// pane — built from the same template map, in the same snapshot — said 9.
func TestCardTracksTheLiveCount(t *testing.T) {
	r := newCardRun(64)
	r.mustEscalate(t, escalatingLine, cardBase)

	last := cardBase
	for i := 1; i < 9; i++ {
		last = cardBase.Add(time.Duration(i) * 2 * time.Second)
		if ev := r.feed(escalatingLine, last); ev.Escalate {
			t.Fatalf("occurrence %d escalated (score %.2f): a known template must stay quiet",
				i+1, ev.Score)
		}
	}

	snap := r.snap(last)
	if len(snap.Escalations) != 1 {
		t.Fatalf("got %d cards, want 1", len(snap.Escalations))
	}
	card := snap.Escalations[0]
	if card.Count != 9 {
		t.Errorf("card Count = %d, want 9", card.Count)
	}
	if !card.LastSeen.Equal(last) {
		t.Errorf("card LastSeen = %s, want %s — it is frozen at the flag instant %s",
			card.LastSeen.Format(time.TimeOnly), last.Format(time.TimeOnly),
			cardBase.Format(time.TimeOnly))
	}
	// The aggregated pane is the truth the card is measured against, and the two panes are
	// side by side on one screen: they read the same map and must never disagree.
	if row := snap.Templates[0]; row.Count != card.Count || !row.LastSeen.Equal(card.LastSeen) {
		t.Errorf("card (x%d, %s) disagrees with the aggregated row (x%d, %s)",
			card.Count, card.LastSeen.Format(time.TimeOnly),
			row.Count, row.LastSeen.Format(time.TimeOnly))
	}
}

// TestReescalationReusesTheCard: a template that goes quiet and comes back escalates a
// second time, by design — the explanation cache expires so the error is worth explaining
// again. That must land on the card it already has, not beside it.
func TestReescalationReusesTheCard(t *testing.T) {
	r := newCardRun(64)
	r.mustEscalate(t, escalatingLine, cardBase)
	for i := 1; i < 5; i++ {
		r.feed(escalatingLine, cardBase.Add(time.Duration(i)*2*time.Second))
	}

	// Past the 15m cooloff (novelty re-arms) and past the 1h cache TTL (the cache stops
	// suppressing), which is the pair of conditions the evidence run hit at 1h41m.
	back := cardBase.Add(61 * time.Minute)
	r.mustEscalate(t, escalatingLine, back)

	snap := r.snap(back)
	if len(snap.Escalations) != 1 {
		t.Fatalf("got %d cards for one template, want 1 — the pane says two problems where there is one",
			len(snap.Escalations))
	}
	card := snap.Escalations[0]
	if card.FlagCount != 2 {
		t.Errorf("FlagCount = %d, want 2", card.FlagCount)
	}
	if !card.FirstFlagged.Equal(cardBase) {
		t.Errorf("FirstFlagged = %s, want %s",
			card.FirstFlagged.Format(time.TimeOnly), cardBase.Format(time.TimeOnly))
	}
	if !card.LastFlagged.Equal(back) {
		t.Errorf("LastFlagged = %s, want %s",
			card.LastFlagged.Format(time.TimeOnly), back.Format(time.TimeOnly))
	}
	if card.Count != 6 {
		t.Errorf("Count = %d, want 6 (five before the silence, one after)", card.Count)
	}
	// The newest flag's reasons are what the card explains itself with, so "it came back
	// after a long silence" is stated rather than implied by a duplicate row.
	if !slices.ContainsFunc(card.Reasons, func(r string) bool {
		return strings.Contains(r, "unseen for")
	}) {
		t.Errorf("Reasons = %v, want the cooloff-novelty reason from the SECOND flag", card.Reasons)
	}
}

// TestCardsAreOnePerTemplate replays the shape of issue #34's evidence run: three
// templates, each escalating twice, which rendered six cards.
func TestCardsAreOnePerTemplate(t *testing.T) {
	names := []string{"addNSSStore", "CNSSCertStore", "InitNSS"}
	r := newCardRun(64)
	back := cardBase.Add(101 * time.Minute) // the evidence run's 1h41m

	for _, n := range names {
		r.mustEscalate(t, "ERROR: "+n+" failed", cardBase)
	}
	for _, n := range names {
		r.mustEscalate(t, "ERROR: "+n+" failed", back)
	}

	snap := r.snap(back)
	if len(snap.Escalations) != len(names) {
		t.Fatalf("got %d cards for %d templates, want %d", len(snap.Escalations), len(names), len(names))
	}
	for _, card := range snap.Escalations {
		if card.FlagCount != 2 {
			t.Errorf("card %q FlagCount = %d, want 2", card.Pattern, card.FlagCount)
		}
	}
	// The pane title counts cards; the status bar and the export count flags. Both numbers
	// stay true and neither is the other.
	if snap.Stats.Escalations != 6 {
		t.Errorf("Stats.Escalations = %d, want 6 — the title's bracket and the JSONL line count",
			snap.Stats.Escalations)
	}
}

// TestReescalationMovesTheCardToTheFront: coming back is news, so the card rises. Cards
// are delivered oldest first and the renderer reverses them, so "front" is last here.
func TestReescalationMovesTheCardToTheFront(t *testing.T) {
	r := newCardRun(64)
	first := r.mustEscalate(t, "ERROR: alpha subsystem is down", cardBase)
	second := r.mustEscalate(t, "ERROR: bravo subsystem is down", cardBase.Add(time.Minute))
	r.mustEscalate(t, "ERROR: alpha subsystem is down", cardBase.Add(61*time.Minute))

	snap := r.snap(cardBase.Add(61 * time.Minute))
	if len(snap.Escalations) != 2 {
		t.Fatalf("got %d cards, want 2", len(snap.Escalations))
	}
	if got := snap.Escalations[len(snap.Escalations)-1].Hash; got != first.Hash {
		t.Errorf("newest card is %q, want the re-escalated alpha %q (bravo is %q)",
			got, first.Hash, second.Hash)
	}
}

// TestFlagCountSurvivesACardAgeingOut pins where the flag history lives. It hangs off the
// template, not off the retained event, so a template whose card scrolled out of the
// bounded set and then came back does not claim to be flagged for the first time.
func TestFlagCountSurvivesACardAgeingOut(t *testing.T) {
	r := newCardRun(256)
	victim := "ERROR: alpha subsystem is down"
	first := r.mustEscalate(t, victim, cardBase)

	// One escalation a minute so the token bucket keeps up, and enough of them to push the
	// first card past escalationsKept.
	for i := 1; i <= escalationsKept+1; i++ {
		r.mustEscalate(t, fmt.Sprintf("ERROR: subsystem %c is down", 'a'+i),
			cardBase.Add(time.Duration(i)*time.Minute))
	}
	if snap := r.snap(cardBase.Add(time.Hour)); len(snap.Escalations) != escalationsKept {
		t.Fatalf("retained %d cards, want the bound of %d", len(snap.Escalations), escalationsKept)
	}

	back := cardBase.Add(70 * time.Minute)
	r.mustEscalate(t, victim, back)

	snap := r.snap(back)
	card := snap.Escalations[len(snap.Escalations)-1]
	if card.Hash != first.Hash {
		t.Fatalf("newest card is %q, want the returning %q", card.Pattern, first.Pattern)
	}
	if card.FlagCount != 2 {
		t.Errorf("FlagCount = %d, want 2 — the first flag was forgotten when the card aged out",
			card.FlagCount)
	}
	if !card.FirstFlagged.Equal(cardBase) {
		t.Errorf("FirstFlagged = %s, want the original %s",
			card.FirstFlagged.Format(time.TimeOnly), cardBase.Format(time.TimeOnly))
	}
}

// TestSnapshotNeverRepeatsAHash is the named invariant, and the guard that stops three
// separate TUI symptoms returning. The cards pane keys its selection, its expand/collapse
// map and its index lookup by template hash (internal/tui: Model.selKey, Model.expanded,
// indexOfCard), so two cards sharing a hash meant:
//
//   - both drew the selection marker, so the pane showed two selections;
//   - expanding one expanded the other;
//   - the down-arrow could not reach the older twin — indexOfCard resolved its hash back
//     to the newer one, so the selection snapped to the top and scroll-lock engaged for no
//     visible reason.
//
// All three were the same root: the collector appended one entry per ESCALATION. The guard
// belongs here and not in the TUI, because after the fix a duplicate hash is unreachable
// from the renderer — a TUI test would have to build a snapshot the collector can no longer
// produce, which is a test of an impossible state.
func TestSnapshotNeverRepeatsAHash(t *testing.T) {
	r := newCardRun(512)
	now := cardBase

	check := func(stage string) {
		t.Helper()
		snap := r.snap(now)
		seen := make(map[string]int, len(snap.Escalations))
		for i, ev := range snap.Escalations {
			if prev, dup := seen[ev.Hash]; dup {
				t.Fatalf("%s: cards %d and %d share hash %s (%q) — one template, two cards",
					stage, prev, i, ev.Hash, ev.Pattern)
			}
			seen[ev.Hash] = i
		}
	}

	// A mixed workload rather than a tidy N-by-2 grid: templates escalating at different
	// times, some more than twice, quiet occurrences interleaved, and enough distinct
	// templates that early cards age past escalationsKept while later ones return.
	subsystems := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}
	for round := range 6 {
		for i, s := range subsystems {
			line := "ERROR: " + s + " subsystem is down"
			r.feed(line, now)
			// Quiet repeats between flags: these must move the count, never the card list.
			for range i {
				now = now.Add(3 * time.Second)
				r.feed(line, now)
			}
			now = now.Add(time.Duration(20+i*7) * time.Second)
			check(fmt.Sprintf("round %d, %s", round, s))
		}
		// Filler templates, unique per round, to churn the bounded set underneath.
		for i := range escalationsKept / 2 {
			r.feed(fmt.Sprintf("ERROR: filler %s%c is down", subsystems[round], 'a'+i), now)
			now = now.Add(31 * time.Second)
		}
		check(fmt.Sprintf("round %d filler", round))
		now = now.Add(70 * time.Minute) // past the cooloff and the cache TTL: everything re-arms
	}

	if snap := r.snap(now); len(snap.Escalations) == 0 {
		t.Fatal("the workload produced no cards at all; the test proves nothing")
	}
}
