// SPDX-License-Identifier: Apache-2.0

package score

import (
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// TestNoveltyAloneNeverEscalates is the regression test for the calibration bug this
// package's novelty weight was fixed for: four hours of a real laptop's journal
// produced 179 escalations in 2304 lines, almost every one of them "novel template
// (first seen) · score 1.00".
//
// The rule it encodes: a template that is merely NEW is not news. A working host emits
// hundreds of distinct-but-benign templates over hours — browser internals,
// NetworkManager, cron, docker veth churn, the kernel — each novel exactly once. If
// every one of them interrupts the user, the tool is uninstalled the same day.
func TestNoveltyAloneNeverEscalates(t *testing.T) {
	f := newFixture(t, liveConfig(), nil) // warmup off: novelty is live from the first line

	// 300 distinct, benign, info-level templates, spread out so nothing can burst.
	const n = 300
	for i := range n {
		res := f.feed(line("benign template "+strconv.Itoa(i)), t0.Add(time.Duration(i)*time.Second))

		if res.Escalate {
			t.Fatalf("benign novel template %d escalated: score %.2f, reasons %v", i, res.Score, res.Reasons)
		}
		if want := Defaults().Weights.Novelty; math.Abs(res.Score-want) > 1e-9 {
			t.Fatalf("template %d scored %.2f, want %.2f: novelty is the only signal in play",
				i, res.Score, want)
		}
		// The signal still FIRES — it is counted and shown, it just is not enough on its
		// own. A scorer that had simply stopped detecting novelty would also produce zero
		// escalations, and would be a different (worse) tool.
		if !hasReason(res.Reasons, "novel template") {
			t.Fatalf("template %d has reasons %v, want novelty to still be reported", i, res.Reasons)
		}
	}

	stats := f.sc.Stats()
	if stats.Escalated != 0 {
		t.Errorf("Escalated = %d on %d benign new templates, want 0", stats.Escalated, n)
	}
	// Nothing may even have REACHED the threshold. Had the gates been what silenced it,
	// the tool would be quiet only by luck: raise the rate limit or clear the cache and
	// it would start crying wolf again.
	if stats.Suppressed != 0 || stats.Cached != 0 {
		t.Errorf("stats = %+v: novel templates crossed the threshold and were only held back "+
			"by the rate limiter or the cache, not by the score", stats)
	}
}

// matrixConfig is the live config with the two gates out of the way, so a matrix row
// measures the SCORE and nothing else. The cache and the rate limiter have their own
// tests; here they would only mask what a weight is doing.
func matrixConfig() Config {
	cfg := liveConfig()
	cfg.CacheTTL = 0
	cfg.RatePerMin = 1000
	return cfg
}

// TestSignalMatrix pins the entire escalate/quiet policy in one table: every
// combination of novelty, burst and severity the weights can produce, with the exact
// score each one lands on.
//
// It exists so that a future weight change FAILS A TEST rather than silently shifting
// what the tool interrupts people about. That is the failure this table is defending
// against — the two calibration bugs this package has had (the absolute burst floor,
// and novelty as a threshold-crosser) were both invisible until someone ran the tool
// against a real system for hours.
func TestSignalMatrix(t *testing.T) {
	// one returns a run that feeds a single line as the very first thing the scorer
	// sees, which makes it novel by definition.
	one := func(build func(model.LogLine) model.LogLine) func(*fixture) Result {
		return func(f *fixture) Result { return f.feed(build(line("shard 7 unreachable")), t0) }
	}
	// repeated returns a run whose trigger is the SECOND occurrence, so novelty is out
	// of the picture and the row measures severity alone.
	repeated := func(build func(model.LogLine) model.LogLine) func(*fixture) Result {
		return func(f *fixture) Result {
			l := build(line("routine failure"))
			f.feed(l, t0)
			return f.feed(l, t0.Add(time.Second))
		}
	}

	plain := func(l model.LogLine) model.LogLine { return l }
	lvl := func(name string) func(model.LogLine) model.LogLine {
		return func(l model.LogLine) model.LogLine { return level(l, name) }
	}
	both := func(name string) func(model.LogLine) model.LogLine {
		return func(l model.LogLine) model.LogLine { return stderr(level(l, name)) }
	}

	cases := []struct {
		name  string
		run   func(*fixture) Result
		score float64
		fire  bool
		why   string
	}{
		{"novel alone", one(plain), 0.45, false,
			"a merely-new template is the steady state on a real host"},
		{"novel + stderr", one(stderr), 0.75, false,
			"plenty of programs write everything to stderr"},
		{"novel + WARN", one(lvl("WARN")), 0.65, false, ""},
		{"novel + WARN + stderr", one(both("WARN")), 0.95, false,
			"the loudest a merely-new line gets, and still under"},
		{"novel + ERROR", one(lvl("ERROR")), 1.05, true,
			"a new FAILURE is the signal novelty exists to catch"},
		{"novel + ERROR + stderr", one(both("ERROR")), 1.35, true, ""},
		{"novel + FATAL", one(lvl("FATAL")), 1.45, true, ""},
		{"FATAL alone, known template", repeated(lvl("FATAL")), 1.00, true,
			"a crash is worth interrupting for by itself"},
		{"stderr + ERROR, known template", repeated(both("ERROR")), 0.90, false,
			"routine error chatter must not escalate"},
		{"burst alone", runBurst, 1.00, true,
			"a CHANGE in rate, on a template that is neither new nor severe"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, matrixConfig(), nil)

			res := c.run(f)

			if math.Abs(res.Score-c.score) > 1e-9 {
				t.Errorf("score = %.2f, want %.2f", res.Score, c.score)
			}
			if res.Escalate != c.fire {
				t.Errorf("Escalate = %v, want %v (score %.2f, threshold %.2f, reasons %v)%s",
					res.Escalate, c.fire, res.Score, matrixConfig().Threshold, res.Reasons, note(c.why))
			}
		})
	}
}

