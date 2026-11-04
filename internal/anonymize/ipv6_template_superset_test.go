// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// ipv6TemplateGenerated builds n lines from a fixed PCG seed: the shapes #135 names, an address the template
// fragments glued to word characters, one-group forms, the loopback, a zone, a prefix, a port and upper
// case, standing alone and in the frames that make a later rule own the bytes before it. 7c starts left of
// 7 in each, which is where a chain entry cut a value.
func ipv6TemplateGenerated(n int) []string {
	r := rand.New(rand.NewPCG(135, 1))
	addrs := []string{
		"2001:db8::1", "2a02:c7f:beef:cafe::1", "fe80::", "fe80::1", "::1", "::a1b2", "2001:db8::", "a1::", "a1b2::c3",
		"2001:0db8:85a3:0000:0000:8a2e:0370:7334", "1:2:3:4:5:6:7:8", "0:0:0:0:0:ffff:192.0.2.1", "2001:db8::ffff:192.0.2.1",
		"2001:DB8::1", "FE80::", "::A1B2", "2A02:C7F:BEEF:CAFE::1",
	}
	leads := []string{"peer:", "peer ", "peer.", "peer", "src:", "route ", "id_", "x-", "cafe", "h1", "=", "[", "(", "s3:"}
	tails := []string{"", "/64", "%eth0", ":443", "]:8080", ".", ",", ")", " up", "-x", "_x", "x", ".x"}
	frames := []string{
		"{A}",
		"host.corp.internal:{A}",
		"a.b:{A}",
		"x.svc:{A}",
		"http://a.b:{A}/x",
		"mongodb://h1:27017,{A},h3/db",
		"/home/bob:{A}",
		"/home/bob/{A}",
		"user=bob host={A} port=5",
		`{"msg":"peer {A} closed"}`,
		"x.10.0.0.5.dc:{A}",
		"db1.lab:{A}",
		"mail a@b.example:{A}",
	}
	out := make([]string, 0, n)
	for range n {
		a := addrs[r.IntN(len(addrs))]
		a = leads[r.IntN(len(leads))] + a + tails[r.IntN(len(tails))]
		out = append(out, strings.ReplaceAll(frames[r.IntN(len(frames))], "{A}", a))
	}
	return out
}

// ipv6TemplateCorpus is every input the gate runs. The earlier gates' corpus runs as it stands, since it holds
// each of their inputs raw, templated and escaped. #135's rows, the IPv6 rows and probes of the earlier gates
// and the generator come raw, through Templatize, through TemplatizeLine and in each of the five escaped forms
// of all three, deduplicated.
func ipv6TemplateCorpus() []string {
	out := vendorKeyUnionCorpus()
	seen := make(map[string]bool, len(out))
	for _, s := range out {
		seen[s] = true
	}
	var rows []string
	for _, r := range ipv6TemplateOverMaskingRows {
		rows = append(rows, r.in)
	}
	for _, r := range ipv6OnTheTemplate {
		rows = append(rows, r.in)
	}
	for _, r := range ipv6TemplateGluedRows {
		rows = append(rows, r.lead+r.addr+r.tail)
	}
	for _, r := range ipv6TemplateRightBoundaryRows {
		rows = append(rows, r.in)
	}
	for _, t := range [][]ipv6Row{ipv6MustAgree, ipv6MustDiffer} {
		for _, r := range t {
			rows = append(rows, r.in)
		}
	}
	rows = append(rows, supersetProbes...)
	rows = append(rows, dottedQuadProbes...)
	rows = append(rows, ipv6TemplateGenerated(4000)...)
	for _, s := range rows {
		tpl, _ := pipeline.Templatize(s)
		line, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: s}))
		for _, v := range []string{s, tpl, line} {
			for _, x := range append([]string{v}, escapedForms(v)...) {
				if !seen[x] {
					seen[x] = true
					out = append(out, x)
				}
			}
		}
	}
	return out
}

