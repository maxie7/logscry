// SPDX-License-Identifier: Apache-2.0

package score

import (
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// steady feeds one template at a constant rate for a duration, returning the time just
// after the last line. Rates are exact, not jittered: this is the boring case.
func steady(f *fixture, l model.LogLine, from time.Time, perSec int, dur time.Duration) time.Time {
	step := time.Second / time.Duration(perSec)
	n := int(dur / step)
	at := from
	for i := range n {
		at = from.Add(time.Duration(i) * step)
		f.feed(l, at)
	}
	return at.Add(step)
}

// TestSteadyHighRateNeverEscalates is the regression test for the calibration bug that
// the absolute burst floor caused: a container emitting a steady 5 lines/sec/template
// tripped "50 in 10s" and cried wolf about a completely healthy service.
//
// The rule it encodes: a steady stream escalates at NO rate, however high. A service
// doing 200 lines a second of one template is busy, not broken. Only a CHANGE is news.
func TestSteadyHighRateNeverEscalates(t *testing.T) {
	for _, perSec := range []int{5, 50, 200} {
		t.Run(rateName(perSec), func(t *testing.T) {
			f := newFixture(t, Defaults(), nil)

			// Two templates, as in the report: one plain, one ERROR on stderr — the
			// loudest a routine line can be (0.9, just under the threshold).
			info := line("user <NUM> failed login from <IP>")
			dbErr := stderr(level(line("db timeout after <NUM>ms"), "ERROR"))

			// Five minutes of simulated traffic at a rigidly constant rate.
			const dur = 5 * time.Minute
			step := time.Second / time.Duration(perSec)
			for i := range int(dur / step) {
				at := t0.Add(time.Duration(i) * step)
				f.feed(info, at)
				f.feed(dbErr, at)
			}

			stats := f.sc.Stats()
			if stats.Escalated != 0 {
				t.Errorf("Escalated = %d at a steady %d lines/sec/template, want 0: "+
					"a busy service is not an anomaly", stats.Escalated, perSec)
			}
			// And not merely gated: nothing may even reach the threshold, or the tool is
			// quiet only by luck of the rate limiter.
			if stats.Suppressed != 0 || stats.Cached != 0 {
				t.Errorf("stats = %+v at a steady %d/sec: routine traffic crossed the threshold",
					stats, perSec)
			}
		})
	}
}

// TestRateChangeEscalates is the other half, and it is what keeps the test above from
// being satisfied by a scorer that simply never fires: the SAME template, at the same
// absolute volumes, must escalate when its rate actually changes.
func TestRateChangeEscalates(t *testing.T) {
	f := newFixture(t, Defaults(), nil)
	l := line("db timeout after <NUM>ms")

	// Establish a baseline: a steady 20/sec for five minutes. Nothing so far.
	at := steady(f, l, t0, 20, 5*time.Minute)
	if got := f.sc.Stats().Escalated; got != 0 {
		t.Fatalf("Escalated = %d during the steady baseline, want 0", got)
	}

	// Now the same template jumps to 10x — 200/sec. That is news.
	steady(f, l, at, 200, 10*time.Second)

	stats := f.sc.Stats()
	if stats.Escalated != 1 {
		t.Fatalf("Escalated = %d after a 10x rate change, want exactly 1", stats.Escalated)
	}
}

// TestSpikeOnABusyTemplateIsStillVisible: the recent-occurrence ring is a bounded
// sample, and on a template already running at hundreds of lines a second it no longer
// spans the whole burst window. Measuring the rate over the whole window anyway would
// divide by history that is not there, understate the rate by exactly the factor that
// hides the spike, and leave the busiest templates in the system — the ones most worth
// watching — permanently unable to burst.
func TestSpikeOnABusyTemplateIsStillVisible(t *testing.T) {
	f := newFixture(t, Defaults(), nil)
	l := line("db timeout after <NUM>ms")

	// A genuinely busy template: 400/sec sustained, well past the point where the ring
	// stops covering the 10s window.
	at := steady(f, l, t0, 400, 3*time.Minute)
	if got := f.sc.Stats().Escalated; got != 0 {
		t.Fatalf("Escalated = %d at a steady 400/sec, want 0", got)
	}

	// It then goes to 4000/sec. Still a 10x change, and still news.
	steady(f, l, at, 4000, 5*time.Second)

	if got := f.sc.Stats().Escalated; got != 1 {
		t.Errorf("Escalated = %d after a 10x spike on a busy template, want 1: "+
			"the ring saturated and the spike became invisible", got)
	}
}

// TestBatchedFlushIsNotASpike: a service that writes its logs in batches delivers a
// clump of lines with near-identical timestamps. Over the burst window its rate is
// unchanged, so it must stay quiet — this is the false positive that a naive
// "occurrences per instant" rate measurement would produce.
func TestBatchedFlushIsNotASpike(t *testing.T) {
	f := newFixture(t, Defaults(), nil)
	l := line("request served in <NUM>ms")

	// 100 lines flushed at once, once a second, for five minutes: 100/sec on average,
	// but every line arrives in a 1ms clump.
	at := t0
	for range 300 {
		for i := range 100 {
			f.feed(l, at.Add(time.Duration(i)*10*time.Microsecond))
		}
		at = at.Add(time.Second)
	}

	if got := f.sc.Stats().Escalated; got != 0 {
		t.Errorf("Escalated = %d on a batched-flush stream, want 0: a clumped write "+
			"pattern is not a rate change", got)
	}
}

func rateName(perSec int) string {
	switch perSec {
	case 5:
		return "5/sec (the reported repro)"
	case 50:
		return "50/sec"
	default:
		return "200/sec"
	}
}
