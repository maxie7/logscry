// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"fmt"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"

	"github.com/maxie7/logscry/internal/model"
)

// The tests in this file are #100: the daemon's event stream breaks, and the Docker source
// reconnects rather than ending. They drive DockerSource.Lines against the fake daemon in
// docker_daemon_test.go, cut either the events stream alone (endEvents, a proxy dropping the
// idle connection) or everything (fakeProxy.down, the daemon unreachable).

// reconnects waits for logscry's next events subscription and returns its query. It fails
// at once if Lines returns instead: the Docker source ended.
func (f *follow) reconnects(d *fakeDaemon) url.Values {
	f.t.Helper()
	select {
	case q := <-d.evReqs:
		return q
	case err := <-f.done:
		f.t.Fatalf("Lines returned %v; want it to re-subscribe and keep following", err)
	case <-time.After(5 * time.Second):
		f.t.Fatal("no events re-subscription within 5s")
	}
	return nil
}

// TestDockerEventsCloseOnceReconnects replaces #97's TestDockerEventsErrorEndsTheSource: the
// events stream ends while a container is followed and its log stream stays up. The source
// re-subscribes from the last event it saw, re-attaches the container from its mark, and
// every line, those logged in the gap included, arrives exactly once.
func TestDockerEventsCloseOnceReconnects(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(fmt.Sprintf("tty=%v", tty), func(t *testing.T) {
			before, beforeRaws := lines("line", 1, 3, 0, out)
			gap, gapRaws := lines("line", 4, 6, 100, out)
			after, afterRaws := lines("line", 7, 9, 200, out)
			d := newFakeDaemon(t, "ab-events", tty, before...)
			f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-events$"}))

			d.nextEventsRequest()
			expectQuery(t, "first attach", d.nextRequest(), "", "100")
			f.until(beforeRaws...)
			// A duplicate start is the last event seen: the re-subscription asks from it.
			d.event(events.ActionStart)
			last := d.lastEventTime()
			time.Sleep(50 * time.Millisecond) // let Lines take it before the stream ends
			d.endEvents()
			// Every follower is stopped on the loss: only once this one has hung up is a
			// line logged in the gap, and read by the re-attach from the mark.
			select {
			case <-d.gone:
			case <-time.After(5 * time.Second):
				t.Fatal("the follower did not hang up after the events stream ended")
			}
			d.logs(gap...)

			if got := f.reconnects(d).Get("since"); got != wireSince(last) {
				t.Errorf("re-subscribe since = %q, want the last event's %q", got, wireSince(last))
			}
			expectQuery(t, "re-attach", d.nextRequest(), wireSince(at(3)), wireTailAll)
			f.until(gapRaws...)
			d.logs(after...)
			f.until(afterRaws...)
			f.stop()

			f.expect(model.Stdout, append(append(beforeRaws, gapRaws...), afterRaws...)...)
		})
	}
}

// TestDockerStartAndDieInsideTheGap: a container starts, logs and exits while the events
// stream is down. Its start and die reach logscry only through the re-subscription's
// replay, back to back. The die must not cut the follower the start launched: the container
// is already stopped, so the daemon sends its log and ends the stream on its own.
func TestDockerStartAndDieInsideTheGap(t *testing.T) {
	history, historyRaws := lines("steady", 1, 2, 0, out)
	blip, blipRaws := lines("blip", 1, 5, 100, out)
	d := newFakeDaemon(t, "ab-steady", false, history...)
	d.add("b10", "ab-blip", false)
	f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-"}))

	d.nextEventsRequest()
	f.until(historyRaws...)
	d.endEvents()
	d.run("b10", true, blip...)

	f.reconnects(d)
	f.until(blipRaws...)
	f.stop()

	f.expect(model.Stdout, append(historyRaws, blipRaws...)...)
	if n := len(d.logRequests("b10")); n != 1 {
		t.Errorf("ab-blip was attached %d times, want once", n)
	}
}

