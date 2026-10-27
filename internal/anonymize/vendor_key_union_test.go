// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"strings"
	"testing"
)

// The keys of GHSA-8qq6-vx8j-rx37's rows, each an invented value of its family whose body holds text an
// earlier rule tags. Every one is built by concatenation, so no whole key is a literal of this file.
var (
	// innerGoogleSkDash holds "-sk-" and a run of sixteen, which 3b tags, '-' being no word character.
	innerGoogleSkDash = "AI" + "za" + "SyDqkxLm-" + "s" + "k-" + "QxZtWnBvLcHpRyMaKdFgJcu"
	// innerGoogleSkUnd holds "_sk-" and a run of sixteen, which rule 11 tags: 3b's \b fails after '_'.
	innerGoogleSkUnd = "AI" + "za" + "SyDqkxLm_" + "s" + "k-" + "QxZtWnBvLcHpRyMaKdFgJcu"
	// innerGoogleSkNum holds "4sk7Ab", which the Template field writes "<NUM>sk<NUM>Ab", rule 11's.
	innerGoogleSkNum = "AI" + "za" + "SyDqkxLmQ4" + "s" + "k7" + "AbZtWnBvLcHpRyMaKdFgJcu"
	// innerGoogleSlackNum holds "5xoxb7Ab", "<NUM>xoxb<NUM>Ab" on the Template field, the Slack family's.
	innerGoogleSlackNum = "AI" + "za" + "SyDqkxLmQ5" + "xo" + "xb7" + "AbZtWnBvLcHpRyMaKdFgJ"
	// innerGoogleGithubPAT holds a fine-grained GitHub token after '_', which rule 12 tags before Google.
	innerGoogleGithubPAT = "AI" + "za" + "SyDqkxLm_" + "git" + "hub_pat_" + strings.Repeat("Ab3CdEfGhJ", 8) + "Km" +
		"QxZtWnBvLcHpRyMaKdFgJcu"
	// innerGoogleUUID holds a UUID between two '-', which 6 tags on the raw line.
	innerGoogleUUID = "AI" + "za" + "SyDqkxLm-" + "550e8400-e29b-41d4-a716-446655440000" + "-QxZtWnBvLc"
	// innerGoogleAWS holds an access key id after a lowercase letter, which rule 11 tags.
	innerGoogleAWS = "AI" + "za" + "SyDqkxLmx" + "AK" + "IA" + "QXZTWNBVLCHPRYMA" + "xKdFgJcuWq"
	// innerStripeSkNum holds "5sk7Ab" early, so on the Template field rule 11 tags from there to the end
	// of the key and through any '_' or '-' glued after it, and the head is under Stripe's floor.
	innerStripeSkNum = "s" + "k_live_" + "Rk5" + "s" + "k7" + "AbQzWvLpXnYtBcHdMqJr"
	// innerGhpSkNum, innerNpmSkNum, innerHfSkNum and innerShopifySkNum are the same shape in other base62 bodies.
	innerGhpSkNum     = "g" + "hp_" + "Xy5" + "s" + "k7" + "AbWvUtSrQpOnMlKjHgFeDcBaZyQwRtYuIo"
	innerNpmSkNum     = "np" + "m_" + "Xy5" + "s" + "k7" + "AbWvUtSrQpOnMlKjHgFeDcBaZyQwRtYuIo"
	innerHfSkNum      = "h" + "f_" + "Xy5" + "s" + "k7" + "AbWvUtSrQpOnMlKjHgFeDcBaZyQwRtYu"
	innerShopifySkNum = "sh" + "pat_" + "Xy5" + "s" + "k7" + "AbWvUtSrQpOnMlKjHgFeDcBaZyQw"
	// innerGhpatStripe holds a Stripe test key after '_', which rule 12 tags before the fine-grained family.
	innerGhpatStripe = "git" + "hub_pat_" + strings.Repeat("Ab3CdEfGhJ", 4) + "_" + "s" + "k_test_" +
		"Rk4mQzXvLpWwXnYtYbZcHdQs" + "_" + strings.Repeat("Mn4PqRsTuV", 3)
	// innerSlackSkDash holds "-sk-" and a run of sixteen in its secret segment, 3b's.
	innerSlackSkDash = "xo" + "xb-" + "2849163057-7340815926413-" + "GkLmNpQr-" + "s" + "k-" + "QxZtWnBvLcHpRyMaKd"
	// innerPypiSlackNum holds "5xoxb7Ab", the Slack family's on the Template field.
	innerPypiSlackNum = "py" + "pi-" + "AgE" + "IcHlwaSvcmcBZHoGrHrKxtQ5" + "xo" + "xb7" + "AbU" +
		strings.Repeat("TnYLFpvnjKdMAzwOjlLay", 3)
	// innerGitlabSuffix holds ".lan-", a name on a private suffix to 9b.
	innerGitlabSuffix = "gl" + "pat-" + "AbCdEfGhJk" + ".lan-" + "QxZtWnBvLc"
	// innerGitlabJWT holds a JWT between two '-', which 1 tags.
	innerGitlabJWT = "gl" + "pat-" + "AbCdEf-" + "ey" + "J" + "hbGciOiJIUz" + ".ey" + "J" + "zdWIiOiIx" + "." + "SflKxwRJSM" + "-QxZt"
	// innerSendgridSuffix's second segment starts "svc-", so the key reads as a name on a private suffix to 9b.
	innerSendgridSuffix = "S" + "G." + "qKhAZNBeFFcmHRqKhAZNBe" + "." + "svc-" + "aGgbCZMunpSxCVOFQgUnycJLlzpFfHgoaNoqXyz"
	// innerSendgridGitlab holds a GitLab token after '-', which rule 12 tags before SendGrid.
	innerSendgridGitlab = "S" + "G." + "qKhAZNBeFFcmHRqKhAZNBe" + "." + "aGgbCZMu-" + "gl" + "pat-" + "QxZtWnBvLcHpRyMaKdFg" +
		"-npSxCVOFQg"
	// innerAWSHome holds "/home/bob/", whose name 10 tags. innerAWSTwilio holds "/SKa5bc", "/SKa<NUM>bc" on
	// the Template field, Twilio's.
	innerAWSHome   = "vMlpuXVqGsysu" + "/home/bob/" + "XbUnszngilzmjKwtT"
	innerAWSTwilio = "vMlpuXVqGsysuXbUnszngi" + "/S" + "Ka5bc" + "lzmjKwtTCYq"
	innerAWSKeyID  = "AK" + "IA" + "QXZTWNBVLCHPRYMA"
	// innerAWSClean is a secret with nothing inside it any rule tags.
	innerAWSClean = "vMlpuXVqGsysuXbUnszngi" + "lzmjKwtTCYqrWvXbQd"
)

