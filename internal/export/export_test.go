// SPDX-License-Identifier: Apache-2.0

package export

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// flagFor is a representative anomaly: the fields the pipeline captures the instant a
// template crosses the threshold. The pattern carries angle brackets on purpose — they are
// what the pipeline masker leaves behind, and what a naive encoder would mangle.
func flagFor(hash string) Flag {
	return Flag{
		Hash:      hash,
		Pattern:   "connection refused to <IP>:<NUM>",
		Level:     "ERROR",
		Source:    "docker:api",
		Count:     3,
		FirstSeen: time.Date(2026, 7, 24, 10, 15, 4, 0, time.UTC),
		LastSeen:  time.Date(2026, 7, 24, 10, 15, 31, 0, time.UTC),
		Score:     1.6,
		Reasons:   []string{"novel template (first seen)", "level ERROR"},
	}
}

func doneFor(hash string) model.Explanation {
	return model.Explanation{
		Hash:        hash,
		Pattern:     "connection refused to <IP>:<NUM>",
		State:       model.ExplainDone,
		Summary:     "The API cannot reach its database.",
		LikelyCause: "The database container is not accepting connections.",
		Suggestion:  "Check that the db service is up.",
		At:          time.Date(2026, 7, 24, 10, 15, 33, 0, time.UTC),
	}
}

// openTemp opens a writer on a fresh file and returns it with its path.
func openTemp(t *testing.T) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anomalies.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return w, path
}

