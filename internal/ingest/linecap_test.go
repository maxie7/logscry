// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
)

// repeatReader is an endless stream of one byte. A test feeds a line of any length through
// it without ever holding that line: the input is generated as it is read.
type repeatReader byte

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

// run returns n bytes of b, generated lazily.
func run(n int, b byte) io.Reader { return io.LimitReader(repeatReader(b), int64(n)) }

// text is a literal piece of input.
func text(s string) io.Reader { return strings.NewReader(s) }

// counted runs read with a Truncations in its context and returns what it emitted, the cut
// count, and how many times notify was called.
func counted(t *testing.T, read func(ctx context.Context, out chan<- model.LogLine) error) ([]model.LogLine, uint64, int) {
	t.Helper()
	var notified atomic.Int32
	tr := NewTruncations(func() { notified.Add(1) })
	got := collect(t, func(ctx context.Context, out chan<- model.LogLine) error {
		return read(WithTruncations(ctx, tr), out)
	})
	return got, tr.Count(), int(notified.Load())
}

// readCapped runs readLines over r with a Truncations in its context.
func readCapped(t *testing.T, r io.Reader) ([]model.LogLine, uint64) {
	t.Helper()
	got, n, _ := counted(t, func(ctx context.Context, out chan<- model.LogLine) error {
		return readLines(ctx, r, "test", model.Stdout, out)
	})
	return got, n
}

// sameRun reports whether s is exactly n copies of b, without building the expected string.
func sameRun(s string, n int, b byte) bool {
	return len(s) == n && strings.Trim(s, string(b)) == ""
}

// capLine is one expected line: as bytes of 'a', then tail. It describes a line of up to
// MaxLineBytes without building it.
type capLine struct {
	as   int
	tail string
}

// capCase is one input to readLines, the lines it must emit, and how many it must cut.
type capCase struct {
	input []io.Reader
	want  []capLine
	cut   uint64
}

// TestReadLinesCapsALine walks the edges of the cap: its exact size, one byte past it, a line
// ending straddling it, and a multi-byte character straddling it. The cap is on a line's
// TEXT: a "\r\n" past it is the line's ending, not more text, and a character is never split.
func TestReadLinesCapsALine(t *testing.T) {
	const c = MaxLineBytes
	cases := map[string]capCase{
		"exactly the cap": {
			input: []io.Reader{run(c, 'a'), text("\nnext\n")},
			want:  []capLine{{c, ""}, {0, "next"}},
		},
		"one byte past the cap": {
			input: []io.Reader{run(c+1, 'a'), text("\nnext\n")},
			want:  []capLine{{c, ""}, {0, "next"}},
			cut:   1,
		},
		"CRLF after exactly the cap": {
			input: []io.Reader{run(c, 'a'), text("\r\nnext\n")},
			want:  []capLine{{c, ""}, {0, "next"}},
		},
		"CRLF straddling the cap": {
			input: []io.Reader{run(c-1, 'a'), text("\r\nnext\n")},
			want:  []capLine{{c - 1, ""}, {0, "next"}},
		},
		"two CRs and a LF after the cap": {
			input: []io.Reader{run(c, 'a'), text("\r\r\nnext\n")},
			want:  []capLine{{c, ""}, {0, "next"}},
		},
		"a CR past the cap that text follows": {
			input: []io.Reader{run(c, 'a'), text("\rx\nnext\n")},
			want:  []capLine{{c, ""}, {0, "next"}},
			cut:   1,
		},
	}
	for _, r := range []string{"é", "€", "😀"} {
		// The character starts k bytes before the cap and runs past it: it is dropped whole.
		for k := 1; k < len(r); k++ {
			cases[fmt.Sprintf("%d-byte rune, %d of its bytes under the cap", len(r), k)] = capCase{
				input: []io.Reader{run(c-k, 'a'), text(r + "tail\nnext\n")},
				want:  []capLine{{c - k, ""}, {0, "next"}},
				cut:   1,
			}
		}
		// The character ends exactly at the cap: it is kept, and what follows is cut.
		cases[fmt.Sprintf("%d-byte rune ending at the cap", len(r))] = capCase{
			input: []io.Reader{run(c-len(r), 'a'), text(r + "tail\nnext\n")},
			want:  []capLine{{c - len(r), r}, {0, "next"}},
			cut:   1,
		}
		cases[fmt.Sprintf("%d-byte rune ending at the cap, then the newline", len(r))] = capCase{
			input: []io.Reader{run(c-len(r), 'a'), text(r + "\nnext\n")},
			want:  []capLine{{c - len(r), r}, {0, "next"}},
		}
	}

	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			got, cut := readCapped(t, io.MultiReader(tt.input...))
			if len(got) != len(tt.want) {
				t.Fatalf("got %d lines, want %d", len(got), len(tt.want))
			}
			for i, w := range tt.want {
				raw := got[i].Raw
				if len(raw) > MaxLineBytes {
					t.Errorf("line %d is %d bytes, over the %d-byte cap", i, len(raw), MaxLineBytes)
				}
				if !strings.HasSuffix(raw, w.tail) || !sameRun(raw[:len(raw)-len(w.tail)], w.as, 'a') {
					t.Errorf("line %d: %d bytes ending %q, want %d bytes of 'a' then %q",
						i, len(raw), raw[max(0, len(raw)-8):], w.as, w.tail)
				}
			}
			if cut != tt.cut {
				t.Errorf("Count() = %d, want %d", cut, tt.cut)
			}
		})
	}
}

