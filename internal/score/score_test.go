// SPDX-License-Identifier: Apache-2.0

package score

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// --- Novelty ---------------------------------------------------------------------

// TestNovelTemplateFiresOnceThenGoesQuiet is the core promise: an unseen template is
// the strongest signal there is, and the second occurrence of it is not news.
func TestNovelTemplateFiresOnceThenGoesQuiet(t *testing.T) {
	f := newFixture(t, liveConfig(), nil)

	first := f.feed(line("connection to shard 7 refused"), t0)
	if !first.Escalate {
		t.Fatalf("a novel template did not escalate: score %.2f, reasons %v", first.Score, first.Reasons)
	}
	if !hasReason(first.Reasons, "novel template") {
		t.Errorf("reasons = %v, want a novelty reason", first.Reasons)
	}

	second := f.feed(line("connection to shard 7 refused"), t0.Add(time.Second))
	if second.Escalate {
		t.Errorf("the same template escalated twice: score %.2f, reasons %v", second.Score, second.Reasons)
	}
}

// TestNoveltyRefiresAfterCooloff: a template that has been gone longer than the
// cooloff is news again — provided the explanation of it has also expired. The cache
// TTL is shortened here to isolate the novelty signal; the interaction between the
// two is TestExplainedTemplateStaysQuietPastCooloff.
func TestNoveltyRefiresAfterCooloff(t *testing.T) {
	cfg := liveConfig()
	cfg.Cooloff = 15 * time.Minute
	cfg.CacheTTL = time.Minute
	f := newFixture(t, cfg, nil)

	if res := f.feed(line("cache miss for user <NUM>"), t0); !res.Escalate {
		t.Fatal("first occurrence did not escalate")
	}

	// Still within the cooloff: seen recently, so not novel.
	within := f.feed(line("cache miss for user <NUM>"), t0.Add(10*time.Minute))
	if within.Escalate {
		t.Errorf("re-fired inside the cooloff: score %.2f, reasons %v", within.Score, within.Reasons)
	}

	// Gone for longer than the cooloff: novel again.
	after := f.feed(line("cache miss for user <NUM>"), t0.Add(10*time.Minute).Add(cfg.Cooloff+time.Second))
	if !after.Escalate {
		t.Fatalf("did not re-fire after the cooloff elapsed: score %.2f, reasons %v", after.Score, after.Reasons)
	}
	if !hasReason(after.Reasons, "novel template (unseen for") {
		t.Errorf("reasons = %v, want the cooloff novelty reason", after.Reasons)
	}
}

// TestExplainedTemplateStaysQuietPastCooloff locks in a deliberate interaction: with
// the defaults the cache (1h) outlives the cooloff (15m), so an error that has already
// been explained and comes back half an hour later does NOT interrupt the user again.
// The explanation has not changed, so there is nothing new to say.
func TestExplainedTemplateStaysQuietPastCooloff(t *testing.T) {
	f := newFixture(t, liveConfig(), nil) // Defaults: cooloff 15m, cache TTL 1h

	if res := f.feed(line("disk almost full"), t0); !res.Escalate {
		t.Fatal("first occurrence did not escalate")
	}

	back := f.feed(line("disk almost full"), t0.Add(30*time.Minute)) // novel again, but explained
	if back.Escalate {
		t.Errorf("an already-explained template re-escalated inside the cache TTL: reasons %v", back.Reasons)
	}
	if got := f.sc.Stats().Cached; got != 1 {
		t.Errorf("Stats().Cached = %d, want 1 (the cache is what kept it quiet)", got)
	}
}

// --- Warmup ----------------------------------------------------------------------

// TestStartupFloodIsImpossible is the test that keeps the tool installable. In the
// first seconds every template is novel; without warmup, starting logscry would
// escalate hundreds of perfectly routine lines at once and the user would never start
// it again.
func TestStartupFloodIsImpossible(t *testing.T) {
	f := newFixture(t, Defaults(), nil) // warmup 30s / 100 lines

	// 200 distinct, never-before-seen templates arriving in two seconds — exactly what
	// attaching to a running container replays.
	for i := range 200 {
		res := f.feed(line("startup template "+strconv.Itoa(i)), t0.Add(time.Duration(i)*10*time.Millisecond))
		if res.Escalate {
			t.Fatalf("template %d escalated during warmup: score %.2f, reasons %v", i, res.Score, res.Reasons)
		}
	}
	if got := f.sc.Stats().Escalated; got != 0 {
		t.Fatalf("Stats().Escalated = %d during warmup, want 0", got)
	}

	// Past warmup — both guards satisfied: >30s elapsed, >100 lines seen — a genuinely
	// new template is now news.
	res := f.feed(line("shard 7 is unreachable"), t0.Add(31*time.Second))
	if !res.Escalate {
		t.Errorf("a novel template after warmup did not escalate: score %.2f, reasons %v", res.Score, res.Reasons)
	}
}

