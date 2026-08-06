// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// journalctl is the binary this source drives. Shelling out is deliberate, and the
// reason is the release build: the alternative — binding libsystemd through
// coreos/go-systemd's sdjournal — is cgo, and the shipped binaries are cross-compiled
// for darwin and windows with CGO_ENABLED=0. A cgo dependency would cost the static
// linux build, both non-linux targets, and the absence of build tags anywhere in this
// package, in exchange for avoiding a dependency on a binary that is present on every
// host that has a journal to read. journald is Linux-only regardless; on a machine
// without journalctl this source fails at startup with a message that says so.
const journalctl = "journalctl"

// priorityCount is the number of syslog priorities journald records (0..7).
const priorityCount = 8

// errPriority is the highest-numbered (least severe) priority still treated as
// error-class. systemd records a unit's stderr at 3 and its stdout at 6 by default, so
// this is also the line between the two streams — see decode.
const errPriority = 3

// priorityLevels maps journald's numeric PRIORITY onto the canonical level set the
// pipeline produces and the scorer weighs. Deliberately a plain table and not a syslog
// implementation: it is the one piece of journald metadata that carries severity, and
// what logscry needs from it is which of five buckets a line falls into.
//
// emerg and alert are FATAL — "system is unusable" and "action must be taken
// immediately" are the crash class the fatal weight exists to interrupt for. crit is
// CRITICAL, which the scorer weighs as ERROR: serious, but not on its own worth an
// interrupt.
var priorityLevels = [priorityCount]string{
	0: "FATAL",    // emerg
	1: "FATAL",    // alert
	2: "CRITICAL", // crit
	3: "ERROR",    // err
	4: "WARN",     // warning
	5: "INFO",     // notice
	6: "INFO",     // info
	7: "DEBUG",    // debug
}

// noPriorityFilter is the PRIORITY floor meaning "everything". journalctl's -p takes the
// least severe priority to show, so 7 (debug) already includes every entry — at that
// value the flag is simply omitted rather than passed as a no-op.
const noPriorityFilter = 7

// JournaldSource follows the systemd journal by running `journalctl -f -o json` and
// decoding one journal entry per line.
//
// It is one long-running Source like DockerSource: Lines blocks until ctx is cancelled
// or journalctl fails, and the fan-in (Run) closes the shared channel only when every
// Source ends.
type JournaldSource struct {
	// Units filters to specific systemd units (--journald-unit, repeatable). Each
	// becomes a -u argument, which journalctl OR-combines.
	Units []string
	// Priority is the PRIORITY floor (0..7): entries less severe than this are not
	// shown. 7 means no filter.
	Priority int

	// path overrides the journalctl binary. Empty resolves "journalctl" on PATH; tests
	// point it at a stand-in so the source can be exercised without a live journal.
	path string
}

// NewJournaldSource returns a Source that follows the systemd journal.
func NewJournaldSource(units []string, priority int) *JournaldSource {
	return &JournaldSource{Units: units, Priority: priority}
}

// Name implements Source. Like DockerSource's "docker" it names the source, not the
// stream: individual lines are tagged "journald:<unit>" by decode, the same way Docker
// tags its lines "docker:<container>".
func (s *JournaldSource) Name() string { return "journald" }

// args builds the journalctl command line: follow, one compact JSON object per line,
// never a pager.
//
// -o json rather than json-pretty because compact output is one entry per line, which is
// exactly the newline-delimited shape readLines already handles.
func (s *JournaldSource) args() []string {
	args := []string{"-f", "-o", "json", "--no-pager"}
	for _, u := range s.Units {
		args = append(args, "-u", u)
	}
	if s.Priority < noPriorityFilter {
		args = append(args, "-p", strconv.Itoa(s.Priority))
	}
	return args
}

// Lines implements Source. It runs journalctl in follow mode and pumps decoded entries
// into out until ctx is cancelled (a clean stop → nil) or journalctl exits.
func (s *JournaldSource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	bin := s.path
	if bin == "" {
		bin = journalctl
	}
	// Resolved before starting, so "not installed" is reported as itself rather than as
	// a generic exec failure from inside Start.
	exe, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("journald: %s not found in PATH — the journald source needs "+
			"systemd's journalctl, which exists only on Linux: %w", bin, err)
	}

	cmd := exec.CommandContext(ctx, exe, s.args()...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("journald: %w", err)
	}
	// journalctl's stderr is diagnostics ABOUT the journal, not journal content, so —
	// unlike SubprocessSource, which emits both streams as log lines — it is captured
	// for the error message instead of being fed into the pipeline as if the journal
	// had said it.
	var diag capBuffer
	cmd.Stderr = &diag

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("journald: %w", err)
	}

	readErr := mapLines(ctx, stdout, s.Name(), model.Stdout, out, decode)
	// All reads from the pipe must complete before Wait (os/exec requirement).
	waitErr := cmd.Wait()

	// Ctrl-C kills journalctl, which surfaces here as a signal exit and a cancelled
	// read. Neither is a failure: this is the clean stop.
	if ctx.Err() != nil {
		return nil
	}
	if waitErr != nil {
		return runError(waitErr, diag.String())
	}
	return readErr
}

