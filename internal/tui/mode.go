// SPDX-License-Identifier: Apache-2.0

package tui

import (
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

// Resolve inspects the process's actual stdin/stdout and applies Decide, returning the
// keyboard source for the caller to hand to tea.WithInput and to close afterwards.
//
// A nil file means "let Bubble Tea read os.Stdin", which now happens only when there
// is no controlling terminal to open. Preferring /dev/tty unconditionally is
// deliberate: it is one input path instead of two, and it is the path that works when
// stdin is busy carrying logs — so the TUI behaves identically whether logs arrive on
// stdin, from a subprocess, or from Docker.
func Resolve(plainFlag bool) (Mode, *os.File) {
	return resolve(plainFlag, os.Stdin, os.Stdout)
}

func resolve(plainFlag bool, stdin, stdout *os.File) (Mode, *os.File) {
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
	return mode, tty
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
