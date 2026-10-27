// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// #116's invented webhook values: the two hosts and each vendor's path, a Slack team id, bot id and
// secret, a Discord snowflake id and token. Written split, as #114's keys are, so that no source line
// carries a whole webhook URL for a secret scanner to match; the strings are the same at run time.
const (
	slackHookHost      = "hooks.sl" + "ack.com"
	discordHost        = "disc" + "ord.com"
	discordAppHost     = "disc" + "ordapp.com"
	slackWebhookPath   = "T2O3" + "Z5XAN" + "/" + "BMMNZ" + "56PB9" + "/" + "rGz5dGscig4d" + "yRHs8dNDj1Rb"
	discordWebhookID   = "444333138" + "050489301"
	discordWebhookPath = discordWebhookID + "/" + "TBvCjppaNmf3X2hEmk7ENcrk0d" + "BhjfA7es0NBH_H7Z1pRaD9G" + "mI0jxJip0bfMOlVvA8h"
)

// webhookFill puts the invented values into a row: {SH} and {SHU} the Slack host and its upper case,
// {DH} and {DAH} the Discord hosts, {SP} and {DP} the paths, {DID} the Discord id alone.
var webhookFill = strings.NewReplacer(
	"{SHU}", strings.ToUpper(slackHookHost), "{SH}", slackHookHost, "{DAH}", discordAppHost, "{DH}", discordHost,
	"{SP}", slackWebhookPath, "{DP}", discordWebhookPath, "{DID}", discordWebhookID,
)

