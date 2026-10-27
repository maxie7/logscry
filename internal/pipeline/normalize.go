// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/maxie7/logscry/internal/model"
)

// levelSynonyms maps lowercase level tokens to the canonical uppercase set
// {DEBUG, INFO, WARN, ERROR, FATAL, PANIC, CRITICAL}. Tokens not present here but
// matching the canonical set (e.g. "info") are handled by canonicalLevel's
// fallback of uppercasing a recognized token.
var levelSynonyms = map[string]string{
	"trace":    "DEBUG",
	"debug":    "DEBUG",
	"dbg":      "DEBUG",
	"info":     "INFO",
	"inf":      "INFO",
	"notice":   "INFO",
	"warn":     "WARN",
	"warning":  "WARN",
	"err":      "ERROR",
	"error":    "ERROR",
	"fatal":    "FATAL",
	"panic":    "PANIC",
	"crit":     "CRITICAL",
	"critical": "CRITICAL",
}

// jsonLevelKeys and jsonMessageKeys are the JSON object keys we probe, in order,
// for the level and human message respectively.
var (
	jsonLevelKeys   = []string{"level", "lvl", "severity", "log.level"}
	jsonMessageKeys = []string{"message", "msg", "text"}
)

// leadingLevelRe matches a leading level token in plain text: a bracketed
// "[ERROR]", a "ERROR:" prefix, or a "level=error" at the start of the line. The
// captured token is canonicalized; the whole match is the prefix that may be
// stripped from the message. A line that is logfmt throughout has its level read
// wherever the key sits instead (logfmtLevel, #83), and a level after the line's own
// header columns is headerLevel's (#31).
var leadingLevelRe = regexp.MustCompile(`(?i)^\s*(?:\[\s*([a-z]+)\s*\]|([a-z]+)\s*:|level\s*=\s*"?([a-z]+)"?)\s*`)

// klogHeaderRe is the header klog and glog write, "Lmmdd hh:mm:ss.uuuuuu threadid file:line] ",
// where L is the severity's initial glued to the date. The whole header must be there, so
// no prose can match it.
var klogHeaderRe = regexp.MustCompile(`^([IWEF])\d{4} \d{2}:\d{2}:\d{2}\.\d{6}\s+\d+ [^\s:\]]+:\d+\] `)

// klogLevels maps klog's severity initial to the canonical set.
var klogLevels = map[byte]string{'I': "INFO", 'W': "WARN", 'E': "ERROR", 'F': "FATAL"}

// calendarWords are the words a timestamp itself spells: syslog's and JUL's month and day
// names, and a 12-hour clock's AM/PM.
var calendarWords = map[string]bool{
	"Jan": true, "Feb": true, "Mar": true, "Apr": true, "May": true, "Jun": true,
	"Jul": true, "Aug": true, "Sep": true, "Oct": true, "Nov": true, "Dec": true,
	"Mon": true, "Tue": true, "Wed": true, "Thu": true, "Fri": true, "Sat": true, "Sun": true,
	"AM": true, "PM": true,
}

// nameColumnRe follows a word that is a column of its own, set off by a separator: Python's
// "asctime - name - levelname" puts the logger's name between dashes, loguru uses a pipe.
var nameColumnRe = regexp.MustCompile(`^[ \t]+(?:-+|\|)[ \t]`)

// firstWordDelimRe must follow a bare level that is a line's first word: "ERROR - x" and
// "WARN | x" are levels, "PANIC button pressed" is prose.
var firstWordDelimRe = regexp.MustCompile(`^[ \t]*(?:[|:\]]|-+[ \t])`)

// Normalize detects the line format (JSON, logfmt or plain text) and fills in Level and
// Message on the LogLine. Raw is never modified — display uses it.
//
// A Level the SOURCE already set wins over anything detected from the text. Such a level
// comes from structured metadata the message body has no trace of — journald's PRIORITY,
// recorded by systemd itself at the journal protocol level — whereas a leading "[INFO]"
// token is a regex guess about prose. The two genuinely disagree in practice: a unit
// logging "INFO: shutting down" at PRIORITY=3 is telling us, through the channel that
// cannot be fooled, that this is an error.
//
// Message extraction runs either way. The source supplies a level, not a parse, so the
// line still goes through the same format detection as every other line and still gets
// its recognized prefix stripped — only the level is overridden.
//
// Sources that know no level leave it empty and reach the detection below unchanged,
// which is every source that existed before journald.
func Normalize(line model.LogLine) model.LogLine {
	given := line.Level
	line.Level, line.Message = detectLevelAndMessage(line.Raw)
	if given != "" {
		line.Level = given
	}
	return line
}

