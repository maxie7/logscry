// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"regexp"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// #116's invented keys, one or more per format family, shared by its tests and the tables. Each is
// written split at its vendor prefix, as #114's are, so that no source line carries a whole key-shaped
// literal for a secret scanner to match; the strings are the same at run time.
const (
	twilioAPIKeySID     = "S" + "K" + "7c1e9a4f2b8d6e0a3c5f7b9dcafe4c6f"
	twilioAccountSID    = "A" + "C" + "e4b7d1a9c3f6e2b8d5a1c7f9e3b6d2a4"
	sendgridSecret      = "S" + "G." + "qK8hA2Z1_0NBe-F3FcmH9R" + "." + "aGg9bCZMunpS0xCVOFQg9U7nycJLlzpFfHgoaNokGfq"
	npmSecret           = "np" + "m_" + "5N42eTQ75GZV7l5jt3j6BE5xEJ7acqTa7O6f"
	gitlabPATSecret     = "gl" + "pat-" + "79FtmP_JLoZj0LtF_duB"
	gitlabRoutable      = "gl" + "pat-" + "BRGQRGU9hFMq188PpsSBKR-KPVy" + ".01." + "bqudbdh46"
	gitlabRunnerSecret  = "gl" + "rt-" + "lgAxUNxpAgW952bvlyvl"
	huggingFaceSecret   = "h" + "f_" + "QmXkTzRvLpWnYbHdSfJgKcNaEuVoMiBtPr"
	huggingFaceDigits   = "h" + "f_" + "0UxXbA9xoZiPAKrHnCGFrCQviidB9RvtBC"
	shopifyAccessSecret = "sh" + "pat_" + "a3f9c2e7b1d84f60e5a9c2b7d1f8e3a6"
	shopifySharedSecret = "sh" + "pss_" + "5d2e8a1c7f3b9e6d4a0c8f2e7b5d1a39"
	pypiSecret          = "py" + "pi-" + "AgE" + "IcHlwaS5vcmc" + "BZHoGrHrKxt17xU9MTnYLFpvnjK8dMAz6w75wO9j0jlLayl6WerALghyQe0T" +
		"nsvaM1d3OMiYjxF2TNXAGaGWNYnl7qTXFLLFCtbZox2uceNiDCaAhTRPQqYOjO8MwdOW384Xo26hx1oo"
)

