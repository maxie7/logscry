// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// TestPlainStalledStdoutDoesNotStallNotices is why a running source's notices never wait
// (#100). --plain reads errs on the goroutine that writes stdout, so a stdout nobody drains,
// such as a paused pager, stops it reading errs while the run goes on. A source that keeps
// reconnecting sends a notice per attempt through errs; here it sends 100 while stdout is
// blocked. Every Send must return: past the room, notices are dropped and counted. The
// one-shot sends that share errs must still find room, and once stdout drains, what was
// kept is printed.
func TestPlainStalledStdoutDoesNotStallNotices(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stderr := captureStderr(t)
	saved := os.Stdout
	os.Stdout = w

	errs, notices := backgroundNotices(1)
	events := make(chan pipeline.Event)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- printPlain(ctx, events, errs, nil, nil, true) }()
	t.Cleanup(func() {
		cancel()
		_ = r.Close() // a write printPlain is blocked in fails, and it returns
		<-done
		os.Stdout = saved
		_ = w.Close()
	})

	// Feed events until printPlain stops taking them: it is blocked writing stdout.
	var fed atomic.Int64
	go func() {
		for i := 0; ; i++ {
			ev := pipeline.Event{Line: model.LogLine{Source: "docker:app"}, Count: i, Pattern: strings.Repeat("x", 200)}
			select {
			case events <- ev:
				fed.Add(1)
			case <-ctx.Done():
				return
			}
		}
	}()
	for last := int64(-1); fed.Load() != last; {
		last = fed.Load()
		time.Sleep(200 * time.Millisecond)
	}

	sent := make(chan struct{})
	go func() {
		for i := 1; i <= 100; i++ {
			notices.Send(fmt.Errorf("docker: events lost, reconnecting (attempt %d, next in 1s): events: EOF", i))
		}
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("the source stalled sending a notice while stdout was blocked")
	}
	if len(errs) != noticeRoom {
		t.Fatalf("errs holds %d, want the %d notices it has room for: printPlain was not stalled", len(errs), noticeRoom)
	}
	if got := notices.Dropped(); got != 100-noticeRoom {
		t.Errorf("%d notice(s) dropped, want %d", got, 100-noticeRoom)
	}
	select {
	case errs <- errors.New("ingest: a source's error"):
	default:
		t.Fatal("a source's error found errs full: the notices took its room")
	}

	go func() { _, _ = io.Copy(io.Discard, r) }()
	if !stderr.waitFor("a source's error", 2*time.Second) {
		t.Fatalf("stderr after stdout drained: %q, want the kept notices and the error", stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, fmt.Sprintf("(attempt %d,", noticeRoom)) || strings.Contains(got, fmt.Sprintf("(attempt %d,", noticeRoom+1)) {
		t.Errorf("stderr: %q, want attempts 1 to %d and no later one", got, noticeRoom)
	}
}
