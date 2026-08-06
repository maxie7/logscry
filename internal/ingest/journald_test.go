// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// sampleEntry is a real `journalctl -o json` line, captured verbatim from a systemd
// host and only reflowed. It is here rather than hand-written because the two fields
// this source depends on most are both easy to assume wrong: PRIORITY is a JSON STRING
// ("6"), not a number, and __REALTIME_TIMESTAMP is microseconds since the epoch, also
// as a string.
const sampleEntry = `{"JOB_RESULT":"done","_SOURCE_REALTIME_TIMESTAMP":"1786050015002801",` +
	`"_MACHINE_ID":"18ac45084a074ffa8a8ff74c0aeeff2b","_SYSTEMD_UNIT":"sysstat-collect.service",` +
	`"_HOSTNAME":"predator-16","__REALTIME_TIMESTAMP":"1786050015002905","CODE_LINE":"796",` +
	`"_UID":"0","SYSLOG_FACILITY":"3","_TRANSPORT":"journal",` +
	`"MESSAGE":"Finished sysstat-collect.service - system activity accounting tool.",` +
	`"UNIT":"sysstat-collect.service","CODE_FILE":"src/core/job.c","_PID":"1",` +
	`"PRIORITY":"6","_COMM":"systemd","SYSLOG_IDENTIFIER":"systemd","_GID":"0",` +
	`"CODE_FUNC":"job_emit_done_message","JOB_ID":"4868","_RUNTIME_SCOPE":"system"}`

// received is a line as readLines hands it to decode: raw JSON, receipt time, source and
// stream from the Source, nothing parsed yet.
func received(raw string) model.LogLine {
	return model.LogLine{
		Time:   time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		Source: "journald",
		Stream: model.Stdout,
		Raw:    raw,
	}
}

func TestJournaldDecodesACapturedEntry(t *testing.T) {
	got := decode(received(sampleEntry))

	if want := "Finished sysstat-collect.service - system activity accounting tool."; got.Raw != want {
		t.Errorf("Raw = %q, want %q", got.Raw, want)
	}
	// PRIORITY "6" is info: a level, and NOT error-class, so the stream is untouched.
	if got.Level != "INFO" {
		t.Errorf("Level = %q, want INFO", got.Level)
	}
	if got.Stream != model.Stdout {
		t.Errorf("Stream = %v, want Stdout", got.Stream)
	}
	// _SYSTEMD_UNIT wins over SYSLOG_IDENTIFIER ("systemd") and _COMM ("systemd"), and
	// ".service" is trimmed.
	if want := "journald:sysstat-collect"; got.Source != want {
		t.Errorf("Source = %q, want %q", got.Source, want)
	}
	if want := time.UnixMicro(1786050015002905); !got.Time.Equal(want) {
		t.Errorf("Time = %v, want %v", got.Time, want)
	}
}

// TestJournaldPriorityMapping pins the whole table, including which priorities are
// error-class enough to be tagged stderr — the mapping is the only thing that carries
// journald severity into scoring.
func TestJournaldPriorityMapping(t *testing.T) {
	tests := []struct {
		priority   string
		wantLevel  string
		wantStream model.Stream
	}{
		{"0", "FATAL", model.Stderr},    // emerg
		{"1", "FATAL", model.Stderr},    // alert
		{"2", "CRITICAL", model.Stderr}, // crit
		{"3", "ERROR", model.Stderr},    // err
		{"4", "WARN", model.Stdout},     // warning
		{"5", "INFO", model.Stdout},     // notice
		{"6", "INFO", model.Stdout},     // info
		{"7", "DEBUG", model.Stdout},    // debug
	}
	for _, tc := range tests {
		t.Run("priority "+tc.priority, func(t *testing.T) {
			raw := `{"MESSAGE":"something happened","PRIORITY":"` + tc.priority + `"}`
			got := decode(received(raw))
			if got.Level != tc.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tc.wantLevel)
			}
			if got.Stream != tc.wantStream {
				t.Errorf("Stream = %v, want %v", got.Stream, tc.wantStream)
			}
			if got.Raw != "something happened" {
				t.Errorf("Raw = %q, want the message", got.Raw)
			}
		})
	}
}