// TestDockerFullCutKeepsEveryLineOnce: the daemon is unreachable for a moment, which cuts the
// events stream and every log stream together, as killing a proxy in front of the socket
// does. A container that keeps running across the gap delivers each of its lines once, the
// gap's included; one started during the gap and still running is attached by the re-list.
func TestDockerFullCutKeepsEveryLineOnce(t *testing.T) {
	before, beforeRaws := lines("steady", 1, 3, 0, out)
	gap, gapRaws := lines("steady", 4, 6, 100, out)
	after, afterRaws := lines("steady", 7, 9, 200, out)
	late, lateRaws := lines("late", 1, 3, 150, out)
	d := newFakeDaemon(t, "ab-steady", false, before...)
	d.add("c20", "ab-late", false)
	p := d.proxy()
	f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-"}))

	d.nextEventsRequest()
	expectQuery(t, "first attach", d.nextRequest(), "", "100")
	f.until(beforeRaws...)
	p.down()
	d.logs(gap...)
	d.run("c20", false, late...)
	time.Sleep(200 * time.Millisecond) // shorter than the first retry
	p.up()

	f.reconnects(d)
	expectQuery(t, "re-attach", d.nextRequest(), wireSince(at(3)), wireTailAll)
	f.until(append(gapRaws, lateRaws...)...)
	d.logs(after...)
	f.until(afterRaws...)
	f.stop()

	got := map[string][]string{}
	for _, ll := range f.got {
		got[ll.Source] = append(got[ll.Source], ll.Raw)
	}
	for src, want := range map[string][]string{
		"docker:ab-steady": append(append(beforeRaws, gapRaws...), afterRaws...),
		"docker:ab-late":   lateRaws,
	} {
		if fmt.Sprint(got[src]) != fmt.Sprint(want) {
			t.Errorf("%s delivered %q, want each line exactly once: %q", src, got[src], want)
		}
	}
}

// fast shortens src's reconnect waits for a test: 10ms, doubling to 80ms.
func fast(src *DockerSource) *DockerSource {
	src.retryMin, src.retryMax = 10*time.Millisecond, 80*time.Millisecond
	return src
}

// notice waits for the source's next notice and returns its text.
func (f *follow) notice() string {
	f.t.Helper()
	select {
	case err := <-f.notices:
		return err.Error()
	case <-time.After(5 * time.Second):
		f.t.Fatal("no notice within 5s")
		return ""
	}
}

// reconnected skips the notices of further failed attempts and returns the first other one.
func (f *follow) reconnected() string {
	f.t.Helper()
	for {
		if n := f.notice(); !strings.HasPrefix(n, lostPrefix) {
			return n
		}
	}
}

const lostPrefix = "ingest: docker: events lost, reconnecting "

var lostNotice = regexp.MustCompile(`^ingest: docker: events lost, reconnecting \(attempt (\d+), next in (\S+)\): (.+)$`)

// expectLost asserts that n says the stream was lost, at attempt, and returns its delay and
// cause.
func expectLost(t *testing.T, n string, attempt int) (delay, cause string) {
	t.Helper()
	m := lostNotice.FindStringSubmatch(n)
	if m == nil || m[1] != strconv.Itoa(attempt) {
		t.Errorf("notice %q, want the events lost at attempt %d", n, attempt)
		return "", ""
	}
	return m[2], m[3]
}

func reattached(n int) string {
	return fmt.Sprintf("ingest: docker: events reconnected, %d container(s) re-attached", n)
}

// hungUp waits for a follower to hang up.
func (d *fakeDaemon) hungUp() {
	d.t.Helper()
	select {
	case <-d.gone:
	case <-time.After(5 * time.Second):
		d.t.Fatal("no follower hung up")
	}
}

// TestDockerEventsCloseRepeatedly: the events stream ends five times. Each loss is said,
// as attempt 1 of its own outage, and each reconnect; the container is attached once per
// connection, from its mark, and every line arrives exactly once.
func TestDockerEventsCloseRepeatedly(t *testing.T) {
	history, want := lines("line", 1, 2, 0, out)
	d := newFakeDaemon(t, "ab-flap", false, history...)
	f := startFollow(t, fast(NewDockerSource(DockerSelector{NameRegex: "^ab-flap$"})))

	d.nextEventsRequest()
	f.until(want...)
	for i := 1; i <= 5; i++ {
		d.endEvents()
		d.hungUp()
		gap, gapRaws := lines("line", 10*i, 10*i+2, 100*i, out)
		d.logs(gap...)
		f.reconnects(d)
		if _, cause := expectLost(t, f.notice(), 1); cause != "events: EOF" {
			t.Errorf("loss %d: cause %q, want %q", i, cause, "events: EOF")
		}
		if got := f.notice(); got != reattached(1) {
			t.Errorf("loss %d: notice %q, want %q", i, got, reattached(1))
		}
		f.until(gapRaws...)
		want = append(want, gapRaws...)
	}
	f.stop()

	f.expect(model.Stdout, want...)
	if n := len(d.logRequests(fakeID)); n != 6 {
		t.Errorf("the container was attached %d times, want once per connection: 6", n)
	}
}

