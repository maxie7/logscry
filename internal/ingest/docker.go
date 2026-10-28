// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"github.com/maxie7/logscry/internal/model"
)

// DockerSelector describes which containers a DockerSource should follow.
type DockerSelector struct {
	All       bool     // --docker-all
	Label     []string // --docker-label k=v (repeatable, AND-combined)
	NameRegex string   // --docker-name <regex>
	IDs       []string // explicit container IDs
}

// DockerSource follows logs from Docker containers, auto-attaching to new ones.
// It is a single long-running Source: its Lines call blocks until ctx is
// cancelled, managing one log follower per matching container internally. The
// fan-in (Run) closes the shared channel only when every Source ends, so Docker —
// being long-lived and dynamic — must be one Source, not one per container.
type DockerSource struct {
	Selector DockerSelector
	// Tail is how many trailing lines to fetch per container on first attach. A
	// restarted container is re-attached from the last line read instead.
	Tail string

	// retryMin and retryMax bound the wait between reconnect attempts (see Lines).
	retryMin, retryMax time.Duration
}

// NewDockerSource returns a Source that follows Docker container logs.
func NewDockerSource(sel DockerSelector) *DockerSource {
	return &DockerSource{Selector: sel, Tail: "100", retryMin: time.Second, retryMax: 30 * time.Second}
}

// Name implements Source.
func (s *DockerSource) Name() string { return "docker" }

// Lines implements Source. It creates a Docker client, subscribes to container
// start/die events, attaches a follower to every already-running container that
// matches the selector, and thereafter attaches/detaches followers as containers
// come and go. It blocks until ctx is cancelled (a clean stop → nil), or until the
// first connection to the daemon fails before it has listed the containers.
//
// Without its event stream the source can no longer see a container start or stop, so
// once the stream fails it reconnects (#100) rather than ending (#97). It stops every
// follower and waits for them, as #97 did, then re-subscribes with since set to the last
// event it handled, so that the daemon replays the gap from its buffer, re-lists the
// running containers and re-attaches each from its mark, so that nothing is read twice.
// A failed attempt is retried after retryMin, doubling up to retryMax, for as long as ctx
// lives. Each loss and each attempt, and the reconnect, is said through the Notices ctx
// carries: the source never stops following Docker without a word.
//
// A container's log survives a restart, so a re-attach to an ID logscry has read
// from resumes after the last line read (see dockerMark) rather than fetching the
// tail again: the tail's lines were already counted.
func (s *DockerSource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	m, err := newMatcher(s.Selector)
	if err != nil {
		return err
	}

	// The client negotiates the API version with the daemon on its first request.
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return fmt.Errorf("docker: client: %w", err)
	}
	defer cli.Close()

	notices := noticesFrom(ctx)
	minDelay, maxDelay := s.retryMin, s.retryMax
	if minDelay <= 0 {
		minDelay, maxDelay = time.Second, 30*time.Second
	}

	st := &dockerState{marks: map[string]dockerMark{}}
	delay := minDelay
	// attempt counts the failed connections since the stream was last up: the notices'
	// "attempt N". 0 is the first connection, which has nothing to resume.
	attempt := 0
	for {
		var up time.Time
		err := s.connect(ctx, cli, m, st, attempt > 0, out, func(attached int) {
			up = time.Now()
			if attempt > 0 {
				notices.Send(fmt.Errorf("docker: events reconnected, %d container(s) re-attached", attached))
			}
		})
		if ctx.Err() != nil {
			return nil
		}
		if attempt == 0 && up.IsZero() {
			// The first connection failed: nothing is followed and there is nothing to
			// resume, and a wrong DOCKER_HOST or a daemon too old is better said at once
			// than retried. The source ends, as before #100.
			return fmt.Errorf("docker: %w", err)
		}
		if !up.IsZero() {
			// The stream was up and is lost: a new outage. The wait starts over only if the
			// stream had stayed up for a full retryMax, so that a connection that drops as
			// soon as it is made cannot make the source notify and retry every retryMin.
			attempt = 0
			if time.Since(up) >= maxDelay {
				delay = minDelay
			}
		}
		attempt++
		notices.Send(fmt.Errorf("docker: events lost, reconnecting (attempt %d, next in %s): %w", attempt, delay, err))
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		delay = min(delay*2, maxDelay)
	}
}

