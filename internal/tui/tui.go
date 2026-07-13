// SPDX-License-Identifier: Apache-2.0

// Package tui renders the live templated stream with Bubble Tea.
//
// The TUI is a pure consumer of pipeline.Snapshot: the pipeline goroutine owns all
// state and hands over immutable copies on its own cadence, so rendering is
// decoupled from the event rate and a slow terminal can never apply backpressure
// to ingestion. Nothing here may write to stdout — see Run.
//
// TODO(M5): flagged-event cards pane beside the stream, card expansion, severity
// badges. View() already composes panes, so it slots in without restructuring.
package tui

import (
	"context"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/maxie7/logscry/internal/pipeline"
)

// Options configures the view.
type Options struct {
	// ExplainDryRun surfaces the events the scorer would have escalated. Escalations
	// are decided and counted either way; this only decides whether they are shown.
	ExplainDryRun bool
	// Output is where the program draws. Nil means os.Stdout, which is what production
	// wants; the tests point it at a pty so they can drive the real program and read
	// what it actually rendered.
	Output io.Writer
}

// StdinIsTerminal reports whether stdin is an interactive terminal rather than a pipe
// carrying logs. A caller that has stdin as its only source uses this to tell "no logs
// were piped in" from "logs are on the way".
func StdinIsTerminal() bool { return isTerminal(os.Stdin) }

// Run starts the terminal UI and blocks until the user quits or ctx is cancelled.
//
// snaps delivers pipeline state; errs delivers background errors, which are shown
// in the status bar rather than printed, because any stray write to stdout would
// corrupt the alternate screen.
//
// in is the keyboard source: nil to let Bubble Tea read stdin as usual, or an open
// /dev/tty when stdin is busy carrying logs (see Resolve). Run does not close it.
func Run(ctx context.Context, snaps <-chan pipeline.Snapshot, errs <-chan error, in *os.File, o Options) error {
	opts := []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithContext(ctx), // SIGINT/SIGTERM cancels ctx, which stops the program
	}
	if in != nil {
		opts = append(opts, tea.WithInput(in))
	}
	if o.Output != nil {
		opts = append(opts, tea.WithOutput(o.Output))
	}

	p := tea.NewProgram(New(snaps, errs, o), opts...)
	_, err := p.Run()
	// A cancelled context is the ordinary Ctrl-C shutdown path, not a failure.
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}
