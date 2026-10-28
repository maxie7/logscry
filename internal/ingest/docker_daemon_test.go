// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/events"

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
//     "all", anything non-numeric or no tail at all = everything);
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

// fakeContainer is one container the fake daemon knows: its log file, which survives a
// restart, and whether it is running.
type fakeContainer struct {
	id, name string
	tty      bool
	running  bool
	log      []fakeEntry
	gen      int // bumped when the container stops: open follow streams end
	stopLen  int // len(log) at the last stop: what a stopped stream may still send
}

// logReq is one logs request, for the tests that count them per container.
type logReq struct {
	id string
	q  url.Values
}

type fakeDaemon struct {
	t   *testing.T
	def *fakeContainer // fakeID, the container every single-container test drives

	mu         sync.Mutex
	containers map[string]*fakeContainer
	changed    chan struct{} // closed and replaced on every change to a log, a gen or the event log
	logReqs    []logReq      // every logs request, in arrival order

	reqs       chan url.Values // one per logs request for fakeID, sent once its history is snapshotted
	gone       chan struct{}   // one per follow stream the client hung up on
	eventsGone chan struct{}   // one per events stream the client hung up on
	listFails  bool            // set by failList: /containers/json answers 500

	// The event log, as the daemon keeps it: every event ever published, of which a since
	// replay sees only the newest evLimit (the daemon's eventsLimit, 256). Live streams see
	// every event published after they subscribed.
	evlog   []events.Message
	evLimit int
	evGen   int             // bumped by endEvents: the open events streams end, later ones work
	evReqs  chan url.Values // one per events request
	evConns int             // events streams currently open
}

func newFakeDaemon(t *testing.T, name string, tty bool, history ...fakeEntry) *fakeDaemon {
	t.Helper()
	def := &fakeContainer{id: fakeID, name: name, tty: tty, running: true, log: history}
	d := &fakeDaemon{
		t:          t,
		def:        def,
		containers: map[string]*fakeContainer{fakeID: def},
		changed:    make(chan struct{}),
		reqs:       make(chan url.Values, 256),
		gone:       make(chan struct{}, 256),
		eventsGone: make(chan struct{}, 256),
		evLimit:    256,
		evReqs:     make(chan url.Values, 256),
	}
	srv := httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("DOCKER_API_VERSION", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	return d
}

// add makes another container known to the daemon, stopped and with an empty log. A test
// runs it with d.run.
func (d *fakeDaemon) add(id, name string, tty bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.containers[id] = &fakeContainer{id: id, name: name, tty: tty}
}

var versionPrefix = regexp.MustCompile(`^/v[0-9.]+`)

func (d *fakeDaemon) serve(w http.ResponseWriter, r *http.Request) {
	p := versionPrefix.ReplaceAllString(r.URL.Path, "")
	switch {
	case p == "/_ping":
		w.Header().Set("Api-Version", "1.47")
		w.WriteHeader(http.StatusOK)
	case p == "/events":
		d.serveEvents(w, r)
	case p == "/containers/json":
		d.serveList(w)
	case strings.HasPrefix(p, "/containers/"):
		id, op, _ := strings.Cut(strings.TrimPrefix(p, "/containers/"), "/")
		d.mu.Lock()
		c := d.containers[id]
		d.mu.Unlock()
		switch {
		case c == nil:
			http.NotFound(w, r)
		case op == "json":
			writeJSON(w, map[string]any{"Id": c.id, "Name": "/" + c.name, "Config": map[string]any{"Tty": c.tty}})
		case op == "logs":
			d.serveLogs(w, r, c)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

// serveList answers the running containers, sorted by id. logscry asks with
// status=running, which is the only filter it ever sends.
func (d *fakeDaemon) serveList(w http.ResponseWriter) {
	d.mu.Lock()
	fails := d.listFails
	var items []map[string]any
	for _, id := range slices.Sorted(maps.Keys(d.containers)) {
		if c := d.containers[id]; c.running {
			items = append(items, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}})
		}
	}
	d.mu.Unlock()
	if fails {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "list unavailable"})
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	writeJSON(w, items)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// serveEvents emulates the daemon's events endpoint (daemon/events/events.go): with since,
// the buffered events stamped at or after it, nanosecond-precise and inclusive, but only
// from the newest evLimit; then every event published after the subscription. The replay
// and the live position are taken under one lock, as the daemon takes them, so nothing
// falls between. Only container start and die are delivered: the filter logscry sends.
func (d *fakeDaemon) serveEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d.mu.Lock()
	gen, pos := d.evGen, len(d.evlog)
	if s := q.Get("since"); s != "" {
		since := parseWireSince(d.t, s)
		pos = max(len(d.evlog)-d.evLimit, 0)
		for pos < len(d.evlog) && d.evlog[pos].TimeNano < since.UnixNano() {
			pos++
		}
	}
	d.evConns++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.evConns--
		d.mu.Unlock()
	}()
	d.evReqs <- q

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	enc := json.NewEncoder(w)
	for {
		d.mu.Lock()
		ended := d.evGen != gen
		batch := slices.Clone(d.evlog[pos:])
		pos = len(d.evlog)
		changed := d.changed
		d.mu.Unlock()
		if ended {
			return
		}
		for _, m := range batch {
			if m.Type != events.ContainerEventType || (m.Action != events.ActionStart && m.Action != events.ActionDie) {
				continue
			}
			if err := enc.Encode(m); err != nil {
				return
			}
		}
		w.(http.Flusher).Flush()
		select {
		case <-changed:
		case <-r.Context().Done():
			signal(d.eventsGone)
			return
		}
	}
}

