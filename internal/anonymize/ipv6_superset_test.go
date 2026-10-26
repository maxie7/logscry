// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/pipeline"
)

// supersetReferenceIPv6Pattern is detector 7's pattern as v0.12.23 shipped it, frozen. It is the
// reference TestGluedRunMasksASupersetOfMain measures against, not a copy that has to follow
// ipv6Pattern: a change that only masks more keeps the gate green. Do not update it when
// ipv6Pattern changes.
const supersetReferenceIPv6Pattern = `(?i)(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,7}:` +
	`|(?:[0-9a-f]{1,4}:){1,6}:[0-9a-f]{1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,5}(?::[0-9a-f]{1,4}){1,2}` +
	`|(?:[0-9a-f]{1,4}:){1,4}(?::[0-9a-f]{1,4}){1,3}` +
	`|(?:[0-9a-f]{1,4}:){1,3}(?::[0-9a-f]{1,4}){1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,2}(?::[0-9a-f]{1,4}){1,5}` +
	`|[0-9a-f]{1,4}:(?::[0-9a-f]{1,4}){1,6}` +
	`|:(?:(?::[0-9a-f]{1,4}){1,7}|:)`

// ipv6PatternAtV01224 and ipv6GluedRunPatternAtV01224 are detector 7's and rule 7a's patterns as
// v0.12.24 shipped them, frozen, for TestDottedQuadMasksASupersetOfV01224 (#49). Superset is not
// transitive across releases, so each release a rule moves is its own reference. Do not update
// either when the live patterns change.
const (
	ipv6PatternAtV01224 = `(?i)(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}` +
		`|(?:[0-9a-f]{1,4}:){1,7}:` +
		`|(?:[0-9a-f]{1,4}:){1,6}:[0-9a-f]{1,4}` +
		`|(?:[0-9a-f]{1,4}:){1,5}(?::[0-9a-f]{1,4}){1,2}` +
		`|(?:[0-9a-f]{1,4}:){1,4}(?::[0-9a-f]{1,4}){1,3}` +
		`|(?:[0-9a-f]{1,4}:){1,3}(?::[0-9a-f]{1,4}){1,4}` +
		`|(?:[0-9a-f]{1,4}:){1,2}(?::[0-9a-f]{1,4}){1,5}` +
		`|[0-9a-f]{1,4}:(?::[0-9a-f]{1,4}){1,6}` +
		`|:(?:(?::[0-9a-f]{1,4}){1,7}|:)`
	ipv6GluedRunPatternAtV01224 = `(?i)(` +
		`(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*)?:{3,}` +
		`(?:(?:[0-9a-f]|<(?:TS|UUID|IP|HEX|NUM|STR)>){1,4}(?::(?:[0-9a-f]|<(?:TS|UUID|IP|HEX|NUM|STR)>){1,4})*)?` +
		`)(?:[\s,;)\]}>"'/%]|\.(?:\s|$)|$)`
)

// supersetReference is one release's IPv6 rules: detector 7's pattern, and rule 7a's where the
// release had it.
type supersetReference struct {
	release       string
	seven, sevenA string
}

var (
	refV01223 = supersetReference{"v0.12.23", supersetReferenceIPv6Pattern, ""}
	refV01224 = supersetReference{"v0.12.24", ipv6PatternAtV01224, ipv6GluedRunPatternAtV01224}
)

// TestDetector7UnchangedAtV01224 states what the two frozen copies of detector 7 say: #134 added 7a
// and left 7 alone, so v0.12.23 and v0.12.24 shipped one pattern.
func TestDetector7UnchangedAtV01224(t *testing.T) {
	if ipv6PatternAtV01224 != supersetReferenceIPv6Pattern {
		t.Error("detector 7's frozen v0.12.24 pattern differs from v0.12.23's")
	}
}