// dockerState is what outlives a connection to the daemon.
type dockerState struct {
	// marks is where each container's followers stopped reading (see followerSet). Only one
	// connection's followers run at a time, so it needs no lock of its own.
	marks map[string]dockerMark
	// event is the newest event handled, and how many were handled at its stamp: where a
	// re-subscription's replay starts, and which of the replayed events were already seen.
	event streamMark
	// subscribed is when the first connection subscribed, by this host's clock: the replay's
	// start until an event has been handled.
	subscribed time.Time
}

// since is where a re-subscription asks the daemon's replay to start.
func (st *dockerState) since() time.Time {
	if st.event.at.IsZero() {
		return st.subscribed
	}
	return st.event.at
}

// connect runs one connection to the daemon. It subscribes to events (from st.since() if
// resumed, so that the daemon replays what happened since), lists the running containers
// and attaches to those that match, then calls connected with how many it attached and
// follows events until ctx is cancelled (→ nil) or the stream fails. Every follower it
// started has exited by the time it returns, and their marks are in st.
//
// An error before the list has succeeded returns without calling connected.
func (s *DockerSource) connect(ctx context.Context, cli *client.Client, m matcher, st *dockerState,
	resumed bool, out chan<- model.LogLine, connected func(attached int),
) error {
	// The followers' parent, and the events subscription's and the list's. It is cancelled
	// on every return: so that the followers stop and waiting for them ends, and so that a
	// list error does not leave the events request open (#98): cli.Close closes only idle
	// connections.
	sctx, cancel := context.WithCancel(ctx)
	// Per-follower errors are expected on shutdown and when a container disappears, so
	// they degrade silently rather than crash the whole Docker source.
	followers := newFollowerSet(sctx, func(fctx context.Context, id, name string, mark dockerMark) dockerMark {
		read, _ := s.followContainer(fctx, cli, id, name, mark, out)
		return read
	})
	// The marks are shared with the previous connection's set, which has exited: no two
	// sets ever run at once, so no container is followed twice across a reconnect.
	followers.marks = st.marks
	defer func() {
		cancel()
		followers.wait()
	}()
	start, stop := followers.start, followers.stop

	// Subscribe to events BEFORE listing so containers started in the gap between
	// list and subscribe are not missed.
	ef := make(client.Filters).
		Add("type", string(events.ContainerEventType)).
		Add("event", string(events.ActionStart), string(events.ActionDie))
	opts := client.EventsListOptions{Filters: ef}
	// What the replay holds is the gap's history: events stamped before this instant, by
	// this host's clock, which on the daemon's own host is the daemon's.
	subscribed := time.Now()
	if resumed {
		since := st.since()
		// "<sec>.<nsec>" passes through the client's GetTimestamp unchanged.
		opts.Since = fmt.Sprintf("%d.%09d", since.Unix(), since.Nanosecond())
	} else if st.subscribed.IsZero() {
		st.subscribed = subscribed
	}
	sub := cli.Events(sctx, opts)
	msgs, errs := sub.Messages, sub.Err
	if resumed {
		// Events returns once the daemon has answered, so a refused subscription is already
		// here. (A first connection lets the list say it, as before #100.)
		select {
		case err := <-errs:
			return fmt.Errorf("events: %w", err)
		default:
		}
	}

	// Attach to already-running matching containers.
	lf := make(client.Filters).Add("status", "running")
	list, err := cli.ContainerList(sctx, client.ContainerListOptions{Filters: lf})
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	attached := 0
	for _, c := range list.Items {
		name := containerName(c.Names)
		if m.matches(c.ID, name, c.Labels) && start(c.ID, name) {
			attached++
		}
	}
	connected(attached)

	// The replay starts at the newest event handled, inclusively, so the events handled at
	// that stamp come back first: they are skipped, as readDockerStream skips the lines at
	// a log's mark, since the daemon replays its buffer in the order it delivered it.
	prev, skip := st.event, st.event.n
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-msgs:
			t := time.Unix(0, msg.TimeNano)
			if resumed && !prev.at.IsZero() {
				if t.Before(prev.at) {
					continue
				}
				if t.Equal(prev.at) && skip > 0 {
					skip--
					continue
				}
			}
			st.event.advance(t)
			switch msg.Action {
			case events.ActionStart:
				name := msg.Actor.Attributes["name"]
				if m.matches(msg.Actor.ID, name, msg.Actor.Attributes) {
					start(msg.Actor.ID, name)
				}
			case events.ActionDie:
				if resumed && t.Before(subscribed) {
					// A die from the gap. The container has stopped, so the daemon sends its
					// log and ends the stream by itself; cancelling would cut a follower the
					// replayed start has just launched before it reads anything.
					followers.retire(msg.Actor.ID)
				} else {
					stop(msg.Actor.ID)
				}
			}
		case err := <-errs:
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("events: %w", err)
		}
	}
}

