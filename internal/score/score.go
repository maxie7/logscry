// SPDX-License-Identifier: Apache-2.0

// Package score decides which events are worth escalating to the LLM. It combines
// novelty, burst, and severity signals, gated by a global rate limiter and an
// explanation cache. This is the make-or-break module (see RDI §6).
//
// The bias is deliberate and everywhere in this file: a tool that cries wolf gets
// uninstalled after one day. Under-alerting is a mild annoyance; over-alerting kills
// the product. When a signal is ambiguous, it does not escalate — so novelty is muted
// during warmup, burst refuses to fire without an established baseline, and severity
// alone (short of a crash) can raise the score but never trigger on its own.
//
// Everything here is pure logic with time passed in as an argument: no clock, no
// sleeps, and therefore tests that pin down the behaviour exactly. Like the pipeline
// state map it is goroutine-confined — the pipeline goroutine is the only caller.
package score

import (
	"fmt"
	"strings"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// Weights tunes each signal's contribution to the score. The defaults are chosen so
// that novelty, burst, and a fatal-class level each reach the threshold on their own,
// while stderr, WARN, and ERROR only ever nudge a score toward it — see Defaults.
// The yaml tags name the knobs in logscry.yaml (RDI §9). They are inert struct tags:
// this package takes no dependency on a YAML library — internal/config does the
// decoding.
type Weights struct {
	Novelty float64 `yaml:"novelty"`
	Burst   float64 `yaml:"burst"`

	// Severity is additive across the stream and the level: an ERROR on stderr scores
	// Error+Stderr. SeverityMax caps that sum, which is what keeps severity from
	// escalating by itself.
	Stderr      float64 `yaml:"stderr"` // Stream == model.Stderr
	Warn        float64 `yaml:"warn"`   // WARN
	Error       float64 `yaml:"error"`  // ERROR, CRITICAL
	Fatal       float64 `yaml:"fatal"`  // FATAL, PANIC
	SeverityMax float64 `yaml:"severity_max"`
}

// Config holds every scoring knob. Defaults returns the tuned-quiet values; config
// (flags + YAML) overrides them.
type Config struct {
	// Threshold is the score at or above which an event escalates.
	Threshold float64 `yaml:"threshold"`
	Weights   Weights `yaml:"weights"`

	// Novelty.
	Cooloff     time.Duration `yaml:"cooloff"`      // a template unseen for longer than this is novel again
	Warmup      time.Duration `yaml:"warmup"`       // novelty is muted until this has elapsed AND
	WarmupLines int           `yaml:"warmup_lines"` // this many lines have been seen

	// Burst. There is no absolute "N in the window" trigger by design: see burst.
	BurstWindow      time.Duration `yaml:"burst_window"`       // sliding window the rate is measured over
	BurstMultiplier  float64       `yaml:"burst_multiplier"`   // fire at this many times the established baseline rate
	BurstMinCount    int           `yaml:"burst_min_count"`    // ...but never on fewer occurrences than this (volume gate)
	BaselineMinAge   time.Duration `yaml:"baseline_min_age"`   // a template has no baseline until it is this old
	BaselineMinCount int           `yaml:"baseline_min_count"` // ...and has been seen this many times

	// Gates.
	RatePerMin int           `yaml:"rate_limit"` // global cap on LLM calls per minute
	CacheTTL   time.Duration `yaml:"cache_ttl"`  // how long an explained template stays quiet

	// ContextLines is how many recent lines an escalation carries with it.
	ContextLines int `yaml:"context_lines"`
}

// Defaults are the tuned-quiet defaults, in one place so that tuning is one edit.
//
// The numbers encode a specific policy, which is easiest to read as what each
// situation scores against the 1.0 threshold:
//
//	stderr alone                   0.3   no
//	ERROR alone                    0.6   no
//	stderr + ERROR                 0.9   no  — deliberately just under: routine
//	                                          error chatter must not escalate
//	novel template (post-warmup)   1.0   yes — the single strongest signal
//	rate at 5x its own baseline    1.0   yes — a CHANGE in rate, not a high rate
//	FATAL / PANIC                  1.0   yes — a crash is worth interrupting for,
//	                                          even during warmup
//
// Note what is absent: a steady stream escalates at no rate whatsoever. A service
// emitting one line a second and one emitting ten thousand are both simply running.
func Defaults() Config {
	return Config{
		Threshold: 1.0,
		Weights: Weights{
			Novelty:     1.0,
			Burst:       1.0,
			Stderr:      0.3,
			Warn:        0.2,
			Error:       0.6,
			Fatal:       1.0,
			SeverityMax: 1.0,
		},
		Cooloff:          15 * time.Minute,
		Warmup:           30 * time.Second,
		WarmupLines:      100,
		BurstWindow:      10 * time.Second,
		BurstMultiplier:  5,
		BurstMinCount:    10,
		BaselineMinAge:   60 * time.Second,
		BaselineMinCount: 20,
		RatePerMin:       10,
		CacheTTL:         time.Hour,
		ContextLines:     30,
	}
}

// Validate rejects configurations that are silently broken — ones that would either
// escalate everything or, worse, could never escalate at all, which the user would
// only discover by never being told anything.
func (c Config) Validate() error {
	if c.Threshold <= 0 {
		return fmt.Errorf("threshold must be > 0, got %g (everything would escalate)", c.Threshold)
	}
	if c.BurstMinCount > model.RecentCap {
		return fmt.Errorf("burst min count %d exceeds the recent-occurrence ring capacity %d, so a burst could never be observed",
			c.BurstMinCount, model.RecentCap)
	}
	if c.BurstWindow <= 0 {
		return fmt.Errorf("burst window must be > 0, got %s", c.BurstWindow)
	}
	if c.BurstMultiplier <= 1 {
		return fmt.Errorf("burst multiplier must be > 1, got %g: at or below 1x, a template running at "+
			"its own steady baseline would be reported as a burst", c.BurstMultiplier)
	}
	if c.RatePerMin < 0 {
		return fmt.Errorf("rate limit must be >= 0, got %d", c.RatePerMin)
	}
	if c.CacheTTL < 0 {
		return fmt.Errorf("cache TTL must be >= 0, got %s", c.CacheTTL)
	}
	if c.ContextLines < 0 {
		return fmt.Errorf("context lines must be >= 0, got %d", c.ContextLines)
	}
	return nil
}

// EscalationRequest is what the LLM worker pool (M4) consumes: the line that fired,
// what came before it, and why the scorer thought it mattered.
type EscalationRequest struct {
	Trigger   model.LogLine
	Context   []string // recent lines across all sources, oldest first
	Pattern   string   // the masked template, e.g. "user <NUM> failed"
	Hash      string
	Count     int // occurrences of this template so far
	FirstSeen time.Time
	Score     float64
	Reasons   []string // human-readable: why this fired
}

// Result is the scorer's verdict on one line, which the pipeline attaches to the
// Event so the UI can show it. Reasons is freshly allocated and never mutated
// afterwards, so it is safe to hand to the renderer's goroutine.
type Result struct {
	Score    float64
	Escalate bool
	// Queued reports that the escalation actually reached the LLM stage, rather than
	// being dropped by a full channel. The two are different outcomes for the user —
	// "explaining…" versus "explanation unavailable" — and only emit knows which
	// happened, so a card is never left waiting on an answer that is not coming.
	Queued  bool
	Reasons []string
}

// Stats are the running escalation counters. Suppressed and Dropped exist so the UI
// can be honest about what it is not telling the user.
type Stats struct {
	Escalated  int // passed every gate
	Suppressed int // over threshold, but the rate limiter refused: the cost cap biting
	Cached     int // over threshold, but already explained: the cache keeping it quiet
	Dropped    int // escalated, but the outbound channel was full
}

// Scorer evaluates lines and decides which ones escalate. Not safe for concurrent
// use: it is owned by the pipeline goroutine.
type Scorer struct {
	cfg   Config
	rl    *RateLimiter
	cache *Cache
	ring  *ContextRing
	out   chan<- EscalationRequest

	stats Stats
	start time.Time // time of the first line seen; zero until then
	lines int
}

// New returns a Scorer. out is the escalation channel the LLM worker pool consumes in
// M4; it may be nil, in which case escalations are still decided and counted but go
// nowhere.
func New(cfg Config, out chan<- EscalationRequest) *Scorer {
	return &Scorer{
		cfg:   cfg,
		rl:    NewRateLimiter(cfg.RatePerMin),
		cache: NewCache(cfg.CacheTTL),
		ring:  NewContextRing(cfg.ContextLines),
		out:   out,
	}
}

// Evaluate scores one line and decides whether it escalates. tmpl is the live template
// state after the pipeline's upsert, and prevLastSeen is that template's LastSeen from
// *before* the upsert overwrote it — which is what makes the cooloff measurable.
//
// It never blocks: a full escalation channel drops the request rather than stall
// ingestion.
func (s *Scorer) Evaluate(line model.LogLine, tmpl *model.Template, prevLastSeen, now time.Time) Result {
	if s.start.IsZero() {
		s.start = now
	}
	s.lines++

	var res Result
	if novel, why := s.novelty(tmpl, prevLastSeen, now); novel {
		res.Score += s.cfg.Weights.Novelty
		res.Reasons = append(res.Reasons, why)
	}
	if burst, why := s.burst(tmpl, now); burst {
		res.Score += s.cfg.Weights.Burst
		res.Reasons = append(res.Reasons, why)
	}
	if sev, why := s.severity(line); sev > 0 {
		res.Score += sev
		res.Reasons = append(res.Reasons, why...)
	}

	res.Escalate = s.decide(tmpl, res.Score, now)
	if res.Escalate {
		res.Queued = s.emit(line, tmpl, res)
	}
	// After the emit, so an escalation's context is the lines that *preceded* it.
	s.ring.Push(line)
	return res
}

// Stats returns a copy of the running counters.
func (s *Scorer) Stats() Stats { return s.stats }

// novelty reports whether the template has never been seen, or has not been seen for
// longer than the cooloff. A brand-new template in a running system is the strongest
// signal there is — but in the first seconds of a run *every* template is new, so
// novelty stays silent until warmup is over. Without that, starting the tool would
// flood the user instantly, and they would never start it again.
func (s *Scorer) novelty(tmpl *model.Template, prevLastSeen, now time.Time) (bool, string) {
	if !s.warmedUp(now) {
		return false, ""
	}
	if tmpl.Count == 1 {
		return true, "novel template (first seen)"
	}
	if gap := now.Sub(prevLastSeen); gap > s.cfg.Cooloff {
		return true, fmt.Sprintf("novel template (unseen for %s, cooloff %s)",
			gap.Truncate(time.Second), s.cfg.Cooloff)
	}
	return false, ""
}

// warmedUp reports whether the scorer has watched the system long enough to have an
// opinion about what is normal for it. Both guards are needed. The duration one kills
// the startup flood: attaching to a container replays a backlog of a hundred lines in
// milliseconds, every one of them "novel". The line-count one covers the opposite
// case — a low-volume service, where 30 seconds buys only a handful of lines and its
// eleventh perfectly routine template would otherwise be called novel at t=31s.
func (s *Scorer) warmedUp(now time.Time) bool {
	return now.Sub(s.start) >= s.cfg.Warmup && s.lines > s.cfg.WarmupLines
}

// burst reports whether a known template's rate has spiked. It is a purely RELATIVE
// signal: a template must be running at a multiple of its own established baseline.
//
// There is deliberately no absolute "N in the window is always a burst" trigger. Such
// a floor does not encode "something changed", it encodes "this system is busy" — and
// a service that steadily emits a few hundred lines a second of one template is busy,
// not broken. Worse, no value of that floor is safe: whatever number is picked, some
// perfectly healthy service sits just above it and gets reported forever. Only a
// CHANGE in rate is news, so only a change can fire.
//
// It fires only for templates with an established baseline, which is also what stops a
// template that is new *and* chatty from "bursting" on top of already being novel —
// the same fact counted twice.
func (s *Scorer) burst(tmpl *model.Template, now time.Time) (bool, string) {
	base, ok := s.baseline(tmpl, now)
	if !ok {
		return false, ""
	}

	n, rate := s.windowRate(tmpl.Recent, now)
	// A minimum-volume gate, not a trigger: without it a template that normally appears
	// once a minute would "burst" at 10x on the strength of two occurrences —
	// technically a spike, in practice two log lines.
	if n < s.cfg.BurstMinCount || rate < s.cfg.BurstMultiplier*base {
		return false, ""
	}
	return true, fmt.Sprintf("burst: %.1f/s vs baseline %.2f/s (%.0f×), %d in the last %s",
		rate, base, rate/base, n, s.cfg.BurstWindow)
}

// windowRate estimates a template's current rate from its recent-occurrence ring,
// returning the occurrences observed in the burst window and the rate they imply.
//
// The subtlety is that Recent is a bounded sample. Above roughly RecentCap/window
// occurrences per second the ring no longer spans the whole window, and dividing by
// the whole window would understate the rate — by exactly the factor that would hide a
// genuine spike on an already-busy template. So when the ring is saturated inside the
// window, the rate is measured over the span it actually covers, which is the true
// instantaneous rate rather than an average diluted by history we no longer have.
//
// This is also what makes the steady-state guarantee hold at ANY rate: a template
// running flat out at ten thousand lines a second measures ten thousand a second,
// matches its own baseline, and says nothing.
func (s *Scorer) windowRate(recent []time.Time, now time.Time) (int, float64) {
	n := countSince(recent, now.Add(-s.cfg.BurstWindow))
	if n == 0 {
		return 0, 0
	}

	seconds := s.cfg.BurstWindow.Seconds()
	if n == len(recent) && len(recent) == model.RecentCap {
		oldest := recent[len(recent)-n]
		// A floor on the span keeps a same-instant clump from dividing by zero; it is
		// far below any real log cadence, so it never rounds a genuine rate down.
		if span := now.Sub(oldest).Seconds(); span < seconds {
			seconds = max(span, 1e-6)
		}
	}
	return n, float64(n) / seconds
}

// baseline returns the template's established rate in occurrences/sec, and whether it
// has one at all. A template earns a baseline only once it is old enough and frequent
// enough for its average rate to mean something.
func (s *Scorer) baseline(tmpl *model.Template, now time.Time) (float64, bool) {
	age := now.Sub(tmpl.FirstSeen)
	if age < s.cfg.BaselineMinAge || age <= 0 || tmpl.Count < s.cfg.BaselineMinCount {
		return 0, false
	}
	rate := float64(tmpl.Count) / age.Seconds()
	if rate <= 0 {
		return 0, false
	}
	return rate, true
}

// severity is the additive weight of the line's stream and level, capped at
// SeverityMax. Plenty of well-behaved programs log routine chatter to stderr, and
// plenty log routine ERRORs, so the default weights put "stderr + ERROR" just below
// the threshold: severity raises the score, it does not trigger on its own. The one
// exception is a fatal-class level, which is worth interrupting for by itself.
func (s *Scorer) severity(line model.LogLine) (float64, []string) {
	var (
		total   float64
		reasons []string
	)
	if line.Stream == model.Stderr && s.cfg.Weights.Stderr > 0 {
		total += s.cfg.Weights.Stderr
		reasons = append(reasons, "stderr")
	}
	if w := s.levelWeight(line.Level); w > 0 {
		total += w
		reasons = append(reasons, "level "+strings.ToUpper(line.Level))
	}
	if limit := s.cfg.Weights.SeverityMax; limit > 0 && total > limit {
		total = limit
	}
	return total, reasons
}

// levelWeight maps a canonical level (see pipeline.Normalize) to its weight. DEBUG,
// INFO, and TRACE carry none.
func (s *Scorer) levelWeight(level string) float64 {
	switch strings.ToUpper(level) {
	case "FATAL", "PANIC":
		return s.cfg.Weights.Fatal
	case "ERROR", "CRITICAL":
		return s.cfg.Weights.Error
	case "WARN":
		return s.cfg.Weights.Warn
	}
	return 0
}

// decide applies the three gates: over threshold, not already explained, and allowed
// by the global rate limiter. The order is load-bearing — an already-explained
// template must not spend a token it has no use for.
//
// Everything that fails a gate is counted, never dropped from the data: it still
// flows to the stream and the aggregated view, it just does not interrupt anyone.
func (s *Scorer) decide(tmpl *model.Template, total float64, now time.Time) bool {
	if total < s.cfg.Threshold {
		return false
	}
	if s.cache.Hit(tmpl.Hash, now) {
		s.stats.Cached++
		return false
	}
	if !s.rl.Allow(now) {
		s.stats.Suppressed++
		return false
	}
	s.cache.Mark(tmpl.Hash, now)
	s.stats.Escalated++
	return true
}

// emit hands the escalation to the LLM stage without ever blocking on it, reporting
// whether it got there. A full channel means the model is slower than the anomalies are
// arriving; dropping is the only acceptable answer, because the alternative is ingestion
// stalling behind an LLM.
func (s *Scorer) emit(line model.LogLine, tmpl *model.Template, res Result) bool {
	if s.out == nil {
		return false
	}
	req := EscalationRequest{
		Trigger:   line,
		Context:   s.ring.Lines(),
		Pattern:   tmpl.Pattern,
		Hash:      tmpl.Hash,
		Count:     tmpl.Count,
		FirstSeen: tmpl.FirstSeen,
		Score:     res.Score,
		Reasons:   res.Reasons,
	}
	select {
	case s.out <- req:
		return true
	default:
		s.stats.Dropped++
		return false
	}
}

// countSince counts the occurrences at or after cutoff. Recent is append-ordered, so
// the scan runs backwards from the newest and stops at the window's edge — the whole
// ring is only walked when the whole ring is inside the window, which is exactly the
// case where the answer is a burst anyway.
func countSince(recent []time.Time, cutoff time.Time) int {
	n := 0
	for i := len(recent) - 1; i >= 0; i-- {
		if recent[i].Before(cutoff) {
			break
		}
		n++
	}
	return n
}
