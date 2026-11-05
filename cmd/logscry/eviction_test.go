// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/pipeline"
)

// TestPlainReportsEvictionOnce: past the bound, eviction is said once on stderr while the run is
// live, however many follow, and the total is printed at exit. The run still exits 0: every line
// was scored and shown, and what was lost is history, not a record (#126).
func TestPlainReportsEvictionOnce(t *testing.T) {
	stderr := captureStderr(t)
	const over = 10_000
	// Letters only, so no two lines share a masked template.
	script := fmt.Sprintf(`awk 'BEGIN { for (i = 0; i < %d; i++) { s = "noise "; n = i; `+
		`for (j = 0; j < 6; j++) { s = s substr("ghijklmnopqrstuvwxyz", n %% 20 + 1, 1); n = int(n / 20) } `+
		`print s } }'`, pipeline.MaxTemplates+over)
	_ = captureStdout(t, func() {
		select {
		case err := <-runInBackground(context.Background(), "--plain", "--explain-dry-run", "--", "sh", "-c", script):
			if err != nil {
				t.Errorf("run = %v, want nil (exit status 0)", err)
			}
		case <-time.After(60 * time.Second):
			t.Error("run did not return once its only source had ended")
		}
	})
	got := stderr.final()
	if c := strings.Count(got, "the least recently seen are being evicted"); c != 1 {
		t.Errorf("stderr has the eviction notice %d times, want exactly 1:\n%s", c, got)
	}
	want := fmt.Sprintf("logscry: pipeline: %d template(s) evicted", over)
	if c := strings.Count(got, want); c != 1 {
		t.Errorf("stderr has %q %d times, want exactly 1:\n%s", want, c, got)
	}
}

// TestSourceEvictionTotal: nothing at zero, and at any other count evictions rather than names,
// because a name that returns counts again (#163).
func TestSourceEvictionTotal(t *testing.T) {
	if got := sourceEvictionTotal(0); got != "" {
		t.Errorf("sourceEvictionTotal(0) = %q, want nothing", got)
	}
	got := sourceEvictionTotal(7)
	for _, want := range []string{"7 source-name eviction(s)", fmt.Sprint(pipeline.MaxSources), "256 KiB"} {
		if !strings.Contains(got, want) {
			t.Errorf("exit line %q lacks %q", got, want)
		}
	}
}

func TestSourcesEvictingNoticeNamesItsBound(t *testing.T) {
	got := errSourcesEvicting.Error()
	for _, want := range []string{fmt.Sprint(pipeline.MaxSources), "256 KiB", "every line is still processed"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice %q lacks %q", got, want)
		}
	}
}