// referenceDetectors is dets as ref's release built them: every IP rule directly before detector 7
// (7a since #134, 7b since #49) removed, ref's 7a put back where it has one, and 7 on ref's pattern.
func referenceDetectors(t *testing.T, dets []detector, ref supersetReference) []detector {
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
	first := at
	for first > 0 && dets[first-1].tag == tagIP {
		first--
	}
	out := append([]detector{}, dets[:first]...)
	if ref.sevenA != "" {
		out = append(out, detector{tagIP, mustLongest(ref.sevenA), 1, true, false})
	}
	d := dets[at]
	d.re = mustLongest(ref.seven)
	out = append(out, d)
	return append(out, dets[at+1:]...)
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

// dottedQuadProbes put each character that may end rule 7b's value (#49), and a port, right after a
// dotted quad, in front of the values a later rule could read through it, glued and not: a fifth
// octet (8), a private name and a hyphenated one (9b), an address glued on by ':' (7), a URL scheme
// (9a), a home path, a userinfo, a host-list member (9c) and a name before the address (9d).
var dottedQuadProbes = []string{
	"peer ::ffff:10.0.0.5.6 down", "peer ::ffff:10.0.0.5.corp.internal down", "peer ::ffff:10.0.0.5-x.corp.internal down",
	"peer ::ffff:10.0.0.5:ab::1 down", "peer ::ffff:10.0.0.5.http://h.example/x", "at ::ffff:10.0.0.5/home/bob/x",
	"postgres://u::ffff:10.0.0.5%x@db/app", "dsn=mongodb://h1,::ffff:10.0.0.5:27017,h3/db",
	"dsn=mongodb://h1,[::ffff:10.0.0.5]:27017,h3/db", "peer ::ffff:10.0.0.5. next", "peer ::ffff:10.0.0.5.",
	"v ::ffff:10.0.0.5. 2.3.4.5 up", "peer ::ffff:10.0.0.5:443:x", "peer ::ffff:10.0.0.5:443.6",
	"a ::ffff:10.0.0.5.lab x", "db.::ffff:10.0.0.5 up", "db.0:0:0:0:0:ffff:10.0.0.5 up",
	"db.lab0:0:0:0:0:ffff:10.0.0.5 up", "x ::ffff:10.0.0.5:443.corp.internal", "peer:::ffff:10.0.0.5.6 down",
	"peer:::ffff:10.0.0.5:ab::1 down", "u:::ffff:10.0.0.5%40x@h", "worker-::ffff:10.0.0.5.corp.internal up",
}

// dottedQuadGroupCountProbes reach 7b's no-"::" form and its left edge: six, seven and eight hex
// groups before a quad (the form needs six, and with seven or more 7 starts on the first group), the
// same counts with a "::" in the prefix, and a prefix longer than any address. Each address goes
// unglued, after "peer:" and after a word ending in a hex character.
func dottedQuadGroupCountProbes() []string {
	addrs := []string{
		"1:2:3:4:5:6:10.0.0.5", "1:2:3:4:5:6:7:10.0.0.5", "1:2:3:4:5:6:7:8:10.0.0.5",
		"1:2::3:4:5:6:10.0.0.5", "1:2:3::4:5:6:7:10.0.0.5", "1:2:3:4::5:6:7:8:10.0.0.5", "::1:2:3:4:5:6:10.0.0.5",
		"1:2:3:4:5:6:7:8:9:10.0.0.5", "1:2:3:4:5:6:7:8:9:a:b:c:10.0.0.5", "1:2:3:4:5:6:7:8::9:10.0.0.5",
	}
	var out []string
	for _, a := range addrs {
		out = append(out, "peer "+a+" down", "peer:"+a+" down", "src"+a+" lost")
	}
	return out
}

// supersetGeneratedDottedQuads is the seeded half of #49's corpus, a second generator so the first
// one's numbers stay comparable: colon runs (1-3), runs of 0-8 hex groups joined by ':', a dotted quad
// (valid, an octet out of range, a leading zero, three octets or five), a port, a dotted name, a
// hyphen or '_' word, a letter, and the characters that may end a value or continue one, in the same
// four framings as supersetGenerated.
func supersetGeneratedDottedQuads(cores int) []string {
	r := rand.New(rand.NewPCG(49, 0))
	const (
		hex    = "0123456789abcdefABCDEF"
		ends   = " ,;)]}>\"'/%.[=@*:"
		octets = "0 1 5 9 10 99 100 192 199 200 249 250 255"
	)
	valid := strings.Fields(octets)
	octet := func() string { return valid[r.IntN(len(valid))] }
	quad := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = octet()
		}
		return strings.Join(parts, ".")
	}
	names := []string{".example", ".corp.internal", ".lab", ".6", ".x", "-x", "_x", "-x.corp.internal"}
	var out []string
	for range cores {
		var b strings.Builder
		for range 1 + r.IntN(8) {
			switch r.IntN(12) {
			case 0:
				b.WriteString(strings.Repeat(":", 1+r.IntN(3)))
			case 1, 2:
				for range r.IntN(9) {
					for range 1 + r.IntN(4) {
						b.WriteByte(hex[r.IntN(len(hex))])
					}
					b.WriteByte(':')
				}
			case 3, 4:
				b.WriteString(quad(4))
			case 5:
				q := strings.Split(quad(4), ".")
				q[r.IntN(4)] = []string{"256", "300", "999"}[r.IntN(3)]
				b.WriteString(strings.Join(q, "."))
			case 6:
				q := strings.Split(quad(4), ".")
				q[r.IntN(4)] = "0" + octet()
				b.WriteString(strings.Join(q, "."))
			case 7:
				b.WriteString(quad(3 + 2*r.IntN(2)))
			case 8:
				b.WriteString(":" + strconv.Itoa(r.IntN(70000)))
			case 9:
				b.WriteString(names[r.IntN(len(names))])
			case 10:
				b.WriteByte(byte('a' + r.IntN(26)))
			default:
				b.WriteByte(ends[r.IntN(len(ends))])
			}
		}
		core := b.String()
		out = append(out, "peer"+core+" down", "src"+core+" lost", "x="+core+";", " "+core+" ")
	}
	return out
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
	for _, r := range dottedQuadOverMaskingRows {
		raw = append(raw, r.in)
	}
	for _, r := range dottedQuadRightBoundaryRows {
		raw = append(raw, r.in)
	}
	raw = append(raw, supersetProbes...)
	raw = append(raw, dottedQuadProbes...)
	raw = append(raw, dottedQuadGroupCountProbes()...)
	raw = append(raw, supersetGenerated(250)...)
	raw = append(raw, supersetGeneratedDottedQuads(250)...)
	out := make([]string, 0, 2*len(raw))
	for _, s := range raw {
		tpl, _ := pipeline.Templatize(s)
		out = append(out, s, tpl)
	}
	return out
}