// TestJournaldPriorityIsAlsoAcceptedAsANumber: journalctl emits a string today, but the
// mapping must not hinge on that.
func TestJournaldPriorityIsAlsoAcceptedAsANumber(t *testing.T) {
	got := decode(received(`{"MESSAGE":"boom","PRIORITY":3}`))
	if got.Level != "ERROR" || got.Stream != model.Stderr {
		t.Errorf("Level = %q, Stream = %v; want ERROR on stderr", got.Level, got.Stream)
	}
}

// TestJournaldUnparseablePriorityAssignsNoSeverity: guessing a severity from a value we
// did not understand is worse than having none, since the guess feeds the scorer.
func TestJournaldUnparseablePriorityAssignsNoSeverity(t *testing.T) {
	for _, raw := range []string{
		`{"MESSAGE":"m"}`,               // absent
		`{"MESSAGE":"m","PRIORITY":""}`, // empty
		`{"MESSAGE":"m","PRIORITY":"x"}`,
		`{"MESSAGE":"m","PRIORITY":"8"}`,  // out of range
		`{"MESSAGE":"m","PRIORITY":"-1"}`, // out of range
	} {
		got := decode(received(raw))
		if got.Level != "" {
			t.Errorf("%s: Level = %q, want empty", raw, got.Level)
		}
		if got.Stream != model.Stdout {
			t.Errorf("%s: Stream = %v, want Stdout", raw, got.Stream)
		}
		if got.Raw != "m" {
			t.Errorf("%s: Raw = %q, want the message anyway", raw, got.Raw)
		}
	}
}

// TestJournaldDecodesAByteArrayMessage: journald renders a field whose value is not
// valid UTF-8 as an array of byte values instead of a string. Kernel messages do this,
// and a struct-based decode would fail the whole entry on one such field.
func TestJournaldDecodesAByteArrayMessage(t *testing.T) {
	// "hi\xff" — the trailing byte is not valid UTF-8, which is what triggers the form.
	raw := `{"MESSAGE":[104,105,255],"PRIORITY":"3"}`
	got := decode(received(raw))

	if want := string([]byte{104, 105, 255}); got.Raw != want {
		t.Errorf("Raw = %q, want %q", got.Raw, want)
	}
	if got.Level != "ERROR" {
		t.Errorf("Level = %q, want ERROR", got.Level)
	}
}

// TestJournaldPassesThroughWhatItCannotDecode: losing a line is worse than showing a raw
// one, and the pipeline handles arbitrary text by definition.
func TestJournaldPassesThroughWhatItCannotDecode(t *testing.T) {
	for _, raw := range []string{
		"-- Journal begins at Mon 2026-01-05 09:14:02 CET. --", // not JSON
		"",
		"{not json at all",
		`{"PRIORITY":"3","_SYSTEMD_UNIT":"nginx.service"}`, // JSON, but no MESSAGE
		// A null MESSAGE is an ABSENT one, not an empty one. It unmarshals into both a
		// string and a []byte without error, so treating it as text would emit a blank
		// line in place of the entry.
		`{"MESSAGE":null,"PRIORITY":"3"}`,
	} {
		in := received(raw)
		got := decode(in)
		if got != in {
			t.Errorf("raw %q: line was modified: %+v, want %+v", raw, got, in)
		}
	}
}

func TestJournaldSourceNameIdentifiesTheSource(t *testing.T) {
	if got := NewJournaldSource(nil, 7).Name(); got != "journald" {
		t.Errorf("Name() = %q, want journald", got)
	}
}

