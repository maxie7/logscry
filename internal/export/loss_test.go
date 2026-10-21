// SPDX-License-Identifier: Apache-2.0

package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// --- lost records (#70) ------------------------------------------------------------------
//
// A live writer drops rather than stall its sender when the disk cannot keep up. These pin
// what a drop costs: whole records, counted as records, never a record built from the halves
// of two escalations, and a file that says it is incomplete.

// waitFor polls cond until it holds, failing the test after a generous bound.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// heldWriter returns a live writer stuck inside the write of its first record, with an empty
// queue behind it: the disk has stalled, and every slot of the queue is free. Closing gate
// lets the disk go on. Cleanup closes it if the test has not, so no goroutine outlives it.
func heldWriter(t *testing.T) (*Writer, *gatedSink, func()) {
	t.Helper()
	sink := &gatedSink{gate: make(chan struct{})}
	w := newWriter(sink, 0)
	released := false
	release := func() {
		if !released {
			released = true
			close(sink.gate)
		}
	}
	t.Cleanup(release)
	w.Flag(flagFor("held"))
	w.WouldEscalate("held", 0, time.Date(2026, 8, 29, 10, 40, 0, 0, time.UTC))
	waitFor(t, "the writer to take the held record", func() bool { return len(w.in) == 0 })
	return w, sink, release
}

// fillQueue sends n messages as complete dry-run records under hashes no other test half
// uses, which leaves the queue n slots fuller.
func fillQueue(w *Writer, n int) {
	at := time.Date(2026, 8, 29, 10, 40, 0, 0, time.UTC)
	for i := range n / 2 {
		h := fmt.Sprintf("fill%04d", i)
		w.Flag(flagFor(h))
		w.WouldEscalate(h, 0, at)
	}
	if n%2 == 1 {
		w.Flag(flagFor("pad")) // a lone flag: it only takes a slot
	}
}

// decodeLines parses every line of s, failing on one that is not a JSON object.
func decodeLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line is not standalone JSON: %v\n%s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// recordsFor returns the records written for hash.
func recordsFor(recs []map[string]any, hash string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if r["template_hash"] == hash {
			out = append(out, r)
		}
	}
	return out
}

// escalationOf is one escalation of template x: its flag carries count, its answer at.
func escalationOf(count int, at time.Time) (Flag, model.Explanation) {
	f := flagFor("x")
	f.Count = count
	ex := doneFor("x")
	ex.At = at
	return f, ex
}

// TestLostCountsRecords: Dropped counts RECORDS. A record is two messages, a flag and its
// resolution, and losing either loses the record once. Counting messages overstated the loss
// by up to 2x: the issue measured 3,061 records lost reported as "6102 record(s) dropped".
// Whatever the disk managed, written plus lost is what was sent.
func TestLostCountsRecords(t *testing.T) {
	sink := &gatedSink{gate: make(chan struct{})}
	w := newWriter(sink, 0)
	const n = 3000 // 6000 messages into a 1024-slot queue behind a stalled disk
	flood(w, n)
	close(sink.gate)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	written := 0
	for _, r := range decodeLines(t, sink.contents()) {
		if r["kind"] == KindWouldEscalate {
			written++
		}
	}
	if lost := w.Dropped(); written+int(lost) != n {
		t.Fatalf("%d records written + %d reported lost = %d, want the %d sent", written, lost, written+int(lost), n)
	}
}

// TestDroppedFlagNeverPairsWithALaterEscalation is the mixed-halves defect. The writer joins a
// flag and its resolution by template hash. When a flag was dropped, its resolution still
// went in, waited unmatched, and was joined to the NEXT flag of the same template: a record
// with one escalation's count and another's explanation, which nothing could tell was wrong.
// The resolution of a dropped flag must be dropped on arrival, even with room in the queue.
func TestDroppedFlagNeverPairsWithALaterEscalation(t *testing.T) {
	w, sink, release := heldWriter(t)
	fillQueue(w, queueCap)

	t1, t2 := time.Date(2026, 8, 29, 11, 0, 1, 0, time.UTC), time.Date(2026, 8, 29, 11, 0, 2, 0, time.UTC)
	f1, r1 := escalationOf(1, t1)
	f2, r2 := escalationOf(2, t2)
	w.Flag(f1) // the queue is full: dropped
	release()
	waitFor(t, "the queue to drain", func() bool { return len(w.in) == 0 })
	w.Resolve(r1) // f1's other half, with room for it now
	w.Flag(f2)
	w.Resolve(r2)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got := recordsFor(decodeLines(t, sink.contents()), "x")
	if len(got) != 1 {
		t.Fatalf("x has %d records, want 1 (the second escalation's):\n%v", len(got), got)
	}
	if c, at := got[0]["count_at_flag"], got[0]["explanation"].(map[string]any)["at"]; c != float64(2) || at != t2.Format(time.RFC3339) {
		t.Errorf("x's record has count_at_flag %v and explanation.at %v; want 2 and %s, both from the second escalation",
			c, at, t2.Format(time.RFC3339))
	}
	if lost := w.Dropped(); lost != 1 {
		t.Errorf("Dropped() = %d, want 1 record", lost)
	}
}

