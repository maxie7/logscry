// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maxie7/logscry/internal/model"
)

// spyBackend records whether Explain was called, so the fail-closed test can prove the raw
// payload never reached the inner backend.
type spyBackend struct{ called atomic.Bool }

func (s *spyBackend) Explain(context.Context, ExplainRequest) (ExplainResponse, error) {
	s.called.Store(true)
	return ExplainResponse{}, nil
}
func (s *spyBackend) Name() string { return "spy" }

// failingMasker always fails to mask, simulating a masker error.
type failingMasker struct{}

func (failingMasker) Mask(string) (string, error) { return "", errors.New("boom") }
func (failingMasker) Restore(s string) string     { return s }

// TestFailClosedSkipsInnerBackend: when masking fails, the escalation is skipped — Explain
// returns an error and the inner backend is NEVER called, so no raw payload can escape. The
// masker is injected via the unexported seam, which is why this test lives in-package.
func TestFailClosedSkipsInnerBackend(t *testing.T) {
	spy := &spyBackend{}
	a := &anonymizing{inner: spy, newMask: func(...string) masker { return failingMasker{} }}

	req := ExplainRequest{Trigger: model.LogLine{Raw: "sensitive 10.0.0.5"}}
	_, err := a.Explain(context.Background(), req)
	if err == nil {
		t.Fatal("Explain returned nil error on a masking failure, want it to fail closed")
	}
	if spy.called.Load() {
		t.Fatal("the inner backend was called despite a masking failure: raw payload could escape")
	}
	if !strings.Contains(err.Error(), "anonymization failed") {
		t.Errorf("error = %q, want it to name the anonymization failure", err.Error())
	}
	// The failure is fatal (no retry can fix a deterministic masking error).
	if transient(err) {
		t.Error("a masking failure was classified transient; it must not be retried")
	}
}

// TestHostListCapFailsClosedLikeResidue: the anonymizer's other fail-closed path (#67). Past its
// cap the host-list rule stops and Mask returns an error, and that error must reach the user by
// exactly the road a residue hit takes -- maskErr, fatal, the inner backend never called, so the
// card reads "explanation unavailable" with the reason and the export records it -- with a reason
// that names the cause. The real anonymizer, not a stub: what is pinned is that this error is a
// Mask error like any other, with no channel of its own.
func TestHostListCapFailsClosedLikeResidue(t *testing.T) {
	spy := &spyBackend{}
	ms := make([]string, 70)
	for i := range ms {
		ms[i] = "rs" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".acme.com"
	}
	req := ExplainRequest{
		Trigger: model.LogLine{Raw: "FATAL lost mongodb://" + strings.Join(ms, ",") + "/db"},
	}
	_, err := NewAnonymizing(spy).Explain(context.Background(), req)
	if err == nil {
		t.Fatal("Explain returned nil error past the host-list cap, want it to fail closed")
	}
	if spy.called.Load() {
		t.Fatal("the inner backend was called past the host-list cap: the unreached members could escape")
	}
	if want := "anonymization failed, escalation skipped: anonymize: a host list"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want the residue path's wrapping and a cause that names the host list (%q...)", err.Error(), want)
	}
	if transient(err) {
		t.Error("a host-list cap failure was classified transient; it must not be retried")
	}
}
