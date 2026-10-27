// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"regexp"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// templateOf is what the Template field carries for raw: the pipeline's TemplatizeLine, which templates
// a JSON or logfmt line on its keys and decodes its message field, and a plain line with the text masker.
func templateOf(raw string) string {
	p, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: raw}))
	return p
}

// jsonEscapeRows are GHSA-p47p-xrhj-3x7p's shapes: a URL whose '/', ':' or '@' a JSON encoder escaped.
// Every URL rule anchors on a literal "://" and stops at a literal '/', so "postgres:\/\/user:pass@host\/db"
// matched no rule, the username, the password and the host were sent, and residue() read the line as
// clean. v0.4.0 onward.
//
// The detectors read a view of the line with those escapes resolved, and every value they mask there is
// projected back onto the line's own bytes, so the model gets the text as the log wrote it, except inside
// a placeholder. want and tmpl are the raw line's and the Template field's output (templateOf); sent are
// fragments of the credential and the host, none of which may survive on either.
var jsonEscapeRows = []struct {
	name, in, want, tmpl string
	sent                 []string
}{
	{"the advisory's line", `postgres:\/\/svcuser:pwsecret@pghost\/db`, `postgres:\/\/<TOKEN_1>@<HOST_1>\/db`,
		`postgres:\/\/<TOKEN_1>@<HOST_1>\/db`, []string{"svcuser", "pwsecret", "pghost"}},
	// On the Template field a plain line's "\u002f" is "\u<NUM>f": the view reads the pipeline's form too.
	{"u002f", `dsn=postgres:\u002f\u002fsvcuser:pwsecret@pghost\u002fdb`, `dsn=postgres:\u002f\u002f<TOKEN_1>@<HOST_1>\u002fdb`,
		`dsn=postgres:\u<NUM>f\u<NUM>f<TOKEN_1>@<HOST_1>\u<NUM>fdb`, []string{"svcuser", "pwsecret", "pghost"}},
	{"u002F", `dsn=postgres:\u002F\u002Fsvcuser:pwsecret@pghost\u002Fdb`, `dsn=postgres:\u002F\u002F<TOKEN_1>@<HOST_1>\u002Fdb`,
		`dsn=postgres:\u<NUM>F\u<NUM>F<TOKEN_1>@<HOST_1>\u<NUM>Fdb`, []string{"svcuser", "pwsecret", "pghost"}},
	// A JSON string inside a JSON string, its inner encoder escaping '/' and its outer one not.
	{"a run of two backslashes", `dsn=postgres:\\/\\/svcuser:pwsecret@pghost\\/db`, `dsn=postgres:\\/\\/<TOKEN_1>@<HOST_1>\\/db`,
		`dsn=postgres:\\/\\/<TOKEN_1>@<HOST_1>\\/db`, []string{"svcuser", "pwsecret", "pghost"}},
	{"a run of three backslashes", `dsn=postgres:\\\/\\\/svcuser:pwsecret@pghost\\\/db`, `dsn=postgres:\\\/\\\/<TOKEN_1>@<HOST_1>\\\/db`,
		`dsn=postgres:\\\/\\\/<TOKEN_1>@<HOST_1>\\\/db`, []string{"svcuser", "pwsecret", "pghost"}},
	// An encoder that escapes ':' and '@' as well. The escaped '@' between the two values is the
	// delimiter, and stays.
	{"':' and '@' escaped in a json message", `{"msg":"dsn=postgres\u003a\/\/svcuser\u003apwsecret\u0040pghost\/db"}`,
		`{"msg":"dsn=postgres\u003a\/\/<TOKEN_1>\u0040<HOST_1>\/db"}`, `{"msg":"dsn=postgres://<TOKEN_1>@<HOST_1>/db"}`,
		[]string{"svcuser", "pwsecret", "pghost"}},
	// Any JSON field but the message is <STR> on the Template field: the value never reaches it as text.
	{"a json field", `{"level":"fatal","dsn":"postgres:\/\/svcuser:pwsecret@pghost\/db"}`,
		`{"level":"fatal","dsn":"postgres:\/\/<TOKEN_1>@<HOST_1>\/db"}`, `{"dsn":<STR>,"level":<STR>}`,
		[]string{"svcuser", "pwsecret", "pghost"}},
	// The message field is decoded by the pipeline, once: one level of escaping arrives as plain "://".
	{"a json message", `{"level":"fatal","msg":"connect postgres:\/\/svcuser:pwsecret@pghost\/db failed"}`,
		`{"level":"fatal","msg":"connect postgres:\/\/<TOKEN_1>@<HOST_1>\/db failed"}`,
		`{"level":<STR>,"msg":"connect postgres://<TOKEN_1>@<HOST_1>/db failed"}`, []string{"svcuser", "pwsecret", "pghost"}},
	{"a json message, u002f", `{"level":"fatal","msg":"connect postgres:\u002f\u002fsvcuser:pwsecret@pghost\u002fdb failed"}`,
		`{"level":"fatal","msg":"connect postgres:\u002f\u002f<TOKEN_1>@<HOST_1>\u002fdb failed"}`,
		`{"level":<STR>,"msg":"connect postgres://<TOKEN_1>@<HOST_1>/db failed"}`, []string{"svcuser", "pwsecret", "pghost"}},
	// Two levels: one is left after the pipeline decodes the message, and re-quoting doubles its backslash.
	// v0.12.26 sent this one on the Template field too.
	{"a json message, two levels", `{"level":"fatal","msg":"connect postgres:\\/\\/svcuser:pwsecret@pghost\\/db failed"}`,
		`{"level":"fatal","msg":"connect postgres:\\/\\/<TOKEN_1>@<HOST_1>\\/db failed"}`,
		`{"level":<STR>,"msg":"connect postgres:\\/\\/<TOKEN_1>@<HOST_1>\\/db failed"}`, []string{"svcuser", "pwsecret", "pghost"}},
	{"a json message, two levels, u002f", `{"msg":"connect postgres:\\u002f\\u002fsvcuser:pwsecret@pghost\\u002fdb failed"}`,
		`{"msg":"connect postgres:\\u002f\\u002f<TOKEN_1>@<HOST_1>\\u002fdb failed"}`,
		`{"msg":"connect postgres:\\u<NUM>f\\u<NUM>f<TOKEN_1>@<HOST_1>\\u<NUM>fdb failed"}`, []string{"svcuser", "pwsecret", "pghost"}},
	{"json inside a json string", `{"inner":"{\"dsn\":\"postgres:\\\/\\\/svcuser:pwsecret@pghost\\\/db\"}"}`,
		`{"inner":"{\"dsn\":\"postgres:\\\/\\\/<TOKEN_1>@<HOST_1>\\\/db\"}"}`, `{"inner":<STR>}`,
		[]string{"svcuser", "pwsecret", "pghost"}},
	// logfmt's quoted message is unquoted with Go syntax, which refuses "\/", so it is kept and re-quoted:
	// "\\/" on the Template field. v0.12.26 sent it there.
	{"a logfmt message", `level=error msg="connect postgres:\/\/svcuser:pwsecret@pghost\/db failed"`,
		`level=error msg="connect postgres:\/\/<TOKEN_1>@<HOST_1>\/db failed"`,
		`level=error msg="connect postgres:\\/\\/<TOKEN_1>@<HOST_1>\\/db failed"`, []string{"svcuser", "pwsecret", "pghost"}},
	{"a logfmt bare value", `level=error dsn=postgres:\/\/svcuser:pwsecret@pghost\/db`,
		`level=error dsn=postgres:\/\/<TOKEN_1>@<HOST_1>\/db`, `level=error dsn=postgres:\/\/<TOKEN_1>@<HOST_1>\/db`,
		[]string{"svcuser", "pwsecret", "pghost"}},
	{"plain text", `connect postgres:\/\/svcuser:pwsecret@pghost\/db failed`, `connect postgres:\/\/<TOKEN_1>@<HOST_1>\/db failed`,
		`connect postgres:\/\/<TOKEN_1>@<HOST_1>\/db failed`, []string{"svcuser", "pwsecret", "pghost"}},
	// v0.12.26's email rule took "pwsecret@db.acme.com" by accident and sent the username. That span covers
	// the '@', so the credential and the host are one token here (jsonEscapeOverMaskingRows).
	{"a dotted host", `dsn=postgres:\/\/svcuser:pwsecret@db.acme.com\/prod`, `dsn=postgres:\/\/<TOKEN_1>\/prod`,
		`dsn=postgres:\/\/<TOKEN_1>\/prod`, []string{"svcuser", "pwsecret", "acme"}},
	// On the Template field the address is the pipeline's <IP>, which 9a reads as the host, as without
	// the escapes.
	{"an address and a port", `dsn=postgres:\/\/svcuser:pwsecret@10.0.0.5:5432\/app`, `dsn=postgres:\/\/<TOKEN_1>@<IP_1>:5432\/app`,
		`dsn=postgres:\/\/<TOKEN_1>@<HOST_1>:<NUM>\/app`, []string{"svcuser", "pwsecret", "10.0.0.5"}},
	{"no password", `dsn=postgres:\/\/svcuser@pghost:5432\/app`, `dsn=postgres:\/\/<TOKEN_1>@<HOST_1>:5432\/app`,
		`dsn=postgres:\/\/<TOKEN_1>@<HOST_1>:<NUM>\/app`, []string{"svcuser", "pghost"}},
	{"no username", `dsn=redis:\/\/:pwsecret@pghost:6379\/0`, `dsn=redis:\/\/<TOKEN_1>@<HOST_1>:6379\/0`,
		`dsn=redis:\/\/<TOKEN_1>@<HOST_1>:<NUM>\/<NUM>`, []string{"pwsecret", "pghost"}},
	// #59's shape escaped: 4e takes the authority whole in the view.
	{"a raw '/' in the password", `dsn=postgres:\/\/svcuser:pw\/secret@pghost\/app`, `dsn=postgres:\/\/<TOKEN_1>\/app`,
		`dsn=postgres:\/\/<TOKEN_1>\/app`, []string{"svcuser", "secret", "pghost"}},
	{"a host list", `dsn=mongodb:\/\/svcuser:pwsecret@pghost:27017,pgreplica:27017\/db`,
		`dsn=mongodb:\/\/<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017\/db`, `dsn=mongodb:\/\/<TOKEN_1>@<HOST_1>:<NUM>,<HOST_2>:<NUM>\/db`,
		[]string{"svcuser", "pwsecret", "pghost", "pgreplica"}},
	{"a url host", `url=https:\/\/api.acme.com\/v1`, `url=https:\/\/<HOST_1>\/v1`, `url=https:\/\/<HOST_1>\/v<NUM>`,
		[]string{"acme"}},
	{"a key glued inside a userinfo", `dsn=s3:\/\/` + slashUserinfoAWS + `.prod:pwsecret@pghost\/x`, `dsn=s3:\/\/<TOKEN_1>@<HOST_1>\/x`,
		`dsn=s<NUM>:\/\/<TOKEN_1>@<HOST_1>\/x`, []string{"EXAMPLE", ".prod", "pwsecret", "pghost"}},
	{"a home path", `path=\/home\/bob\/x`, `path=\/home\/<USER_1>\/x`, `path=\/home\/<USER_1>\/x`, []string{"bob"}},
	{"an email in a json message", `{"msg":"owner bob\u0040corp.com left"}`, `{"msg":"owner <EMAIL_1> left"}`,
		`{"msg":"owner <EMAIL_1> left"}`, []string{"bob", "corp"}},
}

