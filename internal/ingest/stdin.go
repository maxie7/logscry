// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"io"
	"os"

	"github.com/maxie7/logscry/internal/model"
)

// StdinSource reads newline-delimited log lines from os.Stdin.
type StdinSource struct {
	// r is the reader to consume; defaults to os.Stdin when nil.
	r io.Reader
}

// NewStdinSource returns a Source that reads from os.Stdin.
func NewStdinSource() *StdinSource { return &StdinSource{r: os.Stdin} }

// Name implements Source.
func (s *StdinSource) Name() string { return "stdin" }

// Lines implements Source: it reads lines from stdin and emits one LogLine per
// line until EOF or ctx cancellation.
func (s *StdinSource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	r := s.r
	if r == nil {
		r = os.Stdin
	}
	return readLines(ctx, r, s.Name(), model.Stdout, out)
}
