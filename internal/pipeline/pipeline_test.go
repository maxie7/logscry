// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

func TestProcessNewTemplate(t *testing.T) {
	p := New(nil)
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
	p := New(nil)
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
	p := New(nil)
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
// at model.RecentCap and keeps the most-recent timestamps in order (newest last).
func TestRecentRingFillsAndWraps(t *testing.T) {
	p := New(nil)
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	total := model.RecentCap + 10
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
	if len(tmpl.Recent) != model.RecentCap {
		t.Fatalf("Recent len = %d, want %d (bounded)", len(tmpl.Recent), model.RecentCap)
	}
	// Oldest retained is entry (total-model.RecentCap); newest is (total-1).
	wantOldest := base.Add(time.Duration(total-model.RecentCap) * time.Second)
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

// --- Scoring seam (M3) --------------------------------------------------------------

// scoringConfig is the scorer with warmup switched off, so a test does not have to
// feed a hundred lines before novelty is allowed to speak.
func scoringConfig() score.Config {
	cfg := score.Defaults()
	cfg.Warmup, cfg.WarmupLines = 0, 0
	return cfg
}

// TestProcessAttachesScore: the pipeline hands the live template — Recent ring and all
// — to the scorer, and carries the verdict out on the Event for the renderers.
func TestProcessAttachesScore(t *testing.T) {
	p := New(score.New(scoringConfig(), nil))
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	novel := p.Process(model.LogLine{Raw: "shard 7 unreachable"}, base)
	if !novel.Escalate {
		t.Fatalf("a novel template did not escalate: score %.2f", novel.Score)
	}
	if len(novel.Reasons) == 0 {
		t.Error("Event.Reasons is empty; the UI would show an escalation with no explanation")
	}

	// The second occurrence is neither novel nor news.
	again := p.Process(model.LogLine{Raw: "shard 7 unreachable"}, base.Add(time.Second))
	if again.Escalate {
		t.Errorf("the same template escalated twice: reasons %v", again.Reasons)
	}
	if again.Count != 2 {
		t.Errorf("Count = %d, want 2: scoring must not disturb the dedup state", again.Count)
	}

	if got := p.Stats().Escalated; got != 1 {
		t.Errorf("Stats().Escalated = %d, want 1", got)
	}
}

// TestNilScorerLeavesEventsUnscored: scoring is optional, and without it the pipeline
// behaves exactly as it did in M2.
func TestNilScorerLeavesEventsUnscored(t *testing.T) {
	p := New(nil)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	ev := p.Process(model.LogLine{Raw: "shard 7 unreachable"}, base)
	if ev.Escalate || ev.Score != 0 || ev.Reasons != nil {
		t.Errorf("event = %+v, want no scoring at all", ev)
	}
	if ev.Count != 1 || ev.Pattern != "shard <NUM> unreachable" {
		t.Errorf("event = %q x%d, want the templating to work regardless", ev.Pattern, ev.Count)
	}
	if got := p.Stats(); got != (score.Stats{}) {
		t.Errorf("Stats() = %+v, want the zero value without a scorer", got)
	}
}

// TestUpsertReportsPreviousLastSeen: the cooloff is measured against the LastSeen the
// template held *before* this occurrence, and the upsert is the last moment it exists.
func TestUpsertReportsPreviousLastSeen(t *testing.T) {
	p := New(nil)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	_, prev := p.upsert("h", "pattern", base)
	if !prev.IsZero() {
		t.Errorf("prevLastSeen = %v for a brand-new template, want the zero time", prev)
	}

	_, prev = p.upsert("h", "pattern", base.Add(20*time.Minute))
	if !prev.Equal(base) {
		t.Errorf("prevLastSeen = %v, want %v (the previous occurrence, not this one)", prev, base)
	}
}

// TestSnapshotCarriesEscalationCounters: the status bar's numbers come from the scorer,
// through the snapshot, without the renderer ever touching pipeline state.
func TestSnapshotCarriesEscalationCounters(t *testing.T) {
	cfg := scoringConfig()
	cfg.RatePerMin = 1 // one token: the second escalation is suppressed, and counted
	p := New(score.New(cfg, nil))
	c := newCollector(10)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	for i, raw := range []string{"first novel thing", "second novel thing"} {
		ev := p.Process(model.LogLine{Raw: raw}, base.Add(time.Duration(i)*time.Second))
		c.observe(ev, base)
	}

	stats := c.snapshot(p, base).Stats
	if stats.Escalations != 1 {
		t.Errorf("Escalations = %d, want 1 (the rate limit allows one)", stats.Escalations)
	}
	if stats.Suppressed != 1 {
		t.Errorf("Suppressed = %d, want 1: the UI must be able to say what it is not showing", stats.Suppressed)
	}
}
