// SPDX-License-Identifier: Apache-2.0

// Package tui renders the live templated stream and the flagged-event cards with
// Bubble Tea.
//
// The TUI is a pure consumer of pipeline.Snapshot: the pipeline goroutine owns all
// state and hands over immutable copies on its own cadence, so rendering is
// decoupled from the event rate and a slow terminal can never apply backpressure
// to ingestion. Nothing here may write to stdout — see Run.
package tui

import (
	"context"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/maxie7/logscry/internal/pipeline"
)

// Options configures the view.
type Options struct {
	// ExplainDryRun surfaces the events the scorer would have escalated. Escalations
	// are decided and counted either way; this only decides whether they are shown.
	ExplainDryRun bool
	// Explain is whether an LLM stage is attached, which is what the cards pane and
	// the status bar need to know: whether an escalation is a question that is being
	// answered, or one nobody is going to answer.
	Explain bool
	// DockerTail is the --docker-tail replay limit, surfaced in the status bar when a
	// Docker source is attached. Empty when there is none. It is a string because "all"
	// is a legal value alongside a line count.
	DockerTail string
	// RemoteWarnHost is the off-box endpoint raw logs are being sent to with masking off.
	// Empty when there is nothing to warn about. Surfaced in the status bar because a
	// stderr line printed before the alternate screen starts would be wiped unseen — the
	// one place a TUI user would learn their logs are leaving the machine.
	RemoteWarnHost string
}

// StdinIsTerminal reports whether stdin is an interactive terminal rather than a pipe
// carrying logs. A caller that has stdin as its only source uses this to tell "no logs
// were piped in" from "logs are on the way".
func StdinIsTerminal() bool { return isTerminal(os.Stdin) }

// Run starts the terminal UI on t and blocks until the user quits or ctx is cancelled.
//
// snaps delivers pipeline state; errs delivers background errors, which are shown in the
// status bar rather than printed, because any stray write to stdout would corrupt the
// alternate screen.
//
// The keyboard and the screen come from t — the Terminal that Resolve produced — and
// from nowhere else. That is deliberate: this is the ONLY way to start the program, so
// the path a test drives is the path main drives, and a keyboard wired to something
// Bubble Tea cannot read cannot hide behind a green test. Run does not close t.
func (t Terminal) Run(ctx context.Context, snaps <-chan pipeline.Snapshot, errs <-chan error, o Options) error {
	opts := []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithContext(ctx), // SIGINT/SIGTERM cancels ctx, which stops the program
	}
	if t.in != nil {
		opts = append(opts, tea.WithInput(t.in))
	}
	if t.out != nil {
		opts = append(opts, tea.WithOutput(t.out))
	}

	p := tea.NewProgram(New(snaps, errs, o), opts...)
	_, err := p.Run()
	// A cancelled context is the ordinary Ctrl-C shutdown path, not a failure.
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}
