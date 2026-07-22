// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Colours are lipgloss AdaptiveColor: they carry a light and a dark variant, and
// lipgloss downsamples the hex to whatever the terminal can actually show. Nothing here
// hardcodes an escape sequence, so the same styles survive a 16-colour terminal, a
// light background, and NO_COLOR.
var (
	colFatal = lipgloss.AdaptiveColor{Light: "#C0392B", Dark: "#FF5F5F"}
	colError = lipgloss.AdaptiveColor{Light: "#D35400", Dark: "#FF8C42"}
	colWarn  = lipgloss.AdaptiveColor{Light: "#B7791F", Dark: "#E5C07B"}
	colDim   = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#6B7280"}
	colFocus = lipgloss.AdaptiveColor{Light: "#0F766E", Dark: "#5EEAD4"}
	colQuiet = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	colOn    = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#101010"} // text ON a badge
)

// Styles are package-level: lipgloss resolves the terminal's colour profile once,
// and re-creating them per frame would be wasted work at 10 fps.
var (
	styleSource = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // cyan
	styleCount  = lipgloss.NewStyle().Foreground(colDim)
	styleStatus = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("8"))
	styleErr    = lipgloss.NewStyle().Foreground(colFatal)
	styleHint   = lipgloss.NewStyle().Foreground(colDim)
	styleHead   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	styleQuiet  = lipgloss.NewStyle().Foreground(colQuiet).Bold(true)
	styleField  = lipgloss.NewStyle().Foreground(colDim)
	styleSelect = lipgloss.NewStyle().Foreground(colFocus).Bold(true)

	// styleEscalate marks a would-be escalation inline in --explain-dry-run. It has to
	// jump off a wall of log lines — that is the whole point of the mode.
	styleEscalate = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true) // magenta

	// The focused pane's border is the only thing telling the user where the arrow keys
	// will land, so the two states have to be obvious at a glance, not a shade apart.
	styleBorderFocus = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colFocus)
	styleBorderDim   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colDim)
	styleTitleFocus  = lipgloss.NewStyle().Foreground(colFocus).Bold(true)
	styleTitleDim    = lipgloss.NewStyle().Foreground(colDim).Bold(true)

	// badgeStyles colour the severity badge. Fatal and error carry a filled background —
	// they have to be findable in peripheral vision — while anything informational is
	// deliberately quiet: a card that is merely FYI must not shout as loudly as a panic.
	badgeStyles = map[severity]lipgloss.Style{
		sevFatal: lipgloss.NewStyle().Foreground(colOn).Background(colFatal).Bold(true),
		sevError: lipgloss.NewStyle().Foreground(colOn).Background(colError).Bold(true),
		sevWarn:  lipgloss.NewStyle().Foreground(colOn).Background(colWarn),
		sevNone:  lipgloss.NewStyle().Foreground(colDim),
	}

	// levelStyles colours the level token in the stream; unknown levels fall back to plain.
	levelStyles = map[string]lipgloss.Style{
		"ERROR":    lipgloss.NewStyle().Foreground(colFatal),
		"FATAL":    lipgloss.NewStyle().Foreground(colFatal),
		"PANIC":    lipgloss.NewStyle().Foreground(colFatal),
		"CRITICAL": lipgloss.NewStyle().Foreground(colFatal),
		"WARN":     lipgloss.NewStyle().Foreground(colWarn),
		"WARNING":  lipgloss.NewStyle().Foreground(colWarn),
		"INFO":     lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		"DEBUG":    lipgloss.NewStyle().Foreground(colDim),
		"TRACE":    lipgloss.NewStyle().Foreground(colDim),
	}
)

