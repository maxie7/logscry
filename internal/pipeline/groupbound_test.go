// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// groupT0 is the instant the bound tests' lines arrive at.
var groupT0 = time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)

// heldBytes is what the grouper's buffers charge against MaxGroupBytes: each one's text and its
// source name.
func heldBytes(g *grouper) int {
	n := 0
	for _, p := range g.buffers {
		n += len(p.line.Raw) + len(p.line.Source)
	}
	return n
}

// drained empties out and returns what it held.
func drained(out chan model.LogLine) []model.LogLine {
	var got []model.LogLine
	for len(out) > 0 {
		got = append(got, <-out)
	}
	return got
}

// TestGroupingWorkPerLineIsFlat is #166: a line costs the same however many buffers are held.
// Before, flushIdle walked and sorted every buffer on each line, and the live timer walked them
// all again for its deadline, so a stream of distinct source names cost O(n log n) a line. Each
// line here is a header for a stream already held, both ways the coalescers drive the grouper:
// replay checks the deadlines and adds, live adds and re-arms. Nothing expires, so a line may
// read the earliest deadline, and no other.
func TestGroupingWorkPerLineIsFlat(t *testing.T) {
	const lines = 200
	for _, n := range []int{64, 512, 4000} {
		for _, live := range []bool{false, true} {
			out := make(chan model.LogLine, n+lines)
			g := newGrouper(context.Background(), out, time.Second, !live)
			for i := range n {
				g.add(model.LogLine{Source: unit(i), Raw: "order 4242 placed"}, groupT0)
			}
			g.visited = 0
			for i := range lines {
				line := model.LogLine{Source: unit(i % n), Raw: "order 4243 placed"}
				if live {
					g.add(line, groupT0)
					g.nextDeadline()
				} else {
					g.flushIdle(groupT0)
					g.add(line, groupT0)
				}
				drained(out)
			}
			if g.visited > lines {
				t.Errorf("live=%v, %d buffers held: %d buffers visited over %d lines, want at most 1 a line",
					live, n, g.visited, lines)
			}
		}
	}
}

// TestIdleFlushVisitsOnlyWhatItFlushes: when the timer fires, the work is the buffers that expired
// and one more, the first that has not.
func TestIdleFlushVisitsOnlyWhatItFlushes(t *testing.T) {
	const held, expiring = 4000, 10
	out := make(chan model.LogLine, held)
	g := newGrouper(context.Background(), out, time.Second, false)
	for i := range held {
		at := groupT0.Add(500 * time.Millisecond)
		if i < expiring {
			at = groupT0
		}
		g.add(model.LogLine{Source: unit(i), Raw: "order 4242 placed"}, at)
	}
	g.visited = 0
	g.flushIdle(groupT0.Add(time.Second))
	if got := len(drained(out)); got != expiring {
		t.Fatalf("%d buffers flushed, want %d", got, expiring)
	}
	if g.visited > expiring+1 {
		t.Errorf("%d buffers visited to flush %d of %d, want at most %d", g.visited, expiring, held, expiring+1)
	}
}

// groupReplay drives g as CoalesceSourceTime does, every line at groupT0.
func groupReplay(g *grouper, lines []model.LogLine) {
	for _, line := range lines {
		g.flushIdle(groupT0)
		g.add(line, groupT0)
	}
}

// distinctNames is one header from each of n source names.
func distinctNames(n int, raw string) []model.LogLine {
	lines := make([]model.LogLine, n)
	for i := range lines {
		lines[i] = model.LogLine{Source: unit(i), Raw: raw}
	}
	return lines
}

// TestPendingBuffersStayWithinTheirCount: names that never go idle, as on one timestamp, are held
// to MaxGroupBuffers. Before, each one's buffer lasted to the end of input. Over the bound the
// least recently touched is flushed early: it is emitted, so no line is lost.
func TestPendingBuffersStayWithinTheirCount(t *testing.T) {
	const n = MaxGroupBuffers + 1000
	out := make(chan model.LogLine, n)
	g := newGrouper(context.Background(), out, time.Second, true)
	groupReplay(g, distinctNames(n, "order 4242 placed"))
	if len(g.buffers) > MaxGroupBuffers {
		t.Errorf("%d buffers held, want at most %d", len(g.buffers), MaxGroupBuffers)
	}
	if got := len(out) + len(g.buffers); got != n {
		t.Errorf("%d lines emitted or held, want all %d", got, n)
	}
}

