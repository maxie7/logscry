// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// #116's invented AWS values: an access key id of each kind and two secret access keys, each forty
// characters of base64 with a '/' and a '+'. Written split, as #114's keys are, so that no source line
// carries an id or a whole key for a secret scanner to match; the strings are the same at run time.
const (
	awsAccessKeyID  = "AK" + "IA" + "T996RXGVAO6Q4JYL"
	awsSessionKeyID = "AS" + "IA" + "PWBAJ28SDKPT133I"
	awsSecretKey    = "vMlpu/3V9G8sysu8" + "1Xb5U5nszngilzmjK+wtTCYV"
	awsSecretKey2   = "tPY5p6yos/s94UWh" + "EI8g25+v4dKIMenlunKOiDFd"
)

// TestIssue116AWSSecretAccessKey is the named regression for #116's second class. An AWS secret access
// key has no prefix: forty characters of base64, which a bare run of that alphabet cannot tell from a
// hash or an identifier. So no detector read one, and it was sent whole beside its name in an env var,
// an INI or YAML file, a JSON document or a CLI flag, and beside its access key id in a credentials CSV
// or prose. v0.4.0 onward.
//
// Context identifies it, and only context. 13a reads the value after an aws_secret_access_key name in
// each spelling; 13b reads exactly forty characters after a TOKEN, which is where detector 2 or rule 11
// has put the access key id by then. Both run last, so every rule before them sees v0.12.28's input. The
// value is masked, never the name; a bare forty-character run with no context is sent, as it was.
func TestIssue116AWSSecretAccessKey(t *testing.T) {
	sent := []string{"vMlpu", "nszngilzmjK", "wtTCYV"}
	check := func(t *testing.T, in, want string) {
		t.Helper()
		m := New()
		got := mustMask(t, m, in)
		if got != want {
			t.Errorf("an AWS secret access key not masked:\n in:   %q\n got:  %q\n want: %q", in, got, want)
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
	fill := func(s string) string {
		return strings.NewReplacer("{S}", awsSecretKey, "{S2}", awsSecretKey2, "{ID}", awsAccessKeyID, "{SID}", awsSessionKeyID).Replace(s)
	}

	// 13a: the name in each spelling a log or a config file writes it, and the separators after it. The
	// template is TemplatizeLine's, the real Template field; a JSON line's values are <STR> there.
	named := []struct{ name, in, want, tmpl string }{
		{"env var", "AWS_SECRET_ACCESS_KEY={S}", "AWS_SECRET_ACCESS_KEY=<TOKEN_1>", "AWS_SECRET_ACCESS_KEY=<TOKEN_1>"},
		{"export", "export AWS_SECRET_ACCESS_KEY={S}", "export AWS_SECRET_ACCESS_KEY=<TOKEN_1>", "export AWS_SECRET_ACCESS_KEY=<TOKEN_1>"},
		{"env var, quoted", `AWS_SECRET_ACCESS_KEY="{S}"`, `AWS_SECRET_ACCESS_KEY="<TOKEN_1>"`, `AWS_SECRET_ACCESS_KEY=<STR>`},
		{"prefixed env var", "TF_VAR_aws_secret_access_key={S}", "TF_VAR_aws_secret_access_key=<TOKEN_1>", "TF_VAR_aws_secret_access_key=<TOKEN_1>"},
		{"ini", "aws_secret_access_key = {S}", "aws_secret_access_key = <TOKEN_1>", "aws_secret_access_key = <TOKEN_1>"},
		{"yaml", "  aws_secret_access_key: {S}", "  aws_secret_access_key: <TOKEN_1>", "aws_secret_access_key: <TOKEN_1>"},
		{"camelCase yaml", "secretAccessKey: {S}", "secretAccessKey: <TOKEN_1>", "secretAccessKey: <TOKEN_1>"},
		{"cli flag with '='", "run --aws-secret-access-key={S} --region x", "run --aws-secret-access-key=<TOKEN_1> --region x",
			"run --aws-secret-access-key=<TOKEN_1> --region x"},
		{"cli flag with a space", "run --aws-secret-access-key {S} --region x", "run --aws-secret-access-key <TOKEN_1> --region x",
			"run --aws-secret-access-key <TOKEN_1> --region x"},
		{"aws configure set", "aws configure set aws_secret_access_key {S}", "aws configure set aws_secret_access_key <TOKEN_1>",
			"aws configure set aws_secret_access_key <TOKEN_1>"},
		{"ruby hash", ":aws_secret_access_key => '{S}'", ":aws_secret_access_key => '<TOKEN_1>'", ":aws_secret_access_key => <STR>"},
		{"logfmt", "level=info aws_secret_access_key={S} region=x", "level=info aws_secret_access_key=<TOKEN_1> region=x",
			"level=info aws_secret_access_key=<TOKEN_1> region=x"},
		{"prose", "loaded AWS_SECRET_ACCESS_KEY {S} from env", "loaded AWS_SECRET_ACCESS_KEY <TOKEN_1> from env",
			"loaded AWS_SECRET_ACCESS_KEY <TOKEN_1> from env"},
		{"json", `{"aws_secret_access_key":"{S}"}`, `{"aws_secret_access_key":"<TOKEN_1>"}`, `{"aws_secret_access_key":<STR>}`},
		{"json, the sts response", `{"AccessKeyId": "{ID}", "SecretAccessKey": "{S}"}`,
			`{"AccessKeyId": "<TOKEN_1>", "SecretAccessKey": "<TOKEN_2>"}`, `{"AccessKeyId":<STR>,"SecretAccessKey":<STR>}`},
		{"json in a json message", `{"msg":"{\"aws_secret_access_key\":\"{S}\"}"}`, `{"msg":"{\"aws_secret_access_key\":\"<TOKEN_1>\"}"}`,
			`{"msg":"{<STR>:<STR>}"}`},
		{"logfmt msg", `level=error msg="AWS_SECRET_ACCESS_KEY={S} rejected"`, `level=error msg="AWS_SECRET_ACCESS_KEY=<TOKEN_1> rejected"`,
			`level=error msg="AWS_SECRET_ACCESS_KEY=<TOKEN_1> rejected"`},
		{"json message", `{"level":"error","msg":"aws_secret_access_key: {S}"}`, `{"level":"error","msg":"aws_secret_access_key: <TOKEN_1>"}`,
			`{"level":<STR>,"msg":"aws_secret_access_key: <TOKEN_1>"}`},
	}
	for _, c := range named {
		t.Run("named/"+c.name, func(t *testing.T) {
			in := fill(c.in)
			check(t, in, c.want)
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: in}))
			check(t, tIn, c.tmpl)
		})
	}

	// 13b: exactly forty characters after the access key id, separated as a credentials CSV, prose and
	// the usual delimiters separate them. The id is detector 2's TOKEN by the time 13b runs. A second
	// field of the same shape after the first is masked too, so the re-scan never reads the first's token
	// as the second's context.
	pair := []struct{ name, in, want string }{
		{"space", "creds {ID} {S} loaded", "creds <TOKEN_1> <TOKEN_2> loaded"},
		{"csv", "{ID},{S}", "<TOKEN_1>,<TOKEN_2>"},
		{"credentials csv row", "deploy,{ID},{S}", "deploy,<TOKEN_1>,<TOKEN_2>"},
		{"semicolon", "{ID}; {S}", "<TOKEN_1>; <TOKEN_2>"},
		{"pipe", "{ID} | {S} | ok", "<TOKEN_1> | <TOKEN_2> | ok"},
		{"colon", "{ID}:{S}", "<TOKEN_1>:<TOKEN_2>"},
		{"quoted csv", `"{ID}","{S}"`, `"<TOKEN_1>","<TOKEN_2>"`},
		{"session id", "{SID} {S}", "<TOKEN_1> <TOKEN_2>"},
		{"two fields, one token", "{ID},{S},{S2}", "<TOKEN_1>,<TOKEN_2>"},
		{"two pairs", "{ID} {S} {SID} {S2}", "<TOKEN_1> <TOKEN_3> <TOKEN_2> <TOKEN_4>"},
		{"end of text", "rotated {ID} {S}", "rotated <TOKEN_1> <TOKEN_2>"},
	}
	for _, c := range pair {
		t.Run("pair/"+c.name, func(t *testing.T) {
			in := fill(c.in)
			check(t, in, c.want)
			tIn, _ := pipeline.Templatize(in)
			if got := mustMask(t, New(), tIn); strings.Contains(tIn, "vMlpu") && strings.Count(got, "<TOKEN_") != strings.Count(c.want, "<TOKEN_") {
				t.Errorf("the pair's template is not masked field for field:\n in:  %q\n got: %q", tIn, got)
			}
			for _, frag := range sent {
				if got := mustMask(t, New(), tIn); strings.Contains(got, frag) {
					t.Errorf("a fragment survived on the template: %q still in %q", frag, got)
				}
			}
		})
	}

	// Byte-identical with v0.12.28: a value an earlier rule owns stays its, and a name with nothing of the
	// key's shape after it is prose. On the template NUM has broken the run and its count is lost, as it
	// is for twilioLonger, so a value one short or one long of a key is read as one there: a cost, pinned
	// after this loop, and the template of those rows is not compared here.
	lengthOnly := map[string]bool{
		"AWS_SECRET_ACCESS_KEY=" + awsSecretKey[:39]:         true,
		"creds " + awsAccessKeyID + " " + awsSecretKey + "x": true,
	}
	for _, s := range []string{
		"aws_secret_access_key is missing",
		"AWS_SECRET_ACCESS_KEY not set for profile2",
		"aws_secret_access_key: bob@corp.com",
		"AWS_SECRET_ACCESS_KEY=" + awsSecretKey[:39],
		"AWS_SECRET_ACCESS_KEY= ${AWS_SECRET}",
		"creds " + awsAccessKeyID + " rotated by the operator yesterday",
		"creds " + awsAccessKeyID + " " + awsSecretKey + "x",
		"creds " + awsAccessKeyID + " " + awsSecretKey + "==",
	} {
		v, _ := mapperOn(atV01228(t, baseDetectors)).Mask(s)
		tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: s}))
		tv, _ := mapperOn(atV01228(t, baseDetectors)).Mask(tIn)
		for _, p := range []struct{ in, want string }{{s, v}, {tIn, tv}} {
			if p.in == tIn && lengthOnly[s] {
				continue
			}
			if got := mustMask(t, New(), p.in); got != p.want {
				t.Errorf("a shape outside #116 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
			}
		}
	}
	for s := range lengthOnly {
		tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: s}))
		if got := mustMask(t, New(), tIn); strings.Contains(got, "nszngilzmjK") {
			t.Errorf("the template of a value one off a key's length is no longer read as one -- update this test:\n in:  %q\n got: %q", tIn, got)
		}
	}

	// WHAT STAYS SENT, pinned, and named in README's Known limitations: a bare forty-character key with no
	// context, a key after a name outside the list, a pair field that is not exactly forty, and a session
	// token after the key.
	for _, c := range []struct{ name, in, want string }{
		{"a bare key", "key " + awsSecretKey + " used", "key " + awsSecretKey + " used"},
		{"a name outside the list", "secret_key=" + awsSecretKey, "secret_key=" + awsSecretKey},
		{"AWS_SECRET_KEY", "AWS_SECRET_KEY=" + awsSecretKey, "AWS_SECRET_KEY=" + awsSecretKey},
		{"a pair field of forty-one", "creds " + awsAccessKeyID + " " + awsSecretKey + "Q", "creds <TOKEN_1> " + awsSecretKey + "Q"},
	} {
		t.Run("sent/"+c.name, func(t *testing.T) {
			if got := mustMask(t, New(), c.in); got != c.want {
				t.Errorf("a pinned shape moved -- update this test and README's Known limitations:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		})
	}

	// residue() reads both rules, so a raw key beside its name or its id mutes the escalation; v0.12.28
	// read these as clean.
	for _, in := range []string{
		"AWS_SECRET_ACCESS_KEY=" + awsSecretKey,
		`{"SecretAccessKey":"` + awsSecretKey + `"}`,
		"<TOKEN_1>," + awsSecretKey,
	} {
		if tag := New().residue(in); tag != tagToken {
			t.Errorf("a raw AWS secret does not mute: residue(%q) = %q, want %q", in, tag, tagToken)
		}
	}

	// The escape view (GHSA-p47p-xrhj-3x7p): the key's '/' written "\/" or "/" breaks the run on the
	// line as written; the view resolves it, its pass masks the value, and the union projects the span
	// onto the escaped bytes. The same for a ':' after the name written ":".
	for _, f := range jsonEscapeForms {
		for _, c := range []struct{ name, in, want string }{
			{"json", `{"aws_secret_access_key":"{S}"}`, `{"aws_secret_access_key":"<TOKEN_1>"}`},
			{"json message, pair", `{"msg":"creds {ID},{S} loaded"}`, `{"msg":"creds <TOKEN_1>,<TOKEN_2> loaded"}`},
		} {
			t.Run("escaped/"+c.name+"/"+f.name, func(t *testing.T) {
				in, want := f.esc(fill(c.in)), f.esc(c.want)
				m := New()
				if got := mustMask(t, m, in); got != want {
					t.Errorf("an escaped AWS secret not masked:\n in:   %q\n got:  %q\n want: %q", in, got, want)
				} else if r := m.Restore(got); r != in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, r)
				}
			})
		}
	}
}

// TestIssue116AWSPairFieldEndsWhereTheViewEndsIt pins 13b's end (awsPairEnd): an escape of '/' after a field
// continues the field, as the escape view reads it, so a field the view reads as forty-one is not forty as
// written either, and a request v0.12.28 sent is not refused by the re-scan of the line as written. Any
// other escape ends it.
func TestIssue116AWSPairFieldEndsWhereTheViewEndsIt(t *testing.T) {
	for _, esc := range []string{`\/`, `\u002F`, `\u002f`, `\\u002F`} {
		for _, in := range []string{
			"creds " + awsAccessKeyID + " " + awsSecretKey + esc + " loaded",
			`{"msg":"creds ` + awsAccessKeyID + "," + awsSecretKey + esc + `x"}`,
		} {
			want, wantErr := mapperOn(atV01228(t, baseDetectors)).Mask(in)
			got, err := New().Mask(in)
			if got != want || errText(err) != errText(wantErr) {
				t.Errorf("a field the view reads as longer than forty moved:\n in:   %q\n got:  %q (%v)\n want: %q (%v)", in, got, err, want, wantErr)
			}
		}
	}
	for _, c := range []struct{ in, want string }{
		{"creds {ID} {S}\u0022 loaded", "creds <TOKEN_1> <TOKEN_2>\u0022 loaded"},
		{`{"msg":"creds {ID} {S}\nnext"}`, `{"msg":"creds <TOKEN_1> <TOKEN_2>\nnext"}`},
		{`{"msg":"creds {ID},{S}\\"}`, `{"msg":"creds <TOKEN_1>,<TOKEN_2>\\"}`},
		// thirty-nine and an escaped '/' are forty in the view, whose pass masks them
		{`{"msg":"creds {ID} {S39}\u002F loaded"}`, `{"msg":"creds <TOKEN_1> <TOKEN_2> loaded"}`},
	} {
		in := strings.NewReplacer("{ID}", awsAccessKeyID, "{S39}", awsSecretKey[:39], "{S}", awsSecretKey).Replace(c.in)
		m := New()
		if got := mustMask(t, m, in); got != c.want {
			t.Errorf("a pair field before an escape not masked:\n in:   %q\n got:  %q\n want: %q", in, got, c.want)
		} else if r := m.Restore(got); r != in {
			t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, r)
		}
	}
}

