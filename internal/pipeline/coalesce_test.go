// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// 500ms rather than something snappier because the two sends below must BOTH land
	// inside this window: a stall between them flushes the header on its own and the test
	// fails on missing content. Under -race, or on a loaded CI runner, tens of milliseconds
	// of scheduling delay is ordinary. The 2s deadline does not protect against that
	// direction — it only catches a flush that never happens — so the margin has to be here.
	go Coalesce(ctx, in, out, 500*time.Millisecond)

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

// flushOrder feeds one header per source into a coalescer, then either closes the input
// (atEOF) or leaves it open for the idle timer, and returns the sources in the order their
// lines came out.
func flushOrder(t *testing.T, sources []string, atEOF bool) string {
	t.Helper()
	in := make(chan model.LogLine, len(sources))
	out := make(chan model.LogLine, len(sources))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, s := range sources {
		in <- model.LogLine{Source: s, Raw: "header from " + s}
	}
	timeout := time.Hour
	if !atEOF {
		timeout = 20 * time.Millisecond
	} else {
		close(in)
	}
	go Coalesce(ctx, in, out, timeout)

	var got []string
	deadline := time.After(2 * time.Second)
	for len(got) < len(sources) {
		select {
		case ln := <-out:
			got = append(got, ln.Source)
		case <-deadline:
			t.Fatalf("only %d of %d buffered lines were flushed: %v", len(got), len(sources), got)
		}
	}
	return strings.Join(got, ",")
}

// TestCoalesceFlushOrderIsArrivalOrder: when several streams are holding a line and they
// are flushed together — at end of input, or by one firing of the idle timer — the lines
// leave in the order their headers arrived.
//
// Both paths used to range over the buffer map, and Go randomises map order: four buffered
// streams flushed at end of input came out in four different orders over 200 runs. That is
// a live defect, not a curiosity. journald tags one source per unit and Docker one per
// container, so every multi-source run holds many keys, and the order lines reach the
// pipeline is the order the stream pane shows, --plain prints, and the scorer's context
// ring records. It must not depend on the hash seed.
func TestCoalesceFlushOrderIsArrivalOrder(t *testing.T) {
	sources := []string{"journald:a", "journald:b", "journald:c", "journald:d", "journald:e"}
	want := strings.Join(sources, ",")

	t.Run("end of input", func(t *testing.T) {
		for range 200 {
			if got := flushOrder(t, sources, true); got != want {
				t.Fatalf("end-of-input flush order = %s, want arrival order %s", got, want)
			}
		}
	})
	// The timer path can only disagree when several buffers expire in one firing, which
	// depends on scheduling; enough runs make that the common case rather than a rare one.
	t.Run("idle timer", func(t *testing.T) {
		for range 50 {
			if got := flushOrder(t, sources, false); got != want {
				t.Fatalf("idle-timer flush order = %s, want arrival order %s", got, want)
			}
		}
	})
}

