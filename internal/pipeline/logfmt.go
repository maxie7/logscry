// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"strconv"
	"strings"
)

// logfmt templating (#83). A logfmt line — `time="…" level=error msg="…"`, as logrus,
// slog's text handler, go-kit and dockerd/containerd write it — mostly survives the text
// masker: keys are words and bare values get their usual masks. What it does NOT survive is
// the quoted message. strPat masks every quoted string whole, so `msg="disk full"` and
// `msg="connection refused"` were both `msg=<STR>`, and every message of one level from
// one daemon — or from four daemons sharing the shape — was ONE template. Novelty could
// fire once per level and never again, and the "template" a burst was measured on was the
// union of several services' logs.
//
// So a logfmt line is templated the way M9 templates JSON, by its keys, with the message
// field run through the text masker instead of blanked. `error` gets the same treatment:
// it is where logrus's WithError and dockerd put the failure cause, and two failures
// sharing a msg ("Failed to get event") are told apart by it. Every other value keeps the
// rule it had on the text path — a quoted value is <STR>, a bare one is masked — and pairs
// keep their order, so a logfmt line with no quoted message templates exactly as before.
//
// Recognition is strict, because prose carries a key=value or two ("user=bob login
// failed") and must keep the text path: the WHOLE line has to parse as pairs, at least two
// of them, and one key has to be a level or message key. A bare word anywhere disqualifies
// the line, and so does a line break: an event carrying one is several physical lines the
// coalescer grouped, not one logfmt line (a logger escapes a newline inside a quoted
// value), and it keeps the text path it always had.

// logfmtTextKeys are keys whose value is templatized rather than masked whole, besides the
// recognized message key. `err` is deliberately absent: no captured line carries it.
var logfmtTextKeys = map[string]bool{"error": true}

// logfmtPair is one key=value of a recognized logfmt line. value is the value as written:
// with its surrounding quotes when quoted is set.
type logfmtPair struct {
	key    string
	value  string
	quoted bool
}

// parseLogfmt splits trimmed into its pairs, reporting ok=false unless the WHOLE line is
// logfmt in the sense above: two or more pairs separated by whitespace, nothing else, and a
// level or message key among them.
//
// A key is [A-Za-z_][A-Za-z0-9_.\-/]*. A value is either a double-quoted string, which may
// contain backslash escapes and must terminate, or a bare run of non-whitespace containing
// no '"' and no '=' — loggers quote a value carrying either, so meeting one bare means the
// line is not clean logfmt and the text path keeps it. The cost of a non-logfmt line is
// small: prose fails at its first space, before any '='.
//
// A raw line break rejects the line outright. Grouped events are the reason: slog's
// source=file.go:N matches the coalescer's Go-frame cue, so consecutive slog lines arrive
// here joined by "\n", and reading them as one line of repeated keys would hand the
// group its first line's level and leave the message ambiguous.
func parseLogfmt(trimmed string) (pairs []logfmtPair, ok bool) {
	if strings.ContainsAny(trimmed, "\n\r") {
		return nil, false
	}
	s, i := trimmed, 0
	for {
		for i < len(s) && isLogfmtSpace(s[i]) {
			i++
		}
		if i == len(s) {
			break
		}
		start := i
		if !isKeyStart(s[i]) {
			return nil, false
		}
		for i < len(s) && isKeyChar(s[i]) {
			i++
		}
		if i == len(s) || s[i] != '=' {
			return nil, false
		}
		key := s[start:i]
		i++ // '='

		vstart := i
		quoted := i < len(s) && s[i] == '"'
		if quoted {
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' {
					i++
				}
				i++
			}
			if i >= len(s) {
				return nil, false // unterminated
			}
			i++ // closing '"'
			if i < len(s) && !isLogfmtSpace(s[i]) {
				return nil, false // `msg="a"b`: not a pair boundary
			}
		} else {
			for i < len(s) && !isLogfmtSpace(s[i]) {
				if s[i] == '"' || s[i] == '=' {
					return nil, false
				}
				i++
			}
		}
		pairs = append(pairs, logfmtPair{key: key, value: s[vstart:i], quoted: quoted})
	}
	if len(pairs) < 2 || !hasLevelOrMessageKey(pairs) {
		return nil, false
	}
	return pairs, true
}