// followerSet runs at most one follower per container ID, and remembers where each ID's
// last follower stopped reading so that a restart of it resumes there (see dockerMark).
//
// A follower is stopped on die and removes its own entry only once it has unwound. A start
// for the same ID can arrive in between: Lines handles the die and the restart's start
// back to back, while the cancelled follower is still on its way out. Such a start is not
// dropped (#85). It is recorded on the stopped follower, and the follower launches its
// successor as it exits, in the critical section that stores its mark — so the successor
// starts from that final mark and nothing is captured early. A start for a follower that
// was not stopped (a duplicate event, or the overlap between subscribing and listing at
// startup) attaches nothing and leaves nothing pending, as before.
type followerSet struct {
	ctx    context.Context // every follower's parent: cancelled at shutdown or when its connection ends
	follow func(ctx context.Context, id, name string, mark dockerMark) dockerMark

	mu      sync.Mutex
	wg      sync.WaitGroup
	running map[string]*follower
	// marks outlives each follower: it is where a restart of the same id resumes.
	marks map[string]dockerMark
}

// follower is one running follower's entry.
type follower struct {
	cancel      context.CancelFunc
	stopped     bool   // stop was called: a die was seen for this run of the container
	restart     bool   // a start arrived while this stopped follower was still unwinding
	restartName string // that start's container name, the latest if there were several
}

func newFollowerSet(ctx context.Context, follow func(ctx context.Context, id, name string, mark dockerMark) dockerMark) *followerSet {
	return &followerSet{ctx: ctx, follow: follow, running: map[string]*follower{}, marks: map[string]dockerMark{}}
}

// start attaches a follower to id unless one is already running, and reports whether it
// did. If the running one has been stopped, the start is kept and its successor is launched
// when it exits.
func (s *followerSet) start(id, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.running[id]; ok {
		if f.stopped {
			f.restart, f.restartName = true, name
		}
		return false
	}
	s.launchLocked(id, name)
	return true
}

// launchLocked starts a follower for id from its stored mark. s.mu must be held.
func (s *followerSet) launchLocked(id, name string) {
	fctx, cancel := context.WithCancel(s.ctx)
	f := &follower{cancel: cancel}
	s.running[id] = f
	prev := s.marks[id]
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// A follower ending (log EOF, container gone, ctx cancel) removes its own entry so
		// a later start of the same id can re-attach. IDs are unique, so there is no reuse
		// race.
		m := s.follow(fctx, id, name, prev)
		// Released here, however the follower ended. One whose stream ended before its die
		// is never stopped, and its context would stay registered on s.ctx until the source
		// ends (#98). A stop that lands before the entry is removed cancels again, a no-op.
		cancel()
		// The mark is stored in the same critical section that removes the follower, so
		// start sees either a running follower or its final mark — and a successor
		// launched here reads that mark. Its wg.Add comes before this goroutine's Done,
		// so the count cannot reach zero in between. Once the set's ctx is cancelled (at
		// shutdown, or as its connection to the daemon ends) no successor is launched.
		s.mu.Lock()
		defer s.mu.Unlock()
		s.marks[id] = m
		delete(s.running, id)
		if f.restart && s.ctx.Err() == nil {
			s.launchLocked(id, f.restartName)
		}
	}()
}

// stop cancels id's follower; the follower deletes its own entry as it exits.
func (s *followerSet) stop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.running[id]; ok {
		f.stopped = true
		f.cancel()
	}
}

// retire marks id's follower stopped without cancelling it: a die seen for a container
// whose stream the daemon ends by itself. A later start is kept and served when it exits,
// as after stop.
func (s *followerSet) retire(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.running[id]; ok {
		f.stopped = true
	}
}

// wait blocks until every follower, including any successor launched on the way out,
// has exited.
func (s *followerSet) wait() { s.wg.Wait() }

