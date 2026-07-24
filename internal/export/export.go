// SPDX-License-Identifier: Apache-2.0

// Package export appends flagged anomalies to a JSONL file — one JSON object per line,
// one line per flagged event. It is the only persistence logscry has, and the RDI (§2)
// allows exactly this much: "an optional JSONL dump of flagged events".
//
// The point is scriptability. Until this existed an anomaly was pixels — a TUI card or a
// --plain line — and nothing else could consume it. A file of self-contained JSON lines
// can be greped, jq'd, diffed between threshold settings, and failed on in CI.
//
// Two properties shape the whole package:
//
//   - A record is one POINT-IN-TIME snapshot of one flagged event. Everything template-
//     derived (count, last seen, score, reasons) is captured at the instant the anomaly
//     crossed the threshold, not when the model answered seconds later. An anomaly IS the
//     event "this template escalated"; the explanation explains that event, not the state
//     of the world when it finished being explained. Welding the two instants together
//     would produce a line that looks like one snapshot and is not.
//   - Nothing here blocks its caller. The file is owned by a single goroutine reached over
//     a buffered channel, because the loudest caller is the pipeline goroutine that owns
//     the template map, and it must never wait on a disk (the same rule that keeps the LLM
//     call off it, RDI §3).
package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// Kinds of record. The discriminator exists because --explain-dry-run writes lines too:
// it flags what WOULD escalate without calling a model, so those records carry no
// explanation at all. A consumer reading a mixed file must never mistake one for an
// explained anomaly, and `jq 'select(.kind=="anomaly")'` is the whole answer.
const (
	// KindAnomaly is a flagged event that reached a terminal explanation state.
	KindAnomaly = "anomaly"
	// KindWouldEscalate is a flagged event from a run with no LLM stage attached.
	KindWouldEscalate = "would_escalate"
)

// Explanation states, as they appear in the file. ExplainPending has no spelling here on
// purpose: a partial answer never produces a line (see Resolve).
const (
	stateExplained    = "explained"
	stateUnavailable  = "unavailable"
	stateNotRequested = "not_requested"
)

// queueCap bounds the channel into the writer goroutine. Escalations are rate-limited to
// ten a minute by default, so this is several hours of anomalies of headroom in front of
// a disk — it exists so that a stalled filesystem cannot reach back and stall ingestion,
// not because a backlog is expected.
const queueCap = 1024

// Flag is an anomaly's state at the instant it was flagged: the moment the scorer decided
// this template had crossed the threshold. It is one half of a record; the other half is
// how the flag ended (see Resolve, WouldEscalate).
type Flag struct {
	Hash      string
	Pattern   string
	Level     string
	Source    string
	Count     int
	FirstSeen time.Time
	LastSeen  time.Time
	Score     float64
	Reasons   []string
}

// Record is one line of the file. The json tags are a stable contract documented in the
// README — snake_case, every key always present, no omitempty, so a consumer can index
// fields without existence checks.
//
// The _at_flag suffixes are deliberate. A bare "count" reads as a running total, and this
// one is not: a re-occurrence bumps the in-memory count and appends nothing, so the number
// here is what it was when the anomaly fired.
type Record struct {
	Kind           string      `json:"kind"`
	TemplateHash   string      `json:"template_hash"`
	Pattern        string      `json:"pattern"`
	Level          string      `json:"level"`
	Source         string      `json:"source"`
	CountAtFlag    int         `json:"count_at_flag"`
	FirstSeen      time.Time   `json:"first_seen"`
	LastSeenAtFlag time.Time   `json:"last_seen_at_flag"`
	Score          float64     `json:"score"`
	Reasons        []string    `json:"reasons"`
	Explanation    Explanation `json:"explanation"`
}

// Explanation is the model's verdict as the file carries it.
type Explanation struct {
	State       string `json:"state"`
	Summary     string `json:"summary"`
	LikelyCause string `json:"likely_cause"`
	Suggestion  string `json:"suggestion"`
	// Truncated marks an answer salvaged from a stream that ended early: the fields are
	// genuine but short. It is the "incomplete" case, and a consumer that acts on advice
	// must be able to tell it from a finished one.
	Truncated bool `json:"truncated"`
	// Error is why State is "unavailable" — a model that was down, a full escalation
	// queue, a failed anonymization. Empty otherwise.
	Error string    `json:"error"`
	At    time.Time `json:"at"`
}

