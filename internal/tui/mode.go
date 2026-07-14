// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"io"
	"os"

	"github.com/charmbracelet/x/term"
)

// Mode is how logscry renders: the full-screen TUI, or plain lines.
type Mode int

const (
	// ModeTUI runs the Bubble Tea program on the alternate screen.
	ModeTUI Mode = iota
	// ModePlain writes one line per event to stdout.
	ModePlain
)

// String renders the mode for diagnostics.
func (m Mode) String() string {
	if m == ModePlain {
		return "plain"
	}
	return "tui"
}

// Decide picks the render mode. It is pure so the whole matrix below is testable
// without a terminal; Resolve is its impure counterpart.
//
// stdin is contended: it is both a log source (tail -f x | logscry) and where Bubble
// Tea would normally read keystrokes. The resolution is to stop contending — when the
// TUI runs, the keyboard comes from the controlling terminal (/dev/tty), whatever
// stdin happens to be doing. os.Stdin is only a fallback for the exotic case of a
// process with a terminal on stdin but no controlling terminal to open.
//
//	--plain  stdout TTY  /dev/tty  stdin TTY | mode   keyboard comes from
//	-------  ----------  --------  --------- | -----  ---------------------------
//	yes      *           *         *         | plain  -
//	no       no          *         *         | plain  -  (output is piped: never
//	                                         |           write escape codes into it)
//	no       yes         yes       *         | tui    /dev/tty
//	no       yes         no        yes       | tui    os.Stdin (no controlling tty)
//	no       yes         no        no        | plain  -  (e.g. CI: no way to read keys)
func Decide(plainFlag, stdinIsTTY, stdoutIsTTY, devTTYAvailable bool) Mode {
	switch {
	case plainFlag:
		return ModePlain
	case !stdoutIsTTY:
		return ModePlain
	case devTTYAvailable:
		return ModeTUI
	case stdinIsTTY:
		return ModeTUI
	default:
		return ModePlain
	}
}

// openControllingTTY opens the process's controlling terminal. It is a variable so
// tests can drive Resolve without depending on whether the test runner happens to have
// a terminal attached.
var openControllingTTY = func() (*os.File, error) { return os.Open("/dev/tty") }

// Terminal is a resolved renderer: the mode to run in, and — bound to it — the keyboard
// and screen to run it with. Resolve produces one; Terminal.Run consumes it.
//
// The binding is the point. The keyboard and the program used to be wired together at
// the call site, which meant the tests could wire them together differently from main —
// and did, hand-rolling their own input file. So the pty test that exists precisely to
// catch a dead keyboard was passing while it was possible for main to hand Bubble Tea an
// input it could never read a key from. The fields are unexported and there is no other
// way to start the program: whatever the test drives is what main drives.
type Terminal struct {
	Mode Mode
	in   *os.File  // keyboard; nil means "let Bubble Tea read os.Stdin"
	out  io.Writer // screen
}

// Resolve inspects the process's actual stdin/stdout and applies Decide, returning the
// terminal to render on.
//
// A nil keyboard means "let Bubble Tea read os.Stdin", which happens only when there is
// no controlling terminal to open. Preferring /dev/tty unconditionally is deliberate: it
// is one input path instead of two, and it is the path that works when stdin is busy
// carrying logs — so the TUI behaves identically whether logs arrive on stdin, from a
// subprocess, or from Docker.
func Resolve(plainFlag bool) Terminal {
	return resolve(plainFlag, os.Stdin, os.Stdout)
}

func resolve(plainFlag bool, stdin, stdout *os.File) Terminal {
	stdinIsTTY := isTerminal(stdin)
	stdoutIsTTY := isTerminal(stdout)

	// Only a TUI needs a keyboard, so only a TUI pays for opening the terminal.
	var tty *os.File
	if !plainFlag && stdoutIsTTY {
		if f, err := openControllingTTY(); err == nil {
			tty = f
		}
	}

	mode := Decide(plainFlag, stdinIsTTY, stdoutIsTTY, tty != nil)
	if mode != ModeTUI && tty != nil {
		_ = tty.Close()
		tty = nil
	}
	return Terminal{Mode: mode, in: tty, out: stdout}
}

// Close releases the controlling terminal, if one was opened.
func (t Terminal) Close() error {
	if t.in == nil {
		return nil
	}
	return t.in.Close()
}

// isTerminal reports whether f is a terminal.
//
// It asks the terminal driver, via the same package Bubble Tea uses — so this decision
// and Bubble Tea's own behaviour cannot disagree. The obvious-looking stdlib shortcut,
// testing os.ModeCharDevice, is not a terminal test at all: /dev/null is a character
// device too, and calling it a terminal means handing Bubble Tea an input it will
// never be able to read keys from.
func isTerminal(f *os.File) bool {
	return f != nil && term.IsTerminal(f.Fd())
}
