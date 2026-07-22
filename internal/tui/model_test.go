// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// newTestModel returns a sized model, as if Bubble Tea had already sent the
// initial WindowSizeMsg.
func newTestModel(t *testing.T) Model {
	t.Helper()
	m := New(nil, nil, Options{})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return sized.(Model)
}

// key builds a KeyMsg for a single rune, matching what Bubble Tea delivers.
func key(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// testSnapshot is a two-template snapshot with the loudest template first, as the
// pipeline would deliver it.
func testSnapshot() pipeline.Snapshot {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	return pipeline.Snapshot{
		Lines: []pipeline.Event{
			{Line: model.LogLine{Source: "docker:api", Level: "ERROR"}, Pattern: "user <NUM> failed", Count: 12},
			{Line: model.LogLine{Source: "docker:db"}, Pattern: "vacuum done", Count: 1},
		},
		Templates: []pipeline.TemplateSummary{
			{Hash: "a", Pattern: "user <NUM> failed", Level: "ERROR", Count: 12, LastSeen: now},
			{Hash: "b", Pattern: "vacuum done", Count: 1, LastSeen: now},
		},
		Stats: pipeline.Stats{
			TotalLines: 13, UniqueTemplates: 2, LinesPerSec: 240,
			Sources: []string{"docker:api", "docker:db"},
		},
	}
}

func TestUpdateAppliesSnapshot(t *testing.T) {
	m := newTestModel(t)

	updated, cmd := m.Update(snapshotMsg(testSnapshot()))
	got := updated.(Model)

	if got.snap.Stats.TotalLines != 13 || got.snap.Stats.UniqueTemplates != 2 {
		t.Errorf("stats = %d lines / %d templates, want 13 / 2",
			got.snap.Stats.TotalLines, got.snap.Stats.UniqueTemplates)
	}
	if len(got.snap.Lines) != 2 || len(got.snap.Templates) != 2 {
		t.Errorf("got %d lines / %d templates, want 2 / 2",
			len(got.snap.Lines), len(got.snap.Templates))
	}
	// The model must re-arm the reader, or the stream stops after one snapshot.
	if cmd == nil {
		t.Error("Update did not re-issue the snapshot reader command")
	}
	if !strings.Contains(got.View(), "user <NUM> failed") {
		t.Error("stream view does not show the templated line")
	}
}

func TestToggleModeKey(t *testing.T) {
	m := newTestModel(t)
	applied, _ := m.Update(snapshotMsg(testSnapshot()))
	m = applied.(Model)

	if m.mode != streamView {
		t.Fatalf("mode = %v, want streamView by default", m.mode)
	}

	toggled, _ := m.Update(key('t'))
	m = toggled.(Model)
	if m.mode != aggregatedView {
		t.Fatalf("mode after 't' = %v, want aggregatedView", m.mode)
	}
	if view := m.View(); !strings.Contains(view, "COUNT") || !strings.Contains(view, "AGGREGATED") {
		t.Error("aggregated view does not render the template table")
	}

	back, _ := m.Update(key('t'))
	if back.(Model).mode != streamView {
		t.Error("'t' did not toggle back to streamView")
	}
}

// TestPauseFreezesRendering checks that pause holds the view still while the
// pipeline keeps running: snapshots keep being consumed, they just aren't applied.
func TestPauseFreezesRendering(t *testing.T) {
	m := newTestModel(t)
	first, _ := m.Update(snapshotMsg(testSnapshot()))
	m = first.(Model)

	paused, _ := m.Update(key('p'))
	m = paused.(Model)
	if !m.paused {
		t.Fatal("'p' did not pause")
	}

	newer := testSnapshot()
	newer.Stats.TotalLines = 999
	frozen, cmd := m.Update(snapshotMsg(newer))
	m = frozen.(Model)

	if m.snap.Stats.TotalLines != 13 {
		t.Errorf("TotalLines = %d while paused, want the frozen 13", m.snap.Stats.TotalLines)
	}
	// Draining must continue while paused — otherwise the view would resume with a
	// stale backlog instead of the latest state.
	if cmd == nil {
		t.Error("paused model stopped reading the snapshot channel")
	}

	resumed, _ := m.Update(key('p'))
	m = resumed.(Model)
	applied, _ := m.Update(snapshotMsg(newer))
	if got := applied.(Model).snap.Stats.TotalLines; got != 999 {
		t.Errorf("TotalLines = %d after resume, want 999", got)
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []tea.KeyMsg{key('q'), {Type: tea.KeyCtrlC}} {
		m := newTestModel(t)
		_, cmd := m.Update(k)
		if cmd == nil {
			t.Fatalf("%v produced no command, want tea.Quit", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%v did not quit", k)
		}
	}
}

// TestStreamEndedDoesNotQuit: when the sources finish, the TUI stays up so the
// aggregated view remains readable until the user leaves.
func TestStreamEndedDoesNotQuit(t *testing.T) {
	m := newTestModel(t)
	applied, _ := m.Update(snapshotMsg(testSnapshot()))
	m = applied.(Model)

	ended, cmd := m.Update(streamEndedMsg{})
	m = ended.(Model)

	if !m.ended {
		t.Error("streamEndedMsg did not mark the stream as ended")
	}
	if cmd != nil {
		t.Error("streamEndedMsg produced a command; it must not quit or keep reading")
	}
	if !strings.Contains(m.View(), "ENDED") {
		t.Error("status bar does not report the ended stream")
	}
	if !strings.Contains(m.View(), "user <NUM> failed") {
		t.Error("view stopped rendering the last snapshot after the stream ended")
	}
}

// TestErrorSurfacesInStatusBar: background errors must reach the model, never
// stdout, which would corrupt the alternate screen.
func TestErrorSurfacesInStatusBar(t *testing.T) {
	m := newTestModel(t)

	updated, cmd := m.Update(errMsg{errors.New("ingest: docker: permission denied")})
	m = updated.(Model)

	if cmd == nil {
		t.Error("Update did not re-issue the error reader command")
	}
	if !strings.Contains(m.View(), "permission denied") {
		t.Error("status bar does not show the most recent error")
	}
}

// TestErrorListIsBounded keeps a noisy source from growing the model without limit.
func TestErrorListIsBounded(t *testing.T) {
	m := newTestModel(t)
	for i := range maxErrors + 10 {
		updated, _ := m.Update(errMsg{errors.New(strings.Repeat("x", i+1))})
		m = updated.(Model)
	}

	if len(m.errs) != maxErrors {
		t.Errorf("retained %d errors, want %d (bounded)", len(m.errs), maxErrors)
	}
	// The newest must survive the trim; it is the one on display.
	if want := strings.Repeat("x", maxErrors+10); m.lastError() != want {
		t.Errorf("lastError() length = %d, want the newest (%d)", len(m.lastError()), len(want))
	}
}

// TestStatusBarKeepsModeOnNarrowTerminal: the source list is the elastic segment,
// so a narrow terminal drops source names rather than the mode and the counters.
// The width is the floor for that guarantee, and it grew by a segment in M3 when the
// bar started carrying the escalation count.
func TestStatusBarKeepsModeOnNarrowTerminal(t *testing.T) {
	m := New(nil, nil, Options{})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 56, Height: 24})
	m = sized.(Model)

	snap := testSnapshot()
	snap.Stats.Sources = []string{"docker:api", "docker:db", "docker:worker", "docker:cache"}
	applied, _ := m.Update(snapshotMsg(snap))
	m = applied.(Model)

	status := m.viewStatus()
	if !strings.Contains(status, "STREAM") {
		t.Errorf("mode truncated away at width 56:\n%s", status)
	}
	if !strings.Contains(status, "13 lines") {
		t.Errorf("counters truncated away at width 56:\n%s", status)
	}
	for _, line := range strings.Split(status, "\n") {
		if got := visWidth(line); got > 56 {
			t.Errorf("status line is %d runes wide, want <= 56: %q", got, line)
		}
	}
}

// TestStatusBarShowsRemoteWarning: with masking off against a remote endpoint, the status
// bar carries the notice — the only place a TUI user would see it, since a stderr line would
// be wiped by the alternate screen.
func TestStatusBarShowsRemoteWarning(t *testing.T) {
	m := New(nil, nil, Options{RemoteWarnHost: "api.openai.com"})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = sized.(Model)
	applied, _ := m.Update(snapshotMsg(testSnapshot()))
	m = applied.(Model)

	status := m.viewStatus()
	if !strings.Contains(status, "api.openai.com") || !strings.Contains(status, "--llm-anonymize") {
		t.Errorf("remote warning missing from the status bar:\n%s", status)
	}

	// And absent when there is nothing to warn about.
	m2 := New(nil, nil, Options{})
	sized2, _ := m2.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m2 = sized2.(Model)
	applied2, _ := m2.Update(snapshotMsg(testSnapshot()))
	m2 = applied2.(Model)
	if strings.Contains(m2.viewStatus(), "raw logs") {
		t.Error("status bar warned with no remote host set")
	}
}

func TestFormatCount(t *testing.T) {
	tests := map[int]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 12431: "12,431", 1234567: "1,234,567"}
	for n, want := range tests {
		if got := formatCount(n); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate under width = %q, want unchanged", got)
	}
	if got := truncate("hello world", 8); got != "hello w…" {
		t.Errorf("truncate = %q, want %q", got, "hello w…")
	}
	// Runes, not bytes: a multi-byte string must not be cut mid-character.
	if got := truncate("héllo wörld", 6); got != "héllo…" {
		t.Errorf("truncate multibyte = %q, want %q", got, "héllo…")
	}
}