// note formats a row's rationale for a failure message, or nothing when it has none.
func note(why string) string {
	if why == "" {
		return ""
	}
	return ": " + why
}

// runBurst drives the one matrix row that cannot be a single line: it establishes a
// baseline, then raises the rate until the burst signal fires, and returns the
// occurrence it fired on. That line is neither novel nor severe, so its score is the
// burst weight and nothing else.
func runBurst(f *fixture) Result {
	l := line("db timeout after <NUM>ms")

	// One every two seconds for ten minutes: old enough and frequent enough for a
	// baseline of roughly 0.5/s.
	last := f.repeat(l, t0, 300, 2*time.Second)

	// Then the same template ten times a second. Somewhere in there it is a burst.
	for i := range 60 {
		res := f.feed(l, last.Add(time.Duration(i+1)*100*time.Millisecond))
		if hasReason(res.Reasons, "burst") {
			return res
		}
	}
	f.t.Fatal("the rate went up 6x over the baseline and the burst signal never fired")
	return Result{}
}

// TestThresholdIsInclusive pins the comparison semantics RDI §6 specifies: escalate
// iff score >= threshold. The weights are calibrated against that "or equal" — a
// fatal-class line scores exactly 1.00 against a threshold of exactly 1.00, so a strict
// comparison here would silence crashes and nothing else would notice.
func TestThresholdIsInclusive(t *testing.T) {
	cfg := matrixConfig()
	f := newFixture(t, cfg, nil)

	// The second occurrence: novelty is out of the picture, leaving the fatal weight
	// alone against the threshold.
	f.feed(level(line("the sky is falling"), "FATAL"), t0)
	res := f.feed(level(line("the sky is falling"), "FATAL"), t0.Add(time.Second))

	// Deliberately an exact comparison: the whole point is that this lands ON the line.
	if res.Score != cfg.Threshold {
		t.Fatalf("score = %v, want exactly the threshold %v", res.Score, cfg.Threshold)
	}
	if !res.Escalate {
		t.Error("a score exactly equal to the threshold did not escalate: the comparison is strict, " +
			"but the weights assume it is inclusive")
	}
}

