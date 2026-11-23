// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// The coalescer's buffer handling as it was before #166, kept as the reference the grouper is
// checked against: below the bound, the new grouper must emit exactly what this one does, in the
// same order and at the same times, and arm the timer for the same deadline. The code from
// refInOrder to refGrouper.flushAll is main's at 091ab80, verbatim but for the ref prefix on its
// names; refNextDeadline is the walk Coalesce's arm did, lifted into a method. Folding itself
// (isContinuation, traceStartRe, the caps) is shared, not copied: it is out of #166's scope, and
// the comparison is of what the buffers do with it.

// refInOrder returns the buffered keys that pass keep, in the order they must be flushed:
// earliest idle deadline first, then header arrival. At end of input every deadline is
// irrelevant and arrival alone decides.
//
// It exists because the buffers live in a map, and ranging over a map to flush several of
// them at once hands the pipeline its lines in whatever order the runtime's hash seed
// picked — different on every run. journald keys one stream per unit and Docker one per
// container, so a multi-source run routinely holds many buffers, and the order they leave
// in is the order the stream pane shows, --plain prints, and the scorer's context ring
// records (#69).
func refInOrder(buffers map[streamKey]*pending, byDeadline bool, keep func(*pending) bool) []streamKey {
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

// refGrouper is the buffer state and the fold/flush rules both coalescers share. Only what
// drives it differs: Coalesce is clocked by the wall and a timer, CoalesceSourceTime by the
// lines' own timestamps. Owned by the one goroutine running either of them.
type refGrouper struct {
	ctx     context.Context
	out     chan<- model.LogLine
	timeout time.Duration
	buffers map[streamKey]*pending
	seq     uint64 // numbers headers as they arrive; see refInOrder

	// restamp sets each emitted line's Time to the moment it is released. Only replay wants
	// it (see CoalesceSourceTime); a live line keeps the time its source gave it.
	restamp bool
}

func newRefGrouper(ctx context.Context, out chan<- model.LogLine, timeout time.Duration, restamp bool) *refGrouper {
	return &refGrouper{ctx: ctx, out: out, timeout: timeout, buffers: make(map[streamKey]*pending), restamp: restamp}
}

// emit sends p's merged line downstream at time at, reporting false on cancellation so the
// caller can stop rather than block on a consumer that has already gone.
func (g *refGrouper) emit(p *pending, at time.Time) bool {
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
func (g *refGrouper) add(line model.LogLine, now time.Time) bool {
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
func (g *refGrouper) flushIdle(now time.Time) bool {
	expired := func(p *pending) bool { return now.Sub(p.last) >= g.timeout }
	for _, key := range refInOrder(g.buffers, true, expired) {
		if !g.emit(g.buffers[key], g.buffers[key].last.Add(g.timeout)) {
			return false
		}
		delete(g.buffers, key)
	}
	return true
}

// flushAll emits everything still buffered, in arrival order, at time now: ingestion ended.
func (g *refGrouper) flushAll(now time.Time) bool {
	all := func(*pending) bool { return true }
	for _, key := range refInOrder(g.buffers, false, all) {
		if !g.emit(g.buffers[key], now) {
			return false
		}
	}
	return true
}

// refNextDeadline is the earliest idle deadline of the buffers held, found as Coalesce's arm
// found it at 091ab80: by a walk over every buffer.
func (g *refGrouper) refNextDeadline() (time.Time, bool) {
	timeout := g.timeout
	var earliest time.Time
	found := false
	for _, p := range g.buffers {
		if d := p.last.Add(timeout); !found || d.Before(earliest) {
			earliest, found = d, true
		}
	}
	return earliest, found
}

// diffTimeout is the idle timeout of every differential run.
const diffTimeout = 200 * time.Millisecond

// diffRaws are the lines a differential run is made of: headers, every kind of continuation,
// the lines that continue only an active block, Python's chain, and a long indented line that
// reaches maxCoalesceBytes in about twenty folds. All invented.
var diffRaws = []string{
	"order 4242 placed", "INFO: worker 3 up", `level=info msg="job done" id=8812`,
	"    at com.example.ledger.Ledger.post(Ledger.java:42)", `  File "/srv/ledger/app.py", line 7, in post`,
	"\t/srv/ledger/main.go:42 +0x1d", "created by main.main in goroutine 1", "    step(n)",
	pythonTraceOpener, "panic: ledger closed", "goroutine 7 [running]:",
	`Exception in thread "main" java.lang.IllegalStateException: ledger closed`,
	"", "ZeroDivisionError: division by zero", "main.post(0x1)", "rax    0xca",
	pythonContextSep, pythonCauseSep,
	"    " + strings.Repeat("ledger ", 450),
}

// diffSteps are how far the clock moves before a step: mostly not at all, so that ties in
// deadline are common, then to either side of the timeout, and back, which live time never
// does and the replay clock refuses; the comparison holds either way.
var diffSteps = []time.Duration{
	0, 0, 0, time.Millisecond, diffTimeout / 2, diffTimeout - time.Millisecond, diffTimeout,
	diffTimeout + time.Millisecond, 3 * diffTimeout, -50 * time.Millisecond,
}

// groupStep is one step of a differential run: a line, or, live, the idle timer firing.
type groupStep struct {
	fire   bool // live only: flushIdle instead of a line; with no step, at the next deadline
	step   time.Duration
	source int
	stream model.Stream
	raw    string
}

// randomGroupSteps returns n steps over up to 50 sources, with an occasional run to the line cap.
func randomGroupSteps(r *rand.Rand, n int) []groupStep {
	sources := 1 + r.IntN(50)
	var steps []groupStep
	for len(steps) < n {
		st := groupStep{
			fire:   r.IntN(8) == 0,
			step:   diffSteps[r.IntN(len(diffSteps))],
			source: r.IntN(sources),
			stream: model.Stream(r.IntN(2)),
			raw:    diffRaws[r.IntN(len(diffRaws))],
		}
		steps = append(steps, st)
		if r.IntN(200) == 0 {
			for range maxCoalesceLines + 5 {
				steps = append(steps, groupStep{source: st.source, stream: st.stream, raw: "    at frame"})
			}
		}
	}
	return steps
}

// fuzzGroupSteps decodes three bytes a step: source and stream, line, and clock step with the
// timer.
func fuzzGroupSteps(data []byte) []groupStep {
	var steps []groupStep
	for ; len(data) >= 3; data = data[3:] {
		steps = append(steps, groupStep{
			fire:   data[2]&0x80 != 0,
			step:   diffSteps[int(data[2]&0x7f)%len(diffSteps)],
			source: int(data[0] & 0x0f),
			stream: model.Stream(data[0] >> 7),
			raw:    diffRaws[int(data[1])%len(diffRaws)],
		})
	}
	return steps
}

// compareGroupers runs steps through the reference and through the grouper, driven as Coalesce
// drives it (live) or as CoalesceSourceTime does, and fails on the first difference: in what
// either emits, field by field and Time included, in order, or, live, in the deadline the timer
// would be armed for after each step. The steps stay far below the bound, so the grouper must
// flush nothing early.
func compareGroupers(t testing.TB, steps []groupStep, live bool) {
	t.Helper()
	ctx := context.Background()
	early := NewEvictions(nil)
	refOut := make(chan model.LogLine, 4*maxCoalesceLines)
	newOut := make(chan model.LogLine, 4*maxCoalesceLines)
	ref := newRefGrouper(ctx, refOut, diffTimeout, !live)
	g := newGrouper(WithEarlyFlushes(ctx, early), newOut, diffTimeout, !live)

	var refGot, newGot []model.LogLine
	drain := func() {
		for len(refOut) > 0 {
			refGot = append(refGot, <-refOut)
		}
		for len(newOut) > 0 {
			newGot = append(newGot, <-newOut)
		}
	}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	var clock sourceClock
	for i, st := range steps {
		now = now.Add(st.step)
		line := model.LogLine{Time: now, Source: fmt.Sprintf("journald:unit-%d", st.source), Stream: st.stream, Raw: st.raw}
		switch {
		case live && st.fire:
			if d, ok := ref.refNextDeadline(); ok && st.step == 0 && d.After(now) {
				now = d
			}
			ref.flushIdle(now)
			g.flushIdle(now)
		case live:
			ref.add(line, now)
			g.add(line, now)
		default:
			c := clock.observe(line.Time)
			ref.flushIdle(c)
			ref.add(line, c)
			g.flushIdle(c)
			g.add(line, c)
		}
		if live {
			rd, rok := ref.refNextDeadline()
			nd, nok := g.nextDeadline()
			if rok != nok || !rd.Equal(nd) {
				t.Fatalf("step %d: the timer is armed for %v (%v), want %v (%v)", i, nd, nok, rd, rok)
			}
		}
		drain()
		if !sameLines(refGot, newGot) {
			t.Fatalf("step %d: the grouper emitted\n%s\nwhere the reference emitted\n%s", i, dumpTimed(newGot), dumpTimed(refGot))
		}
	}
	end := now
	if !live {
		end = clock.now
	}
	ref.flushAll(end)
	g.flushAll(end)
	drain()
	if !sameLines(refGot, newGot) {
		t.Fatalf("at the end: the grouper emitted\n%s\nwhere the reference emitted\n%s", dumpTimed(newGot), dumpTimed(refGot))
	}
	if n := early.Count(); n != 0 {
		t.Fatalf("%d buffer(s) flushed early far below the bound", n)
	}
}

// sameLines reports whether a and b hold the same lines in the same order, every field equal.
func sameLines(a, b []model.LogLine) bool {
	return slices.EqualFunc(a, b, func(x, y model.LogLine) bool {
		return x.Time.Equal(y.Time) && x.Source == y.Source && x.Stream == y.Stream &&
			x.Raw == y.Raw && x.Level == y.Level && x.Message == y.Message
	})
}

// dumpTimed renders lines for a failure message with their time, source and stream.
func dumpTimed(lines []model.LogLine) string {
	var b strings.Builder
	for i, l := range lines {
		raw := l.Raw
		if len(raw) > 60 {
			raw = raw[:60] + "…"
		}
		fmt.Fprintf(&b, "  [%d] %s %s/%d %q\n", i, l.Time.Format("15:04:05.000"), l.Source, l.Stream, raw)
	}
	return b.String()
}

// TestGrouperMatchesReference: below the bound, the grouper emits exactly what the reference
// does, in the same order and at the same times, and arms the timer for the same deadline, over
// seeded random interleavings of sources, streams, folds, caps and clock steps (#166).
func TestGrouperMatchesReference(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "replay", true: "live"}[live], func(t *testing.T) {
			for seed := range uint64(300) {
				r := rand.New(rand.NewPCG(seed, 166))
				steps := randomGroupSteps(r, 400)
				t.Run(fmt.Sprint(seed), func(t *testing.T) { compareGroupers(t, steps, live) })
			}
		})
	}
}

