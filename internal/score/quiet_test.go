// SPDX-License-Identifier: Apache-2.0

package score

import (
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// routine is a plausible steady-state log: a handful of repeating templates at a
// steady rate, including the two kinds of noise that a naive scorer would cry wolf
// over — routine chatter on stderr, and routine failures logged at ERROR.
//
// The last entry is the loudest a *routine* line can be under the default weights:
// stderr + ERROR = 0.9, deliberately one tenth below the threshold.
var routine = []model.LogLine{
	{Source: "svc:api", Message: "GET /healthz 200 in <NUM>ms", Level: "INFO"},
	{Source: "svc:api", Message: "GET /api/users 200 in <NUM>ms", Level: "INFO"},
	{Source: "svc:api", Message: "cache hit for key <STR>", Level: "DEBUG"},
	{Source: "svc:db", Message: "connection pool reused", Stream: model.Stderr},
	{Source: "svc:db", Message: "retrying upstream call", Level: "WARN", Stream: model.Stderr},
	{Source: "svc:api", Message: "failed to fetch avatar for user <NUM>", Level: "ERROR"},
	{Source: "svc:db", Message: "upstream timeout, retrying", Level: "ERROR", Stream: model.Stderr},
}

// feedRoutine plays count lines of routine traffic at ten lines a second, round-robin
// over the templates, and returns the time of the last one.
func feedRoutine(f *fixture, count int) time.Time {
	const every = 100 * time.Millisecond
	at := t0
	for i := range count {
		at = t0.Add(time.Duration(i) * every)
		l := routine[i%len(routine)]
		l.Raw = l.Message
		f.feed(l, at)
	}
	return at
}

// TestQuietSystemStaysSilent is the property that defines the product. Five minutes of
// entirely ordinary traffic — three thousand lines, including stderr chatter and
// recurring ERRORs — must produce ZERO escalations.
//
// If this test ever fails, the tool is broken, no matter what else passes: a triage
// tool that interrupts you about healthy traffic gets uninstalled the same day.
func TestQuietSystemStaysSilent(t *testing.T) {
	f := newFixture(t, Defaults(), nil)

	feedRoutine(f, 3000)

	stats := f.sc.Stats()
	if stats.Escalated != 0 {
		t.Errorf("Escalated = %d on a healthy system, want 0", stats.Escalated)
	}
	// Nothing may even have *reached* the threshold. Had the gates been what silenced
	// it, the tool would only be quiet by luck: raise the rate limit or clear the cache
	// and it would start crying wolf.
	if stats.Suppressed != 0 || stats.Cached != 0 {
		t.Errorf("stats = %+v: routine traffic crossed the threshold and was only held back "+
			"by the rate limiter or the cache, not by the score", stats)
	}
}

// TestQuietSystemStillSpeaksOnSignal keeps the test above honest. A scorer that never
// escalates anything would also pass it — so here the same routine traffic gets a
// genuine fault dropped into the middle of it, and the scorer must notice exactly that
// one thing, once.
func TestQuietSystemStillSpeaksOnSignal(t *testing.T) {
	f := newFixture(t, Defaults(), nil)

	last := feedRoutine(f, 3000) // warmup is long over by now

	fault := model.LogLine{
		Source: "svc:db", Stream: model.Stderr, Level: "ERROR",
		Message: "shard 7: no route to host",
		Raw:     "shard 7: no route to host",
	}
	res := f.feed(fault, last.Add(100*time.Millisecond))
	if !res.Escalate {
		t.Fatalf("a novel error in a healthy system was not escalated: score %.2f, reasons %v",
			res.Score, res.Reasons)
	}

	// And it says so once. The same fault repeating is the same fault.
	for i := range 50 {
		f.feed(fault, last.Add(time.Duration(200+i*100)*time.Millisecond))
	}
	if got := f.sc.Stats().Escalated; got != 1 {
		t.Errorf("Escalated = %d, want 1: a recurring fault must be explained once, not %d times", got, got)
	}
}
