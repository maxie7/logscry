// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// chunkReader hands out a stream in exactly the slices it was given, so a test can put a
// read boundary anywhere it likes — including in the middle of a JSON token. This is the
// point of the SSE tests: real streams split where the network decides, not where the
// framing would be convenient.
type chunkReader struct {
	chunks []string
	i      int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.i])
	if n < len(r.chunks[r.i]) {
		r.chunks[r.i] = r.chunks[r.i][n:]
		return n, nil
	}
	r.i++
	return n, nil
}

// collect runs readSSE over chunks and returns the payloads it dispatched.
func collect(t *testing.T, chunks []string, limit int64) ([]string, error) {
	t.Helper()
	var got []string
	finished := false
	err := readSSE(&chunkReader{chunks: chunks}, limit,
		func() bool { return finished },
		func(data []byte) {
			s := string(data)
			if strings.Contains(s, `"finish_reason":"stop"`) {
				finished = true
			}
			got = append(got, s)
		})
	return got, err
}

const bigLimit = 1 << 20

// TestSSEChunkingIsInvisible: the same stream split five different hostile ways must yield
// the same payloads. A boundary mid-JSON-token, several events in one read, and a line
// dribbled out a byte at a time all have to behave identically.
func TestSSEChunkingIsInvisible(t *testing.T) {
	stream := "data: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n"
	want := []string{`{"a":1}`, `{"b":2}`}

	byteAtATime := make([]string, 0, len(stream))
	for _, c := range []byte(stream) {
		byteAtATime = append(byteAtATime, string(c))
	}

	splits := map[string][]string{
		"one read":         {stream},
		"mid-JSON-token":   {"data: {\"a\":", "1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n"},
		"mid-prefix":       {"da", "ta: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n"},
		"several per read": {"data: {\"a\":1}\n\ndata: {\"b\":2}\n\n", "data: [DONE]\n\n"},
		"byte at a time":   byteAtATime,
	}
	for name, chunks := range splits {
		t.Run(name, func(t *testing.T) {
			got, err := collect(t, chunks, bigLimit)
			if err != nil {
				t.Fatalf("readSSE errored: %v", err)
			}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("payloads = %q, want %q", got, want)
			}
		})
	}
}

// TestSSEIgnoresNoise: keep-alive comments, blank lines, CRLF endings, and fields we do not
// act on must all pass through without disturbing the payload sequence.
func TestSSEIgnoresNoise(t *testing.T) {
	stream := ": keep-alive\r\n" +
		"\r\n" +
		"event: message\r\n" +
		"id: 42\r\n" +
		"data: {\"a\":1}\r\n" +
		"\r\n" +
		": another keep-alive\n" +
		"retry: 1000\n" +
		"data: {\"b\":2}\n" +
		"\n" +
		"data: [DONE]\n"

	got, err := collect(t, []string{stream}, bigLimit)
	if err != nil {
		t.Fatalf("readSSE errored: %v", err)
	}
	if len(got) != 2 || got[0] != `{"a":1}` || got[1] != `{"b":2}` {
		t.Errorf("payloads = %q, want the two data objects only", got)
	}
}

// TestSSEMalformedChunkIsSkipped: one unparseable payload must not cost the whole answer.
// readSSE hands it on regardless — the classification happens in consumeStream — so what
// this pins is that the FRAMING keeps going and later events still arrive.
func TestSSEMalformedChunkIsSkipped(t *testing.T) {
	stream := "data: {\"a\":1}\n\ndata: not json at all\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n"
	got, err := collect(t, []string{stream}, bigLimit)
	if err != nil {
		t.Fatalf("readSSE errored: %v", err)
	}
	if len(got) != 3 || got[2] != `{"b":2}` {
		t.Errorf("a malformed chunk broke the stream: %q", got)
	}
}

// TestSSETornStream: EOF with no [DONE] and no finish_reason is a stream cut mid-answer.
func TestSSETornStream(t *testing.T) {
	_, err := collect(t, []string{"data: {\"a\":1}\n\ndata: {\"b\":2}\n"}, bigLimit)
	if !errors.Is(err, errStreamTorn) {
		t.Errorf("err = %v, want errStreamTorn", err)
	}
}

// TestSSECleanEndWithoutDone: a provider may close the connection after the final chunk
// instead of sending [DONE]. A finish_reason is proof the model finished, so that EOF is a
// clean end — treating it as torn would badge good answers as incomplete.
func TestSSECleanEndWithoutDone(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"
	if _, err := collect(t, []string{stream}, bigLimit); err != nil {
		t.Errorf("err = %v, want a clean end", err)
	}
}

// TestSSEFinalLineWithoutNewline is the data-plus-io.EOF case: bufio.ReadString returns a
// trailing partial line TOGETHER with io.EOF, so a loop that acts on the error before
// consuming the bytes silently drops the last line — which is exactly where a provider that
// skips the final newline puts its [DONE] or its finish_reason, i.e. the only evidence the
// stream ended cleanly. Both variants must read as clean, not torn.
func TestSSEFinalLineWithoutNewline(t *testing.T) {
	cases := map[string]string{
		"[DONE] unterminated": "data: {\"a\":1}\n\ndata: [DONE]",
		"finish_reason unterminated": "data: {\"a\":1}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}",
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := collect(t, []string{stream}, bigLimit)
			if err != nil {
				t.Errorf("err = %v, want a clean end", err)
			}
			if len(got) == 0 {
				t.Error("the unterminated final line was dropped entirely")
			}
		})
	}
}

// TestSSEByteBudget: running past the budget is OUR limit, reported distinctly from a
// provider hanging up so that it is never retried (see transient).
func TestSSEByteBudget(t *testing.T) {
	stream := strings.Repeat("data: {\"a\":1}\n\n", 100)
	_, err := collect(t, []string{stream}, 64)
	if !errors.Is(err, errStreamLimit) {
		t.Errorf("err = %v, want errStreamLimit", err)
	}
}

// TestSSENewlineFreeRunaway is the regression guard for keeping io.LimitReader.
//
// bufio.Reader.ReadString accumulates until it finds its delimiter, so a provider that
// streams without ever emitting a newline never returns a line and a per-line byte counter
// never advances — the buffer would grow without bound. The LimitReader forces EOF at the
// budget, ReadString hands back what it had, and only then can the counter classify it.
// Without the LimitReader this test hangs or exhausts memory; with a LimitReader alone the
// overrun would misreport as a clean EOF.
func TestSSENewlineFreeRunaway(t *testing.T) {
	const limit = 1 << 16
	endless := &endlessReader{}

	err := readSSE(io.LimitReader(endless, limit), limit,
		func() bool { return false },
		func([]byte) { t.Error("a newline-free stream dispatched a payload") })

	if !errors.Is(err, errStreamLimit) {
		t.Errorf("err = %v, want errStreamLimit", err)
	}
	if endless.read > limit {
		t.Errorf("read %d bytes past the %d-byte budget", endless.read, limit)
	}
}

// endlessReader streams forever and never emits a newline.
type endlessReader struct{ read int }

func (r *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.read += len(p)
	return len(p), nil
}