// detectLevelAndMessage runs the format detection: a JSON object yields its level and
// message fields, anything else falls to the plaintext heuristics.
//
// A logfmt line (logfmt.go) takes its level from its level key, wherever that key sits:
// `time="…" level=error msg="…"` begins with time=, so the leading-token heuristic never
// saw it (#83). Its message is left as the heuristics produce it, because nothing on the
// logfmt path reads it — TemplatizeLine templates such a line from Raw. A level found in
// the middle of prose is still not a level: only a line that is logfmt throughout is read
// this way. A plaintext level after the line's header columns is headerLevel's (#31).
func detectLevelAndMessage(raw string) (level, message string) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if level, msg, ok := normalizeJSON(trimmed); ok {
			return level, msg
		}
	}
	level, message = normalizePlain(raw)
	if pairs, ok := parseLogfmt(trimmed); ok {
		level = logfmtLevel(pairs)
	}
	return level, message
}

// normalizeJSON parses a JSON object and extracts a canonical level and message.
// It reports ok=false when the payload is not a JSON object, letting the caller
// fall back to the plaintext path.
func normalizeJSON(s string) (level, message string, ok bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return "", "", false
	}

	idx := jsonKeyIndex(obj)
	for _, k := range jsonLevelKeys {
		if actual, present := idx[k]; present {
			if v, ok := jsonString(obj[actual]); ok {
				level = canonicalLevel(v)
				break
			}
		}
	}

	message = s // fall back to the raw JSON when no message field is present
	for _, k := range jsonMessageKeys {
		if actual, present := idx[k]; present {
			if v, ok := jsonString(obj[actual]); ok {
				message = v
				break
			}
		}
	}
	return level, message, true
}

// jsonKeyIndex maps the lowercased form of every key in a JSON object to the key as it
// actually appears, so the probes above match "LEVEL" and "Msg" as readily as "level" and
// "msg" — case is a logger's choice, not a different field.
//
// Keys are sorted before the index is built and the first occurrence of a lowercased form
// wins, so an object carrying both "level" and "Level" resolves the same way on every run
// rather than by map iteration order.
func jsonKeyIndex(obj map[string]json.RawMessage) map[string]string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	idx := make(map[string]string, len(keys))
	for _, k := range keys {
		lower := strings.ToLower(k)
		if _, dup := idx[lower]; !dup {
			idx[lower] = k
		}
	}
	return idx
}

