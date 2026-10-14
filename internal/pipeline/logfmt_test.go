// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// TestIssue83LogfmtLines is #83's report, line for line: two dockerd-style info lines and
// two different errors, in the logfmt shape logrus and slog's text handler write. On the
// text path the quoted msg was masked whole to <STR>, so each level was ONE template, and
// "level=" was read only at the start of a line, so a line that begins with "time=" got no
// level — a new error from such a service could not escalate on its own.
func TestIssue83LogfmtLines(t *testing.T) {
	const (
		info1 = `time="2026-07-13T12:00:00.101+02:00" level=info msg="Starting up"`
		info2 = `time="2026-07-13T12:00:01.202+02:00" level=info msg="Loading containers: done."`
		err1  = `time="2026-07-13T12:00:02.303+02:00" level=error msg="failed to start container abc: no space left on device"`
		err2  = `time="2026-07-13T12:00:03.404+02:00" level=error msg="TLS handshake error from 10.0.0.5: EOF"`
	)
	p := New(nil)
	base := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	var evs []Event
	for i, raw := range []string{info1, info2, err1, err2} {
		evs = append(evs, p.Process(model.LogLine{Raw: raw}, base.Add(time.Duration(i)*time.Second)))
	}

	if evs[0].Hash == evs[1].Hash {
		t.Errorf("two different info messages share one template: %q", evs[0].Pattern)
	}
	if evs[2].Hash == evs[3].Hash {
		t.Errorf("two different errors share one template: %q", evs[2].Pattern)
	}
	for i, want := range []string{"INFO", "INFO", "ERROR", "ERROR"} {
		if got := evs[i].Line.Level; got != want {
			t.Errorf("line %d: Level = %q, want %q (pattern %q)", i+1, got, want, evs[i].Pattern)
		}
	}
	for i, ev := range evs {
		if ev.Count != 1 {
			t.Errorf("line %d: x%d, want x1 — four messages are four templates (pattern %q)", i+1, ev.Count, ev.Pattern)
		}
	}
}