// TestPendingBuffersStayWithinTheirBytes: the bytes are bounded as well as the count, since one
// buffer can hold up to maxCoalesceBytes and its name can be as long as its line. 300 lines of
// 60 KiB are 18 MiB.
func TestPendingBuffersStayWithinTheirBytes(t *testing.T) {
	const n = 300
	out := make(chan model.LogLine, n)
	g := newGrouper(context.Background(), out, time.Second, true)
	groupReplay(g, distinctNames(n, "order "+strings.Repeat("x", 60<<10)))
	if b := heldBytes(g); b > MaxGroupBytes {
		t.Errorf("%d bytes held, want at most %d", b, MaxGroupBytes)
	}
	if got := len(out) + len(g.buffers); got != n {
		t.Errorf("%d lines emitted or held, want all %d", got, n)
	}
}

// sourcesOf is each line's source.
func sourcesOf(lines []model.LogLine) []string {
	s := make([]string, len(lines))
	for i, l := range lines {
		s[i] = l.Source
	}
	return s
}

// TestEarlyFlushEmitsTheLeastRecentlyTouched: the victim is the buffer touched longest ago, by a
// header or a fold. Everything here arrives at one instant, so every deadline ties: the oldest
// header (FIFO), or the earliest deadline then header arrival, would both pick a, which folded a
// line after b and c arrived.
func TestEarlyFlushEmitsTheLeastRecentlyTouched(t *testing.T) {
	out := make(chan model.LogLine, 16)
	g := newGrouper(context.Background(), out, time.Second, true)
	g.maxBuffers = 3
	frame := "    at com.example.ledger.Ledger.post(Ledger.java:42)"
	groupReplay(g, []model.LogLine{
		{Source: "a", Raw: "ledger closed"}, {Source: "b", Raw: "order 1"}, {Source: "c", Raw: "order 2"},
		{Source: "a", Raw: frame}, {Source: "d", Raw: "order 3"}, {Source: "e", Raw: "order 4"},
		{Source: "a", Raw: frame}, {Source: "f", Raw: "order 5"},
	})
	if got, want := sourcesOf(drained(out)), []string{"b", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("flushed early: %v, want %v", got, want)
	}
	if p := g.buffers[streamKey{source: "a"}]; p == nil || p.count != 3 {
		t.Errorf("a's buffer = %+v, want held with its 3 lines", p)
	}
}

// TestEarlyFlushTakesWhatTheBytesNeed: one line can push the bytes over by more than one buffer,
// and as many go as it takes, least recently touched first.
func TestEarlyFlushTakesWhatTheBytesNeed(t *testing.T) {
	out := make(chan model.LogLine, 16)
	g := newGrouper(context.Background(), out, time.Second, true)
	g.maxBytes = 150
	small := strings.Repeat("x", 48) // 50 bytes with its two-byte source name
	groupReplay(g, []model.LogLine{
		{Source: "s1", Raw: small}, {Source: "s2", Raw: small}, {Source: "s3", Raw: small},
		{Source: "s4", Raw: strings.Repeat("y", 98)},
	})
	if got, want := sourcesOf(drained(out)), []string{"s1", "s2"}; !slices.Equal(got, want) {
		t.Errorf("flushed early: %v, want %v", got, want)
	}
	if b := heldBytes(g); b != 150 {
		t.Errorf("%d bytes held, want 150", b)
	}
}

