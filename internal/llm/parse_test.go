// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"strings"
	"testing"
)

// TestParseExplanation is the "assume the model misbehaves" contract, because it will.
// Every case here is something a real model does: fenced blocks, a chatty preamble,
// invented key spellings, and — the one that actually bites in production, because
// max_tokens is set — output that simply stops mid-sentence.
//
// The rule under test is DEGRADE, NEVER DISCARD. Nothing may panic, and nothing the
// model said may be thrown away just because it was not said in the shape we asked for.
func TestParseExplanation(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    ExplainResponse
		wantErr bool
	}{
		{
			name: "clean json",
			raw:  `{"summary":"Postgres refused the connection.","likely_cause":"The db container is not up.","suggestion":"docker compose ps db"}`,
			want: ExplainResponse{
				Summary:     "Postgres refused the connection.",
				LikelyCause: "The db container is not up.",
				Suggestion:  "docker compose ps db",
			},
		},
		{
			name: "fenced json block",
			raw:  "```json\n{\"summary\":\"Nil map write.\",\"likely_cause\":\"Map never made.\",\"suggestion\":\"Init it.\"}\n```",
			want: ExplainResponse{Summary: "Nil map write.", LikelyCause: "Map never made.", Suggestion: "Init it."},
		},
		{
			name: "bare fence with no language tag",
			raw:  "```\n{\"summary\":\"Nil map write.\"}\n```",
			want: ExplainResponse{Summary: "Nil map write."},
		},
		{
			name: "leading prose before the object",
			raw:  "Sure! Here is the analysis you asked for:\n\n{\"summary\":\"Disk is full.\",\"suggestion\":\"df -h\"}",
			want: ExplainResponse{Summary: "Disk is full.", Suggestion: "df -h"},
		},
		{
			name: "trailing prose after the object",
			raw:  `{"summary":"Disk is full."} Let me know if you need more detail!`,
			want: ExplainResponse{Summary: "Disk is full."},
		},
		{
			name: "prose with braces before the real object",
			raw:  "Format is {like this}. Answer:\n{\"summary\":\"Disk is full.\"}",
			want: ExplainResponse{Summary: "Disk is full."},
		},
		{
			// max_tokens ran out mid-object: everything the model finished saying survives.
			name: "truncated after a complete pair",
			raw:  `{"summary":"The service panicked.","likely_cause":"A nil pointer in the handler."`,
			want: ExplainResponse{Summary: "The service panicked.", LikelyCause: "A nil pointer in the handler."},
		},
		{
			// ...and truncated mid-string: the completed fields still land.
			name: "truncated mid string",
			raw:  `{"summary":"The service panicked.","likely_cause":"A nil pointer in the han`,
			want: ExplainResponse{Summary: "The service panicked."},
		},
		{
			name: "alternate key spellings",
			raw:  `{"Summary":"Boom.","likelyCause":"Bad config.","what to check":"the config file"}`,
			want: ExplainResponse{Summary: "Boom.", LikelyCause: "Bad config.", Suggestion: "the config file"},
		},
		{
			// No JSON at all. The model still said something useful, and something beats
			// an empty card.
			name: "plain prose with no json",
			raw:  "The database connection was refused.\nCheck that the container is running.",
			want: ExplainResponse{Summary: "The database connection was refused. Check that the container is running."},
		},
		{
			name: "json with unusable keys degrades to prose",
			raw:  `{"analysis":"something"}`,
			want: ExplainResponse{Summary: `{"analysis":"something"}`},
		},
		{
			name:    "empty response",
			raw:     "   \n\t ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseExplanation(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseExplanation(%q) = %+v, want an error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseExplanation(%q) failed: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("parseExplanation(%q)\n got %+v\nwant %+v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestParseExplanationNeverPanics: the parser sits directly in the path of the tail, so
// a malformed response must degrade, never take the process with it.
func TestParseExplanationNeverPanics(t *testing.T) {
	nasty := []string{
		"{", "}", "{{{{", `{"summary":`, `{"summary":"`, `{"summary":"\`,
		"```json", "```json\n{", `{"summary":{"nested":"object"}}`,
		`{"summary":["an","array"]}`, `{"summary":123}`, `[{"summary":"in an array"}]`,
		"\x00\x01\x02", strings.Repeat("{", 10000),
	}
	for _, raw := range nasty {
		// A panic here fails the test by unwinding it; that is the assertion.
		if _, err := parseExplanation(raw); err != nil && strings.TrimSpace(raw) != "" {
			t.Errorf("parseExplanation(%q) errored on non-empty input: %v", raw, err)
		}
	}
}

// TestPromptFallbackIsBounded: an unhelpful model that answers with a wall of prose must
// not put a wall of prose in a one-line card.
func TestProseFallbackIsBounded(t *testing.T) {
	got, err := parseExplanation(strings.Repeat("blah ", 500))
	if err != nil {
		t.Fatalf("parseExplanation failed: %v", err)
	}
	if n := len([]rune(got.Summary)); n > maxSummaryRunes+len("…[truncated]") {
		t.Errorf("prose fallback is %d runes, want it bounded at %d", n, maxSummaryRunes)
	}
}
