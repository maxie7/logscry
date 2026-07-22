// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// Server-Sent Events framing, reduced to what an OpenAI-compatible chat-completions
// stream actually emits.
const (
	sseData = "data:"
	// sseDone is the sentinel that ends a stream cleanly.
	sseDone = "[DONE]"
)

// errStreamTorn is a stream that ended without ever saying it was finished — no [DONE] and
// no finish_reason. Something cut the connection, which is transient by nature: the same
// request may well succeed on a second attempt.
var errStreamTorn = errors.New("the response stream ended mid-answer")

// errStreamLimit is OUR limit, not the provider's failure: the stream ran past the byte
// budget. Distinct from errStreamTorn because it is DETERMINISTIC — a runaway endpoint, or
// the realistic case of a small local model stuck in a repetition loop, produces the same
// overrun on every attempt, after another full read and another full generation. Retrying
// it burns real tokens to fail identically, so transient() refuses it.
var errStreamLimit = errors.New("the response stream ran past its byte budget")

// readSSE reads Server-Sent Events from r, handing each "data:" payload to onData, and
// reports how the stream ended.
//
// It parses a BYTE STREAM, not a sequence of messages. bufio.Reader accumulates until it
// has a whole line, so where the network chose to split its packets is invisible here: a
// boundary mid-JSON-token, several events arriving in one read, or half an event followed
// by a long pause all behave identically. That is the whole reason the framing is read
// line-wise rather than by decoding whatever a single Read happened to return.
//
// Each "data:" line is dispatched on its own rather than being accumulated to the
// blank-line event boundary. Strict SSE concatenates multiple data lines within one event,
// but chat-completions emits exactly one self-contained JSON object per event, and
// dispatching per line means a provider that omits the final blank line does not silently
// lose its last chunk.
//
// It returns nil for a clean end ([DONE], or done reporting true), errStreamTorn for an
// EOF that arrived mid-answer, errStreamLimit for a budget overrun, or the underlying
// transport error.
func readSSE(r io.Reader, limit int64, done func() bool, onData func([]byte)) error {
	br := bufio.NewReader(r)
	var read int64

	for {
		line, err := br.ReadString('\n')

		// ReadString hands back a trailing partial line TOGETHER with io.EOF. Acting on the
		// error first would discard it — and an unterminated last line is exactly where a
		// provider that skips the final newline puts its [DONE] or its finish_reason chunk,
		// i.e. the very evidence that the stream ended cleanly. So the data is always
		// consumed before the error is considered.
		read += int64(len(line))
		if stop, sseErr := handleLine(line, onData); sseErr != nil || stop {
			return sseErr
		}

		if read >= limit {
			// Either the LimitReader below us cut the body off here, or the provider really
			// is unbounded. Both are our budget, not their failure.
			return errStreamLimit
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// A provider may simply close the connection after the last chunk instead
				// of sending [DONE]. If a finish_reason arrived, the model finished and
				// this is a clean end; otherwise the stream was cut mid-answer.
				if done() {
					return nil
				}
				return errStreamTorn
			}
			return err
		}
	}
}

// handleLine processes one SSE line, reporting whether the stream ended cleanly.
func handleLine(line string, onData func([]byte)) (stop bool, err error) {
	line = strings.TrimRight(line, "\r\n")
	switch {
	case line == "":
		return false, nil // event separator
	case strings.HasPrefix(line, ":"):
		return false, nil // a comment, which is how servers keep the connection alive
	case !strings.HasPrefix(line, sseData):
		return false, nil // "event:", "id:", "retry:" — none of which we act on
	}

	payload := strings.TrimSpace(strings.TrimPrefix(line, sseData))
	if payload == sseDone {
		return true, nil
	}
	if payload != "" {
		onData([]byte(payload))
	}
	return false, nil
}

// streamChunk is one chat.completion.chunk, reduced to the fields v1 uses. Delta carries
// the increment, not the accumulated text.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Reasoning mirrors the non-streaming choice: a thinking model streams its
			// chain of thought here, separately from the answer.
			Reasoning string `json:"reasoning"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// hasQuote reports whether s contains a double quote. A JSON string field can only become
// complete when its CLOSING quote arrives, so this is an exact test for "a field might have
// just finished" — and it keeps the buffer from being re-parsed on every token of a long
// answer, which would otherwise be quadratic.
func hasQuote(s string) bool { return strings.ContainsRune(s, '"') }
