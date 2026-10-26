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

// slackKeyV01228 is v0.12.28's Slack family, frozen: #116 added a second witness after the IDs' <NUM>
// and edited nothing else in it. It is part of the reference the gate below measures against; do not
// update it.
var slackKeyV01228 = `(?:xox[abprs]|xapp)(?:-[0-9][A-Za-z0-9-]*` + vendorAlnum + `{16}[A-Za-z0-9-]*|<NUM>` +
	caseOrRun(`(?:[A-Za-z0-9-]|`+pipelinePH+`)`, vendorAlnum) + `)`

// issue116Rules are the patterns #116 added to the chain, each as the chain compiles it: rule 12's seven
// families and rule 13's two.
func issue116Rules() map[string]bool {
	added := map[string]bool{}
	for _, k := range vendorKeys {
		switch k.loose {
		case pypiKey, gitlabKey, sendgridKey, npmKey, huggingFaceKey, shopifyKey, twilioKey:
			added[mustLongest(vendorRule(k)).String()] = true
		}
	}
	added[mustLongest(awsSecretNamed).String()] = true
	added[mustLongest(awsSecretPair).String()] = true
	return added
}

// atV01228 is dets as v0.12.28 built them: #116 appended its rules and widened the Slack entry, and
// edited nothing else, so the chain without the one and with v0.12.28's Slack pattern in the other is
// v0.12.28's. It fails unless it dropped exactly #116's entries and replaced exactly one.
func atV01228(t *testing.T, dets []detector) []detector {
	t.Helper()
	added := issue116Rules()
	slackNow := mustLongest(looseKey(slackKey, `A-Za-z0-9`)).String()
	var out []detector
	replaced := 0
	for _, d := range dets {
		switch src := d.re.String(); {
		case added[src]:
			continue
		case src == slackNow:
			d.re = mustLongest(looseKey(slackKeyV01228, `A-Za-z0-9`))
			replaced++
		}
		out = append(out, d)
	}
	if len(added) != 9 || len(out) != len(dets)-len(added) || replaced != 1 {
		t.Fatalf("the reference dropped %d of %d entries and replaced %d, want #116's 9 and the Slack entry",
			len(dets)-len(out), len(added), replaced)
	}
	return out
}

// issue116Trigger finds what any of #116's rules could read in the input: a vendor prefix, a Slack one, an
// AWS secret's name, or a TOKEN placeholder as text. 13b also reads a TOKEN the chain minted, so an input
// v0.12.28 masked a TOKEN in is read too (see issue116Reads). An input with none of them must come back
// from the chain byte for byte as v0.12.28 returned it.
var issue116Trigger = regexp.MustCompile(`SK|AC|SG\.|npm_|gl(?:pat|oas|dt|rtr|rt|cbt|ptt|ft|imt|agent|wt|soat|ffct)|hf_|shp(?:at|ca|pa|ss)_|pypi-|xox[abprs]|xapp|(?i:secret[_-]?access[_-]?key)|<` + tagToken + `_`)

// issue116Reads reports whether #116's rules could read in, given v0.12.28's output for it.
func issue116Reads(in, refOut string) bool {
	return issue116Trigger.MatchString(in) || strings.Contains(refOut, "<"+tagToken+"_")
}

// vendorSecretKeys are #116's keys as TestIssue116PrefixedVendorTokens names them, and vendorSecretFrames
// the places it puts them, {K} standing for the key: its closed contexts, the reader rows and the glue.
var (
	vendorSecretKeys = []string{
		twilioAPIKeySID, twilioAccountSID, sendgridSecret, npmSecret, gitlabPATSecret, gitlabRoutable,
		gitlabRunnerSecret, huggingFaceSecret, huggingFaceDigits, shopifyAccessSecret, shopifySharedSecret,
		pypiSecret,
	}
	vendorSecretFrames = []string{
		"key {K} rejected", "TOKEN={K}", `level=error msg="key {K} rejected" module=x`,
		`{"level":"error","msg":"key {K} rejected"}`, `{"api_key":"{K}"}`,
		"GET https://api.example.com/v1/send?key={K} 401", "  token: {K}", "token_{K} used", "x-{K} used",
		"token={K} used", "token:{K} used", "key {K}. Retrying", "key {K}, retrying", `rejected "{K}"`,
		"rejected '{K}'", "({K})", "[{K}]", "{{K}}", "{K}: rejected", "rejected {K}", "key {K}_prod used",
		"key {K}-prod used", "key {K}deadline used", "s3://{K}.prod@bucket", "s3://{K}.prod:secret@bucket",
		"https://{K}.prod.acme.com/x", "hdr Bearer {K} expired", "mail {K}@example.com",
		"peer worker-{K}.corp.internal down", "/home/{K}/x", "postgres://{K}:password@localhost/db",
		"postgres://appuser:{K}@db", "x{K}", "1{K}", "{K},{K}", "{K} {K}",
	}
)

