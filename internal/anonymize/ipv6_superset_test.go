// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/pipeline"
)

// supersetReferenceIPv6Pattern is detector 7's pattern as v0.12.23 shipped it, frozen. It is the
// reference TestGluedRunMasksASupersetOfMain measures against, not a copy that has to follow
// ipv6Pattern: #49 changes ipv6Pattern on purpose, and a change that only masks more keeps the
// gate green. Do not update it when ipv6Pattern changes.
const supersetReferenceIPv6Pattern = `(?i)(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,7}:` +
	`|(?:[0-9a-f]{1,4}:){1,6}:[0-9a-f]{1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,5}(?::[0-9a-f]{1,4}){1,2}` +
	`|(?:[0-9a-f]{1,4}:){1,4}(?::[0-9a-f]{1,4}){1,3}` +
	`|(?:[0-9a-f]{1,4}:){1,3}(?::[0-9a-f]{1,4}){1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,2}(?::[0-9a-f]{1,4}){1,5}` +
	`|[0-9a-f]{1,4}:(?::[0-9a-f]{1,4}){1,6}` +
	`|:(?:(?::[0-9a-f]{1,4}){1,7}|:)`

// referenceDetectors is dets as v0.12.23 built them: detector 7 on the frozen pattern, and the
// IP rule directly before it (7a, #134) removed. Where there is no such rule, nothing is removed.
func referenceDetectors(t *testing.T, dets []detector) []detector {
	t.Helper()
	seven := mustLongest(ipv6Pattern).String()
	at := -1
	for i, d := range dets {
		if d.re.String() == seven {
			at = i
		}
	}
	if at < 0 {
		t.Fatal("detector 7 not found by its pattern")
	}
	var out []detector
	for i, d := range dets {
		switch {
		case i == at-1 && d.tag == tagIP:
			continue
		case i == at:
			d.re = mustLongest(supersetReferenceIPv6Pattern)
		}
		out = append(out, d)
	}
	return out
}

// mapperOn is New with a given detector list.
func mapperOn(dets []detector) *Mapper {
	return &Mapper{
		dets:    dets,
		byValue: make(map[string]string),
		byToken: make(map[string]string),
		counts:  make(map[string]int),
	}
}

// maskedBytes runs a fresh Mapper over in and returns which of in's bytes went into a placeholder.
// It reads that from the output and byToken, and fails if the two do not rebuild the input.
func maskedBytes(t *testing.T, dets []detector, in string) (string, []bool, error) {
	t.Helper()
	m := mapperOn(dets)
	out, err := m.Mask(in)
	if err != nil {
		return "", nil, err
	}
	masked := make([]bool, len(in))
	var rebuilt strings.Builder
	prev := 0
	for _, loc := range ourPHRe.FindAllStringIndex(out, -1) {
		rebuilt.WriteString(out[prev:loc[0]])
		orig, ok := m.byToken[out[loc[0]:loc[1]]]
		if !ok {
			t.Fatalf("output %q carries %q, which this Mapper never issued", out, out[loc[0]:loc[1]])
		}
		for i := rebuilt.Len(); i < min(rebuilt.Len()+len(orig), len(in)); i++ {
			masked[i] = true
		}
		rebuilt.WriteString(orig)
		prev = loc[1]
	}
	rebuilt.WriteString(out[prev:])
	if rebuilt.String() != in {
		t.Fatalf("output and mapping do not rebuild the input:\n in:      %q\n out:     %q\n rebuilt: %q", in, out, rebuilt.String())
	}
	return out, masked, nil
}

// supersetGenerated is the seeded half of the gate's corpus: runs of ':' (1-4), hex groups of 0-8
// characters, '.', '/', '%', '[', ']', letters, digits, '_' and spaces, each core glued to a word,
// glued to a hex-ending word, after '=', and on its own. 250 cores keep the gate to seconds under
// -race; #134's measurement ran the same generator over many more.
func supersetGenerated(cores int) []string {
	r := rand.New(rand.NewPCG(134, 0))
	const (
		hex     = "0123456789abcdefABCDEF"
		letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
		singles = "./%[]_ "
	)
	var out []string
	for range cores {
		var b strings.Builder
		for range 1 + r.IntN(12) {
			switch k := r.IntN(11); k {
			case 0:
				b.WriteString(strings.Repeat(":", 1+r.IntN(4)))
			case 1:
				for range r.IntN(9) {
					b.WriteByte(hex[r.IntN(len(hex))])
				}
			case 2:
				b.WriteByte(letters[r.IntN(len(letters))])
			case 3:
				b.WriteByte(byte('0' + r.IntN(10)))
			default:
				b.WriteByte(singles[k-4])
			}
		}
		core := b.String()
		out = append(out, "peer"+core+" down", "src"+core+" lost", "x="+core+";", " "+core+" ")
	}
	return out
}