// TestDroppedResolutionNeverPairsWithALaterEscalation is the same defect the other way round:
// the flag went in, its resolution was the half that could not be queued. The flag waited, and
// the next escalation's resolution, arriving before its own flag (a fast model can beat the
// pipeline to it), was joined to it.
func TestDroppedResolutionNeverPairsWithALaterEscalation(t *testing.T) {
	w, sink, release := heldWriter(t)

	t1, t2 := time.Date(2026, 8, 29, 11, 0, 1, 0, time.UTC), time.Date(2026, 8, 29, 11, 0, 2, 0, time.UTC)
	f1, r1 := escalationOf(1, t1)
	f2, r2 := escalationOf(2, t2)
	w.Flag(f1)
	fillQueue(w, queueCap-1)
	w.Resolve(r1) // the queue is full: dropped
	release()
	waitFor(t, "the queue to drain", func() bool { return len(w.in) == 0 })
	w.Resolve(r2) // the next escalation, answer first
	w.Flag(f2)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got := recordsFor(decodeLines(t, sink.contents()), "x")
	if len(got) != 1 {
		t.Fatalf("x has %d records, want 1 (the second escalation's):\n%v", len(got), got)
	}
	if c, at := got[0]["count_at_flag"], got[0]["explanation"].(map[string]any)["at"]; c != float64(2) || at != t2.Format(time.RFC3339) {
		t.Errorf("x's record has count_at_flag %v and explanation.at %v; want 2 and %s, both from the second escalation",
			c, at, t2.Format(time.RFC3339))
	}
	if lost := w.Dropped(); lost != 1 {
		t.Errorf("Dropped() = %d, want 1 record", lost)
	}
}

// TestTrailerRecordsTheLoss: a file that lost records says so in its last line, so a consumer
// holding only the file can tell it is incomplete, and by how many records.
func TestTrailerRecordsTheLoss(t *testing.T) {
	sink := &gatedSink{gate: make(chan struct{})}
	w := newWriter(sink, 0)
	before := time.Now().UTC().Truncate(time.Second)
	flood(w, 3000)
	close(sink.gate)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()

	recs := decodeLines(t, sink.contents())
	if len(recs) == 0 {
		t.Fatal("the file is empty")
	}
	last := recs[len(recs)-1]
	if last["kind"] != "export_incomplete" {
		t.Fatalf("the last line is %v, want the export_incomplete record", last)
	}
	if len(last) != 3 {
		t.Errorf("the export_incomplete record has keys %v, want exactly kind, records_lost and at", last)
	}
	if n, ok := last["records_lost"].(float64); !ok || uint64(n) != w.Dropped() || n == 0 {
		t.Errorf("records_lost = %v, want Dropped() = %d", last["records_lost"], w.Dropped())
	}
	s, _ := last["at"].(string)
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || !strings.HasSuffix(s, "Z") || at.Before(before) || at.After(after) {
		t.Errorf("at = %q, want RFC 3339 in UTC between %s and %s", s, before, after)
	}
	for _, r := range recs[:len(recs)-1] {
		if r["kind"] != KindWouldEscalate {
			t.Fatalf("a line before the last is %v, want only records", r)
		}
	}
}

// TestCompleteFileIsUnchanged: with nothing lost the file is exactly what it always was — the
// records, and no trailing line.
func TestCompleteFileIsUnchanged(t *testing.T) {
	sink := &chunkSink{}
	w := newWriter(sink, 0)
	at := time.Date(2026, 8, 29, 10, 40, 0, 0, time.UTC)
	var want bytes.Buffer
	for i := range 3 {
		h := fmt.Sprintf("h%d", i)
		w.Flag(flagFor(h))
		w.Resolve(doneFor(h))
		b, err := encode(record(flagFor(h), resolution{hash: h, kind: KindAnomaly, explanation: explanationOf(doneFor(h))}))
		if err != nil {
			t.Fatal(err)
		}
		want.Write(b)
	}
	w.Flag(flagFor("dry"))
	w.WouldEscalate("dry", 0, at)
	b, err := encode(record(flagFor("dry"), resolution{hash: "dry", kind: KindWouldEscalate,
		explanation: Explanation{State: stateNotRequested, At: at}}))
	if err != nil {
		t.Fatal(err)
	}
	want.Write(b)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := sink.contents(); got != want.String() {
		t.Errorf("a complete file changed:\ngot  %q\nwant %q", got, want.String())
	}
}

