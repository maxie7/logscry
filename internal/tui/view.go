// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// Styles are package-level: lipgloss resolves the terminal's colour profile once,
// and re-creating them per frame would be wasted work at 10 fps.
var (
	styleSource = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // cyan
	styleCount  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")) // dim grey
	styleStatus = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("8"))
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("9")) // red
	styleHint   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")) // dim grey
	styleHead   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)

	// styleEscalate marks a would-be escalation in --explain-dry-run. It has to jump
	// off a wall of log lines — that is the whole point of the mode.
	styleEscalate = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true) // magenta

	// levelStyles colours the severity token; unknown levels fall back to plain.
	levelStyles = map[string]lipgloss.Style{
		"ERROR":    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		"FATAL":    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		"PANIC":    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		"CRITICAL": lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		"WARN":     lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		"WARNING":  lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		"INFO":     lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		"DEBUG":    lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		"TRACE":    lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	}
)

// View composes the panes vertically. Each pane is its own renderer, so M5 can add
// the flagged-cards pane beside the stream with a JoinHorizontal here, without
// restructuring anything below.
func (m Model) View() string {
	if !m.ready {
		return "starting logscry…"
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.vp.View(), m.viewStatus())
}

// viewStream renders the live tail: one templated line per event, newest at the
// bottom. It shows the masked pattern rather than the raw text — that is the point
// of M2 — while Event.Line.Raw stays available for a future raw toggle.
func (m Model) viewStream() string {
	if len(m.snap.Lines) == 0 {
		return styleHint.Render("waiting for logs…")
	}
	var b strings.Builder
	for i, ev := range m.snap.Lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(styleSource.Render("[" + ev.Line.Source + " "))
		b.WriteString(renderLevel(ev.Line.Level))
		b.WriteString(styleSource.Render("]"))
		b.WriteString(" " + ev.Pattern)
		if ev.Count > 1 {
			b.WriteString(styleCount.Render(fmt.Sprintf("  x%d", ev.Count)))
		}
		// In dry-run the escalation is the news, so it gets its own line under the
		// event that caused it, carrying the reasons the scorer fired.
		if m.opts.ExplainDryRun && ev.Escalate {
			b.WriteByte('\n')
			b.WriteString(styleEscalate.Render(fmt.Sprintf("  ⇧ WOULD ESCALATE: %s | reasons: %s | score: %.2f",
				ev.Pattern, strings.Join(ev.Reasons, ", "), ev.Score)))
		}
	}
	return b.String()
}

// viewAggregated renders the template table, already sorted by the pipeline
// (count desc, then last seen). This is where thousands of lines visibly collapse
// into a handful of templates.
func (m Model) viewAggregated() string {
	if len(m.snap.Templates) == 0 {
		return styleHint.Render("no templates yet…")
	}

	// The pattern column takes whatever the fixed columns leave, so a narrow
	// terminal truncates the message rather than wrapping the table.
	const fixed = 7 + 1 + 8 + 1 + 12 + 1 // count + level + last-seen + gaps
	patternWidth := max(m.width-fixed, 10)

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

// viewStatus renders the two fixed lines of chrome: the run summary, and below it
// the most recent background error — errors surface here rather than on stdout,
// which would corrupt the alternate screen.
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

	sources := "no sources"
	if len(m.snap.Stats.Sources) > 0 {
		sources = strings.Join(m.snap.Stats.Sources, ", ")
	}

	// The source list is the elastic segment: on a narrow terminal it gives way
	// first, so the counters and the mode (PAUSED, ENDED) never truncate away. Once
	// there is no room for it at all, it goes entirely — separators and all — rather
	// than leaving a stub of punctuation behind.
	const seps = 6 // the two " | " joins
	summary := counters + " | " + mode
	if budget := m.width - runeLen(counters) - runeLen(mode) - seps; budget > 0 {
		summary = counters + " | " + truncate(sources, budget) + " | " + mode
	}
	bar := styleStatus.Width(m.width).Render(truncate(summary, m.width))

	second := styleHint.Render(truncate(" q quit · t toggle view · p pause · ↑/↓/pgup/pgdn scroll", m.width))
	if err := m.lastError(); err != "" {
		second = styleErr.Render(truncate(" ! "+err, m.width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, bar, second)
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

// runeLen is the displayed length of s in runes.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// truncate cuts s to at most width runes, ellipsizing when it does. It counts
// runes, not bytes, so multi-byte log content does not overflow the line.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}
