// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// --- The empty state ------------------------------------------------------------------

// TestEmptyStateIsIntentional. Most of the time there are zero cards, and that is the
// product working — "silent on noise" is the whole thesis. So the empty pane has to read
// as a tool that is watching and has nothing to report, not as a pane that failed to
// render. A reviewer running this against a healthy system sees this state FIRST.
func TestEmptyStateIsIntentional(t *testing.T) {
	m := New(nil, nil, Options{Explain: true})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	applied, _ := sized.(Model).Update(snapshotMsg(testSnapshot()))
	view := applied.(Model).View()

	if !strings.Contains(view, "No anomalies") {
		t.Errorf("the empty cards pane does not say it has nothing to report:\n%s", view)
	}
	// It says what it is watching. A bare "no anomalies" is indistinguishable from a tool
	// that is watching nothing at all.
	if !strings.Contains(view, "Watching 2 templates across 2 sources.") {
		t.Errorf("the empty state does not say what it is watching:\n%s", view)
	}
	if !strings.Contains(view, "Quiet is the expected state") {
		t.Errorf("the empty state does not tell the user that silence is the point:\n%s", view)
	}
}

// TestEmptyStateSingularises: "Watching 1 templates across 1 sources" reads as a
// placeholder someone forgot to finish, which is exactly the impression this pane cannot
// afford to give.
func TestEmptyStateSingularises(t *testing.T) {
	m := New(nil, nil, Options{Explain: true})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

	snap := pipeline.Snapshot{Stats: pipeline.Stats{UniqueTemplates: 1, Sources: []string{"stdin"}}}
	applied, _ := sized.(Model).Update(snapshotMsg(snap))
	if view := applied.(Model).View(); !strings.Contains(view, "Watching 1 template across 1 source.") {
		t.Errorf("the empty state does not singularise:\n%s", view)
	}

	// And at the very start of a run there are no sources yet, which is not a fault.
	empty, _ := sized.(Model).Update(snapshotMsg(pipeline.Snapshot{}))
	if view := empty.(Model).View(); !strings.Contains(view, "no sources yet") {
		t.Errorf("a run with no sources yet reads as broken:\n%s", view)
	}
}

// TestLayoutDoesNotShiftWhenTheFirstCardArrives is the promise the whole two-pane design
// rests on. The cards pane is ALWAYS there — it just fills. If the frame moved when the
// first anomaly landed, every row a user was reading would jump at the exact moment they
// most needed to keep reading it.
func TestLayoutDoesNotShiftWhenTheFirstCardArrives(t *testing.T) {
	for _, width := range []int{80, 120} {
		m := New(nil, nil, Options{Explain: true})
		sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})

		quiet, _ := sized.(Model).Update(snapshotMsg(testSnapshot()))
		quietModel := quiet.(Model)
		quietView := quietModel.View()

		loud, _ := quietModel.Update(snapshotMsg(escalationSnapshot(&model.Explanation{
			Hash: "aaa", State: model.ExplainDone, Summary: "A handler wrote to a nil map.",
		})))
		loudModel := loud.(Model)
		loudView := loudModel.View()

		if !strings.Contains(loudView, "A handler wrote to a nil map.") {
			t.Fatalf("width %d: the card never arrived", width)
		}

		// Same number of rows, and the stream pane is the same size to the row and column.
		if got, want := lines(loudView), lines(quietView); got != want {
			t.Errorf("width %d: the frame went from %d rows to %d when the first card landed",
				width, want, got)
		}
		if loudModel.stream.Height != quietModel.stream.Height {
			t.Errorf("width %d: the stream was resized from %d rows to %d by the first card",
				width, quietModel.stream.Height, loudModel.stream.Height)
		}
		if loudModel.stream.Width != quietModel.stream.Width {
			t.Errorf("width %d: the stream was resized from %d columns to %d by the first card",
				width, quietModel.stream.Width, loudModel.stream.Width)
		}
	}
}

// --- Focus ----------------------------------------------------------------------------

// TestTabSwitchesFocus: with two panes, the arrow keys need an owner, and the user needs
// to be able to see who has it.
func TestTabSwitchesFocus(t *testing.T) {
	m := cardsModel(t, 3)
	if m.focus != focusStream {
		t.Fatal("focus does not start on the stream")
	}
	// The footer is honest about where the focus is: ↵ expands nothing from the stream.
	if !strings.Contains(m.View(), "tab cards") {
		t.Errorf("the footer does not advertise the stream's keys:\n%s", m.footer())
	}

	tabbed, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = tabbed.(Model)
	if m.focus != focusCards {
		t.Fatal("tab did not move the focus to the cards")
	}
	if !strings.Contains(m.View(), "↵ expand") {
		t.Errorf("the footer does not advertise the cards' keys:\n%s", m.footer())
	}

	back, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if back.(Model).focus != focusStream {
		t.Error("shift+tab did not move the focus back to the stream")
	}
}