// awsSecretFrames put rule 13's values where TestIssue116AWSSecretAccessKey does, {S} and {S2} standing for
// a secret access key and {ID} and {SID} for an access key id, with the near misses beside them.
var awsSecretFrames = []string{
	"AWS_SECRET_ACCESS_KEY={S}", `AWS_SECRET_ACCESS_KEY="{S}"`, "TF_VAR_aws_secret_access_key={S}",
	"aws_secret_access_key = {S}", "  aws_secret_access_key: {S}", "secretAccessKey: {S}",
	"run --aws-secret-access-key={S} --region x", "run --aws-secret-access-key {S} --region x",
	":aws_secret_access_key => '{S}'", `{"aws_secret_access_key":"{S}"}`, `{"AccessKeyId": "{ID}", "SecretAccessKey": "{S}"}`,
	`{"msg":"{\"aws_secret_access_key\":\"{S}\"}"}`, `level=error msg="AWS_SECRET_ACCESS_KEY={S} rejected"`,
	"creds {ID} {S} loaded", "{ID},{S}", "deploy,{ID},{S}", "{ID}; {S}", "{ID} | {S} | ok", "{ID}:{S}", `"{ID}","{S}"`,
	"{SID} {S}", "{ID},{S},{S2}", "{ID} {S} {SID} {S2}", "aws_secret_access_key is missing", "aws_secret_access_key: bob@corp.com",
	"creds {ID} {S}x", "creds {ID} {S}==", "creds {ID} {S}Q", "key {S} used", "secret_key={S}", "AWS_SECRET_KEY={S}",
	"Bearer {S2} {S}", "mail {S}@example.com", "{ID}@{S}", "aws_secret_access_key={ID}",
}

// awsSecretGenerated builds n lines from its own fixed PCG seed, so vendorSecretGenerated's lines stay as
// the class-1 gate ran them: an AWS secret's name in each spelling and separator, or an access key id and
// a field of 38 to 42 characters of the alphabet, the second sometimes repeated, in the same frames and
// between the same real values.
func awsSecretGenerated(n int) []string {
	r := rand.New(rand.NewPCG(116, 1))
	const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789/+"
	rs := func(a string, k int) string {
		var b strings.Builder
		for range k {
			b.WriteByte(a[r.IntN(len(a))])
		}
		return b.String()
	}
	pick := func(xs []string) string { return xs[r.IntN(len(xs))] }
	names := []string{
		"AWS_SECRET_ACCESS_KEY", "aws_secret_access_key", "aws-secret-access-key", "SecretAccessKey", "secretAccessKey",
		"secret_access_key", "TF_VAR_aws_secret_access_key", "--aws-secret-access-key", "AWS_SECRET_KEY", "secret_key",
	}
	nameSeps := []string{"=", " = ", ": ", ":", " ", `":"`, `": "`, " => '", "='", `=\"`}
	pairSeps := []string{" ", ",", ", ", ";", " | ", ":", `","`, "\t", "  "}
	ids := []string{"AK" + "IA" + rs("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", 16), "AS" + "IA" + rs("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", 16)}
	value := func() string {
		switch r.IntN(4) {
		case 0:
			return rs(b64, 38+r.IntN(5))
		default:
			return rs(b64, 40)
		}
	}
	item := func() string {
		if r.IntN(2) == 0 {
			return pick(names) + pick(nameSeps) + value()
		}
		s := pick(ids)
		for k := 1 + r.IntN(2); k > 0; k-- {
			s += pick(pairSeps) + value()
		}
		return s
	}
	values := []string{
		"postgres://svcuser:pwsecret@pghost/db", "bob@corp.com", "db.corp.internal", "10.0.0.5",
		stripeLiveSecret, githubClassicSecret, npmSecret, "Bearer abc.def.ghi",
	}
	frames := []string{
		"%s", "loaded %s from env", `{"level":"error","msg":"%s"}`, `level=error msg="%s" module=x`, "  %s",
		"%s rejected", "run %s --region x",
	}
	out := make([]string, 0, n)
	for range n {
		var b strings.Builder
		for k := 1 + r.IntN(2); k > 0; k-- {
			if r.IntN(3) == 0 {
				b.WriteString(pick(values))
			} else {
				b.WriteString(item())
			}
			b.WriteString(pick([]string{" ", " ", ",", "@", "/", ""}))
		}
		out = append(out, strings.Replace(pick(frames), "%s", strings.TrimSpace(b.String()), 1))
	}
	return out
}

