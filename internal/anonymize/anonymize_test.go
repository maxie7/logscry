// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"regexp"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/pipeline"
)

// mustMask fails the test if masking errors — most tests are about WHAT gets masked, not
// the fail-closed path, so a spurious error there should be loud.
func mustMask(t *testing.T, m *Mapper, s string) string {
	t.Helper()
	out, err := m.Mask(s)
	if err != nil {
		t.Fatalf("Mask(%q) errored: %v", s, err)
	}
	return out
}

// TestRoundTrip: every supported type masks to a placeholder and restores to the original
// byte-for-byte. This is the core contract the whole feature rests on.
//
// WHAT THIS TEST CANNOT SEE: whether the value was masked in FULL. A round trip succeeds on
// a PARTIAL mask — that is not a gap in the assertion, it is structural. "peer fe80::1"
// masked to "peer <IP_1>1" restores to "peer fe80::1" byte-for-byte, because Restore maps
// <IP_1> back to "fe80::" and the leaked "1" was never removed. The leftover is precisely
// what makes the round trip succeed, so the check cannot fail on it. That row sat green here
// from v0.4.0 to v0.8.5 on top of a live leak (#41) — the same blindness residue() has, for
// the same reason. TestMaskingIsComplete is the check that does see it; this one stays for
// what it does prove, which is that Restore is a true inverse of Mask.
func TestRoundTrip(t *testing.T) {
	inputs := []string{
		"connection refused to 10.0.0.5 from user alice@corp.example.com",
		"GET https://api.internal:8443/orders failed for db-01.acme.internal",
		"auth header: Bearer abc123DEF456ghi789JKL",
		"request id 550e8400-e29b-41d4-a716-446655440000 timed out",
		"loaded config from /home/maxie/go/pkg/mod/example.com/pkg",
		"dsn=postgres://svcuser:hunter2@db.acme.com:5432/prod",
		"peer fe80::1 unreachable",
	}
	for _, in := range inputs {
		m := New()
		masked := mustMask(t, m, in)
		if masked == in {
			t.Errorf("nothing masked in %q", in)
		}
		if got := m.Restore(masked); got != in {
			t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, got)
		}
	}
}

// TestTypeTags: each detector emits its own tag, because the model needs to know it is
// looking at an IP versus a host versus a secret.
func TestTypeTags(t *testing.T) {
	cases := []struct {
		in  string
		tag string
	}{
		{"ip 192.168.1.1 down", "<IP_1>"},
		{"peer 2001:db8::ff00:42:8329 lost", "<IP_1>"},
		{"mail to bob@team.example.org", "<EMAIL_1>"},
		{"token Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig", "<TOKEN_1>"},
		{"key AKIAIOSFODNN7EXAMPLE used", "<TOKEN_1>"},
		{"id 550e8400-e29b-41d4-a716-446655440000 seen", "<UUID_1>"},
		{"host worker.svc failed", "<HOST_1>"},
		{"path /Users/jane/app.log missing", "<USER_1>"},
	}
	for _, c := range cases {
		m := New()
		masked := mustMask(t, m, c.in)
		if !strings.Contains(masked, c.tag) {
			t.Errorf("Mask(%q) = %q, want it to contain %s", c.in, masked, c.tag)
		}
	}
}

// TestStability: the same value maps to ONE placeholder, so the model can see it recur;
// distinct values get distinct placeholders.
func TestStability(t *testing.T) {
	m := New()
	// One value three times, across separate Mask calls (as trigger + context lines are).
	a := mustMask(t, m, "10.0.0.5 called 10.0.0.5")
	b := mustMask(t, m, "still 10.0.0.5")
	if strings.Count(a, "<IP_1>") != 2 || !strings.Contains(b, "<IP_1>") {
		t.Errorf("unstable placeholder: %q / %q", a, b)
	}
	if strings.Contains(a+b, "<IP_2>") {
		t.Errorf("a repeated value minted a second placeholder: %q %q", a, b)
	}

	// Two distinct values → two placeholders.
	m2 := New()
	out := mustMask(t, m2, "from 10.0.0.5 to 10.0.0.9")
	if !strings.Contains(out, "<IP_1>") || !strings.Contains(out, "<IP_2>") {
		t.Errorf("distinct values did not get distinct placeholders: %q", out)
	}
}

