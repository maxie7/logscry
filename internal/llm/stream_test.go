// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/llm"
	"github.com/maxie7/logscry/internal/model"
)

// sseChunk renders one chat.completion.chunk exactly as a provider frames it.
func sseChunk(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": content}}},
	})
	return "data: " + string(b) + "\n\n"
}

// sseStop renders the terminal chunk plus the [DONE] sentinel.
func sseStop() string {
	return "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
}

// splitEvery cuts s into len-n pieces so a handler can dribble a body out in slices that
// fall wherever they like relative to the framing.
func splitEvery(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

// streamServer answers with the given SSE pieces, flushing after each so the client really
// does see them arrive separately rather than as one buffered body.
func streamServer(t *testing.T, pieces ...string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range pieces {
			_, _ = io.WriteString(w, p)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// streamingBackend builds a backend pointed at srv with streaming on.
func streamingBackend(srv *httptest.Server, tweak func(*llm.Config)) llm.Backend {
	cfg := llm.Defaults()
	cfg.BaseURL = srv.URL + "/v1"
	cfg.HTTPClient = srv.Client()
	cfg.Timeout = 2 * time.Second
	cfg.Backoff = 0
	cfg.Stream = true
	if tweak != nil {
		tweak(&cfg)
	}
	return llm.NewOpenAICompatible(cfg)
}

// explainStreamed runs one streamed Explain, recording every progressive update.
func explainStreamed(t *testing.T, b llm.Backend) (llm.ExplainResponse, []llm.ExplainResponse, error) {
	t.Helper()
	var partials []llm.ExplainResponse
	req := llm.ExplainRequest{
		Trigger:  model.LogLine{Raw: "PANIC: nil map write"},
		Template: "PANIC: nil map write",
		OnPartial: func(p llm.ExplainResponse) {
			partials = append(partials, p)
		},
	}
	resp, err := b.Explain(context.Background(), req)
	return resp, partials, err
}

// theAnswer is one JSON explanation, used wherever a test needs a well-formed answer.
const theAnswer = `{"summary":"the worker cannot reach postgres",` +
	`"likely_cause":"the database container is down",` +
	`"suggestion":"run docker compose ps"}`

// TestStreamProgressiveFields is the feature: fields appear one at a time as the model
// completes them, and a half-written field is NEVER emitted. The body is sliced into 7-byte
// pieces so most boundaries land mid-value.
func TestStreamProgressiveFields(t *testing.T) {
	var pieces []string
	for _, part := range splitEvery(theAnswer, 7) {
		pieces = append(pieces, sseChunk(part))
	}
	pieces = append(pieces, sseStop())

	srv, _ := streamServer(t, pieces...)
	resp, partials, err := explainStreamed(t, streamingBackend(srv, nil))
	if err != nil {
		t.Fatalf("Explain errored: %v", err)
	}

	if len(partials) == 0 {
		t.Fatal("no progressive updates were delivered")
	}
	// Fields only ever accumulate, and every value that appears is already final: a
	// partial that later CHANGED would mean a half-written value had been rendered.
	for i, p := range partials {
		for _, f := range []struct{ got, want string }{
			{p.Summary, resp.Summary},
			{p.LikelyCause, resp.LikelyCause},
			{p.Suggestion, resp.Suggestion},
		} {
			if f.got != "" && f.got != f.want {
				t.Errorf("partial %d rendered a half-written field %q, final is %q", i, f.got, f.want)
			}
		}
	}
	if first := partials[0]; first.Summary == "" || first.LikelyCause != "" {
		t.Errorf("first update should carry the summary alone, got %+v", first)
	}
	if resp.Truncated {
		t.Error("a cleanly finished stream must not be marked truncated")
	}
}

// TestStreamEquivalence: streaming changes WHEN fields appear, never what they are. Same
// content, streamed and not, must parse to the same explanation.
func TestStreamEquivalence(t *testing.T) {
	var pieces []string
	for _, part := range splitEvery(theAnswer, 3) {
		pieces = append(pieces, sseChunk(part))
	}
	pieces = append(pieces, sseStop())
	srvStream, _ := streamServer(t, pieces...)

	srvWhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, theAnswer)
	}))
	t.Cleanup(srvWhole.Close)

	streamed, _, err := explainStreamed(t, streamingBackend(srvStream, nil))
	if err != nil {
		t.Fatalf("streamed Explain errored: %v", err)
	}
	whole, err := streamingBackend(srvWhole, func(c *llm.Config) { c.Stream = false }).
		Explain(context.Background(), llm.ExplainRequest{Template: "x"})
	if err != nil {
		t.Fatalf("non-streamed Explain errored: %v", err)
	}
	if streamed != whole {
		t.Errorf("streamed %+v != non-streamed %+v", streamed, whole)
	}
}