// TestReadLinesHugeLineIsBoundedInMemory: a 64 MiB line costs the reader its fixed buffer and
// at most one cap's worth of kept text, not the line, and the line after it arrives intact.
//
// TotalAlloc is process-wide, so the measurement brackets only the readLines call, the test
// does not run in parallel, and the bound leaves room for whatever else allocates meanwhile:
// 16 MiB, where holding the line costs at least 128 MiB.
func TestReadLinesHugeLineIsBoundedInMemory(t *testing.T) {
	const huge = 64 << 20
	r := io.MultiReader(run(huge, 'a'), text("\nnext\n"))
	tr := NewTruncations(nil)
	ctx := WithTruncations(context.Background(), tr)
	out := make(chan model.LogLine, 4) // room for both lines: readLines runs on this goroutine

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := readLines(ctx, r, "test", model.Stdout, out)
	runtime.ReadMemStats(&after)
	close(out)

	if err != nil {
		t.Fatalf("readLines: %v", err)
	}
	delta := after.TotalAlloc - before.TotalAlloc
	t.Logf("allocated %d bytes reading a %d-byte line", delta, huge)
	if delta >= 16<<20 {
		t.Errorf("allocated %d MiB reading one %d MiB line, want under 16 MiB", delta>>20, huge>>20)
	}
	var got []string
	for ll := range out {
		got = append(got, ll.Raw)
	}
	if len(got) != 2 || !sameRun(got[0], MaxLineBytes, 'a') || got[1] != "next" {
		lens := make([]int, len(got))
		for i, s := range got {
			lens[i] = len(s)
		}
		t.Errorf("line lengths %v, want [%d 4] ending with %q", lens, MaxLineBytes, "next")
	}
	if tr.Count() != 1 {
		t.Errorf("Count() = %d, want 1", tr.Count())
	}
}

// TestReadLinesCapsALineAtEOF: a line over the cap that the stream ends without a newline is
// still emitted, cut, and the read ends cleanly.
func TestReadLinesCapsALineAtEOF(t *testing.T) {
	got, cut := readCapped(t, run(2*MaxLineBytes, 'a'))
	if len(got) != 1 || !sameRun(got[0].Raw, MaxLineBytes, 'a') {
		t.Fatalf("got %d lines (first %d bytes), want one of %d bytes", len(got), firstLen(got), MaxLineBytes)
	}
	if cut != 1 {
		t.Errorf("Count() = %d, want 1", cut)
	}
}

func firstLen(lines []model.LogLine) int {
	if len(lines) == 0 {
		return 0
	}
	return len(lines[0].Raw)
}

// cancelProbe is an endless line with no newline. It signals once the reader is past the cap,
// so the test can cancel during the discard. After the cancel it serves 8 MiB more and then
// ends, so a reader that ignores the cancel still finishes, quickly and in bounded memory.
type cancelProbe struct {
	served    atomic.Int64
	cancelAt  atomic.Int64 // served when the test cancelled; -1 before
	past      chan struct{}
	closePast sync.Once
}

const probeTail = 8 << 20

func newCancelProbe() *cancelProbe {
	p := &cancelProbe{past: make(chan struct{})}
	p.cancelAt.Store(-1)
	return p
}

func (p *cancelProbe) Read(b []byte) (int, error) {
	if at := p.cancelAt.Load(); at >= 0 && p.served.Load()-at >= probeTail {
		return 0, io.EOF
	}
	n, _ := repeatReader('a').Read(b)
	if p.served.Add(int64(n)) > MaxLineBytes+8<<10 {
		p.closePast.Do(func() { close(p.past) })
	}
	return n, nil
}

