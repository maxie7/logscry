// SPDX-License-Identifier: Apache-2.0

package score

import (
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// This file pins the burst volume gate: the minimum number of occurrences inside the
// window below which no ratio, however large, is called a burst.
//
// It exists because of issue #32. Six hours of a real laptop's journal produced 32
// escalations, sixteen of them burst-driven and not one an incident: GNOME compositor
// warnings, container/docker/init.scope churn, and vpnagentd interface and DNS events.
// Every one of the sixteen was a cluster of ten or sixteen lines against a lifetime
// baseline near zero, which the ratio turns into a multiplier in the hundreds.
//
// A NOTE ON WHAT THE TWO GROUPS BELOW ARE WORTH. The ratio gate passes in every one of
// the recorded rows — the smallest observed ratio is 20x. Baseline and rate therefore
// change nothing about the outcome, and only the window count is load-bearing. The
// eleven negative rows are ONE ASSERTION REPEATED ELEVEN TIMES. They are kept for
// documentation: the source names are the point, so that whoever next moves this
// constant knows which real systems it was set against. They are not eleven independent
// proofs and their count must not be read as coverage.
//
// TestFloodFloorIsExactlyPinned carries the entire real burden. A gate that suppresses
// noise is trivially satisfiable by a gate that suppresses everything, and the failure
// that mode produces — silence — is indistinguishable from success on a healthy machine.

// noiseRow replays one recorded false positive: a template with an established baseline
// of baseRate occurrences/sec, then count occurrences clustered inside a single burst
// window. It returns the Result of every line in the cluster.
//
// The baseline is built from 120 occurrences, comfortably past BaselineMinCount, spread
// at 1/baseRate apart. Every gap stays under the cooloff, so novelty never enters and
// what the cluster measures is the burst signal alone.
func noiseRow(f *fixture, l model.LogLine, baseRate float64, count int) []Result {
	f.t.Helper()

	step := time.Duration(float64(time.Second) / baseRate)
	if step > f.sc.cfg.Cooloff {
		f.t.Fatalf("baseline step %s exceeds the cooloff %s: this row would score as novel, "+
			"not as a burst", step, f.sc.cfg.Cooloff)
	}
	last := f.repeat(l, t0, 120, step)

	// A gap longer than the burst window before the cluster starts, so the window holds
	// cluster lines and nothing else and the occurrence count the scorer sees is exactly
	// the count this row claims. Without it the last baseline occurrence is still inside
	// the window and a 16-line cluster measures as 17 — the row would then be testing a
	// number it does not name, and the gate it pins would be off by one.
	start := last.Add(f.sc.cfg.BurstWindow + time.Second)

	// The cluster: ten lines in a second is what a CI job starting ten containers looks
	// like on the wire.
	out := make([]Result, 0, count)
	for i := range count {
		out = append(out, f.feed(l, start.Add(time.Duration(i)*100*time.Millisecond)))
	}
	return out
}

// TestRecordedNoiseDoesNotBurst replays the sixteen false-positive escalations from the
// six-hour run in issue #32. None of them may fire.
//
// Each row is named after the real source it came from, taken from the export's source
// field. That naming is the reason the table is this long — see the file comment: these
// rows are documentation of provenance, not independent coverage.
func TestRecordedNoiseDoesNotBurst(t *testing.T) {
	cases := []struct {
		name string // source: what actually emitted this
		msg  string
		lvl  string
		base float64 // established baseline, occurrences/sec
		n    int     // occurrences clustered inside one burst window
	}{
		// GNOME compositor redraws. Near-zero baseline, arrives in clumps: the highest
		// ratios in the whole run, and the only rows with a cluster above ten.
		{"gnome compositor: stage views actor", "Can't update stage views actor <STR>", "WARN", 0.005, 16},
		{"gnome compositor: negative content width", "Negative content width <NUM>", "WARN", 0.004, 10},

		// A CI runner starting and tearing down containers. Ten containers per job, one
		// line each, inside a second.
		{"container churn: libcontainer started", "Started libcontainer container <HEX>", "", 0.01, 10},
		{"container churn: containerd namespace moby", "address=<STR> namespace=moby", "", 0.01, 10},
		{"container churn: libcontainerd tasks/delete", "module=libcontainerd tasks/delete <HEX>", "", 0.01, 10},
		{"container churn: docker scope deactivated", "docker-<HEX>.scope Deactivated successfully", "", 0.01, 10},
		{"container churn: containerd id namespace", "id=<HEX> namespace=moby", "", 0.01, 10},

		// The VPN client reacting to those same containers appearing and disappearing:
		// every veth pair is a network interface event.
		{"vpn interface: new interface detected", "A new network interface detected", "INFO", 0.01, 10},
		{"vpn interface: interface gone down", "A network interface has gone down", "INFO", 0.01, 10},

		// And the VPN client polling DNS on a fixed interval, which is why its baseline
		// is the highest here and its ratio the lowest.
		{"vpn dns: GetDNSConfig", "GetDNSConfig <STR>", "WARN", 0.01, 10},
		{"vpn dns: getDnsConfiguration", "getDnsConfiguration <STR>", "WARN", 0.05, 10},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, matrixConfig(), nil)

			l := line(c.msg)
			if c.lvl != "" {
				l = level(l, c.lvl)
			}

			for i, res := range noiseRow(f, l, c.base, c.n) {
				if hasReason(res.Reasons, "burst") {
					t.Fatalf("occurrence %d of %d called a burst: score %.2f, reasons %v; "+
						"%d lines in a window is a cluster, not a flood — an event-driven system "+
						"logs in clusters by construction",
						i+1, c.n, res.Score, res.Reasons, c.n)
				}
				if res.Escalate {
					t.Fatalf("occurrence %d of %d escalated: score %.2f, reasons %v",
						i+1, c.n, res.Score, res.Reasons)
				}
			}
		})
	}
}

