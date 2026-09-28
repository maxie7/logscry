// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// ReplaySource plays back a recorded `journalctl -o json` capture (--replay, issue #35).
//
// It is the journald source reading a file instead of a subprocess: the same mapLines loop,
// the same decode, so PRIORITY becomes the level (#24), error-class priorities are tagged
// stderr, and each line is named for its unit — a replay that bypassed decode would be
// replaying a different program. What it adds is the one thing a file has that a live
// stream does not: every line's time is already written down, and replay is clocked by it
// (see pipeline.Options.SourceTime). The file reader is not the feature; the clock is.
//
// Speed only paces the hand-over. At 0 ("max") lines go out as fast as the pipeline takes
// them; at N they are spaced N times faster than they were recorded. Scoring never sees the
// wall clock in a replay, so every speed produces the same answer.
type ReplaySource struct {
	path  string
	speed float64 // multiple of real time; 0 means no pacing at all

	// Counted on the Lines goroutine and read by the caller only after Lines has returned.
	untimed   int
	backwards int
}

// NewReplaySource returns a Source that replays the capture at path. speed is a multiple of
// real time, or 0 for as fast as possible.
func NewReplaySource(path string, speed float64) *ReplaySource {
	return &ReplaySource{path: path, speed: speed}
}

// Name implements Source. Lines are renamed journald:<unit> by decode, exactly as live.
func (s *ReplaySource) Name() string { return "replay" }

// Untimed reports how many lines after the first carried no __REALTIME_TIMESTAMP. They were
// replayed at the capture's current time rather than dropped. Valid once Lines has returned.
func (s *ReplaySource) Untimed() int { return s.untimed }

// Backwards reports how many lines were stamped earlier than a line before them. They were
// replayed, and the replay clock held rather than rewinding. Valid once Lines has returned.
func (s *ReplaySource) Backwards() int { return s.backwards }

// errUntimedCapture is what replaying a file with no clock in it gets. Refusing is the
// point: such a file replays inside one instant, which is exactly the silent failure piping
// a capture through stdin already produces — measured, zero escalations where the capture
// holds four.
var errUntimedCapture = errors.New("the first entry carries no __REALTIME_TIMESTAMP: replay needs a " +
	"capture from `journalctl -o json`, whose entries record their own time")

// Lines implements Source: it decodes the capture, checks the first entry has a time,
// counts the entries whose time could not be followed, paces them, and emits them. It
// returns nil at the end of the file or on cancellation.
func (s *ReplaySource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	f, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	defer func() { _ = f.Close() }()

	readCtx, stopReading := context.WithCancel(ctx)
	decoded := make(chan model.LogLine)
	var readErr error
	go func() {
		defer close(decoded)
		readErr = mapLines(readCtx, f, s.Name(), model.Stdout, decoded, replayDecode)
	}()
	// stop ends the reader and waits for it, so the file is not closed underneath it.
	stop := func() {
		stopReading()
		for range decoded {
		}
	}

	var (
		first       = true
		latest      time.Time // the newest timestamp so far: the capture's clock
		wall0, cap0 time.Time // where pacing measures from
	)
	for ll := range decoded {
		if ll.Raw == "" {
			continue // not a journal entry; journalctl never writes one
		}
		if first {
			if ll.Time.IsZero() {
				stop()
				return fmt.Errorf("replay %s: %w", s.path, errUntimedCapture)
			}
			first = false
			wall0, cap0 = time.Now(), ll.Time
		}
		switch {
		case ll.Time.IsZero():
			s.untimed++
		case ll.Time.Before(latest):
			s.backwards++
		default:
			latest = ll.Time
		}
		if s.speed > 0 && !ll.Time.IsZero() {
			due := wall0.Add(time.Duration(float64(ll.Time.Sub(cap0)) / s.speed))
			if !sleepUntil(ctx, due) {
				stop()
				return nil
			}
		}
		select {
		case out <- ll:
		case <-ctx.Done():
			stop()
			return nil
		}
	}
	if ctx.Err() != nil {
		return nil // a clean stop, as for every other source
	}
	return readErr
}

// replayDecode is decode with the receipt time taken off first. readLines stamps every line
// with the wall clock as it reads it, and decode keeps that stamp when an entry has no
// timestamp of its own — right for a live source, and in a replay it would put today's date
// in the middle of last week's capture. With it zeroed, "no timestamp" stays visible: the
// replay clock holds, and the line is counted.
func replayDecode(ll model.LogLine) model.LogLine {
	ll.Time = time.Time{}
	return decode(ll)
}

// sleepUntil waits until t, reporting false if ctx was cancelled first. Pacing must never be
// the reason Ctrl-C feels slow.
func sleepUntil(ctx context.Context, t time.Time) bool {
	d := time.Until(t)
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