// TestReadLinesStopsDiscardingOnCancel: cancellation is honoured while a line is being
// discarded, not only between lines. The reader stops reading at once, rather than
// draining the rest of a line that may never end.
func TestReadLinesStopsDiscardingOnCancel(t *testing.T) {
	p := newCancelProbe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan model.LogLine, 4)
	done := make(chan error, 1)
	go func() { done <- readLines(ctx, p, "test", model.Stdout, out) }()

	select {
	case <-p.past:
	case <-time.After(2 * time.Second):
		t.Fatal("the reader never got past the cap")
	}
	p.cancelAt.Store(p.served.Load())
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("readLines = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readLines did not return within 2s of the cancel")
	}
	if after := p.served.Load() - p.cancelAt.Load(); after > 64<<10 {
		t.Errorf("read %d bytes after the cancel, want at most 64 KiB", after)
	}
	if len(out) != 0 {
		t.Errorf("emitted %d lines for a line cut short by the cancel, want 0", len(out))
	}
}

// stuckReader returns no bytes and no error, forever.
type stuckReader struct{}

func (stuckReader) Read([]byte) (int, error) { return 0, nil }

// TestReadLinesEndsOnAReaderThatMakesNoProgress: a reader that never yields a byte ends the
// read with bufio's io.ErrNoProgress instead of spinning.
func TestReadLinesEndsOnAReaderThatMakesNoProgress(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- readLines(context.Background(), stuckReader{}, "test", model.Stdout, make(chan model.LogLine, 1))
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrNoProgress) {
			t.Errorf("readLines = %v, want io.ErrNoProgress", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readLines spun on a reader that makes no progress")
	}
}

// TestTruncationsNotifyOnce: every cut is counted, from any number of readers at once, and
// notify is called for the first only.
func TestTruncationsNotifyOnce(t *testing.T) {
	var notified atomic.Int32
	tr := NewTruncations(func() { notified.Add(1) })
	ctx := WithTruncations(context.Background(), tr)

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := make(chan model.LogLine, 4)
			if err := readLines(ctx, io.MultiReader(run(MaxLineBytes+1, 'a'), text("\n")), "test", model.Stdout, out); err != nil {
				t.Errorf("readLines: %v", err)
			}
		}()
	}
	wg.Wait()
	if tr.Count() != 3 {
		t.Errorf("Count() = %d, want 3", tr.Count())
	}
	if n := notified.Load(); n != 1 {
		t.Errorf("notify called %d times, want 1", n)
	}
}

// TestTruncationsStopEndsNotification: after Stop, a cut is still counted but notify is not
// called. That is what lets the caller close the channel notify sends on, while a reader left
// running past a cancel may still be cutting a line.
func TestTruncationsStopEndsNotification(t *testing.T) {
	var notified atomic.Int32
	tr := NewTruncations(func() { notified.Add(1) })
	tr.Stop()
	out := make(chan model.LogLine, 4)
	if err := readLines(WithTruncations(context.Background(), tr),
		io.MultiReader(run(MaxLineBytes+1, 'a'), text("\n")), "test", model.Stdout, out); err != nil {
		t.Fatalf("readLines: %v", err)
	}
	if tr.Count() != 1 {
		t.Errorf("Count() = %d, want 1", tr.Count())
	}
	if n := notified.Load(); n != 0 {
		t.Errorf("notify called %d times after Stop, want 0", n)
	}
}

// TestStdinTruncatesALongLine: the stdin source keeps a long line's first MaxLineBytes, and
// the line after it arrives whole.
func TestStdinTruncatesALongLine(t *testing.T) {
	src := &StdinSource{r: io.MultiReader(run(2*MaxLineBytes, 'a'), text("\nnext\n"))}
	got, cut, _ := counted(t, src.Lines)
	if len(got) != 2 || !sameRun(got[0].Raw, MaxLineBytes, 'a') || got[1].Raw != "next" {
		t.Fatalf("got %d lines (first %d bytes), want %d bytes then %q", len(got), firstLen(got), MaxLineBytes, "next")
	}
	if cut != 1 {
		t.Errorf("Count() = %d, want 1", cut)
	}
}

