// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
	"github.com/maxie7/logscry/internal/score"
)

// These tests drive the prompt's "first seen" age (issue #72) through the whole chain a
// live escalation takes: the pipeline's upsert, the scorer's emit, the pool's copy into an
// ExplainRequest, and — in one of them — the anonymizing decorator's copy. A field left out
// at any copy site arrives zero, and a zero time omits the suffix, which is
// indistinguishable from the bug itself; only the rendered prompt at the far end can tell.

// capturingBackend records every request it is asked to explain.
type capturingBackend struct {
	mu   sync.Mutex
	reqs []ExplainRequest
}

func (c *capturingBackend) Name() string { return "capture" }

func (c *capturingBackend) Explain(_ context.Context, req ExplainRequest) (ExplainResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	return ExplainResponse{Summary: "ok"}, nil
}

// arrival is one line as the pipeline receives it: the line, carrying its source's time,
// and the pipeline's own clock at the moment it is processed.
type arrival struct {
	line model.LogLine
	now  time.Time
}

// escalatePrompts runs arrivals through a real pipeline and scorer at their defaults, hands
// the escalations to the real pool in front of b, and returns the events and the user
// prompts the backend at the end of the chain received.
func escalatePrompts(t *testing.T, b Backend, inner *capturingBackend, arrivals []arrival) ([]pipeline.Event, []string) {
	t.Helper()
	escalations := make(chan score.EscalationRequest, len(arrivals))
	p := pipeline.New(score.New(score.Defaults(), escalations))

	var events []pipeline.Event
	for _, a := range arrivals {
		events = append(events, p.Process(a.line, a.now))
	}
	close(escalations)

	out := make(chan model.Explanation, len(arrivals))
	Run(context.Background(), b, poolConfig(1), escalations, out)
	for ex := range out {
		if ex.State != model.ExplainDone {
			t.Fatalf("explanation state = %v (%s), want done", ex.State, ex.Err)
		}
	}

	var prompts []string
	for _, req := range inner.reqs {
		prompts = append(prompts, userPrompt(req))
	}
	return events, prompts
}

// backlog is the cell #72's fix exists for: one template — the level prefix is stripped
// from the message, so ERROR and FATAL share it — whose source spread six lines over an
// hour, delivered as a backlog within milliseconds of the pipeline's clock two hours later.
// The ERRORs stay under the threshold and the FATAL escalates alone, at Count 6.
func backlog() []arrival {
	t0 := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	attach := t0.Add(3 * time.Hour)
	var as []arrival
	for i := range 5 {
		as = append(as, arrival{
			line: model.LogLine{Source: "docker:db", Raw: "ERROR: db unreachable", Time: t0.Add(time.Duration(i) * 12 * time.Minute)},
			now:  attach.Add(time.Duration(i) * time.Millisecond),
		})
	}
	return append(as, arrival{
		line: model.LogLine{Source: "docker:db", Raw: "FATAL: db unreachable", Time: t0.Add(time.Hour)},
		now:  attach.Add(5 * time.Millisecond),
	})
}

// assertBacklogPrompt checks the backlog run escalated exactly once, on the FATAL, at
// Count 6, and that the prompt carries the hour the source spread the template over.
func assertBacklogPrompt(t *testing.T, events []pipeline.Event, prompts []string) {
	t.Helper()
	var escalated []pipeline.Event
	for _, ev := range events {
		if ev.Escalate {
			escalated = append(escalated, ev)
		}
	}
	if len(escalated) != 1 || escalated[0].Count != 6 {
		t.Fatalf("escalations = %+v, want exactly one at Count 6 (did ERROR and FATAL stop sharing a template?)", escalated)
	}
	if len(prompts) != 1 {
		t.Fatalf("backend saw %d requests, want 1", len(prompts))
	}
	const want = "Occurrences: 6 (first seen 1h0m0s ago)\n"
	if !strings.Contains(prompts[0], want) {
		t.Errorf("user prompt lacks %q:\n%s", want, prompts[0])
	}
}

// TestBacklogEscalationCarriesSourceAge: emit → pool → backend. Before the fix the age was
// the source time minus the pipeline's first-seen, two hours negative, and the suffix was
// silently dropped.
func TestBacklogEscalationCarriesSourceAge(t *testing.T) {
	inner := &capturingBackend{}
	events, prompts := escalatePrompts(t, inner, inner, backlog())
	assertBacklogPrompt(t, events, prompts)
}

// TestBacklogAgeSurvivesAnonymizer: the same chain with the anonymizing decorator in front,
// the second place an ExplainRequest is copied. The assertion is on the masked request the
// inner backend actually receives.
func TestBacklogAgeSurvivesAnonymizer(t *testing.T) {
	inner := &capturingBackend{}
	events, prompts := escalatePrompts(t, NewAnonymizing(inner), inner, backlog())
	assertBacklogPrompt(t, events, prompts)
}

// TestFirstOccurrencePromptHasNoAge: a template's first occurrence carries no age —
// "Occurrences: 1" already says it, and the age is 0 by construction.
//
// The production-shaped row is green before and after #72: live, the trigger is stamped a
// few microseconds BEFORE the pipeline's clock, so the old pipeline-clock subtraction went
// negative and the suffix was omitted by accident. The equal-clocks row pins that it is now
// omitted by rule, whatever the two clocks say.
func TestFirstOccurrencePromptHasNoAge(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		trigger time.Time
	}{
		{"production-shaped: stamped before the pipeline's clock", now.Add(-3 * time.Microsecond)},
		{"clocks agree exactly", now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &capturingBackend{}
			_, prompts := escalatePrompts(t, inner, inner, []arrival{{
				line: model.LogLine{Source: "stdin", Raw: "FATAL: out of memory", Time: tc.trigger},
				now:  now,
			}})
			if len(prompts) != 1 {
				t.Fatalf("backend saw %d requests, want 1", len(prompts))
			}
			if !strings.Contains(prompts[0], "Occurrences: 1\n") || strings.Contains(prompts[0], "first seen") {
				t.Errorf("first occurrence's prompt carries an age:\n%s", prompts[0])
			}
		})
	}
}