// TestIssue116PrefixedVendorTokens is the named regression for #116's first class. Twilio (SK and AC
// SIDs), SendGrid (SG.), npm (npm_), GitLab (glpat- and the rest of its documented prefixes), Hugging
// Face (hf_), Shopify (shpat_, shpca_, shppa_, shpss_) and PyPI (pypi-AgE) tokens matched no detector, so
// each was sent whole, raw and on the template minus its digits, and residue() read the line as clean. A
// rule that owns the value around one (a userinfo, a host, an email local part, a home directory, a
// bearer value) already masked it whole; prose, a query value, a JSON value and an environment variable
// were the leak. v0.4.0 onward.
//
// Each family is one more rule-12 entry, run LAST as #114's are, so every rule before it sees
// v0.12.28's input and the reader rows below are v0.12.28's bytes. Glue before a key is prose (#57);
// after it the format's alphabet decides. On the template a pipeline placeholder is the evidence, over
// a floor in units.
func TestIssue116PrefixedVendorTokens(t *testing.T) {
	type vendorRow struct {
		name, key, env string
		sent           []string // what v0.12.28 sent, raw and on the template; none may survive
		tmplCollapsed  bool     // the key alone is <HEX> on the template, so that template is v0.12.28's
	}
	keys := []vendorRow{
		{"twilio SK", twilioAPIKeySID, "TWILIO_API_KEY", []string{"S" + "K", "dcafe"}, false},
		{"twilio AC", twilioAccountSID, "TWILIO_ACCOUNT_SID", []string{"A" + "Ce4b", "d1a9"}, true},
		{"sendgrid", sendgridSecret, "SENDGRID_API_KEY", []string{"S" + "G.qK", "OFQg", "Hgoa"}, false},
		{"npm", npmSecret, "NPM_TOKEN", []string{"np" + "m_", "eTQ", "acqTa"}, false},
		{"gitlab glpat-", gitlabPATSecret, "GITLAB_TOKEN", []string{"gl" + "pat", "FtmP_JLoZj"}, false},
		{"gitlab routable", gitlabRoutable, "GITLAB_TOKEN", []string{"gl" + "pat", "BRGQRGU", "bqudbdh"}, false},
		{"gitlab glrt-", gitlabRunnerSecret, "RUNNER_TOKEN", []string{"gl" + "rt", "lgAxUNxpAgW"}, false},
		{"hugging face", huggingFaceSecret, "HF_TOKEN", []string{"h" + "f_", "QmXkTz", "MiBtPr"}, false},
		{"hugging face, digits", huggingFaceDigits, "HF_TOKEN", []string{"h" + "f_", "PAKrHnCGFrCQviid"}, false},
		{"shopify shpat_", shopifyAccessSecret, "SHOPIFY_ACCESS_TOKEN", []string{"sh" + "pat_", "a3f9"}, false},
		{"shopify shpss_", shopifySharedSecret, "SHOPIFY_API_SECRET", []string{"sh" + "pss_", "5d2e"}, false},
		{"pypi", pypiSecret, "TWINE_PASSWORD", []string{"py" + "pi-", "IcHlwaS", "TNXAGaGWNY"}, false},
	}
	fill := func(s string, k vendorRow) string {
		return strings.NewReplacer("{K}", k.key, "{ENV}", k.env).Replace(s)
	}

	// Premises: each fragment is the key's, and one at least is on the template (letters survive the
	// pipeline, a hex key's in ones and twos), and
	// a key with a digit that the template does not collapse is mangled by it, so the template rows read
	// a placeholder. A Hugging Face token of letters alone is unchanged there and read as on the raw line.
	for _, k := range keys {
		tKey, _ := pipeline.Templatize("key " + k.key + " rejected")
		onTemplate := k.tmplCollapsed
		for _, frag := range k.sent {
			if !strings.Contains(k.key, frag) {
				t.Fatalf("premise broken: %q is not a fragment of the %s key", frag, k.name)
			}
			onTemplate = onTemplate || strings.Contains(tKey, frag)
		}
		if !onTemplate {
			t.Fatalf("premise broken: no fragment of the %s key is on its template %q", k.name, tKey)
		}
		if !k.tmplCollapsed && strings.ContainsAny(k.key, "0123456789") && !regexp.MustCompile(pipelinePH).MatchString(tKey) {
			t.Fatalf("premise broken: the %s key %q is not mangled by the pipeline masker", k.name, tKey)
		}
	}

	// The text around a key that no rule claims, the same for every family as #114's. The template is
	// TemplatizeLine's, the real Template field.
	closed := []struct{ name, in, want, tmpl string }{
		{"prose", "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
		{"env var", "{ENV}={K}", "{ENV}=<TOKEN_1>", "{ENV}=<TOKEN_1>"},
		{"logfmt msg", `level=error msg="key {K} rejected" module=x`,
			`level=error msg="key <TOKEN_1> rejected" module=x`, `level=error msg="key <TOKEN_1> rejected" module=x`},
		{"json msg", `{"level":"error","msg":"key {K} rejected"}`,
			`{"level":"error","msg":"key <TOKEN_1> rejected"}`, `{"level":<STR>,"msg":"key <TOKEN_1> rejected"}`},
		{"json value", `{"api_key":"{K}"}`, `{"api_key":"<TOKEN_1>"}`, `{"api_key":<STR>}`},
		{"query value", "GET https://api.example.com/v1/send?key={K} 401",
			"GET https://<HOST_1>/v1/send?key=<TOKEN_1> 401", "GET https://<HOST_1>/v<NUM>/send?key=<TOKEN_1> <NUM>"},
		{"yaml value", "  token: {K}", "  token: <TOKEN_1>", "token: <TOKEN_1>"},
		// Glue before the key is prose and stays outside the placeholder (#57, #108).
		{"'_' before", "token_{K} used", "token_<TOKEN_1> used", "token_<TOKEN_1> used"},
		{"'-' before", "x-{K} used", "x-<TOKEN_1> used", "x-<TOKEN_1> used"},
		{"'=' before", "token={K} used", "token=<TOKEN_1> used", "token=<TOKEN_1> used"},
		{"':' before", "token:{K} used", "token:<TOKEN_1> used", "token:<TOKEN_1> used"},
		// A closer after the key stays outside the placeholder (#90's class), one row each.
		{"sentence dot", "key {K}. Retrying", "key <TOKEN_1>. Retrying", "key <TOKEN_1>. Retrying"},
		{"comma", "key {K}, retrying", "key <TOKEN_1>, retrying", "key <TOKEN_1>, retrying"},
		{"double quote", `rejected "{K}"`, `rejected "<TOKEN_1>"`, `rejected <STR>`},
		{"single quote", "rejected '{K}'", "rejected '<TOKEN_1>'", "rejected <STR>"},
		{"paren", "({K})", "(<TOKEN_1>)", "(<TOKEN_1>)"},
		{"bracket", "[{K}]", "[<TOKEN_1>]", "[<TOKEN_1>]"},
		{"brace", "{{K}}", "{<TOKEN_1>}", "{<TOKEN_1>}"},
		{"colon", "{K}: rejected", "<TOKEN_1>: rejected", "<TOKEN_1>: rejected"},
		{"end of text", "rejected {K}", "rejected <TOKEN_1>", "rejected <TOKEN_1>"},
	}
	check := func(t *testing.T, in, want string, sent []string) {
		t.Helper()
		m := New()
		got := mustMask(t, m, in)
		if got != want {
			t.Errorf("a vendor secret not masked:\n in:   %q\n got:  %q\n want: %q", in, got, want)
		}
		for _, frag := range sent {
			if strings.Contains(got, frag) {
				t.Errorf("a fragment v0.12.28 sent survived: %q still in %q", frag, got)
			}
		}
		if r := m.Restore(got); r != in {
			t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, r)
		}
		// The new output is inert: masking it again changes nothing and the re-scan reads it clean.
		if again := mustMask(t, New(), want); again != want {
			t.Errorf("the masked output is not inert:\n out:   %q\n again: %q", want, again)
		}
		if tag := New().residue(want); tag != "" {
			t.Errorf("residue() reads the masked output %q as %q", want, tag)
		}
	}
	for _, k := range keys {
		for _, c := range closed {
			t.Run(k.name+"/"+c.name, func(t *testing.T) {
				in := fill(c.in, k)
				check(t, in, fill(c.want, k), k.sent)
				if k.tmplCollapsed {
					return // the template is v0.12.28's, pinned below
				}
				tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: in}))
				check(t, tIn, fill(c.tmpl, k), k.sent)
			})
		}
	}

	// A standalone SID is one hex run, which the pipeline collapses to <HEX>: no byte of it is left on
	// the template, so that template is v0.12.28's. Glued to a word character there is no \b, NUM takes
	// the digits, and the letters left are the rule's to read.
	for _, c := range []struct{ name, in, tmpl, want string }{
		{"standalone", "key " + twilioAccountSID + " rejected", "key <HEX> rejected", "key <HEX> rejected"},
		{"'_' before", "sid_" + twilioAccountSID + " used", "", "sid_<TOKEN_1> used"},
		{"'_' after", "key " + twilioAccountSID + "_prod used", "", "key <TOKEN_1>_prod used"},
	} {
		t.Run("twilio AC template/"+c.name, func(t *testing.T) {
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: c.in}))
			if c.tmpl != "" && tIn != c.tmpl {
				t.Fatalf("premise broken: the template is not the pinned one:\n got:  %q\n want: %q", tIn, c.tmpl)
			}
			check(t, tIn, c.want, []string{"A" + "Ce4b", "d1a9"})
		})
	}

	// After a key, the format's alphabet decides, raw and templatized alike: '_' and '-' are a SendGrid,
	// GitLab or PyPI body's, so the word is absorbed; for the others it is prose.
	after := []struct{ name, in, want string }{
		{"twilio, '_' after", "key " + twilioAPIKeySID + "_prod used", "key <TOKEN_1>_prod used"},
		{"twilio, '-' after", "key " + twilioAPIKeySID + "-prod used", "key <TOKEN_1>-prod used"},
		{"sendgrid, '_' after is the key's", "key " + sendgridSecret + "_prod used", "key <TOKEN_1> used"},
		{"sendgrid, '-' after is the key's", "key " + sendgridSecret + "-prod used", "key <TOKEN_1> used"},
		{"npm, '_' after", "key " + npmSecret + "_prod used", "key <TOKEN_1>_prod used"},
		{"npm, '-' after", "key " + npmSecret + "-prod used", "key <TOKEN_1>-prod used"},
		{"gitlab, '_' after is the key's", "key " + gitlabPATSecret + "_prod used", "key <TOKEN_1> used"},
		{"gitlab, '-' after is the key's", "key " + gitlabPATSecret + "-prod used", "key <TOKEN_1> used"},
		{"gitlab routable, '_' after is the key's", "key " + gitlabRoutable + "_prod used", "key <TOKEN_1> used"},
		{"hugging face, '_' after", "key " + huggingFaceSecret + "_prod used", "key <TOKEN_1>_prod used"},
		{"hugging face, '-' after", "key " + huggingFaceDigits + "-prod used", "key <TOKEN_1>-prod used"},
		{"shopify, '_' after", "key " + shopifyAccessSecret + "_prod used", "key <TOKEN_1>_prod used"},
		{"shopify, '-' after", "key " + shopifySharedSecret + "-prod used", "key <TOKEN_1>-prod used"},
		{"pypi, '_' after is the key's", "key " + pypiSecret + "_prod used", "key <TOKEN_1> used"},
		{"pypi, '-' after is the key's", "key " + pypiSecret + "-prod used", "key <TOKEN_1> used"},
	}
	for _, c := range after {
		t.Run("after/"+c.name, func(t *testing.T) {
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: c.in}))
			var sent []string
			for _, k := range keys {
				if strings.Contains(c.in, k.key) {
					sent = k.sent
				}
			}
			for _, in := range []string{c.in, tIn} {
				check(t, in, c.want, sent)
			}
		})
	}

	// Byte-identical with v0.12.28, raw and templatized, for every family: the rules that own a value
	// around a key already masked it whole, and run before rule 12, so it never sees one.
	same := []struct{ name, in, want, tmpl string }{
		{"userinfo, '.' after the key (4d's position)", "s3://{K}.prod@bucket", "s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>"},
		{"userinfo with a password (4d's position)", "s3://{K}.prod:secret@bucket", "s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>"},
		{"host label (9e's position)", "https://{K}.prod.acme.com/x", "https://<HOST_1>/x", "https://<HOST_1>/x"},
		{"bearer value (3a)", "hdr Bearer {K} expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"email local part (5)", "mail {K}@example.com", "mail <EMAIL_1>", "mail <EMAIL_1>"},
		{"bare private host, '-' before the key (9b)", "peer worker-{K}.corp.internal down", "peer <HOST_1> down", "peer <HOST_1> down"},
		{"home directory (10)", "/home/{K}/x", "/home/<USER_1>/x", "/home/<USER_1>/x"},
		{"the #46 cell, the key is the username (4)", "postgres://{K}:password@localhost/db",
			"postgres://<TOKEN_1>@<HOST_1>/db", "postgres://<TOKEN_1>@<HOST_1>/db"},
		{"the #46 mirror, the key is the password (4)", "postgres://appuser:{K}@db", "postgres://<TOKEN_1>@<HOST_1>", "postgres://<TOKEN_1>@<HOST_1>"},
	}
	for _, k := range keys {
		for _, c := range same {
			t.Run("same/"+k.name+"/"+c.name, func(t *testing.T) {
				in := fill(c.in, k)
				tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: in}))
				for _, p := range []struct{ in, want string }{{in, c.want}, {tIn, c.tmpl}} {
					if got := mustMask(t, New(), p.in); got != p.want {
						t.Errorf("a shape outside #116 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
					}
				}
			})
		}
	}

	// #114's residual, closed: a user token with a word character glued after it. The hex segment has no
	// \b to end on, so NUM takes its digits and the letters a-f between them were sent. After the IDs'
	// <NUM>s a run of hex letters and placeholders is the pipeline's handwriting of that segment, the
	// placeholder being the evidence. Unglued, the segment is <HEX> and the template stays v0.12.28's.
	t.Run("xoxp glued after, template", func(t *testing.T) {
		in := "key " + slackUserSecret + "_prod used"
		tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: in}))
		check(t, tIn, "key <TOKEN_1>_prod used", []string{"xox" + "p", "f<NUM>a<NUM>c"})
	})
	t.Run("xoxp unglued, template is v0.12.28's", func(t *testing.T) {
		tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: "key " + slackUserSecret + " rejected"}))
		if want := "key xox" + "p<NUM><NUM><NUM>-<HEX> rejected"; tIn != want {
			t.Fatalf("premise broken: the template is not the pinned one:\n got:  %q\n want: %q", tIn, want)
		}
		if got := mustMask(t, New(), tIn); got != tIn {
			t.Errorf("the unglued user-token template moved:\n in:  %q\n got: %q", tIn, got)
		}
	})

	// THE RESIDUAL, pinned. Twilio publishes a SID as two letters and exactly 32 hex digits, and a longer
	// hex run after "AC" is an uppercase hash constant, so a run longer than 32 is not a SID: a word
	// glued after a SID that starts with a hex letter makes the run longer, and the raw line is sent as
	// on v0.12.28. On the template NUM has broken the run and the rule reads it whole.
	t.Run("residual/twilio, a hex letter glued after", func(t *testing.T) {
		in := "key " + twilioAPIKeySID + "deadline used"
		if got := mustMask(t, New(), in); got != in {
			t.Errorf("the pinned residual moved:\n in:  %q\n got: %q", in, got)
		}
		tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: in}))
		if got, want := mustMask(t, New(), tIn), "key <TOKEN_1>line used"; got != want {
			t.Errorf("the template of the residual:\n in:   %q\n got:  %q\n want: %q", tIn, got, want)
		}
	})

	// THE COSTS on the template, pinned; the raw ones are TestAcceptedOverMasking's. A placeholder is the
	// evidence over a floor in units well below the raw one, so a short run with a digit in it after a
	// prefix is a key there. The raw line is unchanged in each.
	costs := []struct{ name, in, tmpl string }{
		{"twilio, three units with a digit", "code S" + "K1ab used", "code <TOKEN_1> used"},
		{"twilio, a hex run longer than a SID", "key S" + "K" + "7c1e9a4f2b8d6e0a3c5f7b9dcafe4c6f0 used", "key <TOKEN_1> used"},
		{"twilio, a glued uppercase hash after AC", "x_A" + "C" + strings.Repeat("0F1E2D3C", 8) + " used", "x_<TOKEN_1> used"},
		{"sendgrid, one segment of twenty-two", "key S" + "G.qK8hA2Z1_0NBe-F3FcmH9R used", "key <TOKEN_1> used"},
		{"npm, twelve units with a digit", "key np" + "m_abcdefghijk1 used", "key <TOKEN_1> used"},
		{"gitlab, a kebab-case name with a digit", "flag gl" + "ft-feed-v2 on", "flag <TOKEN_1> on"},
		{"hugging face, twelve units with a digit", "key h" + "f_abcdefghijk1 used", "key <TOKEN_1> used"},
		{"shopify, three units with a digit", "key sh" + "pat_ab1 used", "key <TOKEN_1> used"},
		{"pypi, twenty-four units with a digit", "key py" + "pi-AgE" + "abcdefghijklmnopqrstuvw1 used", "key <TOKEN_1> used"},
		{"slack, hex letters after four placeholders", "id xox" + "b-1-2-3-a_x", "id <TOKEN_1>_x"},
	}
	for _, c := range costs {
		t.Run("cost/"+c.name, func(t *testing.T) {
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: c.in}))
			for _, p := range []struct{ in, want string }{{c.in, c.in}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("a pinned cost moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}

	// What stays, raw and templatized, and what keeps it. The floor: a word after a prefix is short of
	// the format's length, and a short tail with a digit is under the template floor. The alphabet: '_'
	// ends an npm, Hugging Face or Shopify body, so a snake_case name after the prefix is short pieces.
	// The ceiling: a hex run longer than a SID after SK or AC. The lead: a letter or digit before the
	// prefix makes a longer word that merely contains it.
	for _, s := range []string{
		"np" + "m_config_cache",
		"np" + "m_lifecycle_event=install",
		"h" + "f_hub_download",
		"use h" + "f_xet_storage_v2",
		"S" + "G.web-tier-01",
		"S" + "G.Count=3",
		"M" + "SG." + "qK8hA2Z1_0NBe-F3FcmH9R" + "." + "aGg9bCZMunpS0xCVOFQg9U7nycJLlzpFfHgoaNokGfq",
		"gl" + "pat-expiry",
		"gl" + "pat-v2",
		"gl" + "rt-runner-token",
		"X" + gitlabPATSecret,
		"py" + "pi-simple-index",
		"py" + "pi-AgE",
		"my" + pypiSecret,
		"sh" + "pat_",
		"sh" + "pss_rotation",
		"sh" + "pat_v2",
		"w" + shopifyAccessSecret,
		"S" + "KU-1234 shipped",
		"code S" + "K1a",
		"ACK received",
		"A" + "CCEPTED",
		"hash A" + "C" + strings.Repeat("0F1E2D3C", 8),
		"x" + twilioAPIKeySID,
		"a" + npmSecret,
		"b" + huggingFaceSecret,
	} {
		tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: s}))
		for _, in := range []string{s, tIn} {
			if got := mustMask(t, New(), in); got != in {
				t.Errorf("over-masked a name that is no key:\n in:  %q\n out: %q", in, got)
			}
		}
	}

	// The key in prose and in an environment variable is one value, under one placeholder, and an answer
	// naming it restores the key alone.
	for _, k := range keys {
		t.Run("recurs/"+k.name, func(t *testing.T) {
			m := New()
			if got, want := mustMask(t, m, "key "+k.key+" rejected"), "key <TOKEN_1> rejected"; got != want {
				t.Errorf("got %q, want %q", got, want)
			}
			if got, want := mustMask(t, m, k.env+"="+k.key), k.env+"=<TOKEN_1>"; got != want {
				t.Errorf("recurrence: got %q, want %q", got, want)
			}
			if got := m.Restore("rotate <TOKEN_1>"); got != "rotate "+k.key {
				t.Errorf("Restore = %q, want the key alone", got)
			}
		})
	}

	// residue() reads the same rules, so a raw vendor secret mutes the escalation; v0.12.28 read every one
	// of these as clean.
	for _, k := range keys {
		for _, in := range []string{
			"key " + k.key + " rejected",
			k.env + "=" + k.key,
			"token_" + k.key + " used",
			"https://api.example.com/v1/send?key=" + k.key,
		} {
			if tag := New().residue(in); tag != tagToken {
				t.Errorf("a raw vendor secret does not mute: residue(%q) = %q, want %q", in, tag, tagToken)
			}
		}
	}

	// The escape view (GHSA-p47p-xrhj-3x7p): a key in a JSON message whose URL is escaped is masked in
	// every escaped form, and nothing of it is sent.
	for _, k := range keys {
		for _, f := range jsonEscapeForms {
			in := f.esc(`{"msg":"GET https://api.example.com/v1/send?key=` + k.key + ` 401"}`)
			t.Run("escaped/"+k.name+"/"+f.name, func(t *testing.T) {
				m := New()
				got := mustMask(t, m, in)
				if strings.Count(got, "<TOKEN_1>") != 1 {
					t.Errorf("the key is not one token in an escaped line:\n in:  %q\n got: %q", in, got)
				}
				for _, frag := range k.sent {
					if strings.Contains(got, frag) {
						t.Errorf("a fragment survived: %q still in %q", frag, got)
					}
				}
				if r := m.Restore(got); r != in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, r)
				}
			})
		}
	}
}

