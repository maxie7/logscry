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

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

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
// fan-in (Run) closes the shared channel only when every Source ends, so Docker
// — being long-lived and dynamic — must be one Source, not one per container.
type DockerSource struct {
	Selector DockerSelector
	// Tail is how many trailing lines to fetch per container on first attach. A
	// restarted container is re-attached from the last line read instead.
	Tail string
}

// NewDockerSource returns a Source that follows Docker container logs.
func NewDockerSource(sel DockerSelector) *DockerSource {
	return &DockerSource{Selector: sel, Tail: "100"}
}

// Name implements Source.
func (s *DockerSource) Name() string { return "docker" }

// Lines implements Source. It creates a Docker client, subscribes to container
// start/die events, attaches a follower to every already-running container that
// matches the selector, and thereafter attaches/detaches followers as containers
// come and go. It blocks until ctx is cancelled (a clean stop → nil) or the
// event/client machinery fails.
//
// A container's log survives a restart, so a re-attach to an ID logscry has read
// from resumes after the last line read (see dockerMark) rather than fetching the
// tail again: the tail's lines were already counted.
func (s *DockerSource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	m, err := newMatcher(s.Selector)
	if err != nil {
		return err
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("docker: client: %w", err)
	}
	defer cli.Close()

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		followers = map[string]context.CancelFunc{}
		// marks outlives each follower: it is where a restart of the same id resumes.
		marks = map[string]dockerMark{}
	)

	// start attaches a follower to id unless one is already running.
	start := func(id, name string) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := followers[id]; ok {
			return
		}
		fctx, cancel := context.WithCancel(ctx)
		followers[id] = cancel
		prev := marks[id]
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A follower ending (log EOF, container gone, ctx cancel) removes its
			// own entry so a later start of the same id can re-attach. IDs are
			// unique, so there is no reuse race. Per-follower errors are expected
			// on shutdown and when a container disappears, so they degrade
			// silently rather than crash the whole Docker source.
			m, _ := s.followContainer(fctx, cli, id, name, prev, out)
			// The mark is stored in the same critical section that removes the
			// follower, so start sees either a running follower or its final mark.
			mu.Lock()
			marks[id] = m
			delete(followers, id)
			mu.Unlock()
		}()
	}
	// stop cancels a follower; the follower goroutine deletes its own map entry.
	stop := func(id string) {
		mu.Lock()
		if cancel, ok := followers[id]; ok {
			cancel()
		}
		mu.Unlock()
	}

	// Subscribe to events BEFORE listing so containers started in the gap between
	// list and subscribe are not missed.
	ef := filters.NewArgs()
	ef.Add("type", string(events.ContainerEventType))
	ef.Add("event", string(events.ActionStart))
	ef.Add("event", string(events.ActionDie))
	msgs, errs := cli.Events(ctx, events.ListOptions{Filters: ef})

	// Attach to already-running matching containers.
	lf := filters.NewArgs()
	lf.Add("status", "running")
	list, err := cli.ContainerList(ctx, container.ListOptions{Filters: lf})
	if err != nil {
		return fmt.Errorf("docker: list: %w", err)
	}
	for _, c := range list {
		name := containerName(c.Names)
		if m.matches(c.ID, name, c.Labels) {
			start(c.ID, name)
		}
	}

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case msg := <-msgs:
			switch msg.Action {
			case events.ActionStart:
				name := msg.Actor.Attributes["name"]
				if m.matches(msg.Actor.ID, name, msg.Actor.Attributes) {
					start(msg.Actor.ID, name)
				}
			case events.ActionDie:
				stop(msg.Actor.ID)
			}
		case err := <-errs:
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			wg.Wait()
			return fmt.Errorf("docker: events: %w", err)
		}
	}
}

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
	insp, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return mark, fmt.Errorf("inspect: %w", err)
	}
	tty := insp.Config != nil && insp.Config.Tty

	opts := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
		Tail:       s.Tail,
	}
	if since, ok := mark.since(); ok {
		// "<sec>.<nsec>" passes through the client's GetTimestamp unchanged.
		opts.Since = fmt.Sprintf("%d.%09d", since.Unix(), since.Nanosecond())
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
