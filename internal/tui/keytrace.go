// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A key that "does nothing" is two different bugs wearing the same coat:
//
//   - the program never received the key at all — it was eaten below us, by the terminal,
//     the input decoder, or something else holding the keyboard; or
//   - the program received it and did not act on it.
//
// From outside the process the two are indistinguishable, which is why dead-key reports
// are so hard to chase: every theory fits, and none of them can be falsified. This writes
// down what the event loop ACTUALLY received, so the next report starts from evidence.
//
// It is off unless LOGSCRY_KEYLOG names a file, costs a nil check per keystroke when off,
// and never writes to stdout — a stray write there would corrupt the alternate screen.
//
//	LOGSCRY_KEYLOG=/tmp/keys.log ./bin/logscry --docker-name logtest
//	tail -f /tmp/keys.log
//
// If a key is missing from the log entirely, the program never saw it and the fault is
// below the TUI. If it is in the log and nothing happened, the fault is in handleKey.
var keyTrace io.Writer = openKeyTrace()

func openKeyTrace() io.Writer {
	path := os.Getenv("LOGSCRY_KEYLOG")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil // diagnostics must never take the program down with them
	}
	return f
}

// traceKey records one keystroke, with the state it arrived in — because the report to
// beat is "it stops working after tab", and that is a claim about state.
func traceKey(msg tea.KeyMsg, f focus, m viewMode) {
	if keyTrace == nil {
		return
	}
	pane := "stream"
	if f == focusCards {
		pane = "cards"
	}
	view := "STREAM"
	if m == aggregatedView {
		view = "AGGREGATED"
	}
	// A diagnostic that cannot write is still not allowed to take the program down, or to
	// say anything about it on stdout: there is an alternate screen there.
	_, _ = fmt.Fprintf(keyTrace, "%s key=%-10q type=%-3d runes=%q alt=%v focus=%s view=%s\n",
		time.Now().Format("15:04:05.000"), msg.String(), int(msg.Type), string(msg.Runes), msg.Alt, pane, view)
}
