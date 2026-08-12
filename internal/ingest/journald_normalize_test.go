// SPDX-License-Identifier: Apache-2.0

// This file is the end-to-end half of the journald level mapping: what a unit's line
// finally scores as depends on BOTH the source's PRIORITY handling and the pipeline's
// precedence rule, and issue #24 was a disagreement between exactly those two. Testing
// either alone would have missed it, which is why these tests run decode and Normalize
// back to back rather than asserting on a hand-built model.LogLine.
//
// It is an external test package so the dependency on internal/pipeline is unambiguously
// test-only. internal/pipeline imports internal/{model,export,score} and nothing from
// internal/ingest, so there is no cycle.
package ingest_test

import (
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/ingest"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// journaldLine runs one journal entry through the two stages a real line goes through:
// the source's decode, then the pipeline's Normalize.
func journaldLine(t *testing.T, entry string) model.LogLine {
	t.Helper()
	return pipeline.Normalize(ingest.Decode(model.LogLine{
		Time:   time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		Source: "journald",
		Stream: model.Stdout,
		Raw:    entry,
	}))
}

// entryAt builds a journal entry with the given PRIORITY and MESSAGE. message is
// embedded as a JSON string, so a JSON payload logged to stdout works too.
func entryAt(t *testing.T, priority, message string) string {
	t.Helper()
	return `{"PRIORITY":"` + priority + `","MESSAGE":` + quoteJSON(message) + `}`
}

// TestJournaldDefaultPriorityDefersToTheText is issue #24. systemd records a unit's
// captured stdout at PRIORITY=6 whatever the text says, so at 6 the priority carries no
// severity information at all — it means "this came off stdout", not "this is
// informational". Treating it as a level threw away the only real signal such a line has,
// and a service printing "ERROR: ..." to stdout was scored, and stayed silent, as INFO.
//
// At 6 the source now sets no level, so Normalize's detection runs and the text is
// believed. Every form the detector supports is covered, including the JSON one, which is
// the larger real-world class: services logging structured JSON to stdout.
func TestJournaldDefaultPriorityDefersToTheText(t *testing.T) {
	tests := []struct {
		message   string
		wantLevel string
	}{
		{"ERROR: connection refused", "ERROR"},
		{"[ERROR] connection refused", "ERROR"},
		{`level=error msg="conn refused"`, "ERROR"},
		{`{"level":"error","msg":"conn refused"}`, "ERROR"},
		{"WARN: retrying", "WARN"},
		// No level token and no structured priority means no level, not a manufactured
		// INFO: every renderer already shows an undetected level as "-" (as an "EVENT"
		// badge on a card), and the scorer weighs it at zero either way.
		{"starting up", ""},
	}
	for _, tc := range tests {
		t.Run(tc.message, func(t *testing.T) {
			got := journaldLine(t, entryAt(t, "6", tc.message))
			if got.Level != tc.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tc.wantLevel)
			}
			// Priority 6 is not error-class, so the stream is untouched regardless of
			// what the text claims: systemd does not distinguish captured stdout from
			// captured stderr by priority, so there is nothing to recover here.
			if got.Stream != model.Stdout {
				t.Errorf("Stream = %v, want Stdout", got.Stream)
			}
		})
	}
}

// TestJournaldNonDefaultPriorityStillWinsOverTheText is the other half of the rule, and
// the behaviour that must NOT change: a priority other than 6 was set deliberately
// through the journal protocol, which is worth more than a regex guess about prose.
func TestJournaldNonDefaultPriorityStillWinsOverTheText(t *testing.T) {
	t.Run("an error priority beats a disagreeing text token", func(t *testing.T) {
		got := journaldLine(t, entryAt(t, "3", "INFO: unit stopped"))
		if got.Level != "ERROR" {
			t.Errorf("Level = %q, want the journal's ERROR", got.Level)
		}
		if got.Stream != model.Stderr {
			t.Errorf("Stream = %v, want Stderr", got.Stream)
		}
		// The source supplies a level, not a parse: the message is still normalized and
		// still loses its recognized prefix.
		if want := "unit stopped"; got.Message != want {
			t.Errorf("Message = %q, want %q", got.Message, want)
		}
	})

	// Priority 5 (notice) is a KNOWN LIMITATION, pinned here rather than special-cased.
	// It is non-default, so it is taken at its word — a unit configured SyslogLevel=notice
	// still loses a text-reported ERROR. Special-casing it would mean second-guessing a
	// priority somebody chose, which is the opposite of what this fix decided to trust.
	t.Run("notice is non-default, so a text ERROR is still lost", func(t *testing.T) {
		got := journaldLine(t, entryAt(t, "5", "ERROR: still lost"))
		if got.Level != "INFO" {
			t.Errorf("Level = %q, want INFO — priority 5 is authoritative by design", got.Level)
		}
	})

	// Priority 7 (debug) is the same case at the other end of the scale.
	t.Run("debug is non-default, so a text ERROR is still lost", func(t *testing.T) {
		got := journaldLine(t, entryAt(t, "7", "ERROR: still lost"))
		if got.Level != "DEBUG" {
			t.Errorf("Level = %q, want DEBUG — priority 7 is authoritative by design", got.Level)
		}
	})
}

// quoteJSON renders s as a JSON string literal. Hand-rolled rather than pulled from
// encoding/json so a test helper cannot fail: the inputs above are plain ASCII, and the
// only character needing an escape is the quote in a JSON payload.
func quoteJSON(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := range len(s) {
		if c := s[i]; c == '"' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(append(out, '"'))
}