// TestWarmupNeedsBothGuards: time alone is not enough (a low-volume service has shown
// us almost nothing in 30s), and volume alone is not enough (a backlog arrives in
// milliseconds).
func TestWarmupNeedsBothGuards(t *testing.T) {
	t.Run("enough time, too few lines", func(t *testing.T) {
		f := newFixture(t, Defaults(), nil)
		f.feed(line("first line"), t0)
		// A minute later, but only the second line this process has ever emitted.
		if res := f.feed(line("a brand new template"), t0.Add(time.Minute)); res.Escalate {
			t.Errorf("escalated with only %d lines of history: reasons %v", 2, res.Reasons)
		}
	})

	t.Run("enough lines, too little time", func(t *testing.T) {
		f := newFixture(t, Defaults(), nil)
		for i := range 150 {
			f.feed(line("chatty template "+strconv.Itoa(i%5)), t0.Add(time.Duration(i)*time.Millisecond))
		}
		if res := f.feed(line("a brand new template"), t0.Add(200*time.Millisecond)); res.Escalate {
			t.Errorf("escalated 200ms into the run: reasons %v", res.Reasons)
		}
	})
}

// TestFatalEscalatesDuringWarmup: warmup mutes novelty, not severity. A process that
// panics two seconds after start is exactly what the user wants to hear about.
func TestFatalEscalatesDuringWarmup(t *testing.T) {
	f := newFixture(t, Defaults(), nil)

	res := f.feed(level(line("runtime error: index out of range"), "PANIC"), t0.Add(2*time.Second))
	if !res.Escalate {
		t.Fatalf("a PANIC during warmup did not escalate: score %.2f, reasons %v", res.Score, res.Reasons)
	}
	if hasReason(res.Reasons, "novel template") {
		t.Errorf("novelty fired during warmup: reasons %v", res.Reasons)
	}
}

// --- Burst -----------------------------------------------------------------------

// establishBaseline feeds a template often enough and long enough to earn a baseline
// (defaults: 60s old, 20 occurrences), returning the time of the last occurrence. It
// returns after the template is no longer novel, so burst can be tested on its own.
func establishBaseline(f *fixture, msg string, every time.Duration, n int) time.Time {
	return f.repeat(line(msg), t0, n, every)
}

func TestSteadyTrafficDoesNotBurst(t *testing.T) {
	f := newFixture(t, liveConfig(), nil)

	// One a second for five minutes: a well-established, perfectly boring template.
	last := establishBaseline(f, "request served", time.Second, 300)

	res := f.feed(line("request served"), last.Add(time.Second))
	if res.Escalate || hasReason(res.Reasons, "burst") {
		t.Errorf("steady traffic burst: score %.2f, reasons %v", res.Score, res.Reasons)
	}
}

func TestGenuineSpikeBursts(t *testing.T) {
	// The template's very first occurrence is novel, and is escalated and cached as
	// such. A short cache TTL lets that first explanation expire, so what this test
	// observes is the burst signal on its own rather than the cache suppressing it.
	cfg := liveConfig()
	cfg.CacheTTL = time.Second
	f := newFixture(t, cfg, nil)

	// Baseline: one every 5s for 150s — old enough and frequent enough to count.
	last := establishBaseline(f, "db timeout", 5*time.Second, 30)

	// Then the same template 30 times in 3 seconds. Somewhere in there it is a burst.
	var burst Result
	for i := range 30 {
		at := last.Add(time.Duration(i) * 100 * time.Millisecond)
		if res := f.feed(line("db timeout"), at); hasReason(res.Reasons, "burst") {
			burst = res
			break
		}
	}
	if !hasReason(burst.Reasons, "burst") {
		t.Fatal("a 10x spike over the baseline never fired the burst signal")
	}
	if !burst.Escalate {
		t.Errorf("burst did not escalate: score %.2f, reasons %v", burst.Score, burst.Reasons)
	}
}

// TestNewChattyTemplateDoesNotBurst: a template with no history has no baseline, and
// without a baseline there is nothing to spike against. This is what stops a new,
// naturally chatty template from being reported as both novel AND bursting — one fact
// counted twice.
func TestNewChattyTemplateDoesNotBurst(t *testing.T) {
	f := newFixture(t, liveConfig(), nil)

	// 100 occurrences inside a second, far past the absolute floor of 50 — but the
	// template is one second old, so it has no baseline.
	for i := range 100 {
		res := f.feed(line("very chatty new line"), t0.Add(time.Duration(i)*10*time.Millisecond))
		if hasReason(res.Reasons, "burst") {
			t.Fatalf("occurrence %d of a one-second-old template burst: reasons %v", i, res.Reasons)
		}
	}
}

