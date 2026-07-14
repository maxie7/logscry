// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/maxie7/logscry/internal/pipeline"
)

// maxErrors bounds the retained error list; only the most recent is displayed.
const maxErrors = 20

// viewMode selects what the LEFT pane shows. The cards pane is not a mode — it is
// always there, which is the point of the layout.
type viewMode int

const (
	// streamView is the live tail of templated lines, newest at the bottom.
	streamView viewMode = iota
	// aggregatedView is the template table — the view that makes dedup visible.
	aggregatedView
)

// focus is which pane the arrow keys belong to. Two panes mean the keys need an owner,
// and the user needs to be able to see who has it (see paneStyle).
type focus int

const (
	focusStream focus = iota
	focusCards
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
	now   func() time.Time // injected so relative times are testable

	snap   pipeline.Snapshot
	mode   viewMode
	paused bool
	ended  bool
	errs   []string

	// The cards pane's selection is keyed by template hash, never by index. Escalations
	// arrive at the TOP of this list, so an index would silently slide the selection onto
	// a different card the moment a new one fired — under the cursor of someone reading.
	focus    focus
	selKey   string
	expanded map[string]bool

	// follow is the cards pane's scroll-lock: the selection is pinned to the newest
	// card, so new arrivals keep it there. It goes false the moment the user steps back
	// into older cards, and comes back when they return to the newest.
	follow bool

	// The panes. Both are viewports so that scrolling, clamping and clipping are the
	// library's problem rather than ours.
	stream viewport.Model
	cards  viewport.Model
	geom   geometry

	// Scroll-lock baselines: the counter each pane held when the user scrolled away
	// from its home edge, or -1 while the pane is still following. The difference from
	// the live counter is the "N new" the marker reports.
	streamDetach int
	cardsDetach  int

	width  int
	height int
	ready  bool // a WindowSizeMsg has arrived and the panes are sized
}

// New builds the model over the pipeline's snapshot channel and the error channel
// that background goroutines report into.
func New(snaps <-chan pipeline.Snapshot, errs <-chan error, opts Options) Model {
	return Model{
		snaps:        snaps,
		errCh:        errs,
		opts:         opts,
		now:          time.Now,
		expanded:     make(map[string]bool),
		follow:       true,
		stream:       viewport.New(0, 0),
		cards:        viewport.New(0, 0),
		streamDetach: -1,
		cardsDetach:  -1,
	}
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
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		m.ready = true
		m.refresh()
		return m, nil

	case snapshotMsg:
		if !m.paused {
			m.snap = pipeline.Snapshot(msg)
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
	return m, nil
}

// handleKey routes one keystroke. Every scroll key goes to the FOCUSED pane and nowhere
// else — with two panes on screen, an arrow that moves both of them is an arrow that
// moves neither on purpose.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "tab", "shift+tab":
		if m.focus == focusStream {
			m.focus = focusCards
		} else {
			m.focus = focusStream
		}
		return m, nil

	case "t":
		// Each mode has its own natural landing spot: the newest line at the bottom of
		// the stream, the loudest template at the top of the table.
		if m.mode == streamView {
			m.mode = aggregatedView
			m.refreshStream()
			m.stream.GotoTop()
		} else {
			m.mode = streamView
			m.streamDetach = -1 // back to following: the tail is the point of the tail
			m.refreshStream()
			m.stream.GotoBottom()
		}
		return m, nil

	case "p":
		m.paused = !m.paused
		return m, nil

	case "enter", " ":
		if m.focus == focusCards && m.selKey != "" {
			m.expanded[m.selKey] = !m.expanded[m.selKey]
			m.refreshCards()
		}
		return m, nil
	}

	// home and end are bound here rather than left to the viewport: bubbles ships no
	// default binding for either, so the footer would have been advertising two keys that
	// did nothing — and "end" is the key that gets a user out of scroll-lock.
	if m.focus == focusCards {
		switch msg.String() {
		case "up", "k":
			m.selectBy(-1)
			return m, nil
		case "down", "j":
			m.selectBy(1)
			return m, nil
		case "home":
			m.selectBy(-len(m.snap.Escalations)) // the newest card, and back to following
			return m, nil
		case "end":
			m.selectBy(len(m.snap.Escalations)) // the oldest card still retained
			return m, nil
		}
		var cmd tea.Cmd
		m.cards, cmd = m.cards.Update(msg) // pgup/pgdn
		return m, cmd
	}

	switch msg.String() {
	case "home":
		m.stream.GotoTop()
		m.syncStreamFollow()
		return m, nil
	case "end":
		m.stream.GotoBottom()
		m.syncStreamFollow()
		return m, nil
	}

	var cmd tea.Cmd
	m.stream, cmd = m.stream.Update(msg)
	m.syncStreamFollow()
	return m, cmd
}

// resize refits both panes to the current terminal.
func (m *Model) resize() {
	m.geom = geometryFor(m.width, m.height)
	m.stream.Width, m.stream.Height = m.geom.stream.inner()
	m.cards.Width, m.cards.Height = m.geom.cards.inner()
}

