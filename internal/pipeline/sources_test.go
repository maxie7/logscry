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
)

// --- the source list's bound (#163) ------------------------------------------------------
//
// The list behind the status bar kept every source name of the run. A journald entry names its
// own source, so a stream can drive it. These drive the collector directly, which is the whole of
// what is bounded here: observe is where a name enters the list.

var sourceT0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func sourceEvent(name string) Event { return Event{Line: model.LogLine{Source: name}} }

// unit is a distinct, invented source name.
func unit(i int) string { return fmt.Sprintf("journald:unit-%d", i) }

// sourcesHeld reads the list the way everything downstream of the collector does.
func sourcesHeld(c *collector) []string {
	return c.snapshot(New(nil), sourceT0).Stats.Sources
}

func TestSourceListStaysBounded(t *testing.T) {
	const distinct = 200_000
	c := newCollector(8)
	for i := range distinct {
		c.observe(sourceEvent(unit(i)), sourceT0)
	}
	if got := len(sourcesHeld(c)); got > 4096 {
		t.Errorf("%d distinct names left %d on the list, bound 4096", distinct, got)
	}
}

// A name is as long as the line that carried it, so a count alone would allow about 256 MiB.
func TestSourceListBoundsItsBytes(t *testing.T) {
	long := strings.Repeat("x", 60<<10)
	c := newCollector(8)
	for i := range 64 {
		c.observe(sourceEvent(unit(i)+long), sourceT0)
	}
	total := 0
	for _, name := range sourcesHeld(c) {
		total += len(name)
	}
	if total > 256<<10 {
		t.Errorf("the list holds %d bytes of names, bound %d", total, 256<<10)
	}
}

func TestSourceListEvictsTheLeastRecentlySeen(t *testing.T) {
	const oldest, kept = "journald:oldest", "journald:kept"
	c := newCollector(8)
	c.observe(sourceEvent(oldest), sourceT0)
	for i := range 10_000 {
		c.observe(sourceEvent(kept), sourceT0)
		c.observe(sourceEvent(unit(i)), sourceT0)
	}
	got := sourcesHeld(c)
	if slices.Contains(got, oldest) {
		t.Errorf("%q, silent through 20,000 lines, is still on the list", oldest)
	}
	if !slices.Contains(got, kept) {
		t.Errorf("%q, seen on every other line, was dropped", kept)
	}
}

// checkSources fails unless the map, the list and the byte count describe one set.
func checkSources(t *testing.T, s *sourceSet) {
	t.Helper()
	if got, want := s.order.Len(), len(s.names); got != want {
		t.Fatalf("the order list holds %d names and the map %d", got, want)
	}
	total := 0
	for e := s.order.Front(); e != nil; e = e.Next() {
		name := e.Value.(string)
		if s.names[name] != e {
			t.Fatalf("%q does not map to its own list element", name)
		}
		total += len(name)
	}
	if s.bytes != total {
		t.Fatalf("the byte count is %d and the names add up to %d", s.bytes, total)
	}
}

func TestSourceBoundIsTheDocumentedOne(t *testing.T) {
	if MaxSources != 4096 || MaxSourceBytes != 256<<10 {
		t.Errorf("the bound is %d names, %d bytes: README and BACKLOG say 4096 and 256 KiB",
			MaxSources, MaxSourceBytes)
	}
	if MaxSourceBytes != 64*MaxSources {
		t.Error("the count no longer binds first on names of up to 64 bytes")
	}
	if s := newSourceSet(); s.maxNames != MaxSources || s.maxBytes != MaxSourceBytes {
		t.Errorf("a new set has limits %d and %d", s.maxNames, s.maxBytes)
	}
}

