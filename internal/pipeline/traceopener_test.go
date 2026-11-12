// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// TestTraceOpenerCarriesALevel: three runtimes print a fixed line above a trace, and the
// coalescer already opens a block on each (traceStartRe). The event they start had no level
// unless a token came before it, so a new traceback on stderr scored 0.75 and stayed silent.
// Python's and Java's openers are ERROR: the exception may have been caught and logged by a
// service that is still up. Go's "fatal error:" is FATAL: the runtime cannot recover from it
// and exits. The message stays the raw event, so no template moves. All values are invented.
func TestTraceOpenerCarriesALevel(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantLevel string
	}{
		{"Python, grouped", "Traceback (most recent call last):\n" +
			"  File \"/srv/billing/app.py\", line 12, in <module>\n" +
			"    main()\n" +
			"KeyError: 'amount'", "ERROR"},
		{"Python, the opener alone", "Traceback (most recent call last):", "ERROR"},
		{"Java, main thread", "Exception in thread \"main\" java.lang.IllegalStateException: ledger closed\n" +
			"\tat com.example.billing.Ledger.post(Ledger.java:42)\n" +
			"\tat com.example.billing.App.main(App.java:7)", "ERROR"},
		{"Java, pool thread", "Exception in thread \"pool-1-thread-2\" java.lang.NullPointerException", "ERROR"},
		{"Go, deadlock with its dump", "fatal error: all goroutines are asleep - deadlock!\n" +
			"\n" +
			"goroutine 1 [chan receive]:\n" +
			"main.main()\n" +
			"\t/srv/billing/main.go:9 +0x2d", "FATAL"},
		{"Go, the opener alone", "fatal error: concurrent map writes", "FATAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(model.LogLine{Raw: tt.raw})
			if got.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tt.wantLevel)
			}
			if got.Message != tt.raw {
				t.Errorf("Message = %q, want the raw event: reading the opener strips nothing", got.Message)
			}
		})
	}
}

// TestTraceOpenerLosesToEveryOtherLevel: the opener is read only when nothing else gave a
// level. A leading token, the header rule, a JSON or logfmt key and a level the source
// supplied (journald at a priority other than 6) all still decide. Go's "panic:" was always a
// leading LVL: token, and keeps that reading, prefix stripped.
func TestTraceOpenerLosesToEveryOtherLevel(t *testing.T) {
	const tb = "Traceback (most recent call last):"
	tests := []struct {
		name        string
		in          model.LogLine
		wantLevel   string
		wantMessage string
	}{
		{"leading token", model.LogLine{Raw: "[WARN] " + tb}, "WARN", tb},
		{"header rule", model.LogLine{Raw: "2026-07-13 10:00:00,123 WARNING " + tb}, "WARN", "2026-07-13 10:00:00,123 WARNING " + tb},
		{"JSON level key", model.LogLine{Raw: `{"level":"info","msg":"` + tb + `"}`}, "INFO", tb},
		{"JSON without a level key", model.LogLine{Raw: `{"msg":"` + tb + `"}`}, "", tb},
		{"logfmt level key", model.LogLine{Raw: `level=debug msg="` + tb + `"`}, "DEBUG", `msg="` + tb + `"`},
		{"source level", model.LogLine{Level: "WARN", Raw: tb + "\n  File \"/srv/billing/app.py\", line 12"}, "WARN", tb + "\n  File \"/srv/billing/app.py\", line 12"},
		{"Go panic", model.LogLine{Raw: "panic: boom\n\ngoroutine 1 [running]:"}, "PANIC", "boom\n\ngoroutine 1 [running]:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(tt.in)
			if got.Level != tt.wantLevel || got.Message != tt.wantMessage {
				t.Errorf("got %q / %q, want %q / %q", got.Level, got.Message, tt.wantLevel, tt.wantMessage)
			}
		})
	}
}

