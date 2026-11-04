// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// --- the template table's bound (#126) ---------------------------------------------------
//
// A stream of distinct lines must not be able to grow memory without limit, and bounding it must
// not cost what matters: these pin the bound, what is never evicted, and what an eviction costs.

var evictT0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// spell spells i in letters no masker reads as hex or a number, so every i is its own template.
func spell(i int) string {
	var b [6]byte
	for j := range b {
		b[j] = 'g' + byte(i%20)
		i /= 20
	}
	return string(b[:])
}

func noiseLine(i int) model.LogLine { return model.LogLine{Raw: "noise " + spell(i)} }

// flood sends n distinct INFO-less lines, the first numbered from, all at now, and returns the
// number after the last one.
func flood(p *Pipeline, from, n int, now time.Time) int {
	for i := range n {
		p.Process(noiseLine(from+i), now)
	}
	return from + n
}

// checkTable fails unless the map, the list and the byte count describe one table.
func checkTable(t *testing.T, p *Pipeline) {
	t.Helper()
	if got, want := p.order.Len(), len(p.templates); got != want {
		t.Fatalf("the order list holds %d templates and the map %d", got, want)
	}
	total := 0
	for e := p.order.Front(); e != nil; e = e.Next() {
		tmpl := e.Value.(*model.Template)
		if p.templates[tmpl.Hash] != tmpl {
			t.Fatalf("the list holds %q, which the map does not", tmpl.Pattern)
		}
		if tmpl.Order != e {
			t.Fatalf("%q does not hold its own list element", tmpl.Pattern)
		}
		total += templateBytes(tmpl)
	}
	if p.bytes != total {
		t.Fatalf("the byte count is %d and the table adds up to %d", p.bytes, total)
	}
}

// warm runs a scorer past its warmup and returns the time it ended at.
func warm(p *Pipeline) time.Time {
	cfg := score.Defaults()
	end := evictT0.Add(cfg.Warmup + time.Second)
	for i := range cfg.WarmupLines + 2 {
		at := evictT0
		if i > 0 {
			at = end
		}
		p.Process(model.LogLine{Raw: "warming up"}, at)
	}
	return end
}

func TestTableStopsAtItsCount(t *testing.T) {
	p := New(nil)
	p.maxTemplates = 32
	for i := range 1000 {
		p.Process(noiseLine(i), evictT0.Add(time.Duration(i)*time.Second))
		if len(p.templates) > p.maxTemplates {
			t.Fatalf("after line %d the table holds %d templates, bound %d", i, len(p.templates), p.maxTemplates)
		}
	}
	checkTable(t, p)
}

func TestTableStopsAtItsBytes(t *testing.T) {
	p := New(nil)
	p.maxBytes = 4096
	pad := strings.Repeat("x", 200)
	for i := range 1000 {
		p.Process(model.LogLine{Raw: spell(i) + " " + pad}, evictT0.Add(time.Duration(i)*time.Second))
		if p.bytes > p.maxBytes {
			t.Fatalf("after line %d the table holds %d bytes, bound %d", i, p.bytes, p.maxBytes)
		}
	}
	checkTable(t, p)
	if n := len(p.templates); n > 21 || n == 0 {
		t.Errorf("%d templates of ~224 bytes in a 4096-byte bound", n)
	}
}

// TestRecentCountsTowardTheBytes: a repeated template spends bytes with no new template arriving,
// which is why the check runs on every line. Uncharged, 50,000 full rings are 2.4 GiB inside the
// bound.
func TestRecentCountsTowardTheBytes(t *testing.T) {
	p := New(nil)
	older := p.Process(noiseLine(0), evictT0)
	busy := p.Process(noiseLine(1), evictT0)
	if p.bytes != templateBytes(p.templates[older.Hash])+templateBytes(p.templates[busy.Hash]) {
		t.Fatalf("the byte count starts at %d", p.bytes)
	}
	for i := range model.RecentCap + 8 {
		p.Process(noiseLine(1), evictT0.Add(time.Duration(i)*time.Millisecond))
	}
	if got, want := templateBytes(p.templates[busy.Hash]), len("noise ")+6+model.RecentCap*recentSlot; got != want {
		t.Fatalf("a full ring costs %d, want %d", got, want)
	}
	checkTable(t, p)

	p.maxBytes = p.bytes - 1 // the next occurrence of the busy template must be what evicts
	p.Process(noiseLine(1), evictT0.Add(time.Hour))
	if _, ok := p.templates[older.Hash]; ok {
		t.Error("over the byte bound, the older template survived a line that added no template")
	}
	checkTable(t, p)
}

