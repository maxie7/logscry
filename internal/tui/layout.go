// SPDX-License-Identifier: Apache-2.0

package tui

// The width strategy, kept pure so the whole matrix is testable without a terminal —
// the same split as Decide/Resolve in mode.go.

const (
	// splitMin is the width at which the two-pane layout turns on.
	//
	// Below it, 55/45 of the screen is two columns of ~40 usable characters, and the
	// cards pane wraps prose — an explanation reflowed into 40 columns is a paragraph of
	// confetti. So a narrow terminal gets the stacked layout instead: one full-width
	// stream with the cards pinned above the status bar. Cramped is a bug; stacked is a
	// choice.
	splitMin = 100

	// leftShare is the stream's percentage of the width in the split layout. The stream
	// takes the larger share by percentage, but the cards get the width they need in
	// absolute terms: templated lines are short ("user <NUM> failed login from <IP>"),
	// while a card wraps sentences. At 120 columns — the likely demo width — that is
	// 66 for the stream and 54 for the cards.
	leftShare = 55

	// statusHeight is the number of lines the status bar occupies.
	statusHeight = 2

	// A pane below minPaneHeight rows is not a pane, it is a stripe: two rows of border
	// and nothing between them.
	minPaneHeight = 3

	// The stacked cards pane is a fixed share of the height rather than a fit to its
	// contents — a pane that grows with the first card is a layout that jumps under the
	// user mid-read. Constant for a given terminal size, so nothing moves.
	stackedCardsMin = 4
	stackedCardsMax = 10
)

// layout is which composition the current width can carry.
type layout int

const (
	// layoutSplit is the two-pane money shot: stream left, flagged cards right.
	layoutSplit layout = iota
	// layoutStacked is the narrow fallback: full-width stream, cards pinned below it.
	layoutStacked
)

// String renders the layout for diagnostics and tests.
func (l layout) String() string {
	if l == layoutStacked {
		return "stacked"
	}
	return "split"
}

// layoutFor picks the composition for a width.
func layoutFor(width int) layout {
	if width >= splitMin {
		return layoutSplit
	}
	return layoutStacked
}

// box is the OUTER size of a pane, borders included.
type box struct{ w, h int }

// inner is the space left for content once the border and the pane's title line are
// taken out. It never goes negative, so a terminal too small to draw in renders empty
// panes rather than panicking.
func (b box) inner() (w, h int) {
	return max(b.w-2, 0), max(b.h-3, 0) // 2 border columns; 2 border rows + 1 title row
}

// geometry is the frame: where the two panes are and how big, for one terminal size.
type geometry struct {
	layout layout
	stream box
	cards  box
}

// geometryFor lays out a terminal of this size. The status bar always gets its rows
// first — it is the one piece of chrome that must never be squeezed out — and the panes
// divide what is left.
func geometryFor(width, height int) geometry {
	body := max(height-statusHeight, 0)

	if layoutFor(width) == layoutSplit {
		left := width * leftShare / 100
		return geometry{
			layout: layoutSplit,
			stream: box{w: left, h: body},
			cards:  box{w: width - left, h: body},
		}
	}

	cards := stackedCardsHeight(body)
	return geometry{
		layout: layoutStacked,
		stream: box{w: width, h: body - cards},
		cards:  box{w: width, h: cards},
	}
}

// stackedCardsHeight is the fixed height of the pinned cards pane in the stacked layout:
// about a third of the body, bounded, and never so much that the stream stops being a
// stream.
//
// On a terminal too short to carry both, the cards pane goes entirely rather than being
// drawn as a stripe. Two rows of border with nothing between them communicates nothing;
// the stream, which still works at three rows, gets the space.
func stackedCardsHeight(body int) int {
	h := min(min(max(body/3, stackedCardsMin), stackedCardsMax), body-minPaneHeight)
	if h < minPaneHeight {
		return 0
	}
	return h
}
