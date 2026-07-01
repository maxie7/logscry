// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"errors"
)

// OpenAICompatible talks to any OpenAI-compatible chat-completions endpoint. A
// single configurable base URL + model + API key covers OpenAI, Groq, and local
// Ollama (which exposes an OpenAI-compatible API).
type OpenAICompatible struct {
	BaseURL string
	Model   string
	APIKey  string
}

// NewOpenAICompatible constructs a backend for the given endpoint.
func NewOpenAICompatible(baseURL, model, apiKey string) *OpenAICompatible {
	return &OpenAICompatible{BaseURL: baseURL, Model: model, APIKey: apiKey}
}

// Name implements Backend.
func (b *OpenAICompatible) Name() string { return "openai-compatible" }

// Explain implements Backend.
//
// TODO(M4): POST to {BaseURL}/chat/completions with a tight system prompt
// requesting JSON, decode into ExplainResponse, and degrade gracefully on error.
func (b *OpenAICompatible) Explain(ctx context.Context, req ExplainRequest) (ExplainResponse, error) {
	_ = ctx
	_ = req
	return ExplainResponse{}, errors.New("llm: openai-compatible backend not implemented: TODO(M4)")
}