// TestDockerUnreachableForSeveralAttempts: the daemon refuses connections for five
// attempts, each said with its wait, doubling to the cap. Then it is back: the reconnect is
// said with the containers it re-attached, the one running across the outage delivers each
// of its lines once, and the one started during it is attached from its first line.
func TestDockerUnreachableForSeveralAttempts(t *testing.T) {
	before, beforeRaws := lines("steady", 1, 3, 0, out)
	gap, gapRaws := lines("steady", 4, 6, 100, out)
	late, lateRaws := lines("late", 1, 2, 150, out)
	d := newFakeDaemon(t, "ab-steady", false, before...)
	d.add("c20", "ab-late", false)
	p := d.proxy()
	f := startFollow(t, fast(NewDockerSource(DockerSelector{NameRegex: "^ab-"})))

	d.nextEventsRequest()
	d.nextRequest()
	f.until(beforeRaws...)
	p.down()
	d.logs(gap...)
	d.run("c20", false, late...)
	for i, want := range []string{"10ms", "20ms", "40ms", "80ms", "80ms"} {
		delay, cause := expectLost(t, f.notice(), i+1)
		if delay != want {
			t.Errorf("attempt %d: next in %s, want %s", i+1, delay, want)
		}
		// The subscription is refused, and is what the attempt says: not the list after it.
		if i > 0 && !strings.HasPrefix(cause, "events: Cannot connect to the Docker daemon") {
			t.Errorf("attempt %d: cause %q, want the refused subscription", i+1, cause)
		}
	}
	p.up()

	f.reconnects(d)
	if got := f.reconnected(); got != reattached(2) {
		t.Errorf("notice %q, want %q", got, reattached(2))
	}
	f.until(append(gapRaws, lateRaws...)...)
	f.stop()

	got := map[string][]string{}
	for _, ll := range f.got {
		got[ll.Source] = append(got[ll.Source], ll.Raw)
	}
	if want := append(beforeRaws, gapRaws...); !slices.Equal(got["docker:ab-steady"], want) {
		t.Errorf("ab-steady delivered %q, want each line exactly once: %q", got["docker:ab-steady"], want)
	}
	if !slices.Equal(got["docker:ab-late"], lateRaws) {
		t.Errorf("ab-late delivered %q, want %q", got["docker:ab-late"], lateRaws)
	}
}

// TestDockerCancelDuringBackoff: a cancel during the wait between attempts ends Lines at
// once, cleanly, and nothing more is said.
func TestDockerCancelDuringBackoff(t *testing.T) {
	d := newFakeDaemon(t, "ab-wait", false)
	p := d.proxy()
	src := NewDockerSource(DockerSelector{NameRegex: "^ab-wait$"})
	src.retryMin, src.retryMax = time.Hour, time.Hour
	f := startFollow(t, src)

	d.nextEventsRequest()
	d.nextRequest()
	p.down()
	if delay, _ := expectLost(t, f.notice(), 1); delay != "1h0m0s" {
		t.Errorf("next in %s, want 1h0m0s", delay)
	}
	start := time.Now()
	f.stop()
	if el := time.Since(start); el > time.Second {
		t.Errorf("Lines took %v to return after the cancel, want well under 1s", el)
	}
	select {
	case n := <-f.notices:
		t.Errorf("notice %v after the cancel, want none", n)
	default:
	}
}

// dockerGoroutines counts the goroutines running the Docker source's own code (Lines, its
// followers and their readers) or the client's events subscription. Counting by stack
// leaves out httptest's and the transport's, which come and go on their own.
func dockerGoroutines() (int, string) {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var n int
	var ours []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "/internal/ingest/docker.go:") || strings.Contains(g, "/internal/ingest/reader.go:") ||
			strings.Contains(g, "moby/moby/client.(*Client).Events") {
			n++
			ours = append(ours, g)
		}
	}
	return n, strings.Join(ours, "\n\n")
}

