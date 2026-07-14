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

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// These tests drive the REAL tea.Program on a REAL terminal, because the bug they
// exist for was invisible to every test that came before: the mode matrix asserted
// which mode was CHOSEN, never that a keystroke could actually be READ in it. The TUI
// rendered perfectly and ignored every key, and the suite was green throughout.

// newPTY returns the master and slave ends of a fresh pseudo-terminal, cols wide. The
// slave is a genuine tty, which is what makes Bubble Tea enter raw mode and read keys at
// all — and the width is what decides which layout the program actually renders, so the
// stacked fallback can be driven through the same production path as the split.
func newPTY(t *testing.T, cols int) (master, slave *os.File) {
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
	_ = unix.IoctlSetWinsize(int(s.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: uint16(cols)})

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

// runOnPTY starts the TUI on a pty through the PRODUCTION wiring — Resolve, then
// Terminal.Run — which is the whole point of these tests.
//
// The previous version hand-rolled the program: it called Run with a keyboard file of
// its own choosing, so it proved only that Bubble Tea can read a pty someone hands it.
// It could not have caught main handing Bubble Tea the wrong input, which IS the
// dead-keyboard bug. Now the test stands a real pty in for the controlling terminal and
// for stdin/stdout, and then lets resolve() pick the keyboard exactly as it does in
// production. Whatever main gets, this gets.
func runOnPTY(t *testing.T, snaps chan pipeline.Snapshot, opts Options) (send func(string), exited chan error, output func() string) {
	t.Helper()
	return runOnPTYAt(t, snaps, opts, 120)
}

// runOnPTYAt is runOnPTY at a chosen width, for the tests that care which layout the
// program picks.
func runOnPTYAt(t *testing.T, snaps chan pipeline.Snapshot, opts Options, cols int) (send func(string), exited chan error, output func() string) {
	t.Helper()

	master, slave := newPTY(t, cols)
	var (
		mu  sync.Mutex
		out strings.Builder
	)
	done := make(chan struct{})
	answerQueries(t, master, &out, &mu, done)

	// The test binary has no controlling terminal of its own, so the pty stands in for
	// /dev/tty. Everything after this line is the production path.
	restore := openControllingTTY
	openControllingTTY = func() (*os.File, error) { return slave, nil }
	t.Cleanup(func() { openControllingTTY = restore })

	// Exactly what main does: resolve the terminal, then run on it. stdin is a free
	// terminal here — the Docker / subprocess case, where stdin carries no logs.
	term := resolve(false, slave, slave)
	if term.Mode != ModeTUI {
		t.Fatalf("resolve chose %v on a terminal, want ModeTUI", term.Mode)
	}
	if term.in == nil {
		t.Fatal("resolve produced no keyboard: Bubble Tea would fall back to os.Stdin, " +
			"which is the path that left the keyboard dead")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	errs := make(chan error)
	exited = make(chan error, 1)

	go func() {
		exited <- term.Run(ctx, snaps, errs, opts)
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

	send, exited, output := runOnPTY(t, snaps, Options{})

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

	send, exited, output := runOnPTY(t, snaps, Options{})
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

// TestPTYKeysWorkWithTheLLMPaneUp runs the keyboard test in the configuration a real M4
// run is actually in: an LLM attached, an escalation pinned, and an explanation landing
// underneath it — which grows the chrome and resizes the viewport out from under the
// event loop. Every earlier pty test ran with an empty pinned pane, so none of them
// would have noticed the keyboard dying only once a card was on screen.
func TestPTYKeysWorkWithTheLLMPaneUp(t *testing.T) {
	snaps := make(chan pipeline.Snapshot, 1)
	snaps <- escalationSnapshot(&model.Explanation{Hash: "aaa", State: model.ExplainPending})

	send, exited, output := runOnPTY(t, snaps, Options{Explain: true})
	waitFor(t, "the pending escalation to be pinned", func() bool {
		return strings.Contains(output(), "explaining…")
	})

	// The answer lands seconds later and takes a second line, resizing the viewport.
	snaps <- escalationSnapshot(&model.Explanation{
		Hash: "aaa", State: model.ExplainDone,
		Summary:     "A handler wrote to a nil map.",
		LikelyCause: "The cache map is never initialised.",
		Suggestion:  "Make the map in NewServer.",
	})
	waitFor(t, "the explanation to land on the card", func() bool {
		return strings.Contains(output(), "A handler wrote to a nil map.")
	})

	// And the keyboard still works, which is the whole assertion.
	send("t")
	waitFor(t, "'t' to still switch view with a card on screen", func() bool {
		return strings.Contains(output(), "AGGREGATED")
	})
	send("p")
	waitFor(t, "'p' to still pause with a card on screen", func() bool {
		return strings.Contains(output(), "PAUSED")
	})

	send("q")
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("'q' did not quit once an explanation was on screen")
	}
}

// TestPTYCardFlow is the M5 money shot driven end to end on a real terminal, through the
// production wiring: an anomaly fires and its card appears as "explaining…", the model
// answers and the card fills in, tab moves the focus to the cards, enter expands the
// selected one, and q leaves the terminal as it found it.
//
// Every step here is a thing a user does in the first thirty seconds of the demo, and
// each of them goes through Terminal.Run — the same path main takes, on a keyboard
// resolve() chose. A card that only expands under a hand-built tea.Program proves nothing.
func TestPTYCardFlow(t *testing.T) {
	snaps := make(chan pipeline.Snapshot, 1)
	snaps <- escalationSnapshot(&model.Explanation{Hash: "aaa", State: model.ExplainPending})

	send, exited, output := runOnPTY(t, snaps, Options{Explain: true})

	waitFor(t, "the card to appear as pending", func() bool {
		return strings.Contains(output(), "explaining…")
	})

	snaps <- escalationSnapshot(&model.Explanation{
		Hash: "aaa", State: model.ExplainDone,
		Summary:     "A handler wrote to a nil map.",
		LikelyCause: "The cache map is never initialised.",
		Suggestion:  "Make the map in NewServer.",
	})
	waitFor(t, "the answer to land on the card", func() bool {
		return strings.Contains(output(), "A handler wrote to a nil map.")
	})

	// The cause is behind the expand, so it must NOT be on screen yet.
	if strings.Contains(output(), "The cache map is never initialised.") {
		t.Error("the card was expanded before anyone pressed anything")
	}

	send("\t")
	waitFor(t, "focus to move to the cards", func() bool {
		return strings.Contains(output(), "↵ expand") // the footer follows the focus
	})

	send("\r")
	waitFor(t, "enter to expand the selected card", func() bool {
		return strings.Contains(output(), "The cache map is never initialised.")
	})

	send("q")
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("'q' did not quit with an expanded card on screen")
	}

	// And the terminal is handed back: Bubble Tea must leave the alternate screen, or the
	// user's shell comes back to a screen full of our frame.
	if !strings.Contains(output(), "\x1b[?1049l") {
		t.Error("the program did not leave the alternate screen on the way out")
	}
}

// TestPTYNarrowTerminalStacks drives the narrow fallback on a real 80-column terminal.
// Two panes at 80 columns would be two unusable 40-column columns, so the layout stacks
// instead — and the keyboard, the cards and the quit path all have to keep working there,
// because 80 columns is not an exotic terminal, it is the default one.
func TestPTYNarrowTerminalStacks(t *testing.T) {
	snaps := make(chan pipeline.Snapshot, 1)
	snaps <- escalationSnapshot(&model.Explanation{
		Hash: "aaa", State: model.ExplainDone, Summary: "A handler wrote to a nil map.",
	})

	send, exited, output := runOnPTYAt(t, snaps, Options{Explain: true}, 80)

	waitFor(t, "the stacked frame to render with its card", func() bool {
		out := output()
		return strings.Contains(out, "ANOMALIES") && strings.Contains(out, "A handler wrote to a nil map.")
	})
	// The stream is still there under the cards — stacking is a fallback, not an amputation.
	if !strings.Contains(output(), "STREAM") {
		t.Errorf("the stacked layout lost the stream pane:\n%s", output())
	}

	send("t")
	waitFor(t, "'t' to still toggle the view at 80 columns", func() bool {
		return strings.Contains(output(), "AGGREGATED")
	})

	send("q")
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("'q' did not quit in the stacked layout")
	}
}