// TestSubprocessTruncatesEachStream: stdout and stderr are read separately, so each is cut on
// its own and each cut is counted.
func TestSubprocessTruncatesEachStream(t *testing.T) {
	for _, tool := range []string{"sh", "head", "tr"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	const n = 3 * MaxLineBytes
	script := fmt.Sprintf(`head -c %d /dev/zero | tr '\000' a; echo; echo next; `+
		`head -c %d /dev/zero | tr '\000' b 1>&2; echo 1>&2`, n, n)
	got, cut, _ := counted(t, NewSubprocessSource([]string{"sh", "-c", script}).Lines)

	var stdout, stderr []string
	for _, ll := range got {
		if ll.Stream == model.Stderr {
			stderr = append(stderr, ll.Raw)
		} else {
			stdout = append(stdout, ll.Raw)
		}
	}
	if len(stdout) != 2 || !sameRun(stdout[0], MaxLineBytes, 'a') || stdout[1] != "next" {
		t.Errorf("stdout: %d lines, want %d bytes of 'a' then %q", len(stdout), MaxLineBytes, "next")
	}
	if len(stderr) != 1 || !sameRun(stderr[0], MaxLineBytes, 'b') {
		t.Errorf("stderr: %d lines, want one of %d bytes of 'b'", len(stderr), MaxLineBytes)
	}
	if cut != 2 {
		t.Errorf("Count() = %d, want 2", cut)
	}
}

// TestDockerTruncationKeepsTheTimestamp: Docker's timestamp is at the front of the line, so
// it survives the cut and is still parsed. It counts toward the cap: the message keeps what
// the cap leaves after it.
func TestDockerTruncationKeepsTheTimestamp(t *testing.T) {
	const stamp, next = "2026-03-04T05:06:07.000000001Z", "2026-03-04T05:06:08Z"
	var mark streamMark
	got, cut, _ := counted(t, func(ctx context.Context, out chan<- model.LogLine) error {
		r := io.MultiReader(text(stamp+" "), run(2*MaxLineBytes, 'a'), text("\n"+next+" next\n"))
		return readDockerStream(ctx, r, "docker:x", model.Stdout, &mark, out)
	})
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	want, _ := time.Parse(time.RFC3339Nano, stamp)
	if !got[0].Time.Equal(want) {
		t.Errorf("Time = %v, want the line's own stamp %v", got[0].Time, want)
	}
	if !sameRun(got[0].Raw, MaxLineBytes-len(stamp)-1, 'a') {
		t.Errorf("message is %d bytes, want %d of 'a'", len(got[0].Raw), MaxLineBytes-len(stamp)-1)
	}
	if got[1].Raw != "next" {
		t.Errorf("line after the cut = %q, want %q", got[1].Raw, "next")
	}
	if wantMark, _ := time.Parse(time.RFC3339Nano, next); !mark.at.Equal(wantMark) || mark.n != 1 {
		t.Errorf("mark = %v ×%d, want %v ×1", mark.at, mark.n, wantMark)
	}
	if cut != 1 {
		t.Errorf("Count() = %d, want 1", cut)
	}
}

// journalHead and journalTail wrap a run of 'a' into one journal entry, as journalctl -o json
// writes it. The values are invented.
const (
	journalHead = `{"MESSAGE":"`
	journalTail = `","PRIORITY":"3","_SYSTEMD_UNIT":"app.service","__REALTIME_TIMESTAMP":"1772600767000000"}`
	journalNext = `{"MESSAGE":"next","PRIORITY":"3","_SYSTEMD_UNIT":"app.service","__REALTIME_TIMESTAMP":"1772600768000000"}`
)

// TestJournaldTruncatedEntryPassesThrough: an entry cut at the cap is no longer JSON, so
// decode passes it through as it does any line it cannot decode: raw, on the journald source,
// with no level and its receipt time. The entry after it decodes as usual.
func TestJournaldTruncatedEntryPassesThrough(t *testing.T) {
	got, cut, _ := counted(t, func(ctx context.Context, out chan<- model.LogLine) error {
		r := io.MultiReader(text(journalHead), run(2*MaxLineBytes, 'a'), text(journalTail+"\n"+journalNext+"\n"))
		return mapLines(ctx, r, "journald", model.Stdout, out, decode)
	})
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	cutLine := got[0]
	if !strings.HasPrefix(cutLine.Raw, journalHead) || !sameRun(cutLine.Raw[len(journalHead):], MaxLineBytes-len(journalHead), 'a') {
		t.Errorf("cut entry is %d bytes, want the raw JSON's first %d", len(cutLine.Raw), MaxLineBytes)
	}
	if cutLine.Source != "journald" || cutLine.Level != "" || cutLine.Stream != model.Stdout {
		t.Errorf("cut entry tagged source=%q level=%q stream=%v, want journald, no level, stdout",
			cutLine.Source, cutLine.Level, cutLine.Stream)
	}
	if got[1].Raw != "next" || got[1].Source != "journald:app" || got[1].Level != "ERROR" {
		t.Errorf("entry after the cut = %q on %q at %q, want decoded", got[1].Raw, got[1].Source, got[1].Level)
	}
	if cut != 1 {
		t.Errorf("Count() = %d, want 1", cut)
	}
}

// writeLazyCapture streams parts into a capture file, so a long entry is never held whole.
func writeLazyCapture(t *testing.T, parts ...io.Reader) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(f, io.MultiReader(parts...)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

const journalFirst = `{"MESSAGE":"first","PRIORITY":"6","_SYSTEMD_UNIT":"app.service","__REALTIME_TIMESTAMP":"1772600766000000"}`

// TestReplayTruncatedEntryIsUntimed: in a replay, an entry cut at the cap no longer decodes,
// so it carries no timestamp. Past the first line it is replayed raw and counted untimed, and
// the entries around it are untouched.
func TestReplayTruncatedEntryIsUntimed(t *testing.T) {
	path := writeLazyCapture(t, text(journalFirst+"\n"+journalHead), run(2*MaxLineBytes, 'a'),
		text(journalTail+"\n"+journalNext+"\n"))
	src := NewReplaySource(path, 0)
	got, cut, _ := counted(t, src.Lines)
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3", len(got))
	}
	if got[0].Raw != "first" || got[2].Raw != "next" {
		t.Errorf("entries around the cut = %q, %q; want first, next", got[0].Raw, got[2].Raw)
	}
	if len(got[1].Raw) != MaxLineBytes || !strings.HasPrefix(got[1].Raw, journalHead) || !got[1].Time.IsZero() {
		t.Errorf("cut entry: %d bytes, time %v; want the raw JSON's first %d bytes and no time",
			len(got[1].Raw), got[1].Time, MaxLineBytes)
	}
	if src.Untimed() != 1 {
		t.Errorf("Untimed() = %d, want 1", src.Untimed())
	}
	if cut != 1 {
		t.Errorf("Count() = %d, want 1", cut)
	}
}

// TestReplayRefusesACaptureWhoseFirstEntryIsCut pins a known edge, unchanged by the cap: a
// first entry over the cap does not decode, so it has no timestamp, and replay refuses the
// whole capture the way it refuses one with no clock. The cut is counted, so the truncation
// notice is printed beside the refusal.
func TestReplayRefusesACaptureWhoseFirstEntryIsCut(t *testing.T) {
	path := writeLazyCapture(t, text(journalHead), run(2*MaxLineBytes, 'a'), text(journalTail+"\n"+journalNext+"\n"))
	var notified atomic.Int32
	tr := NewTruncations(func() { notified.Add(1) })
	out := make(chan model.LogLine, 4)
	err := NewReplaySource(path, 0).Lines(WithTruncations(context.Background(), tr), out)
	if !errors.Is(err, errUntimedCapture) {
		t.Errorf("Lines = %v, want errUntimedCapture", err)
	}
	if len(out) != 0 {
		t.Errorf("emitted %d lines before refusing", len(out))
	}
	if tr.Count() != 1 || notified.Load() != 1 {
		t.Errorf("Count() = %d, notify ×%d; want 1 and 1", tr.Count(), notified.Load())
	}
}

// cycleReader repeats one line forever.
type cycleReader struct {
	line []byte
	off  int
}

func (c *cycleReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		k := copy(p[n:], c.line[c.off:])
		n += k
		c.off = (c.off + k) % len(c.line)
	}
	return n, nil
}

// BenchmarkReadLines measures the reader on ordinary lines: one op is one line.
func BenchmarkReadLines(b *testing.B) {
	for _, size := range []int{64, 256, 1 << 10, 4 << 10, 16 << 10} {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			line := []byte(strings.Repeat("x", size) + "\n")
			r := io.LimitReader(&cycleReader{line: line}, int64(b.N)*int64(len(line)))
			out := make(chan model.LogLine, 1024)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for range out {
				}
			}()
			b.SetBytes(int64(len(line)))
			b.ReportAllocs()
			b.ResetTimer()
			if err := readLines(context.Background(), r, "bench", model.Stdout, out); err != nil {
				b.Fatal(err)
			}
			close(out)
			<-done
		})
	}
}