// TestEarlyFlushesNotifyOnceAndCount: each early flush counts, and the first is said once.
func TestEarlyFlushesNotifyOnceAndCount(t *testing.T) {
	notified := 0
	early := NewEvictions(func() { notified++ })
	out := make(chan model.LogLine, 16)
	g := newGrouper(WithEarlyFlushes(context.Background(), early), out, time.Second, true)
	g.maxBuffers = 2
	groupReplay(g, distinctNames(10, "order 4242 placed"))
	if n := early.Count(); n != 8 {
		t.Errorf("%d early flushes counted, want 8", n)
	}
	if notified != 1 {
		t.Errorf("notified %d times, want once", notified)
	}
}

// coalesceAll runs lines through Coalesce (live) or CoalesceSourceTime and returns what came out.
func coalesceAll(t *testing.T, ctx context.Context, live bool, timeout time.Duration, lines []model.LogLine) []model.LogLine {
	t.Helper()
	in := make(chan model.LogLine, len(lines))
	for _, l := range lines {
		in <- l
	}
	close(in)
	out := make(chan model.LogLine, len(lines)+1)
	if live {
		go Coalesce(ctx, in, out, timeout)
	} else {
		go CoalesceSourceTime(ctx, in, out, timeout)
	}
	var got []model.LogLine
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l, ok := <-out:
			if !ok {
				return got
			}
			got = append(got, l)
		case <-deadline:
			t.Fatalf("the coalescer did not close its output; got %d so far", len(got))
		}
	}
}

// TestEarlyFlushSplitsATraceInProgress pins what the bound costs: a trace whose stream is the least
// recently touched when the bound is passed is emitted as far as it has got, and the rest of it is
// a second event, opened by the next frame as a header. Its later frames still fold into that one,
// by indentation and then as the summary of an active block. Through both coalescers at the real
// bound, with no idle flush in reach: 4,096 other names arrive between the trace's first frame and
// its second, and each new name past the bound flushes one buffer early.
func TestEarlyFlushSplitsATraceInProgress(t *testing.T) {
	head := []string{pythonTraceOpener, `  File "/srv/ledger/app.py", line 7, in post`}
	tail := []string{`  File "/srv/ledger/step.py", line 3, in step`, "    step(n)", "ZeroDivisionError: division by zero"}
	trace := func(raw string) model.LogLine {
		return model.LogLine{Time: groupT0, Source: "proc:ledger", Stream: model.Stderr, Raw: raw}
	}
	var lines []model.LogLine
	for _, raw := range head {
		lines = append(lines, trace(raw))
	}
	for i := range MaxGroupBuffers {
		lines = append(lines, model.LogLine{Time: groupT0, Source: unit(i), Raw: "order 4242 placed"})
	}
	for _, raw := range tail {
		lines = append(lines, trace(raw))
	}
	want := []string{strings.Join(head, "\n"), strings.Join(tail, "\n")}

	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "CoalesceSourceTime", true: "Coalesce"}[live], func(t *testing.T) {
			early := NewEvictions(nil)
			got := coalesceAll(t, WithEarlyFlushes(context.Background(), early), live, time.Hour, lines)
			var pieces []string
			for _, l := range got {
				if l.Source == "proc:ledger" {
					pieces = append(pieces, l.Raw)
				}
			}
			if !slices.Equal(pieces, want) {
				t.Errorf("the trace came out as %d event(s), want 2:\n%s", len(pieces), strings.Join(pieces, "\n--\n"))
			}
			if len(got) != MaxGroupBuffers+2 {
				t.Errorf("%d events, want %d: every line emitted", len(got), MaxGroupBuffers+2)
			}
			if n := early.Count(); n != 2 {
				t.Errorf("%d early flushes, want 2: the trace, then the first name", n)
			}
		})
	}
}

// TestGroupBoundIsTheDocumentedOne: the bound is the one README and BACKLOG state, and a grouper
// starts with it.
func TestGroupBoundIsTheDocumentedOne(t *testing.T) {
	if MaxGroupBuffers != 4096 || MaxGroupBytes != 16<<20 {
		t.Errorf("bound = %d buffers, %d bytes; want 4,096 and 16 MiB", MaxGroupBuffers, MaxGroupBytes)
	}
	g := newGrouper(context.Background(), nil, time.Second, false)
	if g.maxBuffers != MaxGroupBuffers || g.maxBytes != MaxGroupBytes {
		t.Errorf("a grouper starts at %d buffers, %d bytes; want the constants", g.maxBuffers, g.maxBytes)
	}
}

