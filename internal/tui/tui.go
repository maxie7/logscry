// SPDX-License-Identifier: Apache-2.0

// Package tui renders the live stream and flagged-event cards using Bubble Tea.
package tui

import "context"

// Run starts the terminal UI and blocks until the user quits or ctx is cancelled.
//
// TODO(M5): Bubble Tea program with a live-stream pane (left) and a scrollable
// list of flagged-event cards (right); keybindings for scroll/expand/pause/quit.
func Run(ctx context.Context) error {
	_ = ctx
	return nil
}
