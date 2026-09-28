// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maxie7/logscry/internal/config"
)

// replayFixture is the committed synthetic capture: ten virtual minutes, four faults.
var replayFixture = filepath.Join("..", "..", "internal", "ingest", "testdata", "replay", "basic.jsonl")

// replayRun runs the whole binary's entry point over the fixture in --plain, discarding
// stdout, and returns the export file's bytes.
func replayRun(t *testing.T, extra ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anomalies.jsonl")
	args := append([]string{"--replay", replayFixture, "--explain-dry-run", "--plain", "--export", path}, extra...)
	captureStdout(t, func() {
		if err := run(context.Background(), args); err != nil {
			t.Errorf("run: %v", err)
		}
	})
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestReplayWithoutDryRunBuildsNoBackend: replay runs the scorer, not a model. Asserted as
// "no request reached the model endpoint", not merely as an exit status — the property is
// that nothing CAN call a model, and an error returned after a pool had started would pass
// an exit-code check.
func TestReplayWithoutDryRunBuildsNoBackend(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := run(context.Background(), []string{"--replay", replayFixture, "--plain", "--llm-url", srv.URL + "/v1"})
	if err == nil || !strings.Contains(err.Error(), "--replay requires --explain-dry-run") {
		t.Fatalf("err = %v, want the dry-run refusal", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the model endpoint received %d request(s) from a refused replay", n)
	}
}

// TestReplayResolvesToNoLLMStage runs the two components back to back: configuration
// resolution, then the code that builds (or does not build) the LLM stage from what it
// resolved. Dry-run set in a config FILE satisfies --replay, and the stage built from that
// same resolved value is empty. The mechanism also refuses on its own: handed a replay
// config that somehow skipped validation, it still builds nothing.
func TestReplayResolvesToNoLLMStage(t *testing.T) {
	file := filepath.Join(t.TempDir(), "logscry.yaml")
	if err := os.WriteFile(file, []byte("explain_dry_run: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load([]string{"--config", file, "--replay", replayFixture})
	if err != nil {
		t.Fatalf("resolution refused a replay with dry-run in the file: %v", err)
	}
	if esc, ex := startLLM(context.Background(), cfg); esc != nil || ex != nil {
		t.Fatal("the resolved replay config built an LLM stage")
	}

	bypassed := config.Defaults()
	bypassed.Replay = replayFixture // ExplainDryRun false: validation was skipped
	if esc, ex := startLLM(context.Background(), bypassed); esc != nil || ex != nil {
		t.Fatal("a replay config built an LLM stage without dry-run")
	}
}

// TestReplayExportIsByteIdentical is the instrument's defining property: the export is the
// artifact that gets diffed between two threshold settings, so two replays of one capture
// under one setting must write the same bytes. Three things broke this before replay
// existed, each measured: the wall clock read per line, the export writer dropping under
// load (548/561/574 records across three runs of one input), and the coalescer flushing
// several streams in map order.
func TestReplayExportIsByteIdentical(t *testing.T) {
	first := replayRun(t)
	for i := range 4 {
		if again := replayRun(t); !bytes.Equal(first, again) {
			t.Fatalf("replay %d wrote a different export:\n--- first\n%s\n--- again\n%s", i+2, first, again)
		}
	}
}

// TestReplaySpeedLeavesTheExportUnchanged: speed is pacing, never an input.
func TestReplaySpeedLeavesTheExportUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("paces ten virtual minutes at 1000×")
	}
	if max, paced := replayRun(t), replayRun(t, "--replay-speed", "1000x"); !bytes.Equal(max, paced) {
		t.Fatalf("1000× wrote a different export than max:\n--- max\n%s\n--- 1000×\n%s", max, paced)
	}
}

// TestReplayFlagsTheFixtureFaults: the four faults the fixture was built around, and nothing
// else, stamped with the CAPTURE's time. A first_seen on today's date would be the wall clock
// leaking into a replay.
func TestReplayFlagsTheFixtureFaults(t *testing.T) {
	var recs []map[string]any
	for line := range strings.Lines(string(replayRun(t))) {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("export line does not parse: %v\n%s", err, line)
		}
		recs = append(recs, rec)
	}
	want := []struct{ pattern, reason string }{
		{"disk quota exceeded", "novel template (first seen)"},
		{"shutting down worker pool", "level ERROR"}, // PRIORITY 3 beats the INFO: prefix (#24)
		{"db retry attempt", "burst:"},
		{"out of memory", "level FATAL"},
	}
	if len(recs) != len(want) {
		t.Fatalf("export holds %d records, want %d:\n%v", len(recs), len(want), recs)
	}
	for i, w := range want {
		r := recs[i]
		if r["kind"] != "would_escalate" || !strings.Contains(r["pattern"].(string), w.pattern) {
			t.Errorf("record %d = %v %q, want would_escalate %q", i, r["kind"], r["pattern"], w.pattern)
		}
		reasons, _ := json.Marshal(r["reasons"])
		if !strings.Contains(string(reasons), w.reason) {
			t.Errorf("record %d reasons %s, want one starting %q", i, reasons, w.reason)
		}
		if fs, _ := r["first_seen"].(string); !strings.HasPrefix(fs, "2026-08-29") {
			t.Errorf("record %d first_seen %q is not the capture's date: the wall clock leaked in", i, fs)
		}
	}
}