// TestSlashEscapesAreWhatTheViewReadsAsSlash holds slashEscapes to escapeForm both ways: each is read
// whole after a backslash and resolves to '/', and no other escape the view reads resolves to '/'.
func TestSlashEscapesAreWhatTheViewReadsAsSlash(t *testing.T) {
	listed := make(map[string]bool)
	for _, e := range slashEscapes() {
		listed[e] = true
		if loc := escapeForm.FindStringIndex(`\` + e); loc == nil || loc[0] != 0 || loc[1] != len(e)+1 || resolved(`\`+e) != '/' {
			t.Errorf("slashEscapes lists %q, which the escape view does not read as '/'", e)
		}
	}
	var forms []string
	for _, e := range []string{"/", "u002f", "u002F", "u003a", "u003A", "u0040", "u0022", "u002b", "u003d", "n", `"`} {
		tpl, _ := pipeline.Templatize(`\` + e)
		forms = append(forms, e, strings.TrimPrefix(tpl, `\`))
	}
	for _, e := range forms {
		loc := escapeForm.FindStringIndex(`\` + e)
		if loc != nil && loc[0] == 0 && loc[1] == len(e)+1 && resolved(`\`+e) == '/' && !listed[e] {
			t.Errorf("the escape view reads %q as '/', and slashEscapes misses it", e)
		}
	}
}