// vendorSecretGenerated builds n lines from a fixed PCG seed: each #116 format with a random body around
// its floor (and Twilio's ceiling), names that start like one, and v0.12.28's keys, alone or glued to
// word characters, between real values -- a DSN, an email, a private host, an address, an AWS key id, a
// bearer value -- in prose, an env var, a JSON value and message, YAML, logfmt and a URL query.
func vendorSecretGenerated(n int) []string {
	r := rand.New(rand.NewPCG(116, 0))
	const (
		an   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
		b64  = an + "_-"
		hexl = "0123456789abcdef"
		hexu = "0123456789ABCDEF"
	)
	rs := func(a string, k int) string {
		var b strings.Builder
		for range k {
			b.WriteByte(a[r.IntN(len(a))])
		}
		return b.String()
	}
	pick := func(xs []string) string { return xs[r.IntN(len(xs))] }
	gl := []string{"pat", "oas", "dt", "rtr", "rt", "cbt", "ptt", "ft", "imt", "agent", "wt", "soat", "ffct"}
	words := []string{"config", "cache", "rotation", "v2", "test", "hub", "download", "runner", "token", "1", "42"}
	key := func() string {
		switch r.IntN(14) {
		case 0:
			return "S" + "K" + rs(pick([]string{hexl, hexu}), 30+r.IntN(5))
		case 1:
			return "A" + "C" + rs(pick([]string{hexl, hexu}), 30+r.IntN(5))
		case 2:
			return "S" + "G." + rs(b64, 20+r.IntN(4)) + "." + rs(b64, 41+r.IntN(4))
		case 3:
			return "np" + "m_" + rs(an, 33+r.IntN(5))
		case 4:
			return "g" + "l" + pick(gl) + "-" + rs(b64, 18+r.IntN(5))
		case 5:
			return "g" + "l" + pick(gl) + "-" + rs(b64, 27) + ".01." + rs("0123456789abcdefghijklmnopqrstuvwxyz", 9)
		case 6:
			return "h" + "f_" + rs(pick([]string{an, an[:52]}), 32+r.IntN(4))
		case 7:
			return "sh" + "p" + pick([]string{"at", "ca", "pa", "ss"}) + "_" + rs(pick([]string{an, hexl}), 30+r.IntN(4))
		case 8:
			return "py" + "pi-" + "AgE" + rs(b64, 79+r.IntN(80))
		case 9:
			d := "0123456789"
			return "xo" + "xp-" + rs(d, 10+r.IntN(4)) + "-" + rs(d, 10+r.IntN(4)) + "-" + rs(d, 10+r.IntN(4)) + "-" + rs(hexl, 32)
		case 10:
			p := pick([]string{"np" + "m_", "h" + "f_", "S" + "G.", "gl" + "pat-", "py" + "pi-", "sh" + "pat_", "S" + "K", "A" + "C", "xox" + "b-"})
			var w []string
			for k := 1 + r.IntN(4); k > 0; k-- {
				w = append(w, pick(words))
			}
			return p + strings.Join(w, pick([]string{"_", "-", ""}))
		case 11:
			return pick([]string{stripeLiveSecret, githubClassicSecret, githubPATSecret, slackBotSecret, slackUserSecret, googleAPISecret})
		default:
			return pick(vendorSecretKeys)
		}
	}
	values := []string{
		"postgres://svcuser:pwsecret@pghost/db", "bob@corp.com", "db.corp.internal", "10.0.0.5",
		"AKIA" + "IOSFODNN7EXAMPLE", "/home/deploy/x", "https://api.acme.com/v1", "2001:db8::1",
	}
	glue := []string{" ", " ", " ", "", "_", "-", "=", ":", ",", ".", "/", "@"}
	frames := []string{
		"%s", "key %s rejected", "TOKEN=%s", `{"api_key":"%s"}`, `{"level":"error","msg":"%s"}`,
		"token: %s", `level=error msg="%s" module=x`, "GET https://api.example.com/v1/x?key=%s 401",
		"hdr Bearer %s expired", "%s,%s",
	}
	out := make([]string, 0, n)
	for range n {
		var b strings.Builder
		for k := 1 + r.IntN(3); k > 0; k-- {
			if r.IntN(3) == 0 {
				b.WriteString(pick(values))
			} else {
				b.WriteString(key())
			}
			b.WriteString(pick(glue))
		}
		s := strings.TrimSpace(b.String())
		f := pick(frames)
		if strings.Count(f, "%s") == 2 {
			s = strings.Replace(strings.Replace(f, "%s", s, 1), "%s", key(), 1)
		} else {
			s = strings.Replace(f, "%s", s, 1)
		}
		out = append(out, s)
	}
	return out
}

