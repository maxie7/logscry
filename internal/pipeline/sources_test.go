// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
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
