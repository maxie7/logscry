// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
)

// A masker replaces variable substrings of one kind with a typed placeholder. The
// set is ordered and applied in sequence: specific patterns MUST run before general
// ones (e.g. IP/UUID/HEX before NUM) or "192.168.1.1" degrades to
// "<NUM>.<NUM>.<NUM>.<NUM>" and a UUID gets partially eaten. fn returns the
// replacement for a match, or the match unchanged to leave it for a later masker.
type masker struct {
	name string
	re   *regexp.Regexp
	fn   func(match string) string
}

// constMask returns a masker fn that always emits the given placeholder.
func constMask(placeholder string) func(string) string {
	return func(string) string { return placeholder }
}

// Component patterns. IPv6 requires either the full 8-group form or "::"
// compression, so clock times like "12:34:56" are not mistaken for an address.
const (
	tsPat   = `\b\d{4}-\d{2}-\d{2}[Tt ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:[Zz]|[+-]\d{2}:?\d{2})?`
	uuidPat = `\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`
	ipv4Pat = `\b\d{1,3}(?:\.\d{1,3}){3}\b`
	ipv6Pat = `(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}` + // 1:2:3:4:5:6:7:8
		`|(?:[0-9a-fA-F]{1,4}:){1,7}:` + // 1::            1:2:3:4:5:6:7::
		`|(?:[0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}` + // 1::8    1:2:3:4:5:6::8
		`|(?:[0-9a-fA-F]{1,4}:){1,5}(?::[0-9a-fA-F]{1,4}){1,2}` +
		`|(?:[0-9a-fA-F]{1,4}:){1,4}(?::[0-9a-fA-F]{1,4}){1,3}` +
		`|(?:[0-9a-fA-F]{1,4}:){1,3}(?::[0-9a-fA-F]{1,4}){1,4}` +
		`|(?:[0-9a-fA-F]{1,4}:){1,2}(?::[0-9a-fA-F]{1,4}){1,5}` +
		`|[0-9a-fA-F]{1,4}:(?::[0-9a-fA-F]{1,4}){1,6}` +
		`|:(?:(?::[0-9a-fA-F]{1,4}){1,7}|:)`
	hexPat = `0x[0-9a-fA-F]+|\b[0-9a-fA-F]{8,}\b`
	numPat = `[+-]?\d+(?:\.\d+)?`
	strPat = `"[^"]*"|'[^']*'`
)

// hexLetterRe detects whether a hex run carries a letter (a–f). Pure-decimal runs
// have none, so they fall through to the NUM masker instead of becoming <HEX>.
var hexLetterRe = regexp.MustCompile(`[a-fA-F]`)

// wsRe collapses internal whitespace runs so trivial spacing differences don't
// split otherwise-identical templates.
var wsRe = regexp.MustCompile(`\s+`)

// mustLongest compiles pat in leftmost-longest mode. IPv6's alternation lists
// shorter "::"-terminated forms that Go's default leftmost-first matching would
// otherwise prefer over the fuller address (e.g. "2001:db8::" beating
// "2001:db8::1"); longest mode picks the complete match.
func mustLongest(pat string) *regexp.Regexp {
	re := regexp.MustCompile(pat)
	re.Longest()
	return re
}

// maskers is the ordered pipeline of variable-substring maskers. Order is load-
// bearing (see the masker doc comment).
var maskers = []masker{
	{"TS", regexp.MustCompile(tsPat), constMask("<TS>")},
	{"UUID", regexp.MustCompile(uuidPat), constMask("<UUID>")},
	{"IP", mustLongest(ipv4Pat + `|` + ipv6Pat), constMask("<IP>")},
	{"HEX", regexp.MustCompile(hexPat), maskHex},
	{"NUM", regexp.MustCompile(numPat), constMask("<NUM>")},
	{"STR", regexp.MustCompile(strPat), constMask("<STR>")},
}

// maskHex masks 0x-prefixed literals and hex runs that contain a letter, but
// leaves pure-decimal runs untouched so the NUM masker classifies them.
func maskHex(match string) string {
	if strings.HasPrefix(match, "0x") || strings.HasPrefix(match, "0X") {
		return "<HEX>"
	}
	if hexLetterRe.MatchString(match) {
		return "<HEX>"
	}
	return match
}

// Templatize produces a masked signature for a log message by replacing variable
// substrings with typed placeholders, returning the human-readable pattern and its
// stable hash. The hash is the dedup key and the unit of "seen before vs new"; it
// is a deterministic function of the pattern string alone.
func Templatize(message string) (pattern, hash string) {
	pattern = message
	for _, m := range maskers {
		pattern = m.re.ReplaceAllStringFunc(pattern, m.fn)
	}
	pattern = strings.TrimSpace(wsRe.ReplaceAllString(pattern, " "))
	return pattern, hashTemplate(pattern)
}

// hashTemplate returns a stable 64-bit FNV-1a hash of the template string, hex
// encoded. Deterministic: no map iteration, no pointers.
func hashTemplate(pattern string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(pattern))
	return fmt.Sprintf("%016x", h.Sum64())
}
