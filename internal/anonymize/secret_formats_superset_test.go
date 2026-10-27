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
// families, rule 13's two and 4f.
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
	added[mustLongest(webhookPath).String()] = true
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
	if len(added) != 10 || len(out) != len(dets)-len(added) || replaced != 1 {
		t.Fatalf("the reference dropped %d of %d entries and replaced %d, want #116's 10 and the Slack entry",
			len(dets)-len(out), len(added), replaced)
	}
	return out
}

// issue116Trigger finds what any of #116's rules could read in the input: a vendor prefix, a Slack one, an
// AWS secret's name, a webhook host, or a TOKEN placeholder as text. 13b also reads a TOKEN the chain minted, so an input
// v0.12.28 masked a TOKEN in is read too (see issue116Reads). An input with none of them must come back
// from the chain byte for byte as v0.12.28 returned it.
var issue116Trigger = regexp.MustCompile(`SK|AC|SG\.|npm_|gl(?:pat|oas|dt|rtr|rt|cbt|ptt|ft|imt|agent|wt|soat|ffct)|hf_|shp(?:at|ca|pa|ss)_|pypi-|xox[abprs]|xapp|(?i:secret[_-]?access[_-]?key|hooks\.slack\.com|discord(?:app)?\.com)|<` + tagToken + `_`)

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

// webhookFrames put 4f's values where TestIssue116WebhookURLs does, filled by webhookFill, with the near
// misses and the shapes that stay sent beside them.
var webhookFrames = []string{
	"posting to https://{SH}/services/{SP} failed", "POST https://{SH}/services/{SP}?wait=true 404", "url https://{SH}/services/{SP}#x",
	"url https://{SH}/services/{SP}/ ok", `url "https://{SH}/services/{SP}"`, `{"webhook":"https://{SH}/services/{SP}"}`,
	"level=error url=https://{SH}/services/{SP} status=404", `level=error msg="post https://{SH}/services/{SP} failed" module=x`,
	"url https://{SH}:443/services/{SP} failed", "SLACK_WEBHOOK={SH}/services/{SP}", "url https://{SHU}/services/{SP}",
	"posted to https://{SH}/services/{SP}. Retrying", "https://user:pw@{SH}/services/{SP}", "POST https://{DH}/api/webhooks/{DP} 401",
	"POST https://{DAH}/api/webhooks/{DP} 401", "POST https://{DH}/api/v10/webhooks/{DP} 401", "POST https://canary.{DH}/api/webhooks/{DP} 401",
	"POST https://{DH}:8443/api/webhooks/{DP} 401", "POST https://{DH}/api/webhooks/{DP}?thread_id=123&wait=true 401",
	`{"level":"error","msg":"webhook https://{DH}/api/webhooks/{DP} failed"}`, "url {DH}/api/webhooks/{DP}",
	"GET https://{DH}/api/webhooks/{DID} 401", "POST https://{DH}/api/webhooks/{DP}/slack 401", "cb https://{SH}/services/{SP}@corp.com x",
	"https://{SH}/ ok", "https://{SH}/services/ ok", "https://{DH}/api/webhooks/ ok", "https://{DH}/api/channels/{DID}/messages",
	"https://example.com/services/{SP}", "https://{SH}.evil.example/services/{SP}", "https://x{SH}/services/{SP}",
	"https://not{DH}/api/webhooks/{DP}", `{"url":"https://{SH}/services/{SP}","email":"bob@corp.com"}`,
	"https://{SH}/workflows/T2O3Z5XAN/A0123/456/abc", "https://hooks.slack-gov.com/services/{SP}",
	"redirect=https%3A%2F%2F{SH}%2Fservices%2FT2O3Z5XAN%2FBMMNZ56PB9%2Fx",
	"POST https://{DH}/api/webhooks/{DP} " + awsAccessKeyID + " " + awsSecretKey, "hook https://{SH}/services/{SP} key " + npmSecret,
}