// View composes the frame: the live stream and the flagged-event cards, with the status
// bar under them.
//
// The two-pane composition IS the thesis of the tool — a torrent of noise on the left,
// a couple of calm explained cards on the right, and most of the time nothing on the
// right at all. Below splitMin the panes stack instead of shrinking into two unusable
// columns (see layout.go).
func (m Model) View() string {
	if !m.ready {
		return "starting logscry…"
	}

	// A pane with no room to be drawn renders as "" and is dropped rather than joined —
	// joining an empty string would cost a blank row that the geometry never budgeted for.
	var panes []string
	for _, p := range []string{
		m.pane(m.streamTitle(), m.stream.View(), m.geom.stream, m.focus == focusStream),
		m.pane(m.cardsPaneTitle(), m.cards.View(), m.geom.cards, m.focus == focusCards),
	} {
		if p != "" {
			panes = append(panes, p)
		}
	}

	body := lipgloss.JoinVertical(lipgloss.Left, panes...)
	if m.geom.layout == layoutSplit {
		body = lipgloss.JoinHorizontal(lipgloss.Top, panes...)
	}
	return lipgloss.JoinVertical(lipgloss.Left, body, m.viewStatus())
}

// pane frames one pane: a border that shows the focus, a title line, and the viewport
// under it. The border style fixes the size, so the pane occupies exactly the box the
// geometry gave it whether it is full, empty, or mid-resize.
func (m Model) pane(title, body string, b box, focused bool) string {
	w, h := b.inner()
	if w <= 0 || h <= 0 {
		return ""
	}
	border, titleStyle := styleBorderDim, styleTitleDim
	if focused {
		border, titleStyle = styleBorderFocus, styleTitleFocus
	}
	content := titleStyle.Render(pad(truncate(title, w), w)) + "\n" + body
	return border.Width(w).Height(h + 1).Render(content) // +1: the title line
}

// streamTitle names the left pane and reports its scroll-lock state.
func (m Model) streamTitle() string {
	name := "STREAM"
	if m.mode == aggregatedView {
		name = "AGGREGATED"
	}
	if n, scrolled := m.streamNew(); scrolled {
		return titleWith(name, scrollMarker(n), m.paneWidth(m.geom.stream))
	}
	return name
}

// cardsPaneTitle does the same for the cards pane.
func (m Model) cardsPaneTitle() string {
	if n, scrolled := m.cardsNew(); scrolled {
		return titleWith(m.cardsTitle(), scrollMarker(n), m.paneWidth(m.geom.cards))
	}
	return m.cardsTitle()
}

// scrollMarker is the "you are not at the live edge" warning. Getting this wrong is what
// ruins a live tail: a user who has scrolled up to read something must not be yanked back
// to the bottom by the next arrival — but they do need telling that arrivals are piling
// up out of sight, or a paused-looking pane reads as a dead one.
func scrollMarker(n int) string {
	if n == 0 {
		return "↑ scrolled"
	}
	return fmt.Sprintf("↑ scrolled — %s new", formatCount(n))
}

// titleWith right-aligns a marker against the title, dropping it entirely when the pane
// is too narrow to carry both — a half-marker is worse than none.
func titleWith(name, marker string, width int) string {
	gap := width - visWidth(name) - visWidth(marker) - 1
	if gap < 1 {
		return name
	}
	return name + strings.Repeat(" ", gap) + marker
}

// paneWidth is the usable width inside a pane.
func (m Model) paneWidth(b box) int {
	w, _ := b.inner()
	return w
}

