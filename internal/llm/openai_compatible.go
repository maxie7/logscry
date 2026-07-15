// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// maxBodyBytes bounds what we read back. A misconfigured base URL can point at
// anything — including something that streams forever — and that must not become
// logscry's memory problem.
const maxBodyBytes = 1 << 20

// maxErrBodyRunes bounds the provider's error text quoted back to the user. Enough to
// say "model 'gemma2:2b' not found", not enough to fill the status bar with a stack of
// someone else's HTML.
const maxErrBodyRunes = 200

// OpenAICompatible talks to any OpenAI-compatible chat-completions endpoint. A single
// configurable base URL + model + API key covers OpenAI, Groq, and local Ollama (which
// exposes an OpenAI-compatible API), which is why v1 ships one backend rather than three.
//
// It is safe for concurrent use by the worker pool: the only mutable state is jsonOff.
type OpenAICompatible struct {
	cfg    Config
	client *http.Client

	// jsonOff records that this server rejected response_format, so every later request
	// this session omits it. Set at most once per worker that hits the rejection, and
	// never unset: the capability cannot come back mid-run.
	jsonOff atomic.Bool
}

// NewOpenAICompatible constructs a backend for the configured endpoint.
func NewOpenAICompatible(cfg Config) *OpenAICompatible {
	client := cfg.HTTPClient
	if client == nil {
		// No client timeout: the per-attempt context deadline is the timeout, and it is
		// also what makes Ctrl+C abort an in-flight request instead of waiting it out.
		client = &http.Client{}
	}
	return &OpenAICompatible{cfg: cfg, client: client}
}

// Name implements Backend.
func (b *OpenAICompatible) Name() string { return "openai-compatible" }

// Explain implements Backend: it asks the model for a JSON explanation of the escalated
// event and parses whatever comes back defensively (see parseExplanation).
//
// Failure is expected, not exceptional — the model will be down, slow, rate-limited, or
// behind a bad key — so the error taxonomy is the point:
//
//   - transient (network, timeout, 429, 5xx): retried, bounded by Retries;
//   - fatal (4xx): NOT retried. A bad key or a bad model name fails identically every
//     time, and retrying it is how a retry storm starts. It is surfaced instead;
//   - a 400/422 while response_format was sent: the one exception. Some OpenAI-compatible
//     servers reject the unknown field, so we drop it, remember that for the session, and
//     try once more. This is a capability downgrade, not a retry: it cannot loop.
func (b *OpenAICompatible) Explain(ctx context.Context, req ExplainRequest) (ExplainResponse, error) {
	sentJSONMode := b.jsonMode()

	resp, err := b.attempt(ctx, req, sentJSONMode)
	if err == nil || !sentJSONMode || !rejectsRequest(err) {
		return resp, err
	}

	b.jsonOff.Store(true)
	resp, err = b.attempt(ctx, req, false)
	if rejectsRequest(err) {
		// The field was not the problem after all, so the user is now looking at a
		// request the server dislikes for another reason — but they may also want to
		// pin json mode off rather than rely on this downgrade every run.
		return resp, fmt.Errorf("%w (retried without response_format; pass --llm-json-mode=false to disable it permanently)", err)
	}
	return resp, err
}

// jsonMode reports whether this request should ask for a JSON object: configured on,
// and not already refused by this server.
func (b *OpenAICompatible) jsonMode() bool { return b.cfg.JSONMode && !b.jsonOff.Load() }

// attempt makes the call, retrying only transient failures with a short backoff.
func (b *OpenAICompatible) attempt(ctx context.Context, req ExplainRequest, jsonMode bool) (ExplainResponse, error) {
	var lastErr error
	for i := 0; i <= b.cfg.Retries; i++ {
		if i > 0 && !sleep(ctx, b.cfg.Backoff) {
			return ExplainResponse{}, ctx.Err()
		}

		resp, err := b.call(ctx, req, jsonMode)
		if err == nil {
			return resp, nil
		}
		// Shutdown (Ctrl+C) is not a failure to retry: it is a reason to stop now.
		if ctx.Err() != nil {
			return ExplainResponse{}, ctx.Err()
		}
		lastErr = err
		if !transient(err) {
			return ExplainResponse{}, err
		}
	}
	return ExplainResponse{}, lastErr
}

