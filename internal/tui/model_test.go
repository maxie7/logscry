// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"errors"
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
	m := New(nil, nil)
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
func TestStatusBarKeepsModeOnNarrowTerminal(t *testing.T) {
	m := New(nil, nil)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 46, Height: 24})
	m = sized.(Model)

	snap := testSnapshot()
	snap.Stats.Sources = []string{"docker:api", "docker:db", "docker:worker", "docker:cache"}
	applied, _ := m.Update(snapshotMsg(snap))
	m = applied.(Model)

	status := m.viewStatus()
	if !strings.Contains(status, "STREAM") {
		t.Errorf("mode truncated away at width 46:\n%s", status)
	}
	if !strings.Contains(status, "13 lines") {
		t.Errorf("counters truncated away at width 46:\n%s", status)
	}
	for _, line := range strings.Split(status, "\n") {
		if got := runeLen(line); got > 46 {
			t.Errorf("status line is %d runes wide, want <= 46: %q", got, line)
		}
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