// settlesAt polls until dockerGoroutines reports want.
func settlesAt(t *testing.T, label string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, stacks := dockerGoroutines()
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d Docker-source goroutines, want %d:\n%s", label, n, want, stacks)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDockerReconnectLeaksNothing is #119's property across reconnects: each connection's
// followers, readers and subscription are gone before the next is made, so after every
// reconnect the source runs exactly what one connection runs, and after the cancel nothing.
func TestDockerReconnectLeaksNothing(t *testing.T) {
	history, raws := lines("line", 1, 2, 0, out)
	d := newFakeDaemon(t, "ab-leak", false, history...)
	f := startFollow(t, fast(NewDockerSource(DockerSelector{NameRegex: "^ab-leak$"})))

	d.nextEventsRequest()
	d.nextRequest()
	f.until(raws...)
	time.Sleep(100 * time.Millisecond)
	steady, _ := dockerGoroutines()
	if steady == 0 {
		t.Fatal("no Docker-source goroutines while following: the count sees nothing")
	}
	for i := 1; i <= 5; i++ {
		d.endEvents()
		f.reconnects(d)
		d.nextRequest()
		raw := fmt.Sprintf("after %d", i)
		d.logs(out(at(100*i), raw))
		f.until(raw)
		settlesAt(t, fmt.Sprintf("after reconnect %d", i), steady)
	}
	f.stop()
	settlesAt(t, "after the cancel", 0)
}

// TestDockerReplayedEventsAreHandledOnce: the re-subscription's since is inclusive, so the
// last event handled before the loss, a die here, comes back first. It must be skipped. Were
// it applied again, it would retire the follower the re-list attached, and the restart the
// replay carries next would then launch a successor on the live die that follows.
func TestDockerReplayedEventsAreHandledOnce(t *testing.T) {
	history, historyRaws := lines("line", 1, 2, 0, out)
	gap, gapRaws := lines("line", 3, 4, 100, out)
	d := newFakeDaemon(t, "ab-replay", false, history...)
	f := startFollow(t, fast(NewDockerSource(DockerSelector{NameRegex: "^ab-replay$"})))

	d.nextEventsRequest()
	d.nextRequest()
	f.until(historyRaws...)
	d.die() // handled: the follower has hung up
	lastDie := d.lastEventTime()
	d.endEvents()
	d.logs(gap...)
	d.event(events.ActionStart) // restarted in the gap

	if got := f.reconnects(d).Get("since"); got != wireSince(lastDie) {
		t.Errorf("re-subscribe since = %q, want the die's %q", got, wireSince(lastDie))
	}
	expectQuery(t, "re-attach", d.nextRequest(), wireSince(at(2)), wireTailAll)
	f.until(gapRaws...)
	d.die()
	select {
	case q := <-d.reqs:
		t.Fatalf("a logs request %v after the live die: a replayed event was applied twice", q)
	case <-time.After(300 * time.Millisecond):
	}
	f.stop()

	f.expect(model.Stdout, append(historyRaws, gapRaws...)...)
}

// TestDockerGapOutrunsTheEventBuffer pins what stays lost: the daemon replays only its
// newest events, of every kind. Here it keeps 2, and five healthcheck-like events follow a
// container that started and died in the gap, so its start is gone from the buffer and its
// lines are never read. The container running across the gap is re-listed all the same.
func TestDockerGapOutrunsTheEventBuffer(t *testing.T) {
	history, historyRaws := lines("steady", 1, 2, 0, out)
	after, afterRaws := lines("steady", 3, 4, 200, out)
	blip, _ := lines("blip", 1, 3, 100, out)
	d := newFakeDaemon(t, "ab-steady", false, history...)
	d.add("b10", "ab-blip", false)
	d.setEventLimit(2)
	f := startFollow(t, fast(NewDockerSource(DockerSelector{NameRegex: "^ab-"})))

	d.nextEventsRequest()
	f.until(historyRaws...)
	d.endEvents()
	d.hungUp()
	d.run("b10", true, blip...)
	d.noise(5)

	f.reconnects(d)
	if got := f.reconnected(); got != reattached(1) {
		t.Errorf("notice %q, want %q", got, reattached(1))
	}
	d.logs(after...)
	f.until(afterRaws...)
	time.Sleep(100 * time.Millisecond)
	f.stop()

	f.expect(model.Stdout, append(historyRaws, afterRaws...)...)
	if n := len(d.logRequests("b10")); n != 0 {
		t.Errorf("ab-blip was attached %d times; its start had left the buffer", n)
	}
}

// TestDockerFailedAttemptClosesItsSubscription is #98's list half, per attempt: an attempt
// whose list fails has opened an events request, and closes it before the wait for the
// next, rather than leaving one open per attempt.
func TestDockerFailedAttemptClosesItsSubscription(t *testing.T) {
	d := newFakeDaemon(t, "ab-list", false)
	src := NewDockerSource(DockerSelector{NameRegex: "^ab-list$"})
	src.retryMin, src.retryMax = 500*time.Millisecond, 500*time.Millisecond
	f := startFollow(t, src)

	d.nextEventsRequest()
	d.nextRequest()
	d.failList()
	d.endEvents()
	expectLost(t, f.notice(), 1)
	f.reconnects(d)
	if _, cause := expectLost(t, f.notice(), 2); !strings.HasPrefix(cause, "list: ") {
		t.Errorf("attempt 2: cause %q, want the list's error", cause)
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for d.openEvents() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d events request(s) still open during the wait after a failed attempt", d.openEvents())
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.stop()
}

// TestDockerReconnectsWhileNoticesGoUnread: nobody reads the notices, as when --plain is
// stuck writing stdout or the TUI waits on its terminal. The source must not wait for them:
// it keeps retrying, each notice past the room is dropped and counted, and once the daemon
// answers again it follows on.
func TestDockerReconnectsWhileNoticesGoUnread(t *testing.T) {
	history, historyRaws := lines("line", 1, 2, 0, out)
	d := newFakeDaemon(t, "ab-unread", false, history...)
	unread := make(chan error, 2)
	n := NewNotices(unread, cap(unread))
	f := startFollowWith(t, fast(NewDockerSource(DockerSelector{NameRegex: "^ab-unread$"})), unread, n)

	d.nextEventsRequest()
	d.nextRequest()
	f.until(historyRaws...)
	d.failList()
	d.endEvents()
	for range 10 {
		f.reconnects(d)
	}
	if got := n.Dropped(); got < 8 {
		t.Errorf("%d notice(s) dropped after 10 attempts with room for 2; want at least 8", got)
	}
	// The attempt in flight may be the one that succeeds, so the reconnect is seen by its
	// re-attach rather than by a further subscription.
	d.healList()
	d.nextRequest()
	d.logs(out(at(100), "after"))
	f.until("after")
	f.stop()

	f.expect(model.Stdout, append(historyRaws, "after")...)
}

// TestDockerFlappingStreamKeepsBackingOff: a stream that drops as soon as it is back does not
// restart the wait at retryMin. Only a connection that stayed up for retryMax does, so a
// flapping proxy cannot make the source retry and notify at the fastest rate for ever.
func TestDockerFlappingStreamKeepsBackingOff(t *testing.T) {
	d := newFakeDaemon(t, "ab-flap", false)
	p := d.proxy()
	src := NewDockerSource(DockerSelector{NameRegex: "^ab-flap$"})
	src.retryMin, src.retryMax = 50*time.Millisecond, time.Minute
	f := startFollow(t, src)

	d.nextEventsRequest()
	d.nextRequest()
	p.down()
	for i, want := range []string{"50ms", "100ms", "200ms"} {
		if delay, _ := expectLost(t, f.notice(), i+1); delay != want {
			t.Errorf("attempt %d: next in %s, want %s", i+1, delay, want)
		}
	}
	p.up()
	if got := f.reconnected(); got != reattached(1) {
		t.Errorf("notice %q, want %q", got, reattached(1))
	}
	d.endEvents()
	if delay, _ := expectLost(t, f.notice(), 1); delay != "400ms" {
		t.Errorf("after a connection that dropped at once: next in %s, want the doubled 400ms", delay)
	}
	f.stop()
}
