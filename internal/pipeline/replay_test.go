// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// replayT0 is the start of every replay test's capture. A fixed date far from the wall
// clock, so a wall-clock read leaking into replay shows up as a timestamp in the wrong year.
var replayT0 = time.Date(2026, 8, 29, 10, 40, 0, 0, time.UTC)

// at is a line stamped at an offset into the capture, the way a journald entry arrives
// from decode: its own time on it, the pipeline's fields still empty.
func at(offset time.Duration, source string, stream model.Stream, raw string) model.LogLine {
	return model.LogLine{Time: replayT0.Add(offset), Source: source, Stream: stream, Raw: raw}
}

// scoringCapture exercises every time-dependent path the scorer has: warmup, first-seen
// novelty, cooloff novelty, a burst against an established baseline, severity, the
// explanation cache, and the rate limiter running dry. It is monotone — out-of-order
// time is TestSourceClockIsMonotone's subject, not this one's.
func scoringCapture() []model.LogLine {
	var lines []model.LogLine
	for i := range 300 { // steady background: one a second
		lines = append(lines, at(time.Duration(i)*time.Second, "journald:app", model.Stdout,
			fmt.Sprintf("request served id=%d", i)))
	}
	for i := range 30 { // a slow template, then a burst of it
		lines = append(lines, at(time.Duration(i)*10*time.Second+500*time.Millisecond, "journald:db", model.Stdout,
			fmt.Sprintf("retry attempt %d", i)))
	}
	for i := range 40 {
		lines = append(lines, at(200*time.Second+time.Duration(i)*100*time.Millisecond+50*time.Millisecond,
			"journald:db", model.Stdout, fmt.Sprintf("retry attempt %d", 100+i)))
	}
	lines = append(lines,
		at(120*time.Second+200*time.Millisecond, "journald:app", model.Stderr, "ERROR: disk quota exceeded"),
		at(121*time.Second+200*time.Millisecond, "journald:app", model.Stderr, "ERROR: disk quota exceeded"), // cached
		at(5*time.Second+300*time.Millisecond, "journald:cron", model.Stderr, "WARN: job skipped"),
		// Back after longer than the cooloff: novel again, and with WARN+stderr still under.
		at(5*time.Second+300*time.Millisecond+16*time.Minute, "journald:cron", model.Stderr, "WARN: job skipped"),
	)
	// Twenty distinct crashes inside one second: more than the bucket holds.
	for i := range 20 {
		lines = append(lines, at(250*time.Second+time.Duration(i)*40*time.Millisecond, "journald:kernel", model.Stderr,
			"FATAL: crash in module "+string(rune('a'+i))))
	}
	sortByTime(lines)
	return lines
}

func sortByTime(lines []model.LogLine) {
	for i := 1; i < len(lines); i++ { // stable insertion sort: ties keep their order
		for j := i; j > 0 && lines[j].Time.Before(lines[j-1].Time); j-- {
			lines[j], lines[j-1] = lines[j-1], lines[j]
		}
	}
}

// scored is the part of an Event that scoring decides — everything the no-drift property
// is about, and nothing that depends on which clock stamped it.
type scored struct {
	Hash, Pattern string
	Count         int
	Score         float64
	Escalate      bool
	Reasons       []string
}

func scoredOf(ev Event) scored {
	return scored{ev.Hash, ev.Pattern, ev.Count, ev.Score, ev.Escalate, ev.Reasons}
}

// TestSourceTimeRunMatchesInjectedNow is the no-drift property, exactly: replay's clock
// adds nothing to scoring.
//
// Process(line, now) with now taken from the line is the contract every scorer and
// pipeline test already runs on — time as an argument. Run with SourceTime must produce
// the same stream of decisions from the same lines, down to the reason strings byte for
// byte (they carry measured rates and gaps, so a clock that differed by a millisecond
// anywhere would show). If this fails, replay is scoring a different program.
func TestSourceTimeRunMatchesInjectedNow(t *testing.T) {
	lines := scoringCapture()

	want := New(score.New(score.Defaults(), nil))
	var direct []scored
	for _, l := range lines {
		direct = append(direct, scoredOf(want.Process(l, l.Time)))
	}

	in := make(chan model.LogLine, len(lines))
	for _, l := range lines {
		in <- l
	}
	close(in)
	events := make(chan Event, len(lines))
	sc := score.New(score.Defaults(), nil)
	Run(context.Background(), in, Options{Events: events, Scorer: sc, SourceTime: true})

	var replayed []scored
	for ev := range events {
		replayed = append(replayed, scoredOf(ev))
	}

	if !reflect.DeepEqual(direct, replayed) {
		for i := range min(len(direct), len(replayed)) {
			if !reflect.DeepEqual(direct[i], replayed[i]) {
				t.Fatalf("line %d scored differently under replay:\n direct:   %+v\n replayed: %+v", i, direct[i], replayed[i])
			}
		}
		t.Fatalf("replay emitted %d events, direct %d", len(replayed), len(direct))
	}
	// The capture must actually exercise the gates, or equality proves nothing.
	st := sc.Stats()
	if st.Escalated == 0 || st.Suppressed == 0 || st.Cached == 0 {
		t.Fatalf("the capture does not reach every gate: %+v", st)
	}
	var burst, cooloff bool
	for _, s := range replayed {
		for _, r := range s.Reasons {
			burst = burst || len(r) > 6 && r[:6] == "burst:"
			cooloff = cooloff || len(r) > 22 && r[:22] == "novel template (unseen"
		}
	}
	if !burst || !cooloff {
		t.Fatalf("the capture must produce a burst and a cooloff novelty (burst=%v cooloff=%v)", burst, cooloff)
	}
}

