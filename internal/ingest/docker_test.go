// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"

	"github.com/maxie7/logscry/internal/model"
)

func TestParseDockerTS(t *testing.T) {
	receipt := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("valid prefix is parsed and stripped", func(t *testing.T) {
		raw := "2024-03-04T05:06:07.123456789Z hello world"
		gotTime, gotMsg := parseDockerTS(receipt, raw)
		want, _ := time.Parse(time.RFC3339Nano, "2024-03-04T05:06:07.123456789Z")
		if !gotTime.Equal(want) {
			t.Errorf("time = %v, want %v", gotTime, want)
		}
		if gotMsg != "hello world" {
			t.Errorf("msg = %q, want %q", gotMsg, "hello world")
		}
	})

	t.Run("malformed prefix falls back to receipt time and untouched line", func(t *testing.T) {
		for _, raw := range []string{"not-a-timestamp here", "noSpaceAtAll", ""} {
			gotTime, gotMsg := parseDockerTS(receipt, raw)
			if !gotTime.Equal(receipt) {
				t.Errorf("raw %q: time = %v, want receipt %v", raw, gotTime, receipt)
			}
			if gotMsg != raw {
				t.Errorf("raw %q: msg = %q, want unchanged", raw, gotMsg)
			}
		}
	})
}

func TestMatcher(t *testing.T) {
	labels := map[string]string{"app": "web", "env": "prod"}

	tests := []struct {
		name string
		sel  DockerSelector
		id   string
		cn   string
		want bool
	}{
		{"all matches anything", DockerSelector{All: true}, "abc", "whatever", true},
		{"name regex hit", DockerSelector{NameRegex: "^web"}, "abc", "web-1", true},
		{"name regex miss", DockerSelector{NameRegex: "^db"}, "abc", "web-1", false},
		{"explicit id hit", DockerSelector{IDs: []string{"abc"}}, "abc", "web-1", true},
		{"explicit id miss", DockerSelector{IDs: []string{"xyz"}}, "abc", "web-1", false},
		{"all labels present", DockerSelector{Label: []string{"app=web", "env=prod"}}, "abc", "web-1", true},
		{"one label missing", DockerSelector{Label: []string{"app=web", "env=staging"}}, "abc", "web-1", false},
		{"nothing selected", DockerSelector{}, "abc", "web-1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := newMatcher(tc.sel)
			if err != nil {
				t.Fatalf("newMatcher: %v", err)
			}
			if got := m.matches(tc.id, tc.cn, labels); got != tc.want {
				t.Errorf("matches(%q, %q, %v) = %v, want %v", tc.id, tc.cn, labels, got, tc.want)
			}
		})
	}
}

func TestNewMatcherErrors(t *testing.T) {
	if _, err := newMatcher(DockerSelector{NameRegex: "("}); err == nil {
		t.Error("expected error for bad name regexp")
	}
	if _, err := newMatcher(DockerSelector{Label: []string{"nokv"}}); err == nil {
		t.Error("expected error for label without '='")
	}
}

// TestStreamLogsDemuxesNonTTY builds a synthetic multiplexed stream (the 8-byte
// framed format Docker uses when a container has no TTY) and verifies streamLogs
// demuxes it: stdout and stderr land on the right Stream, the timestamp prefix is
// parsed off, and no header bytes leak into the message.
func TestStreamLogsDemuxesNonTTY(t *testing.T) {
	var buf bytes.Buffer
	sw := newStdWriter(&buf, stdcopy.Stdout)
	if _, err := sw.Write([]byte("2024-03-04T05:06:07.000000000Z hello out\n")); err != nil {
		t.Fatal(err)
	}
	ew := newStdWriter(&buf, stdcopy.Stderr)
	if _, err := ew.Write([]byte("2024-03-04T05:06:08.000000000Z hello err\n")); err != nil {
		t.Fatal(err)
	}

	got := collect(t, func(ctx context.Context, out chan<- model.LogLine) error {
		return streamLogs(ctx, &buf, false, "docker:x", &dockerMark{}, out)
	})

	byStream := map[model.Stream]model.LogLine{}
	for _, ll := range got {
		byStream[ll.Stream] = ll
		if ll.Source != "docker:x" {
			t.Errorf("unexpected source %q", ll.Source)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %+v", len(got), got)
	}
	if out := byStream[model.Stdout]; out.Raw != "hello out" {
		t.Errorf("stdout line = %q, want %q (header/timestamp leak?)", out.Raw, "hello out")
	}
	if er := byStream[model.Stderr]; er.Raw != "hello err" {
		t.Errorf("stderr line = %q, want %q (header/timestamp leak?)", er.Raw, "hello err")
	}
	want, _ := time.Parse(time.RFC3339Nano, "2024-03-04T05:06:07.000000000Z")
	if out := byStream[model.Stdout]; !out.Time.Equal(want) {
		t.Errorf("stdout time = %v, want %v", out.Time, want)
	}
}

// TestStreamLogsTTYRaw verifies the TTY path: a raw, unframed stream is read
// directly and every line is tagged Stdout.
func TestStreamLogsTTYRaw(t *testing.T) {
	input := "2024-03-04T05:06:07.000000000Z line one\n2024-03-04T05:06:08.000000000Z line two\n"
	got := collect(t, func(ctx context.Context, out chan<- model.LogLine) error {
		return streamLogs(ctx, strings.NewReader(input), true, "docker:x", &dockerMark{}, out)
	})

	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %+v", len(got), got)
	}
	want := []string{"line one", "line two"}
	for i, ll := range got {
		if ll.Stream != model.Stdout {
			t.Errorf("line %d stream = %v, want Stdout", i, ll.Stream)
		}
		if ll.Raw != want[i] {
			t.Errorf("line %d = %q, want %q", i, ll.Raw, want[i])
		}
	}
}