// decode turns one `journalctl -o json` entry into a log line: MESSAGE becomes Raw,
// PRIORITY becomes the level and the stream, the unit becomes the source name, and
// __REALTIME_TIMESTAMP becomes the time.
//
// Anything it cannot make sense of — a line that is not JSON, an entry carrying no
// MESSAGE — is passed through untouched rather than dropped. journalctl is not supposed
// to emit either, but showing a raw line beats silently losing one, and the pipeline
// handles arbitrary text by definition.
func decode(ll model.LogLine) model.LogLine {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ll.Raw), &entry); err != nil {
		return ll
	}
	msg, ok := journalText(entry["MESSAGE"])
	if !ok {
		return ll
	}
	ll.Raw = msg

	if p, ok := journalPriority(entry["PRIORITY"]); ok {
		ll.Level = priorityLevels[p]
		// Error-class entries are tagged stderr, which is faithful rather than
		// invented: systemd forwards a unit's stderr at priority 3 and its stdout at 6.
		// It also keeps journald at parity with the other sources — without it an
		// ERROR from a unit would score below the byte-identical ERROR from a container,
		// which writes to stderr and picks up that weight. It cannot over-fire on its
		// own: the scorer caps the stream+level sum at SeverityMax.
		if p <= errPriority {
			ll.Stream = model.Stderr
		}
	}
	if name := journalUnit(entry); name != "" {
		ll.Source = "journald:" + name
	}
	if t, ok := journalTime(entry["__REALTIME_TIMESTAMP"]); ok {
		ll.Time = t
	}
	return ll
}

// unitFields are the entry fields naming what produced a line, best first.
//
// _SYSTEMD_UNIT leads because it is the string a user would type into --journald-unit,
// which makes the tag on a line and the flag that would select it the same word.
// SYSLOG_IDENTIFIER covers what has no unit — most usefully the kernel — and _COMM is
// the last resort.
var unitFields = []string{"_SYSTEMD_UNIT", "SYSLOG_IDENTIFIER", "_COMM"}

// journalUnit returns a short name for whatever produced the entry, or "" when it says.
// ".service" is trimmed because it is on almost every unit and therefore distinguishes
// nothing, while costing eight columns of a pane that is already the narrow half of the
// layout (the reason SubprocessSource uses a base name).
func journalUnit(entry map[string]json.RawMessage) string {
	for _, f := range unitFields {
		if v, ok := journalText(entry[f]); ok && v != "" {
			return strings.TrimSuffix(v, ".service")
		}
	}
	return ""
}

// journalText reads a journal field as text. A field is normally a JSON string, but
// journald renders one whose value is not valid UTF-8 — kernel messages, binary payloads
// — as an ARRAY OF BYTE VALUES instead, which is why the entry is decoded field by field
// from RawMessage rather than into a struct: a single such field would otherwise fail the
// unmarshal of the whole entry and lose a line that is perfectly readable.
// A JSON null is rejected up front because it unmarshals into BOTH a string and a []byte
// WITHOUT error, leaving each empty — so a "MESSAGE": null would be adopted as an empty
// message and the line emitted blank, instead of falling back like the absent field it
// is. (pipeline.jsonString guards the same trap for the same reason.)
func journalText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var b []byte
	if err := json.Unmarshal(raw, &b); err == nil {
		return string(b), true
	}
	return "", false
}

// journalPriority reads PRIORITY, which journalctl emits as a STRING ("6") though a
// number is accepted too. Out-of-range and unparseable values report false: the entry
// keeps no level and stays on stdout rather than being assigned a severity from a value
// that was not understood.
func journalPriority(raw json.RawMessage) (int, bool) {
	s, ok := journalText(raw)
	if !ok {
		// Not a string: accept a bare JSON number as well.
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return 0, false
		}
		s = strconv.Itoa(n)
	}
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 0 || p >= priorityCount {
		return 0, false
	}
	return p, true
}

