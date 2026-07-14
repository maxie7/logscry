// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/llm"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
	"github.com/maxie7/logscry/internal/score"
)

// These tests wire the real thing end to end — scorer, pipeline goroutine, worker pool,
// HTTP backend — against a fake OpenAI-compatible server, and assert against the server's
// own request count. Nothing here touches the network.

// stage is the whole M4 wiring, exactly as cmd/logscry assembles it.
type stage struct {
	lines chan model.LogLine
	snaps chan pipeline.Snapshot
	calls *atomic.Int64
	last  chan pipeline.Snapshot // the final snapshot, once Run has finished
}

// startStage stands up a fake provider and runs the pipeline + pool against it.
func startStage(t *testing.T, ctx context.Context, scoreCfg score.Config, handler http.HandlerFunc) *stage {
	t.Helper()

	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := llm.Defaults()
	cfg.BaseURL = srv.URL + "/v1"
	cfg.APIKey = "sk-secret-do-not-leak"
	cfg.Backoff = 0
	cfg.Timeout = 2 * time.Second
	cfg.HTTPClient = srv.Client()

	lines := make(chan model.LogLine, 1024)
	snaps := make(chan pipeline.Snapshot, 1)
	escalations := make(chan score.EscalationRequest, cfg.Queue)
	explanations := make(chan model.Explanation, cfg.Queue+cfg.Workers)

	go llm.Run(ctx, llm.NewOpenAICompatible(cfg), cfg, escalations, explanations)
	go pipeline.Run(ctx, lines, pipeline.Options{
		Snapshots:    snaps,
		Interval:     time.Millisecond,
		Scorer:       score.New(scoreCfg, escalations),
		Escalations:  escalations,
		Explanations: explanations,
	})

	// Drain like the renderer does, keeping the latest: Run's parting snapshot is a
	// blocking send, so something must always be reading.
	last := make(chan pipeline.Snapshot, 1)
	go func() {
		var snap pipeline.Snapshot
		for s := range snaps {
			snap = s
		}
		last <- snap
	}()

	return &stage{lines: lines, snaps: snaps, calls: &calls, last: last}
}

