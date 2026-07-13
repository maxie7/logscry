// SPDX-License-Identifier: Apache-2.0

package score

import "time"

// RateLimiter is a token bucket capping global LLM calls. It is the cost guarantee:
// no matter how loud the logs get — a thousand escalating lines a second — the tool
// cannot make more than perMin calls per minute. Non-negotiable per RDI §6.
//
// Refill is continuous rather than per-tick: tokens accrue at perMin/60 per second,
// so a quiet minute leaves a full bucket and a burst spends it. Like the rest of the
// scorer it is goroutine-confined and takes now as an argument, which is what makes
// the exhaust/refill behaviour testable without a single sleep.
type RateLimiter struct {
	capacity float64 // bucket size, in tokens
	perSec   float64 // refill rate
	tokens   float64
	last     time.Time // when tokens was last brought up to date; zero until first use
}

// NewRateLimiter returns a token bucket allowing perMin calls per minute, starting
// full so the first escalation of a run is never delayed. A perMin of zero or less
// denies everything, which is a legitimate "never call the LLM" configuration.
func NewRateLimiter(perMin int) *RateLimiter {
	if perMin < 0 {
		perMin = 0
	}
	return &RateLimiter{
		capacity: float64(perMin),
		perSec:   float64(perMin) / 60,
		tokens:   float64(perMin),
	}
}

// Allow reports whether a call may proceed now, consuming a token if so.
func (r *RateLimiter) Allow(now time.Time) bool {
	if r.capacity <= 0 {
		return false
	}
	if r.last.IsZero() {
		r.last = now
	}
	// A clock that jumps backwards (NTP, a Docker line whose embedded timestamp
	// predates the previous one) must not drain the bucket, so negative elapsed
	// time accrues nothing rather than a negative refill.
	if elapsed := now.Sub(r.last).Seconds(); elapsed > 0 {
		r.tokens = min(r.capacity, r.tokens+elapsed*r.perSec)
		r.last = now
	}
	if r.tokens < 1 {
		return false
	}
	r.tokens--
	return true
}