// FuzzGrouperMatchesReference is TestGrouperMatchesReference over steps the fuzzer builds,
// each input run both ways. The seeds are invented: one source folding, sources interleaved on
// one instant, the timer firing at deadlines, and a run of long lines to the byte cap.
func FuzzGrouperMatchesReference(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00, 0x08, 0x00, 0x00, 0x05, 0x00, 0x00, 0x0d, 0x00, 0x00, 0x00, 0x06})
	f.Add([]byte{0x01, 0x00, 0x00, 0x02, 0x00, 0x00, 0x81, 0x00, 0x00, 0x01, 0x03, 0x00, 0x03, 0x00, 0x00})
	f.Add([]byte{0x00, 0x09, 0x00, 0x00, 0x04, 0x01, 0x01, 0x00, 0x80, 0x00, 0x04, 0x07, 0x02, 0x00, 0x80, 0x00, 0x00, 0x08})
	f.Add(slices.Repeat([]byte{0x03, 0x12, 0x00}, 25))
	f.Add([]byte{0x00, 0x08, 0x00, 0x00, 0x0d, 0x00, 0x00, 0x0c, 0x00, 0x00, 0x10, 0x00, 0x00, 0x0c, 0x00, 0x00, 0x08, 0x00, 0x00, 0x0d, 0x09})
	f.Fuzz(func(t *testing.T, data []byte) {
		steps := fuzzGroupSteps(data)
		compareGroupers(t, steps, false)
		compareGroupers(t, steps, true)
	})
}