// linesOf reads the file back as raw lines, ignoring a trailing empty one.
func linesOf(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// closeAndRead closes the writer (which drains everything queued) and returns the file's
// lines. Closing is the synchronization point: after it returns, every record that was sent
// has been written.
func closeAndRead(t *testing.T, w *Writer, path string) []string {
	t.Helper()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return linesOf(t, path)
}

// decodeOne asserts the file holds exactly one line and returns it decoded as a generic map
// — generic on purpose, so the test sees the JSON a consumer sees rather than the struct
// the encoder was built from.
func decodeOne(t *testing.T, lines []string) map[string]any {
	t.Helper()
	if len(lines) != 1 {
		t.Fatalf("wrote %d lines, want exactly 1:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("line does not parse as standalone JSON: %v\n%s", err, lines[0])
	}
	return got
}

// TestRecordCarriesTheDocumentedSchema pins the file format down. The schema is a contract
// published in the README: a consumer indexes these keys without checking they exist, so a
// rename or an omitempty creeping in is a breaking change and must fail here first.
func TestRecordCarriesTheDocumentedSchema(t *testing.T) {
	w, path := openTemp(t)
	w.Flag(flagFor("abc123"))
	w.Resolve(doneFor("abc123"))
	got := decodeOne(t, closeAndRead(t, w, path))

	want := map[string]any{
		"kind":              "anomaly",
		"template_hash":     "abc123",
		"pattern":           "connection refused to <IP>:<NUM>",
		"level":             "ERROR",
		"source":            "docker:api",
		"count_at_flag":     float64(3),
		"first_seen":        "2026-07-24T10:15:04Z",
		"last_seen_at_flag": "2026-07-24T10:15:31Z",
		"score":             1.6,
	}
	for key, wantVal := range want {
		gotVal, ok := got[key]
		if !ok {
			t.Errorf("the record has no %q key", key)
			continue
		}
		if gotVal != wantVal {
			t.Errorf("%s = %v, want %v", key, gotVal, wantVal)
		}
	}
	reasons, ok := got["reasons"].([]any)
	if !ok || len(reasons) != 2 || reasons[0] != "novel template (first seen)" {
		t.Errorf("reasons = %#v, want the scorer's two reasons", got["reasons"])
	}

	ex, ok := got["explanation"].(map[string]any)
	if !ok {
		t.Fatalf("explanation is not an object: %#v", got["explanation"])
	}
	wantEx := map[string]any{
		"state":        "explained",
		"summary":      "The API cannot reach its database.",
		"likely_cause": "The database container is not accepting connections.",
		"suggestion":   "Check that the db service is up.",
		"truncated":    false,
		"error":        "",
		"at":           "2026-07-24T10:15:33Z",
	}
	for key, wantVal := range wantEx {
		gotVal, ok := ex[key]
		if !ok {
			t.Errorf("the explanation has no %q key", key)
			continue
		}
		if gotVal != wantVal {
			t.Errorf("explanation.%s = %v, want %v", key, gotVal, wantVal)
		}
	}
}

// TestPatternKeepsItsAngleBrackets: the masked signature is the thing people grep this file
// for, and Go's default JSON escaping would spell it \u003cIP\u003e. That is valid JSON and
// completely useless to `grep '<IP>'` — including the anonymization-leak check.
func TestPatternKeepsItsAngleBrackets(t *testing.T) {
	w, path := openTemp(t)
	w.Flag(flagFor("abc123"))
	w.Resolve(doneFor("abc123"))
	lines := closeAndRead(t, w, path)

	if len(lines) != 1 {
		t.Fatalf("wrote %d lines, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "connection refused to <IP>:<NUM>") {
		t.Errorf("the pattern is not greppable in the raw line:\n%s", lines[0])
	}
	if strings.Contains(lines[0], `\u003c`) {
		t.Errorf("the encoder escaped the angle brackets:\n%s", lines[0])
	}
}

// TestStreamedPartialsWriteNoLine is the streaming regression guard, and the reason the file
// is trustworthy at all. With --llm-stream an answer arrives as several progressive updates
// before the terminal one; a file that took each of them would carry the same anomaly three
// times, each a little more complete, and anything counting lines would be wrong.
func TestStreamedPartialsWriteNoLine(t *testing.T) {
	w, path := openTemp(t)
	w.Flag(flagFor("abc123"))

	partial := doneFor("abc123")
	partial.State = model.ExplainPending
	partial.LikelyCause, partial.Suggestion = "", ""
	w.Resolve(partial)
	partial.LikelyCause = "The database container is not accepting connections."
	w.Resolve(partial)
	partial.Suggestion = "Check that the db service is up."
	w.Resolve(partial)

	w.Resolve(doneFor("abc123"))

	got := decodeOne(t, closeAndRead(t, w, path))
	ex := got["explanation"].(map[string]any)
	if ex["state"] != "explained" {
		t.Errorf("state = %v, want the terminal explained", ex["state"])
	}
	if ex["suggestion"] != "Check that the db service is up." {
		t.Errorf("the line is not the terminal answer: %v", ex["suggestion"])
	}
}

// TestUnavailableIsExported: an anomaly the model could not explain is still an anomaly, and
// CI wants to see it. It must carry the reason, or "unavailable" is unactionable.
func TestUnavailableIsExported(t *testing.T) {
	w, path := openTemp(t)
	w.Flag(flagFor("abc123"))
	w.Resolve(model.Explanation{
		Hash:  "abc123",
		State: model.ExplainFailed,
		Err:   "HTTP 500 (server error): model not loaded",
		At:    time.Now(),
	})

	ex := decodeOne(t, closeAndRead(t, w, path))["explanation"].(map[string]any)
	if ex["state"] != "unavailable" {
		t.Errorf("state = %v, want unavailable", ex["state"])
	}
	if ex["error"] != "HTTP 500 (server error): model not loaded" {
		t.Errorf("error = %v, want the provider's reason", ex["error"])
	}
}

// TestIncompleteAnswerIsMarked: a salvaged answer is real but short. A consumer acting on
// "likely cause" has to be able to tell it from one the model finished writing.
func TestIncompleteAnswerIsMarked(t *testing.T) {
	w, path := openTemp(t)
	w.Flag(flagFor("abc123"))
	ex := doneFor("abc123")
	ex.Truncated = true
	ex.Suggestion = ""
	w.Resolve(ex)

	got := decodeOne(t, closeAndRead(t, w, path))["explanation"].(map[string]any)
	if got["state"] != "explained" || got["truncated"] != true {
		t.Errorf("state/truncated = %v/%v, want explained/true", got["state"], got["truncated"])
	}
}

// TestWouldEscalateIsUnmistakable: --explain-dry-run writes lines for what WOULD fire, which
// is what makes a threshold change diffable. Those records have no summary, cause, or
// suggestion because no model was called — so a parser reading a mixed file must be able to
// reject them at a glance rather than treat them as explained anomalies.
func TestWouldEscalateIsUnmistakable(t *testing.T) {
	w, path := openTemp(t)
	w.Flag(flagFor("abc123"))
	w.WouldEscalate("abc123", time.Now())

	got := decodeOne(t, closeAndRead(t, w, path))
	if got["kind"] != KindWouldEscalate {
		t.Errorf("kind = %v, want %s", got["kind"], KindWouldEscalate)
	}
	ex := got["explanation"].(map[string]any)
	if ex["state"] != "not_requested" {
		t.Errorf("state = %v, want not_requested", ex["state"])
	}
	for _, key := range []string{"summary", "likely_cause", "suggestion", "error"} {
		if ex[key] != "" {
			t.Errorf("explanation.%s = %q, want empty: no model was called", key, ex[key])
		}
	}
	// The score and reasons are the whole point of the mode: they are what gets calibrated.
	if got["score"] != 1.6 {
		t.Errorf("score = %v, want the scorer's 1.6", got["score"])
	}
}

// TestResolveBeforeFlagStillWrites covers a real ordering, not a hypothetical one: the
// scorer hands the escalation to the LLM pool from inside Evaluate, BEFORE the pipeline
// calls Flag. A fast backend can therefore answer, and --plain can call Resolve on its own
// goroutine, while the flag is still in flight. Joining on whichever half lands second is
// what stops that from silently dropping the record.
func TestResolveBeforeFlagStillWrites(t *testing.T) {
	w, path := openTemp(t)
	w.Resolve(doneFor("abc123"))
	w.Flag(flagFor("abc123"))

	got := decodeOne(t, closeAndRead(t, w, path))
	if got["template_hash"] != "abc123" || got["source"] != "docker:api" {
		t.Errorf("the record lost its flag-time fields: %#v", got)
	}
}

// TestOneLinePerFlagNotPerExplanation: a second terminal answer for a template that has
// already been written adds nothing — the record belongs to the FLAG, and a re-occurrence
// that only bumps the in-memory count never reaches the exporter at all. A fresh escalation
// of the same template is a new flag, and does get its own line.
func TestOneLinePerFlagNotPerExplanation(t *testing.T) {
	w, path := openTemp(t)
	w.Flag(flagFor("abc123"))
	w.Resolve(doneFor("abc123"))
	w.Resolve(doneFor("abc123")) // a duplicate answer: nothing to write it against

	second := flagFor("abc123")
	second.Count = 40 // the same template escalating again, much later
	w.Flag(second)
	w.Resolve(doneFor("abc123"))

	lines := closeAndRead(t, w, path)
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines, want 2 (one per flag):\n%s", len(lines), strings.Join(lines, "\n"))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatalf("second line does not parse: %v", err)
	}
	if rec["count_at_flag"] != float64(40) {
		t.Errorf("count_at_flag = %v, want 40 — the second record's own flag-time count", rec["count_at_flag"])
	}
}

// TestRecordsLandBeforeClose is the durability property: a process killed mid-run must leave
// a file that is already useful. Records are written and synced as they resolve, not
// accumulated and flushed at the end.
func TestRecordsLandBeforeClose(t *testing.T) {
	w, path := openTemp(t)
	defer func() { _ = w.Close() }()

	w.Flag(flagFor("abc123"))
	w.Resolve(doneFor("abc123"))

	// Poll rather than sleep: the write happens on the writer goroutine, so the only honest
	// assertion is "it lands, without anyone closing the file".
	deadline := time.Now().Add(5 * time.Second)
	for {
		lines := linesOf(t, path)
		if len(lines) == 1 {
			var rec map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
				t.Fatalf("a mid-run line is not valid JSON: %v\n%s", err, lines[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing reached the file while the writer was still open (got %d lines)", len(lines))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestOpenAppendsToAnExistingFile: a second run adds to the file rather than quietly
// destroying the first run's anomalies.
func TestOpenAppendsToAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anomalies.jsonl")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first.Flag(flagFor("abc123"))
	first.Resolve(doneFor("abc123"))
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	second.Flag(flagFor("def456"))
	second.Resolve(doneFor("def456"))
	lines := closeAndRead(t, second, path)

	if len(lines) != 2 {
		t.Fatalf("file has %d lines after two runs, want 2", len(lines))
	}
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("line %d does not parse: %v", i+1, err)
		}
	}
}

// TestNilWriterIsInert is the default-off guarantee at the type level: with no --export
// there is no writer, and every call site is unconditional. If any method stopped tolerating
// a nil receiver, the default path would panic rather than do nothing.
func TestNilWriterIsInert(t *testing.T) {
	var w *Writer
	w.Flag(flagFor("abc123"))
	w.Resolve(doneFor("abc123"))
	w.WouldEscalate("abc123", time.Now())
	if n := w.Dropped(); n != 0 {
		t.Errorf("Dropped() = %d on a nil writer, want 0", n)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close() = %v on a nil writer, want nil", err)
	}
}

// --- line integrity -------------------------------------------------------------------
//
// The tests above prove records land. These prove they land WHOLE, which is a different
// claim: write(2) on a regular file may write fewer bytes than asked, and the bytes it did
// write are already on disk. "One Write per record" prevents records from interleaving; it
// does not make one record atomic.

// chunkSink writes at most max bytes per call, and optionally fails after a given number of
// bytes have gone out. It is the file as the kernel is allowed to behave, not as it usually
// does.
type chunkSink struct {
	mu sync.Mutex

	max       int  // bytes accepted per Write call (0: everything)
	failAfter int  // total bytes to accept before failing ONCE (0: never fail)
	hardFail  bool // Truncate fails too: the rollback cannot happen

	buf     []byte
	written int
	failed  bool
	// didFail makes the failure one-shot. A sink that failed at the same byte count
	// forever would take every subsequent record with it, and then the test could not tell
	// "the writer recovered" from "the writer gave up".
	didFail  bool
	syncs    int
	closed   bool
	truncErr error
}

func (c *chunkSink) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return 0, errors.New("sink is broken")
	}
	n := len(p)
	if c.max > 0 && n > c.max {
		n = c.max
	}
	if c.failAfter > 0 && !c.didFail && c.written+n >= c.failAfter {
		n = c.failAfter - c.written
		c.buf = append(c.buf, p[:n]...)
		c.written += n
		c.failed, c.didFail = true, true
		return n, errors.New("no space left on device")
	}
	c.buf = append(c.buf, p[:n]...)
	c.written += n
	if n < len(p) {
		// What os.File.Write reports on a partial write, wrapped — writeAll has to
		// recognise it through a wrapper, not by string comparison.
		return n, fmt.Errorf("chunked: %w", io.ErrShortWrite)
	}
	return n, nil
}

func (c *chunkSink) Truncate(size int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hardFail {
		c.truncErr = errors.New("truncate refused")
		return c.truncErr
	}
	if size < int64(len(c.buf)) {
		c.buf = c.buf[:size]
		c.written = int(size)
	}
	// A rollback clears the failure: the next record gets a fair attempt.
	c.failed = false
	return nil
}

func (c *chunkSink) Sync() error  { c.mu.Lock(); defer c.mu.Unlock(); c.syncs++; return nil }
func (c *chunkSink) Close() error { c.mu.Lock(); defer c.mu.Unlock(); c.closed = true; return nil }

func (c *chunkSink) contents() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

// parseAll reports whether every line of s is a complete JSON object — the JSONL guarantee.
func parseAll(t *testing.T, s string) int {
	t.Helper()
	if s == "" {
		return 0
	}
	if !strings.HasSuffix(s, "\n") {
		t.Errorf("the file does not end on a line boundary — a partial record was left behind:\n%q", s)
	}
	n := 0
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Errorf("line %d is not standalone JSON: %v\n%s", n+1, err, sc.Text())
		}
		n++
	}
	return n
}

