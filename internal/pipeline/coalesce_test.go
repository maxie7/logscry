// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// drive feeds raws (one LogLine each, same source/stream) into a coalescer, closes the
// input so every buffered event is flushed, and returns the coalesced lines. It fails
// the test if the coalescer does not close its output within a generous deadline.
func drive(t *testing.T, timeout time.Duration, raws ...string) []model.LogLine {
	t.Helper()
	in := make(chan model.LogLine, len(raws))
	for _, r := range raws {
		in <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: r}
	}
	close(in)

	out := make(chan model.LogLine, len(raws)+1)
	go Coalesce(context.Background(), in, out, timeout)

	var got []model.LogLine
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ln, ok := <-out:
			if !ok {
				return got
			}
			got = append(got, ln)
		case <-deadline:
			t.Fatalf("coalescer did not close its output in time; got %d events so far:\n%s", len(got), dump(got))
		}
	}
}

// dump renders coalesced events for a failure message, one indented block per event.
func dump(events []model.LogLine) string {
	var b strings.Builder
	for i, ev := range events {
		fmt.Fprintf(&b, "  [%d] %q\n", i, ev.Raw)
	}
	return b.String()
}

// TestCoalesceCollapsesTraces is the core requirement: a Python traceback, a Java
// traceback, and Go goroutine/panic dumps each fold into ONE logical event, so the
// scorer sees a single template instead of one per frame.
func TestCoalesceCollapsesTraces(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
	}{
		{"python", []string{
			"Traceback (most recent call last):",
			`  File "/app/main.py", line 10, in <module>`,
			"    result = divide(1, 0)",
			`  File "/app/calc.py", line 3, in divide`,
			"    return a / b",
			"ZeroDivisionError: division by zero",
		}},
		{"java", []string{
			`Exception in thread "main" java.lang.RuntimeException: boom`,
			"\tat com.foo.App.main(App.java:10)",
			"Caused by: java.lang.NullPointerException: null",
			"\tat com.foo.App.helper(App.java:20)",
			"\t... 3 more",
		}},
		{"go-panic", []string{
			"panic: something bad happened",
			"",
			"goroutine 1 [running]:",
			"main.doStuff(...)",
			"\t/app/main.go:42 +0x1d",
			"main.main()",
			"\t/app/main.go:12 +0x25",
		}},
		{"go-nil-pointer", []string{
			// A SIGSEGV panic wedges a "[signal ...]" line between the panic header
			// and the goroutine dump; without a marker for it the trace fragments.
			"panic: runtime error: invalid memory address or nil pointer dereference",
			"[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x45a2f1]",
			"",
			"goroutine 6 [running]:",
			"main.(*Server).handle(0x0)",
			"\t/app/server.go:88 +0x2c",
			"created by main.main",
			"\t/app/main.go:19 +0x64",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := drive(t, time.Second, tt.lines...)
			if len(got) != 1 {
				t.Fatalf("got %d events, want 1 (the trace fragmented):\n%s", len(got), dump(got))
			}
			if lines := strings.Count(got[0].Raw, "\n") + 1; lines != len(tt.lines) {
				t.Errorf("folded event holds %d physical lines, want %d:\n%q", lines, len(tt.lines), got[0].Raw)
			}
		})
	}
}

// TestCoalesceSameShapeCollapsesToOneTemplate closes the loop with dedup: two
// tracebacks that differ only in line numbers must template to the SAME signature —
// which is the whole point of grouping (~40 "novel" frames become one recurring event).
func TestCoalesceSameShapeCollapsesToOneTemplate(t *testing.T) {
	trace := func(a, b int) []string {
		return []string{
			"Traceback (most recent call last):",
			fmt.Sprintf(`  File "/app/main.py", line %d, in <module>`, a),
			"    do_thing()",
			fmt.Sprintf(`  File "/app/calc.py", line %d, in divide`, b),
			"    return a / b",
			"ZeroDivisionError: division by zero",
		}
	}
	lines := append(trace(10, 3), trace(87, 42)...)
	got := drive(t, time.Second, lines...)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 tracebacks:\n%s", len(got), dump(got))
	}
	_, h1 := Templatize(Normalize(got[0]).Message)
	_, h2 := Templatize(Normalize(got[1]).Message)
	if h1 != h2 {
		t.Errorf("two tracebacks differing only in line numbers got different templates:\n%q\n%q",
			got[0].Raw, got[1].Raw)
	}
}

// TestCoalesceKeepsUnrelatedLinesSeparate is the negative / conservatism test: a burst
// of unrelated single-line logs must stay N separate events. Prefer under-grouping.
func TestCoalesceKeepsUnrelatedLinesSeparate(t *testing.T) {
	lines := []string{
		"ERROR: db connection failed",
		"WARN: slow query took 1200ms",
		"request 42 completed in 12ms",
		"user login ok for alice",
		"cache miss for key foo",
		"ERROR: db connection failed again",
	}
	got := drive(t, time.Second, lines...)
	if len(got) != len(lines) {
		t.Fatalf("got %d events, want %d (unrelated lines were coalesced):\n%s", len(got), len(lines), dump(got))
	}
	for i, ev := range got {
		if strings.Contains(ev.Raw, "\n") {
			t.Errorf("event %d folded multiple lines: %q", i, ev.Raw)
		}
	}
}

// TestCoalesceFlushesOnNextHeader: a buffered line is emitted the instant the next
// header for its stream arrives, so a busy stream never waits on the idle timer.
func TestCoalesceFlushesOnNextHeader(t *testing.T) {
	got := drive(t, time.Second,
		"ERROR: first thing failed",
		"ERROR: second thing failed",
	)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2:\n%s", len(got), dump(got))
	}
	if got[0].Raw != "ERROR: first thing failed" {
		t.Errorf("first event = %q, want the first header flushed when the second arrived", got[0].Raw)
	}
}

// TestCoalesceFlushesPartialAfterIdleTimeout: a trailing partial multi-line event must
// be emitted after the idle timeout, not held forever — the bounded-latency guarantee.
func TestCoalesceFlushesPartialAfterIdleTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan model.LogLine)
	out := make(chan model.LogLine, 1)
	go Coalesce(ctx, in, out, 50*time.Millisecond)

	// A header plus one continuation, then silence — the input is never closed.
	in <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "Traceback (most recent call last):"}
	in <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: `  File "/app/x.py", line 1, in <module>`}

	select {
	case ln := <-out:
		if !strings.Contains(ln.Raw, "Traceback") || !strings.Contains(ln.Raw, "File") {
			t.Errorf("flushed event missing content: %q", ln.Raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("partial multi-line event was not flushed after the idle timeout")
	}
}
