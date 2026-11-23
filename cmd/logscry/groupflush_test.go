// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/pipeline"
)

// TestReplayReportsEarlyFlushOnce: a capture of distinct units on one timestamp never goes idle,
// so past the coalescer's bound each new unit flushes one buffer early. That is said once while
// the run is live, totalled at exit, and the run still exits 0: every line was emitted (#166).
//
// 4,000 over the bound, not fewer: the notice is posted only while the sources run, and ingestion
// can be up to a channel's 1,024 lines ahead of the coalescer, so the first early flush must come
// well before the capture's last line.
func TestReplayReportsEarlyFlushOnce(t *testing.T) {
	const over = 4000
	var b strings.Builder
	for i := range pipeline.MaxGroupBuffers + over {
		fmt.Fprintf(&b, `{"MESSAGE":"order %d placed","PRIORITY":"6","_SYSTEMD_UNIT":"unit-%d.service","__REALTIME_TIMESTAMP":"1788000000000000"}`+"\n", i, i)
	}
	capture := filepath.Join(t.TempDir(), "capture.jsonl")
	if err := os.WriteFile(capture, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	stderr := captureStderr(t)
	_ = captureStdout(t, func() {
		select {
		case err := <-runInBackground(context.Background(), "--replay", capture, "--explain-dry-run", "--plain"):
			if err != nil {
				t.Errorf("run = %v, want nil (exit status 0)", err)
			}
		case <-time.After(60 * time.Second):
			t.Error("the replay did not return")
		}
	})
	got := stderr.final()
	if c := strings.Count(got, "the least recently touched are being flushed early"); c != 1 {
		t.Errorf("stderr has the early-flush notice %d times, want exactly 1:\n%s", c, got)
	}
	want := fmt.Sprintf("logscry: pipeline: %d grouping buffer(s) flushed early", over)
	if c := strings.Count(got, want); c != 1 {
		t.Errorf("stderr has %q %d times, want exactly 1:\n%s", want, c, got)
	}
}

// TestEarlyFlushTotal: nothing at zero, and the count and the bound at any other.
func TestEarlyFlushTotal(t *testing.T) {
	if got := earlyFlushTotal(0); got != "" {
		t.Errorf("earlyFlushTotal(0) = %q, want nothing", got)
	}
	got := earlyFlushTotal(7)
	for _, want := range []string{"7 grouping buffer(s) flushed early", fmt.Sprint(pipeline.MaxGroupBuffers), "16 MiB"} {
		if !strings.Contains(got, want) {
			t.Errorf("exit line %q lacks %q", got, want)
		}
	}
}

func TestFlushingEarlyNoticeNamesItsBound(t *testing.T) {
	got := errFlushingEarly.Error()
	for _, want := range []string{fmt.Sprint(pipeline.MaxGroupBuffers), "16 MiB", "split in two", "every line is still processed"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice %q lacks %q", got, want)
		}
	}
}