// TestIssue89SourcePositionLines is #89: a log line that merely CITES a Go source position
// is not a stack frame. slog writes source=file.go:N, logrus file="file.go:N", and the
// standard library's log with Lshortfile a file.go:N: prefix. The frame cue used to match
// any file.go:N anywhere on a line, so each such line arriving inside the group timeout was
// folded into the line before it, and an ERROR straight after an INFO vanished into the
// INFO event, its level and novelty gone with it.
//
// Each pair arrives 50ms apart on one stream, well inside the default 200ms timeout, and is
// driven through both coalescers: the replay one on the lines' own clock, and the live one.
func TestIssue89SourcePositionLines(t *testing.T) {
	tests := []struct {
		name          string
		first, second string
		levels        [2]string
	}{
		{"slog",
			`time=2026-07-13T12:00:00.000+02:00 level=INFO source=server.go:99 msg="listening"`,
			`time=2026-07-13T12:00:00.050+02:00 level=ERROR source=worker.go:88 msg="worker exited"`,
			[2]string{"INFO", "ERROR"}},
		{"logrus",
			`time="2026-07-13T12:00:00+02:00" level=info msg="starting" func=main.main file="main.go:42"`,
			`time="2026-07-13T12:00:00+02:00" level=error msg="db unreachable" func=main.connect file="db.go:17"`,
			[2]string{"INFO", "ERROR"}},
		// A level after a prefix is #31's, so neither line gets one: the split is what this
		// row pins.
		{"log Lshortfile",
			"2026/07/13 12:00:00 main.go:42: starting",
			"2026/07/13 12:00:00 db.go:17: ERROR: db unreachable",
			[2]string{"", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := map[string]func() []model.LogLine{
				"source clock": func() []model.LogLine {
					return driveAt(t, 200*time.Millisecond,
						at(0, "journald:app", model.Stdout, tt.first),
						at(50*time.Millisecond, "journald:app", model.Stdout, tt.second))
				},
				"wall clock": func() []model.LogLine { return drive(t, 200*time.Millisecond, tt.first, tt.second) },
			}
			for _, path := range []string{"source clock", "wall clock"} {
				t.Run(path, func(t *testing.T) {
					got := paths[path]()
					if len(got) != 2 {
						t.Fatalf("got %d events, want 2 (a cited source position was read as a frame):\n%s", len(got), dump(got))
					}
					for i, ev := range got {
						if strings.Contains(ev.Raw, "\n") {
							t.Errorf("event %d folded several lines: %q", i, ev.Raw)
						}
						if lvl := Normalize(ev).Level; lvl != tt.levels[i] {
							t.Errorf("event %d level = %q, want %q", i, lvl, tt.levels[i])
						}
					}
				})
			}
		})
	}
}