// TestJSONEscapedURL is the named regression for GHSA-p47p-xrhj-3x7p, with exact outputs for the raw line
// and the Template field, a round trip, and the absence of every fragment of the credential and the host.
func TestJSONEscapedURL(t *testing.T) {
	for _, c := range jsonEscapeRows {
		t.Run(c.name, func(t *testing.T) {
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {templateOf(c.in), c.tmpl}} {
				m := New()
				got := mustMask(t, m, p.in)
				if got != p.want {
					t.Errorf("a JSON-escaped value not masked as its unescaped form:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
				for _, s := range c.sent {
					if strings.Contains(got, s) {
						t.Errorf("%q sent:\n in:  %q\n got: %q", s, p.in, got)
					}
				}
				if r := m.Restore(got); r != p.in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", p.in, r)
				}
			}
		})
	}
}

// jsonEscapeWitnessRows are the shapes on which v0.12.26 read an escape's own characters as part of a
// value, so masking the view alone would mask LESS: 4e takes the '\' of "\/" for the character that makes
// a password's head no port, detector 4 takes "us\u002fer" for a username, and the email rule takes
// "u002fx" for a local part. Unescaped, each is a shape that masks less (#59's numeric-head and
// password-less residuals, a shorter local part). Every span v0.12.26 masks is kept, so each is masked
// exactly as v0.12.26 masks it. want is v0.12.26's output.
var jsonEscapeWitnessRows = []struct{ name, in, want string }{
	{"a '\\' that makes a password's head no port", `postgres://user:1234\/5@db`, `postgres://<TOKEN_1>`},
	{"a username detector 4 reads with the escape in it", `postgres://us\u002fer@db`, `postgres://<TOKEN_1>@<HOST_1>`},
	{"a local part the email rule reads with the escape in it", `bob\u002fx@corp.com`, `bob\<EMAIL_1>`},
}

