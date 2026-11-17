// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"

	"github.com/maxie7/logscry/internal/model"
)

// The tests in this file are #165: the Docker source remembers where each container's
// followers stopped reading, so that a restart resumes there, and it must forget that once the
// container is destroyed and can never restart. They drive DockerSource.Lines against the fake
// daemon in docker_daemon_test.go and count the marks it kept once it has returned.

// delivers reports whether an events subscription with query q is sent m, by the filters the
// client put on it, as the daemon applies them: every term present must match, and a term
// absent matches anything.
func delivers(q url.Values, m events.Message) bool {
	var f map[string]map[string]bool
	if s := q.Get("filters"); s != "" {
		if err := json.Unmarshal([]byte(s), &f); err != nil {
			return false
		}
	}
	for term, value := range map[string]string{"type": string(m.Type), "event": string(m.Action)} {
		if want, ok := f[term]; ok && !want[value] {
			return false
		}
	}
	return true
}

// remove destroys container id while its streams stay open, as the daemon does once `docker
// rm` has taken it: it is gone from the list, inspect and logs, and a destroy event is
// published. It returns what ends the streams it leaves open.
func (d *fakeDaemon) remove(id string) (endStreams func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.containers[id]
	delete(d.containers, id)
	d.publishLocked(events.Message{
		Type:   events.ContainerEventType,
		Action: events.ActionDestroy,
		Actor:  events.Actor{ID: id, Attributes: map[string]string{"name": c.name}},
	})
	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		c.gen++
		c.stopLen = len(c.log)
		d.notifyLocked()
	}
}

// destroy removes a stopped container id: its streams have ended, and it is gone.
func (d *fakeDaemon) destroy(id string) {
	d.endStreamsOf(id)
	d.remove(id)
}

// failAllList makes a list of every container fail with a 500, while the running list works.
func (d *fakeDaemon) failAllList() {
	d.mu.Lock()
	d.allListFails = true
	d.mu.Unlock()
}

