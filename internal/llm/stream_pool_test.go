// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/llm"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// TestStreamStalledConsumerCannotTearTheStream is the reason progressive sends drop instead
// of blocking.
//
// The callback runs on the worker while the HTTP body is still open. If it blocked on a busy
// pipeline goroutine, the read would stall, the per-attempt deadline would fire, and — since
// our own deadline is deliberately not retryable — a perfectly good answer would come back
// salvaged and badged incomplete. A slow UI must never be able to damage a response.
//
// Here nobody reads `out` during the stream: the only receive happens after Run has already
// pushed the final result, which is the blocking send that synchronises the test.
func TestStreamStalledConsumerCannotTearTheStream(t *testing.T) {
	// The body must be far larger than the socket and transport buffers, or a blocked
	// reader could still finish the answer out of memory and the test would pass whether
	// the send blocks or not.
	body, _ := json.Marshal(map[string]string{
		"summary":      "the worker cannot reach postgres",
		"likely_cause": strings.Repeat("the worker retried and failed again. ", 3000),
		"suggestion":   "run docker compose ps",
	})
	pieces := append(chunksFor(string(body), 8), sseStop())
	srv, calls := streamServer(t, pieces...)

	cfg := llm.Defaults()
	cfg.BaseURL = srv.URL + "/v1"
	cfg.HTTPClient = srv.Client()
	cfg.Stream = true
	cfg.Workers = 1
	cfg.Retries = 0
	cfg.Timeout = 750 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan score.EscalationRequest, 1)
	out := make(chan model.Explanation) // unbuffered: a send only completes when we receive
	go llm.Run(ctx, llm.NewOpenAICompatible(cfg), cfg, in, out)

	in <- score.EscalationRequest{Hash: "h1", Pattern: "PANIC: nil map write"}
	close(in)

	// Read NOTHING for longer than one attempt's timeout. A blocking progressive send would
	// park the worker here with the body still open, the deadline would fire, and the answer
	// would come back salvaged and badged. Dropping instead, the stream finishes in
	// milliseconds and the worker simply waits on its final — blocking — send.
	time.Sleep(2 * cfg.Timeout)

	var final model.Explanation
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ex, ok := <-out:
			if !ok {
				if final.State != model.ExplainDone {
					t.Fatalf("never received a terminal explanation, last was %+v", final)
				}
				if final.Truncated {
					t.Error("a stalled consumer caused a good answer to be badged incomplete")
				}
				if final.Summary != "the worker cannot reach postgres" {
					t.Errorf("Summary = %q", final.Summary)
				}
				if got := calls.Load(); got != 1 {
					t.Errorf("provider called %d times, want 1", got)
				}
				return
			}
			final = ex
		case <-deadline:
			t.Fatal("the pool blocked on a progressive send: the read path is waiting on the consumer")
		}
	}
}

// TestStreamCostGuarantee: a streamed call is still ONE call. The rate limiter caps LLM
// requests per minute regardless of log volume, and streaming must not turn one escalation
// into several requests.
func TestStreamCostGuarantee(t *testing.T) {
	pieces := append(chunksFor(theAnswer, 8), sseStop())
	srv, calls := streamServer(t, pieces...)

	cfg := llm.Defaults()
	cfg.BaseURL = srv.URL + "/v1"
	cfg.HTTPClient = srv.Client()
	cfg.Stream = true
	cfg.Workers = 2
	cfg.Retries = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const escalations = 50
	in := make(chan score.EscalationRequest, escalations)
	out := make(chan model.Explanation, escalations*8) // room for every partial too
	go llm.Run(ctx, llm.NewOpenAICompatible(cfg), cfg, in, out)

	for i := range escalations {
		in <- score.EscalationRequest{Hash: string(rune('a' + i%26)), Pattern: "p"}
	}
	close(in)

	done := 0
	deadline := time.After(10 * time.Second)
	for done < escalations {
		select {
		case ex, ok := <-out:
			if !ok {
				t.Fatalf("the pool closed after only %d terminal results", done)
			}
			if ex.State != model.ExplainPending {
				done++
			}
		case <-deadline:
			t.Fatalf("timed out after %d terminal results", done)
		}
	}
	if got := calls.Load(); got != escalations {
		t.Errorf("provider called %d times for %d escalations: a streamed call must be ONE call",
			got, escalations)
	}
}
