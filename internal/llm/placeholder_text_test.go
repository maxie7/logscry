// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/llm"
	"github.com/maxie7/logscry/internal/model"
)

// echoBackend answers with the request it received, so a test can read both what went out and what the
// anonymizer made of it on the way back: the summary is the trigger and its source, the likely cause the
// context lines, the suggestion the template.
type echoBackend struct{ got llm.ExplainRequest }

func (e *echoBackend) Name() string { return "echo" }

func (e *echoBackend) Explain(_ context.Context, req llm.ExplainRequest) (llm.ExplainResponse, error) {
	e.got = req
	return echoOf(req), nil
}

const echoSep = "\x00"

func echoOf(req llm.ExplainRequest) llm.ExplainResponse {
	return llm.ExplainResponse{
		Summary:     req.Trigger.Raw + echoSep + req.Trigger.Source,
		LikelyCause: strings.Join(req.Context, echoSep),
		Suggestion:  req.Template,
	}
}

// placeholderTextRequests carry one of our placeholders as text in one field and a value the chain masks
// in another (#142). Explain masks the trigger, then the source, the template and the context lines, all
// through one Mapper, so until the fix a literal in a later field met the placeholder already minted
// for the trigger's value: the model read one host where the request had two things, and the card showed
// the value where the log had the text. want* are the fields as sent.
//
// The Template row is set by hand: a JSON key is the one place the pipeline keeps a placeholder as text,
// and that key is in the trigger too, so the trigger's own Mask reserves it first in practice.
var placeholderTextRequests = []struct {
	name                              string
	req                               llm.ExplainRequest
	wantRaw, wantSource, wantTemplate string
	wantContext                       []string
}{
	{
		name: "a context line",
		req: llm.ExplainRequest{
			Trigger: model.LogLine{Raw: "[FATAL] connect db.corp.internal refused", Source: "stdin"},
			Context: []string{"[INFO] retry as <HOST_1>"},
		},
		wantRaw: "[FATAL] connect <HOST_2> refused", wantSource: "stdin",
		wantContext: []string{"[INFO] retry as <HOST_1>"},
	},
	{
		name: "the source",
		req: llm.ExplainRequest{
			Trigger: model.LogLine{Raw: "[FATAL] peer 10.0.0.5 down", Source: "proc:./replay <IP_1>"},
		},
		wantRaw: "[FATAL] peer <IP_2> down", wantSource: "proc:./replay <IP_1>",
	},
	{
		name: "the template",
		req: llm.ExplainRequest{
			Trigger:  model.LogLine{Raw: "[FATAL] login from db.corp.internal", Source: "stdin"},
			Template: `{"<HOST_1>":<STR>,"msg":"login from db.corp.internal"}`,
		},
		wantRaw: "[FATAL] login from <HOST_2>", wantSource: "stdin",
		wantTemplate: `{"<HOST_1>":<STR>,"msg":"login from <HOST_2>"}`,
	},
	{
		name: "a context line with an escape",
		req: llm.ExplainRequest{
			Trigger: model.LogLine{Raw: "[FATAL] connect postgres://svcuser:pwsecret@pghost/db failed", Source: "stdin"},
			Context: []string{`{"msg":"replayed postgres:\/\/<TOKEN_1>@<HOST_1>\/db"}`},
		},
		wantRaw: "[FATAL] connect postgres://<TOKEN_2>@<HOST_2>/db failed", wantSource: "stdin",
		wantContext: []string{`{"msg":"replayed postgres:\/\/<TOKEN_1>@<HOST_1>\/db"}`},
	},
}

