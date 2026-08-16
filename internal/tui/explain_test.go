// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// explainModel is a sized model with an LLM attached — the ordinary run, as opposed to
// --explain-dry-run. It is sized at the demo width, where the cards pane is wide enough
// to show a headline without truncating it.
func explainModel(t *testing.T) Model {
	t.Helper()
	m := New(nil, nil, Options{Explain: true})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	return sized.(Model)
}

// expandSelected opens the selected card, which is where a card keeps its detail: the
// cause, the suggestion, the failure reason, and the context lines. It goes through the
// real keys — tab to the cards, then enter — so what the test drives is what a user does.
func expandSelected(t *testing.T, m Model) Model {
	t.Helper()
	focused, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	expanded, _ := focused.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	return expanded.(Model)
}

// escalationSnapshot is one escalated event carrying whatever explanation state a test
// wants to render.
func escalationSnapshot(ex *model.Explanation) pipeline.Snapshot {
	ev := pipeline.Event{
		Line:        model.LogLine{Source: "docker:api", Level: "PANIC"},
		Hash:        "aaa",
		Pattern:     "panic: nil map write in handler <NUM>",
		Escalate:    true,
		Queued:      true,
		Score:       2,
		Reasons:     []string{"novel template (first seen)", "level PANIC"},
		Explanation: ex,
	}
	return pipeline.Snapshot{
		Lines:       []pipeline.Event{ev},
		Escalations: []pipeline.Event{ev},
		Stats:       pipeline.Stats{TotalLines: 1, UniqueTemplates: 1, Escalations: 1},
	}
}

// apply pushes a snapshot through the event loop, as the pipeline would.
func apply(t *testing.T, m Model, snap pipeline.Snapshot) Model {
	t.Helper()
	updated, _ := m.Update(snapshotMsg(snap))
	return updated.(Model)
}

// TestEscalationCardAppearsWhilePending: the explanation takes seconds, and at any real
// log rate the line that caused it is gone in well under one. So the card has to appear
// the instant the event escalates, saying that an answer is coming — otherwise the user
// watches an apparently idle tool and concludes it does nothing.
func TestEscalationCardAppearsWhilePending(t *testing.T) {
	m := apply(t, explainModel(t), escalationSnapshot(&model.Explanation{
		Hash:  "aaa",
		State: model.ExplainPending,
	}))

	view := m.View()
	if !strings.Contains(view, "ANOMALIES") {
		t.Fatalf("no cards pane:\n%s", view)
	}
	if !strings.Contains(view, "panic: nil map write in handler <NUM>") {
		t.Errorf("the card does not show the template:\n%s", view)
	}
	if !strings.Contains(view, "explaining…") {
		t.Errorf("a pending card does not say it is being explained:\n%s", view)
	}
	// Dry-run's wording must not leak into a run that really is calling a model.
	if strings.Contains(view, "WOULD ESCALATE") {
		t.Errorf("the live pane used the dry-run wording:\n%s", view)
	}
}

// TestExplanationUpdatesTheCardInPlace: the answer arrives on a later snapshot, and it
// has to land on the card the user is already looking at — replacing the template
// headline with plain English, which is the entire point of having called a model.
func TestExplanationUpdatesTheCardInPlace(t *testing.T) {
	m := explainModel(t)
	m = apply(t, m, escalationSnapshot(&model.Explanation{Hash: "aaa", State: model.ExplainPending}))
	m = apply(t, m, escalationSnapshot(&model.Explanation{
		Hash:        "aaa",
		State:       model.ExplainDone,
		Summary:     "A handler wrote to a nil map.",
		LikelyCause: "The cache map is never initialised.",
		Suggestion:  "Make the map in NewServer.",
	}))

	view := m.View()
	if strings.Contains(view, "explaining…") {
		t.Errorf("the card still says it is explaining after the answer arrived:\n%s", view)
	}
	if !strings.Contains(view, "A handler wrote to a nil map.") {
		t.Errorf("the answered card does not lead with the summary:\n%s", view)
	}

	// The cause and the check are the other two thirds of why the model was called. They
	// live behind the expand, so that a pane of cards stays scannable.
	expanded := expandSelected(t, m).View()
	for _, want := range []string{
		"cause:  The cache map is never initialised.",
		"check:  Make the map in NewServer.",
	} {
		if !strings.Contains(expanded, want) {
			t.Errorf("the expanded card is missing %q:\n%s", want, expanded)
		}
	}
}

