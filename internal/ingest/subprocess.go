// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"

	"github.com/maxie7/logscry/internal/model"
)

// SubprocessSource runs a command and captures its stdout+stderr as log lines.
type SubprocessSource struct {
	// Argv is the command and its arguments (Argv[0] is the executable).
	Argv []string
}

// NewSubprocessSource returns a Source that runs the given command.
//
// TODO(M1): run argv via exec.Command, read StdoutPipe/StderrPipe, tag Stream
// (Stdout/Stderr) accordingly, and emit LogLines until the process exits.
func NewSubprocessSource(argv []string) *SubprocessSource {
	return &SubprocessSource{Argv: argv}
}

// Name implements Source.
func (s *SubprocessSource) Name() string { return "subprocess" }

// Lines implements Source.
//
// TODO(M1): implement subprocess capture.
func (s *SubprocessSource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	_ = ctx
	_ = out
	return nil
}