func TestAnonymizeKeepsPlaceholderTextAcrossFields(t *testing.T) {
	for _, r := range placeholderTextRequests {
		t.Run(r.name, func(t *testing.T) {
			echo := &echoBackend{}
			resp, err := llm.NewAnonymizing(echo).Explain(context.Background(), r.req)
			if err != nil {
				t.Fatalf("Explain refused: %v", err)
			}
			if g := echo.got; g.Trigger.Raw != r.wantRaw || g.Trigger.Source != r.wantSource ||
				g.Template != r.wantTemplate || strings.Join(g.Context, "\n") != strings.Join(r.wantContext, "\n") {
				t.Errorf("sent:\n raw:      %q\n source:   %q\n template: %q\n context:  %q\nwant:\n raw:      %q\n source:   %q\n template: %q\n context:  %q",
					g.Trigger.Raw, g.Trigger.Source, g.Template, g.Context, r.wantRaw, r.wantSource, r.wantTemplate, r.wantContext)
			}
			if want := echoOf(r.req); resp != want {
				t.Errorf("restored:\n %+v\nwant the request back:\n %+v", resp, want)
			}
		})
	}
}

// TestAnonymizeRoundTripsEveryRequestField: whatever the request carries, every field the model is shown
// comes back through Restore exactly as the log wrote it. A placeholder minted for a value that some field
// also carries as text breaks that, so this is #142 over the hand rows and over generated requests: values
// and literal placeholders of every tag, in every field, before, after and between each other.
func TestAnonymizeRoundTripsEveryRequestField(t *testing.T) {
	reqs := make([]llm.ExplainRequest, 0, len(placeholderTextRequests)+500)
	for _, r := range placeholderTextRequests {
		reqs = append(reqs, r.req)
	}
	reqs = append(reqs, placeholderTextGeneratedRequests(500)...)
	collided := 0
	for _, req := range reqs {
		echo := &echoBackend{}
		resp, err := llm.NewAnonymizing(echo).Explain(context.Background(), req)
		if err != nil {
			t.Errorf("Explain refused:\n req: %+v\n err: %v", req, err)
			continue
		}
		if want := echoOf(req); resp != want {
			collided++
			if collided <= 5 {
				t.Errorf("a field did not come back as the log wrote it:\n got:  %+v\n want: %+v", resp, want)
			}
		}
	}
	if collided > 0 {
		t.Errorf("%d of %d requests did not round-trip", collided, len(reqs))
	}
}

// placeholderTextGeneratedRequests builds n requests from a fixed PCG seed, so a failure reproduces. Values
// are ones the chain masks whole; literals are placeholders the chain would mint for them.
func placeholderTextGeneratedRequests(n int) []llm.ExplainRequest {
	r := rand.New(rand.NewPCG(14, 3))
	values := []string{
		"db.corp.internal", "cache.acme.internal", "10.0.0.5", "192.168.1.20", "bob@corp.com", "alice@acme.io",
		"550e8400-e29b-41d4-a716-446655440000", "/home/deploy/x", "postgres://svcuser:pwsecret@pghost/db",
	}
	tags := []string{"HOST", "IP", "EMAIL", "UUID", "USER", "TOKEN"}
	words := []string{"login", "from", "retry", "as", "peer", "down", "connect", "failed"}
	literal := func() string {
		return "<" + tags[r.IntN(len(tags))] + "_" + string(rune('1'+r.IntN(3))) + ">"
	}
	line := func() string {
		parts := make([]string, 0, 6)
		for k := 1 + r.IntN(5); k > 0; k-- {
			switch r.IntN(3) {
			case 0:
				parts = append(parts, values[r.IntN(len(values))])
			case 1:
				parts = append(parts, literal())
			default:
				parts = append(parts, words[r.IntN(len(words))])
			}
		}
		return strings.Join(parts, " ")
	}
	out := make([]llm.ExplainRequest, n)
	for i := range out {
		req := llm.ExplainRequest{Trigger: model.LogLine{Raw: "[FATAL] " + line(), Source: "stdin"}}
		if r.IntN(4) == 0 {
			req.Trigger.Source = "proc:" + line()
		}
		if r.IntN(2) == 0 {
			req.Template = line()
		}
		for k := r.IntN(4); k > 0; k-- {
			req.Context = append(req.Context, line())
		}
		out[i] = req
	}
	return out
}