// handled starts a fresh container the selector matches and waits for its logs request. Lines
// handles events in order, so every event published before its start has been handled. Call it
// once the connection has listed, or the list may attach it instead of the event.
func (d *fakeDaemon) handled(id, name string) {
	d.t.Helper()
	d.add(id, name, false)
	d.eventFor(id, events.ActionStart)
	deadline := time.Now().Add(5 * time.Second)
	for len(d.logRequests(id)) == 0 {
		if time.Now().After(deadline) {
			d.t.Fatalf("no logs request for %s within 5s of its start", name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// hexID returns a container ID of the daemon's shape: 64 hex digits.
func hexID(i int) string { return fmt.Sprintf("%064x", i) }

// withState gives src a state of its own, to count its marks once Lines has returned.
func withState(src *DockerSource) *DockerSource {
	src.state = &dockerState{marks: map[string]dockerMark{}}
	return src
}

// markIDs returns the IDs src holds a mark for, sorted. Call it only after f.stop: Lines has
// returned, so every follower has stored its mark.
func markIDs(src *DockerSource) []string { return slices.Sorted(maps.Keys(src.state.marks)) }

// TestDockerDestroyedContainersLeaveNoMarks is #165's shape: containers that start, log, die
// and are destroyed, as `docker run --rm` does, one after another. Each one's position is
// useless once it is gone, so none may be kept.
func TestDockerDestroyedContainersLeaveNoMarks(t *testing.T) {
	const n = 20
	d := newFakeDaemon(t, "ab-steady", false)
	src := withState(NewDockerSource(DockerSelector{NameRegex: "^churn-"}))
	f := startFollow(t, src)

	d.nextEventsRequest()
	for i := range n {
		id := hexID(i + 1)
		d.add(id, fmt.Sprintf("churn-%02d", i), false)
		es, raws := lines(fmt.Sprintf("churn %d", i), 1, 2, 10*i, out)
		d.run(id, false, es...)
		f.until(raws...)
		d.endStreamsOf(id)
		d.eventFor(id, events.ActionDie)
		d.destroy(id)
	}
	d.handled("b20", "churn-barrier")
	f.stop()

	got := markIDs(src)
	if kept := slices.DeleteFunc(slices.Clone(got), func(id string) bool { return id == "b20" }); len(kept) != 0 {
		t.Errorf("%d of %d destroyed containers kept a mark; want none", len(kept), n)
	}
	if !slices.Contains(got, "b20") {
		t.Errorf("marks %v, want the running barrier's kept", got)
	}
}

// TestDockerDestroyWhileReading forces the order #165 must survive: the destroy is handled
// while the container's follower is still reading, and the follower exits only after it. The
// exiting follower must not store the mark the destroy forgot.
func TestDockerDestroyWhileReading(t *testing.T) {
	history, raws := lines("line", 1, 3, 0, out)
	d := newFakeDaemon(t, "ab-gone", false, history...)
	src := withState(NewDockerSource(DockerSelector{NameRegex: "^ab-"}))
	f := startFollow(t, src)

	d.nextEventsRequest()
	d.nextRequest()
	f.until(raws...)
	end := d.remove(fakeID)
	d.handled("b20", "ab-barrier") // the destroy is handled; the follower is still reading
	end()
	f.stop()

	if got := markIDs(src); !slices.Equal(got, []string{"b20"}) {
		t.Errorf("marks %v, want only the running barrier's: the destroyed container's came back", got)
	}
	f.expect(model.Stdout, raws...)
}

// TestDockerDestroyInTheGapIsReplayed: a container dies, then is destroyed while the events
// stream is down. The re-subscription's replay carries the destroy, and its mark goes.
func TestDockerDestroyInTheGapIsReplayed(t *testing.T) {
	history, raws := lines("line", 1, 3, 0, out)
	d := newFakeDaemon(t, "ab-gone", false, history...)
	src := withState(fast(NewDockerSource(DockerSelector{NameRegex: "^ab-"})))
	f := startFollow(t, src)

	d.nextEventsRequest()
	d.nextRequest()
	f.until(raws...)
	d.die()
	d.endEvents()
	d.destroy(fakeID)
	f.reconnects(d)
	if got := f.reconnected(); got != reattached(0) {
		t.Errorf("notice %q, want %q", got, reattached(0))
	}
	d.handled("b20", "ab-barrier")
	f.stop()

	if got := markIDs(src); !slices.Equal(got, []string{"b20"}) {
		t.Errorf("marks %v, want only the running barrier's: the gap's destroy was not applied", got)
	}
}

// TestDockerDestroyOutrunsTheEventBuffer: as above, but the daemon's replay buffer has moved on
// past the destroy, as TestDockerGapOutrunsTheEventBuffer has it move past a start. The replay
// never carries it, so only the reconnect's list of every container can tell it is gone.
func TestDockerDestroyOutrunsTheEventBuffer(t *testing.T) {
	history, raws := lines("line", 1, 3, 0, out)
	d := newFakeDaemon(t, "ab-gone", false, history...)
	d.setEventLimit(2)
	src := withState(fast(NewDockerSource(DockerSelector{NameRegex: "^ab-"})))
	f := startFollow(t, src)

	d.nextEventsRequest()
	d.nextRequest()
	f.until(raws...)
	d.die()
	d.endEvents()
	d.destroy(fakeID)
	d.noise(5)
	f.reconnects(d)
	if got := f.reconnected(); got != reattached(0) {
		t.Errorf("notice %q, want %q", got, reattached(0))
	}
	d.handled("b20", "ab-barrier")
	f.stop()

	if got := markIDs(src); !slices.Equal(got, []string{"b20"}) {
		t.Errorf("marks %v, want only the running barrier's: a container destroyed in the gap kept its mark", got)
	}
}

// stoppedAcrossAReconnect is the restart that #98 and #100 keep exact, across a reconnect: a
// container dies and is not destroyed, the events stream is lost and comes back, and the
// container restarts. It still exists, so it may restart, and its mark must survive the
// reconnect: the re-attach resumes after the last line read, and every line arrives once.
func stoppedAcrossAReconnect(t *testing.T, before func(d *fakeDaemon)) {
	first, firstRaws := lines("line", 1, 3, 0, out)
	second, secondRaws := lines("line", 4, 6, 100, out)
	d := newFakeDaemon(t, "ab-stopped", false, first...)
	src := withState(fast(NewDockerSource(DockerSelector{NameRegex: "^ab-stopped$"})))
	f := startFollow(t, src)

	d.nextEventsRequest()
	expectQuery(t, "first attach", d.nextRequest(), "", "100")
	f.until(firstRaws...)
	d.die()
	before(d)
	d.endEvents()
	f.reconnects(d)
	if got := f.reconnected(); got != reattached(0) {
		t.Errorf("notice %q, want %q", got, reattached(0))
	}
	d.logs(second...)
	expectQuery(t, "re-attach", d.start(), wireSince(at(3)), wireTailAll)
	f.until(secondRaws...)
	f.stop()

	f.expect(model.Stdout, append(firstRaws, secondRaws...)...)
	if got := markIDs(src); !slices.Equal(got, []string{fakeID}) {
		t.Errorf("marks %v, want the stopped container's own", got)
	}
}

// TestDockerStoppedContainerKeepsItsMark: a stopped container is listed among every container,
// so the reconnect keeps its mark.
func TestDockerStoppedContainerKeepsItsMark(t *testing.T) {
	stoppedAcrossAReconnect(t, func(*fakeDaemon) {})
}

// TestDockerFailedReconcileKeepsTheMarks: if the list of every container fails, the reconnect
// cannot tell which containers are gone, so it keeps every mark, as before #165, and follows on.
func TestDockerFailedReconcileKeepsTheMarks(t *testing.T) {
	stoppedAcrossAReconnect(t, (*fakeDaemon).failAllList)
}