func TestEvictsTheLeastRecentlySeen(t *testing.T) {
	p := New(nil)
	p.maxTemplates = 3
	a := p.Process(noiseLine(0), evictT0)
	b := p.Process(noiseLine(1), evictT0.Add(time.Second))
	c := p.Process(noiseLine(2), evictT0.Add(2*time.Second))
	p.Process(noiseLine(0), evictT0.Add(3*time.Second)) // a is now the most recent
	d := p.Process(noiseLine(3), evictT0.Add(4*time.Second))

	if _, ok := p.templates[b.Hash]; ok {
		t.Error("b, the least recently seen, was not evicted")
	}
	for name, h := range map[string]string{"a": a.Hash, "c": c.Hash, "d": d.Hash} {
		if _, ok := p.templates[h]; !ok {
			t.Errorf("%s was evicted ahead of b", name)
		}
	}
	checkTable(t, p)
}

// TestBoundIsTheDocumentedOne runs the real constants: everything else here sets a small bound.
func TestBoundIsTheDocumentedOne(t *testing.T) {
	p := New(nil)
	flood(p, 0, MaxTemplates+1000, evictT0)
	if len(p.templates) != MaxTemplates {
		t.Errorf("the table holds %d templates, want exactly %d", len(p.templates), MaxTemplates)
	}
}

// --- never evicted ---------------------------------------------------------------------------

// flaggedPipeline flags one FATAL template with an LLM attached and returns the pipeline, a queue
// holding the request, and the template's hash. With no cache TTL the answer in flight is the only
// thing holding the template.
func flaggedPipeline(t *testing.T, routed bool) (*Pipeline, chan score.EscalationRequest, string) {
	t.Helper()
	cfg := scoringConfig()
	cfg.CacheTTL = 0
	queue := make(chan score.EscalationRequest, 4)
	p := New(score.New(cfg, queue))
	p.explain, p.routed = true, routed
	p.maxTemplates = 8
	ev := p.Process(model.LogLine{Raw: "[FATAL] the disk is gone"}, evictT0)
	if !ev.Escalate || !ev.Queued {
		t.Fatalf("the FATAL line did not queue: %+v", ev)
	}
	return p, queue, ev.Hash
}

func TestAnswerInFlightIsNotEvicted(t *testing.T) {
	p, queue, hash := flaggedPipeline(t, true)
	req := <-queue

	next := flood(p, 0, 100, evictT0.Add(time.Minute))
	if _, ok := p.templates[hash]; !ok {
		t.Fatal("a template was evicted with its answer in flight")
	}
	if len(p.templates) > p.maxTemplates {
		t.Errorf("the table holds %d, bound %d", len(p.templates), p.maxTemplates)
	}

	p.attach(model.Explanation{Hash: hash, Flag: req.Flag, State: model.ExplainDone, Summary: "s", At: evictT0})
	if p.explaining != 0 || p.explained != 1 {
		t.Errorf("explaining = %d, explained = %d, want 0 and 1", p.explaining, p.explained)
	}
	if tmpl := p.templates[hash]; tmpl.Explanation == nil || tmpl.Explanation.Summary != "s" {
		t.Errorf("the answer did not land: %+v", tmpl.Explanation)
	}

	flood(p, next, 100, evictT0.Add(2*time.Minute))
	if _, ok := p.templates[hash]; ok {
		t.Error("the answer landed, and the template is still held")
	}
	checkTable(t, p)
}

// TestAPartialAnswerKeepsTheTemplate: a streamed answer's partials are not the answer.
func TestAPartialAnswerKeepsTheTemplate(t *testing.T) {
	p, queue, hash := flaggedPipeline(t, true)
	req := <-queue
	p.attach(model.Explanation{Hash: hash, Flag: req.Flag, State: model.ExplainPending, Summary: "s", At: evictT0})
	flood(p, 0, 100, evictT0.Add(time.Minute))
	if _, ok := p.templates[hash]; !ok {
		t.Error("a partial answer released the template")
	}
	if len(p.templates) > p.maxTemplates {
		t.Errorf("the table holds %d, bound %d", len(p.templates), p.maxTemplates)
	}
}