// dottedQuadAfterColon finds the one thing #49's two rules need: a dotted quad, raw or as the
// pipeline's <IP>, straight after a ':'. An input without it is one neither rule can touch.
var dottedQuadAfterColon = regexp.MustCompile(`:(?:[0-9]{1,3}\.){3}[0-9]|:<IP>`)

// TestGluedRunMasksASupersetOfMain is the gate #134's rule stands on. For every input, through the
// whole chain and through a Mapper with an extra suffix, the bytes masked now must include every
// byte v0.12.23 masked; an input with no ":::" and no dotted quad after a ':' must come back byte
// for byte as it did; and an input v0.12.23 masked without an error must not be refused now. 7a's
// safety argument is in its chain entry. This is the check of it.
// Listed in ci.yml's SUPERSET_GATES; a new superset gate must follow this name pattern and be added there.
func TestGluedRunMasksASupersetOfMain(t *testing.T) {
	assertMasksASuperset(t, refV01223, func(in string) bool {
		return !strings.Contains(in, ":::") && !dottedQuadAfterColon.MatchString(in)
	})
}

// TestDottedQuadMasksASupersetOfV01224 is the gate #49's rules stand on, against the release before
// them: 7b and 7a's dotted-quad alternative may mask more than v0.12.24, never less, and an input
// with no dotted quad after a ':' comes back byte for byte.
// Listed in ci.yml's SUPERSET_GATES; a new superset gate must follow this name pattern and be added there.
func TestDottedQuadMasksASupersetOfV01224(t *testing.T) {
	assertMasksASuperset(t, refV01224, func(in string) bool {
		return !dottedQuadAfterColon.MatchString(in)
	})
}

// assertMasksASuperset runs the corpus through ref's chain and the current one, for the default
// Mapper and one with an extra suffix. untouched says which inputs must come back unchanged.
func assertMasksASuperset(t *testing.T, ref supersetReference, untouched func(string) bool) {
	corpus := supersetCorpus()
	for _, c := range []struct {
		name string
		dets []detector
	}{
		{"default", baseDetectors},
		{"extra suffix", buildDetectors([]string{"lab"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			refDets := referenceDetectors(t, c.dets, ref)
			more := 0
			for _, in := range corpus {
				refOut, refMasked, refErr := maskedBytes(t, refDets, in)
				out, masked, err := maskedBytes(t, c.dets, in)
				if refErr != nil {
					continue // the reference sent nothing
				}
				if err != nil {
					t.Errorf("refused now, masked on %s:\n in:  %q\n ref: %q\n err: %v", ref.release, in, refOut, err)
					continue
				}
				if untouched(in) && out != refOut {
					t.Errorf("an input no new rule can touch, yet the output moved:\n in:  %q\n ref: %q\n got: %q", in, refOut, out)
				}
				strictly := false
				for i := range in {
					if refMasked[i] && !masked[i] {
						t.Errorf("byte %d (%q) was masked on %s and is sent now:\n in:  %q\n ref: %q\n got: %q",
							i, in[i], ref.release, in, refOut, out)
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
			t.Logf("%d inputs, %d mask strictly more than %s", len(corpus), more, ref.release)
		})
	}
}
