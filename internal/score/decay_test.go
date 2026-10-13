// SPDX-License-Identifier: Apache-2.0

package score

import (
	"testing"
	"time"
)

// This file pins how far back the burst baseline remembers (issue #37). Every history
// here is generated: the shapes come from measurements on real captures, but no line of
// a capture is in this repository.
//
// The three tests bound the forgetting from both sides, the way burstfloor_test.go bounds
// the volume gate:
//   - TestFormerlyChattyTemplateBurstsAgain fails if the baseline remembers too much (the
//     lifetime average, or a half-life past ~80 minutes, misses the incident's onset);
//   - TestSameRateResumptionStaysQuietPastTheCacheTTL fails if it forgets too fast (a
//     half-life under ~26 minutes calls a template resuming at its own rate a burst);
//   - TestClusterIsNotAbsorbedIntoItsOwnBaseline fails if it forgets faster still (a
//     half-life under ~35 seconds swallows a 25-line cluster into its own baseline).

// TestFormerlyChattyTemplateBurstsAgain is the ab37 experiment's schedule: one INFO
// template at 1/s for two hours, 1/min for one hour, then 3/s for 20 seconds. The
// incident runs at three times the rate the template held for two hours. On a real
// journal, v0.10.3's lifetime baseline (0.67/s at the incident) let the default
// multiplier 5 miss it entirely; it fired only at multiplier 4.5 or below.
func TestFormerlyChattyTemplateBurstsAgain(t *testing.T) {
	f := newFixture(t, Defaults(), nil)
	l := line("worker processed job id=<NUM>")

	for i := range 7200 { // phase 1: 1/s for 2 h
		if r := f.feed(l, t0.Add(time.Duration(i)*time.Second)); r.Escalate {
			t.Fatalf("phase 1 line %d escalated: %v", i+1, r.Reasons)
		}
	}
	p2 := t0.Add(2 * time.Hour)
	for i := range 60 { // phase 2: 1/min for 1 h
		if r := f.feed(l, p2.Add(time.Duration(i)*time.Minute)); r.Escalate {
			t.Fatalf("phase 2 line %d escalated: %v", i+1, r.Reasons)
		}
	}
	p3 := t0.Add(3 * time.Hour)
	for k := 1; k <= 60; k++ { // phase 3: 3/s for 20 s
		r := f.feed(l, p3.Add(time.Duration(k-1)*time.Second/3))
		if r.Escalate {
			if !hasReason(r.Reasons, "burst:") {
				t.Fatalf("incident line %d escalated without a burst reason: %v", k, r.Reasons)
			}
			if k > f.sc.cfg.BurstMinCount {
				t.Errorf("incident caught at line %d, want its onset (line %d, the volume gate): "+
					"the baseline still remembers too much of the chatty phase", k, f.sc.cfg.BurstMinCount)
			}
			return
		}
	}
	t.Fatal("the incident never burst: a template that was chatty for two hours and quiet " +
		"for one can never burst again (the lifetime baseline never forgets)")
}

// TestSameRateResumptionStaysQuietPastTheCacheTTL: a template at 3/s for two hours goes
// silent for an hour and resumes at the SAME rate. Nothing changed but a pause, and a
// pause no longer than the explanation cache's TTL — the existing rule for when the same
// template is worth explaining afresh — must not read as a burst. This is the policy edge
// of the half-life, not a measured one: it holds only while the half-life is at least
// ~26 minutes.
func TestSameRateResumptionStaysQuietPastTheCacheTTL(t *testing.T) {
	f := newFixture(t, Defaults(), nil)
	l := line("replicated segment <NUM>")

	last := steady(f, l, t0, 3, 2*time.Hour)
	resume := last.Add(f.sc.cfg.CacheTTL)
	for i := range 180 { // 3/s for 60 s
		r := f.feed(l, resume.Add(time.Duration(i)*time.Second/3))
		if hasReason(r.Reasons, "burst:") {
			t.Fatalf("resumed line %d read as a burst after a %s pause: %v", i+1, f.sc.cfg.CacheTTL, r.Reasons)
		}
	}
}

// TestClusterIsNotAbsorbedIntoItsOwnBaseline is the GLib shape from two real boots: a
// handful of lines, half an hour of silence, then exactly 25 in ten seconds against a
// near-zero baseline. Today that escalates at the 25th line, and a baseline that forgets
// must not change it: if the half-life were short enough for the cluster to dominate its
// own baseline, the ratio would collapse before the volume gate opened and a burst that
// fires today would go silent.
func TestClusterIsNotAbsorbedIntoItsOwnBaseline(t *testing.T) {
	f := newFixture(t, Defaults(), nil)
	l := line("GFileInfo created without standard::is-hidden")

	f.repeat(l, t0, 10, 30*time.Second)
	start := t0.Add(30 * time.Minute)
	for k := 1; k <= 25; k++ {
		r := f.feed(l, start.Add(time.Duration(k-1)*400*time.Millisecond))
		if k < 25 && r.Escalate {
			t.Fatalf("cluster line %d escalated before the volume gate: %v", k, r.Reasons)
		}
		if k == 25 && (!r.Escalate || !hasReason(r.Reasons, "burst:")) {
			t.Fatalf("the 25th cluster line did not burst: escalate=%v reasons=%v", r.Escalate, r.Reasons)
		}
	}
}
