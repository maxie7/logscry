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
		// A streamed summary is promoted to the headline the moment it completes, but the
		// card must keep saying it is still working — see TestStreamingCardStaysHonest.
		"streaming": {
			ex: &model.Explanation{
				Hash: "aaa", State: model.ExplainPending, Summary: "A handler wrote to a nil map.",
			},
			want:     "explaining…",
			wantNot:  "explanation unavailable",
			headline: "A handler wrote to a nil map.",
		},
		// Salvaged from a stream that died: the fields are real, the answer is short, and
		// the card says so rather than passing it off as the model's finished verdict.
		"truncated": {
			ex: &model.Explanation{
				Hash: "aaa", State: model.ExplainDone, Truncated: true,
				Summary: "A handler wrote to a nil map.",
			},
			want:     "answer incomplete",
			wantNot:  "explaining…",
			headline: "A handler wrote to a nil map.",
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

// TestStreamingCardStaysHonest: while an answer streams in, the card shows what has arrived
// AND still says "explaining…" at the same time.
//
// That pairing is the whole point. A card rendering a partial answer while reading as
// finished is the same dishonesty as an unbadged truncation: the summary is there, but the
// model has not committed to the cause or the check yet, and the status line is the only
// thing telling the reader so.
func TestStreamingCardStaysHonest(t *testing.T) {
	streaming := &model.Explanation{
		Hash: "aaa", State: model.ExplainPending,
		Summary:     "A handler wrote to a nil map.",
		LikelyCause: "The map was never initialised.",
	}
	m := apply(t, explainModel(t), escalationSnapshot(streaming))
	focused, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	opened, _ := focused.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	view := opened.(Model).View()

	for _, want := range []string{
		"A handler wrote to a nil map.",  // the summary, already promoted to the headline
		"The map was never initialised.", // the cause, as soon as that field completed
		"explaining…",                    // and still plainly unfinished
	} {
		if !strings.Contains(view, want) {
			t.Errorf("a streaming card does not show %q:\n%s", want, view)
		}
	}
}

// TestExpandCollapse: enter opens the selected card and enter closes it again. The detail
// is what makes a card worth having; hiding it behind a key is what keeps a pane of ten
// cards readable.
func TestExpandCollapse(t *testing.T) {
	m := cardsModel(t, 3)
	focused, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = focused.(Model)

	if strings.Contains(m.View(), "cause:  Cause 2") {
		t.Fatal("the card is expanded before anything was pressed")
	}

	opened, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = opened.(Model)
	if !strings.Contains(m.View(), "cause:  Cause 2") {
		t.Errorf("enter did not expand the selected card:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "check:  Check 2") {
		t.Errorf("the expanded card is missing the suggestion:\n%s", m.View())
	}

	closed, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = closed.(Model)
	if strings.Contains(m.View(), "cause:  Cause 2") {
		t.Errorf("enter did not collapse the card again:\n%s", m.View())
	}

	// Space is the other half of the advertised binding.
	spaced, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if !strings.Contains(spaced.(Model).View(), "cause:  Cause 2") {
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

// ---------------------------------------------------------------------------
// Flag history (issue #34): a card is a TEMPLATE, and it says how often it has fired.
// ---------------------------------------------------------------------------

// The clock the flag-history tests read against. Every value is distinct so an assertion
// on one timestamp cannot pass because a different field happened to print the same one.
var (
	cardNow       = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	cardFirstSeen = cardNow.Add(-3 * time.Hour)    // 09:00:00
	cardFirstFlag = cardNow.Add(-2 * time.Hour)    // 10:00:00
	cardLastFlag  = cardNow.Add(-time.Hour)        // 11:00:00
	cardLastSeen  = cardNow.Add(-30 * time.Second) // 11:59:30
)

// flaggedModel is a sized model showing ONE card for a template flagged `flags` times.
// A single flag sits at cardFirstFlag; more than one spans cardFirstFlag..cardLastFlag.
func flaggedModel(t *testing.T, flags int, ex *model.Explanation) Model {
	t.Helper()
	m := New(nil, nil, Options{Explain: true})
	m.now = func() time.Time { return cardNow }
	// Wide on purpose: this card carries every optional field at once (a source, a relative
	// time, a flag badge and a status), which no real card does, and these tests are about
	// what the card SAYS rather than about where the pane truncates it.
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})

	lastFlag := cardLastFlag
	if flags < 2 {
		lastFlag = cardFirstFlag
	}
	ev := pipeline.Event{
		Hash:      "aaa",
		Pattern:   "panic: nil map write in handler <NUM>",
		Line:      model.LogLine{Source: "docker:api", Level: "PANIC"},
		Count:     9,
		FirstSeen: cardFirstSeen,
		LastSeen:  cardLastSeen,
		Score:     1.35,
		Escalate:  true,
		Queued:    true,
		Reasons: []string{
			"novel template (unseen for 1h41m7s, cooloff 15m0s)", "level PANIC",
		},
		FlagCount:    flags,
		FirstFlagged: cardFirstFlag,
		LastFlagged:  lastFlag,
		Explanation:  ex,
	}
	applied, _ := sized.(Model).Update(snapshotMsg(pipeline.Snapshot{
		Lines:       []pipeline.Event{ev},
		Escalations: []pipeline.Event{ev},
		Stats:       pipeline.Stats{TotalLines: 20, UniqueTemplates: 1, Escalations: flags},
	}))
	return applied.(Model)
}

// TestReescalationIsVisibleOnTheCard: merging the two cards a re-escalation used to
// produce must not lose the fact that it fired twice. The card carries it instead — and
// the numbers beside it are the template's live ones, which is issue #34's actual ask.
func TestReescalationIsVisibleOnTheCard(t *testing.T) {
	m := flaggedModel(t, 2, nil)

	collapsed := m.View()
	for _, want := range []string{
		"⚑2",         // it has fired twice
		"x9",         // and this is how often the template has occurred, live
		"30s ago",    // last seen 30s ago, not two hours ago when it was flagged
		"docker:api", // where from
	} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("the collapsed card is missing %q:\n%s", want, collapsed)
		}
	}

	expanded := expandSelected(t, m).View()
	for _, want := range []string{
		"2× · first 10:00:00 · last 11:00:00", // the flag history
		"first 09:00:00 · last 11:59:30",      // and the occurrence history, which is different
	} {
		if !strings.Contains(expanded, want) {
			t.Errorf("the expanded card is missing %q:\n%s", want, expanded)
		}
	}
}

