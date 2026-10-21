// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/maxie7/logscry/internal/model"
)

// MaxLineBytes is the most of one line's text logscry keeps, not counting its line ending. A
// longer line keeps its first MaxLineBytes, less any character the cut would split, and the
// rest of it, up to its newline, is read and discarded without being held (#124).
//
// It is the coalescer's maxCoalesceBytes, so one physical line is never bigger than the unit
// everything downstream is bounded by. It is well above what the producers write: journald
// splits a stdout line at 48K, Docker splits a message at 16 KiB, and the longest of 70,783
// entries in real journal captures was 4.2 KiB. It is not larger because every line in flight
// may be held at this size — by the channels between the stages and by the stream ring — so
// the cap multiplies into what a crafted stream can make logscry hold.
//
// It is also bufio.Scanner's default limit. Scanner FAILS past it, and the stream ends; here
// the line is cut and the stream goes on.
const MaxLineBytes = 64 << 10

// Truncations counts the lines a run cut at MaxLineBytes, and calls notify on the first cut. A
// cut is data loss, so it is announced as it happens, not only counted at the end.
//
// It reaches the readers through the context (see WithTruncations), because Source.Lines takes
// nothing else per run: every source, and every constructor, stays as it was. A context
// carrying none counts nothing.
type Truncations struct {
	n      atomic.Uint64
	mu     sync.Mutex
	notify func()
	done   bool // notify has been called, or Stop has: it is not called again
}

// NewTruncations returns a counter that calls notify on the first cut. notify runs on the
// reading goroutine and must not block. nil notifies nothing.
func NewTruncations(notify func()) *Truncations { return &Truncations{notify: notify} }

type truncationsKey struct{}

// WithTruncations returns ctx carrying t, which readLines counts its cuts in.
func WithTruncations(ctx context.Context, t *Truncations) context.Context {
	return context.WithValue(ctx, truncationsKey{}, t)
}

// truncationsFrom returns the Truncations ctx carries, or nil, whose methods do nothing.
func truncationsFrom(ctx context.Context) *Truncations {
	t, _ := ctx.Value(truncationsKey{}).(*Truncations)
	return t
}

// Count reports how many lines have been cut so far.
func (t *Truncations) Count() uint64 {
	if t == nil {
		return 0
	}
	return t.n.Load()
}

// Stop ends notification: once it returns, notify is never called, though cuts are still
// counted. A reader can outlive the source that started it — mapLines returns on a cancel
// without waiting for its reader — so whatever notify sends on is closed only after Stop.
func (t *Truncations) Stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.done = true
	t.mu.Unlock()
}

// record counts one cut, and calls notify if it is the first and Stop has not been called.
func (t *Truncations) record() {
	if t == nil {
		return
	}
	t.n.Add(1)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	if t.notify != nil {
		t.notify()
	}
}

// readLines reads newline-delimited lines from r and emits one model.LogLine per
// line into out, tagging each with the given source and stream. It returns when r
// is exhausted (nil on clean EOF) or ctx is cancelled.
//
// A line's text is at most MaxLineBytes; a longer line is cut, and counted in the
// Truncations ctx carries. It reads through bufio.Reader rather than bufio.Scanner because
// Scanner ends the stream on a line past its limit.
func readLines(ctx context.Context, r io.Reader, source string, stream model.Stream, out chan<- model.LogLine) error {
	lr := lineReader{br: bufio.NewReader(r), trunc: truncationsFrom(ctx)}
	for {
		line, ok, err := lr.next(ctx)
		if ok {
			ll := model.LogLine{
				Time:   time.Now(),
				Source: source,
				Stream: stream,
				Raw:    string(line),
			}
			select {
			case out <- ll:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// lineReader reads lines through bufio's fixed buffer, keeping at most MaxLineBytes of each.
// Memory per stream is that buffer plus long, whatever the length of the lines.
type lineReader struct {
	br    *bufio.Reader
	long  []byte // the text of a line longer than the buffer; reused, never past MaxLineBytes
	trunc *Truncations
}

// next returns the next line's text without its ending, and whether there was a line at all:
// "\n" is an empty line, and nothing before EOF is none. The text is valid until the next call.
//
// A line past the cap is counted the moment its text crosses it, so a line that never ends is
// announced while it is being discarded. The discard checks ctx after each buffer, and a
// cancel there returns ctx's error and no line.
func (lr *lineReader) next(ctx context.Context) ([]byte, bool, error) {
	frag, err := lr.br.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) && len(frag) <= MaxLineBytes {
		// The common case: the whole line was in the buffer.
		return trimEnding(frag), len(frag) > 0, err
	}

	text, over := lr.long[:0], false
	for {
		rest := frag
		if err == nil {
			rest = rest[:len(rest)-1] // the '\n'
		}
		if room := MaxLineBytes - len(text); room > 0 {
			n := min(room, len(rest))
			text, rest = append(text, rest[:n]...), rest[n:]
		}
		// Past the cap, only a '\r' can still be the line's ending rather than its text.
		if !over && len(bytes.TrimLeft(rest, "\r")) > 0 {
			over = true
			lr.trunc.record()
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			break
		}
		if cerr := ctx.Err(); cerr != nil {
			lr.long = text
			return nil, false, cerr
		}
		frag, err = lr.br.ReadSlice('\n')
	}
	lr.long = text
	if over {
		return cutRune(text), true, err
	}
	return bytes.TrimRight(text, "\r"), true, err
}

// trimEnding drops a line's ending: its '\n', and any '\r' before it.
func trimEnding(b []byte) []byte {
	return bytes.TrimRight(bytes.TrimSuffix(b, []byte{'\n'}), "\r")
}

// cutRune drops the character a cut split, if there is one: the last character to start in
// b's final UTFMax−1 bytes, when b ends before that character does. Bytes that were invalid
// before the cut are left as they were.
func cutRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i > len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			return b
		}
	}
	return b
}

// mapLines reads lines from r via readLines and rewrites each one through fn before
// emitting it. It exists because readLines can only know what it is told — the source
// name, the stream, and the receipt time — while every source richer than "one line, one
// event" carries more than that INSIDE the line: a timestamp Docker prefixes, a unit and
// a priority journald encodes as JSON fields. fn is where each source decodes its own
// wire format; the read/emit/cancel loop below is identical for all of them and is
// written once here rather than per source.
//
// fn runs on the ingestion hot path, once per line, and must stay cheap: parse and
// rewrite, never block and never do I/O.
func mapLines(ctx context.Context, r io.Reader, source string, stream model.Stream,
	out chan<- model.LogLine, fn func(model.LogLine) model.LogLine,
) error {
	interim := make(chan model.LogLine)
	var readErr error
	go func() {
		defer close(interim)
		readErr = readLines(ctx, r, source, stream, interim)
	}()
	for ll := range interim {
		ll = fn(ll)
		select {
		case out <- ll:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return readErr
}