// isLogfmtSpace is the whitespace class the text masker collapses (RE2's \s), less the
// line breaks parseLogfmt has already refused.
func isLogfmtSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\f'
}

func isKeyStart(c byte) bool {
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isKeyChar(c byte) bool {
	return isKeyStart(c) || ('0' <= c && c <= '9') || c == '.' || c == '-' || c == '/'
}

// hasLevelOrMessageKey reports whether any key, case-insensitively, is one normalize
// already trusts for JSON. That is what separates a log line written in logfmt from other
// all-pairs text — an environment dump (`HOME=/ TERM=linux`), an audit record.
func hasLevelOrMessageKey(pairs []logfmtPair) bool {
	for _, p := range pairs {
		k := strings.ToLower(p.key)
		for _, want := range jsonLevelKeys {
			if k == want {
				return true
			}
		}
		for _, want := range jsonMessageKeys {
			if k == want {
				return true
			}
		}
	}
	return false
}

// logfmtSignature returns the template of a line that is logfmt, reporting ok=false for
// anything else so the caller falls back to the text masker. Pairs are rendered in their
// original order: a producer writes one order per call site, and keeping it keeps every
// template this change does not need to move exactly where it was.
func logfmtSignature(trimmed string) (pattern string, ok bool) {
	pairs, ok := parseLogfmt(trimmed)
	if !ok {
		return "", false
	}
	msg := logfmtMessageIndex(pairs)

	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p.key) // keys are kept literally, as in M9
		b.WriteByte('=')
		b.WriteString(logfmtValueShape(p, i == msg || logfmtTextKeys[strings.ToLower(p.key)]))
	}
	return b.String(), true
}

// logfmtValueShape renders one value. A text field's quoted value is unquoted, run through
// the text masker and re-quoted — the computation maskedMessage does for JSON, so the
// fragment is byte-identical to the template the same text gets on the plain-text path.
// Any other quoted value is <STR>, and a bare value is masked, both exactly as the text
// path treated them.
func logfmtValueShape(p logfmtPair, text bool) string {
	switch {
	case p.quoted && text:
		pattern, _ := Templatize(logfmtUnquote(p.value))
		return strconv.Quote(pattern)
	case p.quoted:
		return "<STR>"
	}
	pattern, _ := Templatize(p.value)
	return pattern
}

// logfmtUnquote decodes a quoted value. logrus and slog both quote with Go syntax, so
// strconv.Unquote is the decoder; a value it rejects falls back to the text between the
// quotes rather than losing the line.
func logfmtUnquote(v string) string {
	if s, err := strconv.Unquote(v); err == nil {
		return s
	}
	return v[1 : len(v)-1]
}

// logfmtMessageIndex returns the index of the pair holding the human message, or -1 when
// there is none or more than one — M9's messageField rule: the text-masking exception is
// made only when the field is unmistakably the message.
func logfmtMessageIndex(pairs []logfmtPair) int {
	found := -1
	for i, p := range pairs {
		k := strings.ToLower(p.key)
		for _, want := range jsonMessageKeys {
			if k != want {
				continue
			}
			if found >= 0 {
				return -1 // ambiguous: e.g. both "msg" and "message"
			}
			found = i
		}
	}
	return found
}

// logfmtLevel returns the canonical level of a logfmt line, read wherever its level key
// sits. Keys are probed in jsonLevelKeys order and the first one present decides, as for
// JSON; an unrecognized value yields "" rather than a guess.
func logfmtLevel(pairs []logfmtPair) string {
	for _, want := range jsonLevelKeys {
		for _, p := range pairs {
			if strings.ToLower(p.key) != want {
				continue
			}
			v := p.value
			if p.quoted {
				v = logfmtUnquote(v)
			}
			return canonicalLevel(v)
		}
	}
	return ""
}