// TestSingleFlagCarriesNoBadge pins the suppression rule. A badge on every card carries no
// information; the badge APPEARING is the signal that a template came back.
func TestSingleFlagCarriesNoBadge(t *testing.T) {
	m := flaggedModel(t, 1, nil)

	if collapsed := m.View(); strings.Contains(collapsed, "⚑") {
		t.Errorf("a card flagged once wears a flag badge:\n%s", collapsed)
	}

	// The flag time is still stated when expanded: it is what says the "why" above it is a
	// verdict from a moment rather than a claim about now.
	expanded := expandSelected(t, m).View()
	if !strings.Contains(expanded, "10:00:00") {
		t.Errorf("the expanded card does not say when it was flagged:\n%s", expanded)
	}
	if strings.Contains(expanded, "×") {
		t.Errorf("a card flagged once shows a multiplier:\n%s", expanded)
	}
}

// TestCardsTitleCountsCardsAndFlags: the title counts what is in the pane, and states the
// flag count only when the two differ — the bracket appearing is itself the information
// that a template came back. The status bar's "esc" is untouched and keeps counting flags.
func TestCardsTitleCountsCardsAndFlags(t *testing.T) {
	title := func(cards, flags int, dry bool) string {
		m := New(nil, nil, Options{Explain: true, ExplainDryRun: dry})
		m.snap = pipeline.Snapshot{
			Escalations: make([]pipeline.Event, cards),
			Stats:       pipeline.Stats{Escalations: flags},
		}
		return m.cardsTitle()
	}

	tests := []struct {
		name         string
		cards, flags int
		dry          bool
		want         string
	}{
		{"none", 0, 0, false, "ANOMALIES"},
		{"equal", 3, 3, false, "ANOMALIES · 3"},
		{"reescalated", 3, 6, false, "ANOMALIES · 3 (6 flags)"},
		{"dry equal", 3, 3, true, "WOULD ESCALATE · 3 (dry run — no LLM called)"},
		{"dry reescalated", 3, 6, true, "WOULD ESCALATE · 3 (6 flags · dry run — no LLM)"},
	}
	for _, tc := range tests {
		if got := title(tc.cards, tc.flags, tc.dry); got != tc.want {
			t.Errorf("%s: cardsTitle() = %q, want %q", tc.name, got, tc.want)
		}
		// The cards pane is 52 cells at a 120-column terminal. A title that truncates there
		// loses the dry-run disclaimer, which is the one thing it exists to say.
		if got := title(tc.cards, tc.flags, tc.dry); visWidth(got) > 52 {
			t.Errorf("%s: title is %d cells and truncates in a 120-column terminal: %q",
				tc.name, visWidth(got), got)
		}
	}
}