// TestStreamFencedJSONStillProgresses: --llm-json-mode=false only stops response_format
// going on the wire; the prompt still demands JSON, so models still send it — often fenced
// or after a preamble. Progressive display is content-driven, so it must survive that.
func TestStreamFencedJSONStillProgresses(t *testing.T) {
	body := "Sure! Here's the analysis:\n```json\n" + theAnswer + "\n```"
	var pieces []string
	for _, part := range splitEvery(body, 9) {
		pieces = append(pieces, sseChunk(part))
	}
	pieces = append(pieces, sseStop())

	srv, _ := streamServer(t, pieces...)
	resp, partials, err := explainStreamed(t, streamingBackend(srv, func(c *llm.Config) { c.JSONMode = false }))
	if err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	if len(partials) == 0 {
		t.Fatal("fenced JSON produced no progressive updates")
	}
	if resp.Summary != "the worker cannot reach postgres" {
		t.Errorf("Summary = %q", resp.Summary)
	}
	// The fence and the preamble are framing, not content: neither may ever reach a card.
	for i, p := range partials {
		for _, field := range []string{p.Summary, p.LikelyCause, p.Suggestion} {
			if strings.Contains(field, "```") || strings.Contains(field, "Sure!") {
				t.Errorf("partial %d leaked raw buffer text: %q", i, field)
			}
		}
	}
}

// TestStreamProseYieldsNoPartials: a model that ignores the format entirely has no
// completed FIELDS to show, so nothing is emitted mid-stream — the card simply stays
// "explaining…" — and the final parse degrades to prose exactly as it always has.
func TestStreamProseYieldsNoPartials(t *testing.T) {
	prose := "The worker could not reach Postgres and gave up after three retries."
	var pieces []string
	for _, part := range splitEvery(prose, 5) {
		pieces = append(pieces, sseChunk(part))
	}
	pieces = append(pieces, sseStop())

	srv, _ := streamServer(t, pieces...)
	resp, partials, err := explainStreamed(t, streamingBackend(srv, nil))
	if err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	if len(partials) != 0 {
		t.Errorf("prose produced %d progressive updates, want none: %+v", len(partials), partials)
	}
	if resp.Summary != prose {
		t.Errorf("final parse should degrade to prose, got %q", resp.Summary)
	}
}

// TestStreamMalformedChunksSkipped: garbage events between good ones cost nothing.
func TestStreamMalformedChunksSkipped(t *testing.T) {
	parts := splitEvery(theAnswer, 20)
	var pieces []string
	for _, part := range parts {
		pieces = append(pieces, sseChunk(part), "data: {not json\n\n")
	}
	pieces = append(pieces, sseStop())

	srv, _ := streamServer(t, pieces...)
	resp, _, err := explainStreamed(t, streamingBackend(srv, nil))
	if err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	if resp.Summary != "the worker cannot reach postgres" {
		t.Errorf("Summary = %q, want the answer intact despite malformed chunks", resp.Summary)
	}
}