// Under the bound nothing changes: the whole list, sorted, and nothing to report.
func TestOrdinaryRunKeepsTheWholeList(t *testing.T) {
	evictions := NewEvictions(nil)
	c := newCollector(8)
	c.sources.evictions = evictions
	names := []string{"stdin", "journald:unit-a", "proc:app", "docker:db", "docker:api"}
	for range 3 {
		for _, name := range names {
			c.observe(sourceEvent(name), sourceT0)
		}
	}
	stats := c.snapshot(New(nil), sourceT0).Stats
	want := slices.Sorted(slices.Values(names))
	if !slices.Equal(stats.Sources, want) {
		t.Errorf("Sources = %v, want %v", stats.Sources, want)
	}
	if stats.SourcesPartial || evictions.Count() != 0 {
		t.Errorf("partial = %v, %d evictions, want neither", stats.SourcesPartial, evictions.Count())
	}
	checkSources(t, c.sources)
}

func TestSourceEvictionsNotifyOnceAndCount(t *testing.T) {
	notified := 0
	c := newCollector(8)
	c.sources.maxNames = 8
	c.sources.evictions = NewEvictions(func() { notified++ })
	for i := range 100 {
		c.observe(sourceEvent(unit(i)), sourceT0)
	}
	if got := c.sources.evictions.Count(); got != 92 {
		t.Errorf("Count() = %d, want 92", got)
	}
	if notified != 1 {
		t.Errorf("notified %d times, want once", notified)
	}
	if !c.snapshot(New(nil), sourceT0).Stats.SourcesPartial {
		t.Error("names were dropped and the snapshot does not say so")
	}
	checkSources(t, c.sources)
}

// A name seen again is a move, not an insertion: a full list of known names evicts nothing.
func TestKnownNamesEvictNothing(t *testing.T) {
	c := newCollector(8)
	c.sources.maxNames = 8
	c.sources.evictions = NewEvictions(nil)
	for range 100 {
		for i := range 8 {
			c.observe(sourceEvent(unit(i)), sourceT0)
		}
	}
	if got := c.sources.evictions.Count(); got != 0 {
		t.Errorf("%d evictions from a list that never passed its bound", got)
	}
}

// One name past the cap, in rotation: every line evicts, the list stays put, and the count is
// evictions rather than names, which is why nothing shows it as "+N more".
func TestRotationStaysAtTheCap(t *testing.T) {
	const lines = 900
	c := newCollector(8)
	c.sources.maxNames = 8
	c.sources.evictions = NewEvictions(nil)
	for i := range lines {
		c.observe(sourceEvent(unit(i%9)), sourceT0)
	}
	if got := len(sourcesHeld(c)); got != 8 {
		t.Errorf("%d names held, want 8", got)
	}
	if got := c.sources.evictions.Count(); got != lines-8 {
		t.Errorf("%d evictions of 9 distinct names, want %d", got, lines-8)
	}
	checkSources(t, c.sources)
}

func TestLongNameEvictsWhatItTakes(t *testing.T) {
	c := newCollector(8)
	c.sources.maxBytes = 100
	for _, name := range []string{"aaaa", "bbbb", "cccc"} {
		c.observe(sourceEvent(name), sourceT0)
	}
	long := strings.Repeat("d", 98)
	c.observe(sourceEvent(long), sourceT0)
	if got := sourcesHeld(c); !slices.Equal(got, []string{long}) {
		t.Errorf("held %d names after a 98-byte name under a 100-byte bound, want only it", len(got))
	}
	checkSources(t, c.sources)
}

// The name on the current line is never its own victim, even alone and over the bound.
func TestLoneNameOverTheBytesIsKept(t *testing.T) {
	c := newCollector(8)
	c.sources.maxBytes = 16
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	c.observe(sourceEvent(first), sourceT0)
	if got := sourcesHeld(c); !slices.Equal(got, []string{first}) {
		t.Errorf("held %d names, want the one just seen", len(got))
	}
	c.observe(sourceEvent(second), sourceT0)
	if got := sourcesHeld(c); !slices.Equal(got, []string{second}) {
		t.Errorf("held %d names, want the one just seen", len(got))
	}
	checkSources(t, c.sources)
}

