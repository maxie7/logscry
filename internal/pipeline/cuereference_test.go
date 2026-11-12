// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"regexp"
	"strings"
	"testing"
)

// The coalescer's cue regexps as they were written before they were anchored as a whole, kept
// verbatim as the reference the cues must match exactly. Each was an alternation of "^" branches
// (traceStartRe's Java branch is unanchored), which Go's regexp does not treat as anchored, so a
// line that matched nothing was tried at every byte offset. A deliberate change to what a cue
// matches changes the cue and its reference together.
var (
	refContinuationRe = regexp.MustCompile(strings.Join([]string{
		`^\s*File ".*", line \d+`,
		`^\s*at\s+[\w$.]+\(`,
		`^\s*Caused by:`,
		`^\s*Suppressed:`,
		`^\s*\.\.\.\s+\d+\s+more\b`,
		goroutineHeader,
		`^\s*created by \S`,
		`^\s*\[signal\b`,
		`^\s*[^\s=]+\.go:\d+(?: \+0x[0-9a-f]+)?(?: fp=0x[0-9a-f]+ sp=0x[0-9a-f]+ pc=0x[0-9a-f]+)?\s*$`,
		`^\s*[^\s=]+\.s:\d+ \+0x[0-9a-f]+(?: fp=0x[0-9a-f]+ sp=0x[0-9a-f]+ pc=0x[0-9a-f]+)?\s*$`,
		`^PC=0x[0-9a-f]+ m=\d+ sigcode=\d+(?: addr=0x[0-9a-f]+)?\s*$`,
	}, "|"))

	refTraceStartRe = regexp.MustCompile(strings.Join([]string{
		`^Traceback \(most recent call last\):`,
		`^\s*panic:`,
		`^fatal error:`,
		goroutineHeader,
		`Exception in thread `,
	}, "|"))

	refExcSummaryRe = regexp.MustCompile(
		`^[\w$.]*(?:Error|Exception|Throwable|Fault|Failure|Warning|Interrupt|Timeout|Panic)\b` +
			`|^[\w$]+(?:\.[\w$]+)+:`,
	)
)

// cueAccessLog is an ordinary 200-byte line that no cue matches, the bulk of a busy stream.
const cueAccessLog = `198.51.100.7 - - [13/Jul/2026:10:00:00 +0000] "GET /api/orders/4242/items?page=3&size=50 HTTP/1.1" 200 5120 "https://shop.example.com/orders/4242" "Mozilla/5.0 (X11; Linux x86_64) curl/8.5.0" 0.012345`

// cueSeeds are invented lines: one that matches and one near miss for every branch, each cue
// after a word and after a line break (where "^" must not match), Java's opener at the start, in
// the middle and cut short, and lines no cue was written for.
var cueSeeds = []string{
	"", " ", "\t", "\n", cueAccessLog, "order 4242 placed by user 17 in 12ms", "\xff\xfe broken \x80",

	`  File "/srv/billing/app.py", line 12, in <module>`, `File "app.py", line x`, `x  File "app.py", line 3`,
	"\tat com.example.billing.Ledger.post(Ledger.java:42)", "at $Proxy12.invoke(Unknown Source)", "\tat nothing here",
	"Caused by: java.io.IOException: closed", "  Caused by", "Suppressed: java.lang.IllegalStateException", "\t... 3 more", "... more",
	"goroutine 1 [running]:", "goroutine 7 gp=0xc000007c40 m=0 mp=0x6a7f20 [select]:", "goroutine 5 started", "goroutine 5 [worker] started",
	"created by main.main in goroutine 1", "created by ", "[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x47c1a2]", "[signals]",
	"\t/srv/billing/main.go:42 +0x1d", "/srv/billing/main.go:42", "source=server.go:99", "main.go:42: listening", "\tmain.go:9 +0x2d fp=0xc000046f88 sp=0xc000046f68 pc=0x47c1a2",
	"\t/usr/local/go/src/runtime/asm_amd64.s:1771 +0x1", "boot.s:12", "PC=0x46f4c1 m=0 sigcode=0", "PC=0x46f4c1 m=0 sigcode=0 addr=0x10", "PC=0x46f4c1 m=0",

	"Traceback (most recent call last):", "  Traceback (most recent call last):", "worker: Traceback (most recent call last):",
	"panic: runtime error: index out of range [3] with length 2", "  panic: boom", "panicked: no", "fatal error: concurrent map writes", "Fatal error: x",
	`Exception in thread "main" java.lang.IllegalStateException: ledger closed`, `Caught Exception in thread "pool-1-thread-2"`, "Exception in thread", "Exception in thread ",

	"ZeroDivisionError: division by zero", "java.lang.NullPointerException: amount", "com.example.LedgerException", "Errors: 3", "INFO: worker 3 up", "GET: /api",
	"KeyboardInterrupt", "socket.timeout: timed out", "ok\n  File \"app.py\", line 3", "ok\nTraceback (most recent call last):", "ok\nValueError: x",
}

// FuzzCuesMatchTheirReference: every cue matches exactly the lines its reference matches.
func FuzzCuesMatchTheirReference(f *testing.F) {
	for _, s := range cueSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		cues := []struct {
			name     string
			ref, got bool
		}{
			{"continuationRe", refContinuationRe.MatchString(s), continuationRe.MatchString(s)},
			{"traceStartRe", refTraceStartRe.MatchString(s), traceStartRe.MatchString(s)},
			{"excSummaryRe", refExcSummaryRe.MatchString(s), excSummaryRe.MatchString(s)},
		}
		for _, c := range cues {
			if c.ref != c.got {
				t.Errorf("%s(%q) = %v, reference %v", c.name, s, c.got, c.ref)
			}
		}
	})
}

// BenchmarkCue measures each cue on an ordinary line, which matches nothing, and on a line it
// matches.
func BenchmarkCue(b *testing.B) {
	cues := []struct {
		name     string
		match    func(string) bool
		matching string
	}{
		{"continuation", continuationRe.MatchString, "\t/srv/billing/main.go:42 +0x1d"},
		{"traceStart", traceStartRe.MatchString, `Exception in thread "main" java.lang.IllegalStateException: ledger closed`},
		{"excSummary", excSummaryRe.MatchString, "java.lang.NullPointerException: amount"},
	}
	for _, c := range cues {
		for _, l := range []struct{ name, raw string }{{"ordinary", cueAccessLog}, {"matching", c.matching}} {
			b.Run(c.name+"/"+l.name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_ = c.match(l.raw)
				}
			})
		}
	}
}
