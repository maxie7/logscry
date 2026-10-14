// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/maxie7/logscry/internal/model"
)

// The tests in this file drive DockerSource.Lines end to end against a fake daemon:
// an httptest server that DOCKER_HOST points at, so client.FromEnv talks to it with
// no production seam. It plays /_ping, /events, /containers/json,
// /containers/{id}/json and /containers/{id}/logs, and records every logs request's
// query so a test can check what reached the wire.
//
// The logs endpoint emulates the daemon's read path as of docker v28.5.2
// (daemon/logs.go, daemon/logger/loggerutils/logfile.go):
//   - tail is applied FIRST, to the combined stdout+stderr log (tail 0 = no history,
//     "all" or anything non-numeric = everything);
//   - then since filters, INCLUSIVELY (a line is skipped only if stamped Before since),
//     and the filter switches off at the first line at or after since;
//   - every line carries a fixed 9-digit RFC3339Nano stamp, as with timestamps=1.
// After that history it follows live appends until the test ends the stream (the
// container stopped) or the client goes away.

const fakeID = "c0ffee"

// rfc3339NanoFixed is the daemon's wire format for a log line's stamp.
const rfc3339NanoFixed = "2006-01-02T15:04:05.000000000Z07:00"

var fakeBase = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// at returns a stamp ms milliseconds after fakeBase.
func at(ms int) time.Time { return fakeBase.Add(time.Duration(ms) * time.Millisecond) }

// wireSince is how a since instant appears in the logs request's query.
func wireSince(t time.Time) string { return fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond()) }

type fakeEntry struct {
	ts     time.Time
	stream model.Stream
	text   string
}

func out(ts time.Time, text string) fakeEntry  { return fakeEntry{ts, model.Stdout, text} }
func errl(ts time.Time, text string) fakeEntry { return fakeEntry{ts, model.Stderr, text} }

type fakeDaemon struct {
	t    *testing.T
	name string
	tty  bool

	mu      sync.Mutex
	log     []fakeEntry   // the container's log file; survives a restart
	gen     int           // bumped when the container stops: open follow streams end
	stopLen int           // len(log) at the last stop: what a stopped stream may still send
	changed chan struct{} // closed and replaced on every change to log or gen

	reqs   chan url.Values // one per logs request, sent once its history is snapshotted
	gone   chan struct{}   // one per follow stream the client hung up on
	events chan events.Message
}

func newFakeDaemon(t *testing.T, name string, tty bool, history ...fakeEntry) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{
		t:       t,
		name:    name,
		tty:     tty,
		log:     history,
		changed: make(chan struct{}),
		reqs:    make(chan url.Values, 16),
		gone:    make(chan struct{}, 16),
		events:  make(chan events.Message, 64),
	}
	srv := httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("DOCKER_API_VERSION", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	return d
}

var versionPrefix = regexp.MustCompile(`^/v[0-9.]+`)

