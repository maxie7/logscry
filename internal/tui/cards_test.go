// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// cardsModel is a sized model with an LLM attached and n escalations already on screen,
// newest last in the snapshot (as the pipeline delivers them).
func cardsModel(t *testing.T, n int) Model {
	t.Helper()
	m := New(nil, nil, Options{Explain: true})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

	snap := pipeline.Snapshot{Stats: pipeline.Stats{UniqueTemplates: 3, Escalations: n}}
	for i := range n {
		snap.Escalations = append(snap.Escalations, pipeline.Event{
			Hash:     "h" + strconv.Itoa(i),
			Pattern:  "failure " + strconv.Itoa(i),
			Line:     model.LogLine{Source: "docker:api", Level: "ERROR"},
			Count:    i + 1,
			Score:    1.5,
			Escalate: true,
			Reasons:  []string{"novel template (first seen)"},
			Explanation: &model.Explanation{
				Hash: "h" + strconv.Itoa(i), State: model.ExplainDone,
				Summary:     "Summary " + strconv.Itoa(i),
				LikelyCause: "Cause " + strconv.Itoa(i),
				Suggestion:  "Check " + strconv.Itoa(i),
			},
		})
	}
	applied, _ := sized.(Model).Update(snapshotMsg(snap))
	return applied.(Model)
}

// TestSeverityBadges pins the mapping every card's colour hangs off, including the case
// that actually turns up most often in the wild: no level at all.
func TestSeverityBadges(t *testing.T) {
	tests := map[string]struct {
		sev  severity
		text string
	}{
		"PANIC":    {sevFatal, "PANIC"},
		"FATAL":    {sevFatal, "FATAL"},
		"CRITICAL": {sevFatal, "CRITICAL"},
		"ERROR":    {sevError, "ERROR"},
		"error":    {sevError, "ERROR"}, // levels arrive in whatever case the app logged
		"WARN":     {sevWarn, "WARN"},
		"WARNING":  {sevWarn, "WARNING"},
		"INFO":     {sevNone, "INFO"},
		"DEBUG":    {sevNone, "DEBUG"},
		"":         {sevNone, "EVENT"},  // no level: still badged, so the column holds
		"NOTICE":   {sevNone, "NOTICE"}, // an app's own invented level is not an error
	}
	for level, want := range tests {
		if got := severityOf(level); got != want.sev {
			t.Errorf("severityOf(%q) = %d, want %d", level, got, want.sev)
		}
		if got := badgeText(level); got != want.text {
			t.Errorf("badgeText(%q) = %q, want %q", level, got, want.text)
		}
		// Whatever the level, the badge occupies exactly one column width — that is what
		// keeps a list of cards scannable rather than ragged.
		if got := visWidth(stripANSI(renderBadge(level))); got != badgeWidth {
			t.Errorf("renderBadge(%q) is %d cells wide, want %d", level, got, badgeWidth)
		}
	}
}

// TestCardStatesRender: the three states a card can be in must each be unmistakable. A
// pending card that looks explained, or a failed one that looks pending, is worse than
// no card — it is a card that lies about whether an answer is coming.
func TestCardStatesRender(t *testing.T) {
	tests := map[string]struct {
		ex       *model.Explanation
		want     string
		wantNot  string
		headline string
	}{
		"pending": {
			ex:       &model.Explanation{Hash: "aaa", State: model.ExplainPending},
			want:     "explaining…",
			wantNot:  "explanation unavailable",
			headline: "panic: nil map write in handler <NUM>", // the template, until the answer lands
		},
		"explained": {
			ex: &model.Explanation{
				Hash: "aaa", State: model.ExplainDone, Summary: "A handler wrote to a nil map.",
			},
			want:     "A handler wrote to a nil map.",
			wantNot:  "explaining…",
			headline: "A handler wrote to a nil map.", // the answer becomes the headline
		},
		"failed": {
			ex: &model.Explanation{
				Hash: "aaa", State: model.ExplainFailed, Err: "connection refused",
			},
			want:     "explanation unavailable",
			wantNot:  "explaining…",
			headline: "panic: nil map write in handler <NUM>",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			m := apply(t, explainModel(t), escalationSnapshot(tc.ex))
			view := m.View()

			if !strings.Contains(view, tc.want) {
				t.Errorf("a %s card does not show %q:\n%s", name, tc.want, view)
			}
			if strings.Contains(view, tc.wantNot) {
				t.Errorf("a %s card wrongly shows %q:\n%s", name, tc.wantNot, view)
			}
			if got := m.cardHeadline(m.snap.Escalations[0]); got != tc.headline {
				t.Errorf("headline = %q, want %q", got, tc.headline)
			}
		})
	}
}