// TestTraceOpenerLookalikesStayUnread pins what carries no level. A goroutine header on its
// own opens debug.Stack() output and SIGQUIT dumps, which are not errors. A web framework's
// "Internal Server Error: /…" line is prose, and README's prose rule stands. An opener counts
// only at the very start of the event's first line, in the runtime's exact shape. Other
// runtimes' traces (.NET, Node.js, Ruby) are not read.
func TestTraceOpenerLookalikesStayUnread(t *testing.T) {
	lines := []struct{ name, raw string }{
		{"goroutine header alone", "goroutine 1 [running]:\nmain.main()\n\t/srv/billing/main.go:9 +0x2d"},
		{"goroutine header, Go 1.23 fields", "goroutine 7 gp=0xc000007c40 m=0 mp=0x6a7f20 [select]:\nmain.worker()"},
		{"SIGQUIT dump", "SIGQUIT: quit\nPC=0x46f4c1 m=0 sigcode=0\n\ngoroutine 0 gp=0x6a8100 m=0 mp=0x6a7f20 [idle]:"},
		{"web framework prose", "Internal Server Error: /admin/shop/item/42/change/"},
		{"opener on the second line", "Internal Server Error: /admin/shop/item/42/change/\nTraceback (most recent call last):"},
		{"indented opener", "  Traceback (most recent call last):"},
		{"opener after a word", "worker: Traceback (most recent call last):"},
		{"Java opener after a word", "Caught Exception in thread \"main\" java.lang.IllegalStateException"},
		{"Java opener without the quoted name", "Exception in thread main java.lang.IllegalStateException"},
		{"Java opener cut off after the name", "Exception in thread \"main\""},
		{"Java thread name across lines", "Exception in thread \"main\n\" java.lang.IllegalStateException"},
		{"plural", "fatal errors: 3"},
		{"title case", "Fatal error: x"},
		{".NET unhandled exception", "Unhandled exception. System.InvalidOperationException: ledger closed"},
		{"Node.js error", "TypeError: Cannot read properties of undefined (reading 'amount')"},
		{"Ruby error", "/srv/billing/app.rb:3:in 'post': ledger closed (RuntimeError)"},
	}
	for _, tt := range lines {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(model.LogLine{Raw: tt.raw}).Level; got != "" {
				t.Errorf("Level = %q, want none", got)
			}
		})
	}
}

// TestTraceOpenersAreTraceStarts keeps one source of truth. An opener that carries a level must
// also open a block in the coalescer, or its level would land on a lone first line while the
// trace's frames became events of their own. Each opener's pattern extends one of traceStartRe's
// alternatives literally (the whole pattern when the alternative is anchored, the pattern after
// its "^" when it is not), so whatever matches the opener matches traceStartRe. Each example
// matches both, and each regexp starts with its prefix, literally, so the fast check can never
// reject a line the regexp would take.
func TestTraceOpenersAreTraceStarts(t *testing.T) {
	examples := map[string]string{
		"ERROR:Traceback": "Traceback (most recent call last):",
		"ERROR:Exception": `Exception in thread "main" java.lang.IllegalStateException: ledger closed`,
		"FATAL:fatal":     "fatal error: concurrent map writes",
	}
	if len(traceOpeners) != len(examples) {
		t.Fatalf("%d openers, want exactly %d: Python's, Java's and Go's", len(traceOpeners), len(examples))
	}
	alts := strings.Split(traceStartRe.String(), "|") // traceStartRe is a strings.Join on "|"
	for _, o := range traceOpeners {
		src := o.re.String()
		if !strings.HasPrefix(src, "^") {
			t.Errorf("%s is not anchored at the start of the event", src)
		}
		extends := false
		for _, alt := range alts {
			if strings.HasPrefix(alt, "^") && strings.HasPrefix(src, alt) ||
				!strings.HasPrefix(alt, "^") && strings.HasPrefix(strings.TrimPrefix(src, "^"), alt) {
				extends = true
			}
		}
		if !extends {
			t.Errorf("%s extends no traceStartRe alternative %q", src, alts)
		}
		if !strings.HasPrefix(src, "^"+regexp.QuoteMeta(o.prefix)) {
			t.Errorf("prefix %q is not where %s starts", o.prefix, src)
		}
		key, _, _ := strings.Cut(o.prefix, " ")
		ex, ok := examples[o.level+":"+key]
		if !ok {
			t.Errorf("no example for the %s opener %s", o.level, src)
			continue
		}
		if !o.re.MatchString(ex) || !traceStartRe.MatchString(ex) {
			t.Errorf("%q: opener %v, traceStartRe %v; want both", ex, o.re.MatchString(ex), traceStartRe.MatchString(ex))
		}
	}
}