// TestReadDockerStreamMarksOnlyWhatItDelivered: the mark is where a restart resumes,
// so it must advance for a line that reached out and must NOT for a line read but cut
// off by cancellation (a die), or that line would be lost on the re-attach. Whether a
// cancelled read gets as far as the send is up to the scheduler, so the undelivered
// half repeats to make a mark-before-send mutant fail rather than pass by luck.
func TestReadDockerStreamMarksOnlyWhatItDelivered(t *testing.T) {
	const line = "2024-03-04T05:06:07.000000005Z hello\n"
	stamp, _ := time.Parse(time.RFC3339Nano, "2024-03-04T05:06:07.000000005Z")

	t.Run("delivered", func(t *testing.T) {
		var mark streamMark
		out := make(chan model.LogLine, 1)
		if err := readDockerStream(context.Background(), strings.NewReader(line), "docker:x", model.Stdout, &mark, out); err != nil {
			t.Fatal(err)
		}
		if want := (streamMark{at: stamp, n: 1}); mark != want {
			t.Errorf("mark = %+v, want %+v", mark, want)
		}
	})

	t.Run("cut off by cancel", func(t *testing.T) {
		prev := streamMark{at: stamp.Add(-time.Second), n: 2}
		for i := 0; i < 200; i++ {
			mark := prev
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			out := make(chan model.LogLine) // nobody reads it
			_ = readDockerStream(ctx, strings.NewReader(line), "docker:x", model.Stdout, &mark, out)
			if mark != prev {
				t.Fatalf("run %d: mark = %+v, want it left at %+v", i, mark, prev)
			}
		}
	})
}

// stdWriter writes the daemon's multiplexed format, which stdcopy.StdCopy reads: each Write
// is one frame, an 8-byte header (byte 0 the stream, bytes 4-7 the payload length, big
// endian) and then the payload. The api module no longer exports the daemon's writer
// (stdcopy.NewStdWriter), so the tests carry their own.
type stdWriter struct {
	w  io.Writer
	st stdcopy.StdType
}

func newStdWriter(w io.Writer, st stdcopy.StdType) io.Writer { return &stdWriter{w, st} }

// Write sends header and payload in one Write, and counts payload bytes only.
func (sw *stdWriter) Write(p []byte) (int, error) {
	frame := make([]byte, 8, 8+len(p))
	frame[0] = byte(sw.st)
	binary.BigEndian.PutUint32(frame[4:], uint32(len(p)))
	n, err := sw.w.Write(append(frame, p...))
	return max(n-8, 0), err
}

// stampedFrame returns ONE multiplexed frame on stream st holding n short lines, each with
// its own daemon stamp a microsecond apart from base, plus those stamps.
func stampedFrame(t *testing.T, st stdcopy.StdType, prefix string, n int, base time.Time) ([]byte, []time.Time) {
	t.Helper()
	var payload bytes.Buffer
	stamps := make([]time.Time, n)
	for i := range n {
		stamps[i] = base.Add(time.Duration(i) * time.Microsecond)
		fmt.Fprintf(&payload, "%s %s %d\n", stamps[i].Format(rfc3339NanoFixed), prefix, i+1)
	}
	var frame bytes.Buffer
	if _, err := newStdWriter(&frame, st).Write(payload.Bytes()); err != nil {
		t.Fatal(err)
	}
	return frame.Bytes(), stamps
}

