// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/llm"
	"github.com/maxie7/logscry/internal/model"
)

// recordingServer stands up a fake OpenAI-compatible endpoint that captures the exact
// request body it received and answers with the given JSON content. The captured bytes are
// the security-relevant artifact: what actually went over the wire, not what the anonymizer
// returned.
func recordingServer(t *testing.T, content string) (*httptest.Server, *atomic.Pointer[string], *atomic.Int64) {
	t.Helper()
	var body atomic.Pointer[string]
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		s := string(raw)
		body.Store(&s)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, content)
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &calls
}

// backendTo builds an OpenAICompatible pointed at srv, optionally wrapped by the anonymizer.
func backendTo(srv *httptest.Server, anonymize bool) llm.Backend {
	cfg := llm.Defaults()
	cfg.BaseURL = srv.URL + "/v1"
	cfg.APIKey = "sk-test"
	cfg.HTTPClient = srv.Client()
	cfg.Timeout = 2 * time.Second
	var b llm.Backend = llm.NewOpenAICompatible(cfg)
	if anonymize {
		b = llm.NewAnonymizing(b)
	}
	return b
}

// secretRequest is a request whose trigger and context carry every deterministic-shape
// value the anonymizer promises to mask.
//
// Template deliberately carries PRE-TEMPLATED secrets — the shapes the pipeline masker
// leaves behind (sk-abcdefghij<NUM>XYZ, AKIAIOSFODNN<NUM>EXAMPLE), not pristine ones. That
// field is the one that leaked in manual wire testing: it reaches the anonymizer already
// rewritten, so a detector anchored on an unbroken run of [A-Za-z0-9] misses the secret and
// the surviving characters go out in the clear.
func secretRequest() llm.ExplainRequest {
	return llm.ExplainRequest{
		Trigger: model.LogLine{
			Source: "proc:/home/maxie/app",
			Stream: model.Stderr,
			Raw:    "auth failed for alice@corp.example.com from 10.0.0.5 token AKIAIOSFODNN7EXAMPLE",
		},
		Context: []string{
			"request 550e8400-e29b-41d4-a716-446655440000 to db-01.acme.internal",
			"loaded /home/maxie/go/pkg/mod/x",
		},
		Template: "auth failed for <STR> from <IP> key sk-abcdefghij<NUM>XYZ id AKIAIOSFODNN<NUM>EXAMPLE",
		Count:    3,
	}
}

// TestAnonymizeHeadlineNoLiteralsOnTheWire is the security assertion: with masking on, none
// of the known sensitive literals may appear ANYWHERE in the bytes the provider received.
func TestAnonymizeHeadlineNoLiteralsOnTheWire(t *testing.T) {
	srv, body, calls := recordingServer(t, `{"summary":"ok","likely_cause":"ok","suggestion":"ok"}`)
	b := backendTo(srv, true)

	if _, err := b.Explain(context.Background(), secretRequest()); err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider called %d times, want 1", calls.Load())
	}

	sent := *body.Load()
	secrets := []string{
		"alice@corp.example.com",
		"10.0.0.5",
		"AKIAIOSFODNN7EXAMPLE",
		"550e8400-e29b-41d4-a716-446655440000",
		"db-01.acme.internal",
		"maxie", // the home-dir username, from both Source and a context line
		// The pre-templated remnants from the Template field. Asserting on the surviving
		// fragment rather than the whole token is the point: the pipeline already ate the
		// digits, and it is these leftovers that went over the wire.
		"abcdefghij",
		"IOSFODNN",
	}
	for _, s := range secrets {
		if strings.Contains(sent, s) {
			t.Errorf("literal %q leaked onto the wire:\n%s", s, sent)
		}
	}
	// And it did send something real: the type-tagged placeholders are present. The tag
	// cores survive JSON encoding — json.Marshal escapes the angle brackets to </>,
	// which is itself a small bonus, but the identity we assert on is the core.
	for _, tag := range []string{"IP_1", "EMAIL_1", "TOKEN_1", "UUID_1", "HOST_1", "USER_1"} {
		if !strings.Contains(sent, tag) {
			t.Errorf("expected placeholder %s in the payload, got:\n%s", tag, sent)
		}
	}
}

