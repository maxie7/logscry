// SPDX-License-Identifier: Apache-2.0

// Package ingest provides log sources (stdin, subprocess, Docker) that emit
// model.LogLine values into a shared channel.
package ingest

import (
	"context"

	"github.com/maxie7/logscry/internal/model"
)

// Source emits log lines until its context is cancelled or the underlying
// stream ends. Implementations must not close out; the caller owns it.
type Source interface {
	// Lines emits LogLines until ctx is cancelled or the source ends.
	Lines(ctx context.Context, out chan<- model.LogLine) error
	// Name is a short, human-readable identifier for the source.
	Name() string
}