// vendorSecretCorpus is every input the gate runs: the reservation gate's corpus, which holds every
// earlier gate's, then #116's keys in every frame, its over-masking rows, the AWS frames and both
// generators, each raw,
// through Templatize, through TemplatizeLine and in each escaped form, deduplicated.
func vendorSecretCorpus() []string {
	var out []string
	seen := make(map[string]bool)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range placeholderTextCorpus() {
		add(s)
	}
	var rows []string
	for _, k := range vendorSecretKeys {
		for _, f := range vendorSecretFrames {
			rows = append(rows, strings.ReplaceAll(f, "{K}", k))
		}
	}
	for _, c := range vendorSecretOverMaskingRows {
		rows = append(rows, c.in)
	}
	rows = append(rows, vendorSecretGenerated(2000)...)
	aws := strings.NewReplacer("{S}", awsSecretKey, "{S2}", awsSecretKey2, "{ID}", awsAccessKeyID, "{SID}", awsSessionKeyID)
	for _, f := range awsSecretFrames {
		rows = append(rows, aws.Replace(f))
	}
	rows = append(rows, awsSecretGenerated(1000)...)
	for _, s := range rows {
		tpl, _ := pipeline.Templatize(s)
		line, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: s}))
		for _, v := range []string{s, tpl, line} {
			add(v)
			for _, f := range jsonEscapeForms {
				add(f.esc(v))
			}
		}
	}
	return out
}

