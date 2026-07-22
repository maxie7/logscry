// SPDX-License-Identifier: Apache-2.0

// Package llm defines the pluggable LLM backend used to explain escalated events,
// and the worker pool that drives it.
//
// The stage is asynchronous by construction (RDI §7): the pool consumes the scorer's
// escalation channel and nothing here can ever block ingestion. A model that is down,
// slow, rate-limited, or returning garbage degrades to "explanation unavailable" on
// one card — it never stalls or kills the tail.
package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// ExplainRequest is the context assembled for a single escalated event.
type ExplainRequest struct {
	Trigger   model.LogLine
	Context   []string // recent surrounding lines
	Template  string
	Count     int
	FirstSeen time.Time

	// OnPartial, when set, receives the explanation as it fills in: a backend that streams
	// calls it each time a FIELD completes, never mid-value. Optional — a backend that does
	// not stream simply never calls it, which is why this is a field here rather than a
	// change to the Backend interface.
	//
	// It is called synchronously, on the goroutine driving Explain, and must not block:
	// parking it would stall the response read (see pool.explain).
	OnPartial func(ExplainResponse) `yaml:"-"`
}

// ExplainResponse is the structured explanation returned by a Backend.
type ExplainResponse struct {
	Summary     string // one-line "what happened"
	LikelyCause string
	Suggestion  string // what to check / try

	// Truncated marks an answer salvaged from a stream that died before the model closed
	// its JSON. The fields are real but the answer is short, and the card says so: rendering
	// a half-formed "likely cause" as if it were the whole verdict is how someone acts on
	// advice the model never finished giving.
	Truncated bool
}

// Backend is a pluggable LLM provider. Implementations must be safe for
// concurrent use by the worker pool.
type Backend interface {
	Explain(ctx context.Context, req ExplainRequest) (ExplainResponse, error)
	Name() string
}

// Config holds every knob of the LLM stage: the backend endpoint, the request shape,
// and the pool around it. Like score.Config the yaml tags name the keys in
// logscry.yaml and internal/config does the decoding.
type Config struct {
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`

	// APIKey comes from the environment (LOGSCRY_API_KEY), never from a flag or the
	// file, so it cannot land in a shell history or a commit. It is sent in exactly one
	// place (see setAuth) and appears in no error, no log line, and no rendered view.
	APIKey string `yaml:"-"`

	// Workers is the pool size. Two is enough to keep a local model busy while leaving
	// the tail untouched, and the rate limiter — not the pool — is what caps cost.
	Workers int `yaml:"workers"`
	// Queue is the capacity of the escalation channel. Full means the model is slower
	// than the anomalies are arriving, and the scorer drops rather than block ingest.
	Queue int `yaml:"queue"`

	// Timeout bounds ONE attempt, not the whole call: with retries, the wall-clock
	// worst case is (Retries+1) × Timeout plus backoff.
	Timeout time.Duration `yaml:"timeout"`
	// MaxTokens caps the answer. It bounds cost and keeps explanations to the three
	// short fields the card has room for.
	MaxTokens int `yaml:"max_tokens"`
	// Temperature is low on purpose: we want consistent, parseable output, not prose.
	Temperature float64 `yaml:"temperature"`
	// JSONMode asks for response_format=json_object. OpenAI, Groq and Ollama all honour
	// it; an unknown OpenAI-compatible server may reject it, so the backend downgrades
	// once and remembers (see OpenAICompatible.Explain). Parsing never relies on it.
	JSONMode bool `yaml:"json_mode"`
	// Stream asks the provider to send the answer as Server-Sent Events, so the card fills
	// in field by field instead of appearing all at once. Default OFF: provider behaviour
	// around stream + response_format varies, and a regression in the explanation path is
	// worse than a card that feels slower. It changes only WHEN fields appear — the final
	// explanation is identical, parsed by the same code either way.
	Stream bool `yaml:"stream"`
	// Retries is the number of EXTRA attempts, made only for transient failures
	// (timeout, 429, 5xx, network). A 4xx is never retried: a bad key or a bad model
	// name will fail identically forever, and hammering it is how a retry storm starts.
	Retries int           `yaml:"retries"`
	Backoff time.Duration `yaml:"backoff"`

	// Anonymize masks sensitive values (IPs, emails, tokens, hostnames, home-dir
	// usernames) out of the outgoing payload and restores them in the answer. Default off:
	// a local Ollama needs none of it, and with it off the payload is exactly what it was
	// before the feature existed. It lives under llm: because it is a property of what the
	// LLM stage sends, sitting with the endpoint and the key.
	Anonymize bool `yaml:"anonymize"`
	// AnonymizeSuffixes extends the bare-hostname allowlist with site-specific private
	// suffixes (e.g. "acmecloud"), for operators whose infra hosts are not covered by the
	// built-in internal/local/svc/... set. Empty by default.
	AnonymizeSuffixes []string `yaml:"anonymize_suffixes"`

	// HTTPClient is the transport. Nil means one built from Timeout; the tests inject
	// their own so no test ever touches the network.
	HTTPClient *http.Client `yaml:"-"`
	// Now is the clock, injectable as everywhere else in logscry. Nil means time.Now.
	Now func() time.Time `yaml:"-"`
}

// Defaults returns the zero-config LLM stage: a local Ollama, two workers, and a
// bounded, cheap request. One defaults table, so tuning is one edit.
func Defaults() Config {
	return Config{
		BaseURL:     "http://localhost:11434/v1",
		Model:       "gemma2:2b",
		Workers:     2,
		Queue:       64,
		Timeout:     30 * time.Second,
		MaxTokens:   300,
		Temperature: 0.1,
		JSONMode:    true,
		Retries:     1,
		Backoff:     500 * time.Millisecond,
	}
}

// Validate rejects configurations that could not work.
//
// Note what it does NOT check: the API key. Ollama's OpenAI-compatible endpoint
// requires the header but ignores its value, so any string — including an obvious
// dummy — is legitimate, and a local endpoint needs no key at all. Refusing to start
// over a key that "looks wrong" would break the tool's whole offline path.
func (c Config) Validate() error {
	if c.BaseURL == "" {
		return fmt.Errorf("llm base URL must not be empty")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("llm base URL %q is not a valid http(s) URL", c.BaseURL)
	}
	if c.Model == "" {
		return fmt.Errorf("llm model must not be empty")
	}
	if c.Workers < 1 {
		return fmt.Errorf("llm workers must be >= 1, got %d", c.Workers)
	}
	if c.Queue < 1 {
		return fmt.Errorf("llm queue must be >= 1, got %d", c.Queue)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("llm timeout must be > 0, got %s", c.Timeout)
	}
	if c.MaxTokens < 1 {
		return fmt.Errorf("llm max tokens must be >= 1, got %d", c.MaxTokens)
	}
	if c.Temperature < 0 || c.Temperature > 2 {
		return fmt.Errorf("llm temperature must be between 0 and 2, got %g", c.Temperature)
	}
	if c.Retries < 0 {
		return fmt.Errorf("llm retries must be >= 0, got %d", c.Retries)
	}
	if c.Backoff < 0 {
		return fmt.Errorf("llm backoff must be >= 0, got %s", c.Backoff)
	}
	return nil
}

// now returns the configured clock, defaulting to time.Now.
func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
