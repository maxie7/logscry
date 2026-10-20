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
	// KindIncomplete is the last line of a live run that lost records (see Incomplete).
	KindIncomplete = "export_incomplete"
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

// How long a live writer's Close waits on a disk that has stopped (see Close). The stall is
// measured as time with NO progress, so a slow disk that keeps completing records is never cut
// off: ten seconds is about a thousand healthy fsyncs, and above the few-second stalls ext4
// writeback is known for. The trailing record gets less, because it is written straight after
// a drain that just proved the disk was moving, and it is one short line.
const (
	defaultStall       = 10 * time.Second
	defaultTrailerWait = 2 * time.Second
)

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
// fields without existence checks. Times are RFC 3339 in UTC (see record).
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

// Incomplete is the line a live writer appends on Close when it lost records, so that the file
// says it is incomplete to a consumer holding nothing else. It is its own kind rather than a
// zero-filled Record: an empty anomaly that a count of anomalies would include is worse than a
// line every `select(.kind == …)` already skips. The count is of RECORDS, the unit the rest of
// the file is in (#70); At is when the run closed, in UTC like every time in the file, which
// places the line among the runs appended to one file.
type Incomplete struct {
	Kind        string    `json:"kind"`
	RecordsLost uint64    `json:"records_lost"`
	At          time.Time `json:"at"`
}

// resolution is the terminal half of a record: how one flag ended. It is built on the
// caller's goroutine (the mapping is pure) so the writer goroutine only ever joins and
// writes.
type resolution struct {
	hash        string
	kind        string
	explanation Explanation
}

// msg is one item in the channel: a flag, or the resolution of one. seq names the record it
// is half of: both halves of one record carry the same seq, and no other message does.
type msg struct {
	flag   Flag
	res    resolution
	isFlag bool
	seq    uint64
}

// hash is the template the message is about.
func (m msg) hash() string {
	if m.isFlag {
		return m.flag.Hash
	}
	return m.res.hash
}