// --- Dry-run escalation pane (M3) ----------------------------------------------------

// dryRunSnapshot is a snapshot carrying one would-be escalation.
func dryRunSnapshot() pipeline.Snapshot {
	snap := testSnapshot()
	esc := pipeline.Event{
		Line:     model.LogLine{Source: "svc:api", Level: "PANIC"},
		Pattern:  "nil pointer dereference in handler",
		Score:    2.0,
		Escalate: true,
		Reasons:  []string{"novel template (first seen)", "level PANIC"},
	}
	snap.Lines = append(snap.Lines, esc)
	snap.Escalations = []pipeline.Event{esc}
	snap.Stats.Escalations = 1
	return snap
}

// TestDryRunSurfacesTheScorersCase: the escalation must still be readable after the
// stream has moved on. Inline-only was useless — at any real log rate it scrolls off in
// well under a second, and reading the reasons is the entire point of the flag.
//
// The reasons and the score sit on the COLLAPSED card, because they are what the
// thresholds get tuned against: a mode whose evidence is one keystroke out of sight is a
// mode nobody calibrates with.
func TestDryRunSurfacesTheScorersCase(t *testing.T) {
	m := New(nil, nil, Options{ExplainDryRun: true})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = sized.(Model)

	applied, _ := m.Update(snapshotMsg(dryRunSnapshot()))
	m = applied.(Model)

	view := m.View()
	if !strings.Contains(view, "WOULD ESCALATE") {
		t.Fatalf("no cards pane:\n%s", view)
	}
	for _, want := range []string{"nil pointer dereference in handler", "level PANIC", "score 2.00"} {
		if !strings.Contains(view, want) {
			t.Errorf("the cards pane is missing %q:\n%s", want, view)
		}
	}
	// It survives the stream scrolling on: the pane renders from Snapshot.Escalations,
	// not from whatever happens to be in the visible tail.
	m.stream.GotoTop()
	if !strings.Contains(m.View(), "WOULD ESCALATE") {
		t.Error("the cards pane vanished when the stream was scrolled away")
	}
}

