// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/maxie7/logscry/internal/model"
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
	hexPat  = `0x[0-9a-fA-F]+|\b[0-9a-fA-F]{8,}\b`
	numPat  = `[+-]?\d+(?:\.\d+)?`
)

// The quoted-string pattern, in three alternatives (#91). A double-quoted string reads
// backslash escapes, so `"user \"bob\" logged in"` is ONE string. Without that, a Go-quoted
// value carrying an escaped quote was a string per escape, the text between them was left in
// the template, and every value made its own template.
//
//   - strEscaped is the escape-reading string. It may open only at the start of the text, or
//     after a character other than a backslash, an apostrophe or a double quote. Not after a
//     backslash: a quote escaped outside any string (`key=\"bob\" said "hi"`) would open one
//     and run on to the next real quote, merging every word in between. Not after an
//     apostrophe or a double quote: the match would start one character early and take
//     precedence over strSingle, or over a string that closed there. RE2 has no lookbehind,
//     so the character is CONSUMED and maskSTR re-emits it, as maskIP does for IPv6.
//   - strPlain is the old double-quoted rule, the fallback wherever strEscaped cannot match:
//     a string that never closes (`read "partial\"`), and a quote with no boundary before it.
//   - strSingle is the old single-quoted rule, unchanged. An escape-reading twin was measured
//     on ten journald boots (58,172 lines) and changed no line strEscaped does not already
//     move, while apostrophes in prose already pair across words under it.
//
// The boundary belongs to strEscaped ALONE: strPat groups the two, and the alternation binds
// looser than that concatenation. Moving it in front of all three would change every pairing
// the fallbacks exist to keep.
//
// What this buys: a template differs from the old pattern's only where a quote follows an odd
// run of backslashes. An escaped backslash before a closing quote (`"a\\"`) closes the string
// under both, a backslash before a line break is left to strPlain, and a string opened one
// character early is re-emitted unchanged. On the ten boots it moved one line.
//
// The trade accepted: a quoted path whose trailing backslash is literal, followed later on
// the line by another quote (`copy "C:\logs\" rotated "x"`), reads as an escape and runs on to
// that quote. Nothing in the text tells the two apart. The residual kept: a quote right after
// a closing quote is no boundary, so in `"a""b\"c"` the second string is strPlain's and still
// mis-pairs.
const (
	strBoundary = `(?:^|[^\\'"])`
	strEscaped  = `"(?:[^"\\]|\\.)*"`
	strPlain    = `"[^"]*"`
	strSingle   = `'[^']*'`
	strPat      = `(?:` + strBoundary + strEscaped + `)|` + strPlain + `|` + strSingle
)

