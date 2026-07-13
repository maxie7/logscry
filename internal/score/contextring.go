// SPDX-License-Identifier: Apache-2.0

package score

import "github.com/maxie7/logscry/internal/model"

// ContextRing is a bounded ring of the last M lines across ALL sources. An escalated
// line rarely explains itself — the interesting part is usually what the process was
// doing just before it — so an escalation carries this temporal context with it.
//
// It is global on purpose: an error in one container is often caused by what another
// one logged a second earlier, and correlation across sources is where this is headed
// (RDI §10).
type ContextRing struct {
	buf    []string
	head   int  // next write index
	filled bool // has wrapped at least once
}

// NewContextRing returns a ring holding the last n lines. n <= 0 disables context.
func NewContextRing(n int) *ContextRing {
	if n <= 0 {
		return &ContextRing{}
	}
	return &ContextRing{buf: make([]string, n)}
}

// Push records a line. It stores the raw text, not the masked template: the LLM wants
// the concrete values that the templating deliberately threw away.
func (r *ContextRing) Push(line model.LogLine) {
	if len(r.buf) == 0 {
		return
	}
	r.buf[r.head] = "[" + line.Source + "] " + line.Raw
	r.head++
	if r.head == len(r.buf) {
		r.head = 0
		r.filled = true
	}
}

// Lines returns a copy of the ring in chronological order, oldest first. The copy
// matters: the returned slice travels to another goroutine on an EscalationRequest,
// while this ring keeps being overwritten by the pipeline.
func (r *ContextRing) Lines() []string {
	if !r.filled {
		return append([]string(nil), r.buf[:r.head]...)
	}
	out := make([]string, 0, len(r.buf))
	out = append(out, r.buf[r.head:]...) // oldest: from head to the end
	out = append(out, r.buf[:r.head]...) // newest: wrapped around
	return out
}