// TestFailedExplanationSaysSo: a model that is down must produce a card that admits it,
// not a spinner that spins forever and not a silent hole where an answer should be.
func TestFailedExplanationSaysSo(t *testing.T) {
	m := apply(t, explainModel(t), escalationSnapshot(&model.Explanation{
		Hash:  "aaa",
		State: model.ExplainFailed,
		Err:   "HTTP 500 (server error): model not loaded",
	}))

	view := m.View()
	if !strings.Contains(view, "explanation unavailable") {
		t.Errorf("a failed card does not say so:\n%s", view)
	}
	if strings.Contains(view, "explaining…") {
		t.Errorf("a failed card still reads as pending:\n%s", view)
	}
	// The reason is on the expanded card: "connection refused" and "bad key" are both
	// fixable, and neither is distinguishable from "the tool is broken" without it.
	if expanded := expandSelected(t, m).View(); !strings.Contains(expanded, "model not loaded") {
		t.Errorf("the failed card gives no reason:\n%s", expanded)
	}
}

// TestStatusBarShowsLLMCounters: the status bar is where "the model is answering",
// "the model is down", and "the model cannot keep up" become visible. Without them, all
// three look exactly like a tool that has nothing to say.
func TestStatusBarShowsLLMCounters(t *testing.T) {
	m := explainModel(t)
	snap := escalationSnapshot(&model.Explanation{Hash: "aaa", State: model.ExplainDone, Summary: "ok"})
	snap.Stats.Explained = 3
	snap.Stats.Explaining = 1
	snap.Stats.ExplainFailed = 2
	snap.Stats.Dropped = 4
	m = apply(t, m, snap)

	status := m.viewStatus()
	for _, want := range []string{"llm", "3✓", "1⏳", "2✗", "drop 4"} {
		if !strings.Contains(status, want) {
			t.Errorf("the status bar is missing %q:\n%s", want, status)
		}
	}
}

// TestStatusBarOmitsLLMCountersWithoutAModel keeps a dry run's status bar exactly as it
// was: there is no LLM, so there is nothing to count.
func TestStatusBarOmitsLLMCountersWithoutAModel(t *testing.T) {
	m := New(nil, nil, Options{ExplainDryRun: true})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = apply(t, sized.(Model), escalationSnapshot(nil))

	if strings.Contains(m.viewStatus(), "llm") {
		t.Errorf("the dry-run status bar counts LLM calls that were never made:\n%s", m.viewStatus())
	}
	if !strings.Contains(m.View(), "WOULD ESCALATE") {
		t.Error("the dry-run pane stopped rendering")
	}
}

// TestCardsPaneNeverResizesTheStream is the M5 replacement for the old chrome-height
// test, and it asserts the inverse property — which is the whole reason the cards moved
// into a pane of their own.
//
// The pinned pane USED to grow as explanations landed, taking rows out of the stream and
// resizing the viewport under the event loop. Now the panes are fixed by the geometry: a
// card can grow, shrink, expand or collapse and the stream must not move by a single row.
func TestCardsPaneNeverResizesTheStream(t *testing.T) {
	base := explainModel(t)
	pending := apply(t, base, escalationSnapshot(&model.Explanation{Hash: "aaa", State: model.ExplainPending}))
	explained := apply(t, pending, escalationSnapshot(&model.Explanation{
		Hash: "aaa", State: model.ExplainDone,
		Summary: "A handler wrote to a nil map.", LikelyCause: "Never initialised.", Suggestion: "Make it.",
	}))
	expanded := expandSelected(t, explained)

	want := base.stream.Height
	for name, m := range map[string]Model{
		"pending": pending, "explained": explained, "expanded": expanded,
	} {
		if got := m.stream.Height; got != want {
			t.Errorf("%s: stream height = %d, want %d — the cards pane is resizing the stream",
				name, got, want)
		}
	}
}
