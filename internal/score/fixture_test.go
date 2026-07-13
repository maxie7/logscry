// SPDX-License-Identifier: Apache-2.0

package score

import (
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// t0 is the start of every test's timeline. Time is an argument everywhere in this
// package, so the whole suite runs on a synthetic clock: no sleeps, no flakes.
var t0 = time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

// fixture drives a Scorer the way the pipeline does: upsert the template state, then
// evaluate. The upsert is mirrored here rather than reused because score cannot import
// pipeline — pipeline imports score — and it is the same handful of lines. Templating
// itself is the pipeline's job and is tested there, so a line's message doubles as its
// template hash.
type fixture struct {
	t         *testing.T
	sc        *Scorer
	templates map[string]*model.Template
}

func newFixture(t *testing.T, cfg Config, out chan<- EscalationRequest) *fixture {
	t.Helper()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config is invalid: %v", err)
	}
	return &fixture{t: t, sc: New(cfg, out), templates: make(map[string]*model.Template)}
}

// feed runs one line through the scorer at time now.
func (f *fixture) feed(line model.LogLine, now time.Time) Result {
	f.t.Helper()
	tmpl, prev := f.upsert(line.Message, now)
	return f.sc.Evaluate(line, tmpl, prev, now)
}

// repeat feeds the same line n times, every step apart, starting at from. It returns
// the time of the last occurrence.
func (f *fixture) repeat(line model.LogLine, from time.Time, n int, step time.Duration) time.Time {
	f.t.Helper()
	at := from
	for i := range n {
		at = from.Add(time.Duration(i) * step)
		f.feed(line, at)
	}
	return at
}

// upsert mirrors pipeline.upsert: it returns the live template and the LastSeen it
// held before this occurrence.
func (f *fixture) upsert(hash string, now time.Time) (*model.Template, time.Time) {
	tmpl, ok := f.templates[hash]
	if !ok {
		tmpl = &model.Template{
			Hash: hash, Pattern: hash,
			FirstSeen: now, LastSeen: now,
			Count:  1,
			Recent: []time.Time{now},
		}
		f.templates[hash] = tmpl
		return tmpl, time.Time{}
	}
	prev := tmpl.LastSeen
	tmpl.Count++
	tmpl.LastSeen = now
	if len(tmpl.Recent) < model.RecentCap {
		tmpl.Recent = append(tmpl.Recent, now)
	} else {
		copy(tmpl.Recent, tmpl.Recent[1:])
		tmpl.Recent[len(tmpl.Recent)-1] = now
	}
	return tmpl, prev
}

// liveConfig is Defaults with warmup switched off, for the tests that are about a
// signal rather than about warmup. Warmup has its own tests.
func liveConfig() Config {
	c := Defaults()
	c.Warmup, c.WarmupLines = 0, 0
	return c
}

// line builds a plain stdout line whose message is msg.
func line(msg string) model.LogLine {
	return model.LogLine{Source: "test", Raw: msg, Message: msg}
}

// stderr marks a line as coming from stderr.
func stderr(l model.LogLine) model.LogLine {
	l.Stream = model.Stderr
	return l
}

// level tags a line with a canonical level, as pipeline.Normalize would.
func level(l model.LogLine, lv string) model.LogLine {
	l.Level = lv
	return l
}

// hasReason reports whether any reason starts with prefix (the reasons carry live
// numbers, so tests match on the kind of reason, not the exact string).
func hasReason(reasons []string, prefix string) bool {
	for _, r := range reasons {
		if len(r) >= len(prefix) && r[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
