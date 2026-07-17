// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// Coalescing folds a multi-line event — a stack trace, a goroutine dump, indented
// output — into ONE logical LogLine before templating, so a traceback becomes a single
// template instead of one "novel" template per frame (the M4 noise this suppresses).
//
// The heuristic is deliberately conservative and language-agnostic: a header line
// starts a logical event and continuation lines attach to it. When in doubt a line is
// its own event, so a high-rate stream of UNRELATED single-line logs is never grouped.

const (
	// maxCoalesceLines caps how many physical lines fold into one logical event, and
	// maxCoalesceBytes caps its size. A runaway indented stream (a pathological
	// producer, or a genuinely enormous dump) is flushed at these bounds rather than
	// buffered without limit — memory stays bounded, and, per the burst lesson, the
	// bias is toward emitting rather than swallowing.
	maxCoalesceLines = 200
	maxCoalesceBytes = 64 << 10
)

// continuationRe matches stack-frame / dump markers at any indentation. These are the
// explicit language cues that attach a line to the event in progress even when it is
// not indented. Kept broad across Python, Java, and Go rather than tied to one format.
var continuationRe = regexp.MustCompile(strings.Join([]string{
	`^\s*File ".*", line \d+`,   // Python:  File "app.py", line 10, in <module>
	`^\s*at\s+[\w$.]+\(`,        // Java:    at com.foo.Bar.baz(Bar.java:1)
	`^\s*Caused by:`,            // Java:    Caused by: ...
	`^\s*Suppressed:`,           // Java:    Suppressed: ...
	`^\s*\.\.\.\s+\d+\s+more\b`, // Java:    ... 3 more
	`^goroutine \d+ \[`,         // Go:      goroutine 1 [running]:
	`^\s*created by \S`,         // Go:      created by main.main
	`^\s*\[signal\b`,            // Go:      [signal SIGSEGV: segmentation violation ...]
	`\.go:\d+`,                  // Go:      /app/main.go:42 +0x1d
}, "|"))

// traceStartRe recognizes a header that itself opens a multi-line block, so the lines
// after it are kept even before the first indented continuation confirms the block —
// notably Go's "[signal ...]" and blank line between "panic:" and the goroutine dump.
var traceStartRe = regexp.MustCompile(strings.Join([]string{
	`^Traceback \(most recent call last\):`, // Python
	`^\s*panic:`,                            // Go
	`^fatal error:`,                         // Go runtime
	`Exception in thread `,                  // Java
}, "|"))

// excSummaryRe matches an un-indented exception summary — the trailing
// "ZeroDivisionError: ..." of a Python trace, or a fully-qualified
// "java.lang.NullPointerException: ...". It is consulted ONLY once a block is already
// active (see isContinuation), so an ordinary "INFO:" / "GET:" log line, which lacks
// the exception-type shape, is never mistaken for a trace tail.
var excSummaryRe = regexp.MustCompile(
	`^[\w$.]*(?:Error|Exception|Throwable|Fault|Failure|Warning|Interrupt|Timeout|Panic)\b` + // ValueError:, RuntimeException
		`|^[\w$]+(?:\.[\w$]+)+:`, // java.lang.NullPointerException:
)

// goFrameRe matches an un-indented Go call frame ("main.doStuff(...)"), also only
// consulted once a block is active.
var goFrameRe = regexp.MustCompile(`^[\w$./]+\(`)

// streamKey identifies the logical stream a line belongs to. Continuations only ever
// attach to a buffer of the SAME source and stream, so a stack trace on one container's
// stderr is never joined to interleaved lines from another source.
type streamKey struct {
	source string
	stream model.Stream
}

// pending is one logical line being assembled: the header LogLine, whose Raw
// accumulates any continuations, plus the state needed to classify the next line.
type pending struct {
	line   model.LogLine // header; Raw grows as continuations attach
	count  int           // physical lines folded in so far (>= 1)
	active bool          // a multi-line block is confirmed in progress
	last   time.Time     // when the last line attached — the idle-flush anchor
}