// ipv6TemplateBytes reads 7c alone, on in and its view, and returns which of in's bytes it reads.
func ipv6TemplateBytes(in string) []bool {
	view, offs := escapeView(in)
	m := Mapper{late: []detector{ipv6TemplateRule}}
	read := make([]bool, len(in))
	for _, sp := range m.lateSpans(in, view, offs) {
		for i := sp.a; i < sp.b; i++ {
			read[i] = true
		}
	}
	return read
}

// TestIPv6TemplateRuleMasksASupersetOfV01236 is the gate #135's rule stands on. Through Mask with New()'s
// chain and one with an extra suffix, New() as it ships (live) against the same chain with the key rules read
// late and 7c neither read nor re-scanned (ref). That ref IS v0.12.36's Mask: 7c is no chain entry, so the
// chain is v0.12.36's, and TestVendorKeyUnionMasksASupersetOfV01229 holds ref to the chain with nothing read
// late. For every input:
//   - one with no <NUM> gives 7c nothing to read, and comes back byte for byte with the same error;
//   - where 7c reads nothing the chain left unmasked, the output is ref's byte for byte, with the same error;
//   - one ref refused is refused, or sent with every byte 7c reads masked, and that is counted;
//   - nothing ref sent is refused, by the re-scan's 7c or by anything else;
//   - every byte ref masked and every byte 7c reads is masked;
//   - a value ref minted that 7c does not touch keeps its bytes and its tag, and its number after renumbering
//     (#142's φ); one it touches lies whole inside one value now;
//   - no placeholder minted for a value is text of the input;
//   - at least one input masks more, so the gate cannot pass empty.
//
// Listed in ci.yml's SUPERSET_GATES; a new superset gate must follow this name pattern and be added there.
func TestIPv6TemplateRuleMasksASupersetOfV01236(t *testing.T) {
	t.Parallel()
	corpus, shipped := ipv6TemplateCorpus(), New()
	for _, c := range []struct {
		name string
		dets []detector
	}{
		{"default", baseDetectors},
		{"extra suffix", buildDetectors([]string{"lab"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel() // the corpus is built once, above, and only read; each input has its own Mappers
			more, retagged, sentNow := 0, 0, 0
			for i, in := range corpus {
				ref, live := mapperOn(c.dets), mapperOn(c.dets)
				ref.late = lateDetectors
				live.late, live.rescan = shipped.late, shipped.rescan
				refOut, refErr := ref.Mask(in)
				out, err := live.Mask(in)
				read := ipv6TemplateBytes(in)
				if !strings.Contains(in, "<NUM>") {
					if anyRead(read, 0, len(in)) || out != refOut || errText(err) != errText(refErr) {
						t.Errorf("input %d has no <NUM> and moved: %s", i, redactedRuns(in))
					}
					continue
				}
				if refErr != nil {
					if err == nil {
						sentNow++
						masked, ok := maskedOf(live, in, out)
						if !ok || !coversRead(masked, read) {
							t.Errorf("input %d, refused on v0.12.36, is sent with a byte 7c reads: %s", i, redactedRuns(in))
						}
					}
					continue
				}
				if err != nil {
					t.Errorf("input %d, clean on v0.12.36, is refused (%v): %s", i, err, redactedRuns(in))
					continue
				}
				refMasked, ok := maskedOf(ref, in, refOut)
				if !ok {
					t.Fatalf("input %d: v0.12.36's output does not rebuild it", i)
				}
				if coversRead(refMasked, read) {
					if out != refOut {
						t.Errorf("input %d, with nothing for 7c to add, moved: %s", i, redactedRuns(in))
					}
					continue
				}
				more++
				for tok := range live.byToken {
					if strings.Contains(in, tok) {
						t.Errorf("input %d: minted %s, which the input carries as text", i, tok)
					}
				}
				masked, ok := maskedOf(live, in, out)
				if !ok {
					t.Errorf("input %d: the output does not rebuild the input: %s", i, redactedRuns(in))
					continue
				}
				if !coversRead(masked, refMasked) || !coversRead(masked, read) {
					t.Errorf("input %d sends a byte v0.12.36 masked or 7c reads: %s", i, redactedRuns(in))
					continue
				}
				refSpans, _ := ref.spansOf(in, refOut)
				liveSpans, _ := live.spansOf(in, out)
				for _, rsp := range refSpans {
					if anyRead(read, rsp.a, rsp.b) {
						whole := false
						for _, ls := range liveSpans {
							if ls.a <= rsp.a && rsp.b <= ls.b {
								whole = true
								retagged += btoi(ls.tag != rsp.tag)
							}
						}
						if !whole {
							t.Errorf("input %d: a value 7c touches is cut: %s", i, redactedRuns(in))
						}
						continue
					}
					v := in[rsp.a:rsp.b]
					liveTok, kept := live.byValue[v]
					refTok := ref.byValue[v]
					if !kept {
						t.Errorf("input %d: a value 7c does not touch is gone: %s", i, redactedRuns(in))
						continue
					}
					if got, ok := renumbered(live, ref, liveTok); !ok || got != refTok {
						t.Errorf("input %d: a value 7c does not touch changed its tag or number: %s", i, redactedRuns(in))
					}
				}
			}
			t.Logf("%d inputs: %d masked more on the branch, %d values taken into a value of another tag, "+
				"%d refused on v0.12.36 and sent now", len(corpus), more, retagged, sentNow)
			if more == 0 {
				t.Error("no input masked more: the gate proved nothing")
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestIPv6TemplateRescanRefusesWhatTheUnionMisses is the fail-closed path, with the union made to miss: a
// Mapper that reads 7c on the re-scan and not in the union. A form the chain leaves whole has 7c's shape in
// the output, or in its view, and Mask refuses. A form with a "::" does not: 7 has rewritten the "::" and
// what is left is #41's shape, so this mutant sends its letters. Those rows say so, because the re-scan does
// not cover them and nothing should read as if it did. What covers them is the union and the gate above.
func TestIPv6TemplateRescanRefusesWhatTheUnionMisses(t *testing.T) {
	escaped := func(s string) string { return strings.ReplaceAll(s, ":", `\`+"u003a") }
	for _, c := range []struct {
		name    string
		in      string
		refused bool
	}{
		{"eight groups", "peer <NUM>:db<NUM>:<NUM>:<NUM>:<NUM>:<NUM>:<NUM>:<NUM> x", true},
		{"eight groups, glued", "peer:<NUM>:db<NUM>:<NUM>:<NUM>:<NUM>:<NUM>:<NUM>:<NUM> x", true},
		{"six groups and a quad", "peer <NUM>:<NUM>:<NUM>:<NUM>:<NUM>:ffff:<IP> down", true},
		{"eight groups, seen only on the view", "peer " + escaped("<NUM>:db<NUM>:<NUM>:<NUM>:<NUM>:<NUM>:<NUM>:<NUM>") + " x", true},
		{"one group", "route fe<NUM>::/<NUM> dev", false},
		{"glued", "peer:<NUM>:db<NUM>::<NUM> closed", false},
		{"loopback", "bind ::<NUM> port", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			mutant := New()
			mutant.late = lateDetectors
			_, err := mutant.Mask(c.in)
			if want := errSurvived(tagIP); c.refused && (err == nil || err.Error() != want.Error()) {
				t.Errorf("Mask(%q) = %v, want the re-scan's refusal", c.in, err)
			}
			if !c.refused && err != nil {
				t.Errorf("Mask(%q) = %v: the re-scan now reads this form, so this row and Mask's note are stale", c.in, err)
			}
			if _, err := New().Mask(c.in); err != nil {
				t.Errorf("New().Mask(%q) = %v, want it masked and sent", c.in, err)
			}
		})
	}
}
