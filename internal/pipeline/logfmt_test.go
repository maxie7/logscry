// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// TestIssue83LogfmtLines is #83's report, line for line: two dockerd-style info lines and
// two different errors, in the logfmt shape logrus and slog's text handler write. On the
// text path the quoted msg was masked whole to <STR>, so each level was ONE template, and
// "level=" was read only at the start of a line, so a line that begins with "time=" got no
// level — a new error from such a service could not escalate on its own.
func TestIssue83LogfmtLines(t *testing.T) {
	const (
		info1 = `time="2026-07-13T12:00:00.101+02:00" level=info msg="Starting up"`
		info2 = `time="2026-07-13T12:00:01.202+02:00" level=info msg="Loading containers: done."`
		err1  = `time="2026-07-13T12:00:02.303+02:00" level=error msg="failed to start container abc: no space left on device"`
		err2  = `time="2026-07-13T12:00:03.404+02:00" level=error msg="TLS handshake error from 10.0.0.5: EOF"`
	)
	p := New(nil)
	base := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	var evs []Event
	for i, raw := range []string{info1, info2, err1, err2} {
		evs = append(evs, p.Process(model.LogLine{Raw: raw}, base.Add(time.Duration(i)*time.Second)))
	}

	if evs[0].Hash == evs[1].Hash {
		t.Errorf("two different info messages share one template: %q", evs[0].Pattern)
	}
	if evs[2].Hash == evs[3].Hash {
		t.Errorf("two different errors share one template: %q", evs[2].Pattern)
	}
	for i, want := range []string{"INFO", "INFO", "ERROR", "ERROR"} {
		if got := evs[i].Line.Level; got != want {
			t.Errorf("line %d: Level = %q, want %q (pattern %q)", i+1, got, want, evs[i].Pattern)
		}
	}
	for i, ev := range evs {
		if ev.Count != 1 {
			t.Errorf("line %d: x%d, want x1 — four messages are four templates (pattern %q)", i+1, ev.Count, ev.Pattern)
		}
	}
}