// --- a bounded Close on a live writer ---------------------------------------------------

// TestCloseGivesUpOnAStalledDisk: a live writer whose disk accepts nothing must not hang the
// exit. Close gives up after a stretch with no progress, counts every record still queued as
// lost, and does not attempt the trailing record on a disk that is not moving.
func TestCloseGivesUpOnAStalledDisk(t *testing.T) {
	sink := &gatedSink{gate: make(chan struct{})}
	t.Cleanup(func() { close(sink.gate) })
	w := newWriter(sink, 0)
	w.stall = 200 * time.Millisecond
	const n = 100 // 200 messages: all of them fit in the queue
	flood(w, n)
	waitFor(t, "the writer to start its first write", func() bool { return len(w.in) == 2*n-2 })

	closed := make(chan error, 1)
	go func() { closed <- w.Close() }()
	select {
	case err := <-closed:
		if err == nil || !strings.Contains(err.Error(), "accepted no write for") ||
			!strings.Contains(err.Error(), "export_incomplete record could not be written") {
			t.Errorf("Close = %v, want it to say it gave up and that the trailing record was not written", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not give up on a disk that accepted nothing")
	}
	// The record stuck in the write is not counted: it may yet land. The rest never will.
	if lost := w.Dropped(); lost != n-1 {
		t.Errorf("Dropped() = %d, want the %d records still queued", lost, n-1)
	}
	if got := sink.contents(); got != "" {
		t.Errorf("the stalled disk received %q", got)
	}
}

// TestCloseDrainsASlowButMovingDisk: the bound is on NO progress, not on time. A disk that is
// slow but keeps completing records is drained to the last one, however long that takes.
func TestCloseDrainsASlowButMovingDisk(t *testing.T) {
	sink := &slowSink{delay: 2 * time.Millisecond}
	w := newWriter(sink, 0)
	w.stall = 200 * time.Millisecond
	const n = 300 // 600 messages: no drops; about 0.6 s of writes, three times the stall
	flood(w, n)
	if err := w.Close(); err != nil {
		t.Fatalf("Close = %v, want a full drain", err)
	}
	if got := strings.Count(sink.contents(), "\n"); got != n {
		t.Errorf("the file holds %d records, want all %d", got, n)
	}
	if lost := w.Dropped(); lost != 0 {
		t.Errorf("Dropped() = %d, want 0", lost)
	}
}

// TestBlockingCloseStillWaits: replay's writer keeps its promise of no drops (#35). Its Close
// waits for the disk however long it takes, and loses nothing.
func TestBlockingCloseStillWaits(t *testing.T) {
	sink := &gatedSink{gate: make(chan struct{})}
	w := newWriter(sink, 0)
	w.block = true
	w.stall = 100 * time.Millisecond
	const n = 100
	flood(w, n)

	closed := make(chan error, 1)
	go func() { closed <- w.Close() }()
	select {
	case err := <-closed:
		close(sink.gate)
		t.Fatalf("a blocking writer's Close returned (%v) while its disk was stalled", err)
	case <-time.After(5 * w.stall):
	}
	close(sink.gate)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return once the disk moved")
	}
	if got := strings.Count(sink.contents(), "\n"); got != n {
		t.Errorf("the file holds %d records, want all %d", got, n)
	}
	if lost := w.Dropped(); lost != 0 {
		t.Errorf("Dropped() = %d, want 0", lost)
	}
}

// trailerStallSink writes like chunkSink, except that the first write waits for first and a
// write carrying the trailing record waits for trailer.
type trailerStallSink struct {
	chunkSink
	first, trailer chan struct{}
	started        bool
}

func (s *trailerStallSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	start := !s.started
	s.started = true
	s.mu.Unlock()
	if start {
		<-s.first
	}
	if bytes.Contains(p, []byte(`"kind":"export_incomplete"`)) {
		<-s.trailer
	}
	return s.chunkSink.Write(p)
}

// TestTrailerWriteIsBounded: the drain finished, so the disk was moving, but the trailing
// record does not land. Close waits for it only briefly, and says it could not be written.
func TestTrailerWriteIsBounded(t *testing.T) {
	sink := &trailerStallSink{first: make(chan struct{}), trailer: make(chan struct{})}
	t.Cleanup(func() { close(sink.trailer) })
	w := newWriter(sink, 0)
	w.stall = 5 * time.Second
	w.trailerWait = 200 * time.Millisecond
	flood(w, 1000) // 2000 messages: some are dropped
	close(sink.first)

	start := time.Now()
	err := w.Close()
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("Close took %s waiting on the trailing record", took)
	}
	if err == nil || !strings.Contains(err.Error(), "export_incomplete record could not be written within") {
		t.Errorf("Close = %v, want it to say the trailing record could not be written", err)
	}
	if w.Dropped() == 0 {
		t.Error("nothing was lost, so there was no trailing record to wait for")
	}
}