// followContainer opens the log stream for one container and pumps its lines
// into out until ctx is cancelled or the stream ends. It returns how far it read,
// starting from mark, the previous follower's result for the same id.
//
// A zero mark is a first attach (or a follower before it read nothing) and fetches
// s.Tail lines of history. Otherwise the daemon is asked for everything since the
// earliest stream's mark, with no tail limit: the daemon applies tail BEFORE since,
// so a limit could only drop lines logged between the restart and the re-attach.
// The daemon's since is inclusive, so the lines at the mark come back; streamLogs
// drops them.
func (s *DockerSource) followContainer(ctx context.Context, cli *client.Client, id, name string, mark dockerMark, out chan<- model.LogLine) (dockerMark, error) {
	insp, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return mark, fmt.Errorf("inspect: %w", err)
	}
	tty := insp.Container.Config != nil && insp.Container.Config.Tty

	opts := client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
		Tail:       s.Tail,
	}
	if since, ok := mark.since(); ok {
		// "<sec>.<nsec>" passes through the client's GetTimestamp unchanged.
		opts.Since = fmt.Sprintf("%d.%09d", since.Unix(), since.Nanosecond())
		// The client sends no tail for "all", which the daemon reads as no limit.
		opts.Tail = "all"
	}
	logs, err := cli.ContainerLogs(ctx, id, opts)
	if err != nil {
		return mark, fmt.Errorf("logs: %w", err)
	}
	defer logs.Close()

	err = streamLogs(ctx, logs, tty, "docker:"+name, &mark, out)
	return mark, err
}

// dockerMark is how far logscry has read one container's log, per stream, in the
// daemon's clock. It is kept per stream because the daemon stamps each stream from
// its own copier goroutine: stamps are monotonic WITHIN a stream but not across
// the two, so only a per-stream mark says which lines have been read.
type dockerMark struct {
	stdout, stderr streamMark
}

// since returns the earliest instant either stream has read up to, or false if
// neither has read anything.
func (m dockerMark) since() (time.Time, bool) {
	a, b := m.stdout.at, m.stderr.at
	switch {
	case a.IsZero() && b.IsZero():
		return time.Time{}, false
	case a.IsZero():
		return b, true
	case b.IsZero() || a.Before(b):
		return a, true
	default:
		return b, true
	}
}

// streamMark is the newest daemon stamp delivered on one stream and how many lines
// were delivered at exactly that stamp. Lines are read in order and stamps are
// monotonic within a stream, so those n lines are the FIRST n at that stamp: that
// is what lets a re-attach drop them without dropping a distinct line sharing it.
type streamMark struct {
	at time.Time
	n  int
}

// advance records one delivered line stamped t.
func (m *streamMark) advance(t time.Time) {
	switch {
	case t.After(m.at):
		*m = streamMark{at: t, n: 1}
	case t.Equal(m.at):
		m.n++
	}
}

// streamLogs reads a container log stream into out. A TTY container yields a
// single raw stream (no framing) tagged as Stdout; a non-TTY container yields
// Docker's multiplexed format (an 8-byte header per frame) which must be demuxed
// with stdcopy so stdout and stderr land on the right Stream. Splitting via
// io.Pipe lets the shared readLines helper handle line buffering per stream.
//
// mark is read and advanced per stream; each stream's goroutine owns its own half,
// and the caller reads it only after streamLogs returns, so it needs no lock.
//
// A pipe write returns only once its reader has consumed every byte, so each reader
// closes its read end when it returns, with its own result. A reader stopped by
// cancellation mid-frame then fails the demuxer's pending write with ctx's error instead
// of leaving it blocked forever (#84); the demuxer returns, closes both write ends, and the
// other reader ends too. A reader that returns on EOF closes nothing that is still in use:
// EOF arrives only after the demuxer has finished and closed both write ends.
func streamLogs(ctx context.Context, r io.Reader, tty bool, source string, mark *dockerMark, out chan<- model.LogLine) error {
	if tty {
		return readDockerStream(ctx, r, source, model.Stdout, &mark.stdout, out)
	}

	outR, outW := io.Pipe()
	errR, errW := io.Pipe()

	var (
		wg             sync.WaitGroup
		outErr, errErr error
		demuxErr       error
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		_, demuxErr = stdcopy.StdCopy(outW, errW, r)
		// Unblock the pipe readers with EOF (or the demux error).
		outW.CloseWithError(demuxErr)
		errW.CloseWithError(demuxErr)
	}()
	go func() {
		defer wg.Done()
		outErr = readDockerStream(ctx, outR, source, model.Stdout, &mark.stdout, out)
		outR.CloseWithError(outErr)
	}()
	go func() {
		defer wg.Done()
		errErr = readDockerStream(ctx, errR, source, model.Stderr, &mark.stderr, out)
		errR.CloseWithError(errErr)
	}()
	wg.Wait()

	return errors.Join(demuxErr, outErr, errErr)
}

