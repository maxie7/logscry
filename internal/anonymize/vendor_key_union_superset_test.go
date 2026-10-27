// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// vendorKeyInnerTagGenerated builds n lines from a fixed PCG seed: a key of every rule-12 family, and an AWS
// secret access key after its name or its id, whose body holds a shape an earlier rule tags -- an sk key
// after '-' or '_', a digit, a prefix and a digit (a NUM pair on the template), another family's key, an
// access key id, a private suffix, a JWT, a UUID, a dotted quad, a home directory -- in prose, an env var,
// a JSON value and message, logfmt, YAML and a URL query, unglued or glued to a word on either side,
// sometimes beside a second key or a value the chain masks.
func vendorKeyInnerTagGenerated(n int) []string {
	r := rand.New(rand.NewPCG(8, 0x8a37))
	const (
		an   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
		b64  = an + "_-"
		hexl = "0123456789abcdef"
		upd  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		aws  = an + "/+"
	)
	rs := func(a string, k int) string {
		var b strings.Builder
		for range k {
			b.WriteByte(a[r.IntN(len(a))])
		}
		return b.String()
	}
	pick := func(xs []string) string { return xs[r.IntN(len(xs))] }
	d := func() string { return string(rune('1' + r.IntN(9))) }
	// Each shape is the text an earlier rule tags, and the characters the body must admit around it.
	shapes := []struct {
		need string
		gen  func() string
	}{
		{"-", func() string { return "-s" + "k-" + rs(an, 16) }},
		{"_-", func() string { return "_s" + "k-" + rs(an, 16) }},
		{"", func() string { return d() + "s" + "k" + d() + "Ab" + rs(an, 4) }},
		{"", func() string { return d() + "xo" + "xb" + d() + "Ab" + rs(an, 4) }},
		{"", func() string { return d() + "xa" + "pp" + d() + "Ab" + rs(an, 4) }},
		{"-", func() string { return "-xo" + "xb-" + rs("0123456789", 10) + "-" + rs(an, 16) }},
		{"_", func() string { return "_s" + "k_test_" + rs(an, 24) }},
		{"_", func() string { return d() + "s" + "k_live_" + rs(an, 6) + d() + rs(an, 6) }},
		{"_", func() string { return "_g" + "hp_" + rs(an, 36) }},
		{"_", func() string { return "_git" + "hub_pat_" + rs(an, 82) }},
		{"-", func() string { return "-AI" + "za" + rs(an, 35) }},
		{"", func() string { return d() + "AI" + "za" + rs(an, 12) + d() + rs(an, 4) }},
		{"-", func() string { return "-py" + "pi-AgE" + rs(an, 82) }},
		{"-", func() string { return "-gl" + "pat-" + rs(an, 20) }},
		{"-", func() string { return d() + "gl" + "pat-" + rs(an, 7) + d() }},
		{".-", func() string { return "-S" + "G." + rs(an, 22) + "." + rs(an, 43) }},
		{"_", func() string { return "_np" + "m_" + rs(an, 36) }},
		{"_", func() string { return d() + "h" + "f_" + rs(an, 12) + d() }},
		{"_", func() string { return "_sh" + "pat_" + rs(an, 32) }},
		{"-", func() string { return "-S" + "K" + rs(hexl, 32) + "-" }},
		{"", func() string { return d() + "S" + "K" + "a" + d() + "bc" + d() }},
		{"/", func() string { return "/A" + "C" + rs(hexl, 32) + "/" }},
		{"/", func() string { return "/S" + "K" + "a" + d() + "bc" }},
		{"", func() string { return "x" + "AK" + "IA" + rs(upd, 16) + "x" }},
		{"", func() string { return "x" + "AS" + "IA" + d() + "QZ" }},
		{".-", func() string { return "." + pick(defaultHostSuffixes) + "-x" }},
		{".", func() string { return "." + pick(defaultHostSuffixes) + d() + "x" }},
		{".-", func() string { return "-ey" + "J" + rs(an, 10) + ".ey" + "J" + rs(an, 10) + "." + rs(an, 10) + "-" }},
		{"-", func() string {
			return "-" + rs(hexl, 8) + "-" + rs(hexl, 4) + "-" + rs(hexl, 4) + "-" + rs(hexl, 4) + "-" + rs(hexl, 12) + "-"
		}},
		{".-", func() string { return "-ab.10.0.0." + d() + ".cd-" }},
		{"/", func() string { return "/home/" + pick([]string{"bob", "deploy", "ci"}) + "/" }},
	}
	// Each family is its prefix, a body over its alphabet and the place a shape may go in it; a body of an
	// exact length (Twilio, the AWS secret) takes the shape in place of as many characters.
	gl := []string{"pat", "oas", "dt", "rtr", "rt", "cbt", "ptt", "ft", "imt", "agent", "wt", "soat", "ffct"}
	families := []func() (pre, body, alpha, ctx string, exact bool){
		func() (string, string, string, string, bool) {
			return "s" + "k_" + pick([]string{"live", "test"}) + "_", rs(an, 24+r.IntN(8)), an, "", false
		},
		func() (string, string, string, string, bool) {
			return "g" + "h" + pick([]string{"p", "o", "u", "s", "r"}) + "_", rs(an, 36), an, "", false
		},
		func() (string, string, string, string, bool) {
			return "git" + "hub_pat_", rs(an+"_", 82), an + "_", "", false
		},
		func() (string, string, string, string, bool) {
			return "xo" + "xb-" + rs("0123456789", 10) + "-" + rs("0123456789", 13) + "-", rs(an, 24), an + "-", "", false
		},
		func() (string, string, string, string, bool) { return "AI" + "za", rs(b64, 35), b64, "", false },
		func() (string, string, string, string, bool) {
			return "py" + "pi-AgE", rs(b64, 82+r.IntN(40)), b64, "", false
		},
		func() (string, string, string, string, bool) {
			return "g" + "l" + pick(gl) + "-", rs(b64+".", 19+r.IntN(20)) + rs(an, 1), b64 + ".", "", false
		},
		func() (string, string, string, string, bool) {
			return "S" + "G.", rs(b64, 22) + "." + rs(b64, 43), b64 + ".", "", false
		},
		func() (string, string, string, string, bool) { return "np" + "m_", rs(an, 36), an, "", false },
		func() (string, string, string, string, bool) { return "h" + "f_", rs(an, 34), an, "", false },
		func() (string, string, string, string, bool) {
			return "sh" + "p" + pick([]string{"at", "ca", "pa", "ss"}) + "_", rs(an, 32), an, "", false
		},
		func() (string, string, string, string, bool) {
			return "", rs(aws, 40), aws, pick([]string{"AWS_SECRET_ACCESS_KEY=", "aws_secret_access_key = ", "secretAccessKey: "}), true
		},
		func() (string, string, string, string, bool) {
			return "", rs(aws, 40), aws, "AK" + "IA" + rs(upd, 16) + pick([]string{" ", ",", "; "}), true
		},
	}
	key := func() string {
		for {
			pre, body, alpha, ctx, exact := families[r.IntN(len(families))]()
			s := shapes[r.IntN(len(shapes))]
			sh := s.gen()
			fits := true
			for _, c := range sh {
				fits = fits && strings.ContainsRune(alpha, c)
			}
			if !fits || len(sh) >= len(body) {
				continue
			}
			p := r.IntN(len(body) - len(sh))
			if exact {
				return ctx + pre + body[:p] + sh + body[p+len(sh):]
			}
			return ctx + pre + body[:p] + sh + body[p:]
		}
	}
	values := []string{
		"postgres://svcuser:pwsecret@pghost/db", "bob@corp.com", "db.corp.internal", "10.0.0.5",
		stripeLiveSecret, googleAPISecret, awsAccessKeyID + " " + awsSecretKey, "/home/deploy/x",
	}
	frames := []string{
		"key {K} rejected", "TOKEN={K}", `{"api_key":"{K}"}`, `{"level":"error","msg":"key {K} rejected"}`,
		`level=error msg="key {K} rejected" module=x`, "  token: {K}", "GET https://api.example.com/v1/x?key={K} 401",
		"token_{K} used", "x-{K} used", "key {K}_prod used", "key {K}-prod used", "key {K}deadline used",
		"key {K}. Retrying", `rejected "{K}"`, "({K})",
	}
	out := make([]string, 0, n)
	for range n {
		s := strings.ReplaceAll(pick(frames), "{K}", key())
		switch r.IntN(4) {
		case 0:
			s = pick(values) + " " + s
		case 1:
			s += " " + key()
		}
		out = append(out, s)
	}
	return out
}