// pending is how many half-records send is holding.
func pending(w *Writer) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pairs)
}

// TestAnUnansweredFlagNeverJoinsALaterOne: a flag whose answer never comes — a run that ended
// with it outstanding — leaves its entry behind until exit, and nothing else. Entries are per
// FLAG: 3 rounds of 100 templates flagged and never answered hold 300, and a later, answered
// flag of each template writes its own record without touching them. (Until #121 the entries
// were per template, and the newer flag replaced the older — the mechanism by which an answer
// still out when its template flagged again was written under the wrong flag.)
//
// The live bound is the flags in flight: every queued flag gets exactly one terminal answer
// while the run is live, so only a run's end leaves entries behind.
func TestAnUnansweredFlagNeverJoinsALaterOne(t *testing.T) {
	sink := &chunkSink{}
	w := newWriter(sink, 0)
	const templates = 100
	t2 := time.Date(2026, 8, 29, 11, 0, 2, 0, time.UTC)
	id := model.FlagID(0)
	for round := range 3 {
		for i := range templates {
			id++
			f := flagFor(fmt.Sprintf("t%03d", i))
			f.ID, f.Count = id, 1
			w.Flag(f) // answered never
		}
		if got, want := pending(w), (round+1)*templates; got != want {
			t.Fatalf("round %d: %d entries held, want %d, one per unanswered flag", round, got, want)
		}
	}
	for i := range templates {
		id++
		h := fmt.Sprintf("t%03d", i)
		f := flagFor(h)
		f.ID, f.Count = id, 2
		ex := doneFor(h)
		ex.Flag, ex.At = id, t2
		w.Flag(f)
		w.Resolve(ex)
	}
	if got := pending(w); got != 3*templates {
		t.Errorf("%d entries held after the answered round, want the %d unanswered flags", got, 3*templates)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs := decodeLines(t, sink.contents())
	if len(recs) != templates {
		t.Fatalf("%d records, want one per answered flag", len(recs))
	}
	for _, r := range recs {
		if r["count_at_flag"] != float64(2) || r["explanation"].(map[string]any)["at"] != t2.Format(time.RFC3339) {
			t.Errorf("a record joins an unanswered flag: %v", r)
		}
	}
	if lost := w.Dropped(); lost != 0 {
		t.Errorf("Dropped() = %d: an unanswered flag is not a lost record", lost)
	}
}

// TestALostFlagNeverTakesAnotherFlagsRecord is #70's accounting with two flags of one template
// in flight (#121). The first flag is queued; the queue is then full, and the second is lost.
// The second's answer must be dropped on arrival, as #70 drops the other half of any lost
// record — and the first flag's answer must still complete the first flag's record. Keyed by
// template hash, the first answer was taken for the other half of the LOST flag and dropped
// with it: the first record vanished without being counted.
func TestALostFlagNeverTakesAnotherFlagsRecord(t *testing.T) {
	w, sink, release := heldWriter(t)
	t1, t2 := time.Date(2026, 8, 29, 11, 0, 1, 0, time.UTC), time.Date(2026, 8, 29, 11, 0, 2, 0, time.UTC)
	f1, r1 := escalationOf(1, t1)
	f2, r2 := escalationOf(2, t2)
	f1.ID, r1.Flag = 1, 1
	f2.ID, r2.Flag = 2, 2
	w.Flag(f1) // queued
	fillQueue(w, queueCap-1)
	w.Flag(f2) // the queue is full: lost
	release()
	waitFor(t, "the queue to drain", func() bool { return len(w.in) == 0 })
	w.Resolve(r1)
	w.Resolve(r2) // the lost flag's other half: dropped on arrival
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := recordsFor(decodeLines(t, sink.contents()), "x")
	if len(got) != 1 {
		t.Fatalf("x has %d records, want 1 (the first flag's):\n%v", len(got), got)
	}
	if c, at := got[0]["count_at_flag"], got[0]["explanation"].(map[string]any)["at"]; c != float64(1) || at != t1.Format(time.RFC3339) {
		t.Errorf("x's record has count_at_flag %v and explanation.at %v; want 1 and %s, both from the first flag",
			c, at, t1.Format(time.RFC3339))
	}
	if lost := w.Dropped(); lost != 1 {
		t.Errorf("Dropped() = %d, want 1 record (the second flag's)", lost)
	}
}