// TestPlainRunDoesNotPin: --plain reads the answers itself, so attach never counts a flag down.
// Pinning on Awaiting there would hold every flagged template for the run: the leak again, at the
// rate limit's pace.
func TestPlainRunDoesNotPin(t *testing.T) {
	p, _, hash := flaggedPipeline(t, false)
	flood(p, 0, 100, evictT0.Add(time.Minute))
	if _, ok := p.templates[hash]; ok {
		t.Error("a plain run's flag held its template, and no answer will ever release it")
	}
	if len(p.templates) > p.maxTemplates {
		t.Errorf("the table holds %d, bound %d", len(p.templates), p.maxTemplates)
	}
}

// TestCardKeepsItsAnswerPastTheTTL runs the real Run at the real bound, on the capture's clock:
// the card's template is two hours past its TTL when the flood goes by, and only the card holds
// it. Evicted, a card would keep its frozen event and lose the answer, which lives on the template.
func TestCardKeepsItsAnswerPastTheTTL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := make(chan model.LogLine, 1024)
	snaps := make(chan Snapshot, 1)
	escalations := make(chan score.EscalationRequest, 4)
	explanations := make(chan model.Explanation, 4)
	notified := 0
	evictions := NewEvictions(func() { notified++ })

	go Run(ctx, lines, Options{
		Snapshots:    snaps,
		Interval:     time.Hour, // only the final snapshot: the bound is what is under test
		Scorer:       score.New(scoringConfig(), escalations),
		Escalations:  escalations,
		Explanations: explanations,
		SourceTime:   true,
		Evictions:    evictions,
	})

	lines <- model.LogLine{Raw: "[FATAL] the disk is gone", Time: evictT0}
	req := <-escalations
	explanations <- model.Explanation{Hash: req.Hash, Flag: req.Flag, Pattern: req.Pattern,
		State: model.ExplainDone, Summary: "the disk is gone", At: evictT0}
	go func() {
		late := evictT0.Add(3 * time.Hour)
		for i := range MaxTemplates + 1000 {
			line := noiseLine(i)
			line.Time = late
			lines <- line
		}
		close(lines)
		for range escalations { //nolint:revive // the pipeline closes it when the lines end
		}
		close(explanations)
	}()

	var last Snapshot
	for snap := range snaps {
		last = snap
	}
	if len(last.Escalations) != 1 {
		t.Fatalf("%d cards, want 1", len(last.Escalations))
	}
	if ex := last.Escalations[0].Explanation; ex == nil || ex.Summary != "the disk is gone" {
		t.Errorf("the card lost its answer to the flood: %+v", ex)
	}
	if last.Stats.Explaining != 0 || last.Stats.Explained != 1 {
		t.Errorf("explaining = %d, explained = %d, want 0 and 1", last.Stats.Explaining, last.Stats.Explained)
	}
	if n := last.Stats.UniqueTemplates; n > MaxTemplates {
		t.Errorf("the table holds %d templates, bound %d", n, MaxTemplates)
	}
	if evictions.Count() == 0 || notified != 1 {
		t.Errorf("%d evictions and %d notices, want some and exactly 1", evictions.Count(), notified)
	}
}

func TestTemplateInsideItsCacheTTLIsNotEvicted(t *testing.T) {
	p := New(score.New(scoringConfig(), nil))
	p.maxTemplates = 8
	ev := p.Process(model.LogLine{Raw: "[FATAL] the disk is gone"}, evictT0)
	if !ev.Escalate {
		t.Fatalf("the FATAL line did not flag: %+v", ev)
	}

	next := flood(p, 0, 100, evictT0.Add(59*time.Minute))
	if _, ok := p.templates[ev.Hash]; !ok {
		t.Fatal("a template was evicted inside its cache TTL")
	}
	if len(p.templates) > p.maxTemplates {
		t.Errorf("the table holds %d, bound %d", len(p.templates), p.maxTemplates)
	}
	again := p.Process(model.LogLine{Raw: "[FATAL] the disk is gone"}, evictT0.Add(59*time.Minute))
	if again.Escalate || again.Count != 2 {
		t.Errorf("inside the TTL it came back as count %d, escalate %v; want 2, false", again.Count, again.Escalate)
	}

	flood(p, next, 100, evictT0.Add(2*time.Hour))
	if _, ok := p.templates[ev.Hash]; ok {
		t.Error("past its TTL, with nothing else holding it, the template was not evicted")
	}
	checkTable(t, p)
}