// TestNoOverMasking is the counter-weight: ordinary log prose — levels, exception classes,
// stack frames, Go module paths, versions, plain numbers, and BARE PUBLIC hostnames — must
// survive materially intact, or the explanation is worthless.
func TestNoOverMasking(t *testing.T) {
	survive := []string{
		"ERROR",
		"java.lang.NullPointerException",
		"at com.foo.Bar(Bar.java:42)",
		"github.com/docker/docker/client.(*Client).ContainerList",
		"golang.org/x/sync/errgroup",
		"go.uber.org/zap",
		"app version v1.2.3 started",
		"processed 4200 records in 12.5s",
		"GET /orders 200 in 42ms",
		"could not reach api.example.com", // bare PUBLIC host: accepted residual gap
	}
	for _, s := range survive {
		m := New()
		if got := mustMask(t, m, s); got != s {
			t.Errorf("over-masked ordinary text:\n in:  %q\n out: %q", s, got)
		}
	}
}

// TestURLAndConnStringHostsMaskedRegardlessOfTLD: a host inside a URL or connection string
// is masked even with a public TLD — the suffix allowlist governs only BARE hosts in prose.
func TestURLAndConnStringHostsMaskedRegardlessOfTLD(t *testing.T) {
	m := New()
	out := mustMask(t, m, "call https://internal.acme.com/v1 then postgres://u:p@db.acme.com/prod")
	for _, leaked := range []string{"internal.acme.com", "db.acme.com", "u:p"} {
		if strings.Contains(out, leaked) {
			t.Errorf("URL/conn-string secret leaked: %q still in %q", leaked, out)
		}
	}
	if !strings.Contains(out, "<HOST_1>") || !strings.Contains(out, "<TOKEN_1>") {
		t.Errorf("expected host + token placeholders, got %q", out)
	}
}

// TestResponseRestoresAllPlaceholders: the model echoes placeholders back; Restore puts the
// real values into every field it is called on.
func TestResponseRestoresAllPlaceholders(t *testing.T) {
	m := New()
	_ = mustMask(t, m, "trigger from 10.0.0.5 host db-01.acme.internal")
	echoed := "check connectivity to <IP_1> and DNS for <HOST_1>"
	got := m.Restore(echoed)
	want := "check connectivity to 10.0.0.5 and DNS for db-01.acme.internal"
	if got != want {
		t.Errorf("Restore = %q, want %q", got, want)
	}
}

// TestFailClosedOnResidue: the verifier reports residue when a raw, deterministic-shape
// value is present. Verified by feeding residue() an unmasked string directly, since the
// detectors are correct enough that Mask does not normally leave residue by construction.
func TestFailClosedOnResidue(t *testing.T) {
	m := New()
	if tag := m.residue("raw 10.0.0.5 slipped through"); tag != tagIP {
		t.Errorf("residue did not catch a raw IP, got %q", tag)
	}
	if tag := m.residue("nothing sensitive here, just ERROR at line 42"); tag != "" {
		t.Errorf("residue false-positived on clean prose, got %q", tag)
	}
}

// TestHomePathNoSpuriousFailClose is the regression guard for the placeholder-inertness
// fix: masking a Go-style home path must NOT error, because the fail-closed re-scan (which
// includes the home-dir detector) would otherwise match <USER_1> inside the result and mute
// exactly the stack traces this feature protects. If a detector's char class stops
// excluding '<'/'>' this fails loudly here.
func TestHomePathNoSpuriousFailClose(t *testing.T) {
	m := New()
	in := "panic at /home/maxie/go/pkg/mod/github.com/x/y@v1.0.0/z.go:10"
	out, err := m.Mask(in)
	if err != nil {
		t.Fatalf("home path spuriously failed closed: %v", err)
	}
	if !strings.Contains(out, "<USER_1>") {
		t.Errorf("username not masked: %q", out)
	}
	if strings.Contains(out, "maxie") {
		t.Errorf("username leaked: %q", out)
	}
	if got := m.Restore(out); got != in {
		t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, got)
	}
}

