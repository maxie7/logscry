// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package tui

import "io"

// setBackground leaves lipgloss as it is: termenv asks a Windows console nothing, so lipgloss's
// own answer cost no wait (see the unix version).
func setBackground(io.Writer) {}