// TestNoveltyWeightIsTunable proves the booster behaviour lives in the default VALUE
// and not in logic that special-cases novelty. Someone who genuinely wants aggressive
// novelty escalation dials --weight-novelty back up to 1.0 and gets exactly the old
// behaviour: a plain, unremarkable first-seen template escalating on its own.
func TestNoveltyWeightIsTunable(t *testing.T) {
	cfg := liveConfig()
	cfg.Weights.Novelty = 1.0 // what --weight-novelty 1.0 produces
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a novelty weight at the threshold is a legitimate configuration: %v", err)
	}
	f := newFixture(t, cfg, nil)

	res := f.feed(line("a perfectly ordinary new template"), t0)

	if !res.Escalate {
		t.Errorf("with novelty dialled to 1.0 a novel template did not escalate: score %.2f, reasons %v; "+
			"the booster behaviour is hardcoded somewhere instead of living in the weight",
			res.Score, res.Reasons)
	}
	if want := 1.0; math.Abs(res.Score-want) > 1e-9 {
		t.Errorf("score = %.2f, want %.2f: the configured weight is not what got added", res.Score, want)
	}
}

// TestNoveltyAndBurstDoNotCoOccur documents a property of the design that predates the
// novelty recalibration and survives it: the two signals can never fire on the same
// line, so "novel AND bursting" is not a combination the weights have to account for.
//
// First-seen novelty needs Count == 1, and a baseline needs twenty occurrences over a
// minute — so a brand-new template cannot burst. Cooloff novelty needs a gap longer
// than the cooloff, and a burst needs ten occurrences inside ten seconds — so by the
// time the rate is up, the gap is long gone. A new template that later bursts escalates
// on the BURST, at the moment it bursts, which is the honest reason.
func TestNoveltyAndBurstDoNotCoOccur(t *testing.T) {
	cfg := matrixConfig()
	f := newFixture(t, cfg, nil)
	l := line("db timeout after <NUM>ms")

	last := f.repeat(l, t0, 300, 2*time.Second) // baseline ~0.5/s

	// Gone for longer than the cooloff, then back — and back at ten times the rate.
	back := last.Add(cfg.Cooloff + 5*time.Minute)

	first := f.feed(l, back)
	if !hasReason(first.Reasons, "novel template (unseen for") {
		t.Fatalf("the first line after the cooloff was not novel: reasons %v", first.Reasons)
	}
	if hasReason(first.Reasons, "burst") {
		t.Error("one occurrence after a silence counted as a burst")
	}
	if first.Escalate {
		t.Errorf("a novel-again template escalated on novelty alone: score %.2f", first.Score)
	}

	var burst Result
	for i := range 60 {
		res := f.feed(l, back.Add(time.Duration(i+1)*100*time.Millisecond))
		if hasReason(res.Reasons, "novel template") && hasReason(res.Reasons, "burst") {
			t.Fatalf("occurrence %d was scored as both novel and bursting — the same fact counted "+
				"twice: score %.2f, reasons %v", i, res.Score, res.Reasons)
		}
		if hasReason(res.Reasons, "burst") && burst.Score == 0 {
			burst = res
		}
	}

	if !hasReason(burst.Reasons, "burst") {
		t.Fatal("the rate went up and the burst signal never fired")
	}
	if !burst.Escalate {
		t.Errorf("the burst did not escalate: score %.2f, reasons %v", burst.Score, burst.Reasons)
	}
	if hasReason(burst.Reasons, "novel template") {
		t.Errorf("the bursting occurrence was also called novel: reasons %v", burst.Reasons)
	}
}