// vendorKeyUnionCorpus is every input the gate runs: #116's gate corpus, which holds every earlier gate's,
// then GHSA-8qq6-vx8j-rx37's rows, its over-long id rows as one line, and the generator, each raw, through
// Templatize, through TemplatizeLine and in each escaped form, deduplicated.
func vendorKeyUnionCorpus() []string {
	out := vendorSecretCorpus()
	seen := make(map[string]bool, len(out))
	for _, s := range out {
		seen[s] = true
	}
	var rows []string
	for _, c := range vendorKeyInnerTagRows {
		rows = append(rows, strings.ReplaceAll(strings.ReplaceAll(c.in, "{K}", c.key), "{E}", strings.ReplaceAll(c.key, "/", `\/`)))
	}
	for _, c := range vendorKeyUnionSameRows {
		rows = append(rows, c.in)
	}
	overlong := strings.NewReplacer("{ID}", "AK"+"IA"+"QXZTWNBVLCHPRYMAK", "{S}", innerAWSClean)
	for _, c := range overlongIDRows {
		rows = append(rows, overlong.Replace(strings.Join(c.fields, " ")))
	}
	rows = append(rows, vendorKeyInnerTagGenerated(2000)...)
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

func escapedForms(s string) []string {
	var out []string
	for _, f := range jsonEscapeForms {
		out = append(out, f.esc(s))
	}
	return out
}

// TestVendorKeyUnionMasksASupersetOfV01229 is the gate GHSA-8qq6-vx8j-rx37's fix stands on. For every input,
// through Mask with New()'s chain and one with an extra suffix, New()'s late rules on (live) against the same
// Mapper with them off (ref), which is v0.12.29's Mask, and with the bytes a late rule reads taken from the
// chain's own rule-12 and rule-13 entries (keyRulesOf), not from lateDetectors:
//   - an input where ref masks every byte a late rule reads comes back byte for byte, with the same error;
//   - one where ref sent such a byte (a key it split) is sent, masks every byte ref masked and every byte a
//     late rule reads, and every value ref minted lies in a value of its own tag or in a TOKEN that holds a
//     late rule's span; a refusal of it is counted, since nothing it sent is sent either;
//   - one ref refused is refused, or sent with every byte a late rule reads masked;
//   - no placeholder minted for a value is text of the input.
//
// It and its two Mappers run in parallel with the other superset gates.
//
// Listed in ci.yml's SUPERSET_GATES; a new superset gate must follow this name pattern and be added there.
func TestVendorKeyUnionMasksASupersetOfV01229(t *testing.T) {
	t.Parallel()
	corpus := vendorKeyUnionCorpus()
	for _, c := range []struct {
		name string
		dets []detector
	}{
		{"default", baseDetectors},
		{"extra suffix", buildDetectors([]string{"lab"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel() // the corpus is built once, above, and only read; each input has its own Mappers
			rules := keyRulesOf(c.dets)
			repaired, merged, refusedNow, sentNow := 0, 0, 0, 0
			for i, in := range corpus {
				ref, live := mapperOn(c.dets), mapperOn(c.dets)
				live.late = New().late
				refOut, refErr := ref.Mask(in)
				out, err := live.Mask(in)
				read := keyBytes(rules, in)
				if refErr != nil {
					if err == nil {
						sentNow++
						masked, ok := maskedOf(live, in, out)
						if !ok || !coversRead(masked, read) {
							t.Errorf("input %d, refused on v0.12.29, is sent with a byte a late rule reads: %s", i, redactedRuns(in))
						}
					}
					continue
				}
				refMasked, ok := maskedOf(ref, in, refOut)
				if !ok {
					t.Fatalf("input %d: v0.12.29's output does not rebuild it", i)
				}
				if coversRead(refMasked, read) {
					if out != refOut || errText(err) != errText(refErr) {
						t.Errorf("input %d, with nothing to repair, moved: %s", i, redactedRuns(in))
					}
					continue
				}
				if err != nil {
					refusedNow++
					continue
				}
				repaired++
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
					t.Errorf("input %d sends a byte v0.12.29 masked or a late rule reads: %s", i, redactedRuns(in))
					continue
				}
				refSpans, _ := ref.spansOf(in, refOut)
				liveSpans, _ := live.spansOf(in, out)
				for _, rsp := range refSpans {
					for _, ls := range liveSpans {
						if ls.a > rsp.a || ls.b < rsp.b {
							continue
						}
						if ls.tag != rsp.tag {
							if ls.tag != tagToken || !anyRead(read, ls.a, ls.b) {
								t.Errorf("input %d: a %s value is now %s: %s", i, rsp.tag, ls.tag, redactedRuns(in))
							}
							merged++
						}
					}
				}
			}
			t.Logf("%d inputs: %d split on v0.12.29 and masked whole now (%d values merged into a key's TOKEN), "+
				"%d split and refused now, %d refused on v0.12.29 and sent now", len(corpus), repaired, merged, refusedNow, sentNow)
		})
	}
}

// keyRulesOf are the rules whose reading the gate holds the output to, taken from the chain itself and not
// from lateDetectors: dets' rule-12 entries and 13a as compiled, and 13b after the access key id it follows.
func keyRulesOf(dets []detector) []detector {
	tail := dets[len(dets)-len(vendorKeys)-2:]
	rules := append([]detector{}, tail[:len(tail)-1]...)
	return append(rules, detector{tagToken, mustLongest(awsPairAfter(awsIDContext)), 1, true, false})
}

// keyBytes marks the bytes of in that rules read, each alone, on in and on its view projected onto in.
func keyBytes(rules []detector, in string) []bool {
	read := make([]bool, len(in))
	view, offs := escapeView(in)
	mark := func(s string, at func(int) int) {
		for _, d := range rules {
			for _, g := range groupSpans(d, s) {
				for j := at(g[0]); j < at(g[1]); j++ {
					read[j] = true
				}
			}
		}
	}
	mark(in, func(i int) int { return i })
	if offs != nil {
		mark(view, func(i int) int { return offs[i] })
	}
	return read
}

// coversRead reports whether every byte want marks is marked in got.
func coversRead(got, want []bool) bool {
	for i := range want {
		if want[i] && !got[i] {
			return false
		}
	}
	return true
}

func anyRead(read []bool, a, b int) bool {
	for i := a; i < b; i++ {
		if read[i] {
			return true
		}
	}
	return false
}

// redactedRuns is an input as a failure message prints it: every run of twelve or more key characters
// cut to its first four and an ellipsis, so no generated key reaches the log whole.
func redactedRuns(s string) string {
	return keyRun.ReplaceAllStringFunc(s, func(r string) string { return r[:4] + "…" })
}

var keyRun = regexp.MustCompile(`[A-Za-z0-9_/+.-]{12,}`)
