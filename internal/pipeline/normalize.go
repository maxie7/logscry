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
// "[ERROR]", a "ERROR:" prefix, or a logfmt "level=error" at the start of the
// line. The captured token is canonicalized; the whole match is the prefix that
// may be stripped from the message.
var leadingLevelRe = regexp.MustCompile(`(?i)^\s*(?:\[\s*([a-z]+)\s*\]|([a-z]+)\s*:|level\s*=\s*"?([a-z]+)"?)\s*`)

// Normalize detects the line format (JSON vs plain text) and fills in Level and
// Message on the LogLine. Raw is never modified — display uses it.
func Normalize(line model.LogLine) model.LogLine {
	trimmed := strings.TrimSpace(line.Raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if level, msg, ok := normalizeJSON(trimmed); ok {
			line.Level = level
			line.Message = msg
			return line
		}
	}
	line.Level, line.Message = normalizePlain(line.Raw)
	return line
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

// normalizePlain runs light heuristics for a leading level token on plain text.
// It conservatively strips only a recognized leading "[LVL]" / "LVL:" / "level="
// prefix from the message; otherwise Message is the line verbatim.
func normalizePlain(raw string) (level, message string) {
	m := leadingLevelRe.FindStringSubmatch(raw)
	if m == nil {
		return "", raw
	}
	// Exactly one of the three capture groups is populated by the alternation.
	token := m[1] + m[2] + m[3]
	canon := canonicalLevel(token)
	if canon == "" {
		// Matched shape but not a real level (e.g. "http://" -> "http:"): leave
		// the line untouched rather than eating a meaningful prefix.
		return "", raw
	}
	return canon, strings.TrimSpace(raw[len(m[0]):])
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