// TestExpandCollapse: enter opens the selected card and enter closes it again. The detail
// is what makes a card worth having; hiding it behind a key is what keeps a pane of ten
// cards readable.
func TestExpandCollapse(t *testing.T) {
	m := cardsModel(t, 3)
	focused, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = focused.(Model)

	if strings.Contains(m.View(), "cause: Cause 2") {
		t.Fatal("the card is expanded before anything was pressed")
	}

	opened, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = opened.(Model)
	if !strings.Contains(m.View(), "cause: Cause 2") {
		t.Errorf("enter did not expand the selected card:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "check: Check 2") {
		t.Errorf("the expanded card is missing the suggestion:\n%s", m.View())
	}

	closed, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = closed.(Model)
	if strings.Contains(m.View(), "cause: Cause 2") {
		t.Errorf("enter did not collapse the card again:\n%s", m.View())
	}

	// Space is the other half of the advertised binding.
	spaced, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if !strings.Contains(spaced.(Model).View(), "cause: Cause 2") {
		t.Error("space did not expand the selected card")
	}
}

// TestExpandedCardCarriesTheEvidence: an expanded card must answer "why did this fire",
// "how long has it been happening", and "what else was going on" — without which the
// user has to go back to the raw logs, which is the thing the tool exists to avoid.
func TestExpandedCardCarriesTheEvidence(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 6, 2, 0, time.UTC)
	ev := pipeline.Event{
		Hash:      "aaa",
		Pattern:   "panic: nil map write",
		Line:      model.LogLine{Source: "docker:api", Level: "PANIC"},
		Count:     12,
		FirstSeen: now.Add(-2 * time.Minute),
		LastSeen:  now.Add(-90 * time.Second),
		Score:     1.35,
		Escalate:  true,
		Reasons:   []string{"novel template (first seen)", "burst 8x baseline"},
		Context:   []string{"POST /checkout 500", "goroutine 42 running"},
	}
	m := New(nil, nil, Options{Explain: true})
	m.now = func() time.Time { return now }
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	applied, _ := sized.(Model).Update(snapshotMsg(pipeline.Snapshot{
		Escalations: []pipeline.Event{ev},
		Stats:       pipeline.Stats{Escalations: 1},
	}))
	view := expandSelected(t, applied.(Model)).View()

	// The reason list wraps rather than truncating, so it is asserted in the pieces the
	// pane actually lays out — but every piece has to be there, the score included: a
	// reason list cut off before the number is a reason list with the evidence removed.
	for _, want := range []string{
		"why:",
		"novel template (first seen)", // why it escalated
		"score 1.35",
		"first 12:04:02 · last 12:04:32", // how long it has been happening
		"1m ago",                         // and in human terms
		"POST /checkout 500",             // what else was going on: the ring buffer
		"goroutine 42 running",
		"x12",        // how often
		"docker:api", // where from
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the expanded card is missing %q:\n%s", want, view)
		}
	}
}

// TestSelectionMovesAndClamps: down walks back through the cards, up walks forward, and
// neither runs off the end of the list.
func TestSelectionMovesAndClamps(t *testing.T) {
	m := cardsModel(t, 3)
	focused, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = focused.(Model)

	if m.selKey != "h2" {
		t.Fatalf("selection starts on %q, want the newest card (h2)", m.selKey)
	}

	down, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = down.(Model)
	if m.selKey != "h1" {
		t.Errorf("after down, selection is %q, want h1", m.selKey)
	}

	// Off the bottom: the selection stops at the oldest card rather than wrapping or
	// running past the end.
	for range 5 {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(Model)
	}
	if m.selKey != "h0" {
		t.Errorf("selection ran past the oldest card to %q, want h0", m.selKey)
	}

	for range 5 {
		prev, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = prev.(Model)
	}
	if m.selKey != "h2" {
		t.Errorf("selection ran past the newest card to %q, want h2", m.selKey)
	}
}

// TestRelTime pins the phrasing on the card's most-read field.
func TestRelTime(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	tests := map[time.Duration]string{
		0:                "just now",
		3 * time.Second:  "just now",
		30 * time.Second: "30s ago",
		90 * time.Second: "1m ago",
		45 * time.Minute: "45m ago",
		3 * time.Hour:    "3h ago",
		50 * time.Hour:   "2d ago",
	}
	for ago, want := range tests {
		if got := relTime(now.Add(-ago), now); got != want {
			t.Errorf("relTime(%s ago) = %q, want %q", ago, got, want)
		}
	}
	if got := relTime(time.Time{}, now); got != "" {
		t.Errorf("relTime(zero) = %q, want empty", got)
	}
}

// stripANSI removes escape sequences so a test can measure the text a badge actually
// paints.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
