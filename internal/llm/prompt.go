// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"fmt"
	"strings"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// The prompt lives here, alone, so it can be iterated on without touching the
// transport. It is deliberately small: the token budget is bounded, and this may be
// running on a 3B model on someone's laptop.
const systemPrompt = `You are logscry's log-triage assistant. You receive ONE anomalous log line from a live tail, the lines that preceded it, its masked template, and how often it has occurred.

Reply with a single JSON object and nothing else:
{"summary": "...", "likely_cause": "...", "suggestion": "..."}

summary:      one sentence — what happened.
likely_cause: the most probable cause, one or two sentences.
suggestion:   the single most useful thing to check or try next. Be concrete.

No markdown, no code fences, no extra keys, no preamble. Keep each field under 200 characters. If the line looks benign, say so in summary.`

const (
	// maxTriggerRunes and maxContextRunes bound what one line may contribute. A single
	// pathological log line — a megabyte of embedded JSON, a stack trace, a base64 blob —
	// must not blow the token budget for the request it happens to land in.
	maxTriggerRunes = 1000
	maxContextRunes = 300
)

// buildMessages renders the request into the system + user pair sent to the model.
//
// The scorer's reasons are deliberately absent: ExplainRequest is pinned by RDI §7 to
// the trigger, the context, the template, and the counts. Telling the model *why we
// flagged it* would invite it to agree with us rather than read the logs.
func buildMessages(req ExplainRequest) []message {
	return []message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt(req)},
	}
}

// userPrompt renders the evidence: the line that fired, what it looks like with its
// variable parts masked, how often it has happened, and what came before it.
func userPrompt(req ExplainRequest) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Trigger line (source=%s, stream=%s, level=%s):\n%s\n",
		orDash(req.Trigger.Source), streamName(req.Trigger.Stream), orDash(req.Trigger.Level),
		truncateRunes(req.Trigger.Raw, maxTriggerRunes))

	fmt.Fprintf(&b, "\nMasked template: %s\n", req.Template)
	fmt.Fprintf(&b, "Occurrences: %d%s\n", req.Count, firstSeenSuffix(req))

	if len(req.Context) > 0 {
		b.WriteString("\nPreceding lines (oldest first):\n")
		for _, line := range req.Context {
			b.WriteString(truncateRunes(line, maxContextRunes))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// firstSeenSuffix renders how long this template has been around, which is what tells
// the model "this is new" apart from "this has been happening all afternoon". It is
// omitted rather than faked when the request carries no first-seen time.
func firstSeenSuffix(req ExplainRequest) string {
	if req.Trigger.Time.IsZero() || req.FirstSeen.IsZero() {
		return ""
	}
	age := req.Trigger.Time.Sub(req.FirstSeen)
	if age < 0 {
		return ""
	}
	return fmt.Sprintf(" (first seen %s ago)", age.Truncate(time.Second))
}

func streamName(s model.Stream) string {
	if s == model.Stderr {
		return "stderr"
	}
	return "stdout"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncateRunes cuts s to at most n runes, marking the cut so the model knows the line
// was clipped rather than being that shape.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…[truncated]"
}