func (d *fakeDaemon) serve(w http.ResponseWriter, r *http.Request) {
	p := versionPrefix.ReplaceAllString(r.URL.Path, "")
	switch p {
	case "/_ping":
		w.Header().Set("Api-Version", "1.47")
		w.WriteHeader(http.StatusOK)
	case "/events":
		d.serveEvents(w, r)
	case "/containers/json":
		writeJSON(w, []map[string]any{{"Id": fakeID, "Names": []string{"/" + d.name}}})
	case "/containers/" + fakeID + "/json":
		writeJSON(w, map[string]any{"Id": fakeID, "Name": "/" + d.name, "Config": map[string]any{"Tty": d.tty}})
	case "/containers/" + fakeID + "/logs":
		d.serveLogs(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (d *fakeDaemon) serveEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-d.events:
			if err := enc.Encode(m); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}
}

func (d *fakeDaemon) serveLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tail, err := strconv.Atoi(q.Get("tail"))
	if err != nil {
		tail = -1 // daemon/logs.go: a non-numeric tail ("all", "") means everything
	}
	var since time.Time
	if s := q.Get("since"); s != "" {
		sec, nsec, _ := strings.Cut(s, ".")
		si, err1 := strconv.ParseInt(sec, 10, 64)
		ni, err2 := strconv.ParseInt(nsec, 10, 64)
		if err1 != nil || err2 != nil || len(nsec) != 9 {
			d.t.Errorf("since = %q, want <sec>.<9-digit nsec>", s)
		}
		since = time.Unix(si, ni)
	}

	d.mu.Lock()
	history := d.log
	if tail >= 0 && tail < len(history) {
		history = history[len(history)-tail:]
	}
	history = slices.Clone(history)
	pos, gen := len(d.log), d.gen
	d.mu.Unlock()
	d.reqs <- q

	w.WriteHeader(http.StatusOK)
	filtering := !since.IsZero()
	for _, e := range history {
		if filtering {
			if e.ts.Before(since) {
				continue
			}
			filtering = false
		}
		d.writeEntry(w, e)
	}
	w.(http.Flusher).Flush()

	for {
		d.mu.Lock()
		end, ended := len(d.log), d.gen != gen
		if ended {
			end = max(pos, d.stopLen) // nothing logged after the stop reaches this stream
		}
		live := slices.Clone(d.log[pos:end])
		pos = end
		changed := d.changed
		d.mu.Unlock()
		for _, e := range live {
			d.writeEntry(w, e)
		}
		w.(http.Flusher).Flush()
		if ended {
			return
		}
		select {
		case <-changed:
		case <-r.Context().Done():
			d.gone <- struct{}{}
			return
		}
	}
}

func (d *fakeDaemon) writeEntry(w http.ResponseWriter, e fakeEntry) {
	line := []byte(e.ts.UTC().Format(rfc3339NanoFixed) + " " + e.text + "\n")
	if d.tty {
		_, _ = w.Write(line)
		return
	}
	st := stdcopy.Stdout
	if e.stream == model.Stderr {
		st = stdcopy.Stderr
	}
	_, _ = stdcopy.NewStdWriter(w, st).Write(line)
}

func (d *fakeDaemon) notifyLocked() {
	close(d.changed)
	d.changed = make(chan struct{})
}

// logs appends lines to the container's log file, as the container writing them.
func (d *fakeDaemon) logs(es ...fakeEntry) {
	d.mu.Lock()
	d.log = append(d.log, es...)
	d.notifyLocked()
	d.mu.Unlock()
}

// endStreams ends every open follow stream, as the daemon does when the container stops.
func (d *fakeDaemon) endStreams() {
	d.mu.Lock()
	d.gen++
	d.stopLen = len(d.log)
	d.notifyLocked()
	d.mu.Unlock()
}

func (d *fakeDaemon) event(action events.Action) {
	d.events <- events.Message{
		Type:   events.ContainerEventType,
		Action: action,
		Actor:  events.Actor{ID: fakeID, Attributes: map[string]string{"name": d.name}},
	}
}

// die stops the container while logscry is still reading it: a die event, which makes
// logscry cancel the follower, and only once the follower has hung up does the stream
// end. That pins the cancel path; TestDockerRestartBoundary takes the other order.
func (d *fakeDaemon) die() {
	d.t.Helper()
	d.event(events.ActionDie)
	select {
	case <-d.gone:
	case <-time.After(5 * time.Second):
		d.t.Fatal("the follower did not hang up on die")
	}
	d.endStreams()
}

// nextRequest waits for the next logs request and returns its query.
func (d *fakeDaemon) nextRequest() url.Values {
	d.t.Helper()
	select {
	case q := <-d.reqs:
		return q
	case <-time.After(5 * time.Second):
		d.t.Fatal("timed out waiting for a logs request")
		return nil
	}
}

// start re-starts the container and returns the query of the logs request logscry
// re-attaches with. It re-sends the start event until that request arrives: Lines
// ignores a start while the old follower is still unwinding (#85), and that
// race is not what these tests are about. A start that arrives once the new follower
// is running is ignored, as it is for a real duplicate event.
func (d *fakeDaemon) start() url.Values {
	d.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		d.event(events.ActionStart)
		select {
		case q := <-d.reqs:
			return q
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			d.t.Fatal("timed out waiting for the re-attach")
			return nil
		}
	}
}