// call makes one HTTP request, bounded by its own timeout. The context it derives is
// the request's: cancelling the parent aborts the connection mid-flight rather than
// leaving the tail waiting on a model that is never going to answer.
func (b *OpenAICompatible) call(ctx context.Context, req ExplainRequest, jsonMode bool) (ExplainResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout)
	defer cancel()

	body, err := json.Marshal(b.chatRequest(req, jsonMode))
	if err != nil {
		return ExplainResponse{}, fmt.Errorf("encoding request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint(), bytes.NewReader(body))
	if err != nil {
		return ExplainResponse{}, fmt.Errorf("building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	b.setAuth(httpReq)

	httpResp, err := b.client.Do(httpReq)
	if err != nil {
		// A per-attempt deadline is a timeout; a cancelled parent is a shutdown. Both
		// arrive here as one error, so say which it was — "timed out" and "cancelled"
		// send the user to very different places.
		if errors.Is(err, context.DeadlineExceeded) {
			return ExplainResponse{}, fmt.Errorf("request timed out after %s: %w", b.cfg.Timeout, err)
		}
		return ExplainResponse{}, fmt.Errorf("calling %s: %w", b.endpoint(), err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(httpResp.Body, maxBodyBytes))
	if err != nil {
		return ExplainResponse{}, fmt.Errorf("reading response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return ExplainResponse{}, &apiError{status: httpResp.StatusCode, body: b.redact(string(payload))}
	}

	var decoded chatResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return ExplainResponse{}, fmt.Errorf("decoding response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return ExplainResponse{}, errors.New("model returned no choices")
	}
	return content(decoded.Choices[0])
}

// content extracts what the model actually said, which is not always in the obvious place.
//
// A reasoning model thinks out loud into a separate field and only then writes its answer
// — so if max_tokens runs out mid-thought, the answer field comes back EMPTY while the
// whole budget was spent. "The model returned nothing" is a useless thing to tell someone
// in that situation, and retrying is worse than useless: the cap is deterministic, so the
// second attempt burns the same tokens to fail identically. Say what happened and what to
// change instead. (Observed with a local Ollama the moment the model was a reasoning one.)
func content(c choice) (ExplainResponse, error) {
	if text := strings.TrimSpace(c.Message.Content); text != "" {
		return parseExplanation(text)
	}
	if c.FinishReason == "length" {
		return ExplainResponse{}, fatalError{
			msg: "the model used its whole token budget before answering (raise --llm-max-tokens)",
		}
	}
	// No answer, but it did think. Thinking out loud is not an explanation, but it is
	// what the model said, and something beats an empty card.
	if reasoning := strings.TrimSpace(c.Message.Reasoning); reasoning != "" {
		return parseExplanation(reasoning)
	}
	return ExplainResponse{}, errEmptyResponse
}

// setAuth is the ONLY place the API key is read. It is never formatted into an error,
// a log line, or anything that reaches the UI — see TestAPIKeyNeverEscapes.
//
// An empty key omits the header cleanly rather than sending "Bearer ", which is what
// makes a local Ollama with no key work. Ollama the other way round is just as
// important: it requires the header but ignores the value, so a dummy key is valid and
// nothing here may second-guess what a key looks like.
func (b *OpenAICompatible) setAuth(req *http.Request) {
	if b.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	}
}

// redact removes the API key from text that is about to become an error — and an error
// here becomes a card in the TUI. The provider's own error body is the one place a key
// could realistically come back to us (a server that echoes the request headers), and it
// is not worth trusting every OpenAI-compatible endpoint in the world not to.
func (b *OpenAICompatible) redact(text string) string {
	if b.cfg.APIKey == "" {
		return text
	}
	return strings.ReplaceAll(text, b.cfg.APIKey, "[redacted]")
}

// endpoint is the chat-completions URL. Trailing slashes are trimmed so that a base URL
// copied from a provider's docs with or without one behaves identically.
func (b *OpenAICompatible) endpoint() string {
	return strings.TrimRight(b.cfg.BaseURL, "/") + "/chat/completions"
}

// chatRequest builds the wire payload: a low temperature for consistent, parseable
// output, and a hard cap on tokens that bounds both cost and card length.
func (b *OpenAICompatible) chatRequest(req ExplainRequest, jsonMode bool) chatRequest {
	out := chatRequest{
		Model:       b.cfg.Model,
		Messages:    buildMessages(req),
		Temperature: b.cfg.Temperature,
		MaxTokens:   b.cfg.MaxTokens,
	}
	// Asked for when available, relied on never: parseExplanation assumes the model
	// ignores it. Streaming is the other field deliberately absent — v1 is
	// non-streaming, and this struct plus the decode in call() is where it would go.
	if jsonMode {
		out.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	return out
}

// The wire types of the OpenAI chat-completions API, reduced to the fields v1 uses.
type (
	chatRequest struct {
		Model          string          `json:"model"`
		Messages       []message       `json:"messages"`
		Temperature    float64         `json:"temperature"`
		MaxTokens      int             `json:"max_tokens"`
		ResponseFormat *responseFormat `json:"response_format,omitempty"`
	}
	message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	responseFormat struct {
		Type string `json:"type"`
	}
	chatResponse struct {
		Choices []choice `json:"choices"`
	}
	choice struct {
		Message struct {
			Content string `json:"content"`
			// Reasoning is where a thinking model puts its chain of thought. Not part of
			// the OpenAI spec, but Ollama and others send it, and it is the difference
			// between an empty card and something to read (see content).
			Reasoning string `json:"reasoning"`
		} `json:"message"`
		// FinishReason is "length" when the answer was cut off by max_tokens.
		FinishReason string `json:"finish_reason"`
	}
)

// fatalError marks a failure that no retry can fix — one determined by the request itself
// rather than by the state of the network or the provider. Retrying it would burn the
// same tokens to fail in exactly the same way.
type fatalError struct{ msg string }

func (e fatalError) Error() string { return e.msg }

// apiError is a non-2xx response. It carries the provider's own message — which is what
// turns "it didn't work" into "model 'gemma2:2b' not found" — and never anything of ours.
type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("HTTP %d (%s)", e.status, statusHint(e.status))
	if body := collapse(e.body); body != "" {
		msg += ": " + truncateRunes(body, maxErrBodyRunes)
	}
	return msg
}

// statusHint turns a status code into the next thing to try. The two that matter most
// are the two that a first run gets wrong: the key and the model name.
func statusHint(status int) string {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return "unauthorized — check LOGSCRY_API_KEY"
	case status == http.StatusNotFound:
		return "not found — check --llm-url and --llm-model"
	case status == http.StatusTooManyRequests:
		return "rate limited by the provider"
	case status >= 500:
		return "server error"
	default:
		return "bad request"
	}
}

// transient reports whether err is worth another attempt. Anything that is not an HTTP
// status — a refused connection, a timeout, a truncated body — is transient by nature;
// among statuses, only 429 and 5xx can plausibly succeed on a second try.
func transient(err error) bool {
	var fatal fatalError
	if errors.As(err, &fatal) {
		return false
	}
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return true
	}
	return apiErr.status == http.StatusTooManyRequests || apiErr.status >= 500
}

// rejectsRequest reports whether the server refused the request itself, which is the
// signature of an endpoint that does not understand response_format.
func rejectsRequest(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.status == http.StatusBadRequest || apiErr.status == http.StatusUnprocessableEntity
}

// sleep waits for d, reporting false if the context was cancelled first. Backoff must
// never be the reason Ctrl+C feels slow.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
