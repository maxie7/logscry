// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bufio"
	"context"
	"io"
	"os"
	"time"

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

// Lines implements Source: it scans lines from stdin and emits one LogLine per
// line until EOF or ctx cancellation.
func (s *StdinSource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	r := s.r
	if r == nil {
		r = os.Stdin
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := model.LogLine{
			Time:   time.Now(),
			Source: s.Name(),
			Stream: model.Stdout,
			Raw:    scanner.Text(),
		}
		select {
		case out <- line:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return scanner.Err()
}
