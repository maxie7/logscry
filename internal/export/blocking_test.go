// SPDX-License-Identifier: Apache-2.0

package export

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// slowSink is a disk that takes its time over every write — the fsync the writer does per
// record, made visible. Everything else is chunkSink's in-memory file.
type slowSink struct {
	chunkSink
	delay time.Duration
}

func (s *slowSink) Write(p []byte) (int, error) {
	time.Sleep(s.delay)
	return s.chunkSink.Write(p)
}

// flood sends n complete records (flag + dry-run resolution) as fast as the caller can,
// which is how the pipeline goroutine sends them during a replay at max speed.
func flood(w *Writer, n int) {
	for i := range n {
		h := fmt.Sprintf("h%05d", i)
		w.Flag(flagFor(h))
		w.WouldEscalate(h, time.Date(2026, 8, 29, 10, 40, 0, 0, time.UTC))
	}
}

// TestBlockingWriterNeverDrops: in replay the file is the artifact that gets diffed, so it
// must hold every record the run produced, however fast the run produced them. Measured
// before this existed: six virtual hours replayed into the non-blocking writer produced
// 548, 561 and 574 records across three runs of the same input — and 3,609 escalations.
func TestBlockingWriterNeverDrops(t *testing.T) {
	sink := &slowSink{delay: 50 * time.Microsecond}
	w := newWriter(sink, 0)
	w.block = true
	const n = 3000 // 6000 messages: far past the 1024-slot queue
	flood(w, n)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if d := w.Dropped(); d != 0 {
		t.Fatalf("a blocking writer dropped %d messages", d)
	}
	if got := strings.Count(sink.contents(), "\n"); got != n {
		t.Fatalf("file holds %d records, want %d", got, n)
	}
}

// TestDefaultWriterStillDrops pins the live contract the blocking mode must not leak into:
// a live pipeline goroutine owns the template map and must never wait on a disk (RDI §3),
// so the default writer drops and counts rather than blocking.
func TestDefaultWriterStillDrops(t *testing.T) {
	sink := &slowSink{delay: 200 * time.Microsecond}
	w := newWriter(sink, 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		flood(w, 3000)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the default writer blocked its sender on a slow disk")
	}
	_ = w.Close()
	if w.Dropped() == 0 {
		t.Fatal("the default writer absorbed 6000 messages into a 1024-slot queue without dropping: it must have blocked")
	}
}

// TestBlockedSendReleasedByClose: a sender blocked on a full queue must not outlive the
// run. Close stops the writer, and the blocked send gives up (counted as a drop) instead
// of waiting forever on a goroutine that will never read again.
func TestBlockedSendReleasedByClose(t *testing.T) {
	gate := make(chan struct{})
	sink := &gatedSink{gate: gate}
	w := newWriter(sink, 0)
	w.block = true

	var wg sync.WaitGroup
	wg.Go(func() { flood(w, 2000) }) // blocks once the queue is full and the sink is stuck

	time.Sleep(50 * time.Millisecond)
	closed := make(chan struct{})
	go func() {
		_ = w.Close()
		close(closed)
	}()
	close(gate) // let the one write in progress finish so the writer can see quit
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("a sender blocked on the queue outlived Close")
	}
}

// gatedSink blocks every write until gate is closed.
type gatedSink struct {
	chunkSink
	gate chan struct{}
}

func (g *gatedSink) Write(p []byte) (int, error) {
	<-g.gate
	return g.chunkSink.Write(p)
}

// TestOpenBlockingOpensABlockingWriter: the constructor replay uses is the one that
// blocks, and Open — what every live run uses — is not.
func TestOpenBlockingOpensABlockingWriter(t *testing.T) {
	dir := t.TempDir()
	live, err := Open(dir + "/live.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close() }()
	replay, err := OpenBlocking(dir + "/replay.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()
	if live.block || !replay.block {
		t.Fatalf("Open blocking=%v, OpenBlocking blocking=%v; want false, true", live.block, replay.block)
	}
}