// follow runs src.Lines in the background against the fake daemon.
type follow struct {
	t      *testing.T
	cancel context.CancelFunc
	done   chan error
	out    chan model.LogLine
	got    []model.LogLine
}

func startFollow(t *testing.T, src *DockerSource) *follow {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := &follow{t: t, cancel: cancel, done: make(chan error, 1), out: make(chan model.LogLine, 1024)}
	go func() { f.done <- src.Lines(ctx, f.out) }()
	t.Cleanup(cancel)
	return f
}

// until reads lines until every one of raws has been seen.
func (f *follow) until(raws ...string) {
	f.t.Helper()
	missing := map[string]bool{}
	for _, r := range raws {
		missing[r] = true
	}
	for _, ll := range f.got {
		delete(missing, ll.Raw)
	}
	deadline := time.After(5 * time.Second)
	for len(missing) > 0 {
		select {
		case ll := <-f.out:
			f.got = append(f.got, ll)
			delete(missing, ll.Raw)
		case <-deadline:
			f.t.Fatalf("timed out waiting for %v; got %v", keys(missing), rawsOf(f.got, -1))
		}
	}
}

// stop cancels Lines, waits for it to return and keeps whatever it delivered.
func (f *follow) stop() {
	f.t.Helper()
	f.cancel()
	select {
	case err := <-f.done:
		if err != nil {
			f.t.Errorf("Lines = %v, want nil on cancel", err)
		}
	case <-time.After(5 * time.Second):
		f.t.Fatal("Lines did not return after cancel")
	}
	for {
		select {
		case ll := <-f.out:
			f.got = append(f.got, ll)
		default:
			return
		}
	}
}

// expect asserts the exact sequence of lines delivered on one stream.
func (f *follow) expect(stream model.Stream, want ...string) {
	f.t.Helper()
	if got := rawsOf(f.got, stream); !slices.Equal(got, want) {
		f.t.Errorf("stream %v delivered %q, want each line exactly once: %q", stream, got, want)
	}
}

// rawsOf returns the Raw of every line on stream, in order (stream -1: all lines).
func rawsOf(lines []model.LogLine, stream model.Stream) []string {
	var r []string
	for _, ll := range lines {
		if stream == -1 || ll.Stream == stream {
			r = append(r, ll.Raw)
		}
	}
	return r
}

func keys(m map[string]bool) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	slices.Sort(k)
	return k
}

func expectQuery(t *testing.T, label string, q url.Values, since, tail string) {
	t.Helper()
	if got := q.Get("since"); got != since {
		t.Errorf("%s: since = %q, want %q", label, got, since)
	}
	if got := q.Get("tail"); got != tail {
		t.Errorf("%s: tail = %q, want %q", label, got, tail)
	}
}

func lines(prefix string, from, to, ms0 int, mk func(time.Time, string) fakeEntry) ([]fakeEntry, []string) {
	var es []fakeEntry
	var raws []string
	for i := from; i <= to; i++ {
		raw := fmt.Sprintf("%s %d", prefix, i)
		es = append(es, mk(at(ms0+i), raw))
		raws = append(raws, raw)
	}
	return es, raws
}

// TestDockerRestartDeliversEachLineOnce is #80's shape: a container prints five lines,
// is restarted, and prints five more. The json-file log survives the restart, so the
// re-attach must resume after the last line read rather than re-read the tail.
func TestDockerRestartDeliversEachLineOnce(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(fmt.Sprintf("tty=%v", tty), func(t *testing.T) {
			first, firstRaws := lines("ab line", 1, 5, 0, out)
			second, secondRaws := lines("ab line", 6, 10, 100, out)
			d := newFakeDaemon(t, "ab-restart", tty, first...)
			f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-restart$"}))

			expectQuery(t, "first attach", d.nextRequest(), "", "100")
			f.until(firstRaws...)
			d.die()
			d.logs(second...)
			expectQuery(t, "re-attach", d.start(), wireSince(at(5)), "all")
			f.until(secondRaws...)
			f.stop()

			f.expect(model.Stdout, append(firstRaws, secondRaws...)...)
		})
	}
}