// TestEstablishedTemplateBurstsAmidAFlood: the evasion the hold exists to stop. Between each line
// of a real burst come more distinct lines than the table holds. Evicted between them, the
// template would restart at Count 1, have no baseline, and the burst would never fire.
func TestEstablishedTemplateBurstsAmidAFlood(t *testing.T) {
	p := New(score.New(score.Defaults(), nil))
	p.maxTemplates = 64
	line := model.LogLine{Raw: "worker finished a job"}
	at := evictT0
	for range 30 {
		p.Process(line, at)
		at = at.Add(5 * time.Second)
	}

	next, fired := 0, false
	for range 40 {
		next = flood(p, next, 200, at)
		ev := p.Process(line, at)
		if len(p.templates) > p.maxTemplates {
			t.Fatalf("the table holds %d, bound %d", len(p.templates), p.maxTemplates)
		}
		fired = fired || slices.ContainsFunc(ev.Reasons, func(r string) bool { return strings.HasPrefix(r, "burst") })
		at = at.Add(200 * time.Millisecond)
	}
	if !fired {
		t.Errorf("the burst did not fire amid the flood; the template's count is %d", p.templates[hashOf(line)].Count)
	}
	checkTable(t, p)
}

// hashOf is the template hash line gets.
func hashOf(line model.LogLine) string {
	_, h := TemplatizeLine(Normalize(line))
	return h
}

// TestFixedClockFloodPlateaus is why "inside its burst window" cannot be read literally: every
// template here is, so a literal rule never evicts, and the bound becomes line rate x 10 s.
func TestFixedClockFloodPlateaus(t *testing.T) {
	p := New(score.New(score.Defaults(), nil))
	p.maxTemplates = 32
	for i := range 1000 {
		p.Process(noiseLine(i), evictT0)
		if len(p.templates) > p.maxTemplates {
			t.Fatalf("after line %d the table holds %d templates, bound %d", i, len(p.templates), p.maxTemplates)
		}
	}
	checkTable(t, p)
}

// establish makes n templates baseline-established and all inside their windows, and returns the
// time just after.
func establish(p *Pipeline, n int) time.Time {
	cfg := score.Defaults()
	at := evictT0
	for round := range cfg.BaselineMinCount {
		at = evictT0.Add(time.Duration(round) * 5 * time.Second)
		for i := range n {
			p.Process(noiseLine(i), at)
		}
	}
	return at.Add(time.Second)
}

// TestTableFullOfHeldTemplatesStaysBounded: the extreme where the bound and the hold cannot both
// be true. A newcomer cannot be held, the walk is limited so a line stays O(1), and the holds yield
// to the bound.
func TestTableFullOfHeldTemplatesStaysBounded(t *testing.T) {
	const size = 2 * evictionWalk
	p := New(score.New(score.Defaults(), nil))
	p.maxTemplates = size
	at := establish(p, size)
	if len(p.templates) != size {
		t.Fatalf("set-up: %d templates, want %d", len(p.templates), size)
	}
	for i := range 1000 {
		p.Process(noiseLine(size+i), at)
		if len(p.templates) > p.maxTemplates {
			t.Fatalf("after newcomer %d the table holds %d templates, bound %d", i, len(p.templates), p.maxTemplates)
		}
	}
	checkTable(t, p)

	p.maxTemplates = 1
	if skips := p.shrink(nil, at); skips > evictionWalk {
		t.Errorf("one shrink took %d skips, limit %d", skips, evictionWalk)
	}
}

// TestPinsNeverYield: unlike a burst hold, a pin outlasts the walk. The table goes over its bound
// instead, by what the rate limiter let through.
func TestPinsNeverYield(t *testing.T) {
	p := New(score.New(scoringConfig(), nil))
	p.maxTemplates = 4
	var hashes []string
	for i := range 8 {
		ev := p.Process(model.LogLine{Raw: "[FATAL] gone " + spell(i)}, evictT0)
		if !ev.Escalate {
			t.Fatalf("line %d did not flag", i)
		}
		hashes = append(hashes, ev.Hash)
	}
	for i, h := range hashes {
		if _, ok := p.templates[h]; !ok {
			t.Errorf("pinned template %d was evicted", i)
		}
	}
	p.Process(noiseLine(0), evictT0)
	p.Process(noiseLine(1), evictT0)
	if len(p.templates) != 9 {
		t.Errorf("the table holds %d, want the 8 pins and the current line", len(p.templates))
	}
	checkTable(t, p)
}

// --- what an eviction costs --------------------------------------------------------------------