// TestJSONEscapeKeepsWhatV01226Masked pins the rows above.
func TestJSONEscapeKeepsWhatV01226Masked(t *testing.T) {
	for _, c := range jsonEscapeWitnessRows {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			got := mustMask(t, m, c.in)
			if got != c.want {
				t.Errorf("a span v0.12.26 masked moved:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
			if r := m.Restore(got); r != c.in {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", c.in, r)
			}
		})
	}
}

// jsonEscapeSameRows carry an escape the view resolves and nothing a rule reads there, so each comes back
// exactly as v0.12.26 returns it, raw and on the Template field: a "\/" in prose that is no URL,
// Python's ASCII-only JSON of a Cyrillic word (its "\u043f" is "\u<NUM>f" on the Template field, which
// the view reads as '/'), .NET's "\u0027", and an escaped path with no host.
var jsonEscapeSameRows = []struct{ name, in, want, tmpl string }{
	{"a regex in prose", `sed s\/foo\/bar\/g failed`, `sed s\/foo\/bar\/g failed`, `sed s\/foo\/bar\/g failed`},
	{"python's ascii-only json of a cyrillic word",
		`app \u043f\u0440\u0438\u043b\u043e\u0436\u0435\u043d\u0438\u0435 stopped`,
		`app \u043f\u0440\u0438\u043b\u043e\u0436\u0435\u043d\u0438\u0435 stopped`,
		`app \u<NUM>f\u<NUM>\u<NUM>\u<NUM>b\u<NUM>e\u<NUM>\u<NUM>\u<NUM>d\u<NUM>\u<NUM> stopped`},
	{".net's escaped apostrophe", `user said it\u0027s down`, `user said it\u0027s down`, `user said it\u<NUM>s down`},
	{"an escaped path and no host", `{"path":"\/var\/log\/app.log","msg":"rotated"}`, `{"path":"\/var\/log\/app.log","msg":"rotated"}`,
		`{"msg":"rotated","path":<STR>}`},
}