// TestDockerRestartBoundary pins decision 1. The daemon's since is inclusive, so the
// lines stamped exactly at the remembered instant come back and must be dropped, while
// a distinct line sharing that instant must not be: one later on the same stream, one
// on the other stream. Stdout read two lines at T, stderr one line just before T, so
// the re-attach asks from stderr's (earlier) instant.
func TestDockerRestartBoundary(t *testing.T) {
	T := at(10)
	Tm := T.Add(-time.Microsecond)
	d := newFakeDaemon(t, "ab-restart", false,
		errl(Tm, "E before T"), out(T, "A at T"), out(T, "B at T"))
	f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-restart$"}))

	d.nextRequest()
	f.until("E before T", "A at T", "B at T")
	d.endStreams() // the stream ends before die this time
	d.event(events.ActionDie)
	d.logs(out(T, "C at T"), errl(T, "F at T"))
	expectQuery(t, "re-attach", d.start(), wireSince(Tm), "all")
	f.until("C at T", "F at T")
	f.stop()

	f.expect(model.Stdout, "A at T", "B at T", "C at T")
	f.expect(model.Stderr, "E before T", "F at T")
}

// TestDockerRestartReadsPastTail pins decision 2. Tail is applied before since, so any
// tail limit on a re-attach can only lose lines logged between the restart and the
// re-attach. Here five arrive in that gap with --docker-tail 3.
func TestDockerRestartReadsPastTail(t *testing.T) {
	first, firstRaws := lines("line", 1, 5, 0, out)
	second, secondRaws := lines("line", 6, 10, 100, out)
	d := newFakeDaemon(t, "ab-restart", false, first...)
	src := NewDockerSource(DockerSelector{NameRegex: "^ab-restart$"})
	src.Tail = "3"
	f := startFollow(t, src)

	expectQuery(t, "first attach", d.nextRequest(), "", "3")
	f.until(firstRaws[2:]...)
	d.die()
	d.logs(second...)
	expectQuery(t, "re-attach", d.start(), wireSince(at(5)), "all")
	f.until(secondRaws...)
	f.stop()

	f.expect(model.Stdout, append(firstRaws[2:], secondRaws...)...)
}

// TestDockerRestartAfterReadingNothing pins decision 3: a follower that read nothing
// leaves no remembered instant, and the re-attach behaves exactly as a first attach.
// It cannot duplicate anything, because nothing was counted.
func TestDockerRestartAfterReadingNothing(t *testing.T) {
	t.Run("silent container", func(t *testing.T) {
		d := newFakeDaemon(t, "ab-restart", false)
		f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-restart$"}))

		expectQuery(t, "first attach", d.nextRequest(), "", "100")
		d.die()
		later, laterRaws := lines("later", 1, 3, 100, out)
		d.logs(later...)
		expectQuery(t, "re-attach", d.start(), "", "100")
		f.until(laterRaws...)
		f.stop()

		f.expect(model.Stdout, laterRaws...)
	})

	// With --docker-tail 0 the re-attach also asks for no history. The two lines the
	// restarted container logs before the re-attach lands are therefore lost: not
	// history the user declined, but a residual loss to re-attach latency, kept because
	// it is an edge of an edge. Only the line logged once the follower is back arrives.
	t.Run("docker-tail 0", func(t *testing.T) {
		history, _ := lines("history", 1, 4, 0, out)
		d := newFakeDaemon(t, "ab-restart", false, history...)
		src := NewDockerSource(DockerSelector{NameRegex: "^ab-restart$"})
		src.Tail = "0"
		f := startFollow(t, src)

		expectQuery(t, "first attach", d.nextRequest(), "", "0")
		d.die()
		d.logs(out(at(101), "gap 1"), out(at(102), "gap 2"))
		expectQuery(t, "re-attach", d.start(), "", "0")
		d.logs(out(at(103), "live 1"))
		f.until("live 1")
		f.stop()

		f.expect(model.Stdout, "live 1")
	})
}

