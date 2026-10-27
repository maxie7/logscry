// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/pipeline"
)

// maskUnreserved is Mask as v0.12.27 shipped it: the same body with nothing reserved, so the chain mints
// a placeholder whatever the line carries as text. #142's fix changed nothing below Mask but the
// reservation placeholder consults and the rebuild error's name, so this is v0.12.27 over the same code.
// It is the reference the gate below measures against; do not update it.
func maskUnreserved(m *Mapper, s string) (string, error) {
	view, offs := escapeView(s)
	if offs == nil {
		return m.maskText(s)
	}
	return m.maskEscaped(s, view, offs)
}

// renumbered maps live's output onto ref's numbering: each token live minted becomes the token ref minted
// for the same value, and any other token, text of the input, stays. It reports false if ref never minted
// such a value or gave it another tag.
func renumbered(live, ref *Mapper, out string) (string, bool) {
	ok := true
	got := ourPHRe.ReplaceAllStringFunc(out, func(tok string) string {
		v, minted := live.byToken[tok]
		if !minted {
			return tok
		}
		r, seen := ref.byValue[v]
		if !seen || r[:strings.LastIndexByte(r, '_')] != tok[:strings.LastIndexByte(tok, '_')] {
			ok = false
		}
		return r
	})
	return got, ok
}

// maskedOf returns which of in's bytes went into a placeholder m minted, with a token m never issued read
// as text of the line, and whether out and m's mapping rebuild in.
func maskedOf(m *Mapper, in, out string) ([]bool, bool) {
	spans, ok := m.spansOf(in, out)
	masked := make([]bool, len(in))
	if !ok {
		return masked, false
	}
	for _, sp := range spans {
		for i := sp.a; i < sp.b; i++ {
			masked[i] = true
		}
	}
	return masked, true
}

// placeholderTextGenerated builds n lines from a fixed PCG seed: literal placeholders of every tag, any of
// the first few numbers and a few wider shapes, before, after, between and glued to values the chain masks,
// in prose, a JSON message and logfmt. Each comes back raw and in every escaped form the view resolves.
func placeholderTextGenerated(n int) []string {
	r := rand.New(rand.NewPCG(14, 2))
	tags := []string{tagIP, tagEmail, tagToken, tagUUID, tagHost, tagUser}
	literal := func() string {
		switch r.IntN(10) {
		case 0:
			return "<" + tags[r.IntN(len(tags))] + "_0" + string(rune('1'+r.IntN(3))) + ">"
		case 1:
			return "<" + tags[r.IntN(len(tags))] + "_1" + string(rune('0'+r.IntN(10))) + ">"
		default:
			return "<" + tags[r.IntN(len(tags))] + "_" + string(rune('1'+r.IntN(4))) + ">"
		}
	}
	values := []string{
		"db.corp.internal", "cache.acme.internal", "api-gw7.prod.acme.com", "10.0.0.5", "192.168.1.20",
		"2001:db8::dead:beef", "bob@corp.com", "alice.ops@acme.io", "550e8400-e29b-41d4-a716-446655440000",
		"/home/deploy/x", "/Users/bob/src", "postgres://svcuser:pwsecret@pghost/db", "redis://:s3cr3tpw@cache.acme.internal:6379/0",
		"https://api.acme.com/v1", "mongodb://h1.corp.internal:27017,h2.corp.internal/db",
	}
	words := []string{"login", "from", "retry", "as", "peer", "down", "connect", "failed", "x"}
	glue := []string{" ", " ", " ", "", "=", ":", "/", ","}
	var lines []string
	for range n {
		var b strings.Builder
		for k := 2 + r.IntN(5); k > 0; k-- {
			switch r.IntN(3) {
			case 0:
				b.WriteString(values[r.IntN(len(values))])
			case 1:
				b.WriteString(literal())
			default:
				b.WriteString(words[r.IntN(len(words))])
			}
			b.WriteString(glue[r.IntN(len(glue))])
		}
		s := strings.TrimSpace(b.String())
		switch r.IntN(3) {
		case 1:
			s = `{"level":"error","msg":"` + strings.ReplaceAll(s, `"`, `\"`) + `"}`
		case 2:
			s = `level=error msg="` + strings.ReplaceAll(s, `"`, `\"`) + `"`
		}
		lines = append(lines, s)
		for _, f := range jsonEscapeForms {
			if e := f.esc(s); e != s {
				lines = append(lines, e)
			}
		}
	}
	return lines
}

// placeholderTextCorpus is the JSON-escape gate's corpus, which holds every earlier gate's, with the #142
// rows and the generator, each raw, through Templatize and through TemplatizeLine.
func placeholderTextCorpus() []string {
	out := jsonEscapeCorpus()
	var rows []string
	for _, r := range placeholderTextRows {
		rows = append(rows, r.in)
		for _, f := range jsonEscapeForms {
			rows = append(rows, f.esc(r.in))
		}
	}
	for _, r := range placeholderTextTemplateRows {
		rows = append(rows, r.raw)
	}
	rows = append(rows, placeholderTextEscapedLine)
	rows = append(rows, jsonEscapePlaceholderTextRows...)
	rows = append(rows, placeholderTextGenerated(1000)...)
	for _, s := range rows {
		tpl, _ := pipeline.Templatize(s)
		out = append(out, s, tpl, templateOf(s))
	}
	return out
}