// answer is a provider that always answers well.
func answer(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"summary\":\"It broke.\",\"likely_cause\":\"A nil map.\",\"suggestion\":\"Initialise it.\"}"}}]}`)
}

// noisy scoring: escalate readily, so these tests are about the LLM stage rather than
// about tripping the scorer. The scorer's own quietness is pinned in internal/score.
func eagerScoring(ratePerMin int) score.Config {
	cfg := score.Defaults()
	cfg.Warmup = 0
	cfg.WarmupLines = 0
	cfg.RatePerMin = ratePerMin
	return cfg
}

// TestRateLimitCapsLLMCalls is THE cost guarantee, proved end to end against the thing
// that would actually bill you: the provider's request count.
//
// A thousand distinct, novel, escalating templates arrive in a few milliseconds. Every
// one of them clears the score threshold. The token bucket is what stands between that
// and a thousand LLM calls — so the assertion is not "the scorer decided to suppress
// things", it is "the server was never asked more than ten times".
func TestRateLimitCapsLLMCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const limit = 10
	st := startStage(t, ctx, eagerScoring(limit), answer)

	for i := range 1000 {
		st.lines <- model.LogLine{
			Source: "proc:app",
			Stream: model.Stderr,
			Raw:    fmt.Sprintf("PANIC: failure of kind %s in subsystem %s", word(i), word(i*7)),
		}
	}
	close(st.lines)

	final := <-st.last // Run returns once the pool has drained

	if got := st.calls.Load(); got > limit {
		t.Errorf("the provider was called %d times for 1000 escalating events, want at most %d: the cost cap leaked",
			got, limit)
	} else if got == 0 {
		t.Fatal("the provider was never called, so this proves nothing")
	}
	if final.Stats.Escalations > limit {
		t.Errorf("escalations = %d, want at most %d", final.Stats.Escalations, limit)
	}
	// And the tool is honest about what it is not telling the user.
	if final.Stats.Suppressed == 0 {
		t.Error("nothing was reported as suppressed, but the rate limiter clearly bit")
	}
	if final.Stats.Explained != final.Stats.Escalations {
		t.Errorf("%d escalations produced %d explanations: an answer went missing",
			final.Stats.Escalations, final.Stats.Explained)
	}
}

// TestExplanationReachesTheSnapshot is the whole M4 path in one assertion: a line goes in
// one end, an HTTP call happens on a worker goroutine, and the answer comes out attached
// to the right template in a Snapshot — the thing the TUI renders.
func TestExplanationReachesTheSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := startStage(t, ctx, eagerScoring(10), answer)
	st.lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "PANIC: nil map write in handler 42"}
	close(st.lines)

	final := <-st.last
	if len(final.Escalations) != 1 {
		t.Fatalf("got %d escalations, want 1", len(final.Escalations))
	}
	ex := final.Escalations[0].Explanation
	if ex == nil || ex.State != model.ExplainDone {
		t.Fatalf("explanation = %+v, want a completed one", ex)
	}
	if ex.Summary != "It broke." || ex.LikelyCause != "A nil map." || ex.Suggestion != "Initialise it." {
		t.Errorf("explanation = %+v, want the model's three fields", ex)
	}
	if ex.Hash != final.Escalations[0].Hash {
		t.Error("the explanation landed on a different template than the one that escalated")
	}
}

// TestModelDownDegradesGracefully: the model is not running (as it very often will not
// be). The tail must keep flowing, the card must say why, and nothing may reach stdout.
func TestModelDownDegradesGracefully(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := startStage(t, ctx, eagerScoring(10), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not loaded", http.StatusInternalServerError)
	})

	st.lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "PANIC: nil map write in handler 42"}
	for i := range 50 { // the tail keeps flowing while the model is failing
		st.lines <- model.LogLine{Source: "proc:app", Raw: fmt.Sprintf("GET /orders %d 200", i)}
	}
	close(st.lines)

	final := <-st.last
	if final.Stats.TotalLines != 51 {
		t.Errorf("processed %d lines, want 51: a dead model interfered with the tail", final.Stats.TotalLines)
	}
	// Every escalation the dead model was asked about came back as a failure — none was
	// left in flight, which is what a card stuck at "explaining…" forever would look like.
	if final.Stats.Escalations == 0 {
		t.Fatal("nothing escalated, so this proves nothing")
	}
	if final.Stats.ExplainFailed != final.Stats.Escalations || final.Stats.Explaining != 0 {
		t.Errorf("%d escalations resolved as %d failed / %d still in flight, want all failed",
			final.Stats.Escalations, final.Stats.ExplainFailed, final.Stats.Explaining)
	}
	ex := final.Escalations[0].Explanation
	if ex == nil || ex.State != model.ExplainFailed {
		t.Fatalf("explanation = %+v, want a failed one", ex)
	}
	if !strings.Contains(ex.Err, "500") {
		t.Errorf("failure reason = %q, want it to say what went wrong", ex.Err)
	}
}

// TestAPIKeyNeverEscapes. The key is a secret the tool is trusted with, and every error
// path here ends up on screen. So: a hostile provider that echoes the Authorization
// header straight back in its error body still cannot get the key into an explanation, a
// snapshot, or anything else the user can see.
func TestAPIKeyNeverEscapes(t *testing.T) {
	const key = "sk-secret-do-not-leak" // the key startStage configures

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := startStage(t, ctx, eagerScoring(10), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected request with "+r.Header.Get("Authorization"), http.StatusUnauthorized)
	})

	st.lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "PANIC: nil map write in handler 42"}
	close(st.lines)

	final := <-st.last
	ex := final.Escalations[0].Explanation
	if ex == nil || ex.State != model.ExplainFailed {
		t.Fatalf("explanation = %+v, want a failed one", ex)
	}
	if strings.Contains(ex.Err, key) {
		t.Errorf("the API key leaked into the failure shown on the card: %q", ex.Err)
	}
	if strings.Contains(fmt.Sprintf("%+v", final), key) {
		t.Error("the API key leaked into the snapshot the renderer draws from")
	}
	// It was redacted, not simply absent because the call never happened.
	if !strings.Contains(ex.Err, "[redacted]") {
		t.Errorf("failure reason = %q, want the echoed key redacted out of it", ex.Err)
	}
}

// word turns an index into a distinct, unmaskable token, so that each generated line is a
// genuinely NEW template rather than the same one with a different number in it (which
// the templater would collapse — correctly — into a single signature).
func word(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	return string([]byte{
		letters[i%26], letters[(i/26)%26], letters[(i/676)%26],
	})
}