// resolution is the terminal half of a record: how one flag ended. It is built on the
// caller's goroutine (the mapping is pure) so the writer goroutine only ever joins and
// writes.
type resolution struct {
	hash        string
	kind        string
	explanation Explanation
}

// msg is one item in the channel: a flag, or the resolution of one.
type msg struct {
	flag   Flag
	res    resolution
	isFlag bool
}

// sink is the file as the writer goroutine uses it. *os.File satisfies it; the interface
// exists so a test can inject a writer that short-writes or fails, which is the only way
// to prove the line-integrity guarantee rather than assume it (see writeAll).
type sink interface {
	io.Writer
	Truncate(size int64) error
	Sync() error
	Close() error
}

// Writer appends records to a JSONL file. A nil *Writer is a working no-op — "export is
// disabled" is the default, and making it inert here means no call site needs a branch.
//
// The exported methods are safe to call from any goroutine. Everything below the channel
// is owned by the single writer goroutine, so there is no lock anywhere.
type Writer struct {
	in       chan msg
	quit     chan struct{}
	finished chan struct{}
	closing  sync.Once
	// dropped counts records that never reached the writer goroutine because the channel
	// was full. Atomic because both the pipeline goroutine and the --plain consumer send;
	// read once, after the goroutine has finished.
	dropped atomic.Uint64

	// Owned by the writer goroutine — do not touch from anywhere else.
	f       sink
	offset  int64                 // file offset of the end of the last COMPLETE record
	flags   map[string]Flag       // flagged, awaiting a resolution
	orphans map[string]resolution // resolved, awaiting its flag (see run)
	err     error                 // first write error; read after <-finished
	broken  bool                  // a partial line could not be rolled back: stop writing
}

// Open opens path for appending and starts the writer goroutine. An existing file is added
// to, never truncated. It fails fast on an unwritable path, so a mistyped --export is a
// startup error rather than a discovery at the end of a long run.
func Open(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return newWriter(f, st.Size()), nil
}

// newWriter is Open's testable half: it takes the sink and the offset of the last complete
// record already in it.
func newWriter(f sink, offset int64) *Writer {
	w := &Writer{
		in:       make(chan msg, queueCap),
		quit:     make(chan struct{}),
		finished: make(chan struct{}),
		f:        f,
		offset:   offset,
		flags:    make(map[string]Flag),
		orphans:  make(map[string]resolution),
	}
	go w.run()
	return w
}

// Flag records that an anomaly fired. Nothing is written yet: a record needs to know how
// the flag ended.
func (w *Writer) Flag(f Flag) {
	if w == nil {
		return
	}
	// The scorer allocates Reasons once and hands the same slice to the renderer. Copying
	// it keeps the writer goroutine off memory another goroutine holds — an allocation per
	// anomaly, which is to say ten a minute.
	f.Reasons = append([]string(nil), f.Reasons...)
	w.send(msg{flag: f, isFlag: true})
}

// Resolve records that a flagged anomaly reached a terminal explanation state, which is
// what completes its record.
//
// A PENDING explanation is dropped on the floor here. That is the streaming guarantee: with
// --llm-stream an answer arrives as several progressive updates before its terminal one,
// and the file must carry exactly one line per anomaly, not one per partial. --plain
// suppresses partials the same way and for the same reason (a line-oriented consumer cannot
// rewrite a line it has printed); a file cannot rewrite one either.
func (w *Writer) Resolve(ex model.Explanation) {
	if w == nil || ex.State == model.ExplainPending {
		return
	}
	w.send(msg{res: resolution{hash: ex.Hash, kind: KindAnomaly, explanation: explanationOf(ex)}})
}