// vendorKeyInnerTagRows are GHSA-8qq6-vx8j-rx37's shapes: a vendor key, or an AWS secret access key, whose
// body holds text an earlier rule tags. Rule 12 and rule 13 run last and stop at our '<', so the key was
// masked on at most one side of that tag and the rest was sent, and residue() read the remainder as clean.
// v0.12.15 onward for #114's families, v0.12.29 for #116's and the AWS secret.
//
// Each row masks the key whole, on the raw line (want) and on the Template field (tmpl, through
// templateOf), and sends no part of it. tmpl is empty where the row is not run on the template. Where the
// Template field's key is two TOKENs side by side, v0.12.29 already masked every byte of it, and the union
// leaves an output that sends nothing as v0.12.29 wrote it.
var vendorKeyInnerTagRows = []struct {
	name, key, in, want, tmpl string
}{
	{"google, 3b's sk key", innerGoogleSkDash, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"google, rule 11's sk key", innerGoogleSkUnd, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"google, sk<NUM> on the template", innerGoogleSkNum, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"google, sk<NUM> in an env var", innerGoogleSkNum, "GOOGLE_API_KEY={K}", "GOOGLE_API_KEY=<TOKEN_1>", "GOOGLE_API_KEY=<TOKEN_1>"},
	{"google, sk<NUM> as a query value", innerGoogleSkNum, "GET https://maps.example.com/api/geocode/json?key={K} 403",
		"GET https://<HOST_1>/api/geocode/json?key=<TOKEN_1> 403", "GET https://<HOST_1>/api/geocode/json?key=<TOKEN_1> <NUM>"},
	{"google, sk<NUM> in a json message", innerGoogleSkNum, `{"level":"error","msg":"key {K} rejected"}`,
		`{"level":"error","msg":"key <TOKEN_1> rejected"}`, `{"level":<STR>,"msg":"key <TOKEN_1> rejected"}`},
	{"google, the slack family on the template", innerGoogleSlackNum, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"google, a fine-grained github token inside", innerGoogleGithubPAT, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"google, a uuid inside", innerGoogleUUID, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"google, an access key id inside", innerGoogleAWS, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"stripe, sk<NUM> on the template", innerStripeSkNum, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"github classic, sk<NUM> on the template", innerGhpSkNum, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"github fine-grained, a stripe key inside", innerGhpatStripe, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"slack, 3b's sk key", innerSlackSkDash, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_2><TOKEN_1> rejected"},
	{"pypi, the slack family on the template", innerPypiSlackNum, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_2><TOKEN_1> rejected"},
	{"gitlab, a private suffix inside", innerGitlabSuffix, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"gitlab, a jwt inside", innerGitlabJWT, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"sendgrid, a private suffix at its dot", innerSendgridSuffix, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"sendgrid, a gitlab token inside", innerSendgridGitlab, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"npm, sk<NUM> on the template", innerNpmSkNum, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"hugging face, sk<NUM> on the template", innerHfSkNum, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
	{"shopify, sk<NUM> on the template", innerShopifySkNum, "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_2><TOKEN_1> rejected"},
	{"aws secret by name, a home directory inside", innerAWSHome, "AWS_SECRET_ACCESS_KEY={K}", "AWS_SECRET_ACCESS_KEY=<TOKEN_1>",
		"AWS_SECRET_ACCESS_KEY=<TOKEN_1>"},
	{"aws secret by name, twilio on the template", innerAWSTwilio, "AWS_SECRET_ACCESS_KEY={K}", "AWS_SECRET_ACCESS_KEY=<TOKEN_1>",
		"AWS_SECRET_ACCESS_KEY=<TOKEN_1>"},
	{"aws secret beside its id, a home directory inside", innerAWSHome, "creds " + innerAWSKeyID + " {K} loaded",
		"creds <TOKEN_1> <TOKEN_2> loaded", "creds <TOKEN_1> <TOKEN_2> loaded"},
	{"aws secret beside its id, twilio on the template", innerAWSTwilio, "creds " + innerAWSKeyID + " {K} loaded",
		"creds <TOKEN_1> <TOKEN_2> loaded", "creds <TOKEN_1> <TOKEN_2> loaded"},
	// The second key is split and the first is not. The split key's value holds the chain's first TOKEN (3b's,
	// inside it), so it is minted first, where the chain minted that TOKEN.
	{"two keys, the second split", innerGoogleSkDash, "key " + googleAPISecret + " and {K} rejected",
		"key <TOKEN_2> and <TOKEN_1> rejected", ""},
	// The inner tag runs past the key: rule 11's sk run takes the '_' glued after the Stripe key, which is
	// not Stripe's alphabet. The union masks it with the key; a late span alone would send "_prod".
	{"stripe, the inner tag runs past the key", innerStripeSkNum, "key {K}_prod used", "key <TOKEN_1>_prod used", "key <TOKEN_1> used"},
	// A '/' JSON-escaped: only the escape view reads the AWS secret whole, and 10 reads the name inside it there.
	{"aws secret by name, escaped, a home directory inside", innerAWSHome, `{"msg":"AWS_SECRET_ACCESS_KEY={E}"}`,
		`{"msg":"AWS_SECRET_ACCESS_KEY=<TOKEN_1>"}`, ""},
}

// vendorKeyUnionSameRows are lines whose keys the chain masks whole, or masks inside a larger value: each
// comes back exactly as v0.12.29 returns it. A key the chain masks inside a host stays that host's.
var vendorKeyUnionSameRows = []struct{ name, in, want string }{
	{"a google key alone", "key " + googleAPISecret + " rejected", "key <TOKEN_1> rejected"},
	{"a google key as a host label", "https://" + googleAPISecret + ".prod.acme.com/x", "https://<HOST_1>/x"},
	{"a google key as an email local part", "mail " + googleAPISecret + "@example.com", "mail <EMAIL_1>"},
	{"a google key with a uuid inside, as a host label", "https://" + innerGoogleUUID + ".prod.acme.com/x", "https://<HOST_1>/x"},
}

// TestVendorKeyWithAnInnerTagIsMaskedWhole is GHSA-8qq6-vx8j-rx37: every row's key is one TOKEN, no part
// of it is sent, Restore gives the line back, and the output re-masks unchanged with a clean residue().
func TestVendorKeyWithAnInnerTagIsMaskedWhole(t *testing.T) {
	for _, c := range vendorKeyInnerTagRows {
		in := strings.ReplaceAll(c.in, "{K}", c.key)
		if strings.Contains(c.in, "{E}") {
			in = strings.ReplaceAll(c.in, "{E}", strings.ReplaceAll(c.key, "/", `\/`))
		}
		forms := []struct{ name, in, want string }{{"raw", in, c.want}}
		if c.tmpl != "" {
			forms = append(forms, struct{ name, in, want string }{"template", templateOf(in), c.tmpl})
		}
		for _, f := range forms {
			t.Run(c.name+"/"+f.name, func(t *testing.T) {
				m := New()
				got := mustMask(t, m, f.in)
				if got != f.want {
					t.Errorf("key not masked whole:\n got:  %q\n want: %q", redactKey(got, c.key), f.want)
				}
				sent := 0
				for _, p := range keyFragments(c.key) {
					if strings.Contains(got, p) {
						sent++
					}
				}
				if sent > 0 {
					t.Errorf("%d fragments of the key were sent", sent)
				}
				if back := m.Restore(got); back != f.in {
					t.Errorf("round trip lost the line")
				}
				if again := mustMask(t, New(), got); again != got {
					t.Errorf("the output re-masks:\n got:   %q\n again: %q", got, again)
				}
				if tag := New().residue(got); tag != "" {
					t.Errorf("residue() reads %s in the output", tag)
				}
			})
		}
	}
	for _, c := range vendorKeyUnionSameRows {
		t.Run("same/"+c.name, func(t *testing.T) {
			if got := mustMask(t, New(), c.in); got != c.want {
				t.Errorf("a line the chain masked moved:\n got:  %q\n want: %q", got, c.want)
			}
		})
	}
}

// overlongIDRows are an AWS secret access key after an id one character longer than a real one, which
// detector 2 refuses and rule 11 reads. An earlier rule that minted the same text first under its own tag --
// 10's USER, for a home directory of that name -- hands rule 11 that placeholder back, and 13b, which reads
// the secret only after our TOKEN, sent it whole on v0.12.29. Found on the way to GHSA-8qq6-vx8j-rx37 and
// closed by it: the late 13b reads the id itself. Each row's fields go through one Mapper, as a request's do.
var overlongIDRows = []struct {
	name         string
	fields, want []string
}{
	{"one line", []string{"cd /home/{ID}/x; creds {ID} {S} loaded"}, []string{"cd /home/<USER_1>/x; creds <USER_1> <TOKEN_1> loaded"}},
	{"two fields", []string{"cd /home/{ID}/x", "creds {ID} {S} loaded"}, []string{"cd /home/<USER_1>/x", "creds <USER_1> <TOKEN_1> loaded"}},
	{"two fields, a comma between", []string{"cd /home/{ID}/x", "{ID},{S}"}, []string{"cd /home/<USER_1>/x", "<USER_1>,<TOKEN_1>"}},
}

// TestAWSSecretAfterAnOverlongIDMintedElsewhereIsMasked pins overlongIDRows: the secret is masked, Restore
// gives each field back, and no part of the secret is sent.
func TestAWSSecretAfterAnOverlongIDMintedElsewhereIsMasked(t *testing.T) {
	fill := strings.NewReplacer("{ID}", "AK"+"IA"+"QXZTWNBVLCHPRYMAK", "{S}", innerAWSClean)
	for _, c := range overlongIDRows {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			for i, f := range c.fields {
				in := fill.Replace(f)
				got := mustMask(t, m, in)
				if got != c.want[i] {
					t.Errorf("field %d: got %q, want %q", i, redactKey(got, innerAWSClean), c.want[i])
				}
				if back := m.Restore(got); back != in {
					t.Errorf("field %d: round trip lost the line", i)
				}
			}
		})
	}
}

// keyFragments are the runs of a key's body a row must not send: every six characters of it that hold no
// digit, the Template field having turned digits into <NUM>, and no '/', which the escaped row writes as "\/".
func keyFragments(key string) []string {
	var out []string
	for i := 4; i+6 <= len(key); i++ {
		if p := key[i : i+6]; !strings.ContainsAny(p, "0123456789/") {
			out = append(out, p)
		}
	}
	return out
}

// redactKey keeps a failure message from printing the key: its body is replaced by an ellipsis.
func redactKey(s, key string) string {
	for _, p := range keyFragments(key) {
		s = strings.ReplaceAll(s, p, "…")
	}
	return s
}