// TestPreTemplatedSecretsMasked covers the leak found in manual wire testing: the Template
// field reaches the anonymizer AFTER the pipeline masker has rewritten it, so a secret's
// shape is already broken — sk-livekeyabcdefghij0123456789XYZ arrives as
// sk-livekeyabcdefghij<NUM>XYZ — and a detector anchored on an unbroken [A-Za-z0-9] run
// misses it, sending the surviving characters in the clear.
//
// The inputs are produced by the REAL pipeline.Templatize rather than hand-written mangled
// forms: the coupling between the two maskers is what caused the bug, so the test pins the
// actual interaction and fails if either side's rules move.
func TestPreTemplatedSecretsMasked(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		remnant string // a distinctive fragment that must not survive
	}{
		// The exact shape found on the wire: only 10 characters survive ahead of the <NUM>,
		// so the {16,} run length is what fails, not just the charset.
		{"sk- key", "key sk-abcdefghij0123456789XYZ rejected", "abcdefghij"},
		{"aws key", "creds AKIAIOSFODNN7EXAMPLE denied", "IOSFODNN"},
		{"jwt", "auth eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r", "dBjftJeZ"},
		{"bearer jwt", "hdr Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u", "dBjftJeZ"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			templated, _ := pipeline.Templatize(c.raw)
			if !strings.Contains(templated, "<NUM>") {
				t.Fatalf("premise broken: %q was not mangled by the pipeline masker", templated)
			}
			m := New()
			out := mustMask(t, m, templated)
			if !strings.Contains(out, "<TOKEN_1>") {
				t.Errorf("pre-templated secret not masked:\n in:  %q\n out: %q", templated, out)
			}
			if strings.Contains(out, c.remnant) {
				t.Errorf("secret remnant %q survived:\n in:  %q\n out: %q", c.remnant, templated, out)
			}
			if got := m.Restore(out); got != templated {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", templated, got)
			}
		})
	}
}

