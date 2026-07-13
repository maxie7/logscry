// SPDX-License-Identifier: Apache-2.0

//go:build linux

package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/maxie7/logscry/internal/pipeline"
)

// These tests drive the REAL tea.Program on a REAL terminal, because the bug they
// exist for was invisible to every test that came before: the mode matrix asserted
// which mode was CHOSEN, never that a keystroke could actually be READ in it. The TUI
// rendered perfectly and ignored every key, and the suite was green throughout.

// newPTY returns the master and slave ends of a fresh pseudo-terminal. The slave is a
// genuine tty, which is what makes Bubble Tea enter raw mode and read keys at all.
func newPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()

	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil { // unlockpt
		_ = m.Close()
		t.Fatalf("unlockpt: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN) // ptsname
	if err != nil {
		_ = m.Close()
		t.Fatalf("ptsname: %v", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		t.Fatalf("open pts: %v", err)
	}
	// Give it a size, or Bubble Tea renders into a zero-width screen and draws nothing.
	_ = unix.IoctlSetWinsize(int(s.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 100})

	t.Cleanup(func() { _ = s.Close(); _ = m.Close() })
	return m, s
}

// answerQueries drains the program's output and replies to the terminal queries Bubble
// Tea sends at startup (cursor position, background colour). A real terminal answers
// them; a test that does not will watch the program block before it ever reads a key,
// and will blame the wrong thing.
func answerQueries(t *testing.T, master *os.File, out *strings.Builder, mu *sync.Mutex, done <-chan struct{}) {
	t.Helper()
	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := master.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				mu.Lock()
				out.WriteString(chunk)
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
}

// runOnPTY starts tui.Run against a pty, exactly as main does when the keyboard comes
// from the controlling terminal — the branch used for a Docker or subprocess source,
// where stdin is not carrying logs. It returns a function to send keys and a channel
// that closes when the program exits.
func runOnPTY(t *testing.T, snaps chan pipeline.Snapshot) (send func(string), exited chan error, output func() string) {
	t.Helper()

	master, slave := newPTY(t)
	var (
		mu  sync.Mutex
		out strings.Builder
	)
	done := make(chan struct{})
	answerQueries(t, master, &out, &mu, done)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	errs := make(chan error)
	exited = make(chan error, 1)

	go func() {
		// slave is both the keyboard and the screen, which is what a terminal is.
		exited <- Run(ctx, snaps, errs, slave, Options{Output: slave})
		close(done)
	}()
	t.Cleanup(cancel)

	return func(keys string) { _, _ = master.WriteString(keys) },
		exited,
		func() string { mu.Lock(); defer mu.Unlock(); return out.String() }
}

// waitFor polls until cond holds, so the tests never sleep for a fixed duration and
// hope.
func waitFor(t *testing.T, what string, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %s", what)
	return false
}

// TestPTYQuitKeyActuallyQuits is the regression test for the dead-keyboard bug. The TUI
// rendered, the status bar ticked, the footer advertised "q quit" — and q did nothing,
// because nothing ever checked that the input we hand Bubble Tea is one it can read.
func TestPTYQuitKeyActuallyQuits(t *testing.T) {
	snaps := make(chan pipeline.Snapshot, 1)
	snaps <- testSnapshot()

	send, exited, output := runOnPTY(t, snaps)

	waitFor(t, "the first frame", func() bool { return strings.Contains(output(), "user <NUM> failed") })

	send("q")
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("'q' did not quit: the program rendered but never read the keyboard")
	}
}

// TestPTYViewKeysWork: the other advertised keys have to work too, and they prove keys
// are being read rather than the program merely dying on something.
func TestPTYViewKeysWork(t *testing.T) {
	snaps := make(chan pipeline.Snapshot, 1)
	snaps <- testSnapshot()

	send, exited, output := runOnPTY(t, snaps)
	waitFor(t, "the first frame", func() bool { return strings.Contains(output(), "STREAM") })

	send("t")
	waitFor(t, "'t' to switch to the aggregated view", func() bool {
		return strings.Contains(output(), "AGGREGATED")
	})

	send("p")
	waitFor(t, "'p' to pause", func() bool { return strings.Contains(output(), "PAUSED") })

	send("q")
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("'q' did not quit")
	}
}
