// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// The flagged-event cards: the right-hand pane, and the reason the tool exists. The
// stream is what logscry is looking at; a card is what it has to say.

// severity is the card's colour class. It is coarser than the log level on purpose:
// what a badge has to answer in one glance is "how bad", not "which of nine words did
// this program happen to print".
type severity int

const (
	sevNone severity = iota // no level detected, or an informational one
	sevWarn
	sevError
	sevFatal
)

// severityOf classifies a log level. An unrecognized level is not an error — plenty of
// programs invent their own — so it lands in sevNone and is styled quietly.
func severityOf(level string) severity {
	switch strings.ToUpper(level) {
	case "PANIC", "FATAL", "CRITICAL", "CRIT", "EMERG", "ALERT":
		return sevFatal
	case "ERROR", "ERR":
		return sevError
	case "WARN", "WARNING":
		return sevWarn
	default:
		return sevNone
	}
}

// badgeText is what the badge says. A line with no level still gets a badge — an empty
// slot would break the column that every other card lines up on.
func badgeText(level string) string {
	if level == "" {
		return "EVENT"
	}
	return strings.ToUpper(level)
}

// badgeWidth keeps every card's summary starting in the same column, which is what makes
// a list of cards scannable instead of ragged. "CRITICAL" is the longest badge that
// fits; anything longer is truncated into it.
const badgeWidth = 8

// renderBadge draws the severity badge for a level.
func renderBadge(level string) string {
	return badgeStyles[severityOf(level)].Render(pad(truncate(badgeText(level), badgeWidth), badgeWidth))
}

// card is one rendered flagged event: its identity, and the lines it occupies. The
// height is not a constant — a card grows when it is expanded, and an explanation adds
// lines when it lands — so the renderer returns the lines and the caller counts them,
// rather than anyone assuming.
type card struct {
	hash  string
	lines []string
}

// height is how many rows this card takes, including the blank line under it.
func (c card) height() int { return len(c.lines) }

// cardEvents are the escalations, newest first: a live tail, where the thing that just
// happened is the thing that matters. The snapshot carries them oldest first.
func (m Model) cardEvents() []pipeline.Event {
	evs := m.snap.Escalations
	out := make([]pipeline.Event, 0, len(evs))
	for i := len(evs) - 1; i >= 0; i-- {
		out = append(out, evs[i])
	}
	return out
}

// renderCards lays out every card at this width, in display order.
func (m Model) renderCards(width int) []card {
	evs := m.cardEvents()
	cards := make([]card, 0, len(evs))
	for _, ev := range evs {
		cards = append(cards, m.renderCard(ev, width, ev.Hash == m.selKey))
	}
	return cards
}

// renderCard draws one card: collapsed, it is the headline and one line of provenance;
// expanded, it is everything that was known about the event and everything the model
// said about it.
func (m Model) renderCard(ev pipeline.Event, width int, selected bool) card {
	// The gutter carries the selection marker, and doubles as the expand affordance:
	// the arrow turns down when the card is open.
	marker := " "
	if selected {
		marker = "▸"
		if m.expanded[ev.Hash] {
			marker = "▾"
		}
	}
	gutter := styleSelect.Render(marker) + " "
	indent := "   " // the width of the gutter, for the lines under the headline
	body := max(width-visWidth(indent), 1)

	// The headline is truncated against the width the badge and the gutter actually leave
	// it, measured in cells — not runes, and not bytes.
	avail := max(width-badgeWidth-visWidth(gutter)-1, 1)
	head := gutter + renderBadge(ev.Line.Level) + " " + truncate(m.cardHeadline(ev), avail)

	lines := []string{head, indent + styleHint.Render(truncate(m.cardMeta(ev), body))}

	// The scorer's verdict stays on the COLLAPSED card, not behind an expand. In dry-run
	// and in any run without a model it is the only thing on the card that is worth
	// reading — it is what the thresholds get tuned against — and asking someone to
	// expand every card to calibrate is asking them not to calibrate. It wraps rather
	// than truncates: a reason list cut off before the score is a reason list with the
	// number removed.
	if why := m.whyText(ev); why != "" {
		lines = append(lines, wrapField("why", why, body, indent)...)
	}
	if selected && m.expanded[ev.Hash] {
		lines = append(lines, m.cardDetail(ev, body, indent)...)
	}
	lines = append(lines, "") // cards are separated by a blank line, never a rule

	return card{hash: ev.Hash, lines: lines}
}