// TestIssue116EveryGitLabPrefixIsAKey: every prefix GitLab's token overview documents, raw and on the
// template, with a twenty-character body and a routable one.
func TestIssue116EveryGitLabPrefixIsAKey(t *testing.T) {
	for _, p := range []string{"pat", "oas", "dt", "rtr", "rt", "cbt", "ptt", "ft", "imt", "agent", "wt", "soat", "ffct"} {
		for _, body := range []string{"79FtmP_JLoZj0LtF_duB", "BRGQRGU9hFMq188PpsSBKR-KPVy" + ".01." + "bqudbdh46"} {
			in := "key g" + "l" + p + "-" + body + " rejected"
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: in}))
			for _, s := range []string{in, tIn} {
				if got, want := mustMask(t, New(), s), "key <TOKEN_1> rejected"; got != want {
					t.Errorf("a GitLab token not masked:\n in:   %q\n got:  %q\n want: %q", s, got, want)
				}
			}
		}
	}
	for _, p := range []string{"at", "ca", "pa", "ss"} {
		in := "key sh" + "p" + p + "_" + "a3f9c2e7b1d84f60e5a9c2b7d1f8e3a6" + " rejected"
		tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: in}))
		for _, s := range []string{in, tIn} {
			if got, want := mustMask(t, New(), s), "key <TOKEN_1> rejected"; got != want {
				t.Errorf("a Shopify token not masked:\n in:   %q\n got:  %q\n want: %q", s, got, want)
			}
		}
	}
}
