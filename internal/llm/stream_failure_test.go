// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/llm"
)

// tornServer streams pieces and then hangs up WITHOUT [DONE] or a finish_reason, which is
// what a dropped connection looks like from the client side.
func tornServer(t *testing.T, pieces ...string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range pieces {
			_, _ = io.WriteString(w, p)
			w.(http.Flusher).Flush()
		}
		// Returning without the terminal chunk closes the body mid-answer.
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// chunksFor frames s as SSE deltas of n bytes each.
func chunksFor(s string, n int) []string {
	var out []string
	for _, part := range splitEvery(s, n) {
		out = append(out, sseChunk(part))
	}
	return out
}

// TestStreamTornMidAnswerRetriesThenSalvages: a stream cut mid-answer is transient, so it
// is retried under the Retries budget; when those run out the partial answer still beats an
// empty card, and it is BADGED so nobody mistakes it for the model's finished verdict.
func TestStreamTornMidAnswerRetriesThenSalvages(t *testing.T) {
	half := `{"summary":"the worker cannot reach postgres","likely_cause":"the database`
	srv, calls := tornServer(t, chunksFor(half, 25)...)

	resp, _, err := explainStreamed(t, streamingBackend(srv, func(c *llm.Config) { c.Retries = 2 }))
	if err != nil {
		t.Fatalf("a salvageable answer should not fail: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("provider called %d times, want 3 (the initial attempt plus 2 retries)", got)
	}
	if resp.Summary != "the worker cannot reach postgres" {
		t.Errorf("Summary = %q, want the completed field salvaged", resp.Summary)
	}
	if resp.LikelyCause != "" {
		t.Errorf("LikelyCause = %q, want the half-written field dropped", resp.LikelyCause)
	}
	if !resp.Truncated {
		t.Error("a salvaged answer must be marked Truncated, or the card lies about being complete")
	}
}

// TestStreamTornAfterClosingBraceIsComplete: the model finished its JSON and the connection
// dropped on the way out. That is not a truncated answer — it must not be retried and must
// not be badged, or a disconnect one byte before [DONE] would downgrade a perfect response.
func TestStreamTornAfterClosingBraceIsComplete(t *testing.T) {
	srv, calls := tornServer(t, chunksFor(theAnswer, 30)...)

	resp, _, err := explainStreamed(t, streamingBackend(srv, func(c *llm.Config) { c.Retries = 2 }))
	if err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider called %d times, want 1: a complete answer must not be retried", got)
	}
	if resp.Truncated {
		t.Error("a complete answer was badged incomplete because the connection dropped after it")
	}
	if resp.Suggestion != "run docker compose ps" {
		t.Errorf("Suggestion = %q, want the full answer", resp.Suggestion)
	}
}

// TestStreamTornWithNothingUsableFails: no completed field means there is nothing to show,
// so it degrades to "explanation unavailable" like any other failure.
func TestStreamTornWithNothingUsableFails(t *testing.T) {
	srv, _ := tornServer(t, sseChunk(`{"summ`))
	if _, _, err := explainStreamed(t, streamingBackend(srv, func(c *llm.Config) { c.Retries = 0 })); err == nil {
		t.Fatal("a stream that produced nothing usable should fail, not return an empty answer")
	}
}

// TestStreamOwnDeadlineIsNotRetried: our own --llm-timeout is deterministic. Attempt 2 runs
// into the identical wall after another full timeout, doubling the wall-clock and the
// provider's tokens for the same outcome, so it must go straight to salvage.
func TestStreamOwnDeadlineIsNotRetried(t *testing.T) {
	var calls atomic.Int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range chunksFor(`{"summary":"the worker cannot reach postgres",`, 20) {
			_, _ = io.WriteString(w, p)
			w.(http.Flusher).Flush()
		}
		select { // then stall until the client's deadline kills it
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	resp, _, err := explainStreamed(t, streamingBackend(srv, func(c *llm.Config) {
		c.Retries = 2
		c.Timeout = 250 * time.Millisecond
	}))
	if err != nil {
		t.Fatalf("a salvageable answer should not fail: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider called %d times, want 1: our own deadline must not be retried", got)
	}
	if resp.Summary != "the worker cannot reach postgres" || !resp.Truncated {
		t.Errorf("want the salvaged summary marked truncated, got %+v", resp)
	}
}

// TestStreamByteBudgetIsNotRetried: exhausting our own budget is deterministic too — a
// runaway endpoint, or a small local model stuck in a repetition loop, overruns identically
// on every attempt, and each one burns real provider tokens.
func TestStreamByteBudgetIsNotRetried(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range chunksFor(`{"summary":"the worker cannot reach postgres",`, 20) {
			_, _ = io.WriteString(w, p)
			w.(http.Flusher).Flush()
		}
		// The repetition loop: the same delta forever, never a finish_reason.
		filler := sseChunk(strings.Repeat("looping ", 512))
		for r.Context().Err() == nil {
			if _, err := io.WriteString(w, filler); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	resp, _, err := explainStreamed(t, streamingBackend(srv, func(c *llm.Config) {
		c.Retries = 2
		c.Timeout = 30 * time.Second // the budget, not the clock, must be what stops this
	}))
	if err != nil {
		t.Fatalf("a salvageable answer should not fail: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider called %d times, want 1: our own byte budget must not be retried", got)
	}
	if resp.Summary != "the worker cannot reach postgres" || !resp.Truncated {
		t.Errorf("want the salvaged summary marked truncated, got %+v", resp)
	}
}

// TestStreamNormalLargeResponseIsNotTorn is the sizing regression guard. A 4096-token answer
// is ~1 MiB of SSE envelopes — right at the OLD maxBodyBytes — so reusing that cap on the
// wire would report a perfectly good response as torn and hand back a badged card.
func TestStreamNormalLargeResponseIsNotTorn(t *testing.T) {
	// Pad inside a JSON string field so the body stays a single valid object.
	padding := strings.Repeat("the worker retried and failed again. ", 2500)
	body, _ := json.Marshal(map[string]string{
		"summary":      "the worker cannot reach postgres",
		"likely_cause": padding,
		"suggestion":   "run docker compose ps",
	})

	// Four bytes per delta, i.e. one token each, which is what makes the envelope overhead
	// dominate exactly as it does against a real provider.
	pieces := append(chunksFor(string(body), 4), sseStop())
	wire := 0
	for _, p := range pieces {
		wire += len(p)
	}
	if wire < 1<<20 {
		t.Fatalf("premise broken: test body is only %d bytes, need > 1 MiB on the wire", wire)
	}

	srv, calls := streamServer(t, pieces...)
	resp, _, err := explainStreamed(t, streamingBackend(srv, nil))
	if err != nil {
		t.Fatalf("a normal large response failed: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider called %d times, want 1", got)
	}
	if resp.Truncated {
		t.Errorf("a %d-byte response was wrongly reported torn — the wire budget is too tight", wire)
	}
	if resp.Summary != "the worker cannot reach postgres" {
		t.Errorf("Summary = %q", resp.Summary)
	}
}

// TestStreamFinishReasonLengthKeepsItsDiagnostic: a max_tokens cut-off is a CLEAN end, so it
// must not be swallowed by the salvage path. A reasoning model that spends its whole budget
// thinking still gets the error naming the flag, rather than silently becoming a partial
// card that never mentions --llm-max-tokens.
func TestStreamFinishReasonLengthKeepsItsDiagnostic(t *testing.T) {
	srv, _ := streamServer(t,
		"data: {\"choices\":[{\"delta\":{\"reasoning\":\"hmm...\"},\"finish_reason\":\"length\"}]}\n\n",
		"data: [DONE]\n\n",
	)
	_, _, err := explainStreamed(t, streamingBackend(srv, nil))
	if err == nil || !strings.Contains(err.Error(), "--llm-max-tokens") {
		t.Fatalf("err = %v, want the max-tokens diagnostic naming the flag", err)
	}
}

// TestStreamFinishReasonLengthWithContentIsNotTruncated: the same clean end WITH an answer
// stays an ordinary result. It is truncated in the model's sense, but it is not a torn
// stream, and conflating the two is what would lose the diagnostic above.
func TestStreamFinishReasonLengthWithContentIsNotTruncated(t *testing.T) {
	srv, _ := streamServer(t,
		sseChunk(`{"summary":"the worker cannot reach postgres","likely_cause":"the datab`),
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n",
	)
	resp, _, err := explainStreamed(t, streamingBackend(srv, nil))
	if err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	if resp.Truncated {
		t.Error("a clean finish_reason:length end was badged as a torn stream")
	}
	if resp.Summary != "the worker cannot reach postgres" {
		t.Errorf("Summary = %q", resp.Summary)
	}
}

// TestStreamRejectionFallsBackToNonStreaming: a server that refuses stream + response_format
// gets exactly ONE non-streaming retry, and the user still gets a normal explanation.
// Streaming is dropped before json mode because parse quality matters more than when fields
// appear.
func TestStreamRejectionFallsBackToNonStreaming(t *testing.T) {
	var calls atomic.Int64
	var sawStream, sawFormatAfterFallback atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)

		if stream, _ := req["stream"].(bool); stream {
			sawStream.Store(true)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"stream is not supported with response_format"}`)
			return
		}
		if _, ok := req["response_format"]; ok {
			sawFormatAfterFallback.Store(true)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":`+jsonQuote(theAnswer)+`}}]}`)
	}))
	defer srv.Close()

	b := streamingBackend(srv, func(c *llm.Config) { c.Retries = 0 })
	resp, _, err := explainStreamed(t, b)
	if err != nil {
		t.Fatalf("the fallback should have produced a normal explanation: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("provider called %d times, want 2 (rejected stream, then one retry without it)", got)
	}
	if !sawStream.Load() {
		t.Error("the first attempt did not ask for streaming")
	}
	if !sawFormatAfterFallback.Load() {
		t.Error("response_format was dropped along with stream; json mode must survive the downgrade")
	}
	if resp.Summary != "the worker cannot reach postgres" {
		t.Errorf("Summary = %q", resp.Summary)
	}

	// The downgrade is remembered: a second escalation does not re-probe.
	calls.Store(0)
	if _, _, err := explainStreamed(t, b); err != nil {
		t.Fatalf("second Explain errored: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider called %d times on the second request, want 1: the downgrade must stick", got)
	}
}

// jsonQuote renders s as a JSON string literal.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestStreamOffSendsNoStreamField pins the default-off guarantee at the wire: with streaming
// disabled the request carries no "stream" key at all, so it is byte-for-byte the request
// this backend has always sent.
func TestStreamOffSendsNoStreamField(t *testing.T) {
	var body atomic.Pointer[string]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s := string(raw)
		body.Store(&s)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":`+jsonQuote(theAnswer)+`}}]}`)
	}))
	defer srv.Close()

	b := streamingBackend(srv, func(c *llm.Config) { c.Stream = false })
	if _, err := b.Explain(context.Background(), llm.ExplainRequest{Template: "x"}); err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	// The word "stream" appears in the system prompt, so the assertion is on the JSON KEY.
	if sent := *body.Load(); strings.Contains(sent, `"stream"`) {
		t.Errorf("streaming off must not send a stream field:\n%s", sent)
	}
}