// TestSourceTimeIsNotWallTime is the negative half: without SourceTime, Run is the live
// program and stamps the wall clock, so the same capture — arriving inside a millisecond —
// scores differently. It is what makes the test above able to fail.
func TestSourceTimeIsNotWallTime(t *testing.T) {
	run := func(sourceTime bool) ([]Event, score.Stats) {
		lines := scoringCapture()
		in := make(chan model.LogLine, len(lines))
		for _, l := range lines {
			in <- l
		}
		close(in)
		events := make(chan Event, len(lines))
		sc := score.New(score.Defaults(), nil)
		Run(context.Background(), in, Options{Events: events, Scorer: sc, SourceTime: sourceTime})
		var evs []Event
		for ev := range events {
			evs = append(evs, ev)
		}
		return evs, sc.Stats()
	}
	live, liveStats := run(false)
	_, replayStats := run(true)
	for _, ev := range live {
		if ev.FirstSeen.Year() == replayT0.Year() && ev.FirstSeen.YearDay() == replayT0.YearDay() {
			t.Fatalf("a live run stamped the capture's date: %s", ev.FirstSeen)
		}
	}
	if liveStats == replayStats {
		t.Fatalf("live and replay scored a ten-minute capture identically (%+v): the clock is not reaching the scorer", liveStats)
	}
}

// TestSourceClockIsMonotone: the clock the scorer sees never runs backwards and never
// stops existing. A line whose timestamp predates its predecessor's (journal files merged
// out of order, or a coalescer flush) takes the current time instead of rewinding it —
// the recent ring is append-ordered and countSince stops at the first older entry, so a
// rewind would silently shrink every burst window. A line with no timestamp at all holds
// the clock where it is.
func TestSourceClockIsMonotone(t *testing.T) {
	var c sourceClock
	steps := []struct {
		in, want time.Time
	}{
		{replayT0, replayT0},
		{replayT0.Add(2 * time.Second), replayT0.Add(2 * time.Second)},
		{replayT0.Add(time.Second), replayT0.Add(2 * time.Second)}, // backwards: held
		{time.Time{}, replayT0.Add(2 * time.Second)},               // untimed: held
		{replayT0.Add(3 * time.Second), replayT0.Add(3 * time.Second)},
	}
	for i, s := range steps {
		if got := c.observe(s.in); !got.Equal(s.want) {
			t.Fatalf("step %d: observe(%s) = %s, want %s", i, s.in, got, s.want)
		}
	}
}

// TestSnapshotNowIsTheReplayClock: in replay the snapshot carries the capture's current
// time, so the renderer can say "3m ago" about a capture from last week. In a live run it
// is zero, and the renderer keeps using the wall clock exactly as before.
func TestSnapshotNowIsTheReplayClock(t *testing.T) {
	for _, sourceTime := range []bool{true, false} {
		lines := []model.LogLine{
			at(0, "journald:app", model.Stdout, "one"),
			at(90*time.Second, "journald:app", model.Stdout, "two"),
		}
		in := make(chan model.LogLine, len(lines))
		for _, l := range lines {
			in <- l
		}
		close(in)
		snaps := make(chan Snapshot, 1)
		go Run(context.Background(), in, Options{Snapshots: snaps, SourceTime: sourceTime})
		var last Snapshot
		for s := range snaps {
			last = s
		}
		switch {
		case sourceTime && !last.Now.Equal(replayT0.Add(90*time.Second)):
			t.Errorf("replay snapshot Now = %s, want the last line's time", last.Now)
		case !sourceTime && !last.Now.IsZero():
			t.Errorf("live snapshot Now = %s, want zero (the renderer uses its own clock)", last.Now)
		}
	}
}