// TestTrimDanglingPlaceholder: text salvaged from a stream that died can stop mid-token,
// leaving "<IP" — a mangled half of a value we hold the real version of and simply never
// reached the end of. Cutting it beats rendering it; complete text must be left alone.
func TestTrimDanglingPlaceholder(t *testing.T) {
	cases := map[string]string{
		"check the route to <IP":    "check the route to",
		"check the route to <IP_":   "check the route to",
		"check the route to <IP_1":  "check the route to",
		"the host is <":             "the host is",
		"check the route to <IP_1>": "check the route to <IP_1>", // complete: untouched
		"nothing to trim here":      "nothing to trim here",
		"a < b in ordinary prose":   "a < b in ordinary prose",
		"restored 10.0.0.5 already": "restored 10.0.0.5 already",
	}
	for in, want := range cases {
		if got := TrimDanglingPlaceholder(in); got != want {
			t.Errorf("TrimDanglingPlaceholder(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExtraHostSuffixes: a site-specific suffix passed to New extends the bare-host
// allowlist without disturbing the defaults.
func TestExtraHostSuffixes(t *testing.T) {
	m := New("acmecloud")
	out := mustMask(t, m, "node worker.acmecloud and db.internal")
	if strings.Contains(out, "worker.acmecloud") || strings.Contains(out, "db.internal") {
		t.Errorf("extra/default suffix host not masked: %q", out)
	}
}

// TestPlaceholdersAreInert: no detector's pattern matches the placeholder syntax, which is
// what makes sequential masking and the fail-closed re-scan safe. Asserted structurally so a
// new detector with a sloppy char class is caught.
func TestPlaceholdersAreInert(t *testing.T) {
	inert := []string{
		"<IP_1> <EMAIL_2> <TOKEN_3> <UUID_4> <HOST_5> <USER_6>",
		// Adversarial: our own placeholders sitting directly behind the very prefixes the
		// secret detectors were widened to anchor on. Widening them to step over PIPELINE
		// placeholders must not let them chew on ours — <TOKEN_1> has '_' where the pipeline
		// alternation demands '>', so none of these may match.
		"Bearer <TOKEN_1> AKIA<TOKEN_2> sk-<TOKEN_3> eyJ<TOKEN_4>.eyJ<TOKEN_5>.<TOKEN_6>",
		// Adversarial for the five detectors widened in #43, which — unlike the four above —
		// are NOT anchored on a distinctive secret prefix. Two independent things have to hold
		// for these: every widened class still excludes '<' and '>', so a run cannot enter a
		// placeholder; and pipelinePH demands '>' immediately after one of six untagged names,
		// so our own <TOKEN_1> cannot be consumed as a unit either.
		//
		// The email detector is the one that needs both. Its local-part class contains '_' and
		// '.', so a run could start inside "TOKEN_1" — the only thing stopping it reaching the
		// '@' is the '>' that is in neither alternative.
		"dsn=postgres://<TOKEN_1>@<HOST_2>/prod",
		"mail <EMAIL_1> then <TOKEN_1>@<HOST_2> refused",
		"loaded from /home/<USER_1>/go/pkg/mod",
		"call https://<HOST_1>/v1 and <UUID_1>",
	}
	for _, s := range inert {
		for _, d := range baseDetectors {
			if loc := d.re.FindStringSubmatchIndex(s); loc != nil && loc[2*d.group] >= 0 {
				t.Errorf("detector %s matched placeholder syntax %q", d.tag, s)
			}
		}
		// The same claim from the fail-closed side: re-scanning masked output is clean.
		if tag := New().residue(s); tag != "" {
			t.Errorf("residue() false-positived on placeholders %q, got %q", s, tag)
		}
	}
	// Belt and suspenders: the tag pattern the detectors must avoid.
	if regexp.MustCompile(`<[A-Z]+_\d+>`).FindString(inert[0]) == "" {
		t.Fatal("test placeholder string is malformed")
	}
}

// ---------------------------------------------------------------------------------------
// Completeness: a value is masked WHOLE, or it is a leak.
// ---------------------------------------------------------------------------------------

// TestIssue41CompressedIPv6 is the named regression guard for #41. ipv6Pattern was compiled
// with plain regexp.MustCompile, so Go's default leftmost-FIRST matching took alternative 2
// — (?:[0-9a-f]{1,4}:){1,7}: — which stops at the "::" and wins over the alternative that
// would have taken the whole address. Everything downstream reported success: Mask returned
// a nil error, residue() returned "", no escalation was skipped, and the card looked normal
// while the tail of the address went to the configured model endpoint as literal text.
func TestIssue41CompressedIPv6(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"tail after elision", "peer 2001:db8::dead:beef down", "peer <IP_1> down"},
		{"multi-group tail", "peer fe80::c0ff:ee00:1234 down", "peer <IP_1> down"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			if got := mustMask(t, m, c.in); got != c.want {
				t.Errorf("compressed IPv6 masked in part:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		})
	}
}

// phMark DELIMITS, inside a completeness row's prefix or suffix, a span that is ordinary
// text in the payload but must come out as a placeholder — the host of a connection string
// whose credentials are the row's secret, say. Written in pairs: "@\x00db.acme.com\x00/prod"
// sends "@db.acme.com/prod" through Mask and expects "@<HOST_n>/prod" back.
//
// It keeps the row declarative about WHERE placeholders land without pinning which tag or
// which number lands there.
const phMark = "\x00"

// phPat is the shape of every placeholder this package mints.
const phPat = `<[A-Z]+_\d+>`

// payloadOf strips the markers, leaving the text actually fed to Mask.
func payloadOf(s string) string { return strings.ReplaceAll(s, phMark, "") }

// expand turns a row's prefix or suffix into a regexp fragment: text outside the markers is
// quoted literally, each marked span becomes the placeholder shape.
func expand(s string) string {
	parts := strings.Split(s, phMark)
	for i, p := range parts {
		if i%2 == 1 { // inside a marked span
			parts[i] = phPat
			continue
		}
		parts[i] = regexp.QuoteMeta(p)
	}
	return strings.Join(parts, "")
}

// completenessRow declares INPUTS only: text around a sensitive value, and the value. What
// the output should look like is derived, never hand-written — that is the whole point. A
// table of expected outputs only catches the cases someone thought of, and #41 was precisely
// a case nobody thought of.
type completenessRow struct {
	name           string
	prefix, secret string
	suffix         string
}

// completenessRows covers every detector in baseDetectors — enforced by
// TestEveryDetectorHasACompletenessRow, so a detector added without a row goes red.
//
// The prefix and suffix are not decoration. They anchor the assertion at both ends, which is
// what lets it prove masking was COMPLETE without a "surviving fragment" threshold to
// defend: a threshold could not have caught fe80::1, whose leftover is a single character.
// They also prove the detector did not OVERREACH, which is the hazard that grows once every
// detector is compiled leftmost-longest — so several rows deliberately put text flush
// against the secret with no separator (a port, a path) rather than a comfortable space.
var completenessRows = []completenessRow{
	// #41 itself. The first three are red under leftmost-first.
	{"ipv6/compressed-tail", "peer ", "2001:db8::dead:beef", " down"},
	{"ipv6/compressed-multi", "peer ", "fe80::c0ff:ee00:1234", " down"},
	// The one-character leak, green under TestRoundTrip since v0.4.0. Keep it prominent.
	{"ipv6/loopback-short-tail", "peer ", "fe80::1", " unreachable"},
	// The control: an expanded address takes alternative 1 and was never affected.
	{"ipv6/expanded", "peer ", "2001:db8:0:0:0:0:2:1", " down"},
	// Greed guard: the address must stop at "]", not swallow the port.
	{"ipv6/bracketed-port", "peer [", "2001:db8::dead:beef", "]:443 refused"},
	// Greed guard: IPv4's trailing \b and char class must stop before ":".
	{"ipv4/with-port", "connect ", "10.0.0.5", ":8080 refused"},
	{"email", "mail to ", "bob@team.example.org", " bounced"},
	{"uuid", "request ", "550e8400-e29b-41d4-a716-446655440000", " timed out"},
	{"jwt", "auth ", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u", " rejected"},
	{"aws-key", "creds ", "AKIAIOSFODNN7EXAMPLE", " denied"},
	{"bearer", "hdr Bearer ", "abc123DEF456ghi789JKL", " expired"},
	{"sk-key", "key ", "sk-abcdefghij0123456789XYZ", " rejected"},
	// The credential detector masks a SUBMATCH: its whole match spans "://svcuser:hunter2@",
	// but its target group is exactly the credentials, which is what the row declares. The
	// phMark is the <HOST_n> the URL-host detector mints for the host that follows.
	{"url-credentials", "dsn=postgres://", "svcuser:hunter2", "@" + phMark + "db.acme.com" + phMark + "/prod"},
	// Greed guard: the host must stop at "/", not eat the path.
	{"url-host", "call https://", "internal.acme.com", "/v1"},
	{"bare-host", "host ", "worker.svc", " failed"},
	{"home-dir", "loaded from /home/", "maxie", "/go/pkg/mod"},
}

// TestMaskingIsComplete is the property TestRoundTrip cannot express: the sensitive value is
// replaced ENTIRELY by one placeholder, and the text around it is returned untouched.
//
// It is stated as a property rather than a table of outputs so it holds for detectors nobody
// has examined — a partial match in any of them fails here without anyone having thought of
// that particular shape first.
func TestMaskingIsComplete(t *testing.T) {
	for _, r := range completenessRows {
		t.Run(r.name, func(t *testing.T) {
			payload := payloadOf(r.prefix) + r.secret + payloadOf(r.suffix)
			m := New()
			masked := mustMask(t, m, payload)

			want := regexp.MustCompile(`^` + expand(r.prefix) + phPat + expand(r.suffix) + `$`)
			if !want.MatchString(masked) {
				t.Errorf("value not masked whole, or surrounding text disturbed:\n in:   %q\n got:  %q\n want: %v",
					payload, masked, want)
			}
			if strings.Contains(masked, r.secret) {
				t.Errorf("the secret survived verbatim: %q still in %q", r.secret, masked)
			}
			if got := m.Restore(masked); got != payload {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", payload, got)
			}
		})
	}
}

// TestEveryDetectorHasACompletenessRow is what makes the property above a PROPERTY rather
// than a fact about the rows someone happened to write. For every detector, some row's
// target group must land EXACTLY on that row's declared secret — so a new detector without a
// row turns this red, and a detector whose group lands only PARTLY on a secret is caught by
// TestMaskingIsComplete instead.
//
// The span is checked on the target group, not on match 0: three detectors deliberately mask
// a submatch and leave their surrounding context — the scheme, the word "Bearer", the rest
// of the path — for a later detector or for the reader.
func TestEveryDetectorHasACompletenessRow(t *testing.T) {
	for i, d := range baseDetectors {
		if _, ok := completenessSecretFor(d); !ok {
			t.Errorf("detector %d (%s, group %d) has no completeness row; add one to completenessRows.\n pattern: %s",
				i, d.tag, d.group, d.re.String())
		}
	}
}

// completenessSecretFor returns the secret from d's CANONICAL completeness row: the first row
// whose target group lands exactly on the declared secret. That row is the one that speaks for
// the detector, and TestEveryUncollapsedDetectorHasATemplatizedRow asks it what this detector's
// value looks like before deciding whether the pipeline masker collapses it.
func completenessSecretFor(d detector) (string, bool) {
	for _, r := range completenessRows {
		prefix := payloadOf(r.prefix)
		payload := prefix + r.secret + payloadOf(r.suffix)
		start, end := len(prefix), len(prefix)+len(r.secret)
		for _, loc := range d.re.FindAllStringSubmatchIndex(payload, -1) {
			if loc[2*d.group] == start && loc[2*d.group+1] == end {
				return r.secret, true
			}
		}
	}
	return "", false
}

// TestAcceptedOverMasking pins the cost of compiling every detector leftmost-longest, in the
// one direction the completeness rows cannot express: text that legitimately reads as part
// of an address gets absorbed into the placeholder.
//
// These are expected OUTPUTS on purpose. They are behaviour claims, not properties, and the
// point of writing them down is that widening the mask further shows up as a diff here
// rather than passing silently.
//
// Both are the cheap direction. This package's asymmetry is the reverse of the pipeline's
// (see the package comment): matching too much costs one placeholder in a restatement that
// is already lossy, while matching too little is disclosure. The second row is the shape #33
// fixed in the pipeline; this package has always over-masked it and now over-masks it by one
// more character. Whether the two IPv6 patterns should be one is #42's question, not this
// change's.
func TestAcceptedOverMasking(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Unbracketed, ":443" IS a valid final hex group, so the port is absorbed. Bracketed
		// (see the ipv6/bracketed-port completeness row) it is not. RFC 3986 requires the
		// brackets for exactly this ambiguity.
		{"ipv6 port absorbed when unbracketed",
			"peer 2001:db8::dead:beef:443 refused", "peer <IP_1> refused"},
		// A C++ scope operator between two hex-ish identifiers reads as an address. #33's
		// boundary and two-group rules would reject it, but porting them here would drop
		// ::1, fe80::/64 and glued addresses — fragmentation there, a leak here.
		{"c++ scope operator", "CNSSCertStore::CNSSCertStore", "CNSSCertStor<IP_1>NSSCertStore"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			if got := mustMask(t, m, c.in); got != c.want {
				t.Errorf("accepted over-masking moved:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// The composition: pipeline.Templatize, THEN Mask.
//
// Everything above this line feeds the anonymizer raw log text. One field of a real request
// does not arrive that way: ExplainRequest.Template has already been through the pipeline's
// template masker (internal/llm/pool.go builds it from score.EscalationRequest.Pattern), so a
// value reaches these detectors shape-broken — hunter2 as hunter<NUM>, db01 as db<NUM>.
//
// The rule, which is the thing worth remembering: THE GAPS ARE EXACTLY THE DETECTORS WHOSE
// VALUES THE PIPELINE DOES NOT ALREADY COLLAPSE. UUIDs and IP addresses arrive as <UUID> and
// <IP> — one placeholder, nothing left to recognize, no gap possible. Every other detector
// receives a damaged but not collapsed value and has to read the pipeline's handwriting.
//
// That is a fact about today's templatizer, not a property of anything, which is why it is
// pinned by tests that run the REAL Templatize rather than by hand-written mangled strings. A
// test that hand-writes what it thinks the templatizer produces can agree with itself forever
// while the two components drift apart — which is how detectors 4, 5, 9a, 9b and 10 were left
// out when the other four were widened at 2da31be.
// ---------------------------------------------------------------------------------------

// templatizedRow is one raw log line, exactly as a log would carry it, with phMark delimiting
// each span that must come back as a single placeholder. The row declares INPUT only: what the
// templatizer does to it, and therefore what Mask must cope with, is derived at run time.
type templatizedRow struct {
	name string
	text string
}

// templatizedRows covers every detector the pipeline does not collapse — enforced by
// TestEveryUncollapsedDetectorHasATemplatizedRow.
//
// Every row carries a MIDDLE digit, because that is the awkward position and the one an
// example chosen to pass would avoid: a digit at the END of a value is harmless (/home/maxie7
// masks to /home/<USER_1><NUM> and loses nothing), while a digit in the MIDDLE splits the value
// and everything past the split survives (/home/deploy2prod leaked "prod"). Suffixes are kept
// flush against the value — a port, a path — so the rows also prove the widened detectors did
// not over-reach, which is the hazard leftmost-longest creates on every detector since #41.
var templatizedRows = []templatizedRow{
	// The wire capture that opened #43, both fields of it. The credential detector masks a
	// submatch; the host that follows is the URL-host detector's, hence two marked spans.
	{"url-credentials/middle-digit",
		"FATAL db postgres://\x00svc7user:hun2ter\x00@\x00db01.corp.internal\x00/prod lost"},
	// The control: no digit in the credential at all, so only the port templatizes.
	{"url-credentials/no-digit",
		"dsn=postgres://\x00u:p\x00@\x00db.acme.com\x00:5432/prod"},
	{"email/digit-in-local", "mail to \x00user01@corp.example.com\x00 bounced"},
	{"email/digit-in-domain", "mail to \x00bob@db01.example.com\x00 bounced"},
	{"email/digit-mid-token-both-sides", "mail to \x00us3r@corp7.example.com\x00 bounced"},
	{"url-host/middle-digit", "call https://\x00api-gw7x.prod.acme.com\x00/v1"},
	{"bare-host/middle-digit", "host \x00db01x.corp.internal\x00 failed"},
	{"bare-host/digit-in-middle-label", "host \x00db.eu1a.corp.internal\x00 failed"},
	{"home-dir/middle-digit", "loaded from /home/\x00deploy2prod\x00/go/pkg/mod"},
	{"home-dir/middle-digit-users", "path /Users/\x00jane7doe\x00/app.log missing"},
	// The four widened at 2da31be, now held to completeness rather than to "a token appeared".
	{"jwt", "auth \x00eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u\x00 rejected"},
	{"aws-key", "creds \x00AKIAIOSFODNN7EXAMPLE\x00 denied"},
	{"bearer-jwt", "hdr Bearer \x00eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u\x00 expired"},
	{"sk-key", "key \x00sk-abcdefghij0123456789XYZ\x00 rejected"},
}

// TestTemplatizedMaskingIsComplete is TestMaskingIsComplete run over the real composition: the
// row is templatized first, and the value must STILL come back as exactly one placeholder with
// the text around it untouched.
//
// The expectation is derived, never hand-written. The marks ride through Templatize with the
// text, so wherever the templatized secret ends up, the marks still delimit it — and two premise
// checks refuse to proceed if that is not true, rather than quietly asserting the wrong span.
func TestTemplatizedMaskingIsComplete(t *testing.T) {
	for _, r := range templatizedRows {
		t.Run(r.name, func(t *testing.T) {
			payload := payloadOf(r.text)
			tMarked, _ := pipeline.Templatize(r.text)
			tPayload, _ := pipeline.Templatize(payload)

			if got, want := strings.Count(tMarked, phMark), strings.Count(r.text, phMark); got != want {
				t.Fatalf("premise broken: templating changed the mark count, %d → %d:\n in:  %q\n out: %q",
					want, got, r.text, tMarked)
			}
			if payloadOf(tMarked) != tPayload {
				t.Fatalf("premise broken: the marks perturbed templating:\n marked:   %q\n unmarked: %q",
					payloadOf(tMarked), tPayload)
			}
			if tPayload == payload {
				t.Fatalf("premise broken: %q was not mangled by the pipeline masker", payload)
			}

			m := New()
			masked := mustMask(t, m, tPayload)

			want := regexp.MustCompile(`^` + expand(tMarked) + `$`)
			if !want.MatchString(masked) {
				t.Errorf("templatized value not masked whole, or surrounding text disturbed:\n raw:  %q\n in:   %q\n got:  %q\n want: %v",
					payload, tPayload, masked, want)
			}
			if got := m.Restore(masked); got != tPayload {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", tPayload, got)
			}
		})
	}
}

// lonePipelinePlaceholder matches a value the pipeline masker collapsed ENTIRELY — the whole
// string is one of its six placeholders and nothing of the original survives.
var lonePipelinePlaceholder = regexp.MustCompile(`^` + pipelinePH + `$`)

// TestEveryUncollapsedDetectorHasATemplatizedRow is the rule, mechanized. It is deliberately
// not a comment: #36 is open and will change what the templatizer collapses, and when it does,
// a detector that is safe today becomes vulnerable without anyone touching this package.
//
// For each detector it asks the REAL templatizer what that detector's canonical secret looks
// like by the time the anonymizer sees it:
//
//   - collapsed to a single pipeline placeholder → the anonymizer never sees a value at all,
//     and no templatized row is required (UUID, IPv4, IPv6 today);
//   - anything else → some templatized row must exist whose target group lands exactly on the
//     templatized secret, or this fails naming the detector.
//
// The partition is DERIVED from the two real components rather than kept in a list here, so it
// cannot rot: change the templatizer and the exemptions change with it, red, in this test.
//
// One honesty note about that exemption. It is per canonical row, and the IPv6 shapes the
// pipeline deliberately does NOT collapse (fe80::1 templates as fe<NUM>::<NUM>, README "Known
// limitations", #40) are not covered by the IPv6 row's exemption. They lose their digits to
// <NUM> before this package ever sees them, so what survives is structure rather than value.
// Whether the two IPv6 patterns should be one is #42's question.
func TestEveryUncollapsedDetectorHasATemplatizedRow(t *testing.T) {
	// The templatized marked spans of every row, as (payload, start, end) triples.
	//
	// Only spans the templatizer actually DAMAGED are collected. A row whose value contains no
	// digit passes through Templatize unchanged, and a detector matching that span would prove
	// nothing about reading the pipeline's handwriting — which is the entire question here. The
	// no-digit rows stay in templatizedRows as controls; they just do not count as coverage.
	type span struct {
		text       string
		start, end int
	}
	var spans []span
	for _, r := range templatizedRows {
		tMarked, _ := pipeline.Templatize(r.text)
		raw, templated := strings.Split(r.text, phMark), strings.Split(tMarked, phMark)
		if len(raw) != len(templated) {
			continue // TestTemplatizedMaskingIsComplete reports the broken premise
		}
		payload, off := payloadOf(tMarked), 0
		for i, p := range templated {
			if i%2 == 1 && p != raw[i] {
				spans = append(spans, span{payload, off, off + len(p)})
			}
			off += len(p)
		}
	}

	for i, d := range baseDetectors {
		secret, ok := completenessSecretFor(d)
		if !ok {
			continue // TestEveryDetectorHasACompletenessRow already reports this
		}
		templated, _ := pipeline.Templatize(secret)
		if lonePipelinePlaceholder.MatchString(templated) {
			continue // the pipeline collapses this class; the anonymizer never sees the value
		}
		covered := false
		for _, s := range spans {
			for _, loc := range d.re.FindAllStringSubmatchIndex(s.text, -1) {
				if loc[2*d.group] == s.start && loc[2*d.group+1] == s.end {
					covered = true
					break
				}
			}
			if covered {
				break
			}
		}
		if !covered {
			t.Errorf("detector %d (%s, group %d) reads a value the pipeline does NOT collapse (%q → %q),\n"+
				"so it must recognize the templatized form and have a row in templatizedRows.\n pattern: %s",
				i, d.tag, d.group, secret, templated, d.re.String())
		}
	}
}

// TestNoSpuriousFailCloseAfterTemplating is TestHomePathNoSpuriousFailClose run through the
// composition. Detectors 4, 5 and 10 are verify-eligible, so a widened pattern that matched its
// own output would not leak — it would MUTE the escalation, which is the opposite failure and
// just as bad for a stack trace that is the whole product.
func TestNoSpuriousFailCloseAfterTemplating(t *testing.T) {
	in, _ := pipeline.Templatize("panic at /home/maxie7/go/pkg/mod/github.com/x/y@v1.0.0/z.go:10")
	m := New()
	out, err := m.Mask(in)
	if err != nil {
		t.Fatalf("templatized home path spuriously failed closed: %v\n in: %q", err, in)
	}
	if !strings.Contains(out, "<USER_1>") {
		t.Errorf("username not masked: %q", out)
	}
	if strings.Contains(out, "maxie") {
		t.Errorf("username leaked: %q", out)
	}
	if got := m.Restore(out); got != in {
		t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, got)
	}
}

// TestNoOverMaskingAfterTemplating is TestNoOverMasking's counter-weight for the composition:
// ordinary log prose that has been through the template masker must survive Mask untouched too.
// Widening a detector to step over <NUM> is exactly the change that could start eating it.
func TestNoOverMaskingAfterTemplating(t *testing.T) {
	survive := []string{
		"java.lang.NullPointerException",
		"at com.foo.Bar(Bar.java:42)",
		"github.com/docker/docker/client.(*Client).ContainerList",
		"golang.org/x/sync/errgroup",
		"go.uber.org/zap",
		"app version v1.2.3 started",
		"processed 4200 records in 12.5s",
		"GET /orders 200 in 42ms",
		"could not reach api.example.com", // bare PUBLIC host: accepted residual gap
	}
	for _, s := range survive {
		in, _ := pipeline.Templatize(s)
		m := New()
		if got := mustMask(t, m, in); got != in {
			t.Errorf("over-masked templatized prose:\n raw: %q\n in:  %q\n out: %q", s, in, got)
		}
	}
}