// TestDockerRestartCarriesTheInstantForward: a restart that reads nothing must not
// forget where the previous follower stopped, or the restart after it re-reads the tail.
func TestDockerRestartCarriesTheInstantForward(t *testing.T) {
	d := newFakeDaemon(t, "ab-restart", false, out(at(1), "line 1"), out(at(2), "line 2"))
	f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-restart$"}))

	d.nextRequest()
	f.until("line 1", "line 2")
	d.die()
	expectQuery(t, "first re-attach", d.start(), wireSince(at(2)), "all")
	d.die()
	d.logs(out(at(200), "line 3"))
	expectQuery(t, "second re-attach", d.start(), wireSince(at(2)), "all")
	f.until("line 3")
	f.stop()

	f.expect(model.Stdout, "line 1", "line 2", "line 3")
}

// TestDockerDieMidWriteIsFollowedAgain is #84's shape end to end: a container logs a burst
// faster than logscry drains it and dies mid-burst, then restarts. The follower is
// back-pressured — out is full, so the demuxer is blocked writing into a pipe — when the
// die cancels it. It must still return and remove itself, so the restart is followed, and
// it resumes from the last line that reached out: the lines it had read but not delivered
// come again, and none twice.
func TestDockerDieMidWriteIsFollowedAgain(t *testing.T) {
	burst, burstRaws := lines("burst", 1, 3000, 0, out)
	after, afterRaws := lines("after", 1, 3, 10000, out)
	d := newFakeDaemon(t, "ab-busy", false)
	f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-busy$"}))

	expectQuery(t, "first attach", d.nextRequest(), "", "100")
	d.logs(burst...)
	deadline := time.After(5 * time.Second)
	for len(f.out) < cap(f.out) {
		select {
		case <-deadline:
			t.Fatalf("out holds %d of %d lines after 5s: the follower was never back-pressured", len(f.out), cap(f.out))
		case <-time.After(time.Millisecond):
		}
	}
	d.die()
	d.logs(after...)
	expectQuery(t, "re-attach", d.start(), wireSince(at(cap(f.out))), "all")
	want := append(burstRaws, afterRaws...)
	f.until(want...)
	f.stop()

	f.expect(model.Stdout, want...)
}

// TestDockerDieAndStartBackToBack is #85: a die and the restart's start, sent exactly once
// each and back to back. Lines cancels the follower on the die and handles the start
// straight after, while the cancelled follower is still unwinding. That start must not be
// lost: the restart is followed from the mark the old follower stores on its way out, and
// every line arrives exactly once.
func TestDockerDieAndStartBackToBack(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(fmt.Sprintf("tty=%v", tty), func(t *testing.T) {
			first, firstRaws := lines("line", 1, 5, 0, out)
			second, secondRaws := lines("line", 6, 10, 100, out)
			d := newFakeDaemon(t, "ab-restart", tty, first...)
			f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-restart$"}))

			expectQuery(t, "first attach", d.nextRequest(), "", "100")
			f.until(firstRaws...)
			d.event(events.ActionDie)
			d.event(events.ActionStart)
			expectQuery(t, "re-attach", d.nextRequest(), wireSince(at(5)), "all")
			d.logs(second...)
			f.until(secondRaws...)
			f.stop()

			f.expect(model.Stdout, append(firstRaws, secondRaws...)...)
		})
	}
}

// TestDockerDuplicateStartIsIgnored: a start for a container whose follower is running —
// a duplicate event, or the overlap between subscribing and listing at startup — attaches
// nothing and leaves nothing pending, so the die that follows is not mistaken for a
// restart.
func TestDockerDuplicateStartIsIgnored(t *testing.T) {
	first, firstRaws := lines("line", 1, 3, 0, out)
	d := newFakeDaemon(t, "ab-dup", false, first...)
	f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-dup$"}))

	expectQuery(t, "first attach", d.nextRequest(), "", "100")
	d.event(events.ActionStart)
	f.until(firstRaws...)
	d.die()
	select {
	case q := <-d.reqs:
		t.Fatalf("a logs request %v arrived after a die with no start", q)
	case <-time.After(300 * time.Millisecond):
	}
	f.stop()

	f.expect(model.Stdout, firstRaws...)
}