// TestRareTemplateDoesNotBurstOnTwoOccurrences: a template that shows up once a minute
// is, on its second occurrence in a 10s window, technically at many times its baseline
// rate. It is not a burst — it is two log lines. The burst-min-count floor is what says
// so.
func TestRareTemplateDoesNotBurstOnTwoOccurrences(t *testing.T) {
	f := newFixture(t, liveConfig(), nil)

	last := establishBaseline(f, "hourly job tick", time.Minute, 25) // baseline ~0.017/s

	res := f.feed(line("hourly job tick"), last.Add(2*time.Second)) // 2 in the window
	if hasReason(res.Reasons, "burst") {
		t.Errorf("two occurrences counted as a burst: reasons %v", res.Reasons)
	}
}

// --- Severity ---------------------------------------------------------------------

// TestSeverityAloneDoesNotEscalate is the cry-wolf guard, and the most important
// assertion in the file. Plenty of well-behaved programs log routine chatter to
// stderr and routine failures at ERROR. On a template we have seen before, neither is
// worth interrupting anyone for — not even both together, which the default weights
// put at 0.9 against a threshold of 1.0.
func TestSeverityAloneDoesNotEscalate(t *testing.T) {
	cases := []struct {
		name      string
		build     func(model.LogLine) model.LogLine
		wantScore float64
	}{
		{"stderr", stderr, 0.3},
		{"WARN", func(l model.LogLine) model.LogLine { return level(l, "WARN") }, 0.2},
		{"ERROR", func(l model.LogLine) model.LogLine { return level(l, "ERROR") }, 0.6},
		{"CRITICAL", func(l model.LogLine) model.LogLine { return level(l, "CRITICAL") }, 0.6},
		{"stderr + ERROR", func(l model.LogLine) model.LogLine { return stderr(level(l, "ERROR")) }, 0.9},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, liveConfig(), nil)

			// Second occurrence, so novelty is out of the picture and severity is the
			// only signal in play.
			f.feed(c.build(line("routine failure")), t0)
			res := f.feed(c.build(line("routine failure")), t0.Add(time.Second))

			if res.Escalate {
				t.Errorf("%s alone escalated: score %.2f, reasons %v", c.name, res.Score, res.Reasons)
			}
			if math.Abs(res.Score-c.wantScore) > 1e-9 {
				t.Errorf("score = %.2f, want %.2f", res.Score, c.wantScore)
			}
		})
	}
}

// TestSeverityRaisesTheScore: severity does not trigger on its own, but it is not
// decoration either — it pushes a borderline event over the line and it explains why.
func TestSeverityRaisesTheScore(t *testing.T) {
	f := newFixture(t, liveConfig(), nil)

	res := f.feed(stderr(level(line("shard 7 is unreachable"), "ERROR")), t0)
	if !res.Escalate {
		t.Fatalf("novel + stderr + ERROR did not escalate: score %.2f", res.Score)
	}
	if want := 1.0 + 0.3 + 0.6; math.Abs(res.Score-want) > 1e-9 {
		t.Errorf("score = %.2f, want %.2f (novelty + stderr + ERROR)", res.Score, want)
	}
	for _, want := range []string{"novel template", "stderr", "level ERROR"} {
		if !hasReason(res.Reasons, want) {
			t.Errorf("reasons = %v, want one starting %q", res.Reasons, want)
		}
	}
}

// TestFatalEscalatesAlone: a crash is the one severity worth interrupting for by
// itself, even on a template that has been seen before.
func TestFatalEscalatesAlone(t *testing.T) {
	for _, lv := range []string{"FATAL", "PANIC"} {
		t.Run(lv, func(t *testing.T) {
			cfg := liveConfig()
			cfg.CacheTTL = 0 // so the cache does not mask the second occurrence
			f := newFixture(t, cfg, nil)

			f.feed(level(line("the sky is falling"), lv), t0)
			res := f.feed(level(line("the sky is falling"), lv), t0.Add(time.Second))

			if !res.Escalate {
				t.Errorf("%s did not escalate on its own: score %.2f, reasons %v", lv, res.Score, res.Reasons)
			}
		})
	}
}

// TestSeverityIsCapped: stderr + FATAL is 1.3 of severity before the cap, and the cap
// exists so that no pile-up of severity can substitute for an actual anomaly signal.
func TestSeverityIsCapped(t *testing.T) {
	cfg := liveConfig()
	cfg.CacheTTL = 0
	f := newFixture(t, cfg, nil)

	f.feed(stderr(level(line("fatal: out of memory"), "FATAL")), t0)
	res := f.feed(stderr(level(line("fatal: out of memory"), "FATAL")), t0.Add(time.Second))

	if want := cfg.Weights.SeverityMax; math.Abs(res.Score-want) > 1e-9 {
		t.Errorf("score = %.2f, want the severity cap %.2f", res.Score, want)
	}
}