// webhookGenerated builds n lines from its own fixed PCG seed: a webhook URL on each host, with or without
// a scheme, a port, a version or a subdomain, a path of one to four segments of the path's alphabet or
// none, and a query, a fragment, a slash, a dot, an '@' or a quote after it, in the same frames and
// between the same real values as the other generators.
func webhookGenerated(n int) []string {
	r := rand.New(rand.NewPCG(116, 2))
	const seg = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"
	rs := func(a string, k int) string {
		var b strings.Builder
		for range k {
			b.WriteByte(a[r.IntN(len(a))])
		}
		return b.String()
	}
	pick := func(xs []string) string { return xs[r.IntN(len(xs))] }
	url := func() string {
		var b strings.Builder
		b.WriteString(pick([]string{"https://", "https://", "http://", "", "x_https://"}))
		slack := r.IntN(2) == 0
		if slack {
			b.WriteString(pick([]string{"hooks.sl" + "ack.com", "HOOKS.SL" + "ACK.COM", "hooks.sl" + "ack.co", "xhooks.sl" + "ack.com"}))
		} else {
			b.WriteString(pick([]string{"", "", "canary.", "ptb.", "x"}) + pick([]string{"disc" + "ord.com", "disc" + "ordapp.com"}))
		}
		if r.IntN(4) == 0 {
			b.WriteString(":" + pick([]string{"443", "8443", "1"}))
		}
		if slack {
			b.WriteString(pick([]string{"/services/", "/services/", "/workflows/", "/services"}))
		} else {
			b.WriteString(pick([]string{"/api/webhooks/", "/api/webhooks/", "/api/v10/webhooks/", "/api/v9/webhooks/", "/api/channels/", "/api/webhooks"}))
		}
		for k := r.IntN(5); k > 0; k-- {
			b.WriteString(rs(seg, 1+r.IntN(70)))
			if k > 1 {
				b.WriteString("/")
			}
		}
		b.WriteString(pick([]string{"", "", "?wait=true", "#x", "/", ".", "@corp.com", `"`, "/slack"}))
		return b.String()
	}
	values := []string{
		"postgres://svcuser:pwsecret@pghost/db", "bob@corp.com", "db.corp.internal", "10.0.0.5",
		stripeLiveSecret, npmSecret, "https://api.acme.com/v1", "AK" + "IA" + "T996RXGVAO6Q4JYL",
	}
	frames := []string{
		"%s", "post %s failed", `{"level":"error","msg":"%s"}`, `{"url":"%s"}`, `level=error msg="%s" module=x`, "WEBHOOK=%s",
		"POST %s 404",
	}
	out := make([]string, 0, n)
	for range n {
		var b strings.Builder
		for k := 1 + r.IntN(2); k > 0; k-- {
			if r.IntN(3) == 0 {
				b.WriteString(pick(values))
			} else {
				b.WriteString(url())
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
// earlier gate's, then #116's keys in every frame, its over-masking rows, the AWS and webhook frames and
// the three generators, each raw,
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
	for _, f := range webhookFrames {
		rows = append(rows, webhookFill.Replace(f))
	}
	rows = append(rows, webhookGenerated(1000)...)
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
//   - where no new value was minted the output is v0.12.28's after renumbering (#142's), but that a HOST
//     inside a path 4f reads is its TOKEN (below), and that on an escaped line a value may now be a TOKEN: a #116 key inside it joins the escape view's union as a
//     TOKEN span, and a union that is not exactly one span of the view is a TOKEN (see valueTag). Where a
//     new value was minted, every value v0.12.28 minted that is minted still has its tag, but a HOST
//     inside a path 4f reads: 9b read a private name there, and it is the webhook path's TOKEN now;
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
			var webhook detector
			added := issue116Rules()
			for _, d := range c.dets {
				if added[d.re.String()] {
					rules = append(rules, d)
				}
				if d.re.String() == mustLongest(webhookPath).String() {
					webhook = d
				}
			}
			refused, refusedNew, more, renum, retagged, newValue, pathHost := 0, 0, 0, 0, 0, 0, 0
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
					case pathHostAsToken(webhook, live, refM, in, out, refOut):
						pathHost++
					default:
						t.Errorf("no new value, and more than the numbering moved:\n in:         %q\n ref:        %q\n got:        %q\n renumbered: %q", in, refOut, out, got)
					}
					continue
				}
				newValue++
				for v, rt := range refM.byValue {
					lt, ok := live.byValue[v]
					switch {
					case !ok || lt[:strings.LastIndexByte(lt, '_')] == rt[:strings.LastIndexByte(rt, '_')]:
					case strings.HasPrefix(lt, "<"+tagToken+"_") && strings.HasPrefix(rt, "<"+tagHost+"_") && inWebhookPath(webhook, in, v):
						pathHost++ // 9b read a name in a webhook's path as a host; 4f reads the path
					default:
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
				"%d refused on both, %d sent on v0.12.28 with a #116 value and refused now, %d hosts in a webhook path now its TOKEN",
				len(corpus), newValue, more, renum, retagged, refused, refusedNew, pathHost)
		})
	}
}

// inWebhookPath reports whether v lies inside a path 4f (d) reads in in, as written or in its escape view.
func inWebhookPath(d detector, in, v string) bool {
	view, _ := escapeView(in)
	for _, x := range []string{in, view} {
		for _, loc := range d.re.FindAllStringSubmatchIndex(x, -1) {
			if loc[2*d.group] >= 0 && strings.Contains(x[loc[2*d.group]:loc[2*d.group+1]], v) {
				return true
			}
		}
	}
	return false
}

// pathHostAsToken reports whether out is refOut with each placeholder standing for the same value, every
// tag that moved having moved from HOST to TOKEN on a value inside a path 4f reads.
func pathHostAsToken(webhook detector, live, ref *Mapper, in, out, refOut string) bool {
	ok := true
	got := ourPHRe.ReplaceAllStringFunc(out, func(tok string) string {
		v, minted := live.byToken[tok]
		if !minted {
			return tok
		}
		r, seen := ref.byValue[v]
		switch {
		case !seen:
			ok = false
		case r[:strings.LastIndexByte(r, '_')] == tok[:strings.LastIndexByte(tok, '_')]:
		case !strings.HasPrefix(tok, "<"+tagToken+"_") || !strings.HasPrefix(r, "<"+tagHost+"_") || !inWebhookPath(webhook, in, v):
			ok = false
		}
		return r
	})
	return ok && got == refOut
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