// TestEarlyFlushChargesTheSourceName: a buffer's name counts toward the bytes as well as its text.
// Three buffers fill the bound exactly; a one-byte line under a two-byte name then passes it, so
// the least recently touched goes; a 100-byte one passes it by two buffers' worth.
func TestEarlyFlushChargesTheSourceName(t *testing.T) {
	out := make(chan model.LogLine, 16)
	g := newGrouper(context.Background(), out, time.Second, true)
	g.maxBytes = 150
	small := strings.Repeat("x", 48)
	groupReplay(g, []model.LogLine{
		{Source: "s1", Raw: small}, {Source: "s2", Raw: small}, {Source: "s3", Raw: small},
		{Source: "s4", Raw: "z"},
	})
	if got, want := sourcesOf(drained(out)), []string{"s1"}; !slices.Equal(got, want) {
		t.Errorf("after the one-byte line, flushed early: %v, want %v", got, want)
	}
	groupReplay(g, []model.LogLine{{Source: "s5", Raw: strings.Repeat("y", 98)}})
	if got, want := sourcesOf(drained(out)), []string{"s2", "s3"}; !slices.Equal(got, want) {
		t.Errorf("after the 100-byte line, flushed early: %v, want %v", got, want)
	}
	if b := heldBytes(g); b != 103 {
		t.Errorf("%d bytes held, want 103", b)
	}
}

// TestAFoldCanFlushEarly: a fold grows its buffer, and when that passes the bytes the least
// recently touched other buffer goes, not the one still growing.
func TestAFoldCanFlushEarly(t *testing.T) {
	out := make(chan model.LogLine, 16)
	g := newGrouper(context.Background(), out, time.Second, true)
	g.maxBytes = 150
	small := strings.Repeat("x", 48)
	groupReplay(g, []model.LogLine{
		{Source: "s1", Raw: small}, {Source: "s2", Raw: small}, {Source: "s3", Raw: small},
		{Source: "s3", Raw: "    at frame"},
	})
	if got, want := sourcesOf(drained(out)), []string{"s1"}; !slices.Equal(got, want) {
		t.Errorf("flushed early: %v, want %v", got, want)
	}
	if p := g.buffers[streamKey{source: "s3"}]; p == nil || p.count != 2 {
		t.Errorf("s3's buffer = %+v, want held with its 2 lines", p)
	}
}

// TestEarlyFlushCancelled: an early flush that cannot be delivered, the consumer gone, stops add
// and drops nothing.
func TestEarlyFlushCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := newGrouper(ctx, make(chan model.LogLine), time.Second, true)
	g.maxBuffers = 1
	if !g.add(model.LogLine{Source: "a", Raw: "order 1"}, groupT0) {
		t.Fatal("add within the bound reported cancellation")
	}
	if g.add(model.LogLine{Source: "b", Raw: "order 2"}, groupT0) {
		t.Error("add past the bound reported success with the consumer gone")
	}
	if len(g.buffers) != 2 {
		t.Errorf("%d buffers held, want both: nothing is dropped", len(g.buffers))
	}
}

// checkUnreachable fails if the grouper still reaches a buffer it has let go of: through its heap's
// spare capacity, or through a buffer's links or index. It also checks that every buffer held is
// where the heap and the touch order say.
func checkUnreachable(t *testing.T, g *grouper, seen map[*pending]bool) {
	t.Helper()
	h := g.deadlines
	for i, p := range h[len(h):cap(h)] {
		if p != nil {
			t.Errorf("the heap's spare slot %d still holds a buffer (%q)", len(h)+i, p.line.Source)
		}
	}
	held := 0
	for p := g.order.front; p != nil; p = p.next {
		held++
		if g.buffers[streamKey{source: p.line.Source, stream: p.line.Stream}] != p || h[p.index] != p {
			t.Errorf("buffer %q is in the touch order but not where the map and the heap say", p.line.Source)
		}
	}
	if held != len(g.buffers) || len(h) != len(g.buffers) {
		t.Errorf("%d buffers in the map, %d in the heap, %d in the touch order", len(g.buffers), len(h), held)
	}
	for p := range seen {
		if g.buffers[streamKey{source: p.line.Source, stream: p.line.Stream}] == p {
			continue
		}
		if p.index != -1 || p.prev != nil || p.next != nil {
			t.Errorf("emitted buffer %q kept index %d, prev %v, next %v; want -1 and no links",
				p.line.Source, p.index, p.prev != nil, p.next != nil)
		}
	}
}