// jsonString decodes a RawMessage as a JSON string, reporting ok=false for
// non-string values (numbers, objects, etc.).
//
// null is checked separately because it unmarshals into a string WITHOUT error, leaving
// it empty — so a `"msg": null` would otherwise be adopted as an empty message instead of
// falling back like the absent field it is.
func jsonString(raw json.RawMessage) (string, bool) {
	if v := bytes.TrimSpace(raw); len(v) == 0 || v[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// normalizePlain runs light heuristics for a level token on plain text. It
// conservatively strips only a recognized leading "[LVL]" / "LVL:" / "level=" prefix
// from the message. Failing that, it looks for a level after the line's own header
// (headerLevel) and leaves Message the line verbatim, so no template moves (#31).
func normalizePlain(raw string) (level, message string) {
	m := leadingLevelRe.FindStringSubmatch(raw)
	if m == nil {
		return headerLevel(raw), raw
	}
	// Exactly one of the three capture groups is populated by the alternation.
	token := m[1] + m[2] + m[3]
	canon := canonicalLevel(token)
	if canon == "" {
		// Matched shape but not a real level (e.g. "http://" -> "http:"): leave
		// the line untouched rather than eating a meaningful prefix.
		return headerLevel(raw), raw
	}
	return canon, strings.TrimSpace(raw[len(m[0]):])
}

// headerLevel reads a level a logger wrote after its own columns: Spring Boot's
// "2026-01-15 10:23:45.123 ERROR 12345 --- …", Logback's "10:00:00.123 [main] ERROR …",
// nginx's "2026/07/13 10:00:00 [error] …", Rails' "E, [… #4242] ERROR -- : …" (#31). Only
// the first physical line is read, so a folded trace keeps the level of its head.
//
// The line is split into tokens at whitespace and at [ ] ( ) | : /, and walked left to
// right. The first token that is a level column is the level. A header token, the stuff a
// log layout puts before its level, is passed over. Anything else is prose and ends the
// walk with no level: a line that starts with an ordinary word is never read, whatever
// follows it, because a level read from prose is #27's cry-wolf in a new place. So does an
// ANSI escape: colour is its own question, not a column.
func headerLevel(raw string) string {
	line, _, _ := strings.Cut(raw, "\n")
	if m := klogHeaderRe.FindStringSubmatch(line); m != nil {
		return klogLevels[m[1][0]]
	}
	line = strings.TrimLeft(line, " \t")

	depth := 0 // open brackets and parentheses that close later on the line
	for i := 0; i < len(line); {
		switch c := line[i]; c {
		case ' ', '\t', '\r', '|', ':', '/':
			i++
			continue
		case '[', '(':
			// An unclosed bracket does not turn the rest of the line into a header.
			if strings.IndexByte(line[i+1:], closer(c)) >= 0 {
				depth++
			}
			i++
			continue
		case ']', ')':
			if depth > 0 {
				depth--
			}
			i++
			continue
		}
		j := i
		for j < len(line) && !isTokenDelim(line[j]) {
			j++
		}
		if level := columnLevel(line, i, j); level != "" {
			return level
		}
		if depth == 0 && !isHeaderToken(line, i, j) {
			return ""
		}
		i = j
	}
	return ""
}

// columnLevel reports the level line[i:j] stands for when it is a level COLUMN, or "".
//
//   - Its left edge is the line's start, whitespace, "[", "(" or "|". Never ":", so
//     Chromium's "[pid:tid:time:ERROR:file(N)]" stays unread: on a desktop that level is
//     browser chatter, and reading it was measured at 82 more escalations over ten
//     boots that had 59 — a scoring decision, not a detector one.
//   - It is all upper-case, or closed by "]" or ":" in any case ("[error]", "Warning:").
//     A bare lower-case "error" is prose.
//   - A bare level that is the line's first word, with no header before it, must be set
//     off by a delimiter: "ERROR - x" is a level, "PANIC button pressed" is prose that
//     would otherwise escalate alone at 1.00.
func columnLevel(line string, i, j int) string {
	tok := line[i:j]
	canon := canonicalLevel(tok)
	if canon == "" {
		return ""
	}
	if i > 0 {
		switch line[i-1] {
		case ' ', '\t', '[', '(', '|':
		default:
			return ""
		}
	}
	closed := j < len(line) && (line[j] == ']' || line[j] == ':')
	if closed {
		return canon
	}
	if tok != strings.ToUpper(tok) {
		return ""
	}
	if i == 0 && !firstWordDelimRe.MatchString(line[j:]) {
		return ""
	}
	return canon
}

// isHeaderToken reports whether line[i:j], which is not a level, is the kind of column a
// log layout writes before its level rather than a word of prose.
func isHeaderToken(line string, i, j int) bool {
	tok := line[i:j]
	switch {
	case strings.ContainsAny(tok, "0123456789"): // a date, time, pid, thread id or offset
		return true
	case isPunct(tok): // a separator: "-", "--", "---", "#", "*"
		return true
	case calendarWords[tok]:
		return true
	case len(tok) >= 3 && len(tok) <= 5 && tok == strings.ToUpper(tok) &&
		i >= 2 && line[i-1] == ' ' && isDigit(line[i-2]):
		// A zone straight after a time: PostgreSQL's "%m" prints "… 10:00:00.123 UTC".
		return true
	case len(tok) == 2 && tok[0] >= 'A' && tok[0] <= 'Z' && tok[1] == ',':
		// Ruby's Logger leads with the severity's initial: "E, [… #4242] ERROR -- : …".
		return true
	case j+1 < len(line) && line[j] == ':' && isDigit(line[j+1]):
		// A source position: Go's Lshortfile "db.go:17:", loguru's "module:func:42".
		return true
	case nameColumnRe.MatchString(line[j:]):
		return true
	}
	return false
}

// isTokenDelim reports whether c ends a header token.
func isTokenDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '[', ']', '(', ')', '|', ':', '/':
		return true
	}
	return false
}

// closer returns the bracket that closes open.
func closer(open byte) byte {
	if open == '(' {
		return ')'
	}
	return ']'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isPunct reports whether s is printable ASCII punctuation only. A control byte is not a
// separator: an ANSI colour escape ends the walk, so a coloured line stays unread rather
// than being read only when its escape happens to sit before a bracketed level.
func isPunct(s string) bool {
	for i := range len(s) {
		c := s[i]
		if c <= ' ' || c >= 0x7f || isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'z') {
			return false
		}
	}
	return true
}

// canonicalLevel maps a raw level token to the canonical uppercase set, returning
// "" when the token is not a recognized level.
func canonicalLevel(token string) string {
	if token == "" {
		return ""
	}
	if canon, ok := levelSynonyms[strings.ToLower(token)]; ok {
		return canon
	}
	return ""
}
