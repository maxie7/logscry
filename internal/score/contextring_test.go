// SPDX-License-Identifier: Apache-2.0

package score

import (
	"strconv"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/model"
)

func TestContextRingKeepsTheLastMLinesInOrder(t *testing.T) {
	r := NewContextRing(3)

	if got := r.Lines(); len(got) != 0 {
		t.Errorf("a fresh ring returned %d lines, want none", len(got))
	}

	for i := range 5 {
		r.Push(model.LogLine{Source: "svc", Raw: "line " + strconv.Itoa(i)})
	}

	got := r.Lines()
	if len(got) != 3 {
		t.Fatalf("ring holds %d lines, want 3 (bounded)", len(got))
	}
	// Oldest first, and only the most recent three survived.
	for i, want := range []string{"line 2", "line 3", "line 4"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("line %d = %q, want it to contain %q", i, got[i], want)
		}
	}
	if !strings.HasPrefix(got[0], "[svc] ") {
		t.Errorf("line = %q, want the source tagged so the LLM knows who said it", got[0])
	}
}

// TestContextRingCopies: the returned slice travels to another goroutine on an
// EscalationRequest while the pipeline keeps overwriting the ring. It must be a copy.
func TestContextRingCopies(t *testing.T) {
	r := NewContextRing(2)
	r.Push(model.LogLine{Source: "svc", Raw: "first"})
	r.Push(model.LogLine{Source: "svc", Raw: "second"})

	snapshot := r.Lines()
	r.Push(model.LogLine{Source: "svc", Raw: "third"}) // wraps, overwriting "first"

	if !strings.Contains(snapshot[0], "first") {
		t.Errorf("the snapshot changed under the reader: %q", snapshot[0])
	}
}

// TestContextRingDisabled: zero context lines is a valid configuration and must not
// panic on the write path.
func TestContextRingDisabled(t *testing.T) {
	r := NewContextRing(0)
	r.Push(model.LogLine{Raw: "anything"})
	if got := r.Lines(); len(got) != 0 {
		t.Errorf("a disabled ring returned %d lines", len(got))
	}
}