// WouldEscalate resolves a flag in a run with no LLM stage attached (--explain-dry-run),
// where no explanation is ever coming and the flag is therefore terminal on arrival.
//
// Dry-run is the calibration and CI mode: it exists to show what WOULD fire, and a file of
// those is the artifact that makes a threshold change diffable. The record is marked
// unmistakably — kind "would_escalate", state "not_requested" — because it has no summary,
// cause, or suggestion, and must never be read as an explained anomaly.
func (w *Writer) WouldEscalate(hash string, at time.Time) {
	if w == nil {
		return
	}
	w.send(msg{res: resolution{
		hash:        hash,
		kind:        KindWouldEscalate,
		explanation: Explanation{State: stateNotRequested, At: at},
	}})
}

// Dropped reports how many records never reached the writer goroutine because the channel
// was full. It is meant to be surfaced: a tool whose job is to not miss anomalies must not
// quietly miss them on the way to disk.
func (w *Writer) Dropped() uint64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

// Close drains what has already been queued, syncs, and closes the file. It returns the
// first write error of the run, if any.
//
// Note what it does NOT do: close the message channel. A pipeline goroutine can outlive the
// run that Closed this writer (Ctrl-C cancels the context and main returns while it is
// still mid-loop), and a send on a closed channel is a panic. Sending into a channel nobody
// reads is merely a drop, so the quit signal is a separate channel and `in` is never closed.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.closing.Do(func() { close(w.quit) })
	<-w.finished
	return w.err // the writer goroutine is done; <-finished orders its last write before this read
}

// send hands a message to the writer goroutine, DROPPING it if the channel is full.
//
// The default case is the point: the loudest caller is the pipeline goroutine that owns the
// template map, and a blocking send would let a slow disk stall ingestion. It is the same
// trade the escalation channel and the cap-1 snapshot channel already make — except that a
// dropped record is a real loss, so it is counted and reported rather than shrugged off.
func (w *Writer) send(m msg) {
	select {
	case w.in <- m:
	default:
		w.dropped.Add(1)
	}
}

// run is the writer goroutine: it owns the file, the join maps, and every write.
func (w *Writer) run() {
	defer close(w.finished)
	for {
		select {
		case m := <-w.in:
			w.handle(m)
		case <-w.quit:
			// Drain what is already queued — those records are from the run that just
			// ended and belong in the file — then stop. Anything sent after this point is
			// from a goroutine outliving the run, and has nowhere to go.
			for {
				select {
				case m := <-w.in:
					w.handle(m)
				default:
					w.closeFile()
					return
				}
			}
		}
	}
}

// handle joins the two halves of a record and writes it when both are in, in EITHER order.
//
// The order matters more than it looks. The scorer hands the escalation to the LLM pool
// from inside Evaluate — before the pipeline gets as far as calling Flag — so a fast
// backend can have its answer back, and Resolve called on another goroutine, while the flag
// is still in flight. Joining on whichever half arrives second removes the assumption
// entirely.
//
// Both maps hold only unmatched halves, so they are bounded by the escalations in flight.
// A flag left unmatched at shutdown is simply never written, which is correct: it never
// reached a terminal state.
func (w *Writer) handle(m msg) {
	if m.isFlag {
		if res, ok := w.orphans[m.flag.Hash]; ok {
			delete(w.orphans, m.flag.Hash)
			w.write(record(m.flag, res))
			return
		}
		w.flags[m.flag.Hash] = m.flag
		return
	}
	if f, ok := w.flags[m.res.hash]; ok {
		// Deleting is what makes a second terminal explanation for the same hash write
		// nothing. The next line for this template needs a new flag, i.e. a new escalation.
		delete(w.flags, m.res.hash)
		w.write(record(f, m.res))
		return
	}
	w.orphans[m.res.hash] = m.res
}

// write appends one record, or leaves the file exactly as it found it.
func (w *Writer) write(r Record) {
	if w.broken {
		return
	}
	b, err := encode(r)
	if err != nil {
		w.note(fmt.Errorf("encode record: %w", err))
		return // nothing was written; the file is untouched
	}
	if err := w.writeAll(b); err != nil {
		w.rollback(err)
		return
	}
	w.offset += int64(len(b))
	if err := w.f.Sync(); err != nil {
		// The record is complete and the file is valid; it may just not have reached the
		// platter. Worth reporting, not worth discarding anything over.
		w.note(fmt.Errorf("sync: %w", err))
	}
}