// TestJSONEscapeLeavesTheseAsV01226 pins the rows above. The superset gate also holds each to v0.12.26.
func TestJSONEscapeLeavesTheseAsV01226(t *testing.T) {
	for _, c := range jsonEscapeSameRows {
		t.Run(c.name, func(t *testing.T) {
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {templateOf(c.in), c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("a line with nothing to mask moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}
}

// jsonEscapeResidualRows are the escapes the view does not resolve, each pinned as sent, and named in
// README's Known limitations.
//
//   - ':' and '@' as the pipeline leaves them on a plain or logfmt line's Template field, "\u<NUM>a" and
//     "\u<NUM>": the second is also the template of every "\u00" escape with two digits after it -- .NET
//     writes the double quote, the apostrophe and '&' that way -- so reading it as '@' would be a guess. The raw line is masked,
//     and a JSON line's message field is decoded by the pipeline before it is templated.
//   - a '.' escaped as "\u002e": no encoder is known to write it, and the host is masked up to it.
//
// leak is a fragment of tmpl that is sent; the row goes red if it is ever masked.
var jsonEscapeResidualRows = []struct{ name, in, want, tmpl, leak string }{
	{"':' and '@' on the template of a plain line", `dsn=postgres:\/\/svcuser\u003apwsecret\u0040pghost\/db`,
		`dsn=postgres:\/\/<TOKEN_1>\u0040<HOST_1>\/db`, `dsn=postgres:\/\/<HOST_1>\u<NUM>apwsecret\u<NUM>pghost\/db`, "pwsecret"},
	{"a dot escaped as u002e", `url=https:\/\/api\u002eacme\u002ecom\/v1`, `url=https:\/\/<HOST_1>\u002eacme\u002ecom\/v1`,
		`url=https:\/\/<HOST_1>\u<NUM>eacme\u<NUM>ecom\/v<NUM>`, "acme"},
}

// TestJSONEscapeShapesNotReached pins the rows above.
func TestJSONEscapeShapesNotReached(t *testing.T) {
	for _, c := range jsonEscapeResidualRows {
		t.Run(c.name, func(t *testing.T) {
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {templateOf(c.in), c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("an escape left unread moved -- update this test and README's Known limitations:\n"+
						" in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
			if !strings.Contains(c.tmpl, c.leak) {
				t.Errorf("the pinned template does not carry the leak it names: %q", c.leak)
			}
		})
	}
}

// jsonEscapeForms are the escapes the view resolves, each as an encoder writes it over a whole line: "\/",
// "\u002f" in both cases, a JSON string inside another ("\\/"), and an encoder that escapes ':' and '@' too.
var jsonEscapeForms = []struct {
	name string
	esc  func(string) string
}{
	{"slash", strings.NewReplacer("/", `\/`).Replace},
	{"u002f", strings.NewReplacer("/", `\u002f`).Replace},
	{"u002F", strings.NewReplacer("/", `\u002F`).Replace},
	{"nested", strings.NewReplacer("/", `\\/`).Replace},
	{"colon and at", strings.NewReplacer("/", `\/`, ":", `\u003a`, "@", `\u0040`).Replace},
}

// jsonEscapeTwinSources are the completeness rows of every rule anchored on "://": 4, 4b and 4c, the empty
// halves and the password-less userinfo, 4d, 4e, 9a, 9c and 9e. Each is read by name, so no row moves.
var jsonEscapeTwinSources = []string{
	"url-credentials", "url-credentials/multi-colon", "url-credentials/pct-encoded-at",
	"url-credentials/raw-at-in-password", "url-credentials/raw-at-in-username", "url-credentials/raw-at-no-password",
	"url-credentials/raw-at-two", "url-credentials/raw-at-multi-host",
	"url-credentials/premasked-username", "url-credentials/premasked-username/raw-at-password",
	"url-credentials/premasked-password", "url-credentials/premasked-password/raw-at-username",
	"url-credentials/empty-username", "url-credentials/empty-password",
	"url-credentials/no-password/bare-host", "url-credentials/no-password/loopback", "url-credentials/no-password/dotted-host",
	"url-credentials/no-password/private-host", "url-credentials/no-password/address-host",
	"url-credentials/glued-key", "url-credentials/glued-key/before", "url-credentials/glued-key/password",
	"url-credentials/raw-slash-in-password", "url-credentials/raw-slash-in-username",
	"url-host", "url-host/digit-scheme", "url-host/underscore",
	"url-host/list-rest", "url-host/list-rest/no-ports",
	"url-host/glued-key", "url-host/list-member/glued-key",
}

// jsonEscapeAtMasked are the twin sources on which v0.12.26's email rule masked the credential's tail and
// the host as one address -- a raw '@' before a dotted or private host -- so it masked the '@' too. Every
// byte v0.12.26 masked stays masked, so with any form that keeps the '@' raw the userinfo, the '@' and the
// host are one TOKEN: the host loses its own tag and is not seen to recur. want is the unescaped form of
// that output; jsonEscapeOverMaskingRows escapes it per form.
var jsonEscapeAtMasked = []struct{ name, want string }{
	{"url-credentials", "dsn=postgres://<TOKEN_1>/prod"},
	{"url-credentials/multi-colon", "dsn=postgres://<TOKEN_1>/prod"},
	{"url-credentials/pct-encoded-at", "dsn=postgres://<TOKEN_1>/prod"},
	{"url-credentials/raw-at-in-username", "dsn=postgres://<TOKEN_1>/prod"},
	{"url-credentials/premasked-username", "dsn=postgres://<TOKEN_1>:<TOKEN_2>/prod"},
	{"url-credentials/premasked-username/raw-at-password", "dsn=postgres://<TOKEN_1>:<TOKEN_2>/prod"},
	{"url-credentials/empty-username", "dsn=redis://<TOKEN_1>:6379/0"},
	{"url-credentials/no-password/dotted-host", "dsn=postgres://<TOKEN_1>/prod"},
	{"url-credentials/no-password/private-host", "dsn=postgres://<TOKEN_1>:6379/0"},
}

// jsonEscapeLettersMasked are twins on which v0.12.26 read the letters of an escape as part of a value, as
// it does for jsonEscapeWitnessRows: "u002fappuser" as an email's local part, "u0040cache.acme.internal"
// as a host on a private suffix. Every byte v0.12.26 masked stays masked, so the value takes those
// letters, and the escape's backslash is left beside the placeholder; with the '@' escaped, the host is
// cut there and keeps its own tag. Nothing is sent: TestJSONEscapedTwinsMaskWhatTheUnescapedRowMasks
// still asserts that no part of the credential or the host survives.
var jsonEscapeLettersMasked = []struct{ name, in, want string }{
	{"the escape's letters v0.12.26 masked, url-credentials/no-password/dotted-host (u002f)",
		`dsn=postgres:\u002f\u002fappuser@db.acme.com\u002fprod`, `dsn=postgres:\u002f\<TOKEN_1>\u002fprod`},
	{"the escape's letters v0.12.26 masked, url-credentials/no-password/dotted-host (u002F)",
		`dsn=postgres:\u002F\u002Fappuser@db.acme.com\u002Fprod`, `dsn=postgres:\u002F\<TOKEN_1>\u002Fprod`},
	{"the escape's letters v0.12.26 masked, url-credentials/no-password/private-host (u002f)",
		`dsn=postgres:\u002f\u002fappuser@cache.acme.internal:6379\u002f0`, `dsn=postgres:\u002f\<TOKEN_1>:6379\u002f0`},
	{"the escape's letters v0.12.26 masked, url-credentials/no-password/private-host (u002F)",
		`dsn=postgres:\u002F\u002Fappuser@cache.acme.internal:6379\u002F0`, `dsn=postgres:\u002F\<TOKEN_1>:6379\u002F0`},
	{"the escape's letters v0.12.26 masked, url-credentials/empty-username (colon and at)",
		`dsn=redis\u003a\/\/\u003as3cr3tpw\u0040cache.acme.internal\u003a6379\/0`, `dsn=redis\u003a\/\/<TOKEN_1>\<HOST_1>\u003a6379\/0`},
	{"the escape's letters v0.12.26 masked, url-credentials/no-password/private-host (colon and at)",
		`dsn=postgres\u003a\/\/appuser\u0040cache.acme.internal\u003a6379\/0`, `dsn=postgres\u003a\/\/<TOKEN_1>\<HOST_1>\u003a6379\/0`},
}

// jsonEscapeOverMaskingRows are GHSA-p47p-xrhj-3x7p's accepted over-masking rows, at package level for
// the gate's sake as #59's are. TestAcceptedOverMasking runs them.
//
// The first is the cost of resolving "\/" everywhere rather than only inside a JSON string: a path that
// escapes its slashes is a path, so a home directory on it is masked. Then jsonEscapeAtMasked's twins,
// one per form that keeps the '@' raw, and jsonEscapeLettersMasked.
var jsonEscapeOverMaskingRows = append([]struct {
	name string
	in   string
	want string
}{
	{"an escaped path outside json is read as a path", `copy C:\/Users\/bob\/x failed`, `copy C:\/Users\/<USER_1>\/x failed`},
}, append(jsonEscapeAtMaskedTwins(), jsonEscapeLettersMasked...)...)

// jsonEscapeAtMaskedTwins escapes each jsonEscapeAtMasked row's completeness payload and its want, in every
// form that keeps the '@' raw, but those jsonEscapeLettersMasked pins.
func jsonEscapeAtMaskedTwins() []struct{ name, in, want string } {
	letters := make(map[string]bool)
	for _, l := range jsonEscapeLettersMasked {
		letters[l.in] = true
	}
	var out []struct{ name, in, want string }
	for _, a := range jsonEscapeAtMasked {
		r, ok := completenessRowNamed(a.name)
		if !ok {
			panic("jsonEscapeAtMasked names no completeness row: " + a.name)
		}
		payload := payloadOf(r.prefix) + r.secret + payloadOf(r.suffix)
		for _, f := range jsonEscapeForms {
			if f.name == "colon and at" || letters[f.esc(payload)] {
				continue
			}
			out = append(out, struct{ name, in, want string }{
				"the '@' v0.12.26 masked, " + a.name + " (" + f.name + ")", f.esc(payload), f.esc(a.want)})
		}
	}
	return out
}

// completenessRowNamed returns the completeness row called name.
func completenessRowNamed(name string) (completenessRow, bool) {
	for _, r := range completenessRows {
		if r.name == name {
			return r, true
		}
	}
	return completenessRow{}, false
}

// valueParts splits a value into the parts a twin must not send: its runs between ':', '@' and '/', each
// three characters or more, in raw form and in the form f writes.
func valueParts(v string, esc func(string) string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ':' || r == '@' || r == '/' }) {
		if len(p) >= 3 {
			out = append(out, p, esc(p))
		}
	}
	return out
}

// TestJSONEscapedTwinsMaskWhatTheUnescapedRowMasks is point 5 of the advisory's remedy: every "://"-anchored
// rule's completeness row, and every one of #59's rows, escaped in each form, masks every value the
// unescaped row masks. Each twin asserts its exact output and the absence of every part of the credential
// and the host.
//
//   - A completeness twin must match the row's own pattern over its escaped prefix and suffix: exact text,
//     a placeholder exactly where the row declares one. Where v0.12.26 masked the '@' by accident the exact
//     output is jsonEscapeOverMaskingRows'.
//   - A #59 twin must equal the row's want escaped the same way, placeholder numbers included, and a
//     residual twin still sends its leak, escaped.
func TestJSONEscapedTwinsMaskWhatTheUnescapedRowMasks(t *testing.T) {
	atMasked := make(map[string]string)
	for _, r := range jsonEscapeOverMaskingRows {
		atMasked[r.in] = r.want
	}
	marked := regexp.MustCompile("\x00([^\x00]*)\x00")
	for _, name := range jsonEscapeTwinSources {
		r, ok := completenessRowNamed(name)
		if !ok {
			t.Fatalf("no completeness row named %q", name)
		}
		for _, f := range jsonEscapeForms {
			t.Run(name+"/"+f.name, func(t *testing.T) {
				in := f.esc(payloadOf(r.prefix) + r.secret + payloadOf(r.suffix))
				m := New()
				got := mustMask(t, m, in)
				if want, ok := atMasked[in]; ok {
					if got != want {
						t.Errorf("twin moved:\n in:   %q\n got:  %q\n want: %q", in, got, want)
					}
				} else if re := regexp.MustCompile(`^` + expand(f.esc(r.prefix)) + phPat + expand(f.esc(r.suffix)) + `$`); !re.MatchString(got) {
					t.Errorf("twin not masked as its row:\n in:   %q\n got:  %q\n want: %v", in, got, re)
				}
				parts := valueParts(r.secret, f.esc)
				for _, sm := range marked.FindAllStringSubmatch(r.prefix+r.suffix, -1) {
					parts = append(parts, valueParts(sm[1], f.esc)...)
				}
				for _, p := range parts {
					if strings.Contains(got, p) {
						t.Errorf("%q sent:\n in:  %q\n got: %q", p, in, got)
					}
				}
				if back := m.Restore(got); back != in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, back)
				}
			})
		}
	}
	for _, c := range slashUserinfoRows {
		for _, f := range jsonEscapeForms {
			t.Run("#59/"+c.name+"/"+f.name, func(t *testing.T) {
				in, want := f.esc(c.in), f.esc(c.want)
				m := New()
				got := mustMask(t, m, in)
				if got != want {
					t.Errorf("twin not masked as its row:\n in:   %q\n got:  %q\n want: %q", in, got, want)
				}
				for _, s := range c.sent {
					for _, p := range []string{s, f.esc(s)} {
						if strings.Contains(got, p) {
							t.Errorf("%q sent:\n in:  %q\n got: %q", p, in, got)
						}
					}
				}
				if back := m.Restore(got); back != in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, back)
				}
			})
		}
	}
	for _, c := range slashUserinfoResidualRows {
		for _, f := range jsonEscapeForms {
			t.Run("#59 by design/"+c.name+"/"+f.name, func(t *testing.T) {
				in, want := f.esc(c.in), f.esc(c.want)
				if got := mustMask(t, New(), in); got != want || !strings.Contains(got, f.esc(c.leak)) {
					t.Errorf("a shape sent by design moved -- update this test and README's Known limitations:\n"+
						" in:   %q\n got:  %q\n want: %q", in, got, want)
				}
			})
		}
	}
}

// BenchmarkMask measures the fast path and the escaped one: a line with no escape is masked as v0.12.26
// masks it, and a line with one runs the chain twice, as written and as its view.
func BenchmarkMask(b *testing.B) {
	for _, c := range []struct{ name, in string }{
		{"ordinary line", "connect postgres://svcuser:pwsecret@db.acme.com:5432/app failed from 10.0.0.5 for bob@corp.com"},
		{"one escaped dsn", `{"level":"error","dsn":"postgres:\/\/svcuser:pwsecret@pghost:5432\/app"}`},
		{"json line with several escaped urls", `{"msg":"fetch https:\/\/api.acme.com\/v1 failed","peer":"https:\/\/10.0.0.5:8443\/x",` +
			`"dsn":"postgres:\/\/svcuser:pwsecret@pghost\/app","home":"\/home\/bob\/x"}`},
		{"a line with vendor keys", "auth failed: stripe=" + stripeLiveSecret + " github=" + githubClassicSecret + " google=" +
			googleAPISecret + " creds " + awsAccessKeyID + " " + awsSecretKey},
	} {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := New().Mask(c.in); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
