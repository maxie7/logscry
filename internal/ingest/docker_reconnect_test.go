// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"fmt"
	"net/url"
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