// TestDryRunWordingIsOffByDefault: an ordinary run must not claim it "would" have
// escalated something it actually did escalate. The card is still there — the pane is
// always there — it just does not borrow dry-run's wording.
func TestDryRunWordingIsOffByDefault(t *testing.T) {
	m := newTestModel(t)
	applied, _ := m.Update(snapshotMsg(dryRunSnapshot()))
	m = applied.(Model)

	view := m.View()
	if strings.Contains(view, "WOULD ESCALATE") {
		t.Error("the dry-run wording rendered without --explain-dry-run")
	}
	if !strings.Contains(view, "ANOMALIES") {
		t.Errorf("the cards pane is missing from an ordinary run:\n%s", view)
	}
	// A scored run with no model attached still has something to show: the scorer fired,
	// and the card says what on and why, it just never says "explaining…".
	if !strings.Contains(view, "nil pointer dereference in handler") {
		t.Errorf("the escalation produced no card:\n%s", view)
	}
	if strings.Contains(view, "explaining…") {
		t.Errorf("a run with no LLM claims an explanation is coming:\n%s", view)
	}
}

// TestCardsAreNewestFirst: the cards pane is a live tail, so the thing that just fired is
// the thing at the top — and it is the one selected, because it is the one being read.
func TestCardsAreNewestFirst(t *testing.T) {
	m := New(nil, nil, Options{ExplainDryRun: true})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = sized.(Model)

	snap := testSnapshot()
	const n = 5
	for i := range n {
		snap.Escalations = append(snap.Escalations, pipeline.Event{
			Hash:    "h" + strconv.Itoa(i),
			Pattern: "failure " + strconv.Itoa(i),
			Line:    model.LogLine{Level: "ERROR"},
			Score:   1, Escalate: true, Reasons: []string{"novel template"},
		})
	}
	snap.Stats.Escalations = n
	applied, _ := m.Update(snapshotMsg(snap))
	m = applied.(Model)

	cards := m.renderCards(40)
	if len(cards) != n {
		t.Fatalf("rendered %d cards, want %d", len(cards), n)
	}
	if cards[0].hash != "h4" {
		t.Errorf("the top card is %q, want the newest (h4)", cards[0].hash)
	}
	if m.selKey != "h4" {
		t.Errorf("selection is on %q, want the newest card (h4)", m.selKey)
	}
	// The newest card must actually be on screen, not merely first in a list scrolled
	// somewhere else.
	if !strings.Contains(m.View(), "failure 4") {
		t.Errorf("the newest card is not visible:\n%s", m.View())
	}
}