// signal sends on a counting channel without blocking: a handler that blocked on it would
// keep httptest's Close waiting.
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// parseWireSince reads a since parameter, which logscry sends as <sec>.<9-digit nsec>.
func parseWireSince(t *testing.T, s string) time.Time {
	sec, nsec, _ := strings.Cut(s, ".")
	si, err1 := strconv.ParseInt(sec, 10, 64)
	ni, err2 := strconv.ParseInt(nsec, 10, 64)
	if err1 != nil || err2 != nil || len(nsec) != 9 {
		t.Errorf("since = %q, want <sec>.<9-digit nsec>", s)
	}
	return time.Unix(si, ni)
}

// serveLogs emulates the logs endpoint for one container. A container that is not running
// gets its history and the end of the stream: the daemon follows only a running one.
func (d *fakeDaemon) serveLogs(w http.ResponseWriter, r *http.Request, c *fakeContainer) {
	q := r.URL.Query()
	tail, err := strconv.Atoi(q.Get("tail"))
	if err != nil {
		tail = -1 // daemon/logs.go: a non-numeric tail ("all", "") means everything
	}
	var since time.Time
	if s := q.Get("since"); s != "" {
		since = parseWireSince(d.t, s)
	}

	d.mu.Lock()
	history := c.log
	if tail >= 0 && tail < len(history) {
		history = history[len(history)-tail:]
	}
	history = slices.Clone(history)
	pos, gen, follow := len(c.log), c.gen, c.running
	d.logReqs = append(d.logReqs, logReq{c.id, q})
	d.notifyLocked()
	d.mu.Unlock()
	if c.id == fakeID {
		d.reqs <- q
	}

	w.WriteHeader(http.StatusOK)
	filtering := !since.IsZero()
	for _, e := range history {
		if filtering {
			if e.ts.Before(since) {
				continue
			}
			filtering = false
		}
		d.writeEntry(w, c, e)
	}
	w.(http.Flusher).Flush()
	if !follow {
		return
	}

	for {
		d.mu.Lock()
		end, ended := len(c.log), c.gen != gen
		if ended {
			end = max(pos, c.stopLen) // nothing logged after the stop reaches this stream
		}
		live := slices.Clone(c.log[pos:end])
		pos = end
		changed := d.changed
		d.mu.Unlock()
		for _, e := range live {
			d.writeEntry(w, c, e)
		}
		w.(http.Flusher).Flush()
		if ended {
			return
		}
		select {
		case <-changed:
		case <-r.Context().Done():
			signal(d.gone)
			return
		}
	}
}

func (d *fakeDaemon) writeEntry(w http.ResponseWriter, c *fakeContainer, e fakeEntry) {
	line := []byte(e.ts.UTC().Format(rfc3339NanoFixed) + " " + e.text + "\n")
	if c.tty {
		_, _ = w.Write(line)
		return
	}
	st := stdcopy.Stdout
	if e.stream == model.Stderr {
		st = stdcopy.Stderr
	}
	_, _ = newStdWriter(w, st).Write(line)
}

func (d *fakeDaemon) notifyLocked() {
	close(d.changed)
	d.changed = make(chan struct{})
}

// logs appends lines to the container's log file, as the container writing them.
func (d *fakeDaemon) logs(es ...fakeEntry) { d.logsTo(fakeID, es...) }

// logsTo appends lines to container id's log file.
func (d *fakeDaemon) logsTo(id string, es ...fakeEntry) {
	d.mu.Lock()
	c := d.containers[id]
	c.log = append(c.log, es...)
	d.notifyLocked()
	d.mu.Unlock()
}

// endStreams ends every open follow stream, as the daemon does when the container stops.
func (d *fakeDaemon) endStreams() { d.endStreamsOf(fakeID) }

