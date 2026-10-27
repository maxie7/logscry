// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// TestIssue31LevelAfterAPrefix is #31: the plaintext detector read a level only at the start
// of a line, so every format that writes its own timestamp, pid, thread or logger column
// first got no level from any source. One row per documented default layout; the shapes
// are the frameworks', the values are invented.
//
// A level found after a header leaves the message as the raw line: nothing is stripped, so
// no template hash moves.
func TestIssue31LevelAfterAPrefix(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantLevel string
	}{
		{"Spring Boot console", "2026-07-13T10:00:00.123Z ERROR 4242 --- [demo] [           main] c.e.demo.Application     : payment declined", "ERROR"},
		{"Spring Boot console, padded level", "2026-07-13T10:00:00.123Z  WARN 4242 --- [demo] [           main] c.e.demo.Application     : pool nearly exhausted", "WARN"},
		{"Spring Boot 2 console, the issue's line", "2026-01-15 10:23:45.123 ERROR 12345 --- [http-nio-8080] com.Foo : x", "ERROR"},
		{"Logback without a configuration", "10:00:00.123 [main] ERROR com.example.App -- payment declined", "ERROR"},
		{"Log4j2 default configuration", "10:00:00.123 [main] WARN  com.example.App - pool nearly exhausted", "WARN"},
		{"Python asctime - name - levelname", "2026-07-13 10:00:00,123 - billing - ERROR - payment declined", "ERROR"},
		{"Python asctime levelname", "2026-07-13 10:00:00,123 ERROR payment declined", "ERROR"},
		{"Celery worker", "[2026-07-13 10:00:00,123: ERROR/MainProcess] Task billing.charge raised", "ERROR"},
		{"gunicorn", "[2026-07-13 10:00:00 +0000] [4242] [ERROR] Worker timed out", "ERROR"},
		{"loguru", "2026-07-13 10:00:00.123 | WARNING  | app.db:connect:42 - pool nearly exhausted", "WARN"},
		{"Go slog default handler", "2026/07/13 10:00:00 ERROR connection refused count=3", "ERROR"},
		{"Go log, level in the message", "2026/07/13 10:00:00 ERROR: connection refused", "ERROR"},
		{"Go log, title-case level and a colon", "2026/07/13 10:00:00 Warning: disk low", "WARN"},
		{"Go log Lmsgprefix", "2026/07/13 10:00:00 [ERROR] connection refused", "ERROR"},
		{"Go log Lshortfile", "2026/07/13 10:00:00 db.go:17: ERROR: db unreachable", "ERROR"},
		{"Go log Lshortfile, no timestamp", "main.go:42: WARNING: disk low", "WARN"},
		{"Ruby Logger, Rails production", "E, [2026-07-13T10:00:00.123456 #4242] ERROR -- : payment declined", "ERROR"},
		{"Ruby Logger, padded level", "I, [2026-07-13T10:00:00.123456 #4242]  INFO -- : Started GET", "INFO"},
		{"nginx error log", "2026/07/13 10:00:00 [error] 4242#4242: *1 open() failed (2: No such file or directory)", "ERROR"},
		{"PostgreSQL log_line_prefix", `2026-07-13 10:00:00.123 UTC [4242] ERROR:  relation "orders" does not exist`, "ERROR"},
		{"klog error", "E0713 10:00:00.123456    4242 server.go:42] payment declined", "ERROR"},
		{"klog fatal", "F0713 10:00:00.123456    4242 server.go:42] cannot bind", "FATAL"},
		{"klog warning", "W0713 10:00:00.123456    4242 server.go:42] slow sync", "WARN"},
		{"klog info", "I0713 10:00:00.123456    4242 server.go:42] synced", "INFO"},
		{"first word, dash separator (the issue's)", "ERROR - fake service error", "ERROR"},
		{"first word, pipe separator", "WARN | pool nearly exhausted", "WARN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(model.LogLine{Raw: tt.raw})
			if got.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tt.wantLevel)
			}
			if got.Message != tt.raw {
				t.Errorf("Message = %q, want the raw line: a level after a header strips nothing", got.Message)
			}
			if got.Raw != tt.raw {
				t.Errorf("Raw modified: %q", got.Raw)
			}
		})
	}
}

// TestLevelAtLineStartIsUnchanged pins the formats whose level already sits at position 0,
// which the leading-token rule reads and strips exactly as before #31. JUL's SEVERE is not a
// level word logscry knows; that is a vocabulary question, not #31's.
func TestLevelAtLineStartIsUnchanged(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantLevel   string
		wantMessage string
	}{
		{"java.util.logging, second line", "WARNING: disk low", "WARN", "disk low"},
		{"java.util.logging SEVERE", "SEVERE: payment declined", "", "SEVERE: payment declined"},
		{"Python basicConfig default", "ERROR:root:payment declined", "ERROR", "root:payment declined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(model.LogLine{Raw: tt.raw})
			if got.Level != tt.wantLevel || got.Message != tt.wantMessage {
				t.Errorf("got %q / %q, want %q / %q", got.Level, got.Message, tt.wantLevel, tt.wantMessage)
			}
		})
	}
}

