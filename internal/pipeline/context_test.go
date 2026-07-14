// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// observeRaw pushes n ordinary lines through a collector, numbered so their order is
// checkable, and returns it.
func observeRaw(c *collector, n int, now time.Time) {
	for i := range n {
		c.observe(Event{Line: model.LogLine{Raw: "line " + strconv.Itoa(i)}}, now)
	}
}

// TestEscalationCarriesItsContext: the card's evidence is the lines that were going past
// when the anomaly fired. They are captured at escalation time, oldest first — not looked
// up later, when the tail has moved on and the context would be of some other moment.
func TestEscalationCarriesItsContext(t *testing.T) {
	now := time.Now()
	c := newCollector(100)
	observeRaw(c, 5, now)

	c.observe(Event{
		Hash: "aaa", Escalate: true,
		Line: model.LogLine{Raw: "panic: nil map"},
	}, now)

	if len(c.escalated) != 1 {
		t.Fatalf("retained %d escalations, want 1", len(c.escalated))
	}
	got := c.escalated[0].Context
	want := []string{"line 2", "line 3", "line 4"}
	if !slices.Equal(got, want) {
		t.Errorf("context = %q, want %q (the %d lines before the trigger, oldest first)",
			got, want, cardContext)
	}
	// The trigger is not its own context: a card does not quote the line it is about.
	if slices.Contains(got, "panic: nil map") {
		t.Errorf("the context includes the trigger line itself: %q", got)
	}
}

// TestContextIsNotCarriedByOrdinaryLines is the memory guarantee. The stream ring holds
// thousands of events; if each carried a copy of its predecessors, the ring would cost
// multiples of what it does. Only the escalations — twenty of them, at most — pay.
func TestContextIsNotCarriedByOrdinaryLines(t *testing.T) {
	now := time.Now()
	c := newCollector(100)
	observeRaw(c, 10, now)
	c.observe(Event{Hash: "aaa", Escalate: true, Line: model.LogLine{Raw: "boom"}}, now)

	for i, ev := range c.lines() {
		if ev.Escalate {
			continue
		}
		if ev.Context != nil {
			t.Errorf("ordinary event %d carries %d context lines; only escalations should",
				i, len(ev.Context))
		}
	}
}

// TestContextAtTheStartOfARun: an anomaly in the first second of a run has less context
// than it wants, and that is not an error — it gets what exists and nothing invented.
func TestContextAtTheStartOfARun(t *testing.T) {
	now := time.Now()
	c := newCollector(100)
	c.observe(Event{Line: model.LogLine{Raw: "the only line before it"}}, now)
	c.observe(Event{Hash: "aaa", Escalate: true, Line: model.LogLine{Raw: "boom"}}, now)

	got := c.escalated[0].Context
	if want := []string{"the only line before it"}; !slices.Equal(got, want) {
		t.Errorf("context = %q, want %q", got, want)
	}

	// And the very first line of a run, escalating with nothing before it at all.
	fresh := newCollector(100)
	fresh.observe(Event{Hash: "bbb", Escalate: true, Line: model.LogLine{Raw: "boom"}}, now)
	if got := fresh.escalated[0].Context; len(got) != 0 {
		t.Errorf("context = %q, want none: nothing preceded the first line of the run", got)
	}
}

// TestContextAcrossARingWrap: the ring wraps in place, so the lines before the trigger
// can live at the far END of the backing array. Walking back from the write head has to
// wrap with it, or an escalation on a long-running process quietly loses its evidence.
func TestContextAcrossARingWrap(t *testing.T) {
	now := time.Now()
	c := newCollector(4) // tiny, so it wraps almost immediately
	observeRaw(c, 6, now)
	c.observe(Event{Hash: "aaa", Escalate: true, Line: model.LogLine{Raw: "boom"}}, now)

	got := c.escalated[0].Context
	if want := []string{"line 3", "line 4", "line 5"}; !slices.Equal(got, want) {
		t.Errorf("context across a wrap = %q, want %q", got, want)
	}
}