// TestReescalationKeepsTheEarlierAnswer: a re-escalation asks the model a new question, so
// the card goes back to "explaining…" — but it must not throw away the answer it already
// has while the new one is in flight. Before the merge this cost nothing, because the OLD
// card kept its answer and a second card carried the pending state.
func TestReescalationKeepsTheEarlierAnswer(t *testing.T) {
	m := flaggedModel(t, 2, &model.Explanation{
		Hash:        "aaa",
		State:       model.ExplainPending,
		Summary:     "A handler wrote to a nil map.",
		LikelyCause: "The cache map is never initialised.",
		Suggestion:  "Make the map in NewServer.",
		At:          cardFirstFlag, // the answer to the FIRST flag
	})

	collapsed := m.View()
	for _, want := range []string{
		"A handler wrote to a nil map.", // the answer is still the headline
		"explaining…",                   // and a new one is on its way
		"⚑2",
	} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("the collapsed card is missing %q:\n%s", want, collapsed)
		}
	}

	expanded := expandSelected(t, m).View()
	for _, want := range []string{
		"The cache map is never initialised.",
		"Make the map in NewServer.",
		"from the flag at 10:00:00", // and it says WHICH flag the answer belongs to
	} {
		if !strings.Contains(expanded, want) {
			t.Errorf("the expanded card is missing %q:\n%s", want, expanded)
		}
	}
}

// TestFailedRetryKeepsTheEarlierAnswer is the failure path, and the regression the merge
// would otherwise have introduced: a single card dropping a good answer for "explanation
// unavailable" the moment a re-explain fails. RDI §7 says a dead model degrades a card, it
// does not empty one.
func TestFailedRetryKeepsTheEarlierAnswer(t *testing.T) {
	t.Run("with an earlier answer", func(t *testing.T) {
		m := flaggedModel(t, 2, &model.Explanation{
			Hash:        "aaa",
			State:       model.ExplainFailed,
			Summary:     "A handler wrote to a nil map.",
			LikelyCause: "The cache map is never initialised.",
			Err:         "connection refused",
			At:          cardFirstFlag,
		})

		collapsed := m.View()
		if !strings.Contains(collapsed, "A handler wrote to a nil map.") {
			t.Errorf("a failed retry threw away the answer the card already had:\n%s", collapsed)
		}
		if !strings.Contains(collapsed, "retry failed") {
			t.Errorf("the card does not say the retry failed:\n%s", collapsed)
		}
		if strings.Contains(collapsed, "explanation unavailable") {
			t.Errorf("the card claims to have no explanation while showing one:\n%s", collapsed)
		}

		expanded := expandSelected(t, m).View()
		for _, want := range []string{
			"The cache map is never initialised.", // the answer survives
			"connection refused",                  // and so does the reason the retry failed
			"from the flag at 10:00:00",
		} {
			if !strings.Contains(expanded, want) {
				t.Errorf("the expanded card is missing %q:\n%s", want, expanded)
			}
		}
	})

	// Nothing changes for the case that has no answer to keep: the first explanation of a
	// template failing still reads exactly as it always has.
	t.Run("with no answer at all", func(t *testing.T) {
		m := flaggedModel(t, 1, &model.Explanation{
			Hash: "aaa", State: model.ExplainFailed, Err: "connection refused", At: cardFirstFlag,
		})
		view := m.View()
		if !strings.Contains(view, "explanation unavailable") {
			t.Errorf("a card with no answer at all stopped saying so:\n%s", view)
		}
		if strings.Contains(view, "retry failed") {
			t.Errorf("a first failed explanation is not a failed retry:\n%s", view)
		}
	})
}

// TestFlagGlyphIsOneCell: the meta line is measured in CELLS, and a glyph counted as one
// that paints two is how a pane silently loses a column (see visWidth's note on "⏳").
//
// This cannot tell whether the terminal FONT has the glyph — a missing one renders as a box
// and still measures one cell — so the flag is also confirmed by eye in the smoke test.
func TestFlagGlyphIsOneCell(t *testing.T) {
	if got := visWidth(flagGlyph); got != 1 {
		t.Errorf("visWidth(%q) = %d, want 1", flagGlyph, got)
	}
}
