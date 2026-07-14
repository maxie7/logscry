// SPDX-License-Identifier: Apache-2.0

package llm

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

	"github.com/maxie7/logscry/internal/model"
)

// No test in this package touches the network: every one of them stands up an
// httptest.Server and points the backend at it.

// testRequest is a representative escalation.
func testRequest() ExplainRequest {
	return ExplainRequest{
		Trigger:   model.LogLine{Source: "docker:api", Stream: model.Stderr, Level: "PANIC", Raw: "panic: nil map write"},
		Context:   []string{"GET /orders 200", "GET /orders 200"},
		Template:  "panic: nil map write",
		Count:     1,
		FirstSeen: time.Now(),
	}
}

// testConfig points a backend at srv with no retry backoff, so a test that exercises the
// retry path does not pay for it in wall-clock time.
func testConfig(srv *httptest.Server) Config {
	cfg := Defaults()
	cfg.BaseURL = srv.URL + "/v1"
	cfg.Model = "test-model"
	cfg.Backoff = 0
	cfg.Timeout = 2 * time.Second
	cfg.HTTPClient = srv.Client()
	return cfg
}

// reply writes a chat-completions response whose single choice carries content.
func reply(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"content": content}}},
	})
}

// counted wraps h with a request counter — the fake server's request count is how the
// cost and retry guarantees get proved, rather than taken on trust.
func counted(h http.HandlerFunc) (http.HandlerFunc, *atomic.Int64) {
	var n atomic.Int64
	return func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}, &n
}

func TestExplainHappyPath(t *testing.T) {
	var got struct {
		Model          string  `json:"model"`
		Temperature    float64 `json:"temperature"`
		MaxTokens      int     `json:"max_tokens"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format"`
		Messages []message `json:"messages"`
	}
	var gotPath, gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		reply(w, `{"summary":"A handler wrote to a nil map.","likely_cause":"The cache map is never made.","suggestion":"Initialise it in NewServer."}`)
	}))
	defer srv.Close()

	cfg := testConfig(srv)
	cfg.APIKey = "sk-test-key"
	resp, err := NewOpenAICompatible(cfg).Explain(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Explain failed: %v", err)
	}

	want := ExplainResponse{
		Summary:     "A handler wrote to a nil map.",
		LikelyCause: "The cache map is never made.",
		Suggestion:  "Initialise it in NewServer.",
	}
	if resp != want {
		t.Errorf("response\n got %+v\nwant %+v", resp, want)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("posted to %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-test-key" {
		t.Errorf("Authorization = %q, want the configured key", gotAuth)
	}

	// The request shape is a cost guarantee as much as the rate limiter is: a low
	// temperature for parseable output, and a hard cap on the tokens spent.
	if got.Model != "test-model" {
		t.Errorf("model = %q, want test-model", got.Model)
	}
	if got.Temperature != cfg.Temperature || got.Temperature > 0.2 {
		t.Errorf("temperature = %g, want the configured %g (and low)", got.Temperature, cfg.Temperature)
	}
	if got.MaxTokens != cfg.MaxTokens {
		t.Errorf("max_tokens = %d, want the configured %d", got.MaxTokens, cfg.MaxTokens)
	}
	if got.ResponseFormat == nil || got.ResponseFormat.Type != "json_object" {
		t.Errorf("response_format = %+v, want json_object (json mode is on by default)", got.ResponseFormat)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v, want a system and a user message", got.Messages)
	}
	// The evidence has to actually reach the model, or the explanation is fiction.
	for _, want := range []string{"panic: nil map write", "GET /orders 200", "docker:api", "stderr", "PANIC"} {
		if !strings.Contains(got.Messages[1].Content, want) {
			t.Errorf("user prompt is missing %q:\n%s", want, got.Messages[1].Content)
		}
	}
}

// TestExplainOmitsAuthWithoutKey: a local Ollama needs no key, and sending a bare
// "Bearer " is not the same as sending nothing.
func TestExplainOmitsAuthWithoutKey(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		reply(w, `{"summary":"ok"}`)
	}))
	defer srv.Close()

	cfg := testConfig(srv) // no APIKey
	if _, err := NewOpenAICompatible(cfg).Explain(context.Background(), testRequest()); err != nil {
		t.Fatalf("Explain failed: %v", err)
	}
	if hadAuth {
		t.Error("an Authorization header was sent with no key configured")
	}
}