// Coalesce reads raw lines from in, folds continuation lines into the preceding
// logical line, and writes coalesced lines to out. It runs as a single goroutine: all
// buffer state is confined here, so the concurrency model needs no lock (RDI §3).
//
// A buffered logical line is flushed when the next header for its stream arrives, when
// timeout elapses since its last line (bounded latency — a partial event is never held
// forever), or when in closes. Sends to out give up on ctx cancellation, and in is
// always drained, so the coalescer never blocks the ingestion path.
func Coalesce(ctx context.Context, in <-chan model.LogLine, out chan<- model.LogLine, timeout time.Duration) {
	defer close(out)

	buffers := make(map[streamKey]*pending)

	// One timer, repointed at the earliest pending deadline. Go 1.23+ makes Stop/Reset
	// safe without draining the channel, so arm can reset freely.
	timer := time.NewTimer(timeout)
	timer.Stop()
	var timerC <-chan time.Time // nil while no buffer is waiting, which parks the select case

	arm := func(now time.Time) {
		var earliest time.Time
		found := false
		for _, p := range buffers {
			if d := p.last.Add(timeout); !found || d.Before(earliest) {
				earliest, found = d, true
			}
		}
		if !found {
			timer.Stop()
			timerC = nil
			return
		}
		wait := earliest.Sub(now)
		if wait < 0 {
			wait = 0
		}
		timer.Stop()
		timer.Reset(wait)
		timerC = timer.C
	}

	// emit sends p's merged line downstream, reporting false on cancellation so the
	// caller can stop rather than block on a consumer that has already gone.
	emit := func(p *pending) bool {
		select {
		case out <- p.line:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-timerC:
			now := time.Now()
			for key, p := range buffers {
				if now.Sub(p.last) >= timeout {
					if !emit(p) {
						return
					}
					delete(buffers, key)
				}
			}
			arm(now)
		case line, ok := <-in:
			if !ok {
				// Ingestion ended: flush everything still buffered, then let the
				// deferred close of out drive Run's normal shutdown.
				for _, p := range buffers {
					if !emit(p) {
						return
					}
				}
				return
			}
			now := time.Now()
			key := streamKey{source: line.Source, stream: line.Stream}
			if p := buffers[key]; p != nil && isContinuation(p, line.Raw) {
				p.line.Raw += "\n" + line.Raw
				p.count++
				p.active = true
				p.last = now
				if p.count >= maxCoalesceLines || len(p.line.Raw) >= maxCoalesceBytes {
					if !emit(p) {
						return
					}
					delete(buffers, key)
				}
				arm(now)
				continue
			}
			// A header: flush any predecessor for this stream, then hold this line as
			// the next potential header.
			if p := buffers[key]; p != nil {
				if !emit(p) {
					return
				}
			}
			buffers[key] = &pending{
				line:   line,
				count:  1,
				active: traceStartRe.MatchString(line.Raw),
				last:   now,
			}
			arm(now)
		}
	}
}

// isContinuation reports whether raw continues the logical line p rather than starting
// a new one. It combines signals, weakest gated behind an already-confirmed block:
//
//  1. Indentation — a leading space or tab with non-blank content. The primary
//     cross-language frame signal (Python code/File lines, Java "\tat", Go "\t/path").
//  2. An explicit frame marker at any indent (see continuationRe).
//  3. Only once p.active: a blank separator, an un-indented exception summary, or a Go
//     call frame. Gating these on an active block is what keeps ordinary prefix-less
//     log lines from being swallowed, so an unrelated burst stays one event per line.
func isContinuation(p *pending, raw string) bool {
	if len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t') && strings.TrimSpace(raw) != "" {
		return true
	}
	if continuationRe.MatchString(raw) {
		return true
	}
	if p.active {
		if strings.TrimSpace(raw) == "" {
			return true
		}
		if excSummaryRe.MatchString(raw) || goFrameRe.MatchString(raw) {
			return true
		}
	}
	return false
}