// whyText is the scorer's case for this event: what fired, and how hard. Empty when the
// pipeline ran without a scorer at all, where there is no case to state.
func (m Model) whyText(ev pipeline.Event) string {
	if len(ev.Reasons) == 0 && ev.Score == 0 {
		return ""
	}
	return fmt.Sprintf("%s · score %.2f", strings.Join(ev.Reasons, ", "), ev.Score)
}

// cardHeadline is the one line a user reads if they read nothing else.
//
// With an answer it is the model's summary — plain English, which is the entire point of
// escalating. Without one it is the template, because a masked pattern is still a far
// better headline than a placeholder: it says what fired.
//
// The headline gets the full width of the card and shares it with nothing. The state of
// the explanation goes on the meta line instead (see cardStatus): at 100 columns the
// cards pane is ~40 wide, and spending 15 of them on the word "explaining…" would leave
// the headline too narrow to say what had actually happened.
func (m Model) cardHeadline(ev pipeline.Event) string {
	// Any summary beats the template, including one still being streamed in — the whole
	// point of streaming is that it lands before the rest of the answer does. The status
	// line, not this, is what says whether the model has finished (see cardStatus).
	ex := ev.Explanation
	if ex != nil && ex.Summary != "" {
		return ex.Summary
	}
	return ev.Pattern
}

// cardMeta is the provenance line: how often, from where, how long ago — and where the
// explanation has got to.
func (m Model) cardMeta(ev pipeline.Event) string {
	parts := []string{"x" + formatCount(ev.Count)}
	if ev.Line.Source != "" {
		parts = append(parts, ev.Line.Source)
	}
	if rel := relTime(ev.LastSeen, m.now()); rel != "" {
		parts = append(parts, rel)
	}
	if status := cardStatus(ev); status != "" {
		parts = append(parts, status)
	}
	return strings.Join(parts, " · ")
}

// cardStatus is where this card's explanation has got to: waiting on the model, or never
// getting one. It is empty for a finished card — the answer IS the headline by then —
// and empty when no model was ever attached, because "unavailable" would be a lie about
// a question nobody asked.
//
// While an answer streams in, this keeps saying "explaining…" even though the headline
// already shows a summary. That pairing is deliberate: a card that renders a partial answer
// while reading as finished is the same dishonesty as an unbadged truncation. Only a
// terminal update clears it.
func cardStatus(ev pipeline.Event) string {
	if ev.Explanation == nil {
		return ""
	}
	switch ev.Explanation.State {
	case model.ExplainFailed:
		return "explanation unavailable"
	case model.ExplainPending:
		return "explaining…"
	case model.ExplainDone:
		if ev.Explanation.Truncated {
			return "answer incomplete"
		}
		return ""
	default:
		return ""
	}
}

// cardDetail is everything the collapsed card left out: what the model concluded, when
// this template has been seen, and the lines that were going past when it happened.
func (m Model) cardDetail(ev pipeline.Event, width int, indent string) []string {
	var lines []string
	field := func(label, value string) {
		if value == "" {
			return
		}
		lines = append(lines, wrapField(label, value, width, indent)...)
	}

	if ex := ev.Explanation; ex != nil {
		switch ex.State {
		// Pending shares this arm so a streamed cause and check appear the moment each
		// completes, rather than all three fields landing at once at the end.
		case model.ExplainDone, model.ExplainPending:
			field("cause", ex.LikelyCause)
			field("check", ex.Suggestion)
		case model.ExplainFailed:
			// The reason a model could not answer is as operationally useful as an answer
			// — "connection refused" and "bad API key" are both fixable, and neither is
			// distinguishable from "the tool is broken" if the card just says nothing.
			field("failed", ex.Err)
		}
	}
	field("seen", m.seenText(ev))

	if len(ev.Context) > 0 {
		lines = append(lines, indent+styleHint.Render("context:"))
		for _, raw := range ev.Context {
			lines = append(lines, indent+"  "+styleHint.Render(truncate(raw, max(width-2, 1))))
		}
	}
	return lines
}

