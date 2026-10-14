// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"testing"
	"time"
)

// The tests in this file drive followerSet directly, with a follow function that
// announces each follower and then blocks until the test releases it with the mark it
// "read". That holds a stopped follower in its unwind for as long as a test needs, which
// is the window #85 is about and which no test can steer through a real follower.

// followCall is one follower the set launched.
type followCall struct {
	id, name string
	mark     dockerMark // the mark it was started from
	ctx      context.Context
	release  chan dockerMark // the mark it returns as it exits
}

type fakeFollows struct{ calls chan *followCall }

func newFakeFollows() *fakeFollows { return &fakeFollows{calls: make(chan *followCall, 16)} }

func (ff *fakeFollows) follow(ctx context.Context, id, name string, mark dockerMark) dockerMark {
	c := &followCall{id: id, name: name, mark: mark, ctx: ctx, release: make(chan dockerMark)}
	ff.calls <- c
	return <-c.release
}

// next returns the next follower launched.
func (ff *fakeFollows) next(t *testing.T) *followCall {
	t.Helper()
	select {
	case c := <-ff.calls:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("no follower was launched")
		return nil
	}
}

// none asserts that no follower is launched within d.
func (ff *fakeFollows) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case c := <-ff.calls:
		t.Fatalf("a follower was launched for %s (%q) from %+v; want none", c.id, c.name, c.mark)
	case <-time.After(d):
	}
}

// waitReturns asserts that s.wait returns within 2s.
func waitReturns(t *testing.T, s *followerSet) {
	t.Helper()
	done := make(chan struct{})
	go func() { s.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not return")
	}
}

func markAt(ms int) dockerMark { return dockerMark{stdout: streamMark{at: at(ms), n: 1}} }

// TestFollowerSetIgnoresStartWhileRunning pins decision 1: only a STOPPED follower takes a
// pending start. A start for one still running — a duplicate event, or the overlap
// between subscribing and listing at startup — attaches nothing and leaves nothing
// behind, whether that follower later ends on its own or is stopped.
func TestFollowerSetIgnoresStartWhileRunning(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		name := "ends on its own"
		if stopped {
			name = "stopped after the duplicate"
		}
		t.Run(name, func(t *testing.T) {
			ff := newFakeFollows()
			s := newFollowerSet(context.Background(), ff.follow)
			s.start("c1", "app")
			c := ff.next(t)
			s.start("c1", "app")
			ff.none(t, 50*time.Millisecond)
			if stopped {
				s.stop("c1")
			}
			c.release <- markAt(5)
			ff.none(t, 200*time.Millisecond)
			waitReturns(t, s)
		})
	}
}

// TestFollowerSetRestartsFromTheExitMark pins decision 2: a start that arrives while the
// stopped follower is unwinding is served when it exits, from the mark it stores on the
// way out — not from whatever was stored when the start arrived (nothing, here).
func TestFollowerSetRestartsFromTheExitMark(t *testing.T) {
	ff := newFakeFollows()
	s := newFollowerSet(context.Background(), ff.follow)
	s.start("c1", "app")
	old := ff.next(t)
	s.stop("c1")
	if old.ctx.Err() == nil {
		t.Fatal("stop did not cancel the follower")
	}
	s.start("c1", "app")
	ff.none(t, 50*time.Millisecond) // nothing runs beside the old follower

	old.release <- markAt(5)
	next := ff.next(t)
	if next.id != "c1" || next.mark != markAt(5) {
		t.Errorf("successor = %s from %+v, want c1 from the exit mark %+v", next.id, next.mark, markAt(5))
	}
	next.release <- markAt(9)
	ff.none(t, 100*time.Millisecond)
	waitReturns(t, s)
}

// TestFollowerSetKeepsPendingAcrossASecondDie pins decision 3: die, start, die, all before
// the old follower exits. The second die finds the old follower, already cancelled, and
// changes nothing, so the pending start is kept: its successor reads, from the old mark,
// what the container logged between the two dies. The container is stopped by then, so
// the daemon sends that history and ends the stream, and the successor exits on its own.
// A third start while the old follower is still unwinding only updates the name.
func TestFollowerSetKeepsPendingAcrossASecondDie(t *testing.T) {
	for _, tt := range []struct {
		name   string
		events func(s *followerSet)
		want   string
	}{
		{"second die", func(s *followerSet) { s.stop("c1"); s.start("c1", "second"); s.stop("c1") }, "second"},
		{"third start", func(s *followerSet) {
			s.stop("c1")
			s.start("c1", "second")
			s.stop("c1")
			s.start("c1", "third")
		}, "third"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ff := newFakeFollows()
			s := newFollowerSet(context.Background(), ff.follow)
			s.start("c1", "first")
			old := ff.next(t)
			tt.events(s)
			old.release <- markAt(5)

			next := ff.next(t)
			if next.name != tt.want || next.mark != markAt(5) {
				t.Errorf("successor = %q from %+v, want %q from %+v", next.name, next.mark, tt.want, markAt(5))
			}
			if next.ctx.Err() != nil {
				t.Error("the successor started cancelled; it must read the history the daemon still has")
			}
			next.release <- markAt(7)
			ff.none(t, 100*time.Millisecond) // exactly one successor
			waitReturns(t, s)
		})
	}
}

// TestFollowerSetShutdown pins decision 4. Once Lines' ctx is cancelled a pending start is
// never served, and wait still returns. And a successor launched from inside an exiting
// follower is counted before that follower is: a wait already in progress covers it.
func TestFollowerSetShutdown(t *testing.T) {
	t.Run("no successor after cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ff := newFakeFollows()
		s := newFollowerSet(ctx, ff.follow)
		s.start("c1", "app")
		old := ff.next(t)
		s.stop("c1")
		s.start("c1", "app")
		cancel()
		old.release <- markAt(5)
		ff.none(t, 200*time.Millisecond)
		waitReturns(t, s)
	})

	t.Run("wait spans the successor", func(t *testing.T) {
		ff := newFakeFollows()
		s := newFollowerSet(context.Background(), ff.follow)
		s.start("c1", "app")
		old := ff.next(t)
		s.stop("c1")
		s.start("c1", "app")
		done := make(chan struct{})
		go func() { s.wait(); close(done) }()

		old.release <- markAt(5)
		next := ff.next(t)
		select {
		case <-done:
			t.Fatal("wait returned while the successor was still running")
		case <-time.After(50 * time.Millisecond):
		}
		next.release <- markAt(6)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("wait did not return once the successor exited")
		}
	})
}

// TestFollowerSetUsesTheStartName pins decision 5: the successor takes the name the start
// event carried, so a container renamed across its restart is sourced by its new name.
func TestFollowerSetUsesTheStartName(t *testing.T) {
	ff := newFakeFollows()
	s := newFollowerSet(context.Background(), ff.follow)
	s.start("c1", "old-name")
	old := ff.next(t)
	s.stop("c1")
	s.start("c1", "new-name")
	old.release <- markAt(5)
	next := ff.next(t)
	if next.name != "new-name" {
		t.Errorf("successor name = %q, want the start event's %q", next.name, "new-name")
	}
	next.release <- markAt(6)
	waitReturns(t, s)
}