// TestJournaldPerLineSourceNaming: the per-line tag mirrors Docker's "docker:<container>"
// and prefers the field a user would type into --journald-unit.
func TestJournaldPerLineSourceNaming(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  string
	}{
		{
			"unit wins over identifier and comm",
			`{"MESSAGE":"m","_SYSTEMD_UNIT":"nginx.service","SYSLOG_IDENTIFIER":"nginx","_COMM":"nginx"}`,
			"journald:nginx",
		},
		{
			"identifier when there is no unit (the kernel)",
			`{"MESSAGE":"m","SYSLOG_IDENTIFIER":"kernel","_COMM":"swapper"}`,
			"journald:kernel",
		},
		{"comm as the last resort", `{"MESSAGE":"m","_COMM":"sshd"}`, "journald:sshd"},
		{"a non-service unit keeps its suffix", `{"MESSAGE":"m","_SYSTEMD_UNIT":"init.scope"}`, "journald:init.scope"},
		{"nothing to name it by falls back to the source", `{"MESSAGE":"m"}`, "journald"},
		{"an empty field is skipped", `{"MESSAGE":"m","_SYSTEMD_UNIT":"","_COMM":"cron"}`, "journald:cron"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decode(received(tc.entry)).Source; got != tc.want {
				t.Errorf("Source = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestJournaldGarbledTimestampKeepsTheReceiptTime is parseDockerTS's rule for the other
// wire format: a timestamp we cannot read must never cost the line.
func TestJournaldGarbledTimestampKeepsTheReceiptTime(t *testing.T) {
	in := received(`{"MESSAGE":"m","__REALTIME_TIMESTAMP":"not-a-number"}`)
	if got := decode(in); !got.Time.Equal(in.Time) {
		t.Errorf("Time = %v, want the receipt time %v", got.Time, in.Time)
	}
}

func TestJournaldArgs(t *testing.T) {
	tests := []struct {
		name  string
		src   *JournaldSource
		want  []string
		notIn string
	}{
		{
			"defaults follow everything as JSON",
			NewJournaldSource(nil, 7),
			[]string{"-f", "-o", "json", "--no-pager"},
			"-p", // -p 7 is identical to no filter, so it is omitted rather than passed
		},
		{
			"units become one -u each",
			NewJournaldSource([]string{"nginx", "sshd.service"}, 7),
			[]string{"-f", "-o", "json", "--no-pager", "-u", "nginx", "-u", "sshd.service"},
			"-p",
		},
		{
			"a priority floor below 7 is passed",
			NewJournaldSource(nil, 3),
			[]string{"-f", "-o", "json", "--no-pager", "-p", "3"},
			"-u",
		},
		{
			"priority 0 is a floor, not an absent one",
			NewJournaldSource(nil, 0),
			[]string{"-f", "-o", "json", "--no-pager", "-p", "0"},
			"-u",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.src.args()
			if !slices.Equal(got, tc.want) {
				t.Errorf("args() = %q, want %q", got, tc.want)
			}
			if slices.Contains(got, tc.notIn) {
				t.Errorf("args() = %q, should not contain %q", got, tc.notIn)
			}
		})
	}
}

// TestJournaldMissingBinaryIsANamedError: on a host without systemd this is THE failure,
// and "exec: journalctl: executable file not found in $PATH" explains nothing about why
// a log tool wanted it.
func TestJournaldMissingBinaryIsANamedError(t *testing.T) {
	src := NewJournaldSource(nil, 7)
	src.path = filepath.Join(t.TempDir(), "definitely-not-journalctl")

	out := make(chan model.LogLine, 1)
	err := src.Lines(context.Background(), out)
	if err == nil {
		t.Fatal("expected an error for a missing journalctl")
	}
	for _, want := range []string{"journald", "journalctl", "PATH", "Linux"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if len(out) != 0 {
		t.Error("a source that could not start emitted a line")
	}
}

// TestJournaldRunErrorSurfacesTheCause: the exit status is useless on its own — the
// reason is on journalctl's stderr, which is the whole point of capturing it.
func TestJournaldRunErrorSurfacesTheCause(t *testing.T) {
	exit := errors.New("exit status 1")

	t.Run("permission denied earns the group hint", func(t *testing.T) {
		err := runError(exit, "Failed to open files in /var/log/journal: Permission denied\n")
		msg := err.Error()
		for _, want := range []string{"Permission denied", "systemd-journal", "usermod -aG"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not mention %q", msg, want)
			}
		}
	})

	t.Run("any other diagnostic is still surfaced", func(t *testing.T) {
		err := runError(exit, "No journal files were found.\n")
		if !strings.Contains(err.Error(), "No journal files were found.") {
			t.Errorf("error %q lost the diagnostic", err)
		}
		if strings.Contains(err.Error(), "usermod") {
			t.Errorf("error %q offers the group hint for an unrelated failure", err)
		}
	})

	t.Run("silent failure still reports the exit", func(t *testing.T) {
		err := runError(exit, "   \n\n")
		if !errors.Is(err, exit) {
			t.Errorf("error %q did not wrap the exit error", err)
		}
	})
}

func TestCapBufferStopsGrowing(t *testing.T) {
	var c capBuffer
	chunk := strings.Repeat("x", 1024)
	for range 100 { // 100KB into a 4KB cap
		n, err := c.Write([]byte(chunk))
		if err != nil || n != len(chunk) {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(chunk))
		}
	}
	if len(c.String()) != diagCap {
		t.Errorf("buffered %d bytes, want the %d cap", len(c.String()), diagCap)
	}
}

// TestJournaldStopsOnContextCancel: the source must shut down with the rest of the
// process rather than leaving journalctl following forever. A stand-in script stands for
// journalctl so the test needs no journal and no systemd.
func TestJournaldStopsOnContextCancel(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	script := filepath.Join(t.TempDir(), "journalctl")
	body := "#!/bin/sh\nwhile :; do echo '{\"MESSAGE\":\"tick\",\"PRIORITY\":\"6\"}'; sleep 0.05; done\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write stand-in: %v", err)
	}
	_ = sh // presence of a shell is what makes the shebang runnable

	src := NewJournaldSource(nil, 7)
	src.path = script

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan model.LogLine, 64)
	done := make(chan error, 1)
	go func() { done <- src.Lines(ctx, out) }()

	// Wait for the stream to actually be flowing, so cancellation is tested against a
	// running follow rather than a race with startup.
	select {
	case ll := <-out:
		if ll.Raw != "tick" || ll.Level != "INFO" {
			t.Errorf("decoded line = %+v, want the message at INFO", ll)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("no lines from the stand-in journalctl within 5s")
	}

	cancel()
	select {
	case err := <-done:
		// A cancelled context is a clean stop, not a failure — journalctl being killed
		// is how the follow ends.
		if err != nil {
			t.Errorf("Lines returned %v on cancellation, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Lines did not return within 5s of cancellation")
	}
}

// TestJournaldDecodeIsCheap guards the hot path: decode runs per line, and the entry it
// is handed is wide (30+ fields). One unmarshal of one object is the budget.
func TestJournaldDecodeIsCheap(t *testing.T) {
	in := received(sampleEntry)
	if n := testing.AllocsPerRun(100, func() { decode(in) }); n > 100 {
		t.Errorf("decode allocates %.0f times per line, which is beyond one object decode", n)
	}
}

func TestSystemJournalWarning(t *testing.T) {
	// The probe reads real paths, so point it at a directory the test owns.
	machine := "18ac45084a074ffa8a8ff74c0aeeff2b"

	setup := func(t *testing.T, mode os.FileMode) string {
		t.Helper()
		root := t.TempDir()
		dir := filepath.Join(root, machine)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "system.journal"), []byte("x"), mode); err != nil {
			t.Fatalf("write journal: %v", err)
		}
		return root
	}

	t.Run("a readable system journal warns about nothing", func(t *testing.T) {
		systemJournalDirs = []string{setup(t, 0o644)}
		if got := SystemJournalWarning(); got != "" {
			t.Errorf("SystemJournalWarning() = %q, want empty", got)
		}
	})

	t.Run("no system journal at all stays quiet", func(t *testing.T) {
		// Volatile-only journald, a container, forwarding elsewhere: not a permission
		// problem, and a warning on a working setup is worse than none.
		systemJournalDirs = []string{t.TempDir(), filepath.Join(t.TempDir(), "nope")}
		if got := SystemJournalWarning(); got != "" {
			t.Errorf("SystemJournalWarning() = %q, want empty", got)
		}
	})

	t.Run("an unreadable system journal explains the reduced view", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can open anything, so there is no denial to detect")
		}
		systemJournalDirs = []string{setup(t, 0o000)}
		got := SystemJournalWarning()
		for _, want := range []string{"own session", "systemd-journal", "usermod -aG"} {
			if !strings.Contains(got, want) {
				t.Errorf("warning %q does not mention %q", got, want)
			}
		}
	})
}

// TestJournalTextRejectsNonText keeps journalText honest about what it claims to have
// read: a number or an object is not a field value this source can use, and reporting
// ok would put "null" or "{}" into a log line.
func TestJournalTextRejectsNonText(t *testing.T) {
	for _, raw := range []string{`{"a":1}`, `null`, `12.5`, `true`, ``} {
		if v, ok := journalText(json.RawMessage(raw)); ok {
			t.Errorf("journalText(%s) = (%q, true), want ok=false", raw, v)
		}
	}
}
