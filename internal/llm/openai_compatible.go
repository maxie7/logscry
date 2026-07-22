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
// logscry's memory problem. When streaming, it bounds the accumulated ANSWER rather than
// the wire (see maxStreamBytes), which is the quantity that actually grows in memory.
const maxBodyBytes = 1 << 20

// maxStreamBytes bounds the WIRE when streaming, and is deliberately much larger than
// maxBodyBytes because SSE inflates the same answer by roughly an order of magnitude: every
// token arrives wrapped in a whole chat.completion.chunk envelope (id, object, created,
// model, system_fingerprint, choices, finish_reason) of ~250 bytes. A 300-token answer that
// is ~1.5 KiB complete is ~72 KiB streamed, and at --llm-max-tokens 4096 — well within what
// the README recommends for reasoning models — it is ~984 KiB, which would sit just under
// maxBodyBytes and trip it the moment the model also streams `reasoning`.
//
// Reusing the 1 MiB cap here would therefore report a perfectly normal answer as torn, and
// hand the user a retried, incomplete-badged card for a response that arrived fine. 8 MiB
// covers ~32k tokens — past any practical max_tokens — while still cutting off a runaway
// endpoint in seconds.
const maxStreamBytes = 8 << 20

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
	// streamOff is the same downgrade for `stream`, which not every OpenAI-compatible
	// server accepts alongside response_format.
	streamOff atomic.Bool
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
//
// It downgrades in that order — streaming first, then response_format — because a server
// that rejects the pair gives no clue which field it disliked, and JSON mode is
// load-bearing for parse quality while streaming only changes when fields appear. Giving up
// the cosmetic capability to keep the functional one is the right way round.
func (b *OpenAICompatible) Explain(ctx context.Context, req ExplainRequest) (ExplainResponse, error) {
	opts := callOpts{stream: b.streaming(), jsonMode: b.jsonMode()}

	resp, err := b.attempt(ctx, req, opts)
	if err == nil || !rejectsRequest(err) {
		return resp, err
	}

	if opts.stream {
		b.streamOff.Store(true)
		opts.stream = false
		resp, err = b.attempt(ctx, req, opts)
		if err == nil || !rejectsRequest(err) {
			return resp, err
		}
	}

	if !opts.jsonMode {
		return resp, err
	}
	b.jsonOff.Store(true)
	opts.jsonMode = false
	resp, err = b.attempt(ctx, req, opts)
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

// streaming reports whether this request should ask for SSE: configured on, and not
// already refused by this server.
func (b *OpenAICompatible) streaming() bool { return b.cfg.Stream && !b.streamOff.Load() }

// callOpts are the per-attempt capability flags, carried together so that a downgrade of
// one cannot silently reset the other.
type callOpts struct {
	stream   bool
	jsonMode bool
}

// attempt makes the call, retrying only transient failures with a short backoff.
//
// Every exit path runs through the salvage check, not just exhaustion: a stream can die
// for a reason that must not be retried (our own deadline, our own byte budget) while
// still having delivered a usable partial answer, and that answer beats an empty card.
// Nothing but a streamed call ever carries salvage, so with streaming off this is exactly
// the loop it always was — a 4xx still returns its error untouched.
func (b *OpenAICompatible) attempt(ctx context.Context, req ExplainRequest, opts callOpts) (ExplainResponse, error) {
	var lastErr error
	for i := 0; i <= b.cfg.Retries; i++ {
		if i > 0 && !sleep(ctx, b.cfg.Backoff) {
			return ExplainResponse{}, ctx.Err()
		}

		resp, err := b.call(ctx, req, opts)
		if err == nil {
			return resp, nil
		}
		// Shutdown (Ctrl+C) is not a failure to retry: it is a reason to stop now.
		if ctx.Err() != nil {
			return ExplainResponse{}, ctx.Err()
		}
		lastErr = err
		if !transient(err) {
			break
		}
	}
	if resp, ok := salvaged(lastErr); ok {
		return resp, nil
	}
	return ExplainResponse{}, lastErr
}

// call makes one HTTP request, bounded by its own timeout. The context it derives is
// the request's: cancelling the parent aborts the connection mid-flight rather than
// leaving the tail waiting on a model that is never going to answer.
func (b *OpenAICompatible) call(ctx context.Context, req ExplainRequest, opts callOpts) (ExplainResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout)
	defer cancel()

	body, err := json.Marshal(b.chatRequest(req, opts))
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

	// An error status is a complete body either way, so it is read the same way for both
	// modes — a provider reports "model not found" as JSON, not as an event stream.
	if httpResp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxBodyBytes))
		return ExplainResponse{}, &apiError{status: httpResp.StatusCode, body: b.redact(string(payload))}
	}

	if opts.stream {
		return b.consumeStream(ctx, httpResp.Body, req.OnPartial)
	}

	payload, err := io.ReadAll(io.LimitReader(httpResp.Body, maxBodyBytes))
	if err != nil {
		return ExplainResponse{}, fmt.Errorf("reading response: %w", err)
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

// consumeStream reads an SSE answer, reporting each completed field through onPartial and
// returning the same result the non-streaming path would have produced.
//
// The accumulator is local, so each ATTEMPT gets a fresh buffer. That is not incidental: a
// retry appending to the previous attempt's partial JSON would splice two half-objects into
// garbage ({"summary": "Cannot re{"summary": "Cannot reach...).
func (b *OpenAICompatible) consumeStream(ctx context.Context, body io.Reader, onPartial func(ExplainResponse)) (ExplainResponse, error) {
	var answer, reasoning strings.Builder
	var finish string
	var sent ExplainResponse
	overflow := false

	err := readSSE(io.LimitReader(body, maxStreamBytes), maxStreamBytes,
		func() bool { return finish != "" },
		func(data []byte) {
			var chunk streamChunk
			if err := json.Unmarshal(data, &chunk); err != nil || len(chunk.Choices) == 0 {
				return // a malformed chunk is skipped, never fatal: degrade, don't discard
			}
			c := chunk.Choices[0]
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
			if answer.Len()+reasoning.Len() > maxBodyBytes {
				overflow = true
				return
			}
			answer.WriteString(c.Delta.Content)
			reasoning.WriteString(c.Delta.Reasoning)

			// A string field can only become complete when its closing quote arrives, so
			// re-parsing on anything else would be wasted work on every token.
			if onPartial == nil || !hasQuote(c.Delta.Content) {
				return
			}
			if p := partialExplanation(answer.String()); p.found && p.resp != sent {
				sent = p.resp
				onPartial(p.resp)
			}
		})
	if overflow && err == nil {
		err = errStreamLimit
	}

	final := choice{FinishReason: finish}
	final.Message.Content = answer.String()
	final.Message.Reasoning = reasoning.String()

	if err == nil {
		return content(final) // clean end: byte-for-byte the non-streaming result
	}

	// A tear AFTER the model closed its JSON is not a tear at all: the answer was whole and
	// the connection merely dropped on the way out. Returning it as an ordinary result is
	// what stops a disconnect one byte before [DONE] costing a retry and a badge.
	if partialExplanation(answer.String()).closed {
		return content(final)
	}
	return ExplainResponse{}, b.torn(ctx, err, final)
}

// torn classifies a stream that ended before the model closed its answer, carrying anything
// worth showing along with the error so attempt can fall back to it.
func (b *OpenAICompatible) torn(ctx context.Context, err error, final choice) error {
	te := &tornError{err: err}
	// Deterministic causes must not be retried. Our own per-attempt deadline reproduces
	// after another full --llm-timeout, and our own byte budget reproduces after another
	// full read and another full generation — the second burning real provider tokens for
	// an identical outcome. A genuine transport tear is a different thing and stays
	// retryable. ctx.Err() distinguishes OUR deadline from a parent cancelled by Ctrl+C,
	// which attempt handles separately.
	te.fatal = errors.Is(err, errStreamLimit) ||
		(errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded))

	if resp, ok := salvageable(final); ok {
		resp.Truncated = true
		te.salvage, te.complete = resp, true
	}
	return te
}