// --- Escalation channel ------------------------------------------------------------

// TestEscalationChannelFullDropsAndCounts: the LLM stage must never be able to stall
// ingestion. When the outbound queue is full the request is dropped and counted —
// the pipeline keeps moving. (That this test terminates at all is half the assertion:
// a blocking send would hang it.)
func TestEscalationChannelFullDropsAndCounts(t *testing.T) {
	out := make(chan EscalationRequest, 1) // capacity 1, and nobody is reading
	cfg := liveConfig()
	cfg.RatePerMin = 100 // let the rate limiter out of the way; this is about the queue
	f := newFixture(t, cfg, out)

	const n = 5
	for i := range n {
		res := f.feed(line("novel failure "+strconv.Itoa(i)), t0.Add(time.Duration(i)*time.Second))
		if !res.Escalate {
			t.Fatalf("line %d did not escalate: reasons %v", i, res.Reasons)
		}
	}

	stats := f.sc.Stats()
	if stats.Escalated != n {
		t.Errorf("Escalated = %d, want %d", stats.Escalated, n)
	}
	if stats.Dropped != n-1 {
		t.Errorf("Dropped = %d, want %d (queue holds one)", stats.Dropped, n-1)
	}
}

// TestEscalationRequestCarriesContext: the trigger line rarely explains itself, so an
// escalation carries what came before it — and not the trigger itself, which travels
// separately.
func TestEscalationRequestCarriesContext(t *testing.T) {
	// Roomy enough to hold both escalations: the chatter's own first occurrence is
	// novel too, and this test is about the second one.
	out := make(chan EscalationRequest, 8)
	cfg := liveConfig()
	cfg.ContextLines = 5
	f := newFixture(t, cfg, out)

	for i := range 20 {
		f.feed(line("routine chatter"), t0.Add(time.Duration(i)*time.Second))
	}
	f.feed(line("everything is broken"), t0.Add(30*time.Second))

	var req EscalationRequest
	select {
	case req = <-out:
	default:
		t.Fatal("nothing was emitted on the escalation channel")
	}
	// Drain to the escalation we care about: the fault, which is the last one.
	for len(out) > 0 {
		req = <-out
	}

	if req.Trigger.Message != "everything is broken" {
		t.Fatalf("Trigger = %q, want the line that fired", req.Trigger.Message)
	}
	if req.Pattern != "everything is broken" {
		t.Errorf("Pattern = %q, want the trigger's template", req.Pattern)
	}
	if len(req.Context) != 5 {
		t.Fatalf("Context has %d lines, want 5 (bounded)", len(req.Context))
	}
	for _, c := range req.Context {
		if strings.Contains(c, "everything is broken") {
			t.Error("the trigger line leaked into its own context")
		}
		if !strings.Contains(c, "routine chatter") {
			t.Errorf("context line = %q, want the preceding chatter", c)
		}
	}
	if req.Score < cfg.Threshold {
		t.Errorf("Score = %.2f, want >= the threshold", req.Score)
	}
	if len(req.Reasons) == 0 {
		t.Error("Reasons is empty; an escalation must say why it fired")
	}
}

// TestNilChannelStillDecidesAndCounts: with no LLM wired up (M3's own default), the
// scorer still makes and counts every decision — that is what --explain-dry-run shows.
func TestNilChannelStillDecidesAndCounts(t *testing.T) {
	f := newFixture(t, liveConfig(), nil)

	res := f.feed(line("something novel"), t0)
	if !res.Escalate {
		t.Fatal("did not escalate with a nil out channel")
	}
	stats := f.sc.Stats()
	if stats.Escalated != 1 || stats.Dropped != 0 {
		t.Errorf("stats = %+v, want 1 escalated and 0 dropped", stats)
	}
}

// --- Config ------------------------------------------------------------------------

// TestValidateRejectsUnworkableConfigs: a config that could never fire is worse than
// one that fires too much, because the user only discovers it by never being told
// anything. Failing loudly beats being quiet for the wrong reason.
func TestValidateRejectsUnworkableConfigs(t *testing.T) {
	cfg := Defaults()
	cfg.BurstMinCount = model.RecentCap + 1
	if err := cfg.Validate(); err == nil {
		t.Error("Validate accepted a burst volume gate the ring can never reach")
	}

	cfg = Defaults()
	cfg.Threshold = 0
	if err := cfg.Validate(); err == nil {
		t.Error("Validate accepted a zero threshold, which would escalate everything")
	}

	// At or below 1x, a template running at its own steady baseline is a "burst" —
	// which is precisely the cry-wolf failure the relative signal exists to avoid.
	cfg = Defaults()
	cfg.BurstMultiplier = 1
	if err := cfg.Validate(); err == nil {
		t.Error("Validate accepted a 1x burst multiplier, which would fire on steady traffic")
	}
}
