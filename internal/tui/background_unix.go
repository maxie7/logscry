// SPDX-License-Identifier: Apache-2.0

//go:build unix

package tui

import (
	"errors"
	"io"
	"os"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"
	"golang.org/x/sys/unix"
)

// backgroundQueryTimeout bounds the TUI's question to the terminal about its background colour.
//
// A terminal that answers does so in one round trip: well under a millisecond locally, a few
// hundred over a long SSH link. One that cannot report its colour still answers the cursor-position
// sentinel sent with it, at once. Only a terminal that answers nothing at all waits this out, once,
// before the TUI starts, against termenv's 5 s per byte (#155). A variable so that a test can wait
// less.
var backgroundQueryTimeout = time.Second

// isForeground reports whether this process is in the foreground of the terminal fd, as termenv
// decides it: only then may it change the terminal's mode and ask. A variable because a test's pty
// is never its controlling terminal.
var isForeground = func(fd int) bool {
	pgrp, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	return err == nil && pgrp == unix.Getpgrp()
}

// readTTY reads the terminal's answer. A variable so that a test can make a read fail or panic.
var readTTY = func(f *os.File, p []byte) (int, error) { return f.Read(p) }

// errNoAnswer is a terminal that said nothing before the deadline.
var errNoAnswer = errors.New("the terminal did not answer in time")

// setBackground tells lipgloss whether the screen the TUI draws on is dark, which decides every
// AdaptiveColor. Package init no longer asks (see internal/bgquery), so the TUI asks here, before
// Bubble Tea takes the terminal and before anything reads the keyboard.
func setBackground(out io.Writer) {
	if f, ok := out.(*os.File); ok {
		lipgloss.SetHasDarkBackground(hasDarkBackground(f))
	}
}

// hasDarkBackground asks the terminal f for its background colour and reports whether it is dark,
// waiting at most backgroundQueryTimeout for the answer.
//
// The question, the parsing of the answer, the light/dark rule and the fallback when there is no
// answer (COLORFGBG, else black) are termenv's own, so the answer is the one lipgloss used to get.
// Only the wait is ours: termenv is handed f behind boundedTTY, in unsafe mode, which makes it read
// through us instead of waiting 5 s per byte itself. Unsafe mode also skips termenv's checks before
// it asks, so the two that matter are made here first; where either fails, termenv decides without
// asking.
func hasDarkBackground(f *os.File) bool {
	fd := f.Fd()
	if os.Getenv("CI") != "" || !isForeground(int(fd)) {
		return termenv.NewOutput(f).HasDarkBackground()
	}
	// Raw, so that the answer is neither echoed nor held back until a newline. The terminal gets
	// its own mode back on every way out of here, a panic included.
	state, err := term.MakeRaw(fd)
	if err != nil {
		return termenv.NewOutput(f).HasDarkBackground()
	}
	defer func() { _ = term.Restore(fd, state) }()

	tty := boundedTTY{File: f, deadline: time.Now().Add(backgroundQueryTimeout)}
	return termenv.NewOutput(tty, termenv.WithUnsafe()).HasDarkBackground()
}

// boundedTTY is a terminal whose reads give up at one deadline shared by the whole question.
type boundedTTY struct {
	*os.File
	deadline time.Time
}

// Read waits until the terminal has something to read or the deadline passes, then reads.
func (b boundedTTY) Read(p []byte) (int, error) {
	if err := waitReadable(int(b.Fd()), b.deadline); err != nil {
		return 0, err
	}
	return readTTY(b.File, p)
}

// waitReadable waits for fd to become readable until deadline, with the select termenv itself
// uses, which, unlike poll, works on macOS terminals.
func waitReadable(fd int, deadline time.Time) error {
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return errNoAnswer
		}
		var fds unix.FdSet
		fds.Set(fd)
		tv := unix.NsecToTimeval(left.Nanoseconds())
		n, err := unix.Select(fd+1, &fds, nil, nil, &tv)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil:
			return err
		case n == 0:
			return errNoAnswer
		}
		return nil
	}
}
