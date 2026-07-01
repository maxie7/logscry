// SPDX-License-Identifier: Apache-2.0

// Package llm defines the pluggable LLM backend used to explain escalated events.
package llm

import (
	"context"
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
}

// ExplainResponse is the structured explanation returned by a Backend.
type ExplainResponse struct {
	Summary     string // one-line "what happened"
	LikelyCause string
	Suggestion  string // what to check / try
}

// Backend is a pluggable LLM provider. Implementations must be safe for
// concurrent use by the worker pool.
type Backend interface {
	Explain(ctx context.Context, req ExplainRequest) (ExplainResponse, error)
	Name() string
}
