// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"testing"

	"github.com/maxie7/logscry/internal/model"
)

func TestNormalizeJSONLevelAndMessage(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantLevel   string
		wantMessage string
	}{
		{"level+msg", `{"level":"info","msg":"server started"}`, "INFO", "server started"},
		{"message key", `{"level":"error","message":"boom"}`, "ERROR", "boom"},
		{"text key", `{"severity":"warn","text":"slow"}`, "WARN", "slow"},
		{"synonym warning", `{"level":"warning","msg":"x"}`, "WARN", "x"},
		{"synonym err", `{"level":"err","msg":"x"}`, "ERROR", "x"},
		{"synonym crit", `{"level":"crit","msg":"x"}`, "CRITICAL", "x"},
		{"log.level key", `{"log.level":"debug","msg":"x"}`, "DEBUG", "x"},
		{"lvl key", `{"lvl":"fatal","msg":"x"}`, "FATAL", "x"},
		{"leading space", `   {"level":"panic","msg":"x"}`, "PANIC", "x"},
		{"no message field falls back to raw", `{"level":"info","k":"v"}`, "INFO", `{"level":"info","k":"v"}`},
		{"unknown level dropped", `{"level":"verbose","msg":"x"}`, "", "x"},
		// Case is a logger's choice, not a different field (M9).
		{"uppercase keys", `{"LEVEL":"error","MSG":"boom"}`, "ERROR", "boom"},
		{"mixed-case keys", `{"Severity":"warn","Message":"slow"}`, "WARN", "slow"},
		// A null value is not a message: fall back rather than adopt an empty one.
		{"null message falls back to raw", `{"level":"info","msg":null}`, "INFO", `{"level":"info","msg":null}`},
		{"null level", `{"level":null,"msg":"x"}`, "", "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(model.LogLine{Raw: tt.raw})
			if got.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tt.wantLevel)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
			if got.Raw != tt.raw {
				t.Errorf("Raw modified: %q", got.Raw)
			}
		})
	}
}

func TestNormalizePlaintext(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantLevel   string
		wantMessage string
	}{
		{"bracket prefix", "[ERROR] disk full", "ERROR", "disk full"},
		{"explicit bracket info", "[INFO] server started", "INFO", "server started"},
		{"colon prefix", "WARN: retrying", "WARN", "retrying"},
		{"logfmt prefix", "level=error something broke", "ERROR", "something broke"},
		{"lowercase bracket", "[info] ready", "INFO", "ready"},
		{"no level", "just a plain line", "", "just a plain line"},
		// Level detection fires ONLY on explicit leading markers ([LVL], LVL:,
		// level=lvl). A bare level word — mid-sentence or even leading without a
		// colon/bracket — is prose, not a level: don't mislabel it (this feeds
		// M3's severity signal).
		{"bare word mid-sentence not a level", "plain info line", "", "plain info line"},
		{"bare leading word not a level", "info server started", "", "info server started"},
		{"colon but not a level", "http://example.com fetched", "", "http://example.com fetched"},
		{"non-level word with colon", "Starting: booting up", "", "Starting: booting up"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(model.LogLine{Raw: tt.raw})
			if got.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tt.wantLevel)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
			if got.Raw != tt.raw {
				t.Errorf("Raw modified: %q", got.Raw)
			}
		})
	}
}

// TestNormalizeMalformedJSONFallsBack guards the cheap-check path: a line that
// starts with '{' but is not valid JSON must not error — it falls through to the
// plaintext heuristics.
func TestNormalizeMalformedJSONFallsBack(t *testing.T) {
	got := Normalize(model.LogLine{Raw: "{not json at all"})
	if got.Level != "" {
		t.Errorf("Level = %q, want empty", got.Level)
	}
	if got.Message != "{not json at all" {
		t.Errorf("Message = %q, want the raw line", got.Message)
	}
}

// TestNormalizeSourceLevelWinsOverTheText is the journald case, and the reason a Source
// may set Level at all. journald's PRIORITY is recorded by systemd at the journal
// protocol level; a level token in the message body is a regex guess about prose. When
// they disagree the structured value is the one that is not guessing.
//
// The message must still be normalized: the source supplies a level, not a parse, so the
// line goes through the same format detection as any other and still gets its recognized
// prefix stripped.
func TestNormalizeSourceLevelWinsOverTheText(t *testing.T) {
	tests := []struct {
		name        string
		in          model.LogLine
		wantMessage string
	}{
		{
			"a disagreeing plaintext token loses",
			model.LogLine{Level: "ERROR", Raw: "[INFO] db connection dropped"},
			"db connection dropped",
		},
		{
			"a disagreeing JSON level loses",
			model.LogLine{Level: "ERROR", Raw: `{"level":"info","msg":"db connection dropped"}`},
			"db connection dropped",
		},
		{
			"text with no level of its own is simply filled in",
			model.LogLine{Level: "ERROR", Raw: "db connection dropped"},
			"db connection dropped",
		},
		{
			"an agreeing token changes nothing",
			model.LogLine{Level: "ERROR", Raw: "error: db connection dropped"},
			"db connection dropped",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Normalize(tc.in)
			if got.Level != "ERROR" {
				t.Errorf("Level = %q, want the source's ERROR", got.Level)
			}
			if got.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q — message normalization must still run",
					got.Message, tc.wantMessage)
			}
			if got.Raw != tc.in.Raw {
				t.Errorf("Raw = %q, want it untouched", got.Raw)
			}
		})
	}
}

// TestNormalizeDetectsLevelWhenTheSourceSuppliesNone is the regression guard for every
// source that predates journald: stdin, subprocess, and Docker never set Level, so they
// must reach the detection path and come out exactly as they always did.
func TestNormalizeDetectsLevelWhenTheSourceSuppliesNone(t *testing.T) {
	tests := []struct {
		raw         string
		wantLevel   string
		wantMessage string
	}{
		{"[WARN] disk at 91%", "WARN", "disk at 91%"},
		{"error: db connection dropped", "ERROR", "db connection dropped"},
		{`{"level":"fatal","msg":"out of memory"}`, "FATAL", "out of memory"},
		{"just a line", "", "just a line"},
		{"http://example.com/x failed", "", "http://example.com/x failed"},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got := Normalize(model.LogLine{Raw: tc.raw}) // Level empty, as every source leaves it
			if got.Level != tc.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tc.wantLevel)
			}
			if got.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tc.wantMessage)
			}
		})
	}
}
