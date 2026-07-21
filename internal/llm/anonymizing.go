// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"fmt"

	"github.com/maxie7/logscry/internal/anonymize"
)

// anonymizing is a Backend decorator that masks sensitive values out of the payload before
// it leaves the process and restores them in the answer that comes back. It is the whole
// seam: the pipeline, the template state, and the TUI keep the REAL values — masking
// happens here, on the way out, and nowhere else.
//
// It wraps an inner Backend rather than living inside OpenAICompatible so that with the
// flag off the exact unwrapped backend runs and the payload is byte-for-byte what it was
// before this feature existed.
type anonymizing struct {
	inner   Backend
	newMask func() masker // one fresh mapper per request; injected so a test can force a failure
}

// masker is the reversible per-request mapping anonymizing depends on. anonymize.Mapper
// satisfies it; the interface exists so a test can substitute one that fails.
type masker interface {
	Mask(string) (string, error)
	Restore(string) string
}

// NewAnonymizing wraps inner so that every outgoing ExplainRequest is masked and every
// ExplainResponse restored. extraHostSuffixes extends the bare-hostname allowlist.
func NewAnonymizing(inner Backend, extraHostSuffixes ...string) Backend {
	return &anonymizing{
		inner:   inner,
		newMask: func() masker { return anonymize.New(extraHostSuffixes...) },
	}
}

// Name implements Backend.
func (a *anonymizing) Name() string { return a.inner.Name() + "+anonymized" }

// Explain masks the request, calls the inner backend, and restores the response. It FAILS
// CLOSED: if any field cannot be masked, it returns the error WITHOUT calling inner, so the
// raw payload never leaves the process — the pool renders that as an "explanation
// unavailable" card with the reason. A privacy feature that silently sends plaintext on
// error would be worse than none.
//
// One mapper serves the whole request, so a value that appears in both the trigger and the
// context gets the same placeholder and the model can see it recur. The mapper holds the
// only copy of the token→original map, in memory, and is discarded when Explain returns.
func (a *anonymizing) Explain(ctx context.Context, req ExplainRequest) (ExplainResponse, error) {
	m := a.newMask()

	masked := req // copy; the caller's request keeps its real values
	var err error
	if masked.Trigger.Raw, err = m.Mask(req.Trigger.Raw); err != nil {
		return ExplainResponse{}, maskErr(err)
	}
	if masked.Trigger.Source, err = m.Mask(req.Trigger.Source); err != nil {
		return ExplainResponse{}, maskErr(err)
	}
	if masked.Template, err = m.Mask(req.Template); err != nil {
		return ExplainResponse{}, maskErr(err)
	}
	if len(req.Context) > 0 {
		masked.Context = make([]string, len(req.Context))
		for i, line := range req.Context {
			if masked.Context[i], err = m.Mask(line); err != nil {
				return ExplainResponse{}, maskErr(err)
			}
		}
	}

	resp, err := a.inner.Explain(ctx, masked)
	if err != nil {
		return resp, err
	}

	// The model echoes placeholders back ("check connectivity to <IP_1>"): restore all
	// three fields before the card renders.
	resp.Summary = m.Restore(resp.Summary)
	resp.LikelyCause = m.Restore(resp.LikelyCause)
	resp.Suggestion = m.Restore(resp.Suggestion)
	return resp, nil
}

// maskErr marks an anonymization failure as fatal: no retry can fix it (the same input
// masks the same way every time), and it must read clearly on the card. It carries no log
// content — only that masking failed and why by type.
func maskErr(err error) error {
	return fatalError{msg: fmt.Sprintf("anonymization failed, escalation skipped: %v", err)}
}