// Whether a Docker source is attached is a fact about the run: the tail notice must not depend on
// a name that eviction can take away.
func TestDockerSeenSurvivesEviction(t *testing.T) {
	c := newCollector(8)
	c.sources.maxNames = 4
	c.observe(sourceEvent("docker:api"), sourceT0)
	for i := range 100 {
		c.observe(sourceEvent(unit(i)), sourceT0)
	}
	stats := c.snapshot(New(nil), sourceT0).Stats
	if slices.Contains(stats.Sources, "docker:api") {
		t.Fatal("the Docker name was not evicted, so this tests nothing")
	}
	if !stats.DockerSeen {
		t.Error("evicting the only Docker name cleared DockerSeen")
	}
}

func TestDockerSeenNeedsADockerName(t *testing.T) {
	c := newCollector(8)
	for _, name := range []string{"stdin", "proc:docker:api", "journald:not-docker:api"} {
		c.observe(sourceEvent(name), sourceT0)
	}
	if c.snapshot(New(nil), sourceT0).Stats.DockerSeen {
		t.Error("DockerSeen with no Docker source")
	}
}

// Snapshot promises to share nothing: a consumer holds the list while this goroutine moves on.
func TestSnapshotsShareNoSourceList(t *testing.T) {
	c := newCollector(8)
	c.sources.maxNames = 2
	c.observe(sourceEvent("a"), sourceT0)
	c.observe(sourceEvent("b"), sourceT0)
	p := New(nil)
	first := c.snapshot(p, sourceT0).Stats.Sources
	second := c.snapshot(p, sourceT0).Stats.Sources
	first[0] = "scribbled"
	if second[0] != "a" {
		t.Errorf("writing one snapshot's list reached another's: %v", second)
	}
	if got := sourcesHeld(c); got[0] != "a" {
		t.Errorf("writing a snapshot's list reached the collector: %v", got)
	}
}

// runSources sends the bound and a thousand more distinct names through Run. Run reads lines
// straight from in, as #126's flood tests do, and this is the real bound: nothing lets a caller of
// Run choose another.
func runSources(ctx context.Context, opts Options) {
	lines := make(chan model.LogLine, 1024)
	go Run(ctx, lines, opts)
	for i := range MaxSources + 1000 {
		lines <- model.LogLine{Raw: "one template", Source: unit(i)}
	}
	close(lines)
}

func TestRunReportsSourceEvictions(t *testing.T) {
	notified := 0
	evictions := NewEvictions(func() { notified++ })
	snaps := make(chan Snapshot, 1)
	go runSources(context.Background(), Options{
		Snapshots:       snaps,
		Interval:        time.Hour, // only the final snapshot
		SourceEvictions: evictions,
	})
	var last Snapshot
	for snap := range snaps {
		last = snap
	}
	if got := len(last.Stats.Sources); got != MaxSources {
		t.Errorf("%d names held, want %d", got, MaxSources)
	}
	if !last.Stats.SourcesPartial {
		t.Error("the final snapshot does not say names were dropped")
	}
	if evictions.Count() != 1000 || notified != 1 {
		t.Errorf("%d evictions and %d notices, want 1000 and 1", evictions.Count(), notified)
	}
}

// --plain keeps no list, so it has nothing to bound and nothing to say.
func TestRunWithoutSnapshotsEvictsNoNames(t *testing.T) {
	notified := 0
	evictions := NewEvictions(func() { notified++ })
	events := make(chan Event, 1024)
	go runSources(context.Background(), Options{Events: events, SourceEvictions: evictions})
	for range events { //nolint:revive // Run closes it when the lines end
	}
	if evictions.Count() != 0 || notified != 0 {
		t.Errorf("%d evictions and %d notices from a run with no list", evictions.Count(), notified)
	}
}

// TestEvictionOrderIsLastSeen tells least recently seen from first in, first out. A name seen on
// every other line cannot: if it is dropped it comes straight back. This one is seen once, at the
// moment that decides which name goes.
func TestEvictionOrderIsLastSeen(t *testing.T) {
	c := newCollector(8)
	c.sources.maxNames = 3
	for _, name := range []string{"a", "b", "c", "a", "d"} {
		c.observe(sourceEvent(name), sourceT0)
	}
	if got, want := sourcesHeld(c), []string{"a", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("held %v, want %v: b went unseen longest", got, want)
	}
}