// TestReturningTemplateScoresNovel is the arithmetic of #27 run for real. Each template is first
// seen during warmup, so it was never flagged; it is evicted, and it returns. Only an ERROR-class
// line can cross the threshold on novelty.
func TestReturningTemplateScoresNovel(t *testing.T) {
	const firstSeen = "novel template (first seen)"
	cases := []struct {
		name     string
		raw      string
		stream   model.Stream
		score    float64
		escalate bool
	}{
		{"plain", "job finished", model.Stdout, 0.45, false},
		{"warn", "[WARN] job finished", model.Stdout, 0.65, false},
		{"stderr", "job finished", model.Stderr, 0.75, false},
		{"warn on stderr", "[WARN] job finished", model.Stderr, 0.95, false},
		{"error", "[ERROR] job finished", model.Stdout, 1.05, true},
		{"error on stderr", "[ERROR] job finished", model.Stderr, 1.35, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := New(score.New(score.Defaults(), nil))
			p.maxTemplates = 8
			line := model.LogLine{Raw: c.raw, Stream: c.stream}
			if ev := p.Process(line, evictT0); ev.Escalate {
				t.Fatalf("the warmup-era sighting flagged: %+v", ev)
			}
			at := warm(p)
			flood(p, 0, 100, at)

			ev := p.Process(line, at.Add(time.Second))
			if ev.Count != 1 || !slices.Contains(ev.Reasons, firstSeen) {
				t.Fatalf("it did not return as new: count %d, reasons %v", ev.Count, ev.Reasons)
			}
			if diff := ev.Score - c.score; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("score %.2f, want %.2f", ev.Score, c.score)
			}
			if ev.Escalate != c.escalate {
				t.Errorf("escalate = %v, want %v", ev.Escalate, c.escalate)
			}
		})
	}
}

// TestReturningErrorIsExplainedOncePerTTL is the stated cost. An ERROR every minute, with the
// table turning over between each: on main the first flag was the only one, because a 60 s gap
// never reaches the cooloff. Here a flag pins its template for a full TTL, so the error can be
// explained again at most once per TTL: three times in three hours, not 180.
func TestReturningErrorIsExplainedOncePerTTL(t *testing.T) {
	p := New(score.New(score.Defaults(), nil))
	p.maxTemplates = 8
	at := warm(p)
	line := model.LogLine{Raw: "[ERROR] the gateway timed out"}
	next := 0
	for minute := range 180 {
		now := at.Add(time.Duration(minute) * time.Minute)
		p.Process(line, now)
		next = flood(p, next, 20, now.Add(time.Second))
	}
	stats := p.Stats()
	if stats.Escalated != 3 {
		t.Errorf("escalated %d times in 3 hours, want 3: once per TTL", stats.Escalated)
	}
	if stats.Cached != 0 || stats.Suppressed != 0 {
		t.Errorf("cached %d, suppressed %d, want neither", stats.Cached, stats.Suppressed)
	}
	checkTable(t, p)
}

// --- visibility --------------------------------------------------------------------------------

func TestEvictionsNotifyOnceAndCount(t *testing.T) {
	notified := 0
	e := NewEvictions(func() { notified++ })
	p := New(nil)
	p.maxTemplates = 8
	p.evictions = e
	flood(p, 0, 100, evictT0)
	if got := e.Count(); got != 92 {
		t.Errorf("Count() = %d, want 92", got)
	}
	if notified != 1 {
		t.Errorf("notified %d times, want once", notified)
	}
}

// TestEvictionsIsReadWhileItCounts: the run reads it at exit, and the pipeline goroutine can
// outlive the run. It is here for the race detector.
func TestEvictionsIsReadWhileItCounts(t *testing.T) {
	e := NewEvictions(nil)
	p := New(nil)
	p.maxTemplates = 2
	p.evictions = e
	done := make(chan struct{})
	go func() {
		defer close(done)
		flood(p, 0, 1000, evictT0)
	}()
	for {
		select {
		case <-done:
			if e.Count() != 998 {
				t.Errorf("Count() = %d, want 998", e.Count())
			}
			return
		default:
			_ = e.Count()
		}
	}
}

func TestNilEvictionsCountsNothing(t *testing.T) {
	var e *Evictions
	e.add()
	if got := e.Count(); got != 0 {
		t.Errorf("Count() = %d", got)
	}
}

// TestWordsAreDistinctTemplates keeps the harness honest: if the masker ever merges them, every
// test above is measuring nothing.
func TestWordsAreDistinctTemplates(t *testing.T) {
	seen := make(map[string]string)
	for i := range 2000 {
		_, h := TemplatizeLine(Normalize(noiseLine(i)))
		if prev, dup := seen[h]; dup {
			t.Fatalf("%q and %q are one template", prev, spell(i))
		}
		seen[h] = spell(i)
	}
}
