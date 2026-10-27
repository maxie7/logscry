// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/maxie7/logscry/internal/pipeline"
)

// escapeForm matches one escaped '/', ':' or '@' as a JSON encoder writes it (GHSA-p47p-xrhj-3x7p): a
// run of backslashes -- one for a string, more for a string inside another -- before '/', before the
// "u" escape of '/', ':' or '@', or before the pipeline's template of the "u" escape of '/', which is
// asked of pipeline.Templatize rather than restated, as #105 does for a host suffix.
//
// Nothing else is read: the template of the "u" escape of ':' or '@' also stands for the double quote,
// the apostrophe and '&', and no encoder is known to escape any other URL character (README's Known limitations).
var escapeForm = func() *regexp.Regexp {
	alts := []string{"/", "u002[fF]", "u003[aA]", "u0040"}
	for _, e := range []string{"u002f", "u002F"} {
		tpl, _ := pipeline.Templatize(`\` + e)
		alts = append(alts, regexp.QuoteMeta(strings.TrimPrefix(tpl, `\`)))
	}
	return regexp.MustCompile(`\\+(?:` + strings.Join(alts, "|") + `)`)
}()

// escapeView returns s with every escapeForm resolved to the one character it stands for, and offs, where
// offs[i] is the offset in s of the view's byte i and offs[len(view)] is len(s). With no escape in s it
// returns s and nil.
//
// A resolved escape is one byte of the view, so a span of the view begins and ends on a boundary of s:
// [offs[a], offs[b]) is the whole of every escape inside it.
func escapeView(s string) (string, []int) {
	locs := escapeForm.FindAllStringIndex(s, -1)
	if locs == nil {
		return s, nil
	}
	var b strings.Builder
	offs := make([]int, 0, len(s)+1)
	prev := 0
	for _, loc := range locs {
		for i := prev; i < loc[0]; i++ {
			offs = append(offs, i)
		}
		b.WriteString(s[prev:loc[0]])
		b.WriteByte(resolved(s[loc[0]:loc[1]]))
		offs = append(offs, loc[0])
		prev = loc[1]
	}
	for i := prev; i < len(s); i++ {
		offs = append(offs, i)
	}
	b.WriteString(s[prev:])
	return b.String(), append(offs, len(s))
}

// resolved is the character one escapeForm match stands for.
func resolved(esc string) byte {
	switch rest := strings.TrimLeft(esc, `\`); {
	case strings.HasPrefix(rest, "u003"):
		return ':'
	case rest == "u0040":
		return '@'
	default:
		return '/'
	}
}

// maskedSpan is one value a pass over a line masked: its bytes [a, b) in the line as written, the tag and
// number of the placeholder that pass minted for it, and whether the pass read the view. A late rule's span
// (lateSpans) has no number: the late pass mints nothing.
type maskedSpan struct {
	a, b int
	tag  string
	n    int
	view bool
	late bool
}

// errNotRebuilt is maskEscaped's refusal when a pass cannot be read back from its output. Until #142 that
// happened when the line carried, as text, a placeholder the pass then minted for a value. Mask reserves
// every placeholder its input carries and the scratch passes share the reservation, so a pass's output
// holds only tokens it minted and text of the line, and always rebuilds: the view cannot add a placeholder
// the line lacks, because it resolves only to '/', ':' and '@', none of them in ourPH. The check stays as an
// assertion of that argument. An escape resolved to a character ourPH reads could break it, and spans read
// back wrong would be projected onto the wrong bytes, or past the end of the line: with the check removed,
// such a line panics in the projection (measured).
var errNotRebuilt = errors.New("anonymize: a masking pass could not be read back from its output")

// maskEscaped masks a line with an escape in it. The chain runs twice, each time on a scratch Mapper: on s,
// exactly as v0.12.26 masks it, and on its view. What each pass masked is read back from its output, the
// view's spans are projected onto s, and every span of either pass is masked, overlapping ones as one
// value. Each value is minted on m with the bytes s has, so the model never sees the view and Restore
// returns what the log wrote.
//
// The union is what makes the result a superset of v0.12.26 by construction. The view alone masks LESS
// where v0.12.26 read an escape's own characters as part of a value: 4e takes the '\' of a password's
// "1234\/5" for the character that makes its head no port, detector 4 takes "us\u002fer" for a username,
// and the email rule takes "u002fx" for a local part. Resolved, each is a shape that masks less.
//
// A value is tagged as the view's span when it is exactly one, since that is how the line reads unescaped;
// otherwise TOKEN if any span in it is a TOKEN, and failing that the widest span's tag, the view's on a
// tie. Values are minted in the order the view's pass minted them, then the order the line's pass did, so
// an escaped line is numbered as its unescaped form is.
//
// Each scratch Mapper shares m's reservation, so neither pass mints a placeholder the request carries as
// text (#142).
//
// It fails closed three ways: a pass that stops at the host-list cap, a pass whose output and mapping do
// not rebuild its input (an assertion since #142, see errNotRebuilt), and a value the re-scan finds in the
// output or in its view.
func (m *Mapper) maskEscaped(s, view string, offs []int) (string, error) {
	return m.maskUnion(s, view, offs, nil)
}

// maskUnion is maskEscaped with the late rules' spans (GHSA-8qq6-vx8j-rx37), on a line with an escape or
// without one, when offs is nil and the chain runs once. A late span joins the chain's only where the chain
// left a byte of it unmasked: a key the chain masked whole, or inside a larger value it masked, is left as
// the chain wrote it. One that joins makes its value a TOKEN, by valueTag's rule, and is not counted in the
// minting order, so a value is minted where the chain minted a span of it, and a value only a late rule
// read is minted last.
func (m *Mapper) maskUnion(s, view string, offs []int, late []maskedSpan) (string, error) {
	texts := []string{s}
	if offs != nil {
		texts = append(texts, view)
	}
	var spans []maskedSpan
	for pass, in := range texts {
		sc := &Mapper{dets: m.dets, byValue: make(map[string]string), byToken: make(map[string]string), counts: make(map[string]int),
			reserved: m.reserved}
		out, err := sc.chain(in)
		if err != nil {
			return "", err
		}
		got, ok := sc.spansOf(in, out)
		if !ok {
			return "", errNotRebuilt
		}
		for _, sp := range got {
			if pass == 1 {
				sp.a, sp.b, sp.view = offs[sp.a], offs[sp.b], true
			}
			spans = append(spans, sp)
		}
	}
	covered := make([]bool, len(s))
	for _, sp := range spans {
		for i := sp.a; i < sp.b; i++ {
			covered[i] = true
		}
	}
	for _, sp := range late {
		if slices.Contains(covered[sp.a:sp.b], false) {
			spans = append(spans, sp)
		}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].a != spans[j].a {
			return spans[i].a < spans[j].a
		}
		return spans[i].b > spans[j].b
	})

	type value struct {
		a, b         int
		tag          string
		order, first int
	}
	var values []value
	for i := 0; i < len(spans); {
		v := value{a: spans[i].a, b: spans[i].b}
		j := i
		for ; j < len(spans) && spans[j].a < v.b; j++ {
			v.b = max(v.b, spans[j].b)
		}
		members := spans[i:j]
		v.tag, v.order, v.first = valueTag(members, v.a, v.b)
		values = append(values, v)
		i = j
	}

	mint := make([]int, len(values))
	for i := range mint {
		mint[i] = i
	}
	sort.SliceStable(mint, func(i, j int) bool {
		x, y := values[mint[i]], values[mint[j]]
		if x.order != y.order {
			return x.order < y.order
		}
		return x.first < y.first
	})
	tokens := make([]string, len(values))
	for _, i := range mint {
		tokens[i] = m.placeholder(values[i].tag, s[values[i].a:values[i].b])
	}

	var b strings.Builder
	prev := 0
	for i, v := range values {
		b.WriteString(s[prev:v.a])
		b.WriteString(tokens[i])
		prev = v.b
	}
	b.WriteString(s[prev:])
	out := b.String()
	if tag := m.residue(out); tag != "" {
		return "", errSurvived(tag)
	}
	if outView, _ := escapeView(out); outView != out {
		if tag := m.residue(outView); tag != "" {
			return "", errSurvived(tag)
		}
	}
	return out, nil
}

// valueTag is the tag of one value made of members, which cover [a, b), and its place in the minting
// order: 0 with the view's number when a span of the view carries the value's tag, 1 with the line's
// number when only a span of the line does, 2 otherwise.
func valueTag(members []maskedSpan, a, b int) (tag string, order, first int) {
	for _, sp := range members {
		if sp.view && !sp.late && sp.a == a && sp.b == b {
			tag = sp.tag
			break
		}
	}
	if tag == "" {
		w := members[0]
		for _, sp := range members {
			if sp.tag == tagToken {
				tag = tagToken
			}
			if sp.b-sp.a > w.b-w.a || (sp.b-sp.a == w.b-w.a && sp.view && !w.view) {
				w = sp
			}
		}
		if tag == "" {
			tag = w.tag
		}
	}
	order, first = 2, 0
	for _, sp := range members {
		if sp.tag != tag || sp.late {
			continue
		}
		o := 1
		if sp.view {
			o = 0
		}
		if o < order || (o == order && sp.n < first) {
			order, first = o, sp.n
		}
	}
	return tag, order, first
}

// spansOf reads back what one pass of the chain masked: out against the scratch Mapper m, which holds that
// pass's mapping and nothing else. A placeholder m never minted is text of the line and is kept. It reports
// false when the two do not rebuild in, which needs in to carry, as text, a placeholder m minted -- since
// #142, one m was not told to reserve.
func (m *Mapper) spansOf(in, out string) ([]maskedSpan, bool) {
	var spans []maskedSpan
	var rebuilt strings.Builder
	prev := 0
	for _, loc := range ourPHRe.FindAllStringIndex(out, -1) {
		rebuilt.WriteString(out[prev:loc[0]])
		tok := out[loc[0]:loc[1]]
		prev = loc[1]
		orig, ok := m.byToken[tok]
		if !ok {
			rebuilt.WriteString(tok)
			continue
		}
		sep := strings.LastIndexByte(tok, '_')
		n, _ := strconv.Atoi(tok[sep+1 : len(tok)-1])
		spans = append(spans, maskedSpan{a: rebuilt.Len(), b: rebuilt.Len() + len(orig), tag: tok[1:sep], n: n})
		rebuilt.WriteString(orig)
	}
	rebuilt.WriteString(out[prev:])
	return spans, rebuilt.String() == in
}

// errSurvived is Mask's refusal when the re-scan finds a value in its output.
func errSurvived(tag string) error {
	return fmt.Errorf("anonymize: a %s value survived masking", tag)
}