// TestPlaceholderReservationMasksASupersetOfV01227 is the gate #142's fix stands on. For every input,
// through Mask with New()'s chain and one with an extra suffix, the late pass off (the late rules are
// TestVendorKeyUnionMasksASupersetOfV01229's), against maskUnreserved:
//   - an input that carries no placeholder of ours as text comes back byte for byte, with the same error;
//   - one that does, and that v0.12.27 sent, comes back with the same bytes masked as the same values
//     under the same tags: only the numbering moved;
//   - one v0.12.27 refused because a pass could not be read back is sent, and masks every byte the line's
//     own pass masks (the JSON-escape gate's claim, on a Mapper with the line reserved);
//   - nothing v0.12.27 sent is refused, and every other refusal is v0.12.27's own, but where its rebuild
//     check refused first: there the re-scan may refuse now, and nothing is sent either way;
//   - no placeholder minted for a value is text of the input.
//
// It and its two Mappers run in parallel with the other superset gates: the corpus is built once, in the
// gate, and only read, and each input has its own Mappers.
//
// Listed in ci.yml's SUPERSET_GATES; a new superset gate must follow this name pattern and be added there.
func TestPlaceholderReservationMasksASupersetOfV01227(t *testing.T) {
	t.Parallel()
	corpus := placeholderTextCorpus()
	for _, c := range []struct {
		name string
		dets []detector
	}{
		{"default", baseDetectors},
		{"extra suffix", buildDetectors([]string{"lab"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			carrying, renum, sentNow, stillRefused := 0, 0, 0, 0
			for _, in := range corpus {
				ref, live := mapperOn(c.dets), mapperOn(c.dets)
				refOut, refErr := maskUnreserved(ref, in)
				out, err := live.Mask(in)
				if !ourPHRe.MatchString(in) {
					if out != refOut || errText(err) != errText(refErr) {
						t.Errorf("an input with no placeholder as text moved:\n in:  %q\n ref: %q (%v)\n got: %q (%v)", in, refOut, refErr, out, err)
					}
					continue
				}
				carrying++
				if err != nil {
					switch {
					case refErr == nil:
						t.Errorf("refused now, sent on v0.12.27:\n in:  %q\n ref: %q\n err: %v", in, refOut, err)
					case errors.Is(err, errNotRebuilt):
						t.Errorf("the rebuild check fired through Mask, which reserves what it would mint:\n in: %q", in)
					case errors.Is(refErr, errNotRebuilt):
						stillRefused++ // the rebuild check no longer stands before what the re-scan finds
					case errText(err) != errText(refErr):
						t.Errorf("refused now, not as v0.12.27 refused:\n in:  %q\n ref: %v\n err: %v", in, refErr, err)
					}
					continue
				}
				for tok, v := range live.byToken {
					if strings.Contains(in, tok) {
						t.Errorf("minted %s for %q, and the input carries %s as text:\n in: %q", tok, v, tok, in)
					}
				}
				masked, ok := maskedOf(live, in, out)
				if !ok {
					t.Errorf("the output does not rebuild the input:\n in:  %q\n got: %q", in, out)
					continue
				}
				switch {
				case refErr == nil:
					if got, ok := renumbered(live, ref, out); !ok || got != refOut {
						t.Errorf("more than the numbering moved:\n in:          %q\n ref:         %q\n got:         %q\n renumbered:  %q", in, refOut, out, got)
					} else if out != refOut {
						renum++
					}
				case errors.Is(refErr, errNotRebuilt):
					sentNow++
					line := mapperOn(c.dets)
					line.reserve(in)
					lineOut, lineErr := maskAtV01226(line, in)
					if lineErr != nil {
						continue // the line's own pass refuses; the view masked what it hid
					}
					lineMasked, ok := maskedOf(line, in, lineOut)
					if !ok {
						t.Errorf("the line's own pass does not rebuild the input:\n in: %q\n out: %q", in, lineOut)
						continue
					}
					for i := range in {
						if lineMasked[i] && !masked[i] {
							t.Errorf("byte %d (%q) is masked by the line's own pass and sent now:\n in:  %q\n line: %q\n got: %q", i, in[i], in, lineOut, out)
							break
						}
					}
				default:
					t.Errorf("sent now, refused on v0.12.27 for another reason:\n in:  %q\n err: %v\n got: %q", in, refErr, out)
				}
			}
			t.Logf("%d inputs, %d carry a placeholder as text: %d renumbered, %d refused on v0.12.27 and sent now, %d refused on both",
				len(corpus), carrying, renum, sentNow, stillRefused)
		})
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