// The IPv6 pattern, in two parts, because on its own the address grammar also
// describes a C++ scope resolution operator: "::" alone and "::" with one adjacent
// hex group are both legal IPv6, so "CDBusNMHelper::GetDNSConfig" masked to
// "CDBusNMHelper<IP>GetDNSConfig" — and, since the match ate the hex characters
// beside it, "CNSSCertStore::CNSSCertStore" masked to "CNSSCertStor<IP>NSSCertStore",
// naming a function that does not exist on the card that is the whole product (#33).
//
// A match must therefore satisfy BOTH clauses below, which do different jobs:
//
//   - ipv6Boundary — the match may not begin inside a token. This is what ends the
//     character-eating: a match that cannot start mid-word cannot swallow the letter
//     before it. Because a qualified C++ name always has identifier characters
//     running up to its "::", it also rejects every "Class::Method" outright. RE2
//     has no lookbehind, so the boundary character is CONSUMED and re-emitted by
//     maskIP.
//   - at least TWO hex groups — covers what the boundary alone does not, namely a
//     "::" that legitimately sits at a boundary: "operator :: used", "call ::AddRef",
//     and a message that BEGINS "Cafe::draw" all pass the boundary and fail here.
//
// The two-group minimum is not an artefact of the capture it was measured on. A
// qualified name is `identifier :: identifier`: exactly one "::" and no single ":"
// anywhere. Every IPv6 form carrying two or more groups needs either a single-colon
// separator or a group on each side of the "::", and the boundary clause disposes of
// the second case. That argument holds off the corpus.
//
// Known and deliberate, all three fragmentation rather than corruption (README,
// "Known limitations"):
//
//   - One-group forms are lost: "fe80::/64" and "::1". "::" plus an all-decimal group
//     WOULD be a sound discriminator for the loopback, since no C++ identifier begins
//     with a digit — not taken, because it widens the match for a shape no capture has
//     yet measured. Reopen it with evidence, not with taste.
//   - An address glued straight onto the preceding token is lost ("peer:2001:db8::1",
//     "upstream.2001:db8::1"): ':' and '.' are excluded from the boundary class, so
//     there is no legal start position. The costliest of the three — the NUM fallback
//     keeps the hex groups, so those lines fragment per-address instead of collapsing.
//   - Residual false positive, unfixable by any textual rule: hex-only segments of
//     four characters or fewer on both sides ("dec::add") ARE an address shape.
const (
	ipv6Group    = `[0-9a-fA-F]{1,4}`
	ipv6Boundary = `(?:^|[^0-9A-Za-z:.])`

	// Forms ending in a hex group, closed with \b so a trailing identifier character
	// cannot be absorbed either. Each carries two groups or more.
	ipv6Body = `(?:` + ipv6Group + `:){7}` + ipv6Group + // 1:2:3:4:5:6:7:8
		`|(?:` + ipv6Group + `:){1,6}:` + ipv6Group + // 1::8    1:2:3:4:5:6::8
		`|(?:` + ipv6Group + `:){1,5}(?::` + ipv6Group + `){1,2}` +
		`|(?:` + ipv6Group + `:){1,4}(?::` + ipv6Group + `){1,3}` +
		`|(?:` + ipv6Group + `:){1,3}(?::` + ipv6Group + `){1,4}` +
		`|(?:` + ipv6Group + `:){1,2}(?::` + ipv6Group + `){1,5}` +
		`|` + ipv6Group + `:(?::` + ipv6Group + `){1,6}` +
		`|::(?:` + ipv6Group + `:){1,6}` + ipv6Group // ::ffff:0:0

	// Trailing "::" ends on a colon, where \b would assert the wrong thing, so it is
	// held to two groups explicitly: "1:2::" masks, "Cafe::" does not.
	ipv6Trailing = `(?:` + ipv6Group + `:){2,7}:`

	ipv6Pat = ipv6Boundary + `(?:(?:` + ipv6Body + `)\b|` + ipv6Trailing + `)`
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
	{"IP", mustLongest(ipv4Pat + `|` + ipv6Pat), maskIP},
	{"HEX", regexp.MustCompile(hexPat), maskHex},
	{"NUM", regexp.MustCompile(numPat), constMask("<NUM>")},
	{"STR", regexp.MustCompile(strPat), maskSTR},
}

// maskIP emits <IP>, re-emitting the boundary character that ipv6Boundary had to
// consume — RE2 has no lookbehind, so the character before the address is part of the
// match. An IPv4 match, and any match at the start of the string, carries no such
// character: those begin with a hex digit or a colon and are replaced whole.
func maskIP(match string) string {
	r, n := utf8.DecodeRuneInString(match)
	if r == ':' || strings.ContainsRune("0123456789abcdefABCDEF", r) {
		return "<IP>"
	}
	return match[:n] + "<IP>"
}

// maskSTR emits <STR>, re-emitting the boundary character strEscaped had to consume. A match
// beginning with a quote carries none: it began at the start of the text, or is a fallback.
func maskSTR(match string) string {
	r, n := utf8.DecodeRuneInString(match)
	if r == '"' || r == '\'' {
		return "<STR>"
	}
	return match[:n] + "<STR>"
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

// TemplatizeLine produces the signature for a whole log line, choosing between three
// branches. A line whose raw form is a single JSON object or array is templated on its KEY
// STRUCTURE (see jsonshape.go); a logfmt line is templated on its keys with its message
// kept (see logfmt.go, #83); anything else runs the text masker over the normalized
// message, exactly as it always has.
//
// The JSON and logfmt branches parse the line a second time — Normalize already parsed it
// for level and message. That is one extra shallow decode per structured line, and it is
// the price of keeping "detect level/message" and "build a signature" two independent
// functions rather than one that does both.
func TemplatizeLine(line model.LogLine) (pattern, hash string) {
	trimmed := strings.TrimSpace(line.Raw)
	if pattern, ok := jsonShapeSignature(trimmed); ok {
		return pattern, hashTemplate(pattern)
	}
	if pattern, ok := logfmtSignature(trimmed); ok {
		return pattern, hashTemplate(pattern)
	}
	return Templatize(line.Message)
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
