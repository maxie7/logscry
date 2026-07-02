// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// readLines reads newline-delimited lines from r and emits one model.LogLine per
// line into out, tagging each with the given source and stream. It returns when r
// is exhausted (nil on clean EOF) or ctx is cancelled.
//
// It uses bufio.Reader rather than bufio.Scanner so that lines longer than
// Scanner's 64KB default cap (e.g. large JSON log records) survive intact.
func readLines(ctx context.Context, r io.Reader, source string, stream model.Stream, out chan<- model.LogLine) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			ll := model.LogLine{
				Time:   time.Now(),
				Source: source,
				Stream: stream,
				Raw:    strings.TrimRight(line, "\r\n"),
			}
			select {
			case out <- ll:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
