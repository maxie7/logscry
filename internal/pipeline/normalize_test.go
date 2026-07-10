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
		{"colon prefix", "WARN: retrying", "WARN", "retrying"},
		{"logfmt prefix", "level=error something broke", "ERROR", "something broke"},
		{"lowercase bracket", "[info] ready", "INFO", "ready"},
		{"no level", "just a plain line", "", "just a plain line"},
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