// liveBody stands in for a follow stream as the Docker client returns it: it yields
// frames, then stays open like a running container's log, and fails with ctx's error once
// ctx is cancelled, as the HTTP body does when the request's context ends.
func liveBody(ctx context.Context, frames ...[]byte) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		for _, f := range frames {
			if _, err := pw.Write(f); err != nil {
				return
			}
		}
	}()
	context.AfterFunc(ctx, func() { pw.CloseWithError(ctx.Err()) })
	return pr
}

// onlyCanceled reports whether err is non-nil and every error joined into it is a
// cancellation: what a follower stopped by its context must return, and nothing else.
func onlyCanceled(err error) bool {
	if err == nil {
		return false
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			if e != nil && !onlyCanceled(e) {
				return false
			}
		}
		return true
	}
	return errors.Is(err, context.Canceled)
}

// TestStreamLogsCancelIsQuiet is #84. A non-TTY follower demuxes through two pipes, and a
// pipe write returns only once its reader has consumed every byte. When the follower is
// cancelled while the demuxer is writing a frame its reader has stopped reading — the
// reader returned at its next send — that write used to block forever: the follower never
// returned, so a restart of its container was never followed again.
//
// The frame here is 2,000 short lines, so all but about 4 KB of it is still unconsumed
// when the one line read from out leaves the reader blocked on the next send. The control
// is the same cancel with no write pending. Both must return promptly, return nothing but
// cancellations (so a die stays as quiet as it always was), and mark only the line that
// reached out: the line held in the blocked send was read but not delivered, and a
// re-attach must send it again (#80).
func TestStreamLogsCancelIsQuiet(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tt := range []struct {
		name  string
		lines int
	}{
		{"no write pending", 1},
		{"write pending", 2000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			frame, stamps := stampedFrame(t, stdcopy.Stdout, "line", tt.lines, base)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := make(chan model.LogLine) // read once, then nobody reads it
			var mark dockerMark
			done := make(chan error, 1)
			go func() { done <- streamLogs(ctx, liveBody(ctx, frame), false, "docker:x", &mark, out) }()

			select {
			case <-out:
			case <-time.After(2 * time.Second):
				t.Fatal("no line arrived")
			}
			cancel()
			var err error
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("streamLogs did not return within 2s of cancel: the demuxer is blocked writing to a pipe whose reader has stopped")
			}
			if !onlyCanceled(err) {
				t.Errorf("streamLogs = %v, want only context.Canceled", err)
			}
			if want := (dockerMark{stdout: streamMark{at: stamps[0], n: 1}}); mark != want {
				t.Errorf("mark = %+v, want only the delivered line: %+v", mark, want)
			}
		})
	}
}

// TestStreamLogsStdoutEndingFirstKeepsStderr: closing a pipe's read end when its reader
// returns must not cut the OTHER stream short. Stdout's frames all come first, then 2,000
// stderr frames, more than a reader buffers, then the end of the stream. Every line
// arrives on its own stream, in order, and the end of the stream is a clean nil.
func TestStreamLogsStdoutEndingFirstKeepsStderr(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var buf bytes.Buffer
	var wantOut, wantErr []string
	for i := range 2 {
		f, _ := stampedFrame(t, stdcopy.Stdout, fmt.Sprintf("out %d", i+1), 1, base.Add(time.Duration(i)*time.Millisecond))
		buf.Write(f)
		wantOut = append(wantOut, fmt.Sprintf("out %d 1", i+1))
	}
	for i := range 2000 {
		f, _ := stampedFrame(t, stdcopy.Stderr, fmt.Sprintf("err %d", i+1), 1, base.Add(time.Second+time.Duration(i)*time.Millisecond))
		buf.Write(f)
		wantErr = append(wantErr, fmt.Sprintf("err %d 1", i+1))
	}

	got := collect(t, func(ctx context.Context, out chan<- model.LogLine) error {
		return streamLogs(ctx, &buf, false, "docker:x", &dockerMark{}, out)
	})

	if got := rawsOf(got, model.Stdout); !slices.Equal(got, wantOut) {
		t.Errorf("stdout = %q, want %q", got, wantOut)
	}
	if got := rawsOf(got, model.Stderr); !slices.Equal(got, wantErr) {
		t.Errorf("stderr delivered %d lines, want all %d in order", len(got), len(wantErr))
	}
}
