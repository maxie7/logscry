// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/llm"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// TestAnonymizeVendorKeyWithAnInnerTagOnTheWire is GHSA-8qq6-vx8j-rx37 on the wire: a vendor key whose body
// an earlier rule tags inside, on the Template field the pipeline writes for it, and an AWS secret access key
// with a home directory in it. No fragment of either is in the request, in any field. Each key is invented
// and built by concatenation.
func TestAnonymizeVendorKeyWithAnInnerTagOnTheWire(t *testing.T) {
	for _, c := range []struct {
		name, raw string
		sent      []string
	}{
		{"google, sk on the template", "[FATAL] key " + "AI" + "za" + "SyDqkxLmQ4" + "s" + "k7" + "AbZtWnBvLcHpRyMaKdFgJcu" + " rejected",
			[]string{"SyDqkxLm", "ZtWnBvLc", "HpRyMaKd"}},
		{"stripe, sk on the template", "[FATAL] key " + "s" + "k_live_" + "Rk5" + "s" + "k7" + "AbQzWvLpXnYtBcHdMqJr" + " rejected",
			[]string{"live_Rk", "AbQzWvLp", "XnYtBcHd"}},
		{"google, an sk key in the body", "[FATAL] key " + "AI" + "za" + "SyDqkxLm-" + "s" + "k-" + "QxZtWnBvLcHpRyMaKdFgJcu" + " rejected",
			[]string{"SyDqkxLm", "QxZtWnBv", "HpRyMaKd"}},
		{"aws secret by name, a home directory in it", "[FATAL] AWS_SECRET_ACCESS_KEY=" + "vMlpuXVqGsysu" + "/home/bob/" + "XbUnszngilzmjKwtT",
			[]string{"vMlpuXVq", "XbUnszng", "home/bob"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			line := model.LogLine{Source: "stdin", Raw: c.raw}
			tpl, _ := pipeline.TemplatizeLine(pipeline.Normalize(line))
			srv, body, calls := recordingServer(t, `{"summary":"ok","likely_cause":"ok","suggestion":"ok"}`)
			req := llm.ExplainRequest{Trigger: line, Template: tpl, Count: 1}
			if _, err := backendTo(srv, true).Explain(context.Background(), req); err != nil {
				t.Fatalf("Explain errored, so the escalation was skipped: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("provider called %d times, want 1", calls.Load())
			}
			sent := *body.Load()
			for _, s := range c.sent {
				if strings.Contains(sent, s) {
					t.Errorf("a fragment of the key, %q, is on the wire", s)
				}
			}
		})
	}
}