// viewStream renders the live tail: one templated line per event, newest at the
// bottom. It shows the masked pattern rather than the raw text — that is the point
// of M2 — while Event.Line.Raw stays available for a future raw toggle.
func (m Model) viewStream(width int) string {
	if len(m.snap.Lines) == 0 {
		return styleHint.Render("waiting for logs…")
	}
	var b strings.Builder
	for i, ev := range m.snap.Lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		// The plain forms are measured first, so the pattern is truncated against the
		// width the decorations actually leave it — never against escape sequences.
		prefix := "[" + ev.Line.Source + " " + levelText(ev.Line.Level) + "] "
		count := ""
		if ev.Count > 1 {
			count = fmt.Sprintf("  x%d", ev.Count)
		}
		avail := max(width-visWidth(prefix)-visWidth(count), 1)

		b.WriteString(styleSource.Render("[" + ev.Line.Source + " "))
		b.WriteString(renderLevel(ev.Line.Level))
		b.WriteString(styleSource.Render("] "))
		b.WriteString(truncate(ev.Pattern, avail))
		if count != "" {
			b.WriteString(styleCount.Render(count))
		}
		// In dry-run, mark the line the scorer fired on, so it is visible WHERE in the
		// tail the anomaly sat. The reasons and the score are not repeated here: they
		// are on the card, in a pane that has the width to show them without cutting the
		// score off the end.
		if m.opts.ExplainDryRun && ev.Escalate {
			b.WriteByte('\n')
			b.WriteString(styleEscalate.Render(truncate(
				fmt.Sprintf("  ⇧ WOULD ESCALATE · score %.2f", ev.Score), width)))
		}
	}
	return b.String()
}

// viewAggregated renders the template table, already sorted by the pipeline
// (count desc, then last seen). This is where thousands of lines visibly collapse
// into a handful of templates.
func (m Model) viewAggregated(width int) string {
	if len(m.snap.Templates) == 0 {
		return styleHint.Render("no templates yet…")
	}

	// The pattern column takes whatever the fixed columns leave, so a narrow pane
	// truncates the message rather than wrapping the table.
	const fixed = 7 + 1 + 8 + 1 + 12 + 1 // count + level + last-seen + gaps
	patternWidth := max(width-fixed, 10)

	var b strings.Builder
	b.WriteString(styleHead.Render(fmt.Sprintf("%7s %-8s %-*s %12s",
		"COUNT", "LEVEL", patternWidth, "PATTERN", "LAST SEEN")))
	for _, t := range m.snap.Templates {
		b.WriteByte('\n')
		fmt.Fprintf(&b, "%7s %-8s %-*s %12s",
			formatCount(t.Count),
			levelText(t.Level),
			patternWidth, truncate(t.Pattern, patternWidth),
			t.LastSeen.Format("15:04:05"))
	}
	return b.String()
}

