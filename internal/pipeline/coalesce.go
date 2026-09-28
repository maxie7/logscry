// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"cmp"
	"context"
	"regexp"
	"slices"
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
	seq    uint64        // arrival order of the header, which orders simultaneous flushes
}

// inOrder returns the buffered keys that pass keep, in the order they must be flushed:
// earliest idle deadline first, then header arrival. At end of input every deadline is
// irrelevant and arrival alone decides.
//
// It exists because the buffers live in a map, and ranging over a map to flush several of
// them at once hands the pipeline its lines in whatever order the runtime's hash seed
// picked — different on every run. journald keys one stream per unit and Docker one per
// container, so a multi-source run routinely holds many buffers, and the order they leave
// in is the order the stream pane shows, --plain prints, and the scorer's context ring
// records (#69).
func inOrder(buffers map[streamKey]*pending, byDeadline bool, keep func(*pending) bool) []streamKey {
	var keys []streamKey
	for k, p := range buffers {
		if keep(p) {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b streamKey) int {
		pa, pb := buffers[a], buffers[b]
		if byDeadline {
			if c := pa.last.Compare(pb.last); c != 0 {
				return c
			}
		}
		return cmp.Compare(pa.seq, pb.seq)
	})
	return keys
}

// grouper is the buffer state and the fold/flush rules both coalescers share. Only what
// drives it differs: Coalesce is clocked by the wall and a timer, CoalesceSourceTime by the
// lines' own timestamps. Owned by the one goroutine running either of them.
type grouper struct {
	ctx     context.Context
	out     chan<- model.LogLine
	timeout time.Duration
	buffers map[streamKey]*pending
	seq     uint64 // numbers headers as they arrive; see inOrder

	// restamp sets each emitted line's Time to the moment it is released. Only replay wants
	// it (see CoalesceSourceTime); a live line keeps the time its source gave it.
	restamp bool
}

func newGrouper(ctx context.Context, out chan<- model.LogLine, timeout time.Duration, restamp bool) *grouper {
	return &grouper{ctx: ctx, out: out, timeout: timeout, buffers: make(map[streamKey]*pending), restamp: restamp}
}

// emit sends p's merged line downstream at time at, reporting false on cancellation so the
// caller can stop rather than block on a consumer that has already gone.
func (g *grouper) emit(p *pending, at time.Time) bool {
	line := p.line
	if g.restamp {
		line.Time = at
	}
	select {
	case g.out <- line:
		return true
	case <-g.ctx.Done():
		return false
	}
}

// add folds line, observed at now, into the buffer for its stream — or, if it starts a new
// logical line, flushes that stream's predecessor and holds this one in its place.
func (g *grouper) add(line model.LogLine, now time.Time) bool {
	key := streamKey{source: line.Source, stream: line.Stream}
	if p := g.buffers[key]; p != nil && isContinuation(p, line.Raw) {
		p.line.Raw += "\n" + line.Raw
		p.count++
		p.active = true
		p.last = now
		if p.count >= maxCoalesceLines || len(p.line.Raw) >= maxCoalesceBytes {
			if !g.emit(p, now) {
				return false
			}
			delete(g.buffers, key)
		}
		return true
	}
	// A header: flush any predecessor for this stream, then hold this line as the next
	// potential header.
	if p := g.buffers[key]; p != nil {
		if !g.emit(p, now) {
			return false
		}
	}
	g.seq++
	g.buffers[key] = &pending{
		line:   line,
		count:  1,
		active: traceStartRe.MatchString(line.Raw),
		last:   now,
		seq:    g.seq,
	}
	return true
}

// flushIdle emits every buffer idle for at least the timeout as of now, earliest deadline
// first. Each is released at its own deadline — the instant a live timer would have fired.
func (g *grouper) flushIdle(now time.Time) bool {
	expired := func(p *pending) bool { return now.Sub(p.last) >= g.timeout }
	for _, key := range inOrder(g.buffers, true, expired) {
		if !g.emit(g.buffers[key], g.buffers[key].last.Add(g.timeout)) {
			return false
		}
		delete(g.buffers, key)
	}
	return true
}

// flushAll emits everything still buffered, in arrival order, at time now: ingestion ended.
func (g *grouper) flushAll(now time.Time) bool {
	all := func(*pending) bool { return true }
	for _, key := range inOrder(g.buffers, false, all) {
		if !g.emit(g.buffers[key], now) {
			return false
		}
	}
	return true
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
	g := newGrouper(ctx, out, timeout, false)

	// One timer, repointed at the earliest pending deadline. Go 1.23+ makes Stop/Reset
	// safe without draining the channel, so arm can reset freely.
	timer := time.NewTimer(timeout)
	timer.Stop()
	var timerC <-chan time.Time // nil while no buffer is waiting, which parks the select case

	arm := func(now time.Time) {
		var earliest time.Time
		found := false
		for _, p := range g.buffers {
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

	for {
		select {
		case <-ctx.Done():
			return
		case <-timerC:
			now := time.Now()
			if !g.flushIdle(now) {
				return
			}
			arm(now)
		case line, ok := <-in:
			if !ok {
				// Ingestion ended: flush everything still buffered, then let the
				// deferred close of out drive Run's normal shutdown.
				g.flushAll(time.Now())
				return
			}
			now := time.Now()
			if !g.add(line, now) {
				return
			}
			arm(now)
		}
	}
}

// CoalesceSourceTime is Coalesce for a replay (--replay, issue #35): the same folding, with
// the idle timeout measured on the capture's clock instead of the wall's.
//
// A wall-clock timer cannot work here. At replay speed a header and its continuation arrive
// microseconds apart whatever their timestamps say, so a gap that split them live would be
// folded, and a timer would never fire at all. Instead, before each line at capture time T,
// every buffer whose deadline T has reached is flushed — which is the order and the moment a
// live timer would have released it.
//
// Every emitted line is RESTAMPED with that release moment, because that is when the live
// pipeline would have processed it: live stamps a line when it arrives (pipeline.Run), and a
// held line arrives up to one timeout after its header. A line flushed by the next header is
// released at that header's time; one flushed idle, at its deadline; one flushed at end of
// input, at the last time seen. The output is therefore monotone by construction.
//
// A line with no timestamp (decode found none) happens at the current capture time.
func CoalesceSourceTime(ctx context.Context, in <-chan model.LogLine, out chan<- model.LogLine, timeout time.Duration) {
	defer close(out)
	g := newGrouper(ctx, out, timeout, true)
	var clock sourceClock

	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-in:
			if !ok {
				g.flushAll(clock.now)
				return
			}
			now := clock.observe(line.Time)
			if !g.flushIdle(now) || !g.add(line, now) {
				return
			}
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
