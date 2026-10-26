// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// flagAnswer is a complete answer in the JSON shape the prompt asks the model for.
const flagAnswer = `{"summary":"the worker cannot reach postgres",` +
	`"likely_cause":"the database container is down","suggestion":"run docker compose ps"}`

// sseDelta frames one piece of content as a provider's chat.completion.chunk.
func sseDelta(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": content}}},
	})
	return "data: " + string(b) + "\n\n"
}

// TestEveryTerminalPathCarriesTheRequestsFlag: an answer is matched to its flag by
// model.FlagID, so an answer that left the pool without the request's flag would be matched to
// no flag at all (#121). Every path out of the pool — the answer, each way a call fails, the
// anonymizer refusing to send, an answer salvaged from a torn stream, and every streamed partial
// on the way — must carry it. Each row runs the real pool over the real backend against a fake
// provider. Shutdown sends nothing, which is also checked: no answer at all, not a flagless one.
func TestEveryTerminalPathCarriesTheRequestsFlag(t *testing.T) {
	const flag = model.FlagID(7)
	answer := func(w http.ResponseWriter, _ *http.Request) { reply(w, flagAnswer) }
	tests := []struct {
		name    string
		handler http.HandlerFunc
		tweak   func(*Config)
		wrap    func(Backend) Backend
		hang    bool // the provider never answers (handler is unused)
		cancel  bool // shut down while the call is in flight
		want    model.ExplainState
		// truncated: the terminal answer was salvaged; partials: at least one was streamed.
		truncated, partials bool
	}{
		{name: "answered", handler: answer, want: model.ExplainDone},
		{
			name:    "a 4xx",
			handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "bad request", http.StatusBadRequest) },
			want:    model.ExplainFailed,
		},
		{
			name:  "every attempt times out",
			hang:  true,
			tweak: func(c *Config) { c.Timeout = 50 * time.Millisecond },
			want:  model.ExplainFailed,
		},
		{
			name: "retries exhausted",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "overloaded", http.StatusServiceUnavailable)
			},
			tweak: func(c *Config) { c.Retries = 2 },
			want:  model.ExplainFailed,
		},
		{
			name:    "the anonymizer fails closed",
			handler: answer,
			wrap: func(inner Backend) Backend {
				return &anonymizing{inner: inner, newMask: func(...string) masker { return failingMasker{} }}
			},
			want: model.ExplainFailed,
		},
		{
			name: "a torn stream is salvaged",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				half := `{"summary":"the worker cannot reach postgres","likely_cause":"the database`
				for i := 0; i < len(half); i += 25 {
					_, _ = io.WriteString(w, sseDelta(half[i:min(i+25, len(half))]))
					w.(http.Flusher).Flush()
				}
				// Returning without the terminal chunk closes the body mid-answer.
			},
			tweak:     func(c *Config) { c.Stream = true },
			want:      model.ExplainDone,
			truncated: true,
			partials:  true,
		},
		{name: "shutdown while the call is in flight", hang: true, cancel: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := tc.handler
			if tc.hang {
				// The body is read first: only then does the server notice the client hanging
				// up. release lets Close finish even if it does not.
				release := make(chan struct{})
				handler = func(_ http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-release:
					}
				}
				defer close(release)
			}
			srv := httptest.NewServer(handler)
			defer srv.Close()
			cfg := testConfig(srv)
			cfg.Workers = 1
			if tc.tweak != nil {
				tc.tweak(&cfg)
			}
			var b Backend = NewOpenAICompatible(cfg)
			if tc.wrap != nil {
				b = tc.wrap(b)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in := make(chan score.EscalationRequest, 1)
			out := make(chan model.Explanation, 64) // room for every partial: none is dropped
			in <- score.EscalationRequest{Hash: "h", Flag: flag, Pattern: "p", Count: 1}
			close(in)
			if tc.cancel {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			Run(ctx, b, cfg, in, out)

			var terminal []model.Explanation
			sawPartial := false
			for ex := range out { // Run closed it
				if ex.Flag != flag {
					t.Errorf("a %v explanation carries flag %d, want the request's %d", ex.State, ex.Flag, flag)
				}
				if ex.State == model.ExplainPending {
					sawPartial = true
					continue
				}
				terminal = append(terminal, ex)
			}
			if tc.cancel {
				if len(terminal) != 0 {
					t.Errorf("shutdown sent %d terminal answers, want none", len(terminal))
				}
				return
			}
			if len(terminal) != 1 {
				t.Fatalf("%d terminal answers, want exactly 1", len(terminal))
			}
			if got := terminal[0]; got.State != tc.want || got.Truncated != tc.truncated {
				t.Errorf("terminal answer is %v (truncated %v), want %v (truncated %v): %+v",
					got.State, got.Truncated, tc.want, tc.truncated, got)
			}
			if tc.partials && !sawPartial {
				t.Error("no partial was streamed, so the row does not check one")
			}
		})
	}
}