// writeAll writes b in full, looping over short writes.
//
// A single Write is NOT atomic on a regular file: write(2) may return a short count, and
// os.File.Write reports that as io.ErrShortWrite — with the bytes it did write already on
// disk. One write per record prevents records from interleaving; it does nothing for the
// integrity of one record. This loop is what does: as long as the last call made progress,
// the remainder goes out. It gives up on a real error, and on no progress at all rather
// than spinning forever.
func (w *Writer) writeAll(b []byte) error {
	for len(b) > 0 {
		n, err := w.f.Write(b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			if errors.Is(err, io.ErrShortWrite) && n > 0 {
				continue // progress was made: write the rest
			}
			return err
		}
		if n == 0 {
			return io.ErrShortWrite // no error and no progress: a writer we cannot make headway with
		}
	}
	return nil
}

// rollback removes a half-written record, so the file always ends on a record boundary and
// every line in it still parses. The record itself is lost — one line is a cheaper price
// than a file a consumer chokes on.
//
// If the rollback ITSELF fails, the writer stops for good: appending after a partial line
// would fuse the two into one unparseable line, and losing the rest of the file is worse
// than losing the rest of the records.
func (w *Writer) rollback(cause error) {
	w.note(cause)
	if err := w.f.Truncate(w.offset); err != nil {
		w.note(fmt.Errorf("could not roll back a partial record, no further records will be written: %w", err))
		w.broken = true
	}
}

// note keeps the first error of the run. The first one is the one that explains the rest.
func (w *Writer) note(err error) {
	if w.err == nil {
		w.err = err
	}
}

func (w *Writer) closeFile() {
	if err := w.f.Sync(); err != nil {
		w.note(fmt.Errorf("sync: %w", err))
	}
	if err := w.f.Close(); err != nil {
		w.note(fmt.Errorf("close: %w", err))
	}
}

// record joins a flag and its resolution into the line that gets written.
func record(f Flag, res resolution) Record {
	reasons := f.Reasons
	if reasons == nil {
		reasons = []string{} // an empty list, never a null: consumers index this
	}
	return Record{
		Kind:           res.kind,
		TemplateHash:   f.Hash,
		Pattern:        f.Pattern,
		Level:          f.Level,
		Source:         f.Source,
		CountAtFlag:    f.Count,
		FirstSeen:      f.FirstSeen,
		LastSeenAtFlag: f.LastSeen,
		Score:          f.Score,
		Reasons:        reasons,
		Explanation:    res.explanation,
	}
}

// explanationOf maps the pipeline's explanation onto the file's schema.
//
// The values are the REAL ones. --llm-anonymize masks at the LLM boundary and restores the
// answer on the way back, so what arrives here already has the real hosts and addresses in
// it — and this file is local, exactly like the terminal that shows the same text.
func explanationOf(ex model.Explanation) Explanation {
	out := Explanation{
		Summary:     ex.Summary,
		LikelyCause: ex.LikelyCause,
		Suggestion:  ex.Suggestion,
		Truncated:   ex.Truncated,
		Error:       ex.Err,
		At:          ex.At,
	}
	switch ex.State {
	case model.ExplainFailed:
		out.State = stateUnavailable
	case model.ExplainDone, model.ExplainPending:
		// Pending never reaches here (Resolve drops it); Done is the ordinary case.
		out.State = stateExplained
	}
	return out
}

// encode marshals one record to the bytes that go to disk, newline included, so the write
// path has a single complete buffer to get onto the file rather than pieces to assemble
// there.
//
// SetEscapeHTML(false) is not cosmetic. Go's default marshalling escapes the angle brackets
// to their \u003c / \u003e form, which would leave every masked pattern in the file spelled
// as "connection refused to \u003cIP\u003e" — valid JSON, but it would silently break the
// greps this file exists to serve, including the one that checks that an anonymization
// placeholder never leaked into it.
func encode(r Record) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil { // Encode appends the newline
		return nil, err
	}
	return buf.Bytes(), nil
}
