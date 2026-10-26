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
	inner Backend
	// newMask builds one fresh mapper per request, told every text the request will show the model;
	// injected so a test can force a failure.
	newMask func(texts ...string) masker
}

// masker is the reversible per-request mapping anonymizing depends on. anonymize.Mapper
// satisfies it; the interface exists so a test can substitute one that fails.
type masker interface {
	Mask(string) (string, error)
	Restore(string) string
}

// NewAnonymizing wraps inner so that every outgoing ExplainRequest is masked and every
// ExplainResponse restored. extraHostSuffixes extends the bare-hostname allowlist.
//
// Each request's mapper reserves every placeholder its texts carry before the first Mask (#142),
// so a literal "<HOST_1>" in a context line is never the token minted for the trigger's host.
func NewAnonymizing(inner Backend, extraHostSuffixes ...string) Backend {
	return &anonymizing{
		inner: inner,
		newMask: func(texts ...string) masker {
			m := anonymize.New(extraHostSuffixes...)
			m.Reserve(texts...)
			return m
		},
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
// What "masked" covers is the anonymizer's business, not this seam's: see anonymize.Mask's
// honest-scope note for what the detectors do and do not reach. Fail-closed is a claim about
// the error path, not a claim of completeness.
//
// One mapper serves the whole request, so a value that appears in both the trigger and the
// context gets the same placeholder and the model can see it recur. The mapper holds the
// only copy of the token→original map, in memory, and is discarded when Explain returns.
//
// The fields are one list, built before the mapper, because the mapper must see all of them
// before it masks any (#142): a placeholder a later field carries as text is reserved before an
// earlier field's value can be given it. Level, Stream and Count are the rest of the prompt, and
// none can carry one: a level is canonicalized to a known name.
func (a *anonymizing) Explain(ctx context.Context, req ExplainRequest) (ExplainResponse, error) {
	masked := req // copy; the caller's request keeps its real values
	if len(req.Context) > 0 {
		masked.Context = append([]string(nil), req.Context...)
	}
	fields := []*string{&masked.Trigger.Raw, &masked.Trigger.Source, &masked.Template}
	for i := range masked.Context {
		fields = append(fields, &masked.Context[i])
	}
	texts := make([]string, len(fields))
	for i, f := range fields {
		texts[i] = *f
	}
	m := a.newMask(texts...)

	if req.OnPartial != nil {
		// Restore over each PROGRESSIVE update too, or the card would show <IP_1> until the
		// answer finished. A placeholder split across chunks is not a hazard here: a partial
		// carries only fields the JSON decoder saw closed, so any placeholder inside one is
		// necessarily whole. That is a property of where the seam sits, not a special case.
		masked.OnPartial = func(p ExplainResponse) {
			req.OnPartial(a.restore(m, p))
		}
	}
	for _, f := range fields {
		var err error
		if *f, err = m.Mask(*f); err != nil {
			return ExplainResponse{}, maskErr(err)
		}
	}

	resp, err := a.inner.Explain(ctx, masked)
	if err != nil {
		return resp, err
	}

	return a.restore(m, resp), nil
}

// restore puts the real values back into every field of one response.
//
// A TRUNCATED answer gets one extra step: it was salvaged from a stream that stopped
// wherever the connection died, so its text can end mid-placeholder ("check <IP"), which
// matches nothing and would render as that fragment. Cutting the stub is limited to the
// salvage case on purpose — a complete answer cannot produce one, and the non-streaming
// path must stay exactly as it was.
func (a *anonymizing) restore(m masker, resp ExplainResponse) ExplainResponse {
	// The model echoes placeholders back ("check connectivity to <IP_1>"): restore all
	// three fields before the card renders.
	resp.Summary = m.Restore(resp.Summary)
	resp.LikelyCause = m.Restore(resp.LikelyCause)
	resp.Suggestion = m.Restore(resp.Suggestion)
	if resp.Truncated {
		resp.Summary = anonymize.TrimDanglingPlaceholder(resp.Summary)
		resp.LikelyCause = anonymize.TrimDanglingPlaceholder(resp.LikelyCause)
		resp.Suggestion = anonymize.TrimDanglingPlaceholder(resp.Suggestion)
	}
	return resp
}

// maskErr marks an anonymization failure as fatal: no retry can fix it (the same input
// masks the same way every time), and it must read clearly on the card. It carries no log
// content — only that masking failed and why by type.
func maskErr(err error) error {
	return fatalError{msg: fmt.Sprintf("anonymization failed, escalation skipped: %v", err)}
}