func (d *fakeDaemon) endStreamsOf(id string) {
	d.mu.Lock()
	c := d.containers[id]
	c.gen++
	c.stopLen = len(c.log)
	d.notifyLocked()
	d.mu.Unlock()
}

func (d *fakeDaemon) event(action events.Action) { d.eventFor(fakeID, action) }

// eventFor publishes a container event, stamped now, and applies it to the container's
// state: the daemon publishes start and die as the container's state changes.
func (d *fakeDaemon) eventFor(id string, action events.Action) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.containers[id]
	switch action {
	case events.ActionStart:
		c.running = true
	case events.ActionDie:
		c.running = false
	}
	d.publishLocked(events.Message{
		Type:   events.ContainerEventType,
		Action: action,
		Actor:  events.Actor{ID: id, Attributes: map[string]string{"name": c.name}},
	})
}

func (d *fakeDaemon) publishLocked(m events.Message) {
	now := time.Now()
	m.Time, m.TimeNano = now.Unix(), now.UnixNano()
	d.evlog = append(d.evlog, m)
	d.notifyLocked()
}

// noise publishes n events logscry does not subscribe to, as healthchecks and execs do.
// They take room in the daemon's replay buffer all the same.
func (d *fakeDaemon) noise(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for range n {
		d.publishLocked(events.Message{Type: events.ContainerEventType, Action: events.ActionExecStart,
			Actor: events.Actor{ID: "n0153", Attributes: map[string]string{"name": "noise"}}})
	}
}

// run starts container id, writes its lines and, if exit, stops it: a die event and the end
// of its streams, as the daemon does when the process exits.
func (d *fakeDaemon) run(id string, exit bool, es ...fakeEntry) {
	d.eventFor(id, events.ActionStart)
	d.logsTo(id, es...)
	if exit {
		d.endStreamsOf(id)
		d.eventFor(id, events.ActionDie)
	}
}

// failList makes the container list fail with a 500 while the events stream works.
func (d *fakeDaemon) failList() {
	d.mu.Lock()
	d.listFails = true
	d.mu.Unlock()
}

// healList makes the container list answer again.
func (d *fakeDaemon) healList() {
	d.mu.Lock()
	d.listFails = false
	d.mu.Unlock()
}

// openEvents reports how many events streams are open.
func (d *fakeDaemon) openEvents() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.evConns
}

// setEventLimit sets how many of the newest events a since replay can see.
func (d *fakeDaemon) setEventLimit(n int) {
	d.mu.Lock()
	d.evLimit = n
	d.mu.Unlock()
}

// endEvents ends the open events streams while every log stream stays open, as a proxy that
// drops the idle events connection does. The client reads it as io.EOF. A later
// subscription works.
func (d *fakeDaemon) endEvents() {
	d.mu.Lock()
	d.evGen++
	d.notifyLocked()
	d.mu.Unlock()
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

// nextEventsRequest waits for the next events request and returns its query.
func (d *fakeDaemon) nextEventsRequest() url.Values {
	d.t.Helper()
	select {
	case q := <-d.evReqs:
		return q
	case <-time.After(5 * time.Second):
		d.t.Fatal("timed out waiting for an events request")
		return nil
	}
}

// logRequests returns the logs requests made so far for container id.
func (d *fakeDaemon) logRequests(id string) []url.Values {
	d.mu.Lock()
	defer d.mu.Unlock()
	var qs []url.Values
	for _, r := range d.logReqs {
		if r.id == id {
			qs = append(qs, r.q)
		}
	}
	return qs
}

// lastEventTime returns the stamp of the newest event published, as logscry's since sends it.
func (d *fakeDaemon) lastEventTime() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return time.Unix(0, d.evlog[len(d.evlog)-1].TimeNano)
}

// start re-starts the container and returns the query of the logs request logscry
// re-attaches with. It sends one start event, as the daemon does, and waits for that
// request: a start that arrives while the old follower is still unwinding is kept and
// served when it exits (#85), so no test needs to send it twice.
func (d *fakeDaemon) start() url.Values {
	d.t.Helper()
	d.event(events.ActionStart)
	select {
	case q := <-d.reqs:
		return q
	case <-time.After(5 * time.Second):
		d.t.Fatal("timed out waiting for the re-attach")
		return nil
	}
}

// fakeProxy is a TCP forwarder in front of the fake daemon, as socat is in front of the
// real socket. down stops it as a kill does: the port refuses, and every connection through
// it, the events stream and every log stream alike, is cut. up listens on the same port
// again.
type fakeProxy struct {
	t      *testing.T
	target string
	addr   string

	mu    sync.Mutex
	ln    net.Listener
	conns map[net.Conn]bool
}

