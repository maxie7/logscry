// SPDX-License-Identifier: Apache-2.0

// Package score decides which events are worth escalating to the LLM. It combines
// novelty, burst, and severity signals, gated by a global rate limiter and an
// explanation cache. This is the make-or-break module (see RDI §6).
package score

import (
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// Weights tunes the relative contribution of each scoring signal.
type Weights struct {
	Novelty  float64
	Burst    float64
	Severity float64
}

// Config holds scoring thresholds and windows (see RDI §6 for defaults).
type Config struct {
	Weights       Weights
	Threshold     float64       // escalate iff score >= Threshold
	Cooloff       time.Duration // novelty cooloff (default 15m)
	BurstWindow   time.Duration // sliding window for burst (default 10s)
	BurstAbsolute int           // absolute burst threshold (default 50)
	RatePerMin    int           // global LLM calls/min (default 10)
	CacheTTL      time.Duration // explanation cache TTL
}

// Score computes an escalation score for a line given its template state.
//
// TODO(M3): combine novelty + burst + severity signals.
func Score(cfg Config, line model.LogLine, tmpl *model.Template, now time.Time) float64 {
	return 0
}

// ShouldEscalate reports whether the line should be sent to the LLM: score is
// above threshold, the template is not already explained within the cache TTL,
// and the global rate limiter allows another call.
//
// TODO(M3): implement the escalation decision.
func ShouldEscalate(cfg Config, s float64, tmpl *model.Template, rl *RateLimiter, cache *Cache, now time.Time) bool {
	return false
}

// RateLimiter is a token-bucket that caps global LLM calls regardless of log
// volume.
type RateLimiter struct{}

// NewRateLimiter returns a token bucket allowing perMin calls per minute.
//
// TODO(M3): implement token-bucket refill/consume.
func NewRateLimiter(perMin int) *RateLimiter { return &RateLimiter{} }

// Allow reports whether a call may proceed now, consuming a token if so.
//
// TODO(M3): implement.
func (r *RateLimiter) Allow(now time.Time) bool { return false }

// Cache remembers which templates have already been explained, keyed by hash.
type Cache struct{}

// NewCache returns an empty explanation cache.
//
// TODO(M3): implement cache with TTL.
func NewCache() *Cache { return &Cache{} }

// Has reports whether the template hash has a fresh explanation.
//
// TODO(M3): implement.
func (c *Cache) Has(hash string, now time.Time) bool { return false }
