// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
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
	p := New()
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
	p := New()
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
	p := New()
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