// driveAt feeds timed lines through CoalesceSourceTime and returns what came out.
func driveAt(t *testing.T, timeout time.Duration, lines ...model.LogLine) []model.LogLine {
	t.Helper()
	in := make(chan model.LogLine, len(lines))
	for _, l := range lines {
		in <- l
	}
	close(in)
	out := make(chan model.LogLine, len(lines)+1)
	go CoalesceSourceTime(context.Background(), in, out, timeout)
	var got []model.LogLine
	deadline := time.After(2 * time.Second)
	for {
		select {
		case l, ok := <-out:
			if !ok {
				return got
			}
			got = append(got, l)
		case <-deadline:
			t.Fatalf("CoalesceSourceTime did not close its output; got %d so far", len(got))
		}
	}
}

// TestCoalesceSourceTimeSplitsOnVirtualGap: the idle timeout is measured on the capture's
// clock. A continuation arriving longer after its header than the timeout is its own
// event, exactly as it would have been live — even though at replay speed the two lines
// arrive microseconds apart and a wall-clock timer would never have fired.
func TestCoalesceSourceTimeSplitsOnVirtualGap(t *testing.T) {
	got := driveAt(t, 200*time.Millisecond,
		at(0, "journald:app", model.Stderr, "Traceback (most recent call last):"),
		at(300*time.Millisecond, "journald:app", model.Stderr, `  File "/srv/app/x.py", line 3, in <module>`),
	)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (the gap exceeds the timeout):\n%s", len(got), dump(got))
	}
}

// TestCoalesceSourceTimeJoinsWithinGap is the other side: inside the timeout it folds.
func TestCoalesceSourceTimeJoinsWithinGap(t *testing.T) {
	got := driveAt(t, 200*time.Millisecond,
		at(0, "journald:app", model.Stderr, "Traceback (most recent call last):"),
		at(100*time.Millisecond, "journald:app", model.Stderr, `  File "/srv/app/x.py", line 3, in <module>`),
		at(150*time.Millisecond, "journald:app", model.Stderr, "ValueError: bad input"),
	)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 folded trace:\n%s", len(got), dump(got))
	}
}

// TestCoalesceSourceTimeStampsArrival: a line held by the coalescer reaches the pipeline
// stamped with the moment a LIVE run would have processed it, because live stamps at
// processing time. Flushed by the next header: that header's time. Flushed by the idle
// timeout: its deadline. Flushed at end of input: the last time seen. The output is
// therefore monotone by construction, and in deadline-then-arrival order.
func TestCoalesceSourceTimeStampsArrival(t *testing.T) {
	const timeout = 200 * time.Millisecond
	got := driveAt(t, timeout,
		at(0, "journald:a", model.Stdout, "a one"),
		at(10*time.Millisecond, "journald:b", model.Stdout, "b one"),
		at(50*time.Millisecond, "journald:a", model.Stdout, "a two"), // flushes "a one" at 50ms
		at(time.Second, "journald:c", model.Stdout, "c one"),         // idle-flushes b (210ms), then a (250ms)
	)
	want := []struct {
		raw string
		at  time.Duration
	}{
		{"a one", 50 * time.Millisecond},
		{"b one", 10*time.Millisecond + timeout},
		{"a two", 50*time.Millisecond + timeout},
		{"c one", time.Second}, // end of input: the last time seen
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d:\n%s", len(got), len(want), dump(got))
	}
	for i, w := range want {
		if got[i].Raw != w.raw || !got[i].Time.Equal(replayT0.Add(w.at)) {
			t.Errorf("event %d = %q at +%s, want %q at +%s",
				i, got[i].Raw, got[i].Time.Sub(replayT0), w.raw, w.at)
		}
	}
}

// TestCoalesceSourceTimeUntimedLineHoldsTheClock: a line with no timestamp (decode found
// none) does not read as the zero time — which would put every buffer's deadline in the
// past — and does not read as "now" either. It happens at the current capture time.
func TestCoalesceSourceTimeUntimedLineHoldsTheClock(t *testing.T) {
	untimed := model.LogLine{Source: "journald:app", Stream: model.Stderr, Raw: `  File "/srv/app/x.py", line 3`}
	got := driveAt(t, 200*time.Millisecond,
		at(0, "journald:app", model.Stderr, "Traceback (most recent call last):"),
		untimed,
	)
	if len(got) != 1 {
		t.Fatalf("an untimed continuation was split off its header:\n%s", dump(got))
	}
	if !got[0].Time.Equal(replayT0) {
		t.Errorf("flushed at %s, want the capture's current time", got[0].Time)
	}
}
