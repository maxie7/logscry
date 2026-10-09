// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// TestFirstSeenSuffix pins the prompt's "first seen N ago" (issue #72). The age is the
// spread of SOURCE timestamps logscry has read for the template: the trigger's own time
// minus the earliest line time. Both sides are on the source's clock, so a backlog
// delivered in milliseconds still reports the hour its source spread it over.
func TestFirstSeenSuffix(t *testing.T) {
	t0 := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		count    int
		trigger  time.Time
		earliest time.Time
		want     string
	}{
		// "Occurrences: 1" already says it, and the age is 0 by construction.
		{"first occurrence is omitted by rule", 1, t0.Add(time.Hour), t0, ""},
		{"backlog spread over an hour", 6, t0.Add(time.Hour), t0, " (first seen 1h0m0s ago)"},
		{"truncated to the second", 3, t0.Add(90*time.Second + 700*time.Millisecond), t0, " (first seen 1m30s ago)"},
		{"no trigger time", 6, time.Time{}, t0, ""},
		{"no earliest time", 6, t0.Add(time.Hour), time.Time{}, ""},
		// Unreachable from the pipeline, which folds the trigger into the min before the
		// scorer sees it; the guard is defensive, and omits rather than inventing an age.
		{"trigger before earliest", 6, t0, t0.Add(time.Minute), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstSeenSuffix(ExplainRequest{
				Trigger:          model.LogLine{Time: tc.trigger},
				Count:            tc.count,
				EarliestLineTime: tc.earliest,
			})
			if got != tc.want {
				t.Errorf("firstSeenSuffix = %q, want %q", got, tc.want)
			}
		})
	}
}