// TestLogfmtRecognition is decision 1 of #83: what counts as a logfmt line. The whole line
// must parse as two or more key=value pairs, and one key must be a level or message key.
// Prose carrying a key=value or two keeps the text path — it fails at its first bare word.
func TestLogfmtRecognition(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		// Claimed: the shapes in the ten captured boots, rebuilt with neutral values.
		{"logrus, quoted time", `time="2026-07-13T12:00:00+02:00" level=info msg="Starting up"`, true},
		{"logrus with trailing fields", `time="2026-07-13T12:00:00+02:00" level=info msg="loading plugin" id=io.example.v1 type=io.example`, true},
		{"slog text handler, bare time", `time=2026-07-13T12:00:00.108+02:00 level=ERROR source=worker.go:88 msg="worker exited" error="exit status 2"`, true},
		{"level first", `level=error msg="conn refused"`, true},
		{"bare msg", `level=info msg=done`, true},
		{"empty value", `level=info msg= id=7`, true},
		{"lvl alias", `t=2026-07-13T12:00:00+02:00 lvl=warn msg=retrying`, true},
		{"message key only", `ts=1788000000 msg="disk full"`, true},
		{"escaped quote in msg", `level=info msg="Using root \"/srv\""`, true},
		{"surrounding whitespace", "  level=info msg=x\t", true},

		// Not claimed: prose, and all-pairs text that is not a log line.
		{"prose with one pair", "user=bob login failed", false},
		{"level prefix then prose", "level=error something broke", false},
		{"pairs then prose", `level=info msg="x" and then some`, false},
		{"prose, replay fixture shape", "request from 198.51.100.7 to api.example.com served id=0 in 12ms", false},
		{"demo api shape", "INFO: request method=GET path=/api/users status=200 dur=12ms", false},
		{"demo worker shape", "INFO: job=1 status=done queue=checkout dur=12ms", false},
		{"single pair", "level=error", false},
		{"env var", "HOME=/", false},
		{"all pairs, no level or msg", "user=bob ip=192.0.2.1", false},
		{"audit record", `apparmor="DENIED" operation="dbus_method_call" bus="session"`, false},
		{"unterminated quote", `level=info msg="oops`, false},
		{"quote inside a bare value", `level=info msg=a"b`, false},
		{"= inside a bare value", `level=info url=/x?a=b`, false},
		{"text glued to a closing quote", `level=info msg="a"b`, false},
		{"key starts with a digit", `level=info 2fa=on`, false},
		{"plaintext level", "[ERROR] disk full", false},
		// Two logfmt lines joined by a line break, as a grouped event carries them: several
		// physical lines, not one logfmt line.
		{"grouped lines", "time=2026-07-13T12:00:00.000+02:00 level=INFO source=api.go:31 msg=\"server config\"\n" +
			"time=2026-07-13T12:00:00.001+02:00 level=ERROR source=worker.go:88 msg=\"worker exited\"", false},
		{"carriage return", "level=info msg=a\r\nlevel=info msg=b", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := logfmtSignature(trim(tt.raw)); got != tt.want {
				t.Errorf("logfmt(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestLogfmtTemplate is decision 2: keys and order kept, the message and error templatized
// with the ordinary masks, every other value masked by the rule it had on the text path.
func TestLogfmtTemplate(t *testing.T) {
	sig := func(raw string) string {
		t.Helper()
		pattern, ok := logfmtSignature(raw)
		if !ok {
			t.Fatalf("not recognized as logfmt: %q", raw)
		}
		return pattern
	}

	t.Run("readable", func(t *testing.T) {
		got := sig(`time="2026-07-13T12:00:00.250+02:00" level=error msg="failed to start container 4821: no space left on device" id=c0ffee1234 pid=812 module=store`)
		want := `time=<STR> level=error msg="failed to start container <NUM>: no space left on device" id=<HEX> pid=<NUM> module=store`
		if got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	})
	t.Run("a number-only difference shares a template", func(t *testing.T) {
		a := sig(`time="2026-07-13T12:00:00+02:00" level=info msg="attached /dev/vd0 as disk 0"`)
		b := sig(`time="2026-07-13T12:00:37+02:00" level=info msg="attached /dev/vd1 as disk 1"`)
		if a != b {
			t.Errorf("split: %q vs %q", a, b)
		}
	})
	t.Run("different messages do not", func(t *testing.T) {
		a := sig(`level=info msg="Starting up"`)
		b := sig(`level=info msg="shutdown complete"`)
		if a == b {
			t.Errorf("collapsed into %q", a)
		}
	})
	t.Run("a different error splits", func(t *testing.T) {
		a := sig(`level=error msg="Failed to get event" error="rpc error: code = Unavailable desc = error reading from server: EOF"`)
		b := sig(`level=error msg="Failed to get event" error="context canceled"`)
		if a == b {
			t.Errorf("collapsed into %q", a)
		}
	})
	t.Run("an error differing only in a number does not", func(t *testing.T) {
		a := sig(`level=warning msg="dial failed" error="dial tcp 192.0.2.1:5432: connect: connection refused"`)
		b := sig(`level=warning msg="dial failed" error="dial tcp 192.0.2.9:6432: connect: connection refused"`)
		if a != b {
			t.Errorf("split: %q vs %q", a, b)
		}
	})
	t.Run("other quoted values are masked whole", func(t *testing.T) {
		got := sig(`level=info msg=ready path="/var/lib/x y" address="unix:///run/x.sock"`)
		if want := `level=info msg=ready path=<STR> address=<STR>`; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("keys are literal and order is kept", func(t *testing.T) {
		got := sig(`msg=up level=info ipv6=false http2=on`)
		if want := `msg=up level=info ipv6=false http2=on`; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("escapes inside the message", func(t *testing.T) {
		got := sig(`level=info msg="Using root \"/srv\""`)
		if want := `level=info msg="Using root <STR>"`; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		multi := sig(`level=warning msg="lookup failed: a.so: not found\nb.so: not found"`)
		if want := `level=warning msg="lookup failed: a.so: not found b.so: not found"`; multi != want {
			t.Errorf("got %q, want %q", multi, want)
		}
	})
	t.Run("ambiguous message key is masked like any value", func(t *testing.T) {
		got := sig(`level=info msg="a 1" message="b 2"`)
		if want := `level=info msg=<STR> message=<STR>`; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("the fragment is the text path's template", func(t *testing.T) {
		const text = "TLS handshake error from 10.0.0.5:51234: EOF after 12ms"
		got := sig(`level=error msg=` + strconv.Quote(text))
		frag, _ := Templatize(text)
		if want := `level=error msg=` + strconv.Quote(frag); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// TestLogfmtLevel is decision 3: inside a logfmt line the level is read wherever its key
// sits, and nowhere else.
func TestLogfmtLevel(t *testing.T) {
	tests := []struct {
		name  string
		line  model.LogLine
		level string
	}{
		{"after time", model.LogLine{Raw: `time="2026-07-13T12:00:00+02:00" level=error msg="x"`}, "ERROR"},
		{"warning synonym", model.LogLine{Raw: `time="2026-07-13T12:00:00+02:00" level=warning msg="x"`}, "WARN"},
		{"slog uppercase", model.LogLine{Raw: `time=2026-07-13T12:00:00.108+02:00 level=ERROR source=a.go:1 msg="x"`}, "ERROR"},
		{"quoted value", model.LogLine{Raw: `ts=1 level="fatal" msg="x"`}, "FATAL"},
		{"last key", model.LogLine{Raw: `ts=1 msg="x" level=info`}, "INFO"},
		{"lvl alias", model.LogLine{Raw: `t=1 lvl=crit msg="x"`}, "CRITICAL"},
		{"severity alias", model.LogLine{Raw: `ts=1 severity=warn msg="x"`}, "WARN"},
		{"unknown value", model.LogLine{Raw: `ts=1 level=verbose msg="x"`}, ""},
		{"no level key", model.LogLine{Raw: `ts=1 msg="x"`}, ""},
		// The source's level still wins over anything read from the text.
		{"source level wins", model.LogLine{Raw: `ts=1 level=info msg="x"`, Level: "ERROR"}, "ERROR"},
		// Mid-line in prose is #31's, not this: only a logfmt line is read anywhere.
		{"prose mid-line stays unread", model.LogLine{Raw: "connection lost level=error retrying"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(tt.line).Level; got != tt.level {
				t.Errorf("Level = %q, want %q", got, tt.level)
			}
		})
	}
}

// TestNonLogfmtLinesKeepTheirTemplate is decision 4: template hashes move for logfmt lines
// only. Every line here reaches the text path through the dispatcher byte for byte —
// Templatize over normalizePlain's message, the pre-#83 composition — with its level
// unchanged. The rows are the demo's shapes, the replay fixture's, prose carrying pairs,
// and the shapes TestTextPathUnchanged pins.
func TestNonLogfmtLinesKeepTheirTemplate(t *testing.T) {
	lines := []string{
		"INFO: request method=GET path=/api/users status=200 dur=12ms",
		"ERROR: cache miss key=session:78, falling back to db",
		"INFO: job=6 status=done queue=checkout dur=12ms",
		"FATAL: checkout worker: connect to postgres:5432 failed: dial tcp 172.20.0.9:5432: connect: connection refused",
		"request from 198.51.100.7 to api.example.com served id=0 in 12ms",
		"db retry attempt 0 to [2001:db8::10]:5432",
		"INFO: shutting down",
		"user=bob login failed",
		"level=error something broke",
		"level=error",
		"connection lost level=error retrying",
		"Session opened: sender=:1.7 path=/org/example/Session1",
		"eth0: LINK-CHANGE up=1 speed=1000 duplex=full",
		"HOME=/",
		"user=bob ip=192.0.2.1",
		`apparmor="DENIED" operation="dbus_method_call" bus="session"`,
		`level=info msg="unterminated`,
		"[ERROR] disk full",
		"user 4821 failed",
		"{not json at all",
		"panic: boom\n\tmain.go:42 +0x1d",
		"time=2026-07-13T12:00:00.000+02:00 level=INFO source=api.go:31 msg=\"server config\"\n" +
			"time=2026-07-13T12:00:00.001+02:00 level=ERROR source=worker.go:88 msg=\"worker exited\"",
	}
	for _, raw := range lines {
		t.Run(raw, func(t *testing.T) {
			if _, ok := logfmtSignature(trim(raw)); ok {
				t.Fatalf("claimed as logfmt: %q", raw)
			}
			line := Normalize(model.LogLine{Raw: raw})
			gotPattern, gotHash := TemplatizeLine(line)
			wantLevel, msg := normalizePlain(raw)
			wantPattern, wantHash := Templatize(msg)
			if gotPattern != wantPattern || gotHash != wantHash {
				t.Errorf("template moved: %q/%s, want %q/%s", gotPattern, gotHash, wantPattern, wantHash)
			}
			if line.Level != wantLevel {
				t.Errorf("Level = %q, want %q", line.Level, wantLevel)
			}
		})
	}
}

// TestLogfmtErrorEscalatesOnItsOwn is the effect #83 was about, through the scorer: a new
// error from a logfmt service now escalates on novelty + ERROR, as a plain-text one does.
// The rest of the matrix holds #27's rule — a new INFO or WARN stays quiet, on stdout and
// on stderr, which is where Docker and subprocess sources deliver much logfmt. 0.95 (a new
// WARN on stderr) is the loudest a merely-new line gets.
func TestLogfmtErrorEscalatesOnItsOwn(t *testing.T) {
	p := New(score.New(scoringConfig(), nil))
	base := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		raw      string
		stream   model.Stream
		level    string
		score    float64
		escalate bool
	}{
		{"new INFO", `time="2026-07-13T12:00:00+02:00" level=info msg="Starting up"`, model.Stdout, "INFO", 0.45, false},
		{"new WARN", `time="2026-07-13T12:00:01+02:00" level=warning msg="slow disk detected"`, model.Stdout, "WARN", 0.65, false},
		{"new ERROR", `time="2026-07-13T12:00:02+02:00" level=error msg="Failed to get event" error="rpc error: code = Unavailable"`, model.Stdout, "ERROR", 1.05, true},
		{"known ERROR", `time="2026-07-13T12:00:03+02:00" level=error msg="Failed to get event" error="rpc error: code = Unavailable"`, model.Stdout, "ERROR", 0.60, false},
		{"new INFO on stderr", `time="2026-07-13T12:00:04+02:00" level=info msg="Loading containers: done."`, model.Stderr, "INFO", 0.75, false},
		{"new WARN on stderr", `time="2026-07-13T12:00:05+02:00" level=warning msg="device node missing"`, model.Stderr, "WARN", 0.95, false},
		{"new ERROR on stderr", `time="2026-07-13T12:00:06+02:00" level=error msg="TLS handshake error from 10.0.0.5: EOF"`, model.Stderr, "ERROR", 1.35, true},
	}
	for i, tt := range tests {
		ev := p.Process(model.LogLine{Raw: tt.raw, Stream: tt.stream}, base.Add(time.Duration(i)*time.Second))
		if ev.Line.Level != tt.level {
			t.Errorf("%s: Level = %q, want %q", tt.name, ev.Line.Level, tt.level)
		}
		if math.Abs(ev.Score-tt.score) > 1e-9 || ev.Escalate != tt.escalate {
			t.Errorf("%s: score %.2f escalate %v, want %.2f %v (reasons %v)",
				tt.name, ev.Score, ev.Escalate, tt.score, tt.escalate, ev.Reasons)
		}
	}
}

// trim is the dispatcher's own normalization of Raw before either structured branch.
func trim(s string) string { return strings.TrimSpace(s) }