// TestProseMentioningALevelStaysUnread is the precision side of #31. A level read from prose
// is #27's cry-wolf in a new place: a new line at ERROR scores 1.05, and one at a fatal-class
// level 1.00 on its own. A line that starts with an ordinary word is never read, whatever
// follows, and a bare upper-case level as the first word needs a delimiter after it.
func TestProseMentioningALevelStaysUnread(t *testing.T) {
	lines := []string{
		"connection error, retrying in 5s",
		"failed to open config: error 2",
		"Retrying after ERROR from upstream",
		"upstream returned ERROR; giving up",
		"request failed with Error: timeout",
		"disk usage warning threshold reached",
		"user muted WARN notifications.",
		"worker exited: fatal signal 11",
		"Encountered a FATAL condition, shutting down!",
		"Fatal error in module loader",
		"Error rate is 0.3% (warn at 1%)",
		"error-prone path taken",
		"Warning signs ignored by the operator",
		"ERRORS: 3, WARNINGS: 2",
		`the word "ERROR" appears in quotes`,
		"job finished (with warnings)",
		"3 errors, 2 warnings",
		"2026-07-13 10:00:00 retry after error: timeout",
		// After a header, a bare lower-case level word is still prose.
		"2026/07/13 10:00:00 error connecting to db",
		"panic averted by the watchdog",
		"PID 4242 reported an ERROR",
		"WARN-level messages are suppressed",
		"see the error: section below",
		// A bare first word followed by a plain word: each would be fatal-class and
		// escalate alone at 1.00.
		"PANIC button pressed",
		"FATAL attraction",
		"CRITICAL path updated",
		"DEBUG mode enabled",
		// An unclosed bracket does not turn the rest of the line into a header.
		"[retry 3 of 5: upstream said ERROR",
	}
	for _, raw := range lines {
		t.Run(raw, func(t *testing.T) {
			got := Normalize(model.LogLine{Raw: raw})
			if got.Level != "" {
				t.Errorf("Level = %q, want none: prose is not a level", got.Level)
			}
			if got.Message != raw {
				t.Errorf("Message = %q, want the raw line", got.Message)
			}
		})
	}
}

// TestShapesLeftUnread pins what #31 deliberately does not read. Chromium's level is glued
// inside its bracket by colons: on a desktop it is browser chatter, and reading it was
// measured at 82 more escalations over ten boots that had 59. An ANSI-coloured line is
// its own question: an escape sequence is not a header column, even where a bracketed level
// follows it, which on the same boots would have added 15 escalations. The rest are
// vocabulary or a prose word before the level.
func TestShapesLeftUnread(t *testing.T) {
	tests := []struct{ name, raw string }{
		{"Chromium", "[4242:4243:0713/100000.123456:ERROR:ui/base/widget.cc(42)] widget lost focus"},
		{"ANSI colour", "\x1b[0;1mERROR: payment declined"},
		{"ANSI colour around a bracketed level", "\x1b[1;33m[WARN 10:00:00.123456]\x1b[0m cache rebuilt"},
		{"syslog file with a host", "Jul 13 10:00:00 web billing[4242]: ERROR: payment declined"},
		{"Apache module:level", "[Mon Jul 13 10:00:00.123456 2026] [core:error] [pid 4242:tid 4243] AH00037: Symbolic link not allowed"},
		{"quoted token in a JSON array", `["2026-07-13T10:00:00", "ERROR", "payment declined"]`},
		{"nginx emerg", "2026/07/13 10:00:00 [emerg] 4242#4242: bind() to 0.0.0.0:80 failed"},
		{".NET console", "fail: Microsoft.AspNetCore.Server.Kestrel[13]"},
		{"PostgreSQL LOG", "2026-07-13 10:00:00.123 UTC [4242] LOG:  checkpoint starting: time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(model.LogLine{Raw: tt.raw}).Level; got != "" {
				t.Errorf("Level = %q, want none", got)
			}
		})
	}
}

// TestLeveledInfoTemplatesStayQuiet is #27's scenario in a header format: 400 distinct,
// benign Spring Boot INFO lines, now read as INFO, through the pipeline and the live scorer.
// Reading the level must not make any of them news.
func TestLeveledInfoTemplatesStayQuiet(t *testing.T) {
	p := New(score.New(scoringConfig(), nil))
	base := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)

	const n = 400
	for i := range n {
		raw := "2026-07-13T10:00:00.123Z  INFO 4242 --- [demo] [           main] c.e.demo.Application     : started " + word(i)
		ev := p.Process(model.LogLine{Raw: raw}, base.Add(time.Duration(i)*time.Second))
		if ev.Line.Level != "INFO" {
			t.Fatalf("line %d: Level = %q, want INFO", i, ev.Line.Level)
		}
		if ev.Escalate {
			t.Fatalf("benign INFO template %d escalated: score %.2f, reasons %v", i, ev.Score, ev.Reasons)
		}
	}
	if s := p.Stats(); s.Escalated != 0 || s.Suppressed != 0 || s.Cached != 0 {
		t.Errorf("stats = %+v on %d benign INFO templates, want nothing to reach the threshold", s, n)
	}
}

// word spells i in letters, so each line is its own template: digits would be masked to
// <NUM> and fold them all into one.
func word(i int) string {
	var b strings.Builder
	for {
		b.WriteByte(byte('a' + i%26))
		i /= 26
		if i == 0 {
			return b.String()
		}
	}
}
