// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// TestLayoutForWidth pins the width strategy: two panes when there is room for two
// panes, and a stacked fallback when there is not. Rendering two 40-column columns is
// not a layout, it is a bug with a border around it.
func TestLayoutForWidth(t *testing.T) {
	tests := map[int]layout{
		40:  layoutStacked,
		80:  layoutStacked,
		99:  layoutStacked,
		100: layoutSplit, // exactly at the threshold: the split turns on
		120: layoutSplit, // the likely demo width
		200: layoutSplit,
	}
	for width, want := range tests {
		if got := layoutFor(width); got != want {
			t.Errorf("layoutFor(%d) = %s, want %s", width, got, want)
		}
	}
}

// TestGeometryFillsTheTerminal: the panes and the status bar must account for every
// column and every row. A pane one column short leaves a ragged edge down the screen;
// one row short leaves a gap above the status bar.
func TestGeometryFillsTheTerminal(t *testing.T) {
	for _, width := range []int{80, 100, 120, 200} {
		const height = 24
		g := geometryFor(width, height)

		switch g.layout {
		case layoutSplit:
			if got := g.stream.w + g.cards.w; got != width {
				t.Errorf("width %d: panes span %d columns, want %d", width, got, width)
			}
			if g.stream.h != height-statusHeight || g.cards.h != height-statusHeight {
				t.Errorf("width %d: panes are %d and %d rows, want %d each",
					width, g.stream.h, g.cards.h, height-statusHeight)
			}
			// The cards pane wraps prose, so it is the one that must never be starved.
			if g.cards.w < 40 {
				t.Errorf("width %d: cards pane is %d columns — too narrow to wrap a sentence",
					width, g.cards.w)
			}
		case layoutStacked:
			if g.stream.w != width || g.cards.w != width {
				t.Errorf("width %d: stacked panes are %d and %d columns, want %d each",
					width, g.stream.w, g.cards.w, width)
			}
			if got := g.stream.h + g.cards.h; got != height-statusHeight {
				t.Errorf("width %d: panes span %d rows, want %d", width, got, height-statusHeight)
			}
			if g.stream.h < minPaneHeight {
				t.Errorf("width %d: the stream is squeezed to %d rows", width, g.stream.h)
			}
		}
	}
}

// TestStackedCardsHeightIsStable is the no-jump guarantee for the narrow layout. The
// cards pane is a fixed share of the height, NOT a fit to its contents — a pane that
// grows when the first card lands is a layout that moves under someone mid-read.
func TestStackedCardsHeightIsStable(t *testing.T) {
	for _, height := range []int{24, 40, 60} {
		want := stackedCardsHeight(height - statusHeight)
		if want < stackedCardsMin {
			t.Errorf("height %d: cards pane is %d rows, below the %d floor", height, want, stackedCardsMin)
		}
		if want > stackedCardsMax {
			t.Errorf("height %d: cards pane is %d rows, over the %d ceiling", height, want, stackedCardsMax)
		}
	}
	// A terminal too short to hold both gives the rows to the stream: a stream with no
	// cards pane still works, a cards pane with no stream is not this program.
	if got := stackedCardsHeight(4); got != 0 {
		t.Errorf("stackedCardsHeight(4) = %d, want 0: the stream must not be squeezed out", got)
	}
}

// TestRenderNeverOverflowsTheTerminal is the "never looks broken" test, and the one that
// actually catches things: EVERY line of the rendered frame, at every width, must fit in
// the terminal. One cell over and the terminal wraps it, every row below it shifts, and
// the layout tears.
//
// It runs against the full View() through the real Update path, with the worst content
// available: long patterns, long prose, an expanded card, and the double-width glyphs in
// the status bar that have already broken this once.
func TestRenderNeverOverflowsTheTerminal(t *testing.T) {
	for _, width := range []int{80, 100, 120, 200} {
		m := New(nil, nil, Options{Explain: true, DockerTail: "100"})
		sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		applied, _ := sized.(Model).Update(snapshotMsg(overflowSnapshot()))
		expanded := expandSelected(t, applied.(Model))

		for _, name := range []string{"collapsed", "expanded"} {
			view := applied.(Model).View()
			if name == "expanded" {
				view = expanded.View()
			}
			for i, line := range strings.Split(view, "\n") {
				if got := visWidth(line); got > width {
					t.Errorf("width %d (%s): line %d is %d cells wide:\n%q",
						width, name, i, got, line)
				}
			}
		}
	}
}

// TestCriticalContentSurvivesEveryWidth: fitting the terminal is worthless if the way it
// fits is by truncating away the thing the user is looking for. At every supported width
// the badge, the summary and the status bar's counters must still be on screen.
func TestCriticalContentSurvivesEveryWidth(t *testing.T) {
	for _, width := range []int{80, 100, 120, 200} {
		m := New(nil, nil, Options{Explain: true, DockerTail: "100"})
		sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		applied, _ := sized.(Model).Update(snapshotMsg(escalationSnapshot(&model.Explanation{
			Hash: "aaa", State: model.ExplainDone, Summary: "Nil map write.",
		})))
		view := applied.(Model).View()

		for _, want := range []string{
			"PANIC",          // the severity badge
			"Nil map write.", // the summary: the whole reason a model was called
			"ANOMALIES",      // the pane is identified
			"esc 1",          // the status bar's counters
			"STREAM",
		} {
			if !strings.Contains(view, want) {
				t.Errorf("width %d: %q was truncated away:\n%s", width, want, view)
			}
		}
	}
}

// overflowSnapshot is deliberately hostile: everything in it is longer than any pane.
func overflowSnapshot() pipeline.Snapshot {
	long := strings.Repeat("verylongtokenwithnospaces", 12)
	ev := pipeline.Event{
		Line:     model.LogLine{Source: "docker:a-very-long-container-name", Level: "CRITICAL"},
		Hash:     "aaa",
		Pattern:  long,
		Count:    12431,
		Score:    2.5,
		Escalate: true,
		Reasons:  []string{"novel template (first seen)", "burst 12x over baseline", "level CRITICAL"},
		Explanation: &model.Explanation{
			Hash: "aaa", State: model.ExplainDone,
			Summary:     long,
			LikelyCause: long,
			Suggestion:  long,
		},
		Context: []string{long, long},
	}
	return pipeline.Snapshot{
		Lines:       []pipeline.Event{ev, ev},
		Escalations: []pipeline.Event{ev},
		Stats: pipeline.Stats{
			TotalLines: 1234567, UniqueTemplates: 999, LinesPerSec: 4200, Escalations: 12,
			Suppressed: 3, Explained: 9, Explaining: 2, ExplainFailed: 1, Dropped: 4,
			Sources: []string{"docker:a-very-long-container-name", "proc:another-long-one"},
		},
	}
}