// TestFocusedPaneIsVisiblyFocused: the border is the only thing telling the user where
// their next keystroke will land. If both panes render identically, focus is a hidden
// mode — and a hidden mode is a bug report waiting to happen.
//
// It forces a colour profile first. Under `go test` there is no terminal, so lipgloss
// resolves to Ascii and renders EVERY style as bare text — which would let a version of
// this that styled nothing at all pass with flying colours.
func TestFocusedPaneIsVisiblyFocused(t *testing.T) {
	restore := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(restore) })

	m := cardsModel(t, 2)
	onStream := m.View()

	tabbed, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	onCards := tabbed.(Model).View()

	if onStream == onCards {
		t.Fatal("the frame is identical whichever pane has focus: nothing shows the focus")
	}
	// Concretely, and independent of any other difference in the frame: the same pane,
	// with the same contents, must paint differently when it holds the focus.
	b := box{w: 40, h: 8}
	if m.pane("TITLE", "body", b, true) == m.pane("TITLE", "body", b, false) {
		t.Error("a focused pane renders identically to an unfocused one")
	}
}

// TestScrollKeysActOnTheFocusedPaneOnly. An arrow key that scrolls both panes scrolls
// neither on purpose.
func TestScrollKeysActOnTheFocusedPaneOnly(t *testing.T) {
	m := busyModel(t, 200, 4)

	// Focus is on the stream: page up must move the stream and leave the cards alone.
	beforeCards := m.cards.YOffset
	beforeSel := m.selKey
	scrolled, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = scrolled.(Model)

	if m.stream.AtBottom() {
		t.Error("pgup did not scroll the stream while the stream had focus")
	}
	if m.cards.YOffset != beforeCards || m.selKey != beforeSel {
		t.Errorf("pgup moved the CARDS pane (%d -> %d, sel %q -> %q) while the stream had focus",
			beforeCards, m.cards.YOffset, beforeSel, m.selKey)
	}

	// Focus the cards: down must move the selection and leave the stream where it is.
	tabbed, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = tabbed.(Model)
	beforeStream := m.stream.YOffset

	down, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = down.(Model)

	if m.selKey == beforeSel {
		t.Error("down did not move the card selection while the cards had focus")
	}
	if m.stream.YOffset != beforeStream {
		t.Errorf("down moved the STREAM (%d -> %d) while the cards had focus",
			beforeStream, m.stream.YOffset)
	}
}

// --- Scroll-lock ----------------------------------------------------------------------

// TestStreamScrollLock is the one people get wrong, and getting it wrong ruins a live
// tail: a user who has scrolled up to read something must NOT be yanked back to the
// bottom by the next arriving line.
func TestStreamScrollLock(t *testing.T) {
	m := busyModel(t, 200, 0)

	// At the bottom: the tail follows. That is the default and it must stay the default.
	if !m.stream.AtBottom() {
		t.Fatal("the stream did not start at the bottom of the tail")
	}
	grown, _ := m.Update(snapshotMsg(busySnapshot(300, 0)))
	m = grown.(Model)
	if !m.stream.AtBottom() {
		t.Error("the stream stopped following new lines while it was at the bottom")
	}

	// Scroll up, then let 100 more lines arrive: the viewport must not move a row.
	up, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = up.(Model)
	if m.stream.AtBottom() {
		t.Fatal("pgup did not scroll the stream up")
	}
	parked := m.stream.YOffset

	arrived, _ := m.Update(snapshotMsg(busySnapshot(400, 0)))
	m = arrived.(Model)
	if m.stream.YOffset != parked {
		t.Errorf("new lines yanked the scrolled-up stream from %d to %d", parked, m.stream.YOffset)
	}

	// And the user is told what they are missing, or a held pane reads as a dead one.
	n, scrolled := m.streamNew()
	if !scrolled || n != 100 {
		t.Errorf("streamNew() = (%d, %v), want (100, true)", n, scrolled)
	}
	if !strings.Contains(m.View(), "↑ scrolled") {
		t.Errorf("the stream does not say it is scrolled away from the tail:\n%s", m.View())
	}

	// Scrolling back to the bottom re-attaches: following is not a one-way door.
	end, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = end.(Model)
	if _, scrolled := m.streamNew(); scrolled {
		t.Error("the stream did not resume following when scrolled back to the bottom")
	}
	back, _ := m.Update(snapshotMsg(busySnapshot(500, 0)))
	if !back.(Model).stream.AtBottom() {
		t.Error("the stream did not follow new lines after re-attaching")
	}
}