// TestExplainRetriesTransient: 500 and 429 are worth another go — the model may be
// restarting, or the provider may be throttling for a second. The retry is BOUNDED:
// asserting against the server's own request count is what proves it cannot storm.
func TestExplainRetriesTransient(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			handler, calls := counted(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "try again later", status)
			})
			srv := httptest.NewServer(handler)
			defer srv.Close()

			cfg := testConfig(srv)
			cfg.Retries = 2
			_, err := NewOpenAICompatible(cfg).Explain(context.Background(), testRequest())
			if err == nil {
				t.Fatal("Explain succeeded against a server that only fails")
			}
			if got, want := calls.Load(), int64(cfg.Retries+1); got != want {
				t.Errorf("made %d attempts, want exactly %d (one call plus %d retries)", got, want, cfg.Retries)
			}
		})
	}
}

// TestExplainRecoversAfterTransientFailure: the retry has to actually be worth making.
func TestExplainRecoversAfterTransientFailure(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "warming up", http.StatusServiceUnavailable)
			return
		}
		reply(w, `{"summary":"recovered"}`)
	}))
	defer srv.Close()

	resp, err := NewOpenAICompatible(testConfig(srv)).Explain(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Explain did not recover on the retry: %v", err)
	}
	if resp.Summary != "recovered" {
		t.Errorf("summary = %q, want the retried answer", resp.Summary)
	}
}

// TestExplainDoesNotRetry4xx: a bad key or a bad model name will fail identically
// forever. Retrying it wastes time the tail does not have and, against a metered
// provider, money — so it is surfaced immediately instead, saying what to fix.
func TestExplainDoesNotRetry4xx(t *testing.T) {
	tests := []struct {
		status   int
		wantHint string
	}{
		{http.StatusUnauthorized, "LOGSCRY_API_KEY"},
		{http.StatusNotFound, "--llm-model"},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			handler, calls := counted(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "no", tt.status)
			})
			srv := httptest.NewServer(handler)
			defer srv.Close()

			cfg := testConfig(srv)
			cfg.Retries = 2
			_, err := NewOpenAICompatible(cfg).Explain(context.Background(), testRequest())
			if err == nil {
				t.Fatal("Explain succeeded against a server that only fails")
			}
			if calls.Load() != 1 {
				t.Errorf("made %d attempts on a %d, want exactly 1: a 4xx is not retried",
					calls.Load(), tt.status)
			}
			if !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("error %q does not tell the user to check %q", err, tt.wantHint)
			}
		})
	}
}

// TestExplainTimesOut: a model that accepts the connection and then thinks forever must
// not hold a worker forever. The attempt is abandoned and the card says so.
func TestExplainTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done(): // the client hung up: exactly what we are asserting
		}
	}))
	defer srv.Close()
	defer close(release)

	cfg := testConfig(srv)
	cfg.Timeout = 50 * time.Millisecond
	cfg.Retries = 0

	done := make(chan error, 1)
	go func() {
		_, err := NewOpenAICompatible(cfg).Explain(context.Background(), testRequest())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Explain succeeded against a server that never answers")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("error = %q, want it to say the request timed out", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Explain hung past its own timeout — a stuck model would stall the worker")
	}
}

// TestExplainCancelsInFlight: Ctrl+C must abort the HTTP call, not wait it out. The
// deadline here is the assertion: a regression fails the test instead of hanging the suite.
func TestExplainCancelsInFlight(t *testing.T) {
	reached := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reached)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	cfg := testConfig(srv)
	cfg.Timeout = time.Hour // the context, not the timeout, is what must end this

	done := make(chan error, 1)
	go func() {
		_, err := NewOpenAICompatible(cfg).Explain(ctx, testRequest())
		done <- err
	}()

	<-reached
	cancel()

	select {
	case err := <-done:
		if !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Explain did not return promptly on cancellation: Ctrl+C would hang")
	}
}

