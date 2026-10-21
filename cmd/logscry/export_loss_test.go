// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/config"
	"github.com/maxie7/logscry/internal/export"
)

// stalledDisk is an export file whose writes wait for gate: a hung mount, a disk that blocks.
// The live writer's queue fills behind it, which is how records get lost.
type stalledDisk struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	gate chan struct{}
	once sync.Once
}

func newStalledDisk() *stalledDisk { return &stalledDisk{gate: make(chan struct{})} }

func (d *stalledDisk) release() { d.once.Do(func() { close(d.gate) }) }

func (d *stalledDisk) Write(p []byte) (int, error) {
	<-d.gate
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.Write(p)
}

func (d *stalledDisk) Truncate(size int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf.Truncate(int(size))
	return nil
}

func (d *stalledDisk) Sync() error  { return nil }
func (d *stalledDisk) Close() error { return nil }

func (d *stalledDisk) contents() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.String()
}

// onDisk makes the live export writer of the next run write to d.
func onDisk(t *testing.T, d *stalledDisk) {
	t.Helper()
	saved := openLiveExport
	openLiveExport = func(string) (*export.Writer, error) { return export.NewWriter(d, 0), nil }
	t.Cleanup(func() {
		openLiveExport = saved
		d.release()
	})
}

// fatalLines writes n lines that each escalate in a dry run: a FATAL, and a template of its
// own (the varying word holds no digits or hex letters, so it is not masked away).
func fatalLines(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	for i := range n {
		var w [4]byte
		for k, v := 0, i; k < 4; k, v = k+1, v/20 {
			w[k] = "ghijklmnopqrstuvwxyz"[v%20]
		}
		fmt.Fprintf(&b, "FATAL: worker %s crashed\n", w[:])
	}
	path := filepath.Join(t.TempDir(), "lines.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// lossArgs runs a dry run over lines with nothing holding escalations back.
func lossArgs(t *testing.T, lines string) []string {
	return []string{"--plain", "--explain-dry-run", "--warmup", "0", "--warmup-lines", "0",
		"--rate-limit", "1000000", "--export", filepath.Join(t.TempDir(), "anomalies.jsonl"),
		"--", "cat", lines}
}

const liveLossLine = "logscry: export: records are being lost"

var lostRe = regexp.MustCompile(`export: (\d+) record\(s\) lost`)

// TestPlainExportLossEndToEnd is #70 as a CI job sees it: a --plain run whose export file
// stalls. The loss is announced while the run is live, once; the run ends with the count of
// lost RECORDS and a non-zero exit status; the file ends with a record saying how many it is
// missing; and what it holds plus what it lost is every escalation.
func TestPlainExportLossEndToEnd(t *testing.T) {
	disk := newStalledDisk()
	onDisk(t, disk)
	const n = 2000
	stderr := captureStderr(t)

	var err error
	stdout := captureStdout(t, func() {
		done := runInBackground(context.Background(), lossArgs(t, fatalLines(t, n))...)
		stderr.waitFor(liveLossLine, 5*time.Second) // the disk stays stalled until it shows
		disk.release()
		select {
		case err = <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("run did not return once the disk moved")
		}
	})
	got := stderr.final()

	if c := strings.Count(got, liveLossLine); c != 1 {
		t.Errorf("the live loss line appears %d times, want once:\n%s", c, got)
	}
	if err == nil {
		t.Fatalf("run returned nil (exit status 0) with records lost; stderr:\n%s", got)
	}
	m := lostRe.FindStringSubmatch(err.Error())
	if m == nil {
		t.Fatalf("run = %q, want the exit-time line counting record(s) lost", err)
	}
	lost, _ := strconv.Atoi(m[1])

	escalations := strings.Count(stdout, "WOULD ESCALATE")
	if escalations != n {
		t.Fatalf("%d escalations, want all %d lines to escalate", escalations, n)
	}
	lines := strings.Split(strings.TrimSuffix(disk.contents(), "\n"), "\n")
	records := 0
	for i, line := range lines {
		var rec map[string]any
		if e := json.Unmarshal([]byte(line), &rec); e != nil {
			t.Fatalf("line %d is not whole JSON: %v\n%s", i+1, e, line)
		}
		if i == len(lines)-1 {
			if rec["kind"] != "export_incomplete" || rec["records_lost"] != float64(lost) {
				t.Errorf("the last line is %v, want export_incomplete with records_lost %d", rec, lost)
			}
			continue
		}
		if rec["kind"] != "would_escalate" {
			t.Fatalf("line %d is %v, want a record", i+1, rec)
		}
		records++
	}
	if records+lost != escalations {
		t.Errorf("%d records in the file + %d lost = %d, want the %d escalations", records, lost, records+lost, escalations)
	}
}

// TestPlainNoExportLoss: a run whose export keeps up announces nothing, exits 0, and writes
// its records and nothing else.
func TestPlainNoExportLoss(t *testing.T) {
	disk := newStalledDisk()
	disk.release() // a disk that never stalls
	onDisk(t, disk)
	const n = 20
	stderr := captureStderr(t)
	captureStdout(t, func() {
		if err := run(context.Background(), lossArgs(t, fatalLines(t, n))); err != nil {
			t.Errorf("run = %v, want nil", err)
		}
	})
	if got := stderr.final(); strings.Contains(got, "export") {
		t.Errorf("stderr mentions the export:\n%s", got)
	}
	if got := strings.Count(disk.contents(), "\n"); got != n {
		t.Errorf("the file holds %d lines, want the %d records", got, n)
	}
	if strings.Contains(disk.contents(), "export_incomplete") {
		t.Error("a complete file carries the export_incomplete record")
	}
}

// TestTUIOptionsCarryExportLoss: the TUI is handed the export's running count of lost records,
// and nothing when there is no export.
func TestTUIOptionsCarryExportLoss(t *testing.T) {
	cfg := config.Defaults()
	if o := tuiOptions(cfg, false, nil); o.ExportLost != nil {
		t.Error("ExportLost is set with no export")
	}
	disk := newStalledDisk()
	exp := export.NewWriter(disk, 0)
	defer func() {
		disk.release()
		_ = exp.Close()
	}()
	at := time.Date(2026, 8, 29, 10, 40, 0, 0, time.UTC)
	for i := range 2000 {
		h := fmt.Sprintf("h%04d", i)
		exp.Flag(export.Flag{Hash: h})
		exp.WouldEscalate(h, 0, at)
	}
	o := tuiOptions(cfg, false, exp)
	if o.ExportLost == nil {
		t.Fatal("ExportLost is nil with an export")
	}
	if got, want := o.ExportLost(), exp.Dropped(); got != want || got == 0 {
		t.Errorf("ExportLost() = %d, want the writer's %d", got, want)
	}
}
