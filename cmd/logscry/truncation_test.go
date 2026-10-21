// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestPlainReportsTruncationOnce: a line cut at the cap is said once on stderr while the run
// is live, however many lines are cut, and the total is printed at exit. The run still exits
// 0: every line was read, and the cut one was processed with its head.
func TestPlainReportsTruncationOnce(t *testing.T) {
	stderr := captureStderr(t)
	const n = 3 << 16 // three times the cap
	script := fmt.Sprintf(`head -c %d /dev/zero | tr '\000' a; echo; `+
		`head -c %d /dev/zero | tr '\000' b; echo; echo next`, n, n)
	// stdout carries the lines themselves, 64 KiB each: kept out of the test's output.
	stdout := captureStdout(t, func() {
		select {
		case err := <-runInBackground(context.Background(), "--plain", "--explain-dry-run", "--", "sh", "-c", script):
			if err != nil {
				t.Errorf("run = %v, want nil (exit status 0)", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("run did not return once its only source had ended")
		}
	})
	if !strings.Contains(stdout, "next") {
		t.Errorf("the line after the cut ones was not printed")
	}
	got := stderr.final()
	if c := strings.Count(got, "was cut to at most its first 64 KiB"); c != 1 {
		t.Errorf("stderr has the truncation notice %d times, want exactly 1:\n%s", c, got)
	}
	if c := strings.Count(got, "logscry: ingest: 2 line(s) over 64 KiB were truncated"); c != 1 {
		t.Errorf("stderr has the exit total %d times, want exactly 1:\n%s", c, got)
	}
}
