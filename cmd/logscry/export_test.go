// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/config"
	"github.com/maxie7/logscry/internal/export"
	"github.com/maxie7/logscry/internal/llm"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// exportPath is a path in a fresh temp dir. The dir is empty, so a test can also assert
// that NOTHING was created there.
func exportPath(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	return dir, filepath.Join(dir, "anomalies.jsonl")
}

// readRecords closes the writer — which drains every queued record — and decodes the file.
func readRecords(t *testing.T, w *export.Writer, path string) []map[string]any {
	t.Helper()
	if err := w.Close(); err != nil {
		t.Fatalf("export close: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("exported line does not parse standalone: %v\n%s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// TestPlainExportsOneLinePerStreamedAnomaly is the --plain half of the streaming guard, and
// the one that matters most in practice: --plain is the CI mode, so this is the wiring a
// pipeline actually runs. A streamed answer arrives as several progressive updates, and the
// file must gain exactly one line — not one per partial.
func TestPlainExportsOneLinePerStreamedAnomaly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, path := exportPath(t)
	exp, err := export.Open(path)
	if err != nil {
		t.Fatalf("export.Open: %v", err)
	}

	lines := make(chan model.LogLine, 4)
	errs := make(chan error)
	escalations := make(chan score.EscalationRequest, 4)
	explanations := make(chan model.Explanation, 4)

	cfg := score.Defaults()
	cfg.Warmup, cfg.WarmupLines = 0, 0
	sc := score.New(cfg, escalations)

	captureStdout(t, func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = runPlain(ctx, lines, errs, sc, escalations, explanations, exp, false)
		}()

		lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Level: "PANIC",
			Raw: "PANIC: nil map write in handler 42"}

		select {
		case req := <-escalations:
			explanations <- model.Explanation{Hash: req.Hash, Pattern: req.Pattern,
				State: model.ExplainPending, Summary: "A handler wrote to a nil map."}
			explanations <- model.Explanation{Hash: req.Hash, Pattern: req.Pattern,
				State: model.ExplainPending, Summary: "A handler wrote to a nil map.",
				LikelyCause: "The cache map is never initialised."}
			explanations <- model.Explanation{Hash: req.Hash, Pattern: req.Pattern,
				State:   model.ExplainDone,
				Summary: "A handler wrote to a nil map.", LikelyCause: "The cache map is never initialised.",
				Suggestion: "Make the map in NewServer.", At: time.Now()}
		case <-time.After(5 * time.Second):
			t.Error("the line never escalated")
		}

		close(lines)
		close(explanations)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runPlain did not return")
		}
	})

	recs := readRecords(t, exp, path)
	if len(recs) != 1 {
		t.Fatalf("exported %d records for one anomaly, want 1 — partials must not be written", len(recs))
	}
	rec := recs[0]

	// The full schema, as the README documents it: a consumer indexes these without checking.
	for _, key := range []string{"kind", "template_hash", "pattern", "level", "source",
		"count_at_flag", "first_seen", "last_seen_at_flag", "score", "reasons", "explanation"} {
		if _, ok := rec[key]; !ok {
			t.Errorf("the exported record has no %q key", key)
		}
	}
	if rec["level"] != "PANIC" || rec["source"] != "proc:app" {
		t.Errorf("level/source = %v/%v, want PANIC/proc:app", rec["level"], rec["source"])
	}
	ex := rec["explanation"].(map[string]any)
	if ex["state"] != "explained" || ex["suggestion"] != "Make the map in NewServer." {
		t.Errorf("the record is not the terminal answer: %#v", ex)
	}
}

// TestPlainWithoutExportCreatesNoFile is the default-off guarantee where a user would notice
// it: no --export means no writer, and nothing must appear on disk.
func TestPlainWithoutExportCreatesNoFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir, _ := exportPath(t)
	lines := make(chan model.LogLine, 4)
	errs := make(chan error)
	escalations := make(chan score.EscalationRequest, 4)
	explanations := make(chan model.Explanation, 4)

	cfg := score.Defaults()
	cfg.Warmup, cfg.WarmupLines = 0, 0
	sc := score.New(cfg, escalations)

	captureStdout(t, func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = runPlain(ctx, lines, errs, sc, escalations, explanations, nil, false)
		}()

		lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Level: "PANIC",
			Raw: "PANIC: nil map write in handler 42"}
		req := <-escalations
		explanations <- model.Explanation{Hash: req.Hash, Pattern: req.Pattern, State: model.ExplainDone,
			Summary: "A handler wrote to a nil map."}

		close(lines)
		close(explanations)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runPlain did not return")
		}
	})

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a run without --export created %d file(s): %v", len(entries), entries)
	}
}