// TestFloodFloorIsExactlyPinned is the load-bearing test in this file, and it is
// two-sided on purpose.
//
// The failure mode a volume gate invites is a gate set so high it can never be reached
// on a real machine. That failure produces silence, and silence on a healthy host is
// exactly what success looks like — nobody would notice for months. So this test pins
// BOTH edges: one occurrence below the gate must stay quiet, and the occurrence that
// reaches it must fire. Raise the constant and the second half fails; lower it and the
// first half fails. A scorer that never bursts cannot satisfy it.
//
// Both edges are derived from the config actually handed to the scorer, never from a
// literal and never from Defaults(): if this file's config ever overrode BurstMinCount,
// a Defaults()-derived pin would silently stop testing the behaviour under test, which
// is the very decoupling the derivation exists to prevent.
func TestFloodFloorIsExactlyPinned(t *testing.T) {
	cfg := matrixConfig()
	min := cfg.BurstMinCount
	f := newFixture(t, cfg, nil)

	l := line("upstream connect error")

	// A slow, well-established baseline: 120 occurrences one every ten seconds. Old
	// enough and frequent enough to earn a baseline (~0.1/s), and far enough under the
	// gate that the ratio is never what decides anything below.
	last := f.repeat(l, t0, 120, 10*time.Second)

	// A gap longer than the burst window, so the window holds only flood lines and the
	// occurrence count is exactly the number fed. Well under the cooloff, so the flood
	// is a burst and not a novelty.
	start := last.Add(cfg.BurstWindow + time.Second)

	// One short of the gate. The ratio gate is already satisfied here — see the
	// assertion below — so the volume gate is the only thing keeping this quiet.
	step := 250 * time.Millisecond
	for i := range min - 1 {
		res := f.feed(l, start.Add(time.Duration(i)*step))
		if hasReason(res.Reasons, "burst") {
			t.Fatalf("occurrence %d fired with only %d in the window, below the gate of %d: "+
				"reasons %v", i+1, i+1, min, res.Reasons)
		}
	}

	// And the one that reaches it.
	res := f.feed(l, start.Add(time.Duration(min-1)*step))
	if !hasReason(res.Reasons, "burst") {
		t.Fatalf("%d occurrences in %s at ~%.1f/s over a %.2f/s baseline did not fire the burst "+
			"signal: score %.2f, reasons %v. The volume gate is set beyond what a real flood "+
			"reaches, which makes burst permanently silent — and silence looks like success.",
			min, cfg.BurstWindow, float64(min)/cfg.BurstWindow.Seconds(), 0.1, res.Score, res.Reasons)
	}
	if !res.Escalate {
		t.Errorf("the burst did not escalate: score %.2f, reasons %v", res.Score, res.Reasons)
	}
	if hasReason(res.Reasons, "novel template") {
		t.Errorf("the flood was also scored as novel: reasons %v", res.Reasons)
	}
}

// TestRetryStormEscalates gives the constant a shape a person can argue with: a service
// that normally logs one retry a minute enters a retry loop at six a second. That is the
// incident the burst signal exists for, and it must still be reported.
//
// The line is deliberately unremarkable — INFO, on stdout, carrying no severity weight
// at all. That is the case only burst can catch, and the reason burst keeps its weight
// of 1.0 rather than being demoted to a booster: demote it and this storm scores 0.45
// and the tool says nothing while the service is failing.
//
// The cache is left ON here, unlike the rest of this file: a retry storm is one event,
// and the tool must say so once rather than once per line.
func TestRetryStormEscalates(t *testing.T) {
	cfg := liveConfig() // cache and rate limiter as shipped
	f := newFixture(t, cfg, nil)

	l := level(line("retrying connection to upstream <IP>, attempt <NUM>"), "INFO")

	// Half an hour of the occasional retry: one a minute. Routine, and silent.
	last := f.repeat(l, t0, 30, time.Minute)
	if got := f.sc.Stats().Escalated; got != 0 {
		t.Fatalf("Escalated = %d during the routine one-a-minute baseline, want 0", got)
	}

	// Then the upstream goes away and the client retries in a loop: six a second for
	// half a minute.
	var first Result
	for i := range 180 {
		res := f.feed(l, last.Add(time.Duration(i+1)*time.Second/6))
		if first.Score == 0 && res.Escalate {
			first = res
		}
	}

	if first.Score == 0 {
		t.Fatal("a retry storm at 6/s over a 0.017/s baseline never escalated")
	}
	if !hasReason(first.Reasons, "burst") {
		t.Errorf("the retry storm escalated for the wrong reason: %v; want the burst, since the "+
			"template is neither new nor fatal", first.Reasons)
	}
	if got := f.sc.Stats().Escalated; got != 1 {
		t.Errorf("Escalated = %d, want 1: a retry storm is one event and gets explained once", got)
	}
}
