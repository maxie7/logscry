// SPDX-License-Identifier: Apache-2.0

package tui

import "os"

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
// stdin is contended: it is both a log source (tail -f x | logscry) and where
// Bubble Tea normally reads keystrokes. The matrix resolves that:
//
//	--plain  stdout TTY  stdin TTY  /dev/tty | mode   keyboard comes from
//	-------  ----------  ---------  -------- | -----  ------------------------
//	yes      *           *          *        | plain  -
//	no       no          *          *        | plain  -  (output is piped: never
//	                                         |           write escape codes into it)
//	no       yes         yes        *        | tui    os.Stdin (Bubble Tea default)
//	no       yes         no         yes      | tui    /dev/tty (stdin carries logs)
//	no       yes         no         no       | plain  -  (no controlling terminal,
//	                                         |           e.g. CI: no way to read keys)
func Decide(plainFlag, stdinIsTTY, stdoutIsTTY, devTTYAvailable bool) Mode {
	switch {
	case plainFlag:
		return ModePlain
	case !stdoutIsTTY:
		return ModePlain
	case stdinIsTTY:
		return ModeTUI
	case devTTYAvailable:
		return ModeTUI
	default:
		return ModePlain
	}
}

// Resolve inspects the process's actual stdin/stdout and applies Decide. When the
// TUI needs a keyboard source other than stdin (because stdin is carrying logs),
// it returns an open /dev/tty for the caller to hand to tea.WithInput and to
// close afterwards; otherwise the file is nil and Bubble Tea reads stdin itself.
func Resolve(plainFlag bool) (Mode, *os.File) {
	stdinIsTTY := isTerminal(os.Stdin)
	stdoutIsTTY := isTerminal(os.Stdout)

	// Only one row of the matrix needs /dev/tty, so only that row pays for opening
	// it. Decide still has the final say, so the logic lives in exactly one place.
	var tty *os.File
	if !plainFlag && stdoutIsTTY && !stdinIsTTY {
		if f, err := os.Open("/dev/tty"); err == nil {
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

// isTerminal reports whether f is a character device — i.e. a terminal rather
// than a pipe, a file, or /dev/null. Stdlib only; no isatty dependency.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
