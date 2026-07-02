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
// Run does not close out; the caller owns that channel (per the Source contract).
func Run(ctx context.Context, sources []Source, out chan<- model.LogLine) error {
	var wg sync.WaitGroup
	errs := make([]error, len(sources))
	for i, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = src.Lines(ctx, out)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