// TestCardsScrollLock is the same rule for the cards, mirrored: the cards pane grows from
// the TOP, so "following" means sitting on the newest card. Step back to an older card
// and a new escalation must not drag the selection off it mid-read.
func TestCardsScrollLock(t *testing.T) {
	m := busyModel(t, 50, 4)
	tabbed, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = tabbed.(Model)

	// Following: a new escalation moves the selection onto it, because the newest card is
	// the one being read.
	if !m.follow || m.selKey != "e3" {
		t.Fatalf("the cards pane did not start following the newest card (sel %q, follow %v)",
			m.selKey, m.follow)
	}
	fresh, _ := m.Update(snapshotMsg(busySnapshot(60, 5)))
	m = fresh.(Model)
	if m.selKey != "e4" {
		t.Errorf("a following cards pane did not move to the new card: sel %q, want e4", m.selKey)
	}

	// Step down to an older card, and the pane detaches.
	down, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = down.(Model)
	if m.follow {
		t.Fatal("the cards pane still claims to be following after the user moved off the newest card")
	}
	parked := m.selKey

	// Now two more escalations fire. The selection must stay exactly where it was.
	for _, n := range []int{6, 7} {
		next, _ := m.Update(snapshotMsg(busySnapshot(60, n)))
		m = next.(Model)
	}
	if m.selKey != parked {
		t.Errorf("new escalations dragged the selection from %q to %q", parked, m.selKey)
	}
	n, scrolled := m.cardsNew()
	if !scrolled || n != 2 {
		t.Errorf("cardsNew() = (%d, %v), want (2, true)", n, scrolled)
	}

	// Walking back up to the newest card re-attaches.
	for range 5 {
		up, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = up.(Model)
	}
	if !m.follow {
		t.Error("returning to the newest card did not resume following")
	}
}

// TestSelectionSurvivesAnAgedOutCard: the pipeline only retains the last 20 escalations.
// When the card under the selection falls off the end of that, there is nothing left to
// hold on to — so the pane must fall back to the newest card and resume following, rather
// than pointing at a card that no longer exists.
func TestSelectionSurvivesAnAgedOutCard(t *testing.T) {
	m := busyModel(t, 50, 4)
	tabbed, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	down, _ := tabbed.(Model).Update(tea.KeyMsg{Type: tea.KeyDown})
	m = down.(Model)

	if m.follow {
		t.Fatal("the pane is still following after the selection moved")
	}

	// A snapshot whose escalations no longer include the selected hash at all.
	snap := busySnapshot(60, 3)
	for i := range snap.Escalations {
		snap.Escalations[i].Hash = "new" + strconv.Itoa(i)
	}
	aged, _ := m.Update(snapshotMsg(snap))
	m = aged.(Model)

	if m.selKey != "new2" {
		t.Errorf("selection = %q after the selected card aged out, want the newest (new2)", m.selKey)
	}
	if !m.follow {
		t.Error("the pane did not resume following after its card aged out")
	}
}

// --- Helpers --------------------------------------------------------------------------

// busyModel is a sized model carrying a full stream and n escalations — the state the
// scroll-lock rules actually have to hold in.
func busyModel(t *testing.T, streamLines, escalations int) Model {
	t.Helper()
	m := New(nil, nil, Options{Explain: true})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	applied, _ := sized.(Model).Update(snapshotMsg(busySnapshot(streamLines, escalations)))
	return applied.(Model)
}

// busySnapshot is n stream lines and e escalations, with the counters to match.
func busySnapshot(n, e int) pipeline.Snapshot {
	snap := pipeline.Snapshot{
		Stats: pipeline.Stats{TotalLines: n, UniqueTemplates: 5, Escalations: e},
	}
	for i := range n {
		snap.Lines = append(snap.Lines, pipeline.Event{
			Line:    model.LogLine{Source: "docker:api", Level: "INFO"},
			Pattern: "line " + strconv.Itoa(i),
			Hash:    "t" + strconv.Itoa(i%5),
		})
	}
	for i := range e {
		snap.Escalations = append(snap.Escalations, pipeline.Event{
			Hash:     "e" + strconv.Itoa(i),
			Pattern:  "escalation " + strconv.Itoa(i),
			Line:     model.LogLine{Source: "docker:api", Level: "ERROR"},
			Count:    1,
			Score:    1.5,
			Escalate: true,
			Reasons:  []string{"novel template (first seen)"},
		})
	}
	return snap
}

// lines counts the rows of a rendered frame.
func lines(view string) int { return len(strings.Split(view, "\n")) }
