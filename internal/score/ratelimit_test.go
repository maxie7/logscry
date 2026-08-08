// SPDX-License-Identifier: Apache-2.0

package score

import (
	"strconv"
	"testing"
	"time"
)

func TestRateLimiterExhaustsAndRefills(t *testing.T) {
	rl := NewRateLimiter(10) // 10/min: one token every 6s, bucket of 10

	// The bucket starts full, so the first ten calls go straight through.
	for i := range 10 {
		if !rl.Allow(t0) {
			t.Fatalf("call %d denied while the bucket should still be full", i)
		}
	}
	if rl.Allow(t0) {
		t.Fatal("the eleventh call was allowed: the bucket is not capping anything")
	}

	// Refill is continuous: six seconds buys exactly one token.
	if rl.Allow(t0.Add(3 * time.Second)) {
		t.Error("allowed a call after 3s, which is only half a token")
	}
	if !rl.Allow(t0.Add(6 * time.Second)) {
		t.Error("denied a call after 6s, which is a full token")
	}
	if rl.Allow(t0.Add(6 * time.Second)) {
		t.Error("that token was spent twice")
	}

	// A long quiet spell refills the bucket, but never past its capacity — an idle
	// hour must not buy the right to make sixty calls at once.
	for i := range 10 {
		if !rl.Allow(t0.Add(time.Hour)) {
			t.Fatalf("call %d denied after an idle hour", i)
		}
	}
	if rl.Allow(t0.Add(time.Hour)) {
		t.Error("the bucket refilled past its capacity")
	}
}

// TestRateLimiterZeroDeniesEverything: --rate-limit 0 is a legitimate "never call the
// LLM" configuration, not an accidental unlimited one.
func TestRateLimiterZeroDeniesEverything(t *testing.T) {
	rl := NewRateLimiter(0)
	if rl.Allow(t0) || rl.Allow(t0.Add(time.Hour)) {
		t.Error("a zero rate limit allowed a call")
	}
}

// TestRateLimiterIgnoresBackwardsTime: log lines carry their own timestamps, and those
// do not always arrive in order (RDI §4, gotcha #2). A clock that goes backwards must
// not drain the bucket.
func TestRateLimiterIgnoresBackwardsTime(t *testing.T) {
	rl := NewRateLimiter(10)
	if !rl.Allow(t0.Add(time.Minute)) {
		t.Fatal("first call denied")
	}
	for i := range 9 {
		if !rl.Allow(t0) { // an hour earlier than the previous call
			t.Fatalf("call %d denied after the clock went backwards", i)
		}
	}
	if rl.Allow(t0) {
		t.Error("backwards time refilled the bucket")
	}
}

// TestCostCapHoldsUnderFlood is the cost guarantee, stated as a test: no matter how
// loud the logs get, the tool cannot make more LLM calls than the rate limit allows.
// This is the number that shows up on someone's bill, so it gets its own assertion.
func TestCostCapHoldsUnderFlood(t *testing.T) {
	const (
		lines   = 10_000
		perMin  = 10
		spanSec = 1 // the whole flood lands inside one second: no meaningful refill
	)

	cfg := liveConfig()
	cfg.RatePerMin = perMin
	f := newFixture(t, cfg, nil)

	// Every single line is a brand-new ERROR template, i.e. every single line clears the
	// threshold. This is the worst case the scorer can be handed: novelty alone would be
	// absorbed by the weights, so the flood is made of genuine faults instead.
	for i := range lines {
		f.feed(fault("distinct failure "+strconv.Itoa(i)),
			t0.Add(time.Duration(i)*time.Second*spanSec/lines))
	}

	stats := f.sc.Stats()
	if stats.Escalated > perMin {
		t.Errorf("Escalated = %d, want at most %d: the cost cap leaked", stats.Escalated, perMin)
	}
	if stats.Escalated+stats.Suppressed != lines {
		t.Errorf("escalated %d + suppressed %d != %d lines: events went missing from the accounting",
			stats.Escalated, stats.Suppressed, lines)
	}
	if stats.Suppressed == 0 {
		t.Error("nothing was recorded as suppressed; the UI would claim it told the user everything")
	}
}
