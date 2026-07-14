// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/maxie7/logscry/internal/pipeline"
)

// maxErrors bounds the retained error list; only the most recent is displayed,
// the rest are kept for the M5 error pane.
const maxErrors = 20

// statusHeight is the number of lines the status bar occupies.
const statusHeight = 2

// How many recent escalations stay pinned. Fewer with an LLM attached, because an
// explained escalation is two lines rather than one and the pane must not eat the
// stream. Small on purpose either way: this is a calibration aid and a "the model is
// answering" signal, not the M5 cards pane.
const (
	escalationPaneMax    = 5
	escalationPaneMaxLLM = 3
)

// pinnedCount is how many escalations the pinned pane is currently showing. Zero until
// something actually escalates, so the pane costs a quiet run nothing — and zero when
// there is nothing to say about an escalation anyway: no model to explain it, and no
// dry run asking what would have been explained.
func (m Model) pinnedCount() int {
	limit := escalationPaneMax
	switch {
	case m.opts.ExplainDryRun:
	case m.opts.Explain:
		limit = escalationPaneMaxLLM
	default:
		return 0
	}
	return min(len(m.snap.Escalations), limit)
}

// chromeHeight is the number of lines not available to the viewport: the status bar,
// plus the pinned escalation pane and its header when there is one. It grows as
// escalations arrive and as their explanations land, so the viewport is resized on
// every snapshot (see resize).
func (m Model) chromeHeight() int {
	h := statusHeight
	if rows := m.escalationRows(); len(rows) > 0 {
		h += len(rows) + 1 // the escalations, plus their header
	}
	return h
}

// resize fits the viewport to whatever the chrome leaves it.
func (m *Model) resize() {
	m.vp.Width = m.width
	m.vp.Height = max(m.height-m.chromeHeight(), 0)
}

// viewMode selects which pane fills the screen.
type viewMode int

const (
	// streamView is the live tail of templated lines, newest at the bottom.
	streamView viewMode = iota
	// aggregatedView is the template table — the view that makes dedup visible.
	aggregatedView
)

// snapshotMsg carries a new pipeline state into the event loop.
type snapshotMsg pipeline.Snapshot

// streamEndedMsg means every source finished and no more snapshots are coming.
// It does not quit: the aggregated view stays readable until the user leaves.
type streamEndedMsg struct{}

// errMsg carries a background error (e.g. from ingestion) that must never be
// printed, because a stray write to stdout would corrupt the alternate screen.
type errMsg struct{ err error }

// Model is the Bubble Tea state. It renders exclusively from snap — it never
// touches the pipeline's template map, which another goroutine owns.
type Model struct {
	snaps <-chan pipeline.Snapshot
	errCh <-chan error
	opts  Options

	snap   pipeline.Snapshot
	mode   viewMode
	paused bool
	ended  bool
	errs   []string

	vp     viewport.Model
	width  int
	height int
	ready  bool // a WindowSizeMsg has arrived and the viewport is sized
}

// New builds the model over the pipeline's snapshot channel and the error channel
// that background goroutines report into.
func New(snaps <-chan pipeline.Snapshot, errs <-chan error, opts Options) Model {
	return Model{snaps: snaps, errCh: errs, opts: opts, vp: viewport.New(0, 0)}
}

// Init starts the two channel readers.
func (m Model) Init() tea.Cmd {
	return tea.Batch(waitForSnapshot(m.snaps), waitForError(m.errCh))
}

// waitForSnapshot blocks on the snapshot channel inside a tea.Cmd — off the event
// loop, which therefore never stalls — and re-issues itself after each delivery.
func waitForSnapshot(ch <-chan pipeline.Snapshot) tea.Cmd {
	return func() tea.Msg {
		snap, ok := <-ch
		if !ok {
			return streamEndedMsg{}
		}
		return snapshotMsg(snap)
	}
}

// waitForError does the same for background errors.
func waitForError(ch <-chan error) tea.Cmd {
	return func() tea.Msg {
		err, ok := <-ch
		if !ok {
			return nil // channel closed: stop listening, nothing more can fail
		}
		return errMsg{err}
	}
}

// Update handles one message. Snapshots keep arriving while paused — pause freezes
// rendering only, never the drain, and never ingestion.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "t":
			// Each mode has its own natural landing spot: the newest line at the
			// bottom of the stream, the loudest template at the top of the table.
			if m.mode == streamView {
				m.mode = aggregatedView
				m.refresh()
				m.vp.GotoTop()
			} else {
				m.mode = streamView
				m.refresh()
				m.vp.GotoBottom()
			}
			return m, nil
		case "p":
			m.paused = !m.paused
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg) // scrolling: up/down/pgup/pgdn
		return m, cmd

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		m.ready = true
		m.refresh()
		return m, nil

	case snapshotMsg:
		if !m.paused {
			m.snap = pipeline.Snapshot(msg)
			// The pinned pane grows with the first few escalations, taking its lines
			// out of the viewport's budget.
			m.resize()
			m.refresh()
		}
		return m, waitForSnapshot(m.snaps)

	case streamEndedMsg:
		m.ended = true
		return m, nil

	case errMsg:
		if msg.err != nil {
			m.errs = append(m.errs, msg.err.Error())
			if len(m.errs) > maxErrors {
				m.errs = m.errs[len(m.errs)-maxErrors:]
			}
		}
		return m, waitForError(m.errCh)
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// refresh re-renders the active pane into the viewport. The stream holds the tail
// — it follows new lines only while the user has not scrolled away from the bottom
// — whereas the aggregated table keeps its offset, since its interesting rows (the
// loudest templates) are at the top.
func (m *Model) refresh() {
	switch m.mode {
	case streamView:
		follow := m.vp.AtBottom()
		m.vp.SetContent(m.viewStream())
		if follow {
			m.vp.GotoBottom()
		}
	case aggregatedView:
		m.vp.SetContent(m.viewAggregated())
	}
}

// lastError returns the most recent error, or "" if there has been none.
func (m Model) lastError() string {
	if len(m.errs) == 0 {
		return ""
	}
	return m.errs[len(m.errs)-1]
}
