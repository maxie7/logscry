// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"sync"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// Run consumes escalations and produces explanations until in is closed or ctx is
// cancelled, then closes out.
//
// This is the whole reason the LLM stage exists as a pool rather than a call in the
// pipeline: the model takes seconds, and the tail cannot wait seconds. Nothing here can
// apply backpressure to ingestion — the pool only ever READS in, and the scorer's send
// is non-blocking, so a stalled worker means dropped escalations (counted, and shown),
// never a stalled tail.
//
// Every blocking operation selects on ctx.Done(), and the HTTP request carries ctx, so
// Ctrl+C aborts in-flight calls rather than waiting out a model that has stopped
// answering.
func Run(ctx context.Context, b Backend, cfg Config, in <-chan score.EscalationRequest, out chan<- model.Explanation) {
	defer close(out)

	workers := max(cfg.Workers, 1)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			work(ctx, b, cfg, in, out)
		}()
	}
	wg.Wait()
}

// work is one worker: take an escalation, explain it, send the result back.
func work(ctx context.Context, b Backend, cfg Config, in <-chan score.EscalationRequest, out chan<- model.Explanation) {
	for {
		select {
		case <-ctx.Done():
			return
		case req, ok := <-in:
			if !ok {
				return // ingestion ended and the pipeline closed the channel
			}
			ex := explain(ctx, b, cfg, req)
			// A request aborted by shutdown is not a failed explanation, and must not be
			// reported as one: on the way out, "explanation unavailable: context
			// canceled" is noise on a card nobody will read. Checked before the send
			// because a select would otherwise be free to pick it over ctx.Done().
			if ctx.Err() != nil {
				return
			}
			select {
			case out <- ex:
			case <-ctx.Done():
				return
			}
		}
	}
}

// explain calls the backend and maps the outcome onto the template. A failure is a
// result like any other: the card says "explanation unavailable" with a short reason and
// the tail carries on. It never re-escalates — the scorer's cache marked this template on
// the way out, deliberately, so that a slow or broken model cannot make the same error
// escalate over and over.
func explain(ctx context.Context, b Backend, cfg Config, req score.EscalationRequest) model.Explanation {
	ex := model.Explanation{Hash: req.Hash, Pattern: req.Pattern, At: cfg.now()}

	resp, err := b.Explain(ctx, ExplainRequest{
		Trigger:   req.Trigger,
		Context:   req.Context,
		Template:  req.Pattern,
		Count:     req.Count,
		FirstSeen: req.FirstSeen,
	})
	if err != nil {
		ex.State = model.ExplainFailed
		ex.Err = collapse(err.Error())
		return ex
	}

	ex.State = model.ExplainDone
	ex.Summary, ex.LikelyCause, ex.Suggestion = resp.Summary, resp.LikelyCause, resp.Suggestion
	return ex
}