// supersetProbes put each character that may end rule 7a's value (#134) right after a ':::' run,
// in front of the values a later rule could read through that character: a home path and a URL
// path after '/', a userinfo and an email local part with '%', a sentence dot before another
// address. The other hand-written rows and the generator rarely build these.
var supersetProbes = []string{
	"route:::1/home/bob/x", "at:::1/home/bob", "postgres://u:::1/x@db/app", "http://:::1/x", "x:::1/24",
	"postgres://u:::1%x@db/app", "postgres://u:p@h/x?y=:::1%z@w", "u:::1%40x@h", "/home/a:::1%x/y", "/home/:::1%x/y",
	"mail a:::1%b@corp.example.com", "via:::fe80%eth0 up",
	"peer:::1. next", "peer:::1.", "peer:::1.\n", "v:::1. 2.3.4 up", "listening on :::22.", "a:::1. b.corp.internal",
	"a:::1.. b", "a:::1.\t10.0.0.5",
}

// supersetCorpus is every input the gate runs: the hand-written rows of this package that carry
// an address or sit next to one, and the generated ones. Each is run raw and templatized.
func supersetCorpus() []string {
	var raw []string
	for _, r := range completenessRows {
		raw = append(raw, payloadOf(r.prefix)+r.secret+payloadOf(r.suffix))
	}
	for _, r := range templatizedRows {
		raw = append(raw, payloadOf(r.text))
	}
	for _, r := range ipv6MustAgree {
		raw = append(raw, r.in)
	}
	for _, r := range ipv6MustDiffer {
		raw = append(raw, r.in)
	}
	for _, r := range gluedRunOverMaskingRows {
		raw = append(raw, r.in)
	}
	for _, r := range gluedRunRightBoundaryRows {
		raw = append(raw, r.in)
	}
	raw = append(raw, supersetProbes...)
	raw = append(raw, supersetGenerated(250)...)
	out := make([]string, 0, 2*len(raw))
	for _, s := range raw {
		tpl, _ := pipeline.Templatize(s)
		out = append(out, s, tpl)
	}
	return out
}

// TestGluedRunMasksASupersetOfMain is the gate #134's rule stands on. For every input, through the
// whole chain and through a Mapper with an extra suffix, the bytes masked now must include every
// byte v0.12.23 masked; an input without ":::" must come back byte for byte as it did; and an
// input v0.12.23 masked without an error must not be refused now. 7a's safety argument is in its
// chain entry. This is the check of it.
func TestGluedRunMasksASupersetOfMain(t *testing.T) {
	corpus := supersetCorpus()
	for _, c := range []struct {
		name string
		dets []detector
	}{
		{"default", baseDetectors},
		{"extra suffix", buildDetectors([]string{"lab"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			ref := referenceDetectors(t, c.dets)
			more := 0
			for _, in := range corpus {
				refOut, refMasked, refErr := maskedBytes(t, ref, in)
				out, masked, err := maskedBytes(t, c.dets, in)
				if refErr != nil {
					continue // the reference sent nothing
				}
				if err != nil {
					t.Errorf("refused now, masked on v0.12.23:\n in:  %q\n ref: %q\n err: %v", in, refOut, err)
					continue
				}
				if !strings.Contains(in, ":::") && out != refOut {
					t.Errorf("no \":::\" in the input, yet the output moved:\n in:  %q\n ref: %q\n got: %q", in, refOut, out)
				}
				strictly := false
				for i := range in {
					if refMasked[i] && !masked[i] {
						t.Errorf("byte %d (%q) was masked on v0.12.23 and is sent now:\n in:  %q\n ref: %q\n got: %q",
							i, in[i], in, refOut, out)
						break
					}
					if masked[i] && !refMasked[i] {
						strictly = true
					}
				}
				if strictly {
					more++
				}
			}
			t.Logf("%d inputs, %d mask strictly more than v0.12.23", len(corpus), more)
		})
	}
}