// seenText is the template's history: first and last occurrence. It is what separates
// "this has been broken all along and you never noticed" from "this started 40 seconds
// ago", which is usually the first thing anyone wants to know.
func (m Model) seenText(ev pipeline.Event) string {
	if ev.FirstSeen.IsZero() {
		return ""
	}
	return fmt.Sprintf("first %s · last %s",
		ev.FirstSeen.Format("15:04:05"), ev.LastSeen.Format("15:04:05"))
}

// wrapField renders "label: value" wrapped to width, with continuation lines aligned
// under the value rather than under the label — prose that re-indents itself every line
// is prose nobody reads.
func wrapField(label, value string, width int, indent string) []string {
	head := fmt.Sprintf("%-7s", label+":")
	body := max(width-visWidth(head), 10)

	wrapped := lipgloss.NewStyle().Width(body).Render(value)
	var out []string
	for i, l := range strings.Split(wrapped, "\n") {
		if i == 0 {
			out = append(out, indent+styleField.Render(head)+l)
			continue
		}
		out = append(out, indent+strings.Repeat(" ", visWidth(head))+l)
	}
	return out
}

// viewCardsEmpty is the state the pane is in almost all the time, and the state a
// reviewer sees first: a healthy system, nothing to report.
//
// It has to read as "working, with nothing to say" — not as an unfinished pane. That is
// the whole thesis of the tool made visible: silence is the correct output, and a tool
// that is silent because it is confident looks nothing like a tool that is silent
// because it is broken. So it says what it is watching, and how much of it.
func (m Model) viewCardsEmpty(width, height int) string {
	watching := fmt.Sprintf("Watching %s %s across %s.",
		formatCount(m.snap.Stats.UniqueTemplates),
		plural(m.snap.Stats.UniqueTemplates, "template", "templates"),
		sourcesPhrase(len(m.snap.Stats.Sources)))

	block := lipgloss.JoinVertical(lipgloss.Center,
		styleQuiet.Render("✓  No anomalies"),
		"",
		styleHint.Render(watching),
		styleHint.Render("Quiet is the expected state — a card appears"),
		styleHint.Render("the moment something is worth your attention."),
	)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
}

// sourcesPhrase avoids "across 0 sources" reading as a fault when the run has simply not
// had a line yet.
func sourcesPhrase(n int) string {
	if n == 0 {
		return "no sources yet"
	}
	return fmt.Sprintf("%d %s", n, plural(n, "source", "sources"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// cardsTitle names the pane and counts what is in it. In dry-run it must say that
// nothing was asked of a model: the whole point of the flag is that these are the events
// that WOULD have been escalated, and quietly calling them "anomalies" would imply an
// answer was sought and none came.
func (m Model) cardsTitle() string {
	n := m.snap.Stats.Escalations
	if m.opts.ExplainDryRun {
		return fmt.Sprintf("WOULD ESCALATE · %s (dry run — no LLM called)", formatCount(n))
	}
	if n == 0 {
		return "ANOMALIES"
	}
	return fmt.Sprintf("ANOMALIES · %s", formatCount(n))
}

// relTime renders how long ago t was, in the coarsest unit that is still true. "" for
// the zero time, which is what an event that never carried one has.
func relTime(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < 0, d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// pad right-pads s to width runes.
func pad(s string, width int) string {
	if n := width - visWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}