// salvageable decides whether a torn answer is worth showing at all.
//
// A completed field always is. Prose is too — a model that ignored the format still said
// something useful, and half a sentence beats an empty card. But a JSON answer torn before
// its first field completed is neither: parseExplanation's prose fallback would hand back
// the raw fragment, and rendering `{"summ` as the model's summary is worse than admitting
// the explanation is unavailable.
func salvageable(final choice) (ExplainResponse, bool) {
	text := final.Message.Content
	if p := partialExplanation(text); p.found {
		return p.resp, true
	}
	if strings.Contains(text, "{") {
		return ExplainResponse{}, false // torn JSON with nothing finished
	}
	resp, err := content(final)
	return resp, err == nil && resp != ExplainResponse{}
}

// tornError is a stream that ended before the model finished, carrying whatever was
// salvageable so that a failure need not cost the user the part that did arrive.
type tornError struct {
	err      error
	salvage  ExplainResponse
	complete bool // salvage is usable
	fatal    bool // deterministic: retrying reproduces it exactly
}

func (e *tornError) Error() string { return e.err.Error() }
func (e *tornError) Unwrap() error { return e.err }

// salvaged returns the partial answer carried by a torn stream, if there is one worth
// showing. Only streaming ever produces these, so every other path is unaffected.
func salvaged(err error) (ExplainResponse, bool) {
	var te *tornError
	if !errors.As(err, &te) || !te.complete {
		return ExplainResponse{}, false
	}
	return te.salvage, true
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
func (b *OpenAICompatible) chatRequest(req ExplainRequest, opts callOpts) chatRequest {
	out := chatRequest{
		Model:       b.cfg.Model,
		Messages:    buildMessages(req),
		Temperature: b.cfg.Temperature,
		MaxTokens:   b.cfg.MaxTokens,
		Stream:      opts.stream,
	}
	// Asked for when available, relied on never: parseExplanation assumes the model
	// ignores it.
	if opts.jsonMode {
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
		// Stream is omitted entirely when false, so a request with streaming off is
		// byte-for-byte the one this backend has always sent.
		Stream bool `json:"stream,omitempty"`
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
	// A torn stream is transient only when something outside our control cut it. Our own
	// deadline and our own byte budget are deterministic — see torn.
	var te *tornError
	if errors.As(err, &te) {
		return !te.fatal
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