// viewStatus renders the two fixed lines of chrome: the run summary, and below it the
// key hints — or the most recent background error, which surfaces here rather than on
// stdout, where it would corrupt the alternate screen.
func (m Model) viewStatus() string {
	mode := "STREAM"
	if m.mode == aggregatedView {
		mode = "AGGREGATED"
	}
	if m.paused {
		mode += " · PAUSED"
	}
	if m.ended {
		mode += " · ENDED"
	}

	counters := fmt.Sprintf(" %s lines → %s templates | %.0f l/s | esc %s",
		formatCount(m.snap.Stats.TotalLines),
		formatCount(m.snap.Stats.UniqueTemplates),
		m.snap.Stats.LinesPerSec,
		formatCount(m.snap.Stats.Escalations))
	// Only shown once it has bitten. When the rate limiter is holding escalations
	// back, saying so is the difference between a quiet tool and a tool that looks
	// quiet because it is hiding things.
	if n := m.snap.Stats.Suppressed; n > 0 {
		counters += " · supp " + formatCount(n)
	}
	counters += m.llmCounters()
	// Part of the protected segment, not the elastic source list: a notice that raw logs
	// are leaving the machine must not be the first thing a narrow terminal drops.
	if h := m.opts.RemoteWarnHost; h != "" {
		counters += " · ⚠ raw logs → " + h + " (--llm-anonymize)"
	}

	sources := "no sources"
	if len(m.snap.Stats.Sources) > 0 {
		sources = strings.Join(m.snap.Stats.Sources, ", ")
	}
	sources += m.dockerTail()

	// The source list is the elastic segment: on a narrow terminal it gives way
	// first, so the counters and the mode (PAUSED, ENDED) never truncate away. Once
	// there is no room for it at all, it goes entirely — separators and all — rather
	// than leaving a stub of punctuation behind.
	const seps = 6 // the two " | " joins
	summary := counters + " | " + mode
	if budget := m.width - visWidth(counters) - visWidth(mode) - seps; budget > 0 {
		summary = counters + " | " + truncate(sources, budget) + " | " + mode
	}
	bar := styleStatus.Width(m.width).Render(truncate(summary, m.width))

	second := styleHint.Render(truncate(m.footer(), m.width))
	if err := m.lastError(); err != "" {
		second = styleErr.Render(truncate(" ! "+err, m.width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, bar, second)
}

// footer advertises the keys that actually do something WHERE THE FOCUS CURRENTLY IS.
// A footer that lists every key in the program is a footer that lies about half of them:
// ↵ expands nothing while the stream has focus, and there is no card to select.
func (m Model) footer() string {
	if m.focus == focusCards {
		return " tab stream · ↑/↓ select · ↵ expand · p pause · q quit"
	}
	return " tab cards · ↑/↓ scroll · t view · p pause · q quit"
}

// dockerTail surfaces the --docker-tail replay limit, and only when a Docker source is
// actually attached (it means nothing otherwise).
//
// It is in the status bar because its absence cost real time during M3: nothing said that
// the default 100 lines was ALL the backlog being replayed, so an event that happened
// further back never appeared, and the tool looked broken rather than bounded.
func (m Model) dockerTail() string {
	if m.opts.DockerTail == "" {
		return ""
	}
	for _, s := range m.snap.Stats.Sources {
		if strings.HasPrefix(s, "docker:") {
			return " · tail:" + m.opts.DockerTail
		}
	}
	return ""
}

// llmCounters is the status bar's account of the LLM stage: answered, in flight, failed.
// It is empty when no model is attached, so a dry run's status bar is unchanged.
//
// Failures and drops appear only once they have happened. They are the numbers that say
// "the model is down" or "the model cannot keep up with the anomalies" — the two things
// that would otherwise look exactly like a tool that has nothing to say.
func (m Model) llmCounters() string {
	if !m.opts.Explain {
		return ""
	}
	s := m.snap.Stats
	out := fmt.Sprintf(" | llm %s✓ %s⏳", formatCount(s.Explained), formatCount(s.Explaining))
	if s.ExplainFailed > 0 {
		out += " " + formatCount(s.ExplainFailed) + "✗"
	}
	if s.Dropped > 0 {
		out += " · drop " + formatCount(s.Dropped)
	}
	return out
}

// renderLevel colours a level token for the stream, using "-" when none was
// detected so the columns stay aligned.
func renderLevel(level string) string {
	text := levelText(level)
	if style, ok := levelStyles[strings.ToUpper(level)]; ok {
		return style.Render(text)
	}
	return text
}

// levelText is the displayed form of a level: "-" when undetected.
func levelText(level string) string {
	if level == "" {
		return "-"
	}
	return level
}

// formatCount renders n with thousands separators (12431 -> "12,431"), which is
// what makes the dedup ratio legible at a glance.
func formatCount(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// visWidth is how many terminal CELLS s occupies — not how many runes it has, and not
// how many bytes.
//
// The distinction is not academic. The status bar carries "⏳", which is one rune and two
// cells; counting it as one meant the bar measured 80 and drew 81, and lipgloss wrapped
// the overflow onto a second line — silently pushing a row of the layout off an 80-column
// screen. Escape sequences are the same problem in reverse: a dozen runes, zero cells.
func visWidth(s string) int { return ansi.StringWidth(s) }

// truncate cuts s to at most width cells, ellipsizing when it does. It is ANSI-aware, so
// it can never cut through the middle of an escape sequence and leak a colour into the
// rest of the pane.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}
