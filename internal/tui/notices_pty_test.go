// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/ingest"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// TestPTYStalledScreenDoesNotStallNotices is why a running source's notices never wait
// (#100). The TUI reads errs on its event loop, and the loop waits for the renderer, which
// waits for the terminal. A screen nobody drains therefore stops the TUI reading errs while
// the run goes on. A source that keeps reconnecting sends a notice per attempt; here it
// sends 100 with the screen stalled. Every Send must return, and once the screen drains, the
// status line shows a kept notice.
func TestPTYStalledScreenDoesNotStallNotices(t *testing.T) {
	master, slave := newPTY(t, 120)
	var (
		mu      sync.Mutex
		screen  strings.Builder
		stalled atomic.Bool
	)
	go func() {
		buf := make([]byte, 4096)
		for {
			for stalled.Load() {
				time.Sleep(5 * time.Millisecond)
			}
			n, err := master.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				mu.Lock()
				screen.WriteString(chunk)
				mu.Unlock()
				if strings.Contains(chunk, "\x1b[6n") {
					_, _ = master.WriteString("\x1b[1;1R")
				}
				if strings.Contains(chunk, "\x1b]11;?") {
					_, _ = master.WriteString("\x1b]11;rgb:0000/0000/0000\x1b\\")
				}
			}
			if err != nil {
				return
			}
		}
	}()
	restore := openControllingTTY
	openControllingTTY = func() (*os.File, error) { return slave, nil }
	t.Cleanup(func() { openControllingTTY = restore })
	term := resolve(false, slave, slave)

	// As main sizes it: one source's error, three one-shot notices, and the notices' room.
	const room = 8
	errs := make(chan error, 1+3+room)
	notices := ingest.NewNotices(errs, room)
	snaps := make(chan pipeline.Snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	exited := make(chan error, 1)
	go func() { exited <- term.Run(ctx, snaps, errs, Options{}) }()
	quit := false
	t.Cleanup(func() {
		stalled.Store(false)
		if !quit {
			cancel()
			<-exited
		}
		cancel()
	})

	// Let the program start, then stop reading the screen and feed it frames that differ in
	// every row, until it stops taking snapshots: its loop is blocked on the terminal.
	time.Sleep(300 * time.Millisecond)
	stalled.Store(true)
	var fed atomic.Int64
	go func() {
		for i := 0; ; i++ {
			var s pipeline.Snapshot
			for j := range 30 {
				raw := fmt.Sprintf("frame %d row %d %s", i, j, strings.Repeat("y", 80))
				s.Lines = append(s.Lines, pipeline.Event{Line: model.LogLine{Source: "docker:app", Raw: raw}, Pattern: raw, Count: i})
			}
			s.Stats.TotalLines = i
			select {
			case snaps <- s:
				fed.Add(1)
			case <-ctx.Done():
				return
			}
		}
	}()
	for last := int64(-1); fed.Load() != last; {
		last = fed.Load()
		time.Sleep(300 * time.Millisecond)
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
		t.Fatal("the source stalled sending a notice while the screen was stalled")
	}
	// The waitForError command, already blocked on errs, may have taken one before the
	// loop could take its message.
	if len(errs) != room {
		t.Fatalf("errs holds %d, want the %d notices it has room for: the TUI was not stalled", len(errs), room)
	}
	if got := notices.Dropped(); got < 100-room-1 {
		t.Errorf("%d notice(s) dropped, want at least %d", got, 100-room-1)
	}
	select {
	case errs <- errors.New("ingest: a source's error"):
	default:
		t.Fatal("a source's error found errs full: the notices took its room")
	}

	stalled.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		shown := strings.Contains(screen.String(), "a source's error")
		mu.Unlock()
		if shown {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the status line never showed the source's error once the screen drained")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Quit as the user does, like every pty test: the graceful path waits for the input loop.
	_, _ = master.WriteString("q")
	select {
	case <-exited:
		quit = true
	case <-time.After(5 * time.Second):
		t.Fatal("q did not quit")
	}
}