// TestIssue116WebhookURLs is the named regression for #116's third class. A Slack or Discord incoming
// webhook URL carries its credential in the path: whoever holds the URL can post to the channel. 9a
// masked the host and nothing read the path, so the team and bot ids and the secret, or the webhook's id
// and token, were sent whole, raw and on the Template field. v0.4.0 onward.
//
// 4f reads the host and the path prefix the vendor documents -- hooks.slack.com/services/,
// discord.com/api/webhooks/ (discordapp.com, a version, a subdomain) -- and masks every path segment
// after it as one TOKEN: the ids and the token are one credential. The host stays 9a's, as it was; a
// query or a fragment after the path stays.
func TestIssue116WebhookURLs(t *testing.T) {
	sent := []string{"dGscig", "dyRHs", "BMMNZ", "XAN/", "TBvCjppaNmf", "BhjfA", "mI0jxJip", "444333138"}
	check := func(t *testing.T, in, want string) {
		t.Helper()
		m := New()
		got := mustMask(t, m, in)
		if got != want {
			t.Errorf("a webhook URL's path not masked:\n in:   %q\n got:  %q\n want: %q", in, got, want)
		}
		for _, frag := range sent {
			if strings.Contains(got, frag) {
				t.Errorf("a fragment v0.12.28 sent survived: %q still in %q", frag, got)
			}
		}
		if r := m.Restore(got); r != in {
			t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, r)
		}
		if again := mustMask(t, New(), want); again != want {
			t.Errorf("the masked output is not inert:\n out:   %q\n again: %q", want, again)
		}
		if tag := New().residue(want); tag != "" {
			t.Errorf("residue() reads the masked output %q as %q", want, tag)
		}
	}

	// Each place a log writes one, raw and through TemplatizeLine, the real Template field.
	for _, c := range []struct{ name, in, want, tmpl string }{
		{"slack, prose", "posting to https://{SH}/services/{SP} failed", "posting to https://<HOST_1>/services/<TOKEN_1> failed",
			"posting to https://<HOST_1>/services/<TOKEN_1> failed"},
		{"slack, query", "POST https://{SH}/services/{SP}?wait=true 404", "POST https://<HOST_1>/services/<TOKEN_1>?wait=true 404",
			"POST https://<HOST_1>/services/<TOKEN_1>?wait=true <NUM>"},
		{"slack, fragment", "url https://{SH}/services/{SP}#x", "url https://<HOST_1>/services/<TOKEN_1>#x",
			"url https://<HOST_1>/services/<TOKEN_1>#x"},
		{"slack, trailing slash", "url https://{SH}/services/{SP}/ ok", "url https://<HOST_1>/services/<TOKEN_1>/ ok",
			"url https://<HOST_1>/services/<TOKEN_1>/ ok"},
		{"slack, quoted", `url "https://{SH}/services/{SP}"`, `url "https://<HOST_1>/services/<TOKEN_1>"`, `url <STR>`},
		{"slack, json value", `{"webhook":"https://{SH}/services/{SP}"}`, `{"webhook":"https://<HOST_1>/services/<TOKEN_1>"}`,
			`{"webhook":<STR>}`},
		{"slack, logfmt", "level=error url=https://{SH}/services/{SP} status=404",
			"level=error url=https://<HOST_1>/services/<TOKEN_1> status=404", "level=error url=https://<HOST_1>/services/<TOKEN_1> status=<NUM>"},
		{"slack, logfmt msg", `level=error msg="post https://{SH}/services/{SP} failed" module=x`,
			`level=error msg="post https://<HOST_1>/services/<TOKEN_1> failed" module=x`,
			`level=error msg="post https://<HOST_1>/services/<TOKEN_1> failed" module=x`},
		{"slack, port", "url https://{SH}:443/services/{SP} failed", "url https://<HOST_1>:443/services/<TOKEN_1> failed",
			"url https://<HOST_1>:<NUM>/services/<TOKEN_1> failed"},
		{"slack, no scheme", "SLACK_WEBHOOK={SH}/services/{SP}", "SLACK_WEBHOOK={SH}/services/<TOKEN_1>", "SLACK_WEBHOOK={SH}/services/<TOKEN_1>"},
		{"slack, upper-case host", "url https://{SHU}/services/{SP}", "url https://<HOST_1>/services/<TOKEN_1>",
			"url https://<HOST_1>/services/<TOKEN_1>"},
		{"slack, sentence dot", "posted to https://{SH}/services/{SP}. Retrying", "posted to https://<HOST_1>/services/<TOKEN_1>. Retrying",
			"posted to https://<HOST_1>/services/<TOKEN_1>. Retrying"},
		{"slack, behind a userinfo", "https://user:pw@{SH}/services/{SP}", "https://<TOKEN_1>@<HOST_1>/services/<TOKEN_2>",
			"https://<TOKEN_1>@<HOST_1>/services/<TOKEN_2>"},
		{"discord", "POST https://{DH}/api/webhooks/{DP} 401", "POST https://<HOST_1>/api/webhooks/<TOKEN_1> 401",
			"POST https://<HOST_1>/api/webhooks/<TOKEN_1> <NUM>"},
		{"discordapp", "POST https://{DAH}/api/webhooks/{DP} 401", "POST https://<HOST_1>/api/webhooks/<TOKEN_1> 401",
			"POST https://<HOST_1>/api/webhooks/<TOKEN_1> <NUM>"},
		{"discord, versioned", "POST https://{DH}/api/v10/webhooks/{DP} 401", "POST https://<HOST_1>/api/v10/webhooks/<TOKEN_1> 401",
			"POST https://<HOST_1>/api/v<NUM>/webhooks/<TOKEN_1> <NUM>"},
		{"discord, canary", "POST https://canary.{DH}/api/webhooks/{DP} 401", "POST https://<HOST_1>/api/webhooks/<TOKEN_1> 401",
			"POST https://<HOST_1>/api/webhooks/<TOKEN_1> <NUM>"},
		{"discord, ptb", "POST https://ptb.{DH}/api/webhooks/{DP} 401", "POST https://<HOST_1>/api/webhooks/<TOKEN_1> 401",
			"POST https://<HOST_1>/api/webhooks/<TOKEN_1> <NUM>"},
		{"discord, port", "POST https://{DH}:8443/api/webhooks/{DP} 401", "POST https://<HOST_1>:8443/api/webhooks/<TOKEN_1> 401",
			"POST https://<HOST_1>:<NUM>/api/webhooks/<TOKEN_1> <NUM>"},
		{"discord, query", "POST https://{DH}/api/webhooks/{DP}?thread_id=123&wait=true 401",
			"POST https://<HOST_1>/api/webhooks/<TOKEN_1>?thread_id=123&wait=true 401",
			"POST https://<HOST_1>/api/webhooks/<TOKEN_1>?thread_id=<NUM>&wait=true <NUM>"},
		{"discord, json message", `{"level":"error","msg":"webhook https://{DH}/api/webhooks/{DP} failed"}`,
			`{"level":"error","msg":"webhook https://<HOST_1>/api/webhooks/<TOKEN_1> failed"}`,
			`{"level":<STR>,"msg":"webhook https://<HOST_1>/api/webhooks/<TOKEN_1> failed"}`},
		{"discord, no scheme", "url {DH}/api/webhooks/{DP}", "url {DH}/api/webhooks/<TOKEN_1>", "url {DH}/api/webhooks/<TOKEN_1>"},
		{"discord, the id alone", "GET https://{DH}/api/webhooks/{DID} 401", "GET https://<HOST_1>/api/webhooks/<TOKEN_1> 401",
			"GET https://<HOST_1>/api/webhooks/<TOKEN_1> <NUM>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := webhookFill.Replace(c.in)
			check(t, in, webhookFill.Replace(c.want))
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: in}))
			check(t, tIn, webhookFill.Replace(c.tmpl))
		})
	}

	// Byte-identical with v0.12.28: a URL on either host whose path is not the webhook prefix, the prefix
	// with nothing after it, the prefix on another host or after a longer one, and 4e's JSON row, where
	// a later '@' across the fields makes the whole run one userinfo TOKEN.
	for _, s := range []string{
		"https://{SH}/ ok",
		"https://{SH}/services/ ok",
		"https://{DH}/api/webhooks ok",
		"https://{DH}/api/webhooks/ ok",
		"https://{DH}/api/channels/{DID}/messages",
		"https://{DH}/invite/abcDEF12",
		"https://example.com/services/{SP}",
		"https://{SH}.evil.example/services/{SP}",
		"https://x{SH}/services/{SP}",
		"https://not{DH}/api/webhooks/{DP}",
		"https://{DH}/api/webhooksx/{DP}",
		"read the {DH}/api/webhooks docs",
		`{"url":"https://{SH}/services/{SP}","email":"bob@corp.com"}`,
	} {
		s = webhookFill.Replace(s)
		v, _ := mapperOn(atV01228(t, baseDetectors)).Mask(s)
		tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: s}))
		tv, _ := mapperOn(atV01228(t, baseDetectors)).Mask(tIn)
		for _, p := range []struct{ in, want string }{{s, v}, {tIn, tv}} {
			if got := mustMask(t, New(), p.in); got != p.want {
				t.Errorf("a shape outside #116 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
			}
		}
	}

	// A token an earlier rule tagged in part: an '@' after it makes it the local part of 5's email, and
	// 4f masks the ids before it, so nothing is sent.
	check(t, webhookFill.Replace("cb https://{SH}/services/{SP}@corp.com x"), "cb https://<HOST_1>/services/<TOKEN_1>/<EMAIL_1> x")

	// WHAT STAYS SENT, pinned, and named in README's Known limitations: Slack's workflow and trigger
	// webhooks, GovSlack's host, and a webhook URL percent-encoded inside another.
	for _, c := range []struct{ name, in, frag string }{
		{"slack workflow", "https://{SH}/workflows/T2O3Z5XAN/A0123/456/abc", "Z5XAN/A0123"},
		{"slack trigger", "https://{SH}/triggers/T2O3Z5XAN/456/abc", "Z5XAN/456"},
		{"govslack", "https://hooks.slack-gov.com/services/{SP}", "dGscig"},
		{"percent-encoded", "redirect=https%3A%2F%2F{SH}%2Fservices%2FT2O3Z5XAN%2FBMMNZ56PB9%2Fx", "BMMNZ"},
	} {
		t.Run("sent/"+c.name, func(t *testing.T) {
			in := webhookFill.Replace(c.in)
			want, _ := mapperOn(atV01228(t, baseDetectors)).Mask(in)
			if got := mustMask(t, New(), in); got != want || !strings.Contains(got, c.frag) {
				t.Errorf("a pinned shape moved -- update this test and README's Known limitations:\n in:   %q\n got:  %q\n want: %q", in, got, want)
			}
		})
	}

	// A path run into another URL's scheme: 9a reads the scheme glued to what precedes it, so 4f ends the
	// path at the last '/' before it and the segment carrying the scheme stays sent, as on v0.12.28; the
	// ids before it are masked. Named in README's Known limitations.
	{
		last := slackWebhookPath[strings.LastIndexByte(slackWebhookPath, '/')+1:]
		in := webhookFill.Replace("url https://{SH}/services/{SP}x_https://api.acme.com/v1")
		if got, want := mustMask(t, New(), in), "url https://<HOST_1>/services/<TOKEN_1>/"+last+"x_https://<HOST_2>/v1"; got != want {
			t.Errorf("a path run into another URL's scheme moved -- update this test and README's Known limitations:\n in:   %q\n got:  %q\n want: %q", in, got, want)
		}
	}

	// residue() reads 4f, so a raw webhook path mutes the escalation; v0.12.28 read these as clean. Its
	// context is the raw host, so a path behind a HOST tag is no longer one it can read.
	for _, in := range []string{
		"https://" + slackHookHost + "/services/" + slackWebhookPath,
		slackHookHost + "/services/" + slackWebhookPath,
		"post https://" + discordHost + "/api/webhooks/" + discordWebhookPath,
	} {
		if tag := New().residue(in); tag != tagToken {
			t.Errorf("a raw webhook path does not mute: residue(%q) = %q, want %q", in, tag, tagToken)
		}
	}

	// The escape view (GHSA-p47p-xrhj-3x7p): a URL in a JSON string is written with "\/" or "/",
	// which breaks the prefix on the line as written; the view resolves it, its pass masks the path, and
	// the union projects the span onto the escaped bytes.
	for _, f := range jsonEscapeForms {
		for _, c := range []struct{ name, in, want string }{
			{"slack", `{"msg":"post https://{SH}/services/{SP} failed"}`, `{"msg":"post https://<HOST_1>/services/<TOKEN_1> failed"}`},
			{"discord", `{"msg":"post https://{DH}/api/webhooks/{DP} failed"}`, `{"msg":"post https://<HOST_1>/api/webhooks/<TOKEN_1> failed"}`},
			{"slack, no scheme", `{"msg":"url {SH}/services/{SP}"}`, `{"msg":"url {SH}/services/<TOKEN_1>"}`},
		} {
			t.Run("escaped/"+c.name+"/"+f.name, func(t *testing.T) {
				in, want := f.esc(webhookFill.Replace(c.in)), f.esc(webhookFill.Replace(c.want))
				m := New()
				got := mustMask(t, m, in)
				if got != want {
					t.Errorf("an escaped webhook URL not masked:\n in:   %q\n got:  %q\n want: %q", in, got, want)
				} else if r := m.Restore(got); r != in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, r)
				}
				for _, frag := range sent {
					if strings.Contains(got, frag) {
						t.Errorf("a fragment survived: %q still in %q", frag, got)
					}
				}
			})
		}
	}
}