// TestShortWritesStillProduceWholeLines: a sink that only ever accepts a few bytes per call
// must still yield complete lines. Without the write loop this is exactly where a record
// gets cut in half and the file stops being JSONL.
func TestShortWritesStillProduceWholeLines(t *testing.T) {
	sink := &chunkSink{max: 7}
	w := newWriter(sink, 0)

	for _, hash := range []string{"abc123", "def456", "ghi789"} {
		w.Flag(flagFor(hash))
		w.Resolve(doneFor(hash))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if n := parseAll(t, sink.contents()); n != 3 {
		t.Errorf("file has %d parseable lines, want 3", n)
	}
}

// TestFailedWriteLeavesNoPartialLine is the failure mode that matters. The sink accepts part
// of a record and then errors, so bytes are already on disk. That record is lost — but the
// file must still be valid JSONL, and the records after it must still parse.
func TestFailedWriteLeavesNoPartialLine(t *testing.T) {
	sink := &chunkSink{max: 16, failAfter: 40} // dies well inside the first record
	w := newWriter(sink, 0)

	w.Flag(flagFor("abc123"))
	w.Resolve(doneFor("abc123"))
	w.Flag(flagFor("def456"))
	w.Resolve(doneFor("def456"))

	err := w.Close()
	if err == nil {
		t.Error("Close() = nil, want the write error reported rather than swallowed")
	} else if !strings.Contains(err.Error(), "no space left on device") {
		t.Errorf("Close() = %v, want the underlying write failure", err)
	}

	got := sink.contents()
	if n := parseAll(t, got); n != 1 {
		t.Errorf("file has %d parseable lines, want 1 (the record after the failure):\n%q", n, got)
	}
	if strings.Contains(got, `"abc123"`) {
		t.Errorf("the half-written record was not rolled back:\n%q", got)
	}
	if !strings.Contains(got, `"def456"`) {
		t.Errorf("writing did not resume after a rolled-back record:\n%q", got)
	}
}

// TestUnrollableFailureStopsWriting: if the partial bytes cannot be truncated away, the file
// ends mid-record and there is nothing to be done about it. Appending the next record would
// fuse the two into one unparseable line and take the following records down with it, so the
// writer stops for good — losing records rather than the file.
func TestUnrollableFailureStopsWriting(t *testing.T) {
	sink := &chunkSink{max: 16, failAfter: 40, hardFail: true}
	w := newWriter(sink, 0)

	w.Flag(flagFor("abc123"))
	w.Resolve(doneFor("abc123"))
	w.Flag(flagFor("def456"))
	w.Resolve(doneFor("def456"))
	if err := w.Close(); err == nil {
		t.Error("Close() = nil, want the failure reported")
	}

	got := sink.contents()
	if strings.Contains(got, `"def456"`) {
		t.Errorf("a record was appended after an unrecoverable partial line:\n%q", got)
	}
	if strings.Count(got, "\n") != 0 {
		t.Errorf("the truncated remains should be the only content:\n%q", got)
	}
}

// TestOffsetStartsFromExistingContent: the rollback point is the end of the last complete
// record, which on an appended-to file is wherever the previous run stopped. Getting this
// wrong would truncate a healthy file back to nothing on the first write error.
func TestOffsetStartsFromExistingContent(t *testing.T) {
	existing := "{\"kind\":\"anomaly\"}\n"
	sink := &chunkSink{max: 16, failAfter: len(existing) + 40, buf: []byte(existing), written: len(existing)}
	w := newWriter(sink, int64(len(existing)))

	w.Flag(flagFor("abc123"))
	w.Resolve(doneFor("abc123"))
	_ = w.Close()

	if got := sink.contents(); got != existing {
		t.Errorf("the rollback did not stop at the pre-existing content:\ngot  %q\nwant %q", got, existing)
	}
}