// chainedTraceback is an invented Django 500: the framework's own line, then a chained
// Python traceback, as a service writes it to stderr.
var chainedTraceback = []string{
	"Internal Server Error: /admin/shop/item/42/change/",
	"Traceback (most recent call last):",
	`  File "/app/venv/lib/python3.12/site-packages/django/core/handlers/exception.py", line 55, in inner`,
	"    response = get_response(request)",
	`  File "/app/shop/admin.py", line 31, in get_form`,
	"    return super().get_form(request, obj, **kwargs)",
	"KeyError: 'price'",
	"",
	"During handling of the above exception, another exception occurred:",
	"",
	"Traceback (most recent call last):",
	`  File "/app/venv/lib/python3.12/site-packages/django/core/handlers/exception.py", line 55, in inner`,
	"    response = get_response(request)",
	"django.core.exceptions.FieldError: Unknown field(s) (price) specified for Item",
}

// TestChainedTracebackRaisesOneCardPerTraceback is #170 end to end: the traceback above on a
// subprocess's stderr, grouped, then scored with warmup off. The chained traceback is a new
// ERROR on stderr, 0.45 + 0.90 = 1.35, and escalates; the framework's line scores 0.75 and
// stays quiet. Before, every event was levelless and nothing escalated.
//
// The chain is one event (#171), so it is two events and one card. Before #171 the chain split
// at its separator: five events, and two cards, one per traceback block.
func TestChainedTracebackRaisesOneCardPerTraceback(t *testing.T) {
	events := drive(t, 200*time.Millisecond, chainedTraceback...)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (the chain is one event, #171):\n%s", len(events), dump(events))
	}

	p := New(score.New(scoringConfig(), nil))
	base := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	var cards []Event
	for i, ln := range events {
		if ev := p.Process(ln, base.Add(time.Duration(i)*time.Millisecond)); ev.Escalate {
			cards = append(cards, ev)
		}
	}
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1, the whole chain (#171):\n%s", len(cards), dump(events))
	}
	for i, ev := range cards {
		if !strings.HasPrefix(ev.Line.Raw, "Traceback (most recent call last):") {
			t.Errorf("card %d is %q, want a traceback block", i, ev.Line.Raw)
		}
		if ev.Line.Level != "ERROR" {
			t.Errorf("card %d: Level = %q, want ERROR", i, ev.Line.Level)
		}
		if math.Abs(ev.Score-1.35) > 1e-9 {
			t.Errorf("card %d: score = %.2f, want 1.35 (reasons %v)", i, ev.Score, ev.Reasons)
		}
	}
}

// BenchmarkNormalizePlainLevelless measures the path every plain line without a level takes:
// an access log line and a prose line, the bulk of a busy stream. The trace-opener check runs
// on exactly these lines.
func BenchmarkNormalizePlainLevelless(b *testing.B) {
	lines := []struct{ name, raw string }{
		{"access-log", `198.51.100.7 - - [13/Jul/2026:10:00:00 +0000] "GET /api/orders/4242 HTTP/1.1" 200 512 "-" "curl/8.5.0"`},
		{"prose", "order 4242 placed by user 17 in 12ms"},
	}
	for _, l := range lines {
		b.Run(l.name, func(b *testing.B) {
			line := model.LogLine{Raw: l.raw}
			b.ReportAllocs()
			for b.Loop() {
				_ = Normalize(line)
			}
		})
	}
}