// TestExportKeepsRealValuesUnderAnonymization is §4 of the feature, end to end through the
// real seam: an httptest provider, the anonymizing decorator, the worker pool, --plain, and
// the file.
//
// --llm-anonymize masks values on the way OUT, to a remote model. It is not a redaction of
// logscry's own output: the terminal keeps the real values and so does this file, which is
// just as local. The test asserts both halves, because either alone can pass for the wrong
// reason — the placeholder really did go over the wire, AND the real address came back into
// the record.
func TestExportKeepsRealValuesUnderAnonymization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wire atomic.Pointer[string]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s := string(raw)
		wire.Store(&s)
		w.Header().Set("Content-Type", "application/json")
		// The model answers in the placeholders it was given, exactly as a real one does.
		answer := `{"summary":"The API cannot reach <IP_1>.","likely_cause":"<IP_1> is refusing connections.","suggestion":"Check connectivity to <IP_1>."}`
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, answer)
	}))
	defer srv.Close()

	llmCfg := llm.Defaults()
	llmCfg.BaseURL = srv.URL + "/v1"
	llmCfg.APIKey = "sk-test"
	llmCfg.HTTPClient = srv.Client()
	llmCfg.Timeout = 5 * time.Second
	llmCfg.Anonymize = true

	_, path := exportPath(t)
	exp, err := export.Open(path)
	if err != nil {
		t.Fatalf("export.Open: %v", err)
	}

	escalations := make(chan score.EscalationRequest, llmCfg.Queue)
	explanations := make(chan model.Explanation, llmCfg.Queue+llmCfg.Workers)
	backend := llm.NewAnonymizing(llm.NewOpenAICompatible(llmCfg))
	go llm.Run(ctx, backend, llmCfg, escalations, explanations)

	scoreCfg := score.Defaults()
	scoreCfg.Warmup, scoreCfg.WarmupLines = 0, 0
	sc := score.New(scoreCfg, escalations)

	lines := make(chan model.LogLine, 4)
	errs := make(chan error)

	captureStdout(t, func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = runPlain(ctx, lines, errs, sc, escalations, explanations, exp, false)
		}()

		lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Level: "ERROR",
			Raw: "ERROR: connection refused to 10.0.0.5:5432"}
		close(lines) // ends ingestion, which closes escalations, which drains the pool

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("runPlain did not return")
		}
	})

	sent := wire.Load()
	if sent == nil {
		t.Fatal("the provider was never called: the escalation did not reach the model")
	}
	if strings.Contains(*sent, "10.0.0.5") {
		t.Errorf("the real address went over the wire — masking was not actually on:\n%s", *sent)
	}

	recs := readRecords(t, exp, path)
	if len(recs) != 1 {
		t.Fatalf("exported %d records, want 1", len(recs))
	}
	line, err := json.Marshal(recs[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(line), "10.0.0.5") {
		t.Errorf("the exported record lost the real address:\n%s", line)
	}
	if strings.Contains(string(line), "<IP_") {
		t.Errorf("an anonymization placeholder leaked into the export file:\n%s", line)
	}
}

// TestExportConfigSurface: off by default, settable from the flag, settable from the file.
func TestExportConfigSurface(t *testing.T) {
	if config.Defaults().Export != "" {
		t.Error("export is on by default")
	}

	cfg, err := config.Load([]string{"--export", "/tmp/anomalies.jsonl"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Export != "/tmp/anomalies.jsonl" {
		t.Errorf("Export = %q, want the flag's path", cfg.Export)
	}

	path := filepath.Join(t.TempDir(), "logscry.yaml")
	if err := os.WriteFile(path, []byte("export: from-file.jsonl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load([]string{"--config", path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Export != "from-file.jsonl" {
		t.Errorf("Export = %q, want the file's path", cfg.Export)
	}
}

// TestOpenExportFailsFast: a mistyped path is a startup error, not something discovered at
// the end of a long run when the anomalies were supposed to have been recorded.
func TestOpenExportFailsFast(t *testing.T) {
	cfg := config.Defaults()
	cfg.Export = filepath.Join(t.TempDir(), "no-such-dir", "anomalies.jsonl")

	if _, err := openExport(cfg); err == nil {
		t.Error("openExport() = nil error for an unwritable path")
	}

	cfg.Export = ""
	w, err := openExport(cfg)
	if err != nil || w != nil {
		t.Errorf("openExport() = %v, %v with no --export; want nil, nil", w, err)
	}
}