// refresh re-renders both panes from the current snapshot.
func (m *Model) refresh() {
	m.refreshStream()
	m.refreshCards()
}

// refreshStream re-renders the left pane. The stream follows the tail only while the
// user has not scrolled away from the bottom; the aggregated table keeps its offset,
// since its interesting rows (the loudest templates) are at the top.
func (m *Model) refreshStream() {
	w, _ := m.geom.stream.inner()
	switch m.mode {
	case streamView:
		follow := m.streamDetach < 0
		m.stream.SetContent(m.viewStream(w))
		if follow {
			m.stream.GotoBottom()
		}
	case aggregatedView:
		m.stream.SetContent(m.viewAggregated(w))
	}
}

// syncStreamFollow records where the tail was when the user scrolled away from it, and
// re-attaches when they scroll back to the bottom. That baseline is what lets the marker
// say how many lines have gone past while they were reading something else.
func (m *Model) syncStreamFollow() {
	switch {
	case m.stream.AtBottom():
		m.streamDetach = -1
	case m.streamDetach < 0:
		m.streamDetach = m.snap.Stats.TotalLines
	}
}

// refreshCards re-renders the right pane and resolves the selection.
//
// The selection survives new arrivals because it is looked up by hash: cards are
// prepended above it, and it stays on the card it was on. It snaps back to the newest
// card only when it is following, or when the card it was on has aged out of the
// retained set entirely — at which point there is nothing left to hold on to.
func (m *Model) refreshCards() {
	w, h := m.geom.cards.inner()
	if len(m.snap.Escalations) == 0 {
		m.selKey = ""
		m.cards.SetContent(m.viewCardsEmpty(w, h))
		m.cards.GotoTop()
		return
	}

	cards := m.renderCards(w)
	idx := indexOfCard(cards, m.selKey)
	if m.follow || idx < 0 {
		idx = 0
		m.follow = true
		m.cardsDetach = -1
		m.selKey = cards[0].hash
		cards = m.renderCards(w) // the marker moved, so the cards must be drawn again
	}

	var lines []string
	start, end := 0, 0
	for i, c := range cards {
		if i == idx {
			start, end = len(lines), len(lines)+c.height()
		}
		lines = append(lines, c.lines...)
	}
	m.cards.SetContent(strings.Join(lines, "\n"))
	m.ensureVisible(start, end)
}

// selectBy moves the selection by delta cards, clamped at both ends.
func (m *Model) selectBy(delta int) {
	w, _ := m.geom.cards.inner()
	cards := m.renderCards(w)
	if len(cards) == 0 {
		return
	}
	i := min(max(indexOfCard(cards, m.selKey)+delta, 0), len(cards)-1)
	m.selKey = cards[i].hash

	// Following means the selection is on the newest card. Step off it and new arrivals
	// must not drag the user back to the top mid-read; step back onto it and the pane
	// resumes following, which is the only way to get out of scroll-lock again.
	switch {
	case i == 0:
		m.follow, m.cardsDetach = true, -1
	case m.follow:
		m.follow, m.cardsDetach = false, m.snap.Stats.Escalations
	}
	m.refreshCards()
}

// ensureVisible scrolls the cards pane the minimum distance needed to bring the
// selected card into view — and no further, so a selection that is already on screen
// never moves the viewport at all.
//
// A card taller than the pane (an expanded one, in the stacked layout) cannot be shown
// whole, and the two ends are not equally worth showing: scrolling to its END lands the
// user on the last of its context lines with the badge and the headline off the top,
// which is a pane that looks like it has lost its place. So the head of the card wins.
func (m *Model) ensureVisible(start, end int) {
	h := m.cards.Height
	if h <= 0 {
		return
	}
	switch {
	case start < m.cards.YOffset:
		m.cards.SetYOffset(start)
	case end > m.cards.YOffset+h:
		m.cards.SetYOffset(min(end-h, start))
	}
}

// indexOfCard finds a card by hash, or -1.
func indexOfCard(cards []card, hash string) int {
	if hash == "" {
		return -1
	}
	for i, c := range cards {
		if c.hash == hash {
			return i
		}
	}
	return -1
}

// streamNew is how many lines have arrived since the user scrolled away from the tail,
// and false when the pane is still following.
func (m Model) streamNew() (int, bool) {
	if m.streamDetach < 0 || m.mode != streamView {
		return 0, false
	}
	return max(m.snap.Stats.TotalLines-m.streamDetach, 0), true
}

// cardsNew is the same for the cards pane: escalations that have fired while the user
// was reading an older card.
func (m Model) cardsNew() (int, bool) {
	if m.cardsDetach < 0 || m.follow {
		return 0, false
	}
	return max(m.snap.Stats.Escalations-m.cardsDetach, 0), true
}

// lastError returns the most recent error, or "" if there has been none.
func (m Model) lastError() string {
	if len(m.errs) == 0 {
		return ""
	}
	return m.errs[len(m.errs)-1]
}
