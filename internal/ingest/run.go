// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"errors"
	"sync"

	"github.com/maxie7/logscry/internal/model"
)

// Run launches every source in its own goroutine, fanning all of their lines into
// the shared out channel. It blocks until every source's Lines call returns —
// either because the source ended or ctx was cancelled — and returns the joined
// error of all sources.
//
// A source that fails does not end the run, and its error does not wait for the
// others: report is called with it the moment that source's Lines returns, while the
// rest keep running (#97). It is not called once ctx is cancelled, since a source
// stopped by the cancel is not a failure. report runs on the source's goroutine,
// always before Run returns, and must not block; nil reports nothing.
//
// Run does not close out; the caller owns that channel (per the Source contract).
func Run(ctx context.Context, sources []Source, out chan<- model.LogLine, report func(error)) error {
	var wg sync.WaitGroup
	errs := make([]error, len(sources))
	for i, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = src.Lines(ctx, out)
			if errs[i] != nil && ctx.Err() == nil && report != nil {
				report(errs[i])
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
