// SPDX-License-Identifier: Apache-2.0

//go:build linux

package tui

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"
	"golang.org/x/sys/unix"

	"github.com/maxie7/logscry/internal/pipeline"
)

// fakeTerminal is a pty whose far end plays a terminal: it answers OSC 11 with osc, unless osc is
// empty, and the cursor-position query when dsr is set. It records what it was asked and the
// slave's mode at the moment the colour question arrived.
type fakeTerminal struct {
	slave *os.File

	mu       sync.Mutex
	asked    strings.Builder
	duringQ  *unix.Termios
	question chan struct{} // closed when OSC 11 arrives
}

func newFakeTerminal(t *testing.T, osc string, dsr bool) *fakeTerminal {
	t.Helper()
	master, slave := newPTY(t, 80)
	ft := &fakeTerminal{slave: slave, question: make(chan struct{})}
	go func() {
		buf := make([]byte, 256)
		var once sync.Once
		for {
			n, err := master.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				ft.mu.Lock()
				ft.asked.WriteString(chunk)
				ft.mu.Unlock()
				if strings.Contains(chunk, "\x1b]11;?") {
					once.Do(func() {
						mode, _ := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
						ft.mu.Lock()
						ft.duringQ = mode
						ft.mu.Unlock()
						close(ft.question)
					})
					if osc != "" {
						_, _ = master.WriteString(osc)
					}
				}
				if dsr && strings.Contains(chunk, "\x1b[6n") {
					_, _ = master.WriteString("\x1b[1;1R")
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return ft
}

// wasAsked reports whether the colour question reached the terminal.
func (ft *fakeTerminal) wasAsked() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return strings.Contains(ft.asked.String(), "\x1b]11;?")
}

// mode reads the terminal's current mode.
func (ft *fakeTerminal) mode(t *testing.T) unix.Termios {
	t.Helper()
	m, err := unix.IoctlGetTermios(int(ft.slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("tcgetattr: %v", err)
	}
	return *m
}

// inForeground makes the test's pty count as this process's foreground terminal, which a pty
// that is not the controlling terminal never is: without it nothing is ever asked.
func inForeground(t *testing.T) {
	t.Helper()
	restore := isForeground
	isForeground = func(int) bool { return true }
	t.Cleanup(func() { isForeground = restore })
}

// plainEnv clears what termenv reads from the environment, so that a developer's own terminal or
// GitHub's CI=true cannot decide a case.
func plainEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CI", "")
	t.Setenv("COLORFGBG", "")
}

// termenvAnswer is what termenv itself, unbounded, makes of the same terminal: the answer lipgloss
// used to get from bubbletea's init.
func termenvAnswer(t *testing.T, osc string) bool {
	t.Helper()
	ft := newFakeTerminal(t, osc, true)
	state, err := term.MakeRaw(ft.slave.Fd())
	if err != nil {
		t.Fatalf("raw: %v", err)
	}
	defer func() { _ = term.Restore(ft.slave.Fd(), state) }()
	got := make(chan bool, 1)
	go func() { got <- termenv.NewOutput(ft.slave, termenv.WithUnsafe()).HasDarkBackground() }()
	select {
	case dark := <-got:
		return dark
	case <-time.After(5 * time.Second):
		t.Fatal("termenv got no answer from a terminal that answers")
		return false
	}
}

// TestBackgroundQueryMatchesTermenv: on a terminal that answers, the bounded question must reach
// the same verdict termenv reached when bubbletea's init asked (#155), so that every AdaptiveColor
// resolves as it did. Each case is compared with termenv on an identical terminal, and with the
// verdict written down, so a case cannot pass by both being wrong.
func TestBackgroundQueryMatchesTermenv(t *testing.T) {
	cases := []struct {
		name string
		osc  string // the answer to OSC 11; empty: the terminal cannot report its colour
		env  map[string]string
		dark bool
	}{
		{"black", "\x1b]11;rgb:0000/0000/0000\x1b\\", nil, true},
		{"white", "\x1b]11;rgb:ffff/ffff/ffff\x1b\\", nil, false},
		{"just dark", "\x1b]11;rgb:7f7f/7f7f/7f7f\x1b\\", nil, true},   // lightness 0.498
		{"just light", "\x1b]11;rgb:8080/8080/8080\x1b\\", nil, false}, // lightness 0.502
		{"solarized dark", "\x1b]11;rgb:0000/2b2b/3636\x1b\\", nil, true},
		{"solarized light", "\x1b]11;rgb:fdfd/f6f6/e3e3\x1b\\", nil, false},
		{"BEL terminated", "\x1b]11;rgb:ffff/ffff/ffff\a", nil, false},
		// termenv reads only four-digit components; anything else falls back to black.
		{"two-digit components", "\x1b]11;rgb:ff/ff/ff\x1b\\", nil, true},
		{"no colour report", "", nil, true},
		{"no colour report, COLORFGBG", "", map[string]string{"COLORFGBG": "0;15"}, false},
		{"answer beats COLORFGBG", "\x1b]11;rgb:ffff/ffff/ffff\x1b\\", map[string]string{"COLORFGBG": "15;0"}, false},
		// termenv asks screen and tmux nothing: they may be attached to several terminals.
		{"TERM=screen", "\x1b]11;rgb:ffff/ffff/ffff\x1b\\", map[string]string{"TERM": "screen"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plainEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			inForeground(t)

			ft := newFakeTerminal(t, tc.osc, true)
			got := hasDarkBackground(ft.slave)
			want := termenvAnswer(t, tc.osc)

			if got != want {
				t.Errorf("bounded question says dark=%v, termenv says dark=%v", got, want)
			}
			if got != tc.dark {
				t.Errorf("dark = %v, want %v", got, tc.dark)
			}
			if asked := ft.wasAsked(); asked == (tc.env["TERM"] == "screen") {
				t.Errorf("terminal asked = %v; only TERM=screen goes unasked", asked)
			}
		})
	}
}

// TestBackgroundQueryIsBounded: a terminal that answers nothing at all costs the bound, not
// termenv's 5 s per byte (#155), and the verdict is termenv's fallback.
func TestBackgroundQueryIsBounded(t *testing.T) {
	restore := backgroundQueryTimeout
	backgroundQueryTimeout = 100 * time.Millisecond
	t.Cleanup(func() { backgroundQueryTimeout = restore })

	for _, tc := range []struct {
		colorfgbg string
		dark      bool
	}{{"", true}, {"0;15", false}} {
		t.Run("COLORFGBG="+tc.colorfgbg, func(t *testing.T) {
			plainEnv(t)
			t.Setenv("COLORFGBG", tc.colorfgbg)
			inForeground(t)
			ft := newFakeTerminal(t, "", false)

			start := time.Now()
			dark := hasDarkBackground(ft.slave)
			elapsed := time.Since(start)

			if !ft.wasAsked() {
				t.Fatal("the terminal was never asked, so the bound was never tested")
			}
			if elapsed < backgroundQueryTimeout {
				t.Errorf("gave up after %v, before the %v bound", elapsed, backgroundQueryTimeout)
			}
			if elapsed > time.Second {
				t.Errorf("took %v on a silent terminal with a %v bound", elapsed, backgroundQueryTimeout)
			}
			if dark != tc.dark {
				t.Errorf("dark = %v, want termenv's fallback %v", dark, tc.dark)
			}
		})
	}
}

// TestBackgroundQueryRestoresTheMode: asking puts the terminal in raw mode, and every way out of
// the question must hand the terminal back in the mode it was in: an answer, a timeout, a failed
// read and a panic. A terminal left raw has no echo and no line editing after logscry exits.
func TestBackgroundQueryRestoresTheMode(t *testing.T) {
	restoreTimeout := backgroundQueryTimeout
	backgroundQueryTimeout = 100 * time.Millisecond
	t.Cleanup(func() { backgroundQueryTimeout = restoreTimeout })

	white := "\x1b]11;rgb:ffff/ffff/ffff\x1b\\"
	cases := []struct {
		name      string
		osc       string
		dsr       bool
		read      func(*os.File, []byte) (int, error) // nil: the real read
		wantPanic bool
	}{
		{"answer", white, true, nil, false},
		{"timeout", "", false, nil, false},
		{"read error", white, true, func(*os.File, []byte) (int, error) { return 0, errors.New("read failed") }, false},
		{"panic", white, true, func(*os.File, []byte) (int, error) { panic("read panicked") }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plainEnv(t)
			inForeground(t)
			if tc.read != nil {
				restore := readTTY
				readTTY = tc.read
				t.Cleanup(func() { readTTY = restore })
			}
			ft := newFakeTerminal(t, tc.osc, tc.dsr)
			before := ft.mode(t)

			panicked := func() (p bool) {
				defer func() { p = recover() != nil }()
				hasDarkBackground(ft.slave)
				return false
			}()

			if panicked != tc.wantPanic {
				t.Fatalf("panicked = %v, want %v", panicked, tc.wantPanic)
			}
			<-ft.question
			ft.mu.Lock()
			during := ft.duringQ
			ft.mu.Unlock()
			if during == nil || *during == before {
				t.Fatal("the terminal was not in a different mode while asked, so the restore was never tested")
			}
			if after := ft.mode(t); after != before {
				t.Errorf("the terminal's mode after asking differs from before:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}

// TestPTYTUIFollowsTheTerminalsBackground runs the TUI through the production path on a terminal
// that answers light, and on one that answers dark, and checks the colour the focused pane's
// border is drawn in: the AdaptiveColor's Light variant on the one, its Dark on the other, as
// before #155.
func TestPTYTUIFollowsTheTerminalsBackground(t *testing.T) {
	// colFocus as lipgloss writes it in the TrueColor profile: Light #0F766E, Dark #5EEAD4 (whose
	// blue comes out as 211 after lipgloss's conversion).
	const lightFocus, darkFocus = "38;2;15;118;110", "38;2;94;234;211"
	cases := []struct {
		name, osc   string
		dark        bool
		want, avoid string
	}{
		{"light", "\x1b]11;rgb:fafa/fafa/fafa\x1b\\", false, lightFocus, darkFocus},
		{"dark", "\x1b]11;rgb:1c1c/1c1c/1c1c\x1b\\", true, darkFocus, lightFocus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plainEnv(t)
			inForeground(t)
			profile := lipgloss.ColorProfile()
			lipgloss.SetColorProfile(termenv.TrueColor)
			wasDark := lipgloss.HasDarkBackground()
			t.Cleanup(func() {
				lipgloss.SetColorProfile(profile)
				lipgloss.SetHasDarkBackground(wasDark)
			})

			snaps := make(chan pipeline.Snapshot, 1)
			snaps <- testSnapshot()
			send, exited, output := runOnPTYWith(t, snaps, Options{}, 120, tc.osc)
			waitFor(t, "the first frame", func() bool { return strings.Contains(output(), "user <NUM> failed") })

			if got := lipgloss.HasDarkBackground(); got != tc.dark {
				t.Errorf("lipgloss dark = %v, want %v", got, tc.dark)
			}
			if out := output(); !strings.Contains(out, tc.want) || strings.Contains(out, tc.avoid) {
				t.Errorf("focused border: want colour %s and not %s in the frame", tc.want, tc.avoid)
			}

			send("q")
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				t.Fatal("'q' did not quit")
			}
		})
	}
}
