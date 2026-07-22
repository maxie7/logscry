// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"encoding/json"
	"errors"
	"strings"
)

// errEmptyResponse is the one and only parse failure: the model said nothing at all.
// Everything else — fences, prose, wrong keys, a response cut off mid-sentence — is
// something we can still get value out of, so it degrades instead of failing.
var errEmptyResponse = errors.New("model returned an empty response")

// maxSummaryRunes bounds the prose fallback, which is otherwise unbounded model output
// landing in a one-line card.
const maxSummaryRunes = 400

// parseExplanation turns whatever the model actually said into an ExplainResponse.
//
// It assumes the model misbehaves, because it will: small local models wrap JSON in
// ```json fences, preface it with "Sure! Here's the analysis:", invent key spellings,
// and — when max_tokens bites — stop mid-object. None of that may cost the user their
// explanation, and none of it may take down the tail. So the rule here is DEGRADE,
// NEVER DISCARD: pull out whatever structure survives, and if none does, hand back the
// prose as the summary. An empty body is the only thing that can fail.
func parseExplanation(raw string) (ExplainResponse, error) {
	if strings.TrimSpace(raw) == "" {
		return ExplainResponse{}, errEmptyResponse
	}
	if p := partialExplanation(raw); p.found {
		return p.resp, nil
	}

	// No JSON survived. The model still said something, and something is worth more to
	// the person watching the tail than an empty card.
	text := stripFences(strings.TrimSpace(raw))
	return ExplainResponse{Summary: truncateRunes(collapse(text), maxSummaryRunes)}, nil
}

// parsed is what one attempt at reading a JSON object out of model output yielded.
type parsed struct {
	resp ExplainResponse
	// found reports that at least one field was COMPLETE — decoded key and value both.
	found bool
	// closed reports that the object's closing '}' was reached, i.e. the model finished
	// writing it. On a stream that is the difference between "the answer is complete and
	// the connection merely dropped afterwards" and "the answer is genuinely cut short".
	closed bool
}

// partialExplanation extracts the COMPLETED fields of a possibly-truncated body.
//
// It is parseExplanation without the prose fallback, and that is the point: a stream can be
// re-read after every chunk, and only whole field values may ever reach the card. Raw buffer
// text, a half-written value, an opening fence, a preamble — none of it can escape here,
// because nothing is returned that json did not finish decoding.
//
// Being a strict subset of the final parse rather than a parallel implementation is
// deliberate: progressive display cannot drift from the authoritative answer, because it is
// the same code reading the same bytes.
func partialExplanation(raw string) parsed {
	text := stripFences(strings.TrimSpace(raw))

	// Every '{' is a candidate opening brace, tried in order: the first one may belong
	// to prose ("...as shown in {this example}"), and the real object may follow it.
	for i, c := range text {
		if c != '{' {
			continue
		}
		if p := decodeObject(text[i:]); p.found {
			return p
		}
	}
	return parsed{}
}

// stripFences unwraps a ```json ... ``` block, which is the single most common thing a
// model does when told not to.
func stripFences(s string) string {
	const fence = "```"
	i := strings.Index(s, fence)
	if i < 0 {
		return s
	}
	rest := s[i+len(fence):]
	// Drop the language tag on the opening line ("json"), but only if that line is a
	// tag and not the payload itself (some models emit ```{"summary": ...).
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 && !strings.Contains(rest[:nl], "{") {
		rest = rest[nl+1:]
	}
	if j := strings.Index(rest, fence); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// decodeObject reads the JSON object starting at s[0] and maps it onto the response,
// reporting which fields survived and whether the object was ever closed.
//
// It streams tokens rather than unmarshalling the whole document, which is what makes
// truncation survivable: a response cut off mid-object still yields every key/value
// pair the model finished, and the decoder simply stops at the tear. Trailing prose
// after the closing brace is never read at all.
func decodeObject(s string) parsed {
	dec := json.NewDecoder(strings.NewReader(s))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return parsed{}
	}

	var p parsed
	for {
		tok, err := dec.Token()
		if err != nil {
			break // truncated: the model stopped mid-object
		}
		key, ok := tok.(string)
		if !ok {
			// A closing delimiter, not a key: the model finished the object. This is the
			// only place that fact is observable, and a streamed answer needs it to tell a
			// complete response from a torn one.
			p.closed = tok == json.Delim('}')
			break
		}
		var val any
		if err := dec.Decode(&val); err != nil {
			break // the value was cut off mid-flight
		}
		text, ok := val.(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		if field := fieldFor(key); field != nil {
			*field(&p.resp) = strings.TrimSpace(text)
			p.found = true
		}
	}
	return p
}

// fieldFor maps a key the model used onto the field it meant, or nil if it meant none
// of them. Models paraphrase key names as freely as they paraphrase anything else, so
// the match is on a normalized key and every plausible synonym is accepted.
func fieldFor(key string) func(*ExplainResponse) *string {
	switch normalizeKey(key) {
	case "summary", "what", "whathappened":
		return func(r *ExplainResponse) *string { return &r.Summary }
	case "likelycause", "cause", "probablecause", "rootcause", "why":
		return func(r *ExplainResponse) *string { return &r.LikelyCause }
	case "suggestion", "suggestions", "check", "whattocheck", "nextstep", "action":
		return func(r *ExplainResponse) *string { return &r.Suggestion }
	}
	return nil
}

// normalizeKey lowercases a key and strips the separators models vary on, so
// "likely_cause", "likelyCause", "Likely Cause", and "likely-cause" are one key.
func normalizeKey(key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		switch r {
		case '_', '-', ' ', '\t':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// collapse folds a multi-line answer onto one line, so the prose fallback fits a card
// row instead of tearing the pane apart.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
