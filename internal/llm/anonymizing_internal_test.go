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
	a := &anonymizing{inner: spy, newMask: func() masker { return failingMasker{} }}

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