// TestJSONModeDowngrade: not every OpenAI-compatible server understands response_format.
// One that rejects it gets the request again without it — once — and is remembered, so
// the session does not pay for the discovery twice.
func TestJSONModeDowngrade(t *testing.T) {
	var sawJSONMode []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ResponseFormat *responseFormat `json:"response_format"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		sawJSONMode = append(sawJSONMode, body.ResponseFormat != nil)

		if body.ResponseFormat != nil {
			http.Error(w, `{"error":"unknown field response_format"}`, http.StatusBadRequest)
			return
		}
		reply(w, `{"summary":"answered without json mode"}`)
	}))
	defer srv.Close()

	b := NewOpenAICompatible(testConfig(srv))
	resp, err := b.Explain(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("the backend did not recover from a rejected response_format: %v", err)
	}
	if resp.Summary != "answered without json mode" {
		t.Errorf("summary = %q, want the downgraded answer", resp.Summary)
	}

	// And it stays downgraded: the next request never asks again.
	if _, err := b.Explain(context.Background(), testRequest()); err != nil {
		t.Fatalf("second Explain failed: %v", err)
	}
	want := []bool{true, false, false}
	if len(sawJSONMode) != len(want) {
		t.Fatalf("server saw %d requests (%v), want %d", len(sawJSONMode), sawJSONMode, len(want))
	}
	for i, w := range want {
		if sawJSONMode[i] != w {
			t.Errorf("request %d sent response_format = %v, want %v (%v)", i+1, sawJSONMode[i], w, sawJSONMode)
		}
	}
}

// TestJSONModeDowngradeSurfacesPersistentFailure: if the request is still rejected with
// the field gone, response_format was never the problem — but the user may still want to
// pin it off, so the error says how.
func TestJSONModeDowngradeSurfacesPersistentFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "malformed request", http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := NewOpenAICompatible(testConfig(srv)).Explain(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Explain succeeded against a server that only fails")
	}
	if !strings.Contains(err.Error(), "--llm-json-mode=false") {
		t.Errorf("error = %q, want it to name the flag that disables json mode", err)
	}
}

// TestTokenBudgetExhausted is a failure found by running against a real local Ollama, not
// by imagining one. A reasoning model thinks out loud into a separate field before it
// answers — so when max_tokens runs out mid-thought, the whole budget is spent and the
// content comes back EMPTY. "The model returned an empty response" sends the user looking
// in entirely the wrong place, so the error names the cap and the flag that raises it.
//
// And it is not retried: the cap is deterministic, so a second attempt burns the same
// tokens to fail identically.
func TestTokenBudgetExhausted(t *testing.T) {
	handler, calls := counted(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"content": "", "reasoning": "Thinking Process: 1. Analyse the log line…"},
				"finish_reason": "length",
			}},
		})
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	cfg := testConfig(srv)
	cfg.Retries = 2
	_, err := NewOpenAICompatible(cfg).Explain(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Explain succeeded on an answer the model never finished")
	}
	if !strings.Contains(err.Error(), "--llm-max-tokens") {
		t.Errorf("error = %q, want it to name the flag that fixes this", err)
	}
	if calls.Load() != 1 {
		t.Errorf("made %d attempts, want exactly 1: the token cap fails the same way every time",
			calls.Load())
	}
}

// TestReasoningIsUsedWhenTheAnswerIsEmpty: a model that thought but did not answer still
// said something, and something beats an empty card.
func TestReasoningIsUsedWhenTheAnswerIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"content": "", "reasoning": "The database refused the connection."},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	resp, err := NewOpenAICompatible(testConfig(srv)).Explain(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Explain discarded the only thing the model said: %v", err)
	}
	if !strings.Contains(resp.Summary, "refused the connection") {
		t.Errorf("summary = %q, want the model's reasoning kept", resp.Summary)
	}
}

// TestExplainDegradesOnGarbage: whatever the model says, the worker gets a result and the
// tail keeps flowing.
func TestExplainDegradesOnGarbage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "I'm sorry, I can't help with that.")
	}))
	defer srv.Close()

	resp, err := NewOpenAICompatible(testConfig(srv)).Explain(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Explain threw away a non-JSON answer: %v", err)
	}
	if !strings.Contains(resp.Summary, "can't help") {
		t.Errorf("summary = %q, want the model's prose kept as the summary", resp.Summary)
	}
}