// TestAnonymizeRestoresResponse: the model echoes placeholders back, and all three fields
// come back with the real values so the card reads about the real system.
func TestAnonymizeRestoresResponse(t *testing.T) {
	content := `{"summary":"host <HOST_1> refused <IP_1>",` +
		`"likely_cause":"credential rotated? mail <EMAIL_1>",` +
		`"suggestion":"check DNS for <HOST_1> and reach <IP_1>"}`
	srv, _, _ := recordingServer(t, content)
	b := backendTo(srv, true)

	resp, err := b.Explain(context.Background(), secretRequest())
	if err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	if !strings.Contains(resp.Summary, "db-01.acme.internal") || !strings.Contains(resp.Summary, "10.0.0.5") {
		t.Errorf("Summary not restored: %q", resp.Summary)
	}
	if !strings.Contains(resp.LikelyCause, "alice@corp.example.com") {
		t.Errorf("LikelyCause not restored: %q", resp.LikelyCause)
	}
	if !strings.Contains(resp.Suggestion, "db-01.acme.internal") || !strings.Contains(resp.Suggestion, "10.0.0.5") {
		t.Errorf("Suggestion not restored: %q", resp.Suggestion)
	}
}

// TestDefaultOffSendsRawValues: with masking off the payload is what it always was — the raw
// values go over the wire unchanged. This pins the "byte-for-byte unchanged when off"
// guarantee from the other side.
func TestDefaultOffSendsRawValues(t *testing.T) {
	srv, body, _ := recordingServer(t, `{"summary":"ok","likely_cause":"ok","suggestion":"ok"}`)
	b := backendTo(srv, false)

	if _, err := b.Explain(context.Background(), secretRequest()); err != nil {
		t.Fatalf("Explain errored: %v", err)
	}
	sent := *body.Load()
	for _, s := range []string{"alice@corp.example.com", "10.0.0.5", "db-01.acme.internal"} {
		if !strings.Contains(sent, s) {
			t.Errorf("masking-off should send raw %q, but it was absent:\n%s", s, sent)
		}
	}
}

// TestAnonymizeSlashUserinfoOnTheWire is #59 at the seam: a userinfo carrying a raw '/' is masked
// whole, through the host, in every field, and the request is SENT -- the remedy adds no way for an
// escalation to be skipped. Main sent the password remainder and the host here, under a username
// tagged as a host. The answer comes back with the real authority on the card.
func TestAnonymizeSlashUserinfoOnTheWire(t *testing.T) {
	srv, body, calls := recordingServer(t, `{"summary":"login to <TOKEN_1> failed","likely_cause":"ok","suggestion":"ok"}`)
	b := backendTo(srv, true)
	req := llm.ExplainRequest{
		Trigger:  model.LogLine{Source: "stdin", Raw: "FATAL connect postgres://svcuser:pa/ss9q@pgprimary7/app failed"},
		Context:  []string{"retrying postgres://svc/user:hunter9q@pgreplica7:5432/app"},
		Template: "FATAL connect postgres://svcuser:pa/ss<NUM>q@pgprimary<NUM>/app failed",
		Count:    1,
	}
	resp, err := b.Explain(context.Background(), req)
	if err != nil {
		t.Fatalf("Explain errored, so the escalation was skipped: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider called %d times, want 1", calls.Load())
	}
	sent := *body.Load()
	for _, s := range []string{"svcuser", "pa/ss", "ss9q", "pgprimary", "user:hunter", "hunter9q", "pgreplica"} {
		if strings.Contains(sent, s) {
			t.Errorf("literal %q leaked onto the wire:\n%s", s, sent)
		}
	}
	if want := "login to svcuser:pa/ss9q@pgprimary7 failed"; resp.Summary != want {
		t.Errorf("Summary not restored:\n got:  %q\n want: %q", resp.Summary, want)
	}
}

