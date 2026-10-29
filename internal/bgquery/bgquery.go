// SPDX-License-Identifier: Apache-2.0

// Package bgquery keeps bubbletea's package init from asking the terminal for its background
// colour (#155).
//
// bubbletea v1's init calls lipgloss.HasDarkBackground, which writes OSC 11 to stdout and, on a
// terminal that never answers, waits termenv's 5 s for a reply. Package init runs before main, so
// every invocation paid it: --version and --plain included, which draw nothing in colour. termenv
// asks nothing when TERM starts with "dumb", so this package sets TERM=dumb for the span of
// package init and main puts it back with Restore, first thing. The colour profile is unaffected:
// lipgloss reads TERM for it lazily, on the first render, after Restore. The TUI then asks the
// question itself, bounded (see tui.Terminal.Run).
//
// That only works if this package's init runs before termenv's, and the Go specification
// guarantees it does. Packages are initialized in import-path order, each as soon as all of its
// imports are: "the first uninitialized package in the list sorted by import path whose imports
// are all initialized". This package imports nothing but "os", which termenv imports too, so it
// is ready whenever termenv is, and "github.com/maxie7/…" sorts before "github.com/muesli/termenv".
// It therefore initializes before termenv, and so before lipgloss and bubbletea, which import it.
//
// An import that is ready later than termenv would break that silently. Two tests guard it:
// TestImportsOnlyOS, the cheap structural guard, holds this package to its single import, and
// TestStartupAsksTheTerminalNothing (cmd/logscry), the end-to-end guard, starts the real program
// on a terminal that never answers and fails if it is asked anything.
package bgquery

import "os"

// What init found, for Restore.
var (
	swapped bool   // init set TERM=dumb
	term    string // TERM before that
	hadTerm bool   // whether TERM was set at all
)

// Restore puts TERM back exactly as the process found it, unset if it was unset. main calls it
// before anything else, so that the configuration, every source, and the first render see the
// real TERM. It does nothing when init changed nothing.
func Restore() {
	if !swapped {
		return
	}
	swapped = false
	if hadTerm {
		_ = os.Setenv("TERM", term)
	} else {
		_ = os.Unsetenv("TERM")
	}
}
