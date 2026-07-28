// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// JSON-per-line templating. The text masker (template.go) was built for line-oriented
// logs — "user 4821 failed" — and applied to a nested JSON object it masks every string,
// KEYS INCLUDED, into <STR>: the signature degrades to brace soup that says nothing and
// dedups wrong (same-shaped different events collapse; one event logged with different
// nesting splits). Field NAMES are the stable, meaningful part of a structured event, so
// a JSON line is templated on its KEY STRUCTURE instead: keys kept literally, leaf values
// replaced with typed placeholders, keys sorted so key order cannot split a template.
//
// The one exception is the recognized message field, whose value IS the human payload and
// IS where the variable parts live — masking it to a bare <STR> would collapse
// {"msg":"disk full"} into {"msg":"connection refused"}, which is the same dedup failure
// from the other side. That field alone is run through the text masker. Every other value
// stays a plain typed placeholder: running the text masker over arbitrary values would
// bring the soup straight back and couple key-structure templating to value-masking.

const (
	// maxJSONDepth caps recursion; deeper objects and arrays render as "{...}" / "[...]".
	// maxJSONNodes caps the total values rendered, shared across the whole line; once
	// spent, remaining siblings are dropped and a single "..." marks the truncation.
	// Between them a pathological producer — deeply nested, or an object with ten
	// thousand keys — costs bounded work and yields a bounded pattern instead of hanging.
	// Keys are sorted before rendering, so where the truncation falls is deterministic.
	maxJSONDepth = 6
	maxJSONNodes = 128
)

// jsonShapeSignature returns the key-structure signature of a line that is a single JSON
// object or array, reporting ok=false for anything else so the caller falls back to the
// text masker.
//
// Top-level ARRAYS take this branch too: the failure mode is identical (keys inside the
// elements would otherwise be masked away) and the recursion already handles them. Top-
// level SCALARS do not — a line that is a bare 42 or "hello world" is ordinary text, and
// the text masker is the right tool for it.
func jsonShapeSignature(trimmed string) (pattern string, ok bool) {
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", false // cheap reject before paying for a parse
	}
	// Detection is a real parse, not a leading-brace check: "{not json at all" must still
	// take the text path. Unmarshal validates the whole document, so trailing junk after a
	// well-formed object is rejected as well.
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return "", false
	}

	budget := maxJSONNodes
	if obj, isObj := decodeObject(raw); isObj {
		return shapeObject(obj, 0, &budget, messageField(obj)), true
	}
	return shapeValue(raw, 0, &budget), true
}

// shapeValue renders one JSON value: an object or array recursively, any scalar as its
// typed placeholder.
func shapeValue(raw json.RawMessage, depth int, budget *int) string {
	v := bytes.TrimSpace(raw)
	if len(v) == 0 {
		return "<NULL>"
	}
	switch v[0] {
	case '{':
		obj, ok := decodeObject(v)
		if !ok {
			return "{...}" // unreachable: the top-level parse already validated this
		}
		// msgKey is empty below the top level: the message field is the one Normalize
		// extracts, and it looks only at the top-level object (RDI §5.1).
		return shapeObject(obj, depth, budget, "")
	case '[':
		return shapeArray(v, depth, budget)
	}
	return scalarShape(v[0])
}

// shapeObject renders an object as {"key":<shape>,...} with keys sorted — sorting is what
// makes the same event logged with a different key order produce the same signature,
// without which dedup still splits. msgKey, non-empty only at the top level, names the one
// field whose value is text-masked rather than blanked.
//
// An empty object renders as "{}", which means every field-less line — a heartbeat, an
// ack — lands on ONE template regardless of source. That is deliberate: structurally
// identical is exactly what this signature is defined to mean, and a line carrying no
// fields carries nothing to tell it apart by. A flood of them reads as a burst on one
// template, which is the honest picture.
func shapeObject(obj map[string]json.RawMessage, depth int, budget *int, msgKey string) string {
	if depth >= maxJSONDepth {
		return "{...}"
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if *budget <= 0 {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString("...")
			break
		}
		*budget--
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(k))
		b.WriteByte(':')
		if k == msgKey {
			b.WriteString(maskedMessage(obj[k]))
			continue
		}
		b.WriteString(shapeValue(obj[k], depth+1, budget))
	}
	b.WriteByte('}')
	return b.String()
}

// shapeArray renders an array as its DISTINCT element shapes, sorted: [1,2,3] -> [<NUM>],
// [1,"a"] -> [<NUM>,<STR>], [] -> []. Length is a value-like property, not structure —
// folding it in would split a template by how many items happened to be logged.
func shapeArray(raw json.RawMessage, depth int, budget *int) string {
	if depth >= maxJSONDepth {
		return "[...]"
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return "[...]" // unreachable: the top-level parse already validated this
	}
	shapes := make([]string, 0, len(arr))
	for _, el := range arr {
		if *budget <= 0 {
			shapes = append(shapes, "...")
			break
		}
		*budget--
		shapes = append(shapes, shapeValue(el, depth+1, budget))
	}
	slices.Sort(shapes)
	return "[" + strings.Join(slices.Compact(shapes), ",") + "]"
}

// scalarShape classifies a scalar from its first byte — enough, since the document has
// already been validated — into the existing masker vocabulary.
func scalarShape(c byte) string {
	switch c {
	case '"':
		return "<STR>"
	case 't', 'f':
		return "<BOOL>"
	case 'n':
		return "<NULL>"
	}
	return "<NUM>"
}

// maskedMessage renders the recognized message field: its value run through the text
// masker and re-quoted, so the fragment inside a JSON signature is byte-identical to the
// template the same text gets on the plain-text path. The full hashes still differ — the
// JSON wrapper is real information — but the message-fragment computation is shared
// rather than reinvented.
//
// A message value that is itself JSON-as-a-string ({"msg":"{\"event\":\"start\"}"}) is
// deliberately NOT re-parsed: it comes out text-masked like any other string. Recursing
// into message values is a different feature, not this one.
func maskedMessage(raw json.RawMessage) string {
	s, ok := jsonString(raw)
	if !ok {
		return "<STR>" // messageField only ever names a string value; belt and braces
	}
	pattern, _ := Templatize(s)
	return strconv.Quote(pattern)
}

// messageField returns the key holding the human message, or "" when the object has none,
// when more than one candidate is present, or when the value is not a string. Ambiguity
// and non-strings fall back to an ordinary typed placeholder: the text-masking exception
// is worth making only when it is unmistakably the message, and forcing a text masker
// onto an object or a number is meaningless.
func messageField(obj map[string]json.RawMessage) string {
	idx := jsonKeyIndex(obj)
	found := ""
	for _, k := range jsonMessageKeys {
		actual, present := idx[k]
		if !present {
			continue
		}
		if found != "" {
			return "" // ambiguous: e.g. both "msg" and "message"
		}
		found = actual
	}
	if found == "" {
		return ""
	}
	if _, ok := jsonString(obj[found]); !ok {
		return ""
	}
	return found
}

// decodeObject decodes raw as a JSON object, reporting ok=false for any other value.
func decodeObject(raw json.RawMessage) (obj map[string]json.RawMessage, ok bool) {
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	return obj, obj != nil // a literal null unmarshals into a nil map, not an object
}