// TestAnonymizeJSONEscapesOnTheWire is GHSA-p47p-xrhj-3x7p at the seam: a URL whose '/', ':' or '@' a JSON
// encoder escaped sent its username, password and host from every field Explain masks. Each subtest puts an
// escaped DSN, with its own invented values, in one field and an ordinary line in the rest; none of its
// values may reach the wire, and the Summary comes back with the line as the log wrote it.
func TestAnonymizeJSONEscapesOnTheWire(t *testing.T) {
	plain := model.LogLine{Source: "stdin", Raw: "FATAL worker stopped"}
	for _, c := range []struct {
		name string
		req  llm.ExplainRequest
		sent []string
	}{
		{"trigger", llm.ExplainRequest{
			Trigger:  model.LogLine{Source: "stdin", Raw: `{"level":"fatal","dsn":"postgres:\/\/svcalpha:pwalpha9q@pgalpha\/app"}`},
			Template: `{"dsn":<STR>,"level":<STR>}`,
		}, []string{"svcalpha", "pwalpha", "pgalpha"}},
		{"source", llm.ExplainRequest{
			Trigger:  model.LogLine{Source: `exec:psql postgres:\u002f\u002fsvcbravo:pwbravo9q@pgbravo\u002fapp`, Raw: plain.Raw},
			Template: "FATAL worker stopped",
		}, []string{"svcbravo", "pwbravo", "pgbravo"}},
		{"context", llm.ExplainRequest{
			Trigger:  plain,
			Context:  []string{`[stdin] {"msg":"retry","dsn":"postgres:\\/\\/svccharlie:pwcharlie9q@pgcharlie\\/app"}`},
			Template: "FATAL worker stopped",
		}, []string{"svccharlie", "pwcharlie", "pgcharlie"}},
		// A JSON message holding two levels of escaping arrives on the Template field with one left,
		// re-quoted: "\\/".
		{"template", llm.ExplainRequest{
			Trigger:  plain,
			Template: `{"level":<STR>,"msg":"connect postgres:\\/\\/svcdelta:pwdelta<NUM>q@pgdelta\\/app failed"}`,
		}, []string{"svcdelta", "pwdelta", "pgdelta"}},
		{"template, u002f", llm.ExplainRequest{
			Trigger:  plain,
			Template: `connect postgres:\u<NUM>f\u<NUM>fsvcecho:pwecho<NUM>q@pgecho\u<NUM>fapp failed`,
		}, []string{"svcecho", "pwecho", "pgecho"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, body, calls := recordingServer(t, `{"summary":"ok","likely_cause":"ok","suggestion":"ok"}`)
			c.req.Count = 1
			if _, err := backendTo(srv, true).Explain(context.Background(), c.req); err != nil {
				t.Fatalf("Explain errored, so the escalation was skipped: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("provider called %d times, want 1", calls.Load())
			}
			sent := *body.Load()
			for _, s := range c.sent {
				if strings.Contains(sent, s) {
					t.Errorf("literal %q leaked onto the wire:\n%s", s, sent)
				}
			}
		})
	}
	// The model never sees the view: a placeholder stands for the bytes the log wrote, so Restore puts the
	// escaped '/' of a password back as "\/".
	t.Run("restore", func(t *testing.T) {
		srv, body, _ := recordingServer(t, `{"summary":"login to <TOKEN_1> failed","likely_cause":"ok","suggestion":"ok"}`)
		resp, err := backendTo(srv, true).Explain(context.Background(), llm.ExplainRequest{
			Trigger:  model.LogLine{Source: "stdin", Raw: `connect postgres:\/\/svcfox:pw\/fox9q@pgfox\/app failed`},
			Template: `connect postgres:\/\/svcfox:pw\/fox<NUM>q@pgfox\/app failed`,
			Count:    1,
		})
		if err != nil {
			t.Fatalf("Explain errored, so the escalation was skipped: %v", err)
		}
		for _, s := range []string{"svcfox", "fox9q", "pgfox"} {
			if strings.Contains(*body.Load(), s) {
				t.Errorf("literal %q leaked onto the wire:\n%s", s, *body.Load())
			}
		}
		if want := `login to svcfox:pw\/fox9q@pgfox failed`; resp.Summary != want {
			t.Errorf("Summary not restored:\n got:  %q\n want: %q", resp.Summary, want)
		}
	})
}