// TestGoFrameLineShape pins what the Go frame cue is: a line holding nothing but a source
// position — a path ending in file.go:N, the PC offset " +0x…" when the runtime prints one,
// and the "fp= sp= pc=" that GOTRACEBACK=system appends. Such a line continues the event
// before it at any indentation and without an active block, because a producer or shipper
// that strips indentation still leaves this shape, and nothing but a stack frame has it.
// A line that carries a position among other text is a log line.
func TestGoFrameLineShape(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"/build/app/main.go:15 +0x106", true},
		{"/build/app/main.go:9", true}, // an inlined frame prints no PC offset
		{"/go/pkg/mod/example.com/lib@v1.2.3/pool.go:40 +0x55", true},
		{"example.com/app/cmd/app/main.go:21 +0x2f", true}, // -trimpath
		{"C:/build/app/main.go:15 +0x106", true},
		{"/build/app/main.go:15 +0x106 fp=0xc000067f48 sp=0xc000067ef8 pc=0x484f46", true},

		{`time=2026-07-13T12:00:00.000+02:00 level=INFO source=server.go:99 msg="listening"`, false},
		{`time="2026-07-13T12:00:00+02:00" level=info msg="starting" file="main.go:42"`, false},
		{"2026/07/13 12:00:00 main.go:42: starting", false},
		{"main.go:42: starting", false},
		{"./main.go:12:5: undefined: foo", false},
		{"source=server.go:99", false},
		{"see main.go:42 for details", false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := isContinuation(&pending{}, tt.raw); got != tt.want {
				t.Errorf("isContinuation(idle header, %q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// goroutineDump is a real Go 1.26 panic under GOTRACEBACK=all, with the build path
// replaced: two goroutines, an inlined frame with no PC offset, and a "created by" line.
var goroutineDump = []string{
	"panic: runtime error: index out of range [3] with length 3",
	"",
	"goroutine 1 [running]:",
	"main.lookup(...)",
	"\t/build/app/main.go:9",
	"main.main()",
	"\t/build/app/main.go:15 +0x106",
	"",
	"goroutine 18 [chan receive]:",
	"main.(*Pool).worker(...)",
	"\t/build/app/main.go:7",
	"created by main.main in goroutine 1",
	"\t/build/app/main.go:13 +0x98",
}

// TestCoalesceGoFrameLines: anchoring the frame cue (#89) leaves real Go frames folding as
// they did, and is what keeps a log line out of a trace.
func TestCoalesceGoFrameLines(t *testing.T) {
	one := func(t *testing.T, lines []string) model.LogLine {
		t.Helper()
		got := drive(t, time.Second, lines...)
		if len(got) != 1 {
			t.Fatalf("got %d events, want 1:\n%s", len(got), dump(got))
		}
		if n := strings.Count(got[0].Raw, "\n") + 1; n != len(lines) {
			t.Errorf("folded event holds %d physical lines, want %d:\n%q", n, len(lines), got[0].Raw)
		}
		return got[0]
	}

	t.Run("goroutine dump, indented", func(t *testing.T) { one(t, goroutineDump) })
	// With the indentation gone the file lines fold on the frame cue alone.
	t.Run("goroutine dump, indentation stripped", func(t *testing.T) { one(t, stripped(goroutineDump)) })

	// The cue is not gated on an active block: a stripped file line continues the call
	// line before it even with no panic header above.
	t.Run("stripped frame after an idle header", func(t *testing.T) {
		one(t, []string{"main.main()", "/build/app/main.go:15 +0x106"})
	})

	// Gating the old unanchored cue would not have fixed #89: any fold makes a block
	// active, so a log line citing a position would still join the trace before it.
	t.Run("log line after a dump", func(t *testing.T) {
		slog := `time=2026-07-13T12:00:00.000+02:00 level=ERROR source=main.go:31 msg="restarting"`
		got := drive(t, time.Second, append(append([]string{}, goroutineDump...), slog)...)
		if len(got) != 2 || got[1].Raw != slog {
			t.Fatalf("got %d events, want the dump then the log line on its own:\n%s", len(got), dump(got))
		}
	})

	// #83's line-break guard stays: a logfmt line with a folded continuation is several
	// physical lines, and keeps the text path's template and level.
	t.Run("logfmt line with a folded frame", func(t *testing.T) {
		ev := one(t, []string{
			`time=2026-07-13T12:00:00.000+02:00 level=ERROR msg="worker exited"`,
			"\t/build/app/worker.go:88 +0x2c",
		})
		line := Normalize(ev)
		gotPattern, gotHash := TemplatizeLine(line)
		wantLevel, msg := normalizePlain(ev.Raw)
		wantPattern, wantHash := Templatize(msg)
		if gotPattern != wantPattern || gotHash != wantHash {
			t.Errorf("grouped event left the text path: %q/%s, want %q/%s", gotPattern, gotHash, wantPattern, wantHash)
		}
		if line.Level != wantLevel {
			t.Errorf("Level = %q, want the text path's %q", line.Level, wantLevel)
		}
	})
}

// stripped returns lines with their leading indentation removed, as a producer or shipper
// that strips it would deliver them.
func stripped(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimLeft(l, " \t")
	}
	return out
}

// godump reads a Go runtime dump from testdata/godump, one string per physical line. Each
// file is the stderr of a real Go 1.26 binary (an index-out-of-range panic beside one parked
// worker goroutine, or that worker and a SIGQUIT the program sends itself, or debug.Stack()
// written on its own), with the build and GOROOT paths and every address replaced.
func godump(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "godump", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// heads renders each event's first line, for a failure message about a dump that fragmented
// into dozens of events.
func heads(events []model.LogLine) string {
	var b strings.Builder
	for i, ev := range events {
		first, _, _ := strings.Cut(ev.Raw, "\n")
		fmt.Fprintf(&b, "  [%d] %d lines, %q\n", i, strings.Count(ev.Raw, "\n")+1, first)
	}
	return b.String()
}

// TestGoDumpsFoldIntoOneEvent is #94: every dump the Go runtime writes folds into one event,
// with its indentation and with it stripped. Since Go 1.23 GOTRACEBACK=system and crash, and
// every SIGQUIT dump, write each goroutine header as "goroutine 1 gp=0x… m=0 mp=0x… [running]:".
// A SIGQUIT dump adds a "PC=… m=… sigcode=…" line and a register dump, a stripped dump has
// assembly frames ("asm_amd64.s:1771 +0x1"), and debug.Stack() begins at the goroutine header.
// The coalescer recognized none of these, so each such dump arrived as many events.
func TestGoDumpsFoldIntoOneEvent(t *testing.T) {
	dumps := []struct {
		name  string
		lines func(*testing.T) []string
	}{
		{"panic, GOTRACEBACK=all", func(*testing.T) []string { return goroutineDump }},
		{"panic, GOTRACEBACK=system", func(t *testing.T) []string { return godump(t, "system") }},
		// crash writes what system does: the SIGABRT it raises after the dump goes to the
		// default handler, which prints nothing.
		{"panic, GOTRACEBACK=crash", func(t *testing.T) []string { return godump(t, "crash") }},
		{"SIGQUIT", func(t *testing.T) []string { return godump(t, "sigquit") }},
		{"debug.Stack() written alone", func(t *testing.T) []string { return godump(t, "stack") }},
	}
	for _, d := range dumps {
		t.Run(d.name, func(t *testing.T) {
			for _, form := range []string{"tabbed", "tabs stripped"} {
				t.Run(form, func(t *testing.T) {
					lines := d.lines(t)
					if form == "tabs stripped" {
						lines = stripped(lines)
					}
					got := drive(t, time.Second, lines...)
					if len(got) != 1 {
						t.Fatalf("%d lines arrived as %d events, want 1 (the dump fragmented):\n%s", len(lines), len(got), heads(got))
					}
					if got[0].Raw != strings.Join(lines, "\n") {
						t.Errorf("the folded event is not the dump, line for line:\n%s", heads(got))
					}
				})
			}
		})
	}

	// The dump a signal writes starts a new event: two dumps back to back stay two.
	t.Run("system panic, then a SIGQUIT dump", func(t *testing.T) {
		for _, form := range []string{"tabbed", "tabs stripped"} {
			t.Run(form, func(t *testing.T) {
				first, second := godump(t, "system"), godump(t, "sigquit")
				if form == "tabs stripped" {
					first, second = stripped(first), stripped(second)
				}
				got := drive(t, time.Second, slices.Concat(first, second)...)
				if len(got) != 2 || got[0].Raw != strings.Join(first, "\n") || got[1].Raw != strings.Join(second, "\n") {
					t.Fatalf("got %d events, want the panic dump then the SIGQUIT dump:\n%s", len(got), heads(got))
				}
			})
		}
	})
}

// TestGoDumpCueShapes pins the Go dump cues line by line, on an idle header and inside an
// active block. The goroutine header, an assembly frame and the signal PC line continue an
// event at any time; a register line only once a block is in progress.
func TestGoDumpCueShapes(t *testing.T) {
	tests := []struct {
		raw          string
		idle, active bool
	}{
		{"goroutine 1 gp=0xc000002380 m=0 mp=0x5a1b20 [running]:", true, true},
		{"goroutine 2 gp=0xc000002e00 m=nil [force gc (idle)]:", true, true},
		{"goroutine 7 [chan receive, 2 minutes, locked to thread]:", true, true},
		{"/usr/local/go/src/runtime/asm_amd64.s:1771 +0x1 fp=0xc000067fe8 sp=0xc000067fe0 pc=0x47da81", true, true},
		{"/usr/local/go/src/runtime/asm_amd64.s:1771 +0x1", true, true},
		{"PC=0x46f4c1 m=0 sigcode=0", true, true},
		{"PC=0x46f4c1 m=3 sigcode=1 addr=0x0", true, true},
		{"rax    0xca", false, true},
		{"r8     0x0", false, true},
		{"rflags 0x212", false, true},

		{"goroutine 5 started", false, false},
		{"goroutine 5 [worker] started", false, false},
		{"received SIGQUIT, shutting down", false, false},
		{"SIGQUIT: quit", false, false},
		{"SIGQUIT: quit requested", false, false},
		{"/src/boot.s:12", false, false},
		{"nasm failed at /src/boot.s:12", false, false},
		{"assembling /src/boot.s:12", false, false},
		{"PC=0x46f4c1", false, false},
		{"rax    0xca and more", false, false},
		{"cache: 0x1f", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := isContinuation(&pending{}, tt.raw); got != tt.idle {
				t.Errorf("isContinuation(idle header, %q) = %v, want %v", tt.raw, got, tt.idle)
			}
			if got := isContinuation(&pending{active: true}, tt.raw); got != tt.active {
				t.Errorf("isContinuation(active block, %q) = %v, want %v", tt.raw, got, tt.active)
			}
		})
	}
}

// TestNotPartOfAGoDump is the other half of #94: a line that is not part of a Go dump stays its
// own event, after a finished dump or on its own.
func TestNotPartOfAGoDump(t *testing.T) {
	slog := `time=2026-07-13T12:00:00.000+02:00 level=ERROR source=main.go:31 msg="restarting"`
	tests := []struct {
		name  string
		lines []string
		want  []string // the events, each one physical line except a whole dump
	}{
		{"goroutine prose after a dump",
			slices.Concat(goroutineDump, []string{"goroutine 5 started"}),
			[]string{strings.Join(goroutineDump, "\n"), "goroutine 5 started"}},
		{"goroutine prose opens no block",
			[]string{"goroutine 5 started", "retry(3) scheduled"},
			[]string{"goroutine 5 started", "retry(3) scheduled"}},
		{"bracketed goroutine prose after a dump",
			slices.Concat(goroutineDump, []string{"goroutine 5 [worker] started"}),
			[]string{strings.Join(goroutineDump, "\n"), "goroutine 5 [worker] started"}},
		{"SIGQUIT in prose after a dump",
			slices.Concat(goroutineDump, []string{"received SIGQUIT, shutting down"}),
			[]string{strings.Join(goroutineDump, "\n"), "received SIGQUIT, shutting down"}},
		{"SIGQUIT in prose opens no block",
			[]string{"SIGQUIT: quit requested", "shutdown(ctx) begun"},
			[]string{"SIGQUIT: quit requested", "shutdown(ctx) begun"}},
		{"a bare .s position after a call line",
			[]string{"main.main()", "/src/boot.s:12"},
			[]string{"main.main()", "/src/boot.s:12"}},
		{"a line ending in .s:N after a dump",
			slices.Concat(goroutineDump, []string{"nasm failed at /src/boot.s:12"}),
			[]string{strings.Join(goroutineDump, "\n"), "nasm failed at /src/boot.s:12"}},
		{"a .s position in prose after a dump",
			slices.Concat(goroutineDump, []string{"assembling /src/boot.s:12"}),
			[]string{strings.Join(goroutineDump, "\n"), "assembling /src/boot.s:12"}},
		{"two panics back to back",
			slices.Concat(goroutineDump, goroutineDump),
			[]string{strings.Join(goroutineDump, "\n"), strings.Join(goroutineDump, "\n")}},
		{"two panics back to back, tabs stripped",
			slices.Concat(stripped(goroutineDump), stripped(goroutineDump)),
			[]string{strings.Join(stripped(goroutineDump), "\n"), strings.Join(stripped(goroutineDump), "\n")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := drive(t, time.Second, tt.lines...)
			raws := make([]string, len(got))
			for i, ev := range got {
				raws[i] = ev.Raw
			}
			if !slices.Equal(raws, tt.want) {
				t.Fatalf("got %d events, want %d:\n%s", len(got), len(tt.want), heads(got))
			}
		})
	}

	// A log line right after each of the new dumps stays its own event. The dump in front of it
	// is not what this checks (TestGoDumpsFoldIntoOneEvent is), so only the last event is.
	for _, name := range []string{"system", "crash", "sigquit", "stack"} {
		for _, line := range []string{"ERROR: db connection failed", slog} {
			for _, form := range []string{"tabbed", "tabs stripped"} {
				t.Run(fmt.Sprintf("%s after the %s dump, %s", line, name, form), func(t *testing.T) {
					lines := godump(t, name)
					if form == "tabs stripped" {
						lines = stripped(lines)
					}
					got := drive(t, time.Second, append(lines, line)...)
					if last := got[len(got)-1].Raw; last != line {
						t.Fatalf("the last event is not the log line on its own:\n%s", heads(got))
					}
				})
			}
		}
	}
}
