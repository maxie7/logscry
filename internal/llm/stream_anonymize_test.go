// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/llm"
	"github.com/maxie7/logscry/internal/model"
)

// TestStreamAnonymizerPlaceholderSplitAcrossChunks is the landmine this feature had to
// avoid. The model echoes <IP_1> back, and the stream splits it — "<IP" in one chunk, "_1>"
// in the next. Restoring per-chunk would emit a mangled "<IP" to the card and never restore
// the real address.
//
// It cannot happen here, and not by special-casing: a progressive update carries only fields
// the JSON decoder saw CLOSED, so any placeholder inside one is necessarily whole. This test
// puts a boundary in the worst possible place and pins that property.
func TestStreamAnonymizerPlaceholderSplitAcrossChunks(t *testing.T) {
	// The answer as the model would write it, referring to the masked values.
	answer := `{"summary":"the worker cannot reach <IP_1>",` +
		`"likely_cause":"<HOST_1> is refusing connections",` +
		`"suggestion":"check the route to <IP_1> from <HOST_1>"}`

	// Split so that every placeholder straddles a boundary: cut right after each "<XX".
	var pieces []string
	rest := answer
	for {
		i := strings.Index(rest, "_1>")
		if i < 0 {
			break
		}
		pieces = append(pieces, sseChunk(rest[:i]))
		rest = rest[i:]
		pieces = append(pieces, sseChunk(rest[:3]))
		rest = rest[3:]
	}
	pieces = append(pieces, sseChunk(rest), sseStop())

	srv, _ := streamServer(t, pieces...)
	inner := streamingBackend(srv, nil)
	b := llm.NewAnonymizing(inner)

	var partials []llm.ExplainResponse
	resp, err := b.Explain(context.Background(), llm.ExplainRequest{
		Trigger:   model.LogLine{Raw: "dial tcp 10.0.0.5: connection refused to db-01.acme.internal"},
		Template:  "dial tcp <IP>: connection refused",
		OnPartial: func(p llm.ExplainResponse) { partials = append(partials, p) },
	})
	if err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	if len(partials) == 0 {
		t.Fatal("no progressive updates were delivered")
	}

	// No update may ever show a half-placeholder, nor an unrestored one.
	for i, p := range partials {
		for _, field := range []string{p.Summary, p.LikelyCause, p.Suggestion} {
			if field == "" {
				continue
			}
			if strings.Contains(field, "<IP") || strings.Contains(field, "<HOST") {
				t.Errorf("partial %d leaked a placeholder fragment: %q", i, field)
			}
		}
	}
	// And the real values came back, in every field.
	if !strings.Contains(resp.Summary, "10.0.0.5") {
		t.Errorf("Summary not restored: %q", resp.Summary)
	}
	if !strings.Contains(resp.LikelyCause, "db-01.acme.internal") {
		t.Errorf("LikelyCause not restored: %q", resp.LikelyCause)
	}
	if !strings.Contains(resp.Suggestion, "10.0.0.5") || !strings.Contains(resp.Suggestion, "db-01.acme.internal") {
		t.Errorf("Suggestion not restored: %q", resp.Suggestion)
	}
}

// TestStreamAnonymizerSalvageTrimsDanglingPlaceholder: a stream that dies mid-placeholder is
// the one case where a fragment CAN reach the card, because salvage falls back to raw prose
// rather than decoded fields. "check <IP" is a mangled half of a value we hold the real
// version of, so it is cut rather than rendered.
func TestStreamAnonymizerSalvageTrimsDanglingPlaceholder(t *testing.T) {
	// No '{' anywhere: this is the prose-salvage path, the only one that can end mid-token.
	srv, _ := tornServer(t, sseChunk("the worker could not reach <IP"))

	b := llm.NewAnonymizing(streamingBackend(srv, func(c *llm.Config) { c.Retries = 0 }))
	resp, err := b.Explain(context.Background(), llm.ExplainRequest{
		Trigger:  model.LogLine{Raw: "dial tcp 10.0.0.5: connection refused"},
		Template: "dial tcp <IP>: connection refused",
	})
	if err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	if !resp.Truncated {
		t.Fatal("a salvaged answer must be marked Truncated")
	}
	if strings.Contains(resp.Summary, "<IP") {
		t.Errorf("a dangling placeholder reached the card: %q", resp.Summary)
	}
	if !strings.Contains(resp.Summary, "the worker could not reach") {
		t.Errorf("trimming ate the real content: %q", resp.Summary)
	}
}

// TestStreamAnonymizerOffUnchanged: with masking off, streaming behaves exactly as it does
// without the decorator — the guarantee that neither feature can disturb the other.
func TestStreamAnonymizerOffUnchanged(t *testing.T) {
	pieces := append(chunksFor(theAnswer, 11), sseStop())
	srv, _ := streamServer(t, pieces...)

	cfg := llm.Defaults()
	cfg.BaseURL = srv.URL + "/v1"
	cfg.HTTPClient = srv.Client()
	cfg.Timeout = 2 * time.Second
	cfg.Stream = true

	resp, err := llm.NewOpenAICompatible(cfg).Explain(context.Background(), llm.ExplainRequest{Template: "x"})
	if err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	if resp.Summary != "the worker cannot reach postgres" || resp.Truncated {
		t.Errorf("unexpected result: %+v", resp)
	}
}