// TestEmittedBuffersAreUnreachable: a buffer that has been emitted, whichever way it left, is
// reachable from nothing the grouper keeps. The heap's backing array outlives its length, so a slot
// Pop vacated and did not clear would hold an emitted line, up to maxCoalesceBytes, for as long as
// the run; so would a link from a buffer still held. Every way out is taken here: early flushes,
// header flushes, a cap flush, the idle flush and the flush at end of input.
func TestEmittedBuffersAreUnreachable(t *testing.T) {
	out := make(chan model.LogLine, 4*maxCoalesceLines)
	g := newGrouper(context.Background(), out, time.Second, true)
	g.maxBuffers = 48
	seen := map[*pending]bool{}
	step := func(line model.LogLine) {
		g.flushIdle(groupT0)
		g.add(line, groupT0)
		for _, p := range g.buffers {
			seen[p] = true
		}
		drained(out)
	}
	for i := range 64 { // 16 early flushes
		step(model.LogLine{Source: unit(i), Raw: "order 4242 placed"})
	}
	for i := 16; i < 64; i++ { // a header flush each
		step(model.LogLine{Source: unit(i), Raw: "order 4243 placed"})
	}
	step(model.LogLine{Source: unit(63), Raw: "ledger closed"})
	for range maxCoalesceLines - 1 { // the cap flush
		step(model.LogLine{Source: unit(63), Raw: "    at frame"})
	}
	checkUnreachable(t, g, seen)

	g.flushIdle(groupT0.Add(time.Hour))
	drained(out)
	if len(g.deadlines) != 0 || g.bytes != 0 {
		t.Fatalf("after the idle flush: %d buffers, %d bytes held, want none", len(g.deadlines), g.bytes)
	}
	checkUnreachable(t, g, seen)

	for i := range 8 {
		step(model.LogLine{Source: unit(i), Raw: "order 4244 placed"})
	}
	g.flushAll(groupT0)
	drained(out)
	if len(g.deadlines) != 0 || g.bytes != 0 {
		t.Fatalf("after the flush at end of input: %d buffers, %d bytes held, want none", len(g.deadlines), g.bytes)
	}
	checkUnreachable(t, g, seen)
}

// TestIdleFlushCancelled: an idle flush that cannot be delivered stops, and the buffer it could not
// deliver is still held, so a heap pop never runs ahead of its emit.
func TestIdleFlushCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := newGrouper(ctx, make(chan model.LogLine), time.Second, true)
	g.add(model.LogLine{Source: "a", Raw: "order 1"}, groupT0)
	if g.flushIdle(groupT0.Add(time.Second)) {
		t.Error("flushIdle reported success with the consumer gone")
	}
	if len(g.buffers) != 1 || len(g.deadlines) != 1 {
		t.Errorf("%d buffers, %d deadlines held, want the one that was not delivered", len(g.buffers), len(g.deadlines))
	}
}

// TestFlushAllCancelled: the flush at end of input stops at the first line it cannot deliver.
func TestFlushAllCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := newGrouper(ctx, make(chan model.LogLine), time.Second, true)
	g.add(model.LogLine{Source: "a", Raw: "order 1"}, groupT0)
	if g.flushAll(groupT0) {
		t.Error("flushAll reported success with the consumer gone")
	}
	if len(g.buffers) != 1 {
		t.Errorf("%d buffers held, want the one that was not delivered", len(g.buffers))
	}
}