// TestVendorSecretChainMasksASupersetOfV01228 is the gate #116's fix stands on. For every input, through
// Mask -- the escape view and the reservation included -- with New()'s chain and one with an extra
// suffix, against the same chain at v0.12.28 (atV01228):
//   - nothing v0.12.28 sent is refused unless one of #116's rules reads v0.12.28's output -- it sent a
//     value #116 masks -- and nothing it refused is sent. There the re-scan can find a #116 key the pass
//     could not reach -- one glued after another, whose lead a token has turned into our '>', or a pair
//     field whose TOKEN the escape view's union made of a span neither pass read as one -- and the
//     request is refused, where v0.12.28 sent the key;
//   - every byte v0.12.28 masked is masked;
//   - an input with nothing #116's rules could read comes back byte for byte;
//   - where no new value was minted the output is v0.12.28's after renumbering (#142's), but that on an
//     escaped line a value may now be a TOKEN: a #116 key inside it joins the escape view's union as a
//     TOKEN span, and a union that is not exactly one span of the view is a TOKEN (see valueTag). Where a
//     new value was minted, every value v0.12.28 minted that is minted still has its tag;
//   - no placeholder minted for a value is text of the input.
//
// Listed in ci.yml's SUPERSET_GATES; a new superset gate must follow this name pattern and be added there.
func TestVendorSecretChainMasksASupersetOfV01228(t *testing.T) {
	corpus := vendorSecretCorpus()
	for _, c := range []struct {
		name string
		dets []detector
	}{
		{"default", baseDetectors},
		{"extra suffix", buildDetectors([]string{"lab"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			ref := atV01228(t, c.dets)
			var rules []detector
			added := issue116Rules()
			for _, d := range c.dets {
				if added[d.re.String()] {
					rules = append(rules, d)
				}
			}
			refused, refusedNew, more, renum, retagged, newValue := 0, 0, 0, 0, 0, 0
			for _, in := range corpus {
				refM, live := mapperOn(ref), mapperOn(c.dets)
				refOut, refErr := refM.Mask(in)
				out, err := live.Mask(in)
				if !issue116Reads(in, refOut) {
					if out != refOut || errText(err) != errText(refErr) {
						t.Errorf("an input with nothing #116 reads moved:\n in:  %q\n ref: %q (%v)\n got: %q (%v)", in, refOut, refErr, out, err)
					}
					continue
				}
				minted := false
				for v := range live.byValue {
					if _, ok := refM.byValue[v]; !ok {
						minted = true
					}
				}
				switch {
				case refErr != nil && err != nil:
					refused++
					continue
				case refErr == nil && err != nil && reads116(rules, refOut):
					refusedNew++ // v0.12.28 sent a #116 value here; the re-scan finds one the pass could not reach
					continue
				case refErr == nil && err != nil:
					t.Errorf("refused now, sent on v0.12.28, and #116 reads nothing it sent:\n in:  %q\n ref: %q\n err: %v", in, refOut, err)
					continue
				case refErr != nil:
					t.Errorf("sent now, refused on v0.12.28:\n in:  %q\n ref: %v\n got: %q", in, refErr, out)
					continue
				}
				for tok, v := range live.byToken {
					if strings.Contains(in, tok) {
						t.Errorf("minted %s for %q, and the input carries %s as text:\n in: %q", tok, v, tok, in)
					}
				}
				refMasked, ok := maskedOf(refM, in, refOut)
				masked, ok2 := maskedOf(live, in, out)
				if !ok || !ok2 {
					t.Errorf("an output does not rebuild the input:\n in:  %q\n ref: %q\n got: %q", in, refOut, out)
					continue
				}
				for i := range in {
					if refMasked[i] && !masked[i] {
						t.Errorf("byte %d (%q) is masked on v0.12.28 and sent now:\n in:  %q\n ref: %q\n got: %q", i, in[i], in, refOut, out)
						break
					}
				}
				if !minted {
					got, ok := renumbered(live, refM, out)
					switch {
					case ok && got == refOut:
						if out != refOut {
							renum++
						}
					case retaggedAsToken(live, refM, in, out, refOut):
						retagged++
					default:
						t.Errorf("no new value, and more than the numbering moved:\n in:         %q\n ref:        %q\n got:        %q\n renumbered: %q", in, refOut, out, got)
					}
					continue
				}
				newValue++
				for v, rt := range refM.byValue {
					if lt, ok := live.byValue[v]; ok && lt[:strings.LastIndexByte(lt, '_')] != rt[:strings.LastIndexByte(rt, '_')] {
						t.Errorf("%q was %s on v0.12.28 and is %s now:\n in: %q", v, rt, lt, in)
					}
				}
				for i := range in {
					if masked[i] && !refMasked[i] {
						more++
						break
					}
				}
			}
			t.Logf("%d inputs: %d with a new value (%d mask more bytes), %d renumbered only, %d escaped and retagged TOKEN, "+
				"%d refused on both, %d sent on v0.12.28 with a #116 value and refused now",
				len(corpus), newValue, more, renum, retagged, refused, refusedNew)
		})
	}
}

// reads116 reports whether one of #116's rules reads s, as written or in its escape view: run on v0.12.28's
// output, whether v0.12.28 sent a value #116 masks.
func reads116(rules []detector, s string) bool {
	view, _ := escapeView(s)
	for _, d := range rules {
		for _, x := range []string{s, view} {
			if loc := d.re.FindStringSubmatchIndex(x); loc != nil && loc[2*d.group] >= 0 {
				return true
			}
		}
	}
	return false
}

// retaggedAsToken reports whether out is refOut with each placeholder standing for the same value, every
// tag that moved having moved to TOKEN, on a line the escape view resolves.
func retaggedAsToken(live, ref *Mapper, in, out, refOut string) bool {
	if _, offs := escapeView(in); offs == nil {
		return false
	}
	ok := true
	got := ourPHRe.ReplaceAllStringFunc(out, func(tok string) string {
		v, minted := live.byToken[tok]
		if !minted {
			return tok
		}
		r, seen := ref.byValue[v]
		if !seen || (r[:strings.LastIndexByte(r, '_')] != tok[:strings.LastIndexByte(tok, '_')] && !strings.HasPrefix(tok, "<"+tagToken+"_")) {
			ok = false
		}
		return r
	})
	return ok && got == refOut
}