// proxy puts a fakeProxy in front of the daemon and points DOCKER_HOST at it. Call it
// before startFollow.
func (d *fakeDaemon) proxy() *fakeProxy {
	d.t.Helper()
	target := strings.TrimPrefix(os.Getenv("DOCKER_HOST"), "tcp://")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		d.t.Fatalf("proxy: %v", err)
	}
	p := &fakeProxy{t: d.t, target: target, addr: ln.Addr().String(), ln: ln, conns: map[net.Conn]bool{}}
	go p.serve(ln)
	d.t.Cleanup(p.down)
	d.t.Setenv("DOCKER_HOST", "tcp://"+p.addr)
	return p
}

func (p *fakeProxy) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		u, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		if p.ln != ln { // cut while dialling
			p.mu.Unlock()
			_ = c.Close()
			_ = u.Close()
			continue
		}
		p.conns[c], p.conns[u] = true, true
		p.mu.Unlock()
		pipe := func(dst, src net.Conn) {
			_, _ = io.Copy(dst, src)
			_ = dst.Close()
			_ = src.Close()
		}
		go pipe(u, c)
		go pipe(c, u)
	}
}

func (p *fakeProxy) down() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln == nil {
		return
	}
	_ = p.ln.Close()
	p.ln = nil
	for c := range p.conns {
		_ = c.Close()
	}
	clear(p.conns)
}

func (p *fakeProxy) up() {
	p.t.Helper()
	var ln net.Listener
	var err error
	for range 50 {
		if ln, err = net.Listen("tcp", p.addr); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		p.t.Fatalf("proxy up: %v", err)
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go p.serve(ln)
}

// follow runs src.Lines in the background against the fake daemon.
type follow struct {
	t       *testing.T
	cancel  context.CancelFunc
	done    chan error
	out     chan model.LogLine
	got     []model.LogLine
	notices chan error // what the source said while running (see Notices)
}

func startFollow(t *testing.T, src *DockerSource) *follow {
	t.Helper()
	notices := make(chan error, 1024)
	return startFollowWith(t, src, notices, NewNotices(notices, cap(notices)))
}

// startFollowWith is startFollow with the source's notices sent through n, which a test
// may give a channel nobody reads.
func startFollowWith(t *testing.T, src *DockerSource, notices chan error, n *Notices) *follow {
	t.Helper()
	ctx, cancel := context.WithCancel(WithNotices(context.Background(), n))
	f := &follow{t: t, cancel: cancel, done: make(chan error, 1), out: make(chan model.LogLine, 1024), notices: notices}
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

// wireTailAll is the tail a logs request carries for "all": none. The client leaves the
// parameter out for "" and "all" (moby/moby/client v0.6.2, container_logs.go), and the
// daemon reads a missing tail as no limit, so expectQuery sees "".
const wireTailAll = ""

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
			expectQuery(t, "re-attach", d.start(), wireSince(at(5)), wireTailAll)
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
	expectQuery(t, "re-attach", d.start(), wireSince(Tm), wireTailAll)
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
	expectQuery(t, "re-attach", d.start(), wireSince(at(5)), wireTailAll)
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
	expectQuery(t, "first re-attach", d.start(), wireSince(at(2)), wireTailAll)
	d.die()
	d.logs(out(at(200), "line 3"))
	expectQuery(t, "second re-attach", d.start(), wireSince(at(2)), wireTailAll)
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
	expectQuery(t, "re-attach", d.start(), wireSince(at(cap(f.out))), wireTailAll)
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
			expectQuery(t, "re-attach", d.nextRequest(), wireSince(at(5)), wireTailAll)
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

// TestDockerListErrorClosesTheEventsSubscription is #98's second half: Lines subscribes to
// events before it lists, and the list fails. Lines returns the list error, and the events
// request it opened must close with it rather than stay open until the run's ctx ends:
// cli.Close closes only idle connections. The caller's ctx is not cancelled here.
func TestDockerListErrorClosesTheEventsSubscription(t *testing.T) {
	d := newFakeDaemon(t, "ab-list", false)
	d.failList()
	f := startFollow(t, NewDockerSource(DockerSelector{NameRegex: "^ab-list$"}))

	select {
	case err := <-f.done:
		if err == nil || !strings.Contains(err.Error(), "docker: list") {
			t.Fatalf("Lines = %v, want the docker: list error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Lines had not returned 2s after the list failed")
	}
	select {
	case <-d.eventsGone:
	case <-time.After(2 * time.Second):
		t.Fatal("the events request was still open 2s after Lines returned")
	}
}