// journalTime reads __REALTIME_TIMESTAMP, microseconds since the epoch as a string. It
// reports false on anything it cannot parse, leaving the receipt time in place — the
// same fallback parseDockerTS makes, for the same reason: a missing or garbled timestamp
// must never cost the line.
func journalTime(raw json.RawMessage) (time.Time, bool) {
	s, ok := journalText(raw)
	if !ok {
		return time.Time{}, false
	}
	usec, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMicro(usec), true
}

// runError turns journalctl's exit into something actionable. Its stderr is the only
// place the real cause appears — "Failed to open files in /var/log/journal: Permission
// denied" for a user outside the systemd-journal group, "No journal files were found" on
// a host with no persistent journal — and a bare "exit status 1" throws all of it away.
func runError(err error, diag string) error {
	msg := firstLine(diag)
	if msg == "" {
		return fmt.Errorf("journald: journalctl: %w", err)
	}
	if strings.Contains(strings.ToLower(msg), "permission denied") {
		return fmt.Errorf("journald: journalctl: %s — %s", msg, journalGroupHint)
	}
	return fmt.Errorf("journald: journalctl: %s", msg)
}

// journalGroupHint is the fix for the access problem, in both the hard-failure message
// above and the reduced-view warning below. One string because it is one remedy, and a
// user who hits either needs the exact command rather than the name of a group.
const journalGroupHint = "reading the system journal needs membership of the " +
	"systemd-journal group (sudo usermod -aG systemd-journal $USER, then log in again) or root"

// firstLine returns the first non-empty line of s, trimmed. journalctl can print several
// lines of diagnostics; the first names the cause and the rest elaborate.
func firstLine(s string) string {
	for line := range strings.Lines(s) {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// diagCap bounds how much of journalctl's stderr is retained: enough for the diagnostic
// that goes into an error message, never enough for a chatty process to grow the buffer
// without limit over a long run.
const diagCap = 4 << 10

// capBuffer accumulates up to diagCap bytes and silently discards the rest. Writes come
// from the goroutine os/exec runs for cmd.Stderr and are read only after Wait, so it
// needs no lock.
type capBuffer struct{ b []byte }

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := diagCap - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(room, len(p))]...)
	}
	// Report the full length: a short write would be an error to the caller, and
	// dropping the overflow is this type's job, not a failure.
	return len(p), nil
}

func (c *capBuffer) String() string { return string(c.b) }

// systemJournalDirs are where journald keeps the SYSTEM journal — persistent storage
// first, then the volatile runtime journal used when persistence is off. A var so tests
// can point the probe at a directory they control.
var systemJournalDirs = []string{"/var/log/journal", "/run/log/journal"}

// SystemJournalWarning reports why the system journal will not be fully visible, or ""
// when it will be — or when there is nothing to conclude.
//
// This exists because the common failure is SILENT, and a silent one is the worst kind
// for a tool whose whole promise is to notice things. A user outside the systemd-journal
// group runs --journald and journalctl starts, follows, and streams perfectly happily —
// their own session's journal, and nothing from any system service. There is no error to
// report and no exit code to check, so logscry looks broken while doing exactly what it
// was told. The hard failures (no journalctl, an outright permission error) already
// announce themselves; this is the one that does not.
//
// The probe opens the same files journalctl would rather than checking group membership,
// because access is also granted by ACL — Debian and Ubuntu give the "adm" group read
// access that way — and a check of the group would warn a user whose setup works.
//
// Silence when uncertain is deliberate: no system journal files at all means journald is
// volatile-only, forwarding elsewhere, or this is a container, none of which is a
// permission problem. A warning that fires on a working setup would be noise, and this
// tool's entire thesis is that noise is what kills it.
func SystemJournalWarning() string {
	var found, denied bool
	for _, dir := range systemJournalDirs {
		// The pattern is a constant, so the only Glob error (ErrBadPattern) is
		// impossible; a missing directory simply matches nothing.
		matches, _ := filepath.Glob(filepath.Join(dir, "*", "system*.journal"))
		for _, path := range matches {
			found = true
			f, err := os.Open(path)
			if err == nil {
				_ = f.Close()
				return "" // one readable system journal is all journalctl needs
			}
			if errors.Is(err, fs.ErrPermission) {
				denied = true
			}
		}
	}
	if !found || !denied {
		return ""
	}
	return "journald: the system journal is not readable, so only your own session's " +
		"logs will be followed — " + journalGroupHint
}
