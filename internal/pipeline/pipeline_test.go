// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

func TestProcessNewTemplate(t *testing.T) {
	p := New()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	ev := p.Process(model.LogLine{Source: "stdin", Raw: "user 4821 failed"}, now)

	if ev.Count != 1 {
		t.Errorf("Count = %d, want 1", ev.Count)
	}
	if ev.Pattern != "user <NUM> failed" {
		t.Errorf("Pattern = %q, want user <NUM> failed", ev.Pattern)
	}
	if !ev.FirstSeen.Equal(now) || !ev.LastSeen.Equal(now) {
		t.Errorf("FirstSeen/LastSeen = %v/%v, want %v", ev.FirstSeen, ev.LastSeen, now)
	}
	if ev.Hash == "" {
		t.Error("Hash is empty")
	}
}

func TestProcessDedupIncrementsCount(t *testing.T) {
	p := New()
	t0 := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)

	first := p.Process(model.LogLine{Raw: "user 1 failed"}, t0)
	second := p.Process(model.LogLine{Raw: "user 2 failed"}, t1) // same template

	if first.Hash != second.Hash {
		t.Fatalf("expected same template hash, got %s and %s", first.Hash, second.Hash)
	}
	if second.Count != 2 {
		t.Errorf("Count = %d, want 2", second.Count)
	}
	if !second.FirstSeen.Equal(t0) {
		t.Errorf("FirstSeen = %v, want %v (unchanged)", second.FirstSeen, t0)
	}
	if !second.LastSeen.Equal(t1) {
		t.Errorf("LastSeen = %v, want %v (advanced)", second.LastSeen, t1)
	}
}

func TestProcessDistinctTemplatesTrackedSeparately(t *testing.T) {
	p := New()
	now := time.Now()
	a := p.Process(model.LogLine{Raw: "user 1 failed"}, now)
	b := p.Process(model.LogLine{Raw: "disk full"}, now)
	if a.Hash == b.Hash {
		t.Fatal("distinct messages shared a template")
	}
	if a.Count != 1 || b.Count != 1 {
		t.Errorf("counts = %d, %d, want 1, 1", a.Count, b.Count)
	}
}

// TestRecentRingFillsAndWraps checks the burst-detection ring buffer stays bounded
// at recentCap and keeps the most-recent timestamps in order (newest last).
func TestRecentRingFillsAndWraps(t *testing.T) {
	p := New()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	total := recentCap + 10
	var hash string
	for i := 0; i < total; i++ {
		ev := p.Process(model.LogLine{Raw: "repeated line"}, base.Add(time.Duration(i)*time.Second))
		hash = ev.Hash
	}

	tmpl := p.templates[hash]
	if tmpl == nil {
		t.Fatal("template not found after processing")
	}
	if tmpl.Count != total {
		t.Errorf("Count = %d, want %d", tmpl.Count, total)
	}
	if len(tmpl.Recent) != recentCap {
		t.Fatalf("Recent len = %d, want %d (bounded)", len(tmpl.Recent), recentCap)
	}
	// Oldest retained is entry (total-recentCap); newest is (total-1).
	wantOldest := base.Add(time.Duration(total-recentCap) * time.Second)
	wantNewest := base.Add(time.Duration(total-1) * time.Second)
	if !tmpl.Recent[0].Equal(wantOldest) {
		t.Errorf("Recent[0] = %v, want %v", tmpl.Recent[0], wantOldest)
	}
	if !tmpl.Recent[len(tmpl.Recent)-1].Equal(wantNewest) {
		t.Errorf("Recent[last] = %v, want %v", tmpl.Recent[len(tmpl.Recent)-1], wantNewest)
	}
	// Timestamps must be strictly increasing (no wrap-around disorder).
	for i := 1; i < len(tmpl.Recent); i++ {
		if !tmpl.Recent[i].After(tmpl.Recent[i-1]) {
			t.Fatalf("Recent not ordered at %d: %v !after %v", i, tmpl.Recent[i], tmpl.Recent[i-1])
		}
	}
}

// TestRunProcessesAndClosesOutput exercises the goroutine path: lines in -> Events
// out, with counts reflecting dedup, and out closed after in drains.
func TestRunProcessesAndClosesOutput(t *testing.T) {
	in := make(chan model.LogLine, 3)
	in <- model.LogLine{Source: "stdin", Raw: "user 1 failed"}
	in <- model.LogLine{Source: "stdin", Raw: "user 2 failed"}
	in <- model.LogLine{Source: "stdin", Raw: "disk full"}
	close(in)

	out := make(chan Event, 3)
	go Run(context.Background(), in, Options{Events: out})

	var events []Event
	for ev := range out { // Run closes out when in drains
		events = append(events, ev)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].Count != 1 || events[1].Count != 2 {
		t.Errorf("dedup counts = %d, %d, want 1, 2", events[0].Count, events[1].Count)
	}
	if events[2].Pattern != "disk full" || events[2].Count != 1 {
		t.Errorf("third event = %q x%d, want disk full x1", events[2].Pattern, events[2].Count)
	}
}
