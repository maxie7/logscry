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

// mapLines reads lines from r via readLines and rewrites each one through fn before
// emitting it. It exists because readLines can only know what it is told — the source
// name, the stream, and the receipt time — while every source richer than "one line, one
// event" carries more than that INSIDE the line: a timestamp Docker prefixes, a unit and
// a priority journald encodes as JSON fields. fn is where each source decodes its own
// wire format; the read/emit/cancel loop below is identical for all of them and is
// written once here rather than per source.
//
// fn runs on the ingestion hot path, once per line, and must stay cheap: parse and
// rewrite, never block and never do I/O.
func mapLines(ctx context.Context, r io.Reader, source string, stream model.Stream,
	out chan<- model.LogLine, fn func(model.LogLine) model.LogLine,
) error {
	interim := make(chan model.LogLine)
	var readErr error
	go func() {
		defer close(interim)
		readErr = readLines(ctx, r, source, stream, interim)
	}()
	for ll := range interim {
		ll = fn(ll)
		select {
		case out <- ll:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return readErr
}