// pairState is what send knows about the record a template has in flight: the seq it was
// given, which half has gone through send, and whether that half was dropped.
type pairState struct {
	seq    uint64
	isFlag bool
	lost   bool
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
	// dropped counts RECORDS lost on the way to the file: a record is lost once, when either
	// of its halves cannot be queued, or when Close gives up with it still queued. Atomic
	// because the pipeline goroutine and the --plain consumer both send, and a renderer reads
	// it while the run is live.
	dropped atomic.Uint64
	// block makes send wait for room instead of dropping. Set only by OpenBlocking, before
	// any send, and never changed afterwards.
	block bool
	// stall and trailerWait bound Close on a live writer whose disk has stopped; see Close.
	// Set before any send, like block.
	stall       time.Duration
	trailerWait time.Duration

	// mu guards pairs and seq, send's account of each record's two halves. A flag and its
	// resolution arrive on different goroutines, so classifying a half and queueing it must be
	// one step. The live send never waits while holding it; the blocking one (replay) has a
	// single sender, the pipeline goroutine, so nothing waits behind it.
	//
	// pairs is keyed by template hash, so it holds at most one entry per template. An entry
	// lives from a record's first half to its second. While a run is live every flag gets
	// exactly one resolution: on the spot in a dry run, at once as "unavailable" when the LLM
	// queue is full, and otherwise the pool's one terminal answer, explained or failed (a
	// failed anonymization is a failure). So an entry is left behind only by a run that ends
	// with an answer outstanding, and the next half of the same side for that template
	// replaces it.
	mu    sync.Mutex
	pairs map[string]pairState
	seq   uint64
	// queued counts records whose two halves both went into the queue, which the writer
	// goroutine will therefore start writing unless Close gives up first.
	queued atomic.Uint64
	// progress moves each time the writer goroutine handles a message or finishes a write.
	// It is how Close tells a slow disk from a stopped one.
	progress atomic.Uint64
	// drained is closed once the writer has drained the queue at Close, and trailerDone once
	// it has written the trailing record, or found there was none to write.
	drained     chan struct{}
	trailerDone chan struct{}
	// gate guards started and gaveUp, so that Close giving up and the writer starting a
	// record cannot interleave: a record is either started, and may land, or counted lost.
	gate    sync.Mutex
	started uint64 // records the writer goroutine has started to write
	gaveUp  bool   // Close stopped waiting: write nothing more

	// Owned by the writer goroutine — do not touch from anywhere else.
	f       sink
	offset  int64          // file offset of the end of the last COMPLETE record
	flags   map[string]msg // flagged, awaiting a resolution
	orphans map[string]msg // resolved, awaiting its flag (see run)
	err     error          // first write error; read after <-finished
	broken  bool           // a partial line could not be rolled back: stop writing
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

// OpenBlocking is Open for a replay (--replay, issue #35): a full queue makes the sender WAIT
// for the disk instead of dropping the record.
//
// That trade is wrong for a live run and right for a replay, for the same reason. A live
// pipeline goroutine owns the template map and must never wait on a disk (RDI §3), so losing
// a record is the price of ingestion never stalling. A replay has no real-time contract to
// keep — it is reading a file — and its export is the artifact that gets diffed, so a record
// lost to a slow disk is a result that differs from run to run. Measured with the dropping
// writer: six virtual hours replayed at full speed wrote 548, 561 and 574 records in three
// runs of one input that escalated 3,609 times.
func OpenBlocking(path string) (*Writer, error) {
	w, err := Open(path)
	if err != nil {
		return nil, err
	}
	w.block = true
	return w, nil
}

// NewWriter is a live writer over s, a file whose last complete record ends at offset. It is
// Open with the file supplied, so that a test outside this package can put a disk that stalls
// under the real writer; s needs Write, Truncate, Sync and Close, as *os.File has them.
func NewWriter(s sink, offset int64) *Writer {
	return newWriter(s, offset)
}

// newWriter is Open's testable half: it takes the sink and the offset of the last complete
// record already in it.
func newWriter(f sink, offset int64) *Writer {
	w := &Writer{
		in:          make(chan msg, queueCap),
		quit:        make(chan struct{}),
		finished:    make(chan struct{}),
		drained:     make(chan struct{}),
		trailerDone: make(chan struct{}),
		stall:       defaultStall,
		trailerWait: defaultTrailerWait,
		pairs:       make(map[string]pairState),
		f:           f,
		offset:      offset,
		flags:       make(map[string]msg),
		orphans:     make(map[string]msg),
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

// Dropped reports how many RECORDS have been lost on the way to the file: dropped because the
// queue was full, or still queued when Close gave up on a stalled disk. It is meant to be
// surfaced, while the run is live and at its end: a tool whose job is to not miss anomalies
// must not quietly miss them on the way to disk. Safe to call at any time.
func (w *Writer) Dropped() uint64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

// Close drains what has already been queued, writes the trailing Incomplete record if any
// record was lost, syncs, and closes the file. It returns the first write error of the run,
// if any.
//
// A LIVE writer's Close is bounded, because a disk that has stopped would otherwise hang the
// exit with it. It gives up once the writer has made no progress for the stall (default 10 s),
// which a slow disk that keeps completing records never trips. Giving up counts every record
// still queued as lost, stops the writer writing anything more, and skips the trailing record
// on a disk that is not moving. The record being written at that moment is not counted: it
// may yet land, whole or cut short. The trailing record has a bound of its own (default 2 s),
// measured from the end of the drain. A BLOCKING writer (replay) waits however long the disk
// takes, because its promise is that it never drops (#35).
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
	if !w.block {
		if err := w.await(); err != nil {
			return err // the writer goroutine is stuck, so w.err is still its to write
		}
	}
	<-w.finished
	return w.err // the writer goroutine is done; <-finished orders its last write before this read
}

// await waits for the writer goroutine to finish, and gives up on a disk that has stopped:
// no progress for w.stall, or no trailing record within w.trailerWait of the drain.
func (w *Writer) await() error {
	tick := time.NewTicker(max(w.stall/10, time.Millisecond))
	defer tick.Stop()
	last, since := w.progress.Load(), time.Now()
	drained, trailerDone := w.drained, w.trailerDone
	var trailerTimeout <-chan time.Time
	for {
		select {
		case <-w.finished:
			return nil
		case <-drained:
			drained = nil
			trailer := time.NewTimer(w.trailerWait)
			defer trailer.Stop()
			trailerTimeout = trailer.C
		case <-trailerDone:
			trailerDone, trailerTimeout = nil, nil
			last, since = w.progress.Load(), time.Now()
		case <-trailerTimeout:
			w.giveUp()
			return fmt.Errorf("incomplete: the trailing %s record could not be written within %s",
				KindIncomplete, w.trailerWait)
		case now := <-tick.C:
			if p := w.progress.Load(); p != last {
				last, since = p, now
				continue
			}
			if trailerTimeout != nil || now.Sub(since) < w.stall {
				continue
			}
			return w.stalled(w.giveUp())
		}
	}
}

// giveUp stops the writer writing anything more and counts every record still queued as lost.
// It returns how many that was.
func (w *Writer) giveUp() uint64 {
	w.gate.Lock()
	defer w.gate.Unlock()
	w.gaveUp = true
	queued := w.queued.Load()
	if queued <= w.started {
		return 0
	}
	abandoned := queued - w.started
	w.dropped.Add(abandoned)
	return abandoned
}

// stalled is what Close reports when it gave up on a disk that made no progress.
func (w *Writer) stalled(abandoned uint64) error {
	msg := fmt.Sprintf("the export file accepted no write for %s; gave up", w.stall)
	if abandoned > 0 {
		msg += fmt.Sprintf(" with %d record(s) still queued", abandoned)
	}
	if w.dropped.Load() > 0 {
		msg = "incomplete: " + msg + fmt.Sprintf("; the trailing %s record could not be written", KindIncomplete)
	}
	return errors.New(msg)
}

// send hands one half of a record to the writer goroutine, DROPPING it if the channel is
// full, and counts what that costs in records.
//
// A record is lost once, when its first half is dropped, and its other half is then dropped
// on arrival even if there is room for it. Queueing that half is how a lost record used to
// corrupt another one: it waited in the writer, unmatched, and was joined to the NEXT
// escalation of the same template — one escalation's flag with another's explanation. When it
// is the second half that cannot be queued, the record is lost then; its first half is already
// in the writer, where its seq keeps it from joining anything else.
//
// The default case is the point: the loudest caller is the pipeline goroutine that owns the
// template map, and a blocking send would let a slow disk stall ingestion. It is the same
// trade the escalation channel and the cap-1 snapshot channel already make — except that a
// dropped record is a real loss, so it is counted and reported rather than shrugged off.
//
// A blocking writer (OpenBlocking) waits for room instead — but never past Close: once the
// writer has been told to stop, nothing will read the queue again, and a sender waiting on
// it would wait forever. That send is counted as a drop, which it is.
func (w *Writer) send(m msg) {
	hash := m.hash()
	w.mu.Lock()
	defer w.mu.Unlock()

	if p, ok := w.pairs[hash]; ok && p.isFlag != m.isFlag {
		// The second half of the record in flight for this template.
		delete(w.pairs, hash)
		if p.lost {
			return // the record is already counted lost; its other half must not be written
		}
		m.seq = p.seq
		if w.enqueue(m) {
			w.queued.Add(1)
		} else {
			w.dropped.Add(1)
		}
		return
	}
	// A first half. One of the same side still in flight for this template is replaced, as
	// the writer has always replaced it: the newer half is the one a resolution will answer.
	w.seq++
	m.seq = w.seq
	queued := w.enqueue(m)
	if !queued {
		w.dropped.Add(1)
	}
	w.pairs[hash] = pairState{seq: m.seq, isFlag: m.isFlag, lost: !queued}
}

// enqueue puts m on the channel, or reports that it could not: the channel was full, or, for a
// blocking writer, Close came first.
func (w *Writer) enqueue(m msg) bool {
	if w.block {
		select {
		case w.in <- m:
			return true
		case <-w.quit:
			return false
		}
	}
	select {
	case w.in <- m:
		return true
	default:
		return false
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
					close(w.drained)
					w.writeTrailer()
					close(w.trailerDone)
					w.closeFile()
					w.progress.Add(1)
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
// The halves join only when they carry the same seq, i.e. belong to one record. A waiting half
// with another seq is stale — its partner was dropped (see send) — and gives way.
//
// Both maps hold only unmatched halves, one per template at most. A flag left unmatched at
// shutdown is simply never written, which is correct: it never reached a terminal state.
func (w *Writer) handle(m msg) {
	defer w.progress.Add(1)
	hash := m.hash()
	waiting, other := w.orphans, w.flags
	if !m.isFlag {
		waiting, other = w.flags, w.orphans
	}
	if p, ok := waiting[hash]; ok {
		// Deleting is what makes a second terminal explanation for the same hash write
		// nothing. The next line for this template needs a new flag, i.e. a new escalation.
		delete(waiting, hash)
		if p.seq == m.seq {
			if m.isFlag {
				w.writeRecord(record(m.flag, p.res))
			} else {
				w.writeRecord(record(p.flag, m.res))
			}
			return
		}
	}
	other[hash] = m
}

// writeRecord writes one joined record, unless Close has given up on the disk, in which case
// the record was counted lost and must not appear.
func (w *Writer) writeRecord(r Record) {
	w.gate.Lock()
	if w.gaveUp {
		w.gate.Unlock()
		return
	}
	w.started++
	w.gate.Unlock()
	w.write(r)
}

// writeTrailer appends the Incomplete record when records were lost and Close has not given up.
func (w *Writer) writeTrailer() {
	n := w.dropped.Load()
	if n == 0 {
		return
	}
	w.gate.Lock()
	gaveUp := w.gaveUp
	w.gate.Unlock()
	if gaveUp {
		return
	}
	w.write(Incomplete{Kind: KindIncomplete, RecordsLost: n, At: time.Now().UTC()})
}

// write appends one line, or leaves the file exactly as it found it.
func (w *Writer) write(r any) {
	defer w.progress.Add(1)
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
//
// It is also where every time in the file is put into UTC, and the only place (#71). What
// arrives is in time.Local — journald's timestamps and the pipeline's clock both are — and
// encoding/json spells a time in its own zone, so one instant read "10:40:00.5Z" on one
// machine and "19:40:00.5+09:00" on another, and a replay export diffed differently across
// zones. The instant is untouched; only its spelling is fixed, here at the file boundary,
// so the TUI and --plain keep showing local time.
func record(f Flag, res resolution) Record {
	reasons := f.Reasons
	if reasons == nil {
		reasons = []string{} // an empty list, never a null: consumers index this
	}
	ex := res.explanation
	ex.At = ex.At.UTC()
	return Record{
		Kind:           res.kind,
		TemplateHash:   f.Hash,
		Pattern:        f.Pattern,
		Level:          f.Level,
		Source:         f.Source,
		CountAtFlag:    f.Count,
		FirstSeen:      f.FirstSeen.UTC(),
		LastSeenAtFlag: f.LastSeen.UTC(),
		Score:          f.Score,
		Reasons:        reasons,
		Explanation:    ex,
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
func encode(r any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil { // Encode appends the newline
		return nil, err
	}
	return buf.Bytes(), nil
}