// readDockerStream reads lines from r via the shared readLines helper, then strips and
// parses each line's RFC3339Nano timestamp prefix (readLines itself cannot, since it
// hardcodes receipt Time and the raw line).
//
// It also skips what an earlier follower of the same stream already delivered, as
// recorded in mark: lines stamped before mark.at, and the first mark.n lines stamped
// exactly at it. That needs a dropped line, which mapLines' transform cannot express,
// so the read/emit loop is mapLines' own, written out. mark advances only once a line
// is actually sent, so a line read but cut off by cancellation is re-read on restart.
// A line without a parseable stamp is passed through and does not move the mark.
func readDockerStream(ctx context.Context, r io.Reader, source string, stream model.Stream, mark *streamMark, out chan<- model.LogLine) error {
	prev, skip := *mark, mark.n
	interim := make(chan model.LogLine)
	var readErr error
	go func() {
		defer close(interim)
		readErr = readLines(ctx, r, source, stream, interim)
	}()
	for ll := range interim {
		raw := ll.Raw
		ll.Time, ll.Raw = parseDockerTS(ll.Time, ll.Raw)
		stamped := len(ll.Raw) != len(raw)
		if stamped && !prev.at.IsZero() {
			if ll.Time.Before(prev.at) {
				continue
			}
			if ll.Time.Equal(prev.at) && skip > 0 {
				skip--
				continue
			}
		}
		select {
		case out <- ll:
		case <-ctx.Done():
			return ctx.Err()
		}
		if stamped {
			mark.advance(ll.Time)
		}
	}
	return readErr
}

// parseDockerTS splits a Docker "<RFC3339Nano> <message>" line into its parsed
// time and the message body. On any parse failure it falls back to the receipt
// time and the untouched line, so a missing/garbled prefix never loses data.
func parseDockerTS(receipt time.Time, raw string) (time.Time, string) {
	if i := strings.IndexByte(raw, ' '); i > 0 {
		if t, err := time.Parse(time.RFC3339Nano, raw[:i]); err == nil {
			return t, raw[i+1:]
		}
	}
	return receipt, raw
}

// containerName returns a clean container name from Docker's Names slice, which
// carries a leading "/".
func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// matcher is the pure container-selection predicate, shared by the initial list
// and the event stream so both apply identical rules.
type matcher struct {
	all    bool
	ids    map[string]bool
	name   *regexp.Regexp
	labels map[string]string
}

// newMatcher compiles a DockerSelector into a matcher, returning an error for a
// bad name regex or a --docker-label value missing its "=".
func newMatcher(sel DockerSelector) (matcher, error) {
	m := matcher{all: sel.All}

	if len(sel.IDs) > 0 {
		m.ids = make(map[string]bool, len(sel.IDs))
		for _, id := range sel.IDs {
			m.ids[id] = true
		}
	}
	if sel.NameRegex != "" {
		re, err := regexp.Compile(sel.NameRegex)
		if err != nil {
			return matcher{}, fmt.Errorf("docker: --docker-name: %w", err)
		}
		m.name = re
	}
	if len(sel.Label) > 0 {
		m.labels = make(map[string]string, len(sel.Label))
		for _, kv := range sel.Label {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return matcher{}, fmt.Errorf("docker: --docker-label %q: want k=v", kv)
			}
			m.labels[k] = v
		}
	}
	return m, nil
}

// matches reports whether a container with the given id, name and labels should
// be followed. --docker-all wins outright; otherwise the container matches if it
// satisfies any configured criterion (explicit id / name regex / all required
// labels).
func (m matcher) matches(id, name string, labels map[string]string) bool {
	if m.all {
		return true
	}
	if m.ids[id] {
		return true
	}
	if m.name != nil && m.name.MatchString(name) {
		return true
	}
	if len(m.labels) > 0 && labelsMatch(m.labels, labels) {
		return true
	}
	return false
}

// labelsMatch reports whether every required k=v label is present in have.
func labelsMatch(want, have map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}
