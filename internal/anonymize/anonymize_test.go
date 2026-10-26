// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/model"
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
		// A UUID used as a hostname LABEL is part of the host, so it comes back <HOST_n> rather
		// than <UUID_n>. That is a deliberate change of what the model is told it is reasoning
		// about, and it is the cheap half of the trade: before #48 this input produced <UUID_1>
		// AND ".blob.core.windows.net" in the clear, so the tag was never the thing being
		// preserved -- the choice is "<UUID_n> plus a leaked domain" against "<HOST_n> and
		// nothing leaked". The converse, that a UUID which is NOT part of a host keeps its own
		// tag, is TestUUIDOutsideAHostKeepsItsOwnTag and is not optional: detector 6 now runs
		// after both host detectors, so every other detector gets first refusal on a UUID.
		{"call https://550e8400-e29b-41d4-a716-446655440000.blob.core.windows.net/x", "<HOST_1>"},
		// The half of a credential detector 4 can no longer reach is a TOKEN, not an EMAIL. Before
		// #46 this input masked to "<TOKEN_1>:<EMAIL_1>": the email detector swallowing
		// "hunter2@db.acme.com" was the only thing keeping the password off the wire, and it did so
		// under the wrong tag and glued to the host.
		{"dsn=postgres://sk-livekeyabcdefghij0123456789XYZ:hunter2@db.acme.com/prod", "<TOKEN_2>"},
		// #55: a userinfo with no password is a credential, and the tag is the point. Before the
		// fix this input came back with "appuser" in the clear beside <HOST_1>; against a dotted
		// host it came back <EMAIL_1>, which is the wrong tag for a username and glued it to the
		// host besides. Contains cannot see a tag that is right for the wrong span, so the exact
		// output is pinned in TestIssue55PasswordlessUserinfo and this row states the type claim.
		{"dsn=postgres://appuser@db:5432/app", "<TOKEN_1>"},
		// #58: a raw '@' inside the password is part of the credential, and the credential is ONE
		// value up to the LAST '@'. Before the fix this came back <TOKEN_1>@<HOST_1>@db -- the
		// "ss" behind the first '@' tagged as a host and the real host in the clear. Contains
		// cannot tell those apart, so the exact output is pinned in TestIssue58RawAtInUserinfo.
		{"dsn=postgres://user:p@ss@db", "<TOKEN_1>"},
		// #57: an access key that is a LABEL of a hostname is part of the host, as #48's UUID is, so
		// the name comes back <HOST_n>. Before the fix it came back <TOKEN_1> with the domain beside
		// it in the clear; the exact output is pinned in TestIssue57PartlyRecognisedValue.
		{"call https://AKIAIOSFODNN7EXAMPLE.prod.acme.com/x", "<HOST_1>"},
		// #108: a key glued to a word character in prose is still a key. Before the fix this came back
		// unchanged, the key in the clear; the exact output is pinned in TestIssue108KeyGluedToAWordCharacter.
		{"key AKIAIOSFODNN7EXAMPLE_prod used", "<TOKEN_1>"},
		// #51: an sk- key with a '-' or a '_' in its tail is a key. Before the fix this came back unchanged,
		// the key in the clear; the exact output is pinned in TestIssue51SkKeyWithASeparatorInItsTail.
		{"key " + skProjKey + " rejected", "<TOKEN_1>"},
		// #114: a Stripe, GitHub, Slack or Google secret is a key. Before the fix these came back unchanged,
		// the key in the clear; the exact outputs are pinned in TestIssue114VendorSecretFormats.
		{"key " + stripeLiveSecret + " rejected", "<TOKEN_1>"},
		{"key " + githubClassicSecret + " rejected", "<TOKEN_1>"},
		{"key " + githubPATSecret + " rejected", "<TOKEN_1>"},
		{"key " + slackBotSecret + " rejected", "<TOKEN_1>"},
		{"key " + googleAPISecret + " rejected", "<TOKEN_1>"},
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
		// #51: an sk- key's alphabet is a kebab-case name's, so these stay by the witness, by \b, or by
		// the floor (TestIssue51SkKeyWithASeparatorInItsTail).
		"sk-learn-compatible-estimator",
		"sk-worker-pool-7f9c",
		"task-AbCdEfGhIjKlMnOpQr",
		"Accept-Language: sk-SK,sk;q=0.9",
		"sk-1 failed",
		"region sk-2",
		// #114: a word that shares a vendor prefix stays by the floor, the witness or the lead
		// (TestIssue114VendorSecretFormats).
		"sk" + "_test_helper",
		"gh" + "p_cache_dir",
		"github" + "_pat_rotation_policy_v2_enabled",
		"the xox" + "b- prefix",
		"xox" + "b-2024-rollout-plan",
		"AI" + "za_test_1",
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

// withTokens builds a Mapper whose mapping is set DIRECTLY, for the shapes Mask cannot
// produce — a nested or cyclic mapping — which are exactly the ones Restore must not leave to
// chance (#50).
func withTokens(pairs ...string) *Mapper {
	m := New()
	for i := 0; i+1 < len(pairs); i += 2 {
		m.byToken[pairs[i]] = pairs[i+1]
	}
	return m
}

// TestRestoreIsOrderIndependent is #50's guard: a nested mapping restored many times, with a
// FRESH Mapper each iteration, so Go's map-order randomisation is exercised rather than sampled
// once. The rate a single-pass-per-token Restore gets this wrong is set by the keys' hashes, not a
// coin flip, so the count is reported rather than stopping at the first failure.
func TestRestoreIsOrderIndependent(t *testing.T) {
	const runs = 1000
	cases := []struct {
		name  string
		pairs []string
		in    string
		want  string
	}{
		{
			name:  "nested pair",
			pairs: []string{"<TOKEN_1>", "sk-livekeyabcdefghij0123456789XYZ", "<TOKEN_2>", "<TOKEN_1>:hunter2"},
			in:    "credential <TOKEN_2> rejected",
			want:  "credential sk-livekeyabcdefghij0123456789XYZ:hunter2 rejected",
		},
		{
			// A substituted value must never combine with the text beside it into a new token:
			// "<IP" + "_1>" is the ORIGINAL text "<IP_1>", not a reference to IP_1.
			name:  "replacement combines with neighbouring text",
			pairs: []string{"<TOKEN_1>", "<IP", "<IP_1>", "10.0.0.5"},
			in:    "<TOKEN_1>_1>",
			want:  "<IP_1>",
		},
	}
	for _, c := range cases {
		wrong, first := 0, ""
		for i := 0; i < runs; i++ {
			if got := withTokens(c.pairs...).Restore(c.in); got != c.want {
				if wrong == 0 {
					first = got
				}
				wrong++
			}
		}
		if wrong > 0 {
			t.Errorf("%s: Restore wrong in %d of %d runs; first bad result %q, want %q",
				c.name, wrong, runs, first, c.want)
		}
	}
}

// TestRestoreResolvesByRule pins what Restore does with every shape a mapping can take, nested or
// not. Each row's answer is independent of map order.
func TestRestoreResolvesByRule(t *testing.T) {
	cases := []struct {
		name  string
		pairs []string
		in    string
		want  string
	}{
		{"two-level nesting",
			[]string{"<TOKEN_1>", "s3cr3t", "<TOKEN_2>", "<TOKEN_1>:hunter2"},
			"dsn <TOKEN_2>", "dsn s3cr3t:hunter2"},
		{"three-level nesting",
			[]string{"<HOST_1>", "db", "<TOKEN_1>", "u:<HOST_1>", "<TOKEN_2>", "[<TOKEN_1>]"},
			"<TOKEN_2>!", "[u:db]!"},
		{"unknown token at top level stays verbatim",
			[]string{"<IP_1>", "10.0.0.5"},
			"<IP_1> and <IP_9>", "10.0.0.5 and <IP_9>"},
		{"unknown token inside a value stays verbatim",
			[]string{"<TOKEN_1>", "a<IP_9>b"},
			"<TOKEN_1>", "a<IP_9>b"},
		{"repeated token",
			[]string{"<IP_1>", "10.0.0.5"},
			"<IP_1> -> <IP_1>", "10.0.0.5 -> 10.0.0.5"},
		{"token glued to text",
			[]string{"<IP_1>", "10.0.0.5"},
			"x<IP_1>y", "x10.0.0.5y"},
		// The expansion path is a STACK: a token leaves it when its expansion returns. A
		// visited-set that only grows would read the second visit below as a cycle. The repeated
		// token must itself NEST, because a leaf value is returned before it is ever pushed.
		{"known token twice in one value",
			[]string{"<TOKEN_1>", "<TOKEN_2>:<TOKEN_2>", "<TOKEN_2>", "k<TOKEN_3>", "<TOKEN_3>", "v"},
			"<TOKEN_1>", "kv:kv"},
		{"diamond",
			[]string{"<TOKEN_1>", "<TOKEN_2><TOKEN_3>", "<TOKEN_2>", "x<TOKEN_4>",
				"<TOKEN_3>", "y<TOKEN_4>", "<TOKEN_4>", "z<TOKEN_5>", "<TOKEN_5>", "w"},
			"<TOKEN_1>", "xzwyzw"},
		// A cyclic mapping has no value, so the outermost token stays verbatim rather than
		// becoming a partial unrolling presented as real; an unrelated token still restores.
		{"two-token cycle",
			[]string{"<TOKEN_1>", "a<TOKEN_2>", "<TOKEN_2>", "b<TOKEN_1>", "<IP_1>", "10.0.0.5"},
			"<TOKEN_1> and <IP_1>", "<TOKEN_1> and 10.0.0.5"},
		{"self-cycle",
			[]string{"<TOKEN_1>", "x<TOKEN_1>"},
			"see <TOKEN_1>", "see <TOKEN_1>"},
		{"token whose value reaches a cycle",
			[]string{"<TOKEN_1>", "a<TOKEN_2>", "<TOKEN_2>", "b<TOKEN_1>", "<TOKEN_3>", "c<TOKEN_1>"},
			"<TOKEN_3>", "<TOKEN_3>"},
	}
	for _, c := range cases {
		if got := withTokens(c.pairs...).Restore(c.in); got != c.want {
			t.Errorf("%s: Restore(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestEveryTagMintsARestorableToken: Restore finds tokens by ourPH, so a token that does not match
// it is never restored and reaches the card as a literal placeholder. The tags are read from the
// real detector tables — apply is placeholder's only caller and it passes d.tag — so a future tag
// such as "IPV6", whose digit fails [A-Z]+, turns this red instead of being absent from a list.
func TestEveryTagMintsARestorableToken(t *testing.T) {
	whole := regexp.MustCompile(`^(?:` + ourPH + `)$`)
	for _, dets := range [][]detector{baseDetectors, buildDetectors([]string{"example"})} {
		for _, d := range dets {
			m := New()
			tok := m.placeholder(d.tag, "value")
			if !whole.MatchString(tok) {
				t.Errorf("tag %q mints %q, which ourPH does not match: Restore would never restore it", d.tag, tok)
			}
			if got := m.Restore("x " + tok + " y"); got != "x value y" {
				t.Errorf("tag %q: Restore(%q) = %q, want %q", d.tag, tok, got, "x value y")
			}
		}
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
		// Adversarial for the one-sided credential fallbacks (4b/4c) added for #46. Those are the
		// only detectors that read one of our own tags ON PURPOSE, so they are where "the pattern
		// must not match placeholder syntax" stops being the right claim. What has to hold is
		// narrower, and is what apply() and residue() actually rely on: the TARGET GROUP must never
		// land on text this package minted. Each string below is a fallback's own output fed back to
		// it, and in every one it is the group class — '<' and '>' excluded — that refuses.
		"dsn=postgres://<TOKEN_1>:<TOKEN_2>@<HOST_3>/prod",
		"dsn=redis://:<TOKEN_1>@<HOST_2>:6379/0",
		"dsn=postgres://<TOKEN_1>:@<HOST_2>/prod",
		"mail <EMAIL_1> then <TOKEN_1>@<HOST_2> refused",
		// #58's OLD output fed back: the truncated mask left a stray '@' between two of our tags.
		// Detector 4 now admits '@' and reads to the last one, so this is the string that would
		// show a group stepping over a tag to reach it. The group class still excludes '<', so the
		// run cannot start on <TOKEN_1> and there is nothing before it to start on.
		"dsn=postgres://<TOKEN_1>@<HOST_2>@<HOST_3>/prod",
		// #61's output fed back: 9c reads every earlier member's tag as context on purpose, so this
		// is the string that would show its group landing on a later one. The group class excludes
		// '<', so it cannot start on <HOST_2>, and nothing raw follows a comma to start on.
		"dsn=mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017/db?replicaSet=rs0",
		// #67's output fed back: 9c re-applies itself until nothing changes, so its own output, with
		// tags from 7, 8 and 2 among its members, is exactly what its last pass reads.
		"dsn=mongodb://<HOST_1>:27017,<IP_1>:27017,<HOST_2>:27017,<HOST_3>:27017/db",
		"dsn=mongodb://<HOST_1>,[<IP_1>]:27017,<TOKEN_1>,<HOST_2>/db",
		"loaded from /home/<USER_1>/go/pkg/mod",
		"call https://<HOST_1>/v1 and <UUID_1>",
		// #66's OLD output fed back: a '_' is a host character now, and these put one directly
		// behind our own tag, where a group that could start on the tag would show.
		"call http://<HOST_1>_host:8080/x",
		"peer db_primary.<HOST_1> down",
		// #54's OLD output fed back: our own <IP_1> glued to hostname labels, which is exactly what
		// the IPv4-label rule must NOT read as an address -- its address unit is a raw IPv4 or the
		// pipeline's untagged <IP>, never ours. And its new output, a fused member in a list.
		"resolver miss for https://<IP_1>.nip.io/x",
		"peer worker-<IP_1>.<HOST_1> unreachable",
		"peer db.<IP_1>.<HOST_1> down",
		"dsn=mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db",
		// #52's new outputs fed back: the scheme anchor admits a pipeline placeholder inside the
		// scheme and anything before it, and the group behind it must still refuse our own tags.
		"see x_https://<HOST_1>/x",
		"url=s<NUM>://<TOKEN_1>:<TOKEN_2>@<HOST_1>",
		"mongodb://<HOST_1>,<HOST_3>_x://<HOST_2>",
		// #105's new outputs fed back, under the rewritten configured suffixes below: a placeholder
		// now ends a suffix form, and the host tag beside a pipeline placeholder must stay inert.
		"peer <HOST_1> and <HOST_2> down",
		"<HOST_1>:<NUM> refused",
		"<HOST_1><NUM> up",
		"reach <HOST_1>. retrying",
		`time=<STR> level=error msg="peer <HOST_1> down"`,
		// #57's OLD outputs fed back: our own <TOKEN_1> glued to the remainder main sent, in a userinfo,
		// a list member and a host label. The glued-key rules read a RAW key only, never our tag. And
		// the new outputs, among them the two costs, a key after a comma and in an email domain.
		"s3://<TOKEN_1>.prod@<HOST_1>",
		"s3://<TOKEN_1>.prod:<TOKEN_2>@<HOST_1>",
		"mongodb://<HOST_1>,<TOKEN_1>.prod,<HOST_2>/db",
		"https://<TOKEN_1>.prod.acme.com/x",
		"s3://<TOKEN_1>@<HOST_1>",
		"postgres://<HOST_2>@<HOST_1>/db",
		"keys=a,<HOST_1>",
		"mail ops@<HOST_1>",
		// #108's new outputs fed back: our own <TOKEN_1> with the glue a key was masked out of, on either
		// side, and two tags joined by it. The glued-key rules read a raw key after a character outside
		// its alphabet, so a '_' or a letter beside our tag must give them nothing to start on.
		"key <TOKEN_1>_prod used",
		"key x_<TOKEN_1> used",
		"key id<TOKEN_1> used",
		"auth token_<TOKEN_1> rejected",
		"creds=prod_<TOKEN_1>",
		"<TOKEN_1>_<TOKEN_2>",
		"southeast<TOKEN_1>",
		// #110's OLD outputs fed back: our own <TOKEN_1> behind "Bearer" with the remainder main sent, and
		// the prefix 3a took as a token of its own. The bearer rule reads a RAW key inside the value, so a
		// value that starts on our tag, or carries one, gives it nothing to match. And the new output.
		"hdr Bearer <TOKEN_1>.prod expired",
		"hdr Bearer <TOKEN_1>_prod expired",
		"hdr Bearer <TOKEN_1>~prod expired",
		"hdr Bearer <TOKEN_2><TOKEN_1>.x expired",
		"hdr Bearer <TOKEN_1> expired",
		// #51's new outputs fed back: our own tags beside the sk prefixes and in the cells that moved. The sk
		// key reads a pipeline placeholder as evidence, never ours, so "sk-proj-", "sk-" and "sk<NUM>" before
		// our tag give it nothing to match; and the residuals' leftovers read as clean.
		"key sk-proj-<TOKEN_1> used",
		"key sk<NUM><TOKEN_1> used",
		"OPENAI_API_KEY=<TOKEN_1>",
		"postgres://<TOKEN_1>:<TOKEN_2>@<HOST_1>/db",
		"peer worker-<TOKEN_1>.<HOST_1> down",
		"/home/<TOKEN_1>/x",
		// #114's new outputs fed back: our own tag behind each vendor prefix, raw and as the template leaves
		// it, and the outputs themselves. A vendor body reads a pipeline placeholder as evidence, never ours,
		// so a prefix before our tag gives rule 12 nothing to match.
		"key sk" + "_live_<TOKEN_1> used",
		"key wh" + "sec_<TOKEN_1> used",
		"key gh" + "p_<TOKEN_1> used",
		"key github" + "_pat_<TOKEN_1> used",
		"key xox" + "b-<TOKEN_1> used",
		"key xox" + "b<NUM><TOKEN_1> used",
		"key xox" + "b<NUM><NUM>-<TOKEN_1> used",
		"key AI" + "za<TOKEN_1> used",
		"key <TOKEN_1>_prod used",
		"GOOGLE_API_KEY=<TOKEN_1>",
		"https://<HOST_1>/maps/api/geocode/json?address=x&key=<TOKEN_1>",
	}
	withSuffixes := buildDetectors([]string{"k8s", "acme1", "eu-1", "v1.2", "deadbeef", "2corp"})
	for _, s := range inert {
		for _, dets := range [][]detector{baseDetectors, withSuffixes} {
			for _, d := range dets {
				if loc := d.re.FindStringSubmatchIndex(s); loc != nil && loc[2*d.group] >= 0 {
					t.Errorf("detector %s matched placeholder syntax %q", d.tag, s)
				}
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

// gluedRunRightBoundaryRows are lines on which rule 7a (#134) must NOT fire, with their exact
// output, which is v0.12.23's. In each, what follows the groups after a ':::' could continue a value
// a later rule reads, so 7a stops short and 7 and the rest run as before. TestIssue134RightBoundary
// asserts both the string and that it equals the v0.12.23 chain's.
var gluedRunRightBoundaryRows = []struct {
	name, in, want string
}{
	// A key, a URL and a field value glued straight after the run: their first characters are hex,
	// and taking them would leave a prefix no later rule recognises.
	{"a key glued after the run", "key:::" + googleAPISecret + " used", "key<IP_1>:<TOKEN_1> used"},
	{"a hex-only scheme glued after the run", "peer:::ab://h.example/x", "peer<IP_1>:ab://<HOST_1>/x"},
	{"a hex-led scheme glued after the run", "peer:::ftp://h.example/x", "peer<IP_1>:ftp://<HOST_1>/x"},
	{"':::' as a field separator", "ts:::INFO:::msg", "ts<IP_1>:INFO<IP_1>:msg"},
	{"an S3 ARN", "arn:aws:s3:::backup-bucket/key", "arn:aws:s<IP_1>:backup-bucket/key"},
	// A dotted name or number after the run: '.' continues an IPv4 address or a host.
	{"a public name after the run", "peer:::1.example.com down", "peer<IP_1>:1.example.com down"},
	{"a private name after the run", "peer:::1.db.corp.internal down", "peer<IP_1>:<HOST_1> down"},
	{"a version after the run", "peer:::1.2.3 down", "peer<IP_1>:1.2.3 down"},
	// KNOWN_GAP #137: the rest of #134's class. Each neighbour can continue a value a later
	// rule reads, so 7a does not fire, and 7 sends the groups as it always did. Pinned, leak
	// included, so closing them shows up here as a diff.
	{"KNOWN_GAP #137 a hyphen after the groups", "peer:::1-x", "peer<IP_1>:1-x"},
	{"KNOWN_GAP #137 an underscore after the groups", "peer:::1_x", "peer<IP_1>:1_x"},
	{"KNOWN_GAP #137 a letter after the groups", "peer:::1x", "peer<IP_1>:1x"},
	{"KNOWN_GAP #137 a dot inside a word after the groups", "peer:::1.x", "peer<IP_1>:1.x"},
}

// TestIssue134RightBoundary is the other half of #134's guard: where 7a must not fire, the output
// is v0.12.23's, byte for byte.
func TestIssue134RightBoundary(t *testing.T) {
	ref := referenceDetectors(t, baseDetectors, refV01223)
	for _, c := range gluedRunRightBoundaryRows {
		t.Run(c.name, func(t *testing.T) {
			got := mustMask(t, New(), c.in)
			if got != c.want {
				t.Errorf("right boundary moved:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
			if old := mustMask(t, mapperOn(ref), c.in); got != old {
				t.Errorf("differs from v0.12.23:\n in:   %q\n got:  %q\n then: %q", c.in, got, old)
			}
		})
	}
}

// dottedQuadRightBoundaryRows are lines on which neither rule 7b nor 7a's dotted-quad alternative
// (#49) may fire, with their exact output, which is v0.12.24's. TestIssue49RightBoundary asserts both
// the string and that it equals the v0.12.24 chain's.
var dottedQuadRightBoundaryRows = []struct {
	name, in, want string
}{
	// Not an address: the quad is detector 8's ipv4Pattern, so an octet out of range, a leading zero,
	// three octets or five are no dotted quad, and what main does with them stands. On five, 8 masks
	// the last four, which a quad ending after the fourth would cut.
	{"an octet out of range", "peer ::ffff:10.0.0.256 down", "peer <IP_1>.0.0.256 down"},
	{"a leading zero", "peer ::ffff:010.0.0.5 down", "peer <IP_1>.0.0.5 down"},
	{"three octets", "peer ::ffff:10.0.0 down", "peer <IP_1>.0.0 down"},
	{"five octets", "peer ::ffff:10.0.0.5.6 down", "peer <IP_1>.<IP_2> down"},
	// A value a later rule reads through the character after the quad: a private name (9b), and an
	// address glued on by ':' (7's next match).
	{"a private name after the quad", "peer ::ffff:10.0.0.5.corp.internal down", "peer <IP_1>.<HOST_1> down"},
	{"an address after the quad", "peer ::ffff:10.0.0.5:ab::1 down", "peer <IP_1>.0.0.<IP_2> down"},
	// KNOWN_GAP #137: the right-boundary remainder of 7a and 7b alike. Each follower can continue a
	// value a later rule reads, so neither rule fires, and the octets go as they always did. Pinned,
	// leak included, so closing them shows up here as a diff.
	{"KNOWN_GAP #137 a dotted name after the quad", "peer ::ffff:10.0.0.5.example down", "peer <IP_1>.0.0.5.example down"},
	{"KNOWN_GAP #137 a hyphen after the quad", "peer ::ffff:10.0.0.5-x down", "peer <IP_1>.0.0.5-x down"},
	{"KNOWN_GAP #137 a letter after the quad", "peer ::ffff:10.0.0.5x down", "peer <IP_1>.0.0.5x down"},
	{"KNOWN_GAP #137 an underscore after the quad", "peer ::ffff:10.0.0.5_x down", "peer <IP_1>.0.0.5_x down"},
	{"KNOWN_GAP #137 a hyphen after a glued quad", "peer:::ffff:10.0.0.5-x down", "peer<IP_1>:ffff:<HOST_1> down"},
	{"KNOWN_GAP #137 a dotted name after a glued quad", "peer:::ffff:10.0.0.5.example down", "peer<IP_1>:ffff:<HOST_1> down"},
}

// TestIssue49RightBoundary is #49's guard on the right: where neither rule may fire, the output is
// v0.12.24's, byte for byte.
func TestIssue49RightBoundary(t *testing.T) {
	ref := referenceDetectors(t, baseDetectors, refV01224)
	for _, c := range dottedQuadRightBoundaryRows {
		t.Run(c.name, func(t *testing.T) {
			got := mustMask(t, New(), c.in)
			if got != c.want {
				t.Errorf("right boundary moved:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
			if old := mustMask(t, mapperOn(ref), c.in); got != old {
				t.Errorf("differs from v0.12.24:\n in:   %q\n got:  %q\n then: %q", c.in, got, old)
			}
		})
	}
}

// TestIssue46PreMaskedCredentialHalf is the named regression guard for #46, and the reason it
// is not called "the password leaks" is the finding: the defect is SYMMETRIC.
//
// Detector 4's target group spans "user:pass" as ONE value. Detectors 1-3b run first and mint a
// <TOKEN_n> wherever they recognise a secret, and detector 4's classes exclude '<' and '>' by
// design — that exclusion is the inertness invariant, not an oversight. So a placeholder minted
// anywhere inside that span blocks the whole detector, and whichever half was NOT recognisable
// goes to the configured model endpoint as literal text. Mask returned a nil error the whole
// time, residue() returned "", no escalation was skipped, and the card looked normal.
//
// The dotted-host row is the one worth keeping. Before the fix it did NOT leak — the email
// detector matched "hunter2@db.acme.com" and swallowed the password under an <EMAIL_n> tag,
// glued to the host. That accident is why a reproduction against a fully-qualified host shows
// nothing wrong, and why the shapes that do leak are the ones with a service name, a loopback or
// an address after the '@': the ordinary DSN forms in container logs.
func TestIssue46PreMaskedCredentialHalf(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		leaked []string // must not survive anywhere in the masked output
	}{
		{"sk- username, bare host", "postgres://sk-livekeyabcdefghij0123456789XYZ:hunter2@redis:6379/0",
			[]string{"hunter2"}},
		{"sk- username, loopback", "postgres://sk-livekeyabcdefghij0123456789XYZ:hunter2@localhost:5432/app",
			[]string{"hunter2"}},
		{"sk- username, address host", "postgres://sk-livekeyabcdefghij0123456789XYZ:hunter2@10.0.0.5:5432/app",
			[]string{"hunter2"}},
		{"AKIA username, bare host", "s3://AKIAIOSFODNN7EXAMPLE:wJalrXUtnFEMIKauth@bucket",
			[]string{"wJalrXUtnFEMIKauth"}},
		{"JWT username, bare host", "postgres://eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u:hunter2@redis",
			[]string{"hunter2"}},
		// The mirror: the secret is the PASSWORD, so the USERNAME is what detector 4 can no
		// longer reach. The email detector cannot rescue this side — it would need the username
		// as a local part and the username sits BEFORE the colon — so the shapes that rescued
		// the password leak here instead. A different detector rescues it for a different shape;
		// see the address-host row below.
		{"sk- password", "postgres://appuser:sk-livekeyabcdefghij0123456789XYZ@db",
			[]string{"appuser"}},
		{"AKIA password", "s3://myaccount:AKIAIOSFODNN7EXAMPLE@bucket",
			[]string{"myaccount"}},
		{"JWT password", "amqp://svcbroker:eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u@rabbit",
			[]string{"svcbroker"}},
		{"sk- password, dotted host", "postgres://appuser:sk-livekeyabcdefghij0123456789XYZ@db.acme.com/prod",
			[]string{"appuser"}},
		// The third accidental rescue, and the one that makes "conditional" mean "leaks
		// somewhere else" rather than "safe". With an ADDRESS after the '@', detector 8 masks
		// the host before the URL-host detector runs, which destroys that detector's long parse
		// and leaves it falling back onto the userinfo — so "appuser" was masked, as <HOST_n>.
		// Correct outcome by the wrong route, under the wrong tag, and only for this shape.
		{"sk- password, address host", "postgres://appuser:sk-livekeyabcdefghij0123456789XYZ@10.0.0.5:5432/db",
			[]string{"appuser"}},
		// Both halves recognisable: nothing was ever wrong here, kept as the control.
		{"both halves secret", "postgres://sk-livekeyabcdefghij0123456789XYZ:sk-otherkeyabcdefghij9876543210AB@db",
			[]string{"livekey", "otherkey"}},
		// The accidental rescue, now done by the right detector under the right tag.
		{"sk- username, dotted host", "postgres://sk-livekeyabcdefghij0123456789XYZ:hunter2@db.acme.com/prod",
			[]string{"hunter2", "db.acme.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			masked := mustMask(t, m, c.in)
			for _, leak := range c.leaked {
				if strings.Contains(masked, leak) {
					t.Errorf("credential half survived masking: %q still in %q\n in: %q", leak, masked, c.in)
				}
			}
			if got := m.Restore(masked); got != c.in {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", c.in, got)
			}
		})
	}
}

// TestEmptyCredentialHalf is the SECOND defect the same patch closes, kept as its own test
// because the cause is different and folding it into #46's description would misreport it.
//
// Nothing is pre-masked here and no detector interferes with any other. Detector 4 simply
// required at least one character on each side of the colon, and a Redis DSN has no username —
// Redis had none before 6.0, so "redis://:password@host" is the ordinary shape rather than an
// edge case. It matched no credential detector at all, from v0.4.0 onward.
//
// Like #46 it was masked by accident on a host with a dotted alphabetic suffix (the email
// detector again), so the rows that matter are the ones without one.
func TestEmptyCredentialHalf(t *testing.T) {
	cases := []struct{ name, in, leaked string }{
		{"no username, bare host", "redis://:s3cr3tpw@redis:6379/0", "s3cr3tpw"},
		{"no username, loopback", "redis://:s3cr3tpw@localhost:6379/0", "s3cr3tpw"},
		{"no username, address host", "redis://:s3cr3tpw@10.0.0.5:6379/0", "s3cr3tpw"},
		{"no username, dotted host", "redis://:s3cr3tpw@cache.acme.internal:6379/0", "s3cr3tpw"},
		{"no password", "postgres://svcacct:@db", "svcacct"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			masked := mustMask(t, m, c.in)
			if strings.Contains(masked, c.leaked) {
				t.Errorf("credential survived masking: %q still in %q\n in: %q", c.leaked, masked, c.in)
			}
			if got := m.Restore(masked); got != c.in {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", c.in, got)
			}
		})
	}
}

// TestIssue55PasswordlessUserinfo is the named regression for #55, and it carries exact outputs
// because TestTypeTags checks Contains and so cannot see a tag that is right for the wrong span.
//
// A userinfo with no password matched NO credential detector from v0.4.0 onward. Detectors 4, 4b
// and 4c were all anchored on the ':' inside it, and 9a's userinfo run is CONTEXT that steps over
// the value to reach the host rather than a group that captures it — apply() writes only the
// target group and residue() tests only the target group, so a run that is neither masks nothing.
// "postgres://appuser@db" therefore sent "appuser", Mask returned nil, residue() returned "" and
// the escalation was SENT.
//
// Whether the username actually left the process depended on the authority, and that is where the
// issue AS FILED was wrong in the DANGEROUS direction. It said a dotted host rescues the username
// as an email and an address literal rescues it as a host. Both rescues are PARTIAL: each holds
// only while the username lies inside the RESCUING detector's alphabet, and both alphabets are
// narrower than RFC 3986's userinfo production. The rows below carry one character that defeats
// each, and they are the rows that would have been left out by an example chosen to pass.
func TestIssue55PasswordlessUserinfo(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// The shapes that leaked outright: an ordinary container or compose service name after the
		// '@', which is how a DSN is written in every docker-compose file in existence.
		{"bare host", "dsn=postgres://appuser@db:5432/app",
			"dsn=postgres://<TOKEN_1>@<HOST_1>:5432/app"},
		{"loopback", "dsn=postgres://appuser@localhost:5432/app",
			"dsn=postgres://<TOKEN_1>@<HOST_1>:5432/app"},
		{"service name", "dsn=amqp://svcbroker@rabbit:5672/vhost",
			"dsn=amqp://<TOKEN_1>@<HOST_1>:5672/vhost"},
		// The accidental rescues, now done by the right detector under the right tag. Before the
		// fix the first came back <EMAIL_1> covering the username AND the host as one value, and
		// the second came back <HOST_1> for the username with the address separately <IP_1>.
		{"dotted host", "dsn=postgres://appuser@db.acme.com/prod",
			"dsn=postgres://<TOKEN_1>@<HOST_1>/prod"},
		{"address host", "dsn=postgres://appuser@10.0.0.5:5432/db",
			"dsn=postgres://<TOKEN_1>@<IP_1>:5432/db"},
		// Each rescue defeated by ONE character. '!' is a sub-delim, legal in a userinfo and absent
		// from the email detector's local-part class, so before the fix this masked to
		// "dsn=postgres://<HOST_1>!<EMAIL_1>/prod" and the '!' went out between two placeholders.
		{"dotted host, sub-delim in username", "dsn=postgres://us!er@db.acme.com/prod",
			"dsn=postgres://<TOKEN_1>@<HOST_1>/prod"},
		// '_' is legal in a userinfo and absent from 9a's group class, so the host-tag rescue took
		// "svc" and sent the rest: "dsn=postgres://<HOST_1>_user@<IP_1>:5432/db".
		{"address host, underscore in username", "dsn=postgres://svc_user@10.0.0.5:5432/db",
			"dsn=postgres://<TOKEN_1>@<IP_1>:5432/db"},
		// Percent-encoding, which is what makes a password containing '@' or ':' legal at all. It
		// works because the classes are NEGATED — "%40" is three characters, none of which is '@' —
		// and until these rows nothing in this package tested that it does.
		{"pct-encoded username, no password", "dsn=postgres://u%40ser@db:5432/app",
			"dsn=postgres://<TOKEN_1>@<HOST_1>:5432/app"},
		{"pct-encoded password", "dsn=postgres://appuser:p%40ss@db:5432/app",
			"dsn=postgres://<TOKEN_1>@<HOST_1>:5432/app"},
		// A userinfo may contain more than one ':' (RFC 3986 §3.2.1). The merged rule spans it
		// exactly as detector 4 did, because detector 4's PASSWORD class already admitted ':' —
		// only the FIRST colon was ever structural, and only as a required delimiter.
		{"multi-colon userinfo", "dsn=postgres://appuser:hun:ter2@db:5432/app",
			"dsn=postgres://<TOKEN_1>@<HOST_1>:5432/app"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			got := mustMask(t, m, c.in)
			if got != c.want {
				t.Errorf("password-less userinfo not masked as expected:\n in:   %q\n got:  %q\n want: %q",
					c.in, got, c.want)
			}
			if r := m.Restore(got); r != c.in {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", c.in, r)
			}
		})
	}

	// The class is verify-eligible now, and it could not have been before: no detector matched the
	// shape at all, so residue() could not have caught it however hard it looked. If some future
	// detector ever blocks this rule the way 1-3b block detector 4, the escalation is MUTED rather
	// than sent — which is the outcome #46 taught us to want.
	if tag := New().residue("postgres://appuser@db:5432/app"); tag != tagToken {
		t.Errorf("a password-less userinfo is not verify-eligible: residue = %q, want %q", tag, tagToken)
	}

	// The negative half, in TestIssue48UUIDInHostname's style. These are ASSERTIONS that the gaps
	// are open, not tolerated failures: each goes red when its issue is fixed and is the reminder
	// to update the docs that call it open. One run therefore shows both what this fix reaches and
	// what it does not.
	open := []struct{ name, in, leak string }{
		// A half only PARTLY recognised by an earlier detector sat here as #57, with and without a
		// colon, until it was closed; it is TestIssue57PartlyRecognisedValue now.
		// A raw '@' in the password sat here as #58 until it was closed; it is
		// TestIssue58RawAtInUserinfo now, with the '/' case below kept as its control.
		// A raw '/' in the password. net/url ERRORS on this one, so unlike the '@' case there is no
		// reliable parse to widen towards and the honest candidate is to fail closed rather than
		// mask in part. Today the username is masked as a HOST and both the password remainder and
		// the real host are sent. #59.
		{"raw '/' in the password (#59)", "postgres://user:p/ss@db", "p/ss"},
		// No scheme, so no "://" for any credential rule to anchor on. By design rather than by
		// defect, and here so one run also shows the boundary of what the anchor reaches.
		{"no scheme, by design", "user@db:5432/app", "user@db"},
	}
	for _, c := range open {
		t.Run(c.name, func(t *testing.T) {
			masked := mustMask(t, New(), c.in)
			if !strings.Contains(masked, c.leak) {
				t.Errorf("this gap appears to be closed — update this test and the docs that call it open:\n"+
					" in:  %q\n out: %q\n looking for: %q", c.in, masked, c.leak)
			}
		})
	}
}

// TestIssue58RawAtInUserinfo is the named regression for #58, and it carries exact outputs for
// the same reason TestIssue55PasswordlessUserinfo does: the failure was a tag on the wrong span,
// and Contains cannot see that.
//
// Every credential class excluded '@' because '@' is "the delimiter they anchor on". So on
// "postgres://user:p@ss@db" detector 4 stopped at the FIRST '@', 9a read the "ss" behind it as an
// authority and tagged it HOST, and the real host "db" went to the configured model endpoint in
// the clear. A raw '@' in the USERNAME was worse: "us@er:pass@db" sent the password AND the host.
// Mask returned nil, residue() returned "", the escalation was SENT. v0.4.0 -> v0.9.2, sixteen
// tagged releases.
//
// A raw '@' in a userinfo is illegal — RFC 3986 §3.2.1 requires %40 — and common, because people
// put '@' in passwords and paste DSNs into logs without encoding them. It is NOT an unparseable
// URI: §3.2 makes the delimiter the LAST '@' before the authority ends, and net/url already does
// exactly that (user="user" pass="p@ss" host="db", no error). The rule here now says the same.
//
// THE FIX IS NOT ONE CHARACTER, and the 4c row is why. Admitting '@' into the classes and nothing
// else leaves detector 4 free to take a PREFIX of the userinfo when a tag sits between a raw '@'
// and the real delimiter -- "us@er:<TOKEN_1>@db" matched as "us@" -- and with 9a then reading to
// the last '@', the "er" went out in the clear where it used to be mis-tagged as a host. Trading a
// host leak for a username-fragment leak is the wrong direction, so detector 4 carries the second
// half of the RFC's rule as a trailing context (authorityTail): the delimiting '@' is the one
// after which NO further '@' appears before the authority ends. With that, 4 declines on the
// blocked shape and 4c takes the half, as it was written to.
func TestIssue58RawAtInUserinfo(t *testing.T) {
	const sk = "sk-livekeyabcdefghij0123456789XYZ"
	cases := []struct{ name, in, want string }{
		// The issue's own example, and the ordinary compose shapes around it.
		{"bare host", "postgres://user:p@ss@db", "postgres://<TOKEN_1>@<HOST_1>"},
		{"port and path", "postgres://user:p@ss@db:5432/app", "postgres://<TOKEN_1>@<HOST_1>:5432/app"},
		// The accidental rescue: the email detector took "ss@db.acme.com" whole, under the wrong
		// tag. Nothing left the process here, which is why a reproduction against a fully
		// qualified host shows nothing wrong -- the same accident #46 and #55 hid behind.
		{"dotted host", "postgres://user:p@ss@db.acme.com/prod", "postgres://<TOKEN_1>@<HOST_1>/prod"},
		// An address literal: the IP was masked, but "ss" still went out tagged as a HOST.
		{"address host", "postgres://user:p@ss@10.0.0.5:5432/app", "postgres://<TOKEN_1>@<IP_1>:5432/app"},
		{"bracketed ipv6 host", "postgres://user:p@ss@[2001:db8::1]:5432/app",
			"postgres://<TOKEN_1>@[<IP_1>]:5432/app"},
		// The username side, which the issue did not name and which leaks MORE: the password
		// sits behind the first '@' and goes out with the host.
		{"raw '@' in the username", "postgres://us@er:pass@db", "postgres://<TOKEN_1>@<HOST_1>"},
		{"raw '@' in a password-less userinfo", "postgres://us@er@db", "postgres://<TOKEN_1>@<HOST_1>"},
		{"two raw '@'s", "postgres://user:p@s@s@db", "postgres://<TOKEN_1>@<HOST_1>"},
		{"empty username", "redis://:p@ss@redis:6379/0", "redis://<TOKEN_1>@<HOST_1>:6379/0"},
		// #46's two cells with a raw '@' in the half the fallback must reach.
		{"pre-masked username, raw '@' in the password (4b)",
			"postgres://" + sk + ":p@ss@db", "postgres://<TOKEN_1>:<TOKEN_2>@<HOST_1>"},
		// The cell that forces authorityTail. See the comment above.
		{"raw '@' in the username, pre-masked password (4c)",
			"postgres://us@er:" + sk + "@db", "postgres://<TOKEN_2>:<TOKEN_1>@<HOST_1>"},
		// A replica-set URI: the credential is closed here. Until #61 this row pinned the second
		// host LITERAL -- "<HOST_1>:27017,host2:27017/db" -- as an assertion that the gap was open;
		// 9c takes the rest of the list now, and TestIssue61MultiHostAuthority owns the shape.
		{"multi-host, raw '@' in the password", "mongodb://user:p@ss@host1:27017,host2:27017/db",
			"mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017/db"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			got := mustMask(t, m, c.in)
			if got != c.want {
				t.Errorf("raw '@' in a userinfo not masked to the last '@':\n in:   %q\n got:  %q\n want: %q",
					c.in, got, c.want)
			}
			if r := m.Restore(got); r != c.in {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", c.in, r)
			}
		})
	}

	// Verify-eligibility, stated honestly. Detector 4 describes the WHOLE raw-'@' userinfo now, so
	// one that ever survives masking whole mutes the escalation. That was true before too -- the
	// old rule matched a PREFIX of it -- and it is not what failed: what failed was the LEFTOVER,
	// and the leftover is a shape the re-scan reads as clean, which is #41's blindness and the
	// reason the fix lives in the pattern rather than in residue().
	if tag := New().residue("postgres://user:p@ss@db"); tag != tagToken {
		t.Errorf("a raw-'@' userinfo is not verify-eligible: residue = %q, want %q", tag, tagToken)
	}
	if tag := New().residue("postgres://<TOKEN_1>@<HOST_1>@db"); tag != "" {
		t.Errorf("residue() sees the old leftover as %q; the claim in the comment above is wrong", tag)
	}

	// The negative half, as ASSERTIONS that these gaps are open. Each goes red when its issue is
	// fixed and is the reminder to update the docs that call it open.
	open := []struct{ name, in, leak string }{
		// A raw '/' in the password. net/url ERRORS on this one, and it errors the same way on a
		// raw '?' and '#', which is the measurement that keeps those two out of THIS fix: bounding
		// the userinfo at '?' and '#' would reopen "postgres://user:p?ss@db", fully masked today.
		// All three are #59's class -- no reliable parse -- and #59's remedy, whatever it is.
		{"raw '/' in the password (#59)", "postgres://user:p/ss@db", "p/ss"},
		// #57 with a raw '@' behind the tag sat here until it was closed; it is
		// TestIssue57PartlyRecognisedValue now.
		// The second host of a replica set sat here as #61 until it was closed; it is
		// TestIssue61MultiHostAuthority now, with the open cells of ITS sweep pinned there.
	}
	for _, c := range open {
		t.Run(c.name, func(t *testing.T) {
			masked := mustMask(t, New(), c.in)
			if !strings.Contains(masked, c.leak) {
				t.Errorf("this gap appears to be closed — update this test and the docs that call it open:\n"+
					" in:  %q\n out: %q\n looking for: %q", c.in, masked, c.leak)
			}
		})
	}
}

// TestIssue61MultiHostAuthority is the named regression for #61, and it is the first of a FIFTH
// kind of cause in this package. Not an under-matching pattern (#41), not a value another component
// rewrote first (#43), not a detector blocked by another's tag (#46, #48, #54, #57), not a shape no
// rule described (#55): detector 9a matched exactly what it was written for -- one host -- and
// stopped where its author thought the value ended, at the port colon and the comma. Its notion of
// the value was smaller than the value. mongodb://user:pass@host1:27017,host2:27017,host3:27017/db
// sent host2 and host3 to the configured model endpoint, in the trigger line AND in the masked
// template of the same request. v0.4.0 -> v0.9.3, seventeen tagged releases.
//
// Neither residue() nor the completeness rows could see it, by construction: both ask whether the
// group landed on the DECLARED secret, and it did. What would have is a sweep of the authority
// production the way #55 swept the userinfo production, which had not been done.
//
// THE FIX IS A COMPANION RULE, 9c, AND NOT A WIDER CLASS ON 9a -- measured rather than preferred.
// Widening 9a's own group to span the list closes every shape below and OPENS a leak on a shape
// masked today: "peers=https://a.com,https://b.com" is <HOST_1>,https://<HOST_2> and would become
// <HOST_1>://b.com, because a comma member that is the next URL's scheme is a legal reg-name (RFC 3986
// §3.2.2 puts ',' in sub-delims; net/url reads that string as host "a.com,https" and path "//b.com")
// and RE2 has no lookahead to refuse it. A trailing context that rejects "://" cannot go on 9a --
// a REQUIRED terminator on a first-pass rule narrows what it already masks -- but it can go on a
// companion, because a companion's no-match is today's behaviour. So 9c reads the first member's
// tag as context, the way 4b and 4c read a credential half.
//
// Until #67 it took hosts 2..n as ONE group, so a list of any length came back as two placeholders.
// Since #67 the group is one member and 9c is re-applied until nothing changes, so every member is
// its own placeholder (TestIssue67LaterMemberTag). Each closed case below pins the exact output AND
// asserts that no declared member survives literally -- absence of known strings, not a survival
// threshold (#41 rejected thresholds, and that stands) -- and that is why the move was a layout
// change that rewrote `want` on the three-member rows and nothing else.
func TestIssue61MultiHostAuthority(t *testing.T) {
	const (
		sk   = "sk-livekeyabcdefghij0123456789XYZ"
		uuid = "550e8400-e29b-41d4-a716-446655440000"
	)
	closed := []struct {
		name, in, want string
		members        []string // every host of the list; none may appear literally in the output
	}{
		// The issue's example and its siblings: replica set, failover list, no credential at all.
		{"replica set", "mongodb://user:pass@host1:27017,host2:27017,host3:27017/db?replicaSet=rs0",
			"mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017,<HOST_3>:27017/db?replicaSet=rs0", []string{"host1", "host2", "host3"}},
		{"mysql failover", "mysql://user:pass@host1,host2/db",
			"mysql://<TOKEN_1>@<HOST_1>,<HOST_2>/db", []string{"host1", "host2"}},
		{"no credential", "mongodb://host1:27017,host2:27017/db?replicaSet=rs0",
			"mongodb://<HOST_1>:27017,<HOST_2>:27017/db?replicaSet=rs0", []string{"host1", "host2"}},
		{"bare names, no ports", "mongodb://host1,host2,host3/db",
			"mongodb://<HOST_1>,<HOST_2>,<HOST_3>/db", []string{"host1", "host2", "host3"}},
		{"redis sentinel", "redis://:pw@host1:6379,host2:6379/0",
			"redis://<TOKEN_1>@<HOST_1>:6379,<HOST_2>:6379/0", []string{"host1", "host2"}},
		// Suffixes in both orders. 9c runs BEFORE 9b, so a private-suffix member later in the list
		// is part of the rest rather than 9b's own tag; after 9b it would have blocked 9c's group
		// and sent every member behind it (measured: <HOST_1>,<HOST_2>,h3.acme.com/db).
		{"private then public", "mongodb://user:pass@h1.corp.internal:27017,h2.acme.com:27017/db",
			"mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017/db", []string{"h1.corp.internal", "h2.acme.com"}},
		{"public, private, public", "mongodb://h1.acme.com,h2.corp.internal,h3.acme.com/db",
			"mongodb://<HOST_1>,<HOST_2>,<HOST_3>/db", []string{"h1.acme.com", "h2.corp.internal", "h3.acme.com"}},
		// Case: the group class sits under (?i) and Go folds the negated listEnd class too, so an
		// uppercase character neither starts a member nor ends one (measured, not assumed).
		{"mixed case members", "mongodb://host1,Host2.ACME.com,host3/db",
			"mongodb://<HOST_1>,<HOST_2>,<HOST_3>/db", []string{"host1", "Host2.ACME.com", "host3"}},
		{"mixed case, ports, credential", "MongoDB://user:pass@Host1:27017,HOST2:27017,Host3:27017/DB",
			"MongoDB://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017,<HOST_3>:27017/DB", []string{"Host1", "HOST2", "Host3"}},
		// A pre-masked credential half on either side: 9c's userinfo skip steps over the tags
		// exactly as 9a's does.
		{"pre-masked username", "mongodb://" + sk + ":pass@host1:27017,host2:27017/db",
			"mongodb://<TOKEN_1>:<TOKEN_2>@<HOST_1>:27017,<HOST_2>:27017/db", []string{"host1", "host2"}},
		{"pre-masked password", "mongodb://user:" + sk + "@host1:27017,host2:27017/db",
			"mongodb://<TOKEN_2>:<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017/db", []string{"host1", "host2"}},
		{"raw '@' in the password (#58's row)", "mongodb://user:p@ss@host1:27017,host2:27017/db",
			"mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017/db", []string{"host1", "host2"}},
		// THE FIRST MEMBER'S TAG SET, derived by walking the chain rather than sampled. Before 9c
		// runs, a tag can land on member 1 from 7 and 8 (an address literal, bracketed or not), from
		// 9a (the ordinary case), from 1/2/3b only when a host literally IS a key, from 9d and 9e (an
		// address or a key fused with labels), and adjacent to another when a key is directly
		// followed by an address literal. 5 reached member 1 only through #57's shape until #57 was
		// closed; that userinfo is 4d's now and comes back one token. 3a is impossible
		// (it needs whitespace, and none can precede member 1 inside an authority); 4 mints on the
		// userinfo, never on a member; 9b, 6 and 10 run after 9c. The context therefore admits one
		// or more adjacent tags of ours, a bracketed one (ours, or the pipeline's <IP> on the
		// Template field), or a raw run -- and every excluded placement is unreachable, not
		// unobserved. One row per reachable tag:
		{"address first", "mongodb://user:pass@10.0.0.5:27017,host2:27017/db",
			"mongodb://<TOKEN_1>@<IP_1>:27017,<HOST_1>:27017/db", []string{"host2"}},
		{"address first, pre-masked username", "mongodb://" + sk + ":pass@10.0.0.5:27017,host2:27017/db",
			"mongodb://<TOKEN_1>:<TOKEN_2>@<IP_1>:27017,<HOST_1>:27017/db", []string{"host2"}},
		{"bracketed ipv6 first", "mongodb://user:pass@[2001:db8::1]:27017,host2:27017/db",
			"mongodb://<TOKEN_1>@[<IP_1>]:27017,<HOST_1>:27017/db", []string{"host2"}},
		{"unbracketed ipv6 first", "mongodb://2001:db8::1,host2/db",
			"mongodb://<IP_1>,<HOST_1>/db", []string{"host2"}},
		{"key-shaped first member (theoretical)", "mongodb://AKIAIOSFODNN7EXAMPLE,host2/db",
			"mongodb://<TOKEN_1>,<HOST_1>/db", []string{"host2"}},
		{"adjacent tags first: a key, then an address (theoretical)", "mongodb://sk-abcdefghij0123456789XYZ::1,host2/db",
			"mongodb://<TOKEN_1><IP_1>,<HOST_1>/db", []string{"host2"}},
		// A UUID label as a later member. 9c runs before 6, so the member is a host run to it; after
		// 6 it would have been <HOST_1>,<UUID_1>.acme.com/db (measured) -- #48's leak, one member in.
		{"uuid-label member", "mongodb://host1," + uuid + ".acme.com/db",
			"mongodb://<HOST_1>,<HOST_2>/db", []string{"host1", uuid + ".acme.com"}},
		// The boundary shapes, measured rather than reasoned. The group class is positive, so '?' and
		// '#' end it by construction -- this is NOT #58's decision about a negated userinfo class
		// imported by analogy, it is what the class says.
		{"list ends at '?'", "mongodb://host1,host2?replicaSet=rs0",
			"mongodb://<HOST_1>,<HOST_2>?replicaSet=rs0", []string{"host1", "host2"}},
		{"list ends at '#'", "mongodb://host1,host2#frag",
			"mongodb://<HOST_1>,<HOST_2>#frag", []string{"host1", "host2"}},
		{"list ends at end of text", "dsn=mongodb://user:pass@host1:27017,host2:27017",
			"dsn=mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017", []string{"host1", "host2"}},
		{"angle-bracketed", "<mongodb://user:pass@host1:27017,host2:27017/db>",
			"<mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017/db>", []string{"host1", "host2"}},
		// An empty member. libpq accepts one ("an empty item in the list selects the default"), so
		// the separator is one or more commas; with a single comma this sent h2.
		{"empty member", "mongodb://h1,,h2/db",
			"mongodb://<HOST_1>,,<HOST_2>/db", []string{"h1", "h2"}},
		{"trailing comma after the list", "mongodb://host1,host2,/db",
			"mongodb://<HOST_1>,<HOST_2>,/db", []string{"host1", "host2"}},
		{"two lists on one line", "mongodb://user:pass@host1:27017,host2:27017/db,mongodb://user:pass@host3:27017,host4:27017/db",
			"mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_3>:27017/db,mongodb://<TOKEN_1>@<HOST_2>:27017,<HOST_4>:27017/db",
			[]string{"host1", "host2", "host3", "host4"}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			got := mustMask(t, m, c.in)
			if got != c.want {
				t.Errorf("host list not masked as expected:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
			for _, member := range c.members {
				if strings.Contains(got, member) {
					t.Errorf("list member survived masking: %q still in %q", member, got)
				}
			}
			if r := m.Restore(got); r != c.in {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", c.in, r)
			}
		})
	}

	// The negative half, part one: byte-identical before and after. What anchors 9c to an authority
	// is "scheme://" followed, with no whitespace ANYWHERE, by an optional userinfo run, the first
	// member, a comma and a host run. So a comma-separated list of URLs keeps every URL (the shape
	// that decided the design), a space after the comma ends the URI, a member in scheme position is
	// declined by the "://" rejection, a scheme-less list is prose by design, and a bracketed address
	// in prose -- with or without a scheme or an '@' elsewhere on the line -- is never reached.
	same := []struct{ name, in, want string }{
		{"single host with a port", "postgres://user:pass@db:5432/app", "postgres://<TOKEN_1>@<HOST_1>:5432/app"},
		{"comma-separated urls", "peers=https://a.com,https://b.com", "peers=https://<HOST_1>,https://<HOST_2>"},
		{"comma-separated urls, credential in the second", "peers=https://a.com,https://u:p@b.com",
			"peers=https://<HOST_1>,https://<TOKEN_1>@<HOST_2>"},
		{"comma then space in prose", "tried https://api.acme.com, then gave up", "tried https://<HOST_1>, then gave up"},
		{"colon then space in prose", "url: https://api.acme.com: refused", "url: https://<HOST_1>: refused"},
		{"space after the comma ends the uri", "mongodb://host1:27017, host2:27017/db", "mongodb://<HOST_1>:27017, host2:27017/db"},
		{"member in scheme position", "mongodb://host1,host2://x", "mongodb://<HOST_1>,host2://<HOST_2>"},
		{"no scheme, by design", "bootstrap=host1:9092,host2:9092", "bootstrap=host1:9092,host2:9092"},
		{"prose: bracketed address, comma", "[10.0.0.5] connected, retrying", "[<IP_1>] connected, retrying"},
		{"prose: bracketed address glued to a comma", "[10.0.0.5],retrying", "[<IP_1>],retrying"},
		{"prose: bracketed address, space before the comma", "peer [10.0.0.5] , retrying", "peer [<IP_1>] , retrying"},
		{"prose: two bracketed addresses, no scheme, no '@'", "peer [10.0.0.5] down [10.0.0.6],restarting",
			"peer [<IP_1>] down [<IP_2>],restarting"},
		{"prose: a scheme earlier on the line", "GET https://api.acme.com/x from [10.0.0.5],retrying",
			"GET https://<HOST_1>/x from [<IP_1>],retrying"},
		{"prose: an '@' earlier on the line", "mail bob@corp.example.com from [10.0.0.5],retrying",
			"mail <EMAIL_1> from [<IP_1>],retrying"},
		{"prose: address glued to a comma, no scheme", "10.0.0.5,retrying", "<IP_1>,retrying"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			if got := mustMask(t, New(), c.in); got != c.want {
				t.Errorf("a shape outside #61 moved:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		})
	}

	// The negative half, part two: ASSERTIONS that these gaps are open, in the style of the #55 and
	// #58 tests. Each goes red when its issue is fixed and is the reminder to update the docs.
	open := []struct{ name, in, leak string }{
		// A tag minted on a LATER member sat here as #67, one row per reachable tag, until it was
		// closed; it is TestIssue67LaterMemberTag now.
		// #59's shape with a list: the authority ended at the raw '/', so host2 is PATH to the RFC
		// and to net/url alike, and no context design reaches it. Still #59.
		{"raw '/' in the password, with a list (#59)", "postgres://user:p/ss@h1.acme.com,host2/db", "host2"},
		// An underscore in a hostname sat here as #66 until it was closed; it is
		// TestIssue66UnderscoreInHostname now.
	}
	for _, c := range open {
		t.Run(c.name, func(t *testing.T) {
			masked := mustMask(t, New(), c.in)
			if !strings.Contains(masked, c.leak) {
				t.Errorf("this gap appears to be closed — update this test and the docs that call it open:\n"+
					" in:  %q\n out: %q\n looking for: %q", c.in, masked, c.leak)
			}
		})
	}

	// Verify-eligibility, stated honestly: there is none. Both host detectors and 9c are
	// verify:false, so the re-scan never looked at the old leftover -- and had they been eligible it
	// would still have read it as clean, because "host2:27017,host3" is not a shape any of them
	// matches on its own (#41's structural reason). The fix lives in the chain, not in residue().
	if tag := New().residue("mongodb://<TOKEN_1>@<HOST_1>:27017,host2:27017/db"); tag != "" {
		t.Errorf("residue() sees the old leftover as %q; the claim in the comment above is wrong", tag)
	}
}

// TestIssue67LaterMemberTag is the named regression for #67, a remainder of #61's fix. 9c took hosts
// 2..n as one group, and that group excludes '<' -- the inertness invariant every target group
// carries -- so a placeholder minted on a LATER member before 9c ran ended the group there, and
// every member after it went to the configured model endpoint:
// mongodb://host1:27017,10.0.0.5:27017,host3:27017/db sent host3. Mask returned nil and residue()
// returned "": 9c is verify:false, and "host3:27017/db" is not a shape it matches on its own.
// v0.9.4 onward; before #61 no rule masked members 2..n at all.
//
// The tags that can reach a later member were derived by walking the chain, not sampled: 7 and 8
// (an address literal, bracketed or not) are the ordinary way in; 1, 2 and 3b only when a member
// literally IS a key. 3a needs whitespace and 10 a '/', both of which end the authority; any '@'
// inside one is detector 4's delimiter first, so 5 cannot; 4 mints on the userinfo; 6 and 9b run
// after 9c.
//
// THE FIX RE-ANCHORS ON THE TAG rather than undoing it, because here the inner tag is RIGHT -- an
// address in a list is an address. That is what separates this from #54, whose inner tag is wrong.
// 9c's group is one member now, its context steps over every member already tagged, and Mask
// re-applies it until a pass changes nothing, so each member is its own placeholder.
//
// The Template field was NOT safe either, which the issue said it was: an unbracketed address
// collapses to the pipeline's <IP>, which the tolerant host run steps over, but a bracketed one
// becomes "[<IP>]" and a key "<TOKEN_n>", and 9c could start on neither, so host<NUM> was sent.
func TestIssue67LaterMemberTag(t *testing.T) {
	closed := []struct {
		name, in, want string
		members        []string // every member; none may appear literally in the output
	}{
		// The four cells #61 pinned open, one per reachable tag.
		{"address as a later member", "mongodb://host1:27017,10.0.0.5:27017,host3:27017/db",
			"mongodb://<HOST_1>:27017,<IP_1>:27017,<HOST_2>:27017/db", []string{"host1", "10.0.0.5", "host3"}},
		{"unbracketed ipv6 as a later member", "mongodb://h1,2001:db8::1,host3/db",
			"mongodb://<HOST_1>,<IP_1>,<HOST_2>/db", []string{"h1", "2001:db8::1", "host3"}},
		{"bracketed ipv6 as a later member", "mongodb://h1,[2001:db8::1]:27017,host3:27017/db",
			"mongodb://<HOST_1>,[<IP_1>]:27017,<HOST_2>:27017/db", []string{"h1", "2001:db8::1", "host3"}},
		{"key-shaped later member (theoretical)", "mongodb://h1,AKIAIOSFODNN7EXAMPLE,host3/db",
			"mongodb://<HOST_1>,<TOKEN_1>,<HOST_2>/db", []string{"h1", "AKIAIOSFODNN7EXAMPLE", "host3"}},
		// A run of tags in the middle, and tags alternating with names: the second needs a pass per
		// name, re-anchoring each time on a tag 8 minted rather than one 9c did.
		{"two consecutive addresses", "mongodb://h1,10.0.0.5,10.0.0.6,host4/db",
			"mongodb://<HOST_1>,<IP_1>,<IP_2>,<HOST_2>/db", []string{"h1", "10.0.0.5", "10.0.0.6", "host4"}},
		{"addresses alternating with names", "mongodb://h1,10.0.0.5,h3,10.0.0.6,h5/db",
			"mongodb://<HOST_1>,<IP_1>,<HOST_2>,<IP_2>,<HOST_3>/db", []string{"h1", "10.0.0.5", "h3", "10.0.0.6", "h5"}},
		{"two addresses first", "mongodb://10.0.0.5,10.0.0.6,host3/db",
			"mongodb://<IP_1>,<IP_2>,<HOST_1>/db", []string{"10.0.0.5", "10.0.0.6", "host3"}},
		// Adjacent tags as a later member, the later-position twin of #61's #57-shaped first member:
		// a key directly followed by an address literal. Theoretical, and here because the context
		// admits one or more adjacent tags in both positions.
		{"adjacent tags as a later member (theoretical)", "mongodb://h1,sk-abcdefghij0123456789XYZ::1,host3/db",
			"mongodb://<HOST_1>,<TOKEN_1><IP_1>,<HOST_2>/db", []string{"h1", "abcdefghij", "host3"}},
		// One placeholder per member: the layout #61 could not give.
		{"eight-member replica set",
			"mongodb://user:pass@m1:27017,m2:27017,m3:27017,m4:27017,m5:27017,m6:27017,m7:27017,m8:27017/db?replicaSet=rs0",
			"mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017,<HOST_3>:27017,<HOST_4>:27017,<HOST_5>:27017,<HOST_6>:27017,<HOST_7>:27017,<HOST_8>:27017/db?replicaSet=rs0",
			[]string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8"}},
		// #61's port asymmetry, retired: it sat in TestAcceptedOverMasking as
		// "<HOST_1>:27017,<HOST_2>:27019", with 27018 inside <HOST_2>.
		{"interior ports visible", "mongodb://user:pass@host1:27017,host2:27018,host3:27019/db",
			"mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27018,<HOST_3>:27019/db", []string{"host1", "host2", "host3"}},
		// One pass masks the next member of EVERY list on the line, so two lists interleave their
		// numbering pass by pass.
		{"two three-member lists on one line", "mongodb://host1,host2,host3/db,mongodb://host4,host5,host6/db",
			"mongodb://<HOST_1>,<HOST_3>,<HOST_5>/db,mongodb://<HOST_2>,<HOST_4>,<HOST_6>/db",
			[]string{"host1", "host2", "host3", "host4", "host5", "host6"}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			got := mustMask(t, m, c.in)
			if got != c.want {
				t.Errorf("host list not masked as expected:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
			for _, member := range c.members {
				if strings.Contains(got, member) {
					t.Errorf("list member survived masking: %q still in %q", member, got)
				}
			}
			if r := m.Restore(got); r != c.in {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", c.in, r)
			}
		})
	}

	// The Template field, through the real pipeline.Templatize. The bracketed and key-shaped rows
	// sent host<NUM> before the fix; the other two were masked, as one placeholder for the rest.
	templated := []struct{ name, raw, want string }{
		{"address as a later member", "mongodb://host1:27017,10.0.0.5:27017,host3:27017/db",
			"mongodb://<HOST_1>:<NUM>,<HOST_2>:<NUM>,<HOST_1>:<NUM>/db"},
		{"unbracketed ipv6 as a later member", "mongodb://h1,2001:db8::1,host3/db",
			"mongodb://<HOST_1>,<HOST_2>,<HOST_3>/db"},
		{"bracketed ipv6 as a later member", "mongodb://h1,[2001:db8::1]:27017,host3:27017/db",
			"mongodb://<HOST_1>,[<IP>]:<NUM>,<HOST_2>:<NUM>/db"},
		{"key-shaped later member (theoretical)", "mongodb://h1,AKIAIOSFODNN7EXAMPLE,host3/db",
			"mongodb://<HOST_1>,<TOKEN_1>,<HOST_2>/db"},
	}
	for _, c := range templated {
		t.Run("template/"+c.name, func(t *testing.T) {
			in, _ := pipeline.Templatize(c.raw)
			m := New()
			got := mustMask(t, m, in)
			if got != c.want {
				t.Errorf("templatized host list not masked as expected:\n raw:  %q\n in:   %q\n got:  %q\n want: %q",
					c.raw, in, got, c.want)
			}
			if strings.Contains(got, "host<NUM>") {
				t.Errorf("a templatized member survived masking: %q", got)
			}
			if r := m.Restore(got); r != in {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", in, r)
			}
		})
	}

	// Byte-identical before and after: an address LAST blocks nothing, because nothing follows it,
	// and an address FIRST was #61's own row, closed by 9c's member-1 context.
	same := []struct{ name, in, want string }{
		{"address last", "mongodb://host1:27017,host2:27017,10.0.0.5:27017/db",
			"mongodb://<HOST_1>:27017,<HOST_2>:27017,<IP_1>:27017/db"},
		{"address first", "mongodb://10.0.0.5:27017,host2:27017/db", "mongodb://<IP_1>:27017,<HOST_1>:27017/db"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			if got := mustMask(t, New(), c.in); got != c.want {
				t.Errorf("a shape outside #67 moved:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		})
	}

	// What one placeholder per member buys besides the leak: a member that recurs elsewhere in the
	// request gets the SAME placeholder there, because the stable token is keyed on the exact value.
	// Before, the list's tail was the single value "host2:27017,host3", so host2 alone in a context
	// line minted a new token and the model could not see it recur.
	t.Run("a member recurs under one placeholder", func(t *testing.T) {
		m := New()
		mustMask(t, m, "mongodb://host1:27017,host2:27017,host3:27017/db")
		const in, want = "dial https://host2:8443/x failed", "dial https://<HOST_2>:8443/x failed"
		if got := mustMask(t, m, in); got != want {
			t.Errorf("a list member did not recur under its list placeholder:\n in:   %q\n got:  %q\n want: %q", in, got, want)
		}
	})

	// Verify-eligibility, stated honestly: there is still none, and the old leftover reads as clean.
	if tag := New().residue("mongodb://<HOST_1>:27017,<IP_1>:27017,host3:27017/db"); tag != "" {
		t.Errorf("residue() sees the old leftover as %q; the claim in the comment above is wrong", tag)
	}
}

// TestHostListFixpoint pins the mechanism #67 added: 9c is the one rule Mask re-applies until a
// pass changes nothing, the loop terminates within the bound applyToFixpoint argues, the bound
// counts the LONGEST list on a line rather than the sum of them, and past maxHostListPasses Mask
// fails closed instead of returning what it has.
func TestHostListFixpoint(t *testing.T) {
	tagRe := regexp.MustCompile(phPat)
	// list builds "mongodb://" + n comma-separated members + "/db"; member(i) names member i.
	list := func(n int, member func(i int) string) string {
		ms := make([]string, n)
		for i := range ms {
			ms[i] = member(i + 1)
		}
		return "mongodb://" + strings.Join(ms, ",") + "/db"
	}
	name := func(i int) string { return "n" + strconv.Itoa(i) + "x" }
	cells := func(n int) string {
		var b strings.Builder
		b.WriteString("https://api.acme.com")
		for i := 0; i < n; i++ {
			b.WriteString("," + strconv.Itoa(i%10))
		}
		return b.String()
	}

	t.Run("only 9c iterates", func(t *testing.T) {
		for _, dets := range [][]detector{baseDetectors, buildDetectors([]string{"example"})} {
			var n int
			for _, d := range dets {
				if d.fixpoint {
					n++
					if d.tag != tagHost || d.verify || d.group != 1 {
						t.Errorf("an unexpected rule iterates: tag %s, group %d, verify %v\n pattern: %s",
							d.tag, d.group, d.verify, d.re.String())
					}
				}
			}
			if n != 1 {
				t.Errorf("%d rules iterate, want exactly one (9c): apply's one-pass contract is every other rule's", n)
			}
		}
	})

	// Termination, up to the cap: maxHostListPasses later raw members is the most a list may need,
	// and every member comes back as its own placeholder. The shapes are the ones that change how
	// many passes a list takes: every member raw, addresses between the names (fewer raw members,
	// re-anchoring on a tag 8 minted), and an empty item between every pair.
	shapes := []struct {
		name   string
		member func(i int) string
		sep    string
	}{
		{"all raw", name, ","},
		{"addresses between names", func(i int) string {
			if i%2 == 0 {
				return "10.0.0." + strconv.Itoa(i)
			}
			return name(i)
		}, ","},
		{"empty items", name, ",,"},
		// #54: a member that is an address inside a name reaches 9c already a whole tag, so it costs
		// no pass, and the list re-anchors on it as it does on 8's.
		{"address-label names between names", func(i int) string {
			if i%2 == 0 {
				return "10.0.0." + strconv.Itoa(i) + ".nip.io"
			}
			return name(i)
		}, ","},
		// #57: a member with a key glued inside it is 9e's before 9c runs, so it reaches 9c as a whole
		// tag, costs no pass, and the list re-anchors on it as it does on 9d's.
		{"key-fused names between names", func(i int) string {
			if i%2 == 0 {
				return fmt.Sprintf("AKIA%016d.prod", i)
			}
			return name(i)
		}, ","},
	}
	for _, sh := range shapes {
		t.Run("terminates/"+sh.name, func(t *testing.T) {
			for n := 2; n <= maxHostListPasses+1; n++ {
				ms := make([]string, n)
				for i := range ms {
					ms[i] = sh.member(i + 1)
				}
				in := "mongodb://" + strings.Join(ms, sh.sep) + "/db"
				m := New()
				got, err := m.Mask(in)
				if err != nil {
					t.Fatalf("%d members: Mask errored below the cap: %v", n, err)
				}
				if c := len(tagRe.FindAllString(got, -1)); c != n {
					t.Errorf("%d members came back as %d placeholders: %q", n, c, got)
				}
				for _, mem := range ms {
					if strings.Contains(got, mem+",") || strings.Contains(got, mem+"/") {
						t.Errorf("%d members: %q survived masking in %q", n, mem, got)
					}
				}
				if r := m.Restore(got); r != in {
					t.Errorf("%d members: round-trip mismatch", n)
				}
			}
		})
	}

	// One pass masks the next member of EVERY list on the line, so the cap counts the longest
	// list and not the sum. Two lists at the cap on one line need 64 passes, not 128.
	t.Run("the cap counts the longest list, not the sum", func(t *testing.T) {
		for _, n := range []int{40, maxHostListPasses + 1} {
			one := list(n, name)
			two := one + " " + list(n, func(i int) string { return "m" + strconv.Itoa(i) + "y" })
			got, err := New().Mask(two)
			if err != nil {
				t.Fatalf("two %d-member lists on one line: %v", n, err)
			}
			if c := len(tagRe.FindAllString(got, -1)); c != 2*n {
				t.Errorf("two %d-member lists came back as %d placeholders, want %d", n, c, 2*n)
			}
		}
	})

	// A list and a long CSV tail on one line behave as each would alone: masked the same way when
	// both settle, and failing closed exactly when the tail alone would.
	t.Run("a list beside a csv tail behaves as each alone", func(t *testing.T) {
		shape := func(s string) string { return tagRe.ReplaceAllString(s, "<T>") }
		lst := list(40, name)
		for _, cellsN := range []int{60, maxHostListPasses + 1} {
			tail := cells(cellsN)
			aloneL, errL := New().Mask(lst)
			aloneC, errC := New().Mask(tail)
			both, errB := New().Mask(lst + " " + tail)
			if errL != nil {
				t.Fatalf("the 40-member list alone errored: %v", errL)
			}
			if (errB != nil) != (errC != nil) {
				t.Errorf("%d cells: together err=%v, the tail alone err=%v", cellsN, errB, errC)
			}
			if errB == nil && shape(both) != shape(aloneL)+" "+shape(aloneC) {
				t.Errorf("%d cells: together the line masked differently from each alone:\n both:  %q\n alone: %q",
					cellsN, both, aloneL+" "+aloneC)
			}
		}
	})

	// THE CAP FAILS CLOSED. One raw member past it -- in a host list, or in comma cells after a
	// path-less URL, which 9c reads the same way -- and Mask returns no string at all, so the caller
	// can only skip the escalation. Returning what it had would send every member it had not reached.
	t.Run("past the cap Mask fails closed", func(t *testing.T) {
		for _, in := range []string{list(maxHostListPasses+2, name), cells(maxHostListPasses + 1)} {
			got, err := New().Mask(in)
			if err == nil {
				t.Errorf("Mask did not fail closed past the cap; it returned %q", got)
				continue
			}
			if got != "" {
				t.Errorf("Mask returned text beside its error: %q", got)
			}
			if !strings.Contains(err.Error(), "host list") {
				t.Errorf("the error does not name its cause: %v", err)
			}
		}
	})
}

// TestIssue66UnderscoreInHostname is the named regression for #66, and it is the fifth kind of cause
// again, the second instance of it: the host class was smaller than the production. RFC 3986 puts
// '_' in unreserved, so it is legal anywhere in a reg-name, and it is ordinary where logs come from
// -- Docker container names, Kafka brokers, internal hosts. No host class admitted it, so every host
// rule stopped at the first '_' and sent the rest: http://my_host:8080/x went out as
// http://<HOST_1>_host:8080/x, and a dotted name lost its whole domain behind the '_'. The email
// detector's domain class had the same gap, and there the WHOLE address was sent, local part
// included. Mask returned nil and residue() returned "": 9a, 9b and 9c are verify:false, and no
// declared secret in the completeness table carried a '_'. v0.4.0 onward.
//
// Each closed case pins the exact output, asserts that no fragment the old output sent survives,
// and round-trips.
func TestIssue66UnderscoreInHostname(t *testing.T) {
	closed := []struct {
		name, in, want string
		sent           []string // what the old output sent; none may survive
	}{
		// The issue's shapes. 9a's group stopped at the '_'.
		{"single host", "http://my_host:8080/x", "http://<HOST_1>:8080/x", []string{"my_host", "_host"}},
		{"credentialed dsn", "postgres://u:p@my_db:5432/app",
			"postgres://<TOKEN_1>@<HOST_1>:5432/app", []string{"my_db", "_db"}},
		{"dotted domain behind the underscore", "postgres://u:p@db_1.acme.com/app",
			"postgres://<TOKEN_1>@<HOST_1>/app", []string{"db_1", "acme.com"}},
		{"two underscores and a digit", "http://my_host_2:8080/x", "http://<HOST_1>:8080/x", []string{"_host_2"}},
		// 9c: the group is hostRun, so a later member stopped at its '_' the same way.
		// "_2" alone is not checked: it occurs inside <HOST_2>.
		{"host-list member", "mongodb://host1,host_2/db", "mongodb://<HOST_1>,<HOST_2>/db", []string{"host_2"}},
		{"host list, both members", "mongodb://user:pass@my_db:27017,my_db_2:27017/db",
			"mongodb://<TOKEN_1>@<HOST_1>:27017,<HOST_2>:27017/db", []string{"my_db", "_db"}},
		// 9b: its \b cannot start after a '_', so the label before the private suffix was sent.
		{"bare private host", "peer db_primary.corp.internal down", "peer <HOST_1> down",
			[]string{"db_primary", "corp.internal"}},
		{"bare private host, snake_case", "snake_case_host.corp.internal", "<HOST_1>",
			[]string{"snake_case_host", "corp.internal"}},
		// Leading and trailing '_' in a label are admitted: a DNS service label begins with one, and
		// refusing a trailing one buys nothing. The hyphen rule is unchanged.
		{"leading underscore label", "lookup _dmarc.corp.internal failed", "lookup <HOST_1> failed",
			[]string{"_dmarc"}},
		{"two leading-underscore labels", "srv _sip._tcp.corp.internal", "srv <HOST_1>", []string{"_sip", "_tcp"}},
		{"trailing underscore label", "peer db_.corp.internal down", "peer <HOST_1> down", []string{"db_"}},
		// 9a's group could not START on a '_', so this host was sent whole.
		{"url host starting with an underscore", "http://_svc.acme.com/x", "http://<HOST_1>/x",
			[]string{"_svc", "acme.com"}},
		// Detector 5's domain class: the email did not match at all and the whole address was sent.
		{"email with an underscore in the domain", "mail to user@my_host.example.com bounced",
			"mail to <EMAIL_1> bounced", []string{"user@", "my_host", "example.com"}},
		// The same rule is what reads an ssh-style user@host, so that was sent whole too.
		{"ssh-style user@host", "ssh session for deploy@build_01.example.com",
			"ssh session for <EMAIL_1>", []string{"deploy", "build_01"}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			got := mustMask(t, m, c.in)
			if got != c.want {
				t.Errorf("underscore host not masked as expected:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
			for _, frag := range c.sent {
				if strings.Contains(got, frag) {
					t.Errorf("a fragment the old output sent survived: %q still in %q", frag, got)
				}
			}
			if r := m.Restore(got); r != c.in {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", c.in, r)
			}
		})
	}

	// The Template field, through the real pipeline.Templatize: a digit beside a '_' templatizes
	// to <NUM> (my_host_2 -> my_host_<NUM>), and the tolerant host run must step over it.
	templated := []struct{ name, raw, want string }{
		{"single host", "http://my_host:8080/x", "http://<HOST_1>:<NUM>/x"},
		{"digit after the underscore", "http://my_host_2:8080/x", "http://<HOST_1>:<NUM>/x"},
		{"host-list member", "mongodb://host1,host_2/db", "mongodb://<HOST_1>,<HOST_2>/db"},
		{"bare private host", "peer db_primary01.corp.internal down", "peer <HOST_1> down"},
		{"email", "mail to ops@my_host7.example.com bounced", "mail to <EMAIL_1> bounced"},
	}
	for _, c := range templated {
		t.Run("template/"+c.name, func(t *testing.T) {
			in, _ := pipeline.Templatize(c.raw)
			m := New()
			if got := mustMask(t, m, in); got != c.want {
				t.Errorf("templatized underscore host not masked as expected:\n raw:  %q\n in:   %q\n got:  %q\n want: %q",
					c.raw, in, got, c.want)
			}
		})
	}

	// Byte-identical before and after.
	same := []struct{ name, in, want string }{
		// A space ends the token; 9b takes the host after it, as before.
		{"identifier, space, host", "THE_HOST_IS db.corp.internal", "THE_HOST_IS <HOST_1>"},
		// The one place widening would NARROW. A member in scheme position fails 9c's "://"
		// rejection, so the group shrinks to end at a terminator; '_' stays one, and "h2" stays
		// masked. Were '_' a host character there, no shorter member could end legally and h2 would
		// be sent. What follows the "://" was #52's class (the scheme anchor), and the one change to
		// this row since: the anchor recognises "x://" after the '_' now, so "y" is masked as well
		// (9a runs first, hence <HOST_2>) -- while h2 stays masked, which is what the row guards.
		{"member in scheme position", "mongodb://h1,h2_x://y", "mongodb://<HOST_1>,<HOST_3>_x://<HOST_2>"},
		// No scheme and no private suffix: prose, by design.
		{"no scheme, by design", "bootstrap=my_host:9092", "bootstrap=my_host:9092"},
		{"bare public host, by design", "could not reach my_host.example.com", "could not reach my_host.example.com"},
		{"snake_case identifier", "snake_case_var=1", "snake_case_var=1"},
		{"dotted identifier, public-looking suffix", "pkg_name.sub", "pkg_name.sub"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			if got := mustMask(t, New(), c.in); got != c.want {
				t.Errorf("a shape outside #66 moved:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		})
	}

	// Email is verify-eligible, so an address with a '_' in its domain that ever survives masking
	// now MUTES the escalation; before, the re-scan read it as clean.
	if tag := New().residue("user@my_host.example.com"); tag != tagEmail {
		t.Errorf("an email with an underscore in its domain is not verify-eligible: residue = %q, want %q", tag, tagEmail)
	}
}

// TestNoDetectorGroupLandsOnOurOwnOutput is the durable output of the #46 interference audit,
// mechanized so it does not have to be re-derived by hand.
//
// The audit's question was: for every detector spanning a COMPOSITE value, which earlier
// detectors can mint a placeholder inside that span, and what happens? The answer turns entirely
// on one property — every detector's TARGET GROUP excludes '<' and '>', so a placeholder inside
// a later detector's span truncates or blocks that detector. That property is what makes
// sequential masking safe, what makes the fail-closed re-scan sound, and what makes Restore's
// single non-fixpoint pass correct; it is also the reason #46 existed at all.
//
// TestPlaceholdersAreInert asserts it against hand-written strings. This asserts it against the
// real thing: the actual masked output of every completeness row, which is the only corpus
// TestIssue48UUIDInHostname is the named regression for #48, carrying the three shapes from the
// issue verbatim.
//
// Detector 6 ran before both host detectors, so a UUID used as a hostname LABEL minted a
// <UUID_n> inside their span. Every host class excludes '<' and '>' — that inertness is what
// makes sequential masking safe — so the host detector could not step over the tag and the domain
// around it went out in the clear. Both host detectors are verify:false, so residue() reported
// clean and the escalation was SENT rather than skipped.
//
// It is live on the RAW path specifically. On the Template field the pipeline has already
// collapsed the UUID to <UUID>, which pipelinePH names, so the tolerant host group steps over it;
// but Trigger.Raw, Trigger.Source and every context line reach Mask as raw text
// (internal/llm/anonymizing.go), and that is most of a payload.
//
// An IPv4 literal in a host label did exactly the same thing, and it could NOT be fixed the same
// way, because the two IPv4 cases want opposite orders — https://10.0.0.5/x needs detector 8 to
// win (the address IS the authority, and the IP tag is the product) while
// https://10.0.0.5.nip.io/x needs 9a to win (the address is a label INSIDE the authority). No
// ordering of a linear chain satisfies both, because the right answer depends on what the matched
// span is PART OF and the chain has no notion of that. That was filed as #54, with the
// impossibility carried as a result, and it sat here as an open assertion until it was closed by a
// rule rather than an order (TestIssue54IPv4LabelInHostname).
func TestIssue48UUIDInHostname(t *testing.T) {
	const uuid = "550e8400-e29b-41d4-a716-446655440000"

	closed := []struct {
		name string
		in   string
		// every fragment that must NOT survive, including the one the old behaviour sent
		leaks []string
	}{
		// 9a's group is required immediately after "://", so a tag in the FIRST label kills the
		// whole match. Nothing was behind it either: a public suffix is not on the bare-host
		// allowlist, so 9b never looks at it.
		{"leading label, public suffix",
			"upload failed to https://" + uuid + ".blob.core.windows.net/x",
			[]string{"blob.core.windows.net", ".blob", "windows", uuid}},
		// 9a's group takes the labels before the tag and stops, sending the tail.
		{"middle label, public suffix",
			"gateway 502 from https://api." + uuid + ".acme.com/",
			[]string{"acme.com", ".acme", "api.", uuid}},
		// 9b's \b re-anchors after the '>' and takes only the private suffix, sending every
		// label before the UUID.
		{"bare host, private suffix",
			"peer worker-" + uuid + ".corp.internal unreachable",
			[]string{"worker-", "corp.internal", uuid}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			masked := mustMask(t, m, c.in)
			for _, leak := range c.leaks {
				if strings.Contains(masked, leak) {
					t.Errorf("%q leaked:\n in:  %q\n out: %q", leak, c.in, masked)
				}
			}
			if got := m.Restore(masked); got != c.in {
				t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", c.in, got)
			}
		})
	}

	// An IPv4 label sat here as #54, asserted open, until it was closed; it is
	// TestIssue54IPv4LabelInHostname now.
}

// TestIssue54IPv4LabelInHostname is the named regression for #54, the half of #48 that ordering
// could not reach. Detector 8 ran before every host rule, so an address that was a LABEL inside a
// hostname was tagged <IP_n> on its own, and every host class excludes '<': the host rules were
// blocked or truncated and the rest of the name went to the configured model endpoint.
// https://10.0.0.5.nip.io/x sent ".nip.io", worker-10.0.0.5.corp.internal sent "worker-", and as a
// later member of a host list the mixed member ended the list and sent every member after it. Mask
// returned nil and residue() returned "": the host rules are verify:false, and the leftover is not
// a shape any of them matches. v0.4.0 onward.
//
// Running 9a first is not the answer, and that is the impossibility #48 recorded:
// https://10.0.0.5/x must keep coming back <IP_1>. The fix is a MORE SPECIFIC rule, not a new
// order: one that matches only an address joined to hostname labels across '.' or '-', and so
// never an address that stands alone. Every address 8 masked whole before, it still does.
//
// The Template field was NOT safe either. There the address is the pipeline's <IP>, and the
// tolerant host runs step over it in a URL or on a private suffix -- but a bare name on a public
// suffix is no host rule's business, so "resolve <IP>.nip.io failed" sent ".nip.io".
func TestIssue54IPv4LabelInHostname(t *testing.T) {
	closed := []struct {
		name, in, want, tmpl string
		sent                 []string // what main sent; none may survive in either output
	}{
		// The issue's shapes and the comment's later member.
		{"url, public suffix", "resolver miss for https://10.0.0.5.nip.io/x",
			"resolver miss for https://<HOST_1>/x", "resolver miss for https://<HOST_1>/x", []string{"nip.io", "10.0.0.5"}},
		{"bare, private suffix", "peer worker-10.0.0.5.corp.internal unreachable",
			"peer <HOST_1> unreachable", "peer <HOST_1> unreachable", []string{"worker-", "corp.internal"}},
		// 9d runs before 9a, so the fused member is HOST_1 and the members around it number after it.
		{"later member of a host list", "mongodb://h1,10.0.0.5.nip.io,host3/db",
			"mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db", "mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db",
			[]string{"nip.io", "host3", "h1"}},
		{"later member with ports", "mongodb://h1,10.0.0.5.nip.io:27017,host3:27017/db",
			"mongodb://<HOST_2>,<HOST_1>:27017,<HOST_3>:27017/db", "mongodb://<HOST_2>,<HOST_1>:<NUM>,<HOST_3>:<NUM>/db",
			[]string{"nip.io", "host3", "h1"}},
		{"first member of a host list", "mongodb://10.0.0.5.nip.io,host2/db",
			"mongodb://<HOST_1>,<HOST_2>/db", "mongodb://<HOST_1>,<HOST_2>/db", []string{"nip.io", "host2"}},
		// No scheme and a public suffix: on the Template field too, where no host rule reached it.
		{"bare, public suffix", "resolve 10.0.0.5.nip.io failed",
			"resolve <HOST_1> failed", "resolve <HOST_1> failed", []string{"nip.io"}},
		{"url with a port", "https://10.0.0.5.nip.io:8443/x",
			"https://<HOST_1>:8443/x", "https://<HOST_1>:<NUM>/x", []string{"nip.io"}},
		{"credentialed dsn", "postgres://u:p@10.0.0.5.nip.io/db",
			"postgres://<TOKEN_1>@<HOST_1>/db", "postgres://<TOKEN_1>@<HOST_1>/db", []string{"nip.io", "u:p"}},
		// The address as label 2: main masked "api." as a host and sent the rest.
		{"url, address as label 2", "https://api.10.0.0.5.nip.io/x",
			"https://<HOST_1>/x", "https://<HOST_1>/x", []string{"api", "nip.io"}},
		{"bare, address as label 2", "peer api.10.0.0.5.nip.io down",
			"peer <HOST_1> down", "peer <HOST_1> down", []string{"api", "nip.io"}},
		// The address last before a private suffix: 9b took the suffix and sent the label before.
		{"bare, address before a private suffix", "peer db.10.0.0.5.corp.internal down",
			"peer <HOST_1> down", "peer <HOST_1> down", []string{"db.", "corp.internal"}},
		{"url, address before a private suffix", "https://db.10.0.0.5.corp.internal/x",
			"https://<HOST_1>/x", "https://<HOST_1>/x", []string{"db.", "corp.internal"}},
		{"hyphen after the address", "https://10.0.0.5-lb.acme.com/x",
			"https://<HOST_1>/x", "https://<HOST_1>/x", []string{"lb.acme.com"}},
		{"two addresses in one name", "x 10.0.0.5.10.0.0.6.nip.io y",
			"x <HOST_1> y", "x <HOST_1> y", []string{"nip.io", "10.0.0.6"}},
		{"mixed case", "https://10.0.0.5.NIP.IO/x", "https://<HOST_1>/x", "https://<HOST_1>/x", []string{"NIP.IO"}},
		// A sentence dot after a fused name stays outside it, as it does after a bare address.
		{"sentence dot after the name", "reached api.10.0.0.5.nip.io.",
			"reached <HOST_1>.", "reached <HOST_1>.", []string{"api", "nip.io"}},
		// The same with text after the dot. At the end of the text a rule that ate the dot would tie
		// on length with one that leaves it as context; here eating it is longer, so this row is the
		// one that tells them apart.
		{"sentence dot, then a space", "reached api.10.0.0.5.nip.io. Retrying",
			"reached <HOST_1>. Retrying", "reached <HOST_1>. Retrying", []string{"api", "nip.io"}},
		// The address alone elsewhere on the line keeps its own tag and numbering.
		{"an address beside the name", "https://10.0.0.5.nip.io/x from 10.0.0.6",
			"https://<HOST_1>/x from <IP_1>", "https://<HOST_1>/x from <IP>", []string{"nip.io"}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				m := New()
				got := mustMask(t, m, p.in)
				if got != p.want {
					t.Errorf("address in a hostname not masked as part of it:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
				for _, frag := range c.sent {
					if strings.Contains(got, frag) {
						t.Errorf("a fragment main sent survived: %q still in %q", frag, got)
					}
				}
				if r := m.Restore(got); r != p.in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", p.in, r)
				}
			}
		})
	}

	// Byte-identical with main, raw and templatized. An address that IS the authority, or stands
	// alone in prose with a port, a prefix length or a sentence dot, is still 8's. So is one beside
	// text that is not a name: a run with no letter outside the address (a BSD netstat port, a
	// range) is not a DNS name, whose top label is alphabetic. A partial octet is no address, and
	// an address glued to a word with no separator is 9a's whole host, as before.
	same := []struct{ name, in, want, tmpl string }{
		{"the address is the authority", "https://10.0.0.5/x", "https://<IP_1>/x", "https://<HOST_1>/x"},
		{"address and port", "connect 10.0.0.5:8080 refused", "connect <IP_1>:8080 refused", "connect <IP>:<NUM> refused"},
		{"sentence dot", "connected to 10.0.0.5.", "connected to <IP_1>.", "connected to <IP>."},
		{"sentence dot, then a space", "connected to 10.0.0.5. Retrying", "connected to <IP_1>. Retrying", "connected to <IP>. Retrying"},
		{"prefix length", "route 10.0.0.5/24 added", "route <IP_1>/24 added", "route <IP>/<NUM> added"},
		{"address in prose", "peer 10.0.0.5 down", "peer <IP_1> down", "peer <IP> down"},
		{"address as a list member (#67)", "mongodb://h1,10.0.0.5,host3/db",
			"mongodb://<HOST_1>,<IP_1>,<HOST_2>/db", "mongodb://<HOST_1>,<HOST_2>,<HOST_3>/db"},
		{"bsd netstat address.port", "tcp 10.0.0.5.443 ESTABLISHED", "tcp <IP_1>.443 ESTABLISHED", "tcp <IP>.<NUM> ESTABLISHED"},
		{"range, numeric end", "scan 10.0.0.5-20 done", "scan <IP_1>-20 done", "scan <IP><NUM> done"},
		{"range of two addresses", "scan 10.0.0.5-10.0.0.9 done", "scan <IP_1>-<IP_2> done", "scan <IP>-<IP> done"},
		{"partial octet", "https://x10.0.0.5.nip.io/x", "https://<HOST_1>/x", "https://<HOST_1>/x"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("a shape outside #54 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}
	// A partial octet on the raw line: no address, so no rule reads it, exactly as on main. (The
	// pipeline does not validate octets, so on the Template field it is "worker-<IP>", which is
	// TestAcceptedOverMasking's "word glued before an address" row, masked whole now.)
	if got := mustMask(t, New(), "peer worker-10.0.0.256 down"); got != "peer worker-10.0.0.256 down" {
		t.Errorf("a partial octet moved: %q", got)
	}

	// The class's other inner tag, a key as a host label or a list member, sat here pinned at main's
	// output until #57 closed it; it is TestIssue57PartlyRecognisedValue now.

	// The name is ONE value: it recurs under one placeholder across the request, as a list member
	// and in a URL, and the address on its own is a different value with its own tag.
	t.Run("the name recurs under one placeholder", func(t *testing.T) {
		m := New()
		mustMask(t, m, "mongodb://h1,10.0.0.5.nip.io,host3/db")
		for _, c := range []struct{ in, want string }{
			{"dial https://10.0.0.5.nip.io/x failed", "dial https://<HOST_1>/x failed"},
			{"resolve 10.0.0.5.nip.io failed", "resolve <HOST_1> failed"},
			{"peer 10.0.0.5 down", "peer <IP_1> down"},
		} {
			if got := mustMask(t, m, c.in); got != c.want {
				t.Errorf("recurrence:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		}
		if got := m.Restore("<HOST_1>"); got != "10.0.0.5.nip.io" {
			t.Errorf("Restore(<HOST_1>) = %q, want the whole name", got)
		}
	})

	// residue() needs no change, and these two say why. The old leftover reads as clean, so the fix
	// lives in the chain; and a RAW address left inside a name -- the new rule missing it -- is still
	// caught by detector 8 and mutes the escalation, exactly as on main.
	if tag := New().residue("https://<IP_1>.nip.io/x"); tag != "" {
		t.Errorf("residue() sees the old leftover as %q; the claim in the comment above is wrong", tag)
	}
	if tag := New().residue("https://10.0.0.5.nip.io/x"); tag != tagIP {
		t.Errorf("a raw address inside a name no longer mutes: residue = %q, want %q", tag, tagIP)
	}
}

// TestIssue57PartlyRecognisedValue is the named regression for #57. Detectors 1, 2 and 3b run before
// every credential and host rule, so a key that is only PART of a value -- a userinfo half, a host
// list member, a hostname label -- was tagged on its own, mid-value. Every later group excludes '<',
// the inertness invariant, so no rule could reach the remainder, and it went to the configured model
// endpoint: s3://AKIA...EXAMPLE.prod:secret@bucket sent ".prod", and as a later list member the
// mixed member ended 9c's list and sent every member after it. Mask returned nil and residue()
// returned "": the remainder is not a shape any detector matches. v0.4.0 onward; the list member
// since v0.9.4, before which no later member was masked at all.
//
// #46's own fix did not cover it. 4b and 4c take the half the other half's key left behind; here
// the key and its remainder are ONE half, and the remainder may lie on either side of the tag.
//
// Moving 1, 2 and 3b later was rejected for #46 and stays rejected: a key that IS a half must keep
// its own tag. The fix is #54's lesson, a MORE SPECIFIC rule rather than an order: 4d matches only
// a userinfo with a key glued inside a half, 9e only a hostname with a key glued inside it, and both
// run before the key detectors, so a key that stands alone in its value is still theirs.
func TestIssue57PartlyRecognisedValue(t *testing.T) {
	const (
		aws = "AKIAIOSFODNN7EXAMPLE"
		sk  = "sk-abcdefghij0123456789XYZ"
		jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u"
	)
	closed := []struct {
		name, in, want, tmpl string
		sent                 []string // what main sent on one path or both; none may survive
	}{
		// The issue's shapes and the colon-less form, and #58's raw '@' behind the tag. The whole
		// userinfo is one TOKEN: a key glued to other text cannot be a half on its own, so the value
		// is detector 4's, read before 2 can tag the key.
		{"aws key, colon-less (#55's open row)", "s3://" + aws + ".prod@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{".prod", "IOSFODNN"}},
		{"aws key, with a colon (#55's open row)", "s3://" + aws + ".prod:secret@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{".prod", "secret"}},
		{"aws key, raw '@' behind it (#58's open row)", "s3://" + aws + ".prod@x@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{".prod", "@x"}},
		// The remainder BEFORE the key, and the password side, where the multi-colon and the
		// left-hand remainder were each sent while 4c took the username.
		{"remainder before the key", "s3://prod." + aws + "@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{"prod."}},
		{"remainder before the key, with a colon", "s3://prod." + aws + ":secret@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{"prod.", "secret"}},
		{"password side, after the key", "s3://user:" + aws + ".prod@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{".prod", "user"}},
		{"password side, before the key", "s3://user:prod." + aws + "@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{"prod.", "user"}},
		{"password side, a colon after the key", "s3://user:" + aws + ":extra@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{":extra"}},
		{"password side, a colon before the key", "s3://user:pa:" + aws + "@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{"pa:"}},
		// 4d runs before 9e: a raw '@' in front of a glued key is the userinfo's, not a host's.
		{"raw '@' in front of the key", "s3://user@" + aws + ".prod@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{".prod", "user@"}},
		// sk-. Its glue is any non-word character; on the template the mangled form carries no \b,
		// so a '_' is glue there too, and that row is the one path main sent it on.
		{"sk- key, colon-less", "s3://" + sk + ".prod@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{".prod"}},
		{"sk- key, with a colon", "s3://" + sk + ".prod:secret@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{".prod", "secret"}},
		{"sk- key, hyphen after", "s3://" + sk + "-prod@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{"-prod"}},
		{"sk- key, remainder before", "s3://prod." + sk + "@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{"prod."}},
		{"sk- key, underscore after (template only)", "s3://" + sk + "_prod@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{"_prod"}},
		// A JWT. '-' and '_' after it are base64url, absorbed into its third segment (pinned below);
		// a '.' or a '~' is not.
		{"jwt, dot after", "s3://" + jwt + ".prod@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{".prod"}},
		{"jwt, dot after, with a colon", "s3://" + jwt + ".prod:secret@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{".prod", "secret"}},
		{"jwt, tilde after", "s3://" + jwt + "~prod@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{"~prod"}},
		{"jwt, remainder before", "s3://prod." + jwt + "@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>", []string{"prod."}},
		// #61's "#57-shaped first member", where the email rule put a tag beside the key's and two
		// tags sat adjacent. The userinfo is one token now and the host list is ordinary.
		{"glued key before a host list (#61's row)", "s3://" + aws + ".prod@h1.acme.com,host2/db",
			"s3://<TOKEN_1>@<HOST_1>,<HOST_2>/db", "s<NUM>://<TOKEN_1>@<HOST_1>,<HOST_2>/db",
			[]string{".prod", "h1.acme.com", "host2"}},
		// The comment's cell, moved from #54: a key glued inside a LATER list member ended 9c's list.
		// The fused member is one HOST, minted by 9e before 9a, so it numbers first -- 9d's layout.
		{"later member, aws key (the comment's cell)", "mongodb://h1," + aws + ".prod,host3/db",
			"mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db", "mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db",
			[]string{".prod", "host3", "h1,"}},
		{"later member, sk- key", "mongodb://h1," + sk + ".prod,host3/db",
			"mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db", "mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db",
			[]string{".prod", "host3"}},
		{"later member, jwt", "mongodb://h1," + jwt + ".prod,host3/db",
			"mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db", "mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db",
			[]string{".prod", "host3"}},
		{"first member", "mongodb://" + aws + ".prod,host2/db",
			"mongodb://<HOST_1>,<HOST_2>/db", "mongodb://<HOST_1>,<HOST_2>/db", []string{".prod", "host2"}},
		// Two fused members side by side: 9e consumes nothing after its group, so the comma between
		// them is the second one's context, and each is its own match in one pass.
		{"two fused members adjacent", "mongodb://h1," + aws + ".a,AKIAIOSFODNN7EXAMPLF.b,host4/db",
			"mongodb://<HOST_3>,<HOST_1>,<HOST_2>,<HOST_4>/db", "mongodb://<HOST_3>,<HOST_1>,<HOST_2>,<HOST_4>/db",
			[]string{".a,", ".b,", "host4"}},
		// The host-label cell, #54's "theoretical" one: a key as a label sent the domain. HOST, and the
		// whole name is the value -- containment, as for #48's UUID and #54's address.
		{"host label, first", "https://" + aws + ".prod.acme.com/x",
			"https://<HOST_1>/x", "https://<HOST_1>/x", []string{"acme.com", ".prod"}},
		{"host label, middle", "https://api." + aws + ".acme.com/x",
			"https://<HOST_1>/x", "https://<HOST_1>/x", []string{"acme.com", "api."}},
		{"host label, with a port", "https://" + aws + ".prod.acme.com:8443/x",
			"https://<HOST_1>:8443/x", "https://<HOST_1>:<NUM>/x", []string{"acme.com"}},
		// A credential in front of a key-labelled host. 9e's HOST tag makes 4 decline (authorityTail
		// admits no '<'), so 4b and 4c take the halves as two tokens -- main's layout here too.
		{"host label behind a credential", "postgres://u:p@" + aws + ".prod.acme.com/db",
			"postgres://<TOKEN_2>:<TOKEN_1>@<HOST_1>/db", "postgres://<TOKEN_2>:<TOKEN_1>@<HOST_1>/db",
			[]string{"acme.com", "u:p"}},
		// The same without a password: no 4b/4c to fall back on, so 9a's fallback tags the username
		// HOST, exactly as it did on main. Inherited, not new; nothing is sent.
		{"host label behind a password-less userinfo", "postgres://appuser@" + aws + ".prod.acme.com/db",
			"postgres://<HOST_2>@<HOST_1>/db", "postgres://<HOST_2>@<HOST_1>/db", []string{"acme.com", "appuser"}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				m := New()
				got := mustMask(t, m, p.in)
				if got != p.want {
					t.Errorf("a key inside a larger value not masked with it:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
				for _, frag := range c.sent {
					if strings.Contains(got, frag) {
						t.Errorf("a fragment main sent survived: %q still in %q", frag, got)
					}
				}
				if r := m.Restore(got); r != p.in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", p.in, r)
				}
			}
		})
	}

	// Byte-identical with main, raw and templatized. A key that IS a whole half keeps its own tag --
	// that is what #46 ran the key rules first for, and the per-key exclusion after the key is what
	// keeps the mangled template form (AKIAIOSFODNN<NUM> before EXAMPLE) from reading as glued. A key
	// alone, behind Bearer, as a whole member or a whole host; a userinfo with no key; a JWT whose
	// '-' tail is base64url; a '_' before a key, which defeats \b so no key is seen and 4 masks the
	// whole userinfo; and a key glued to text in PROSE, whose remainder is not part of a value this
	// package protects -- outside a userinfo or an authority the text beside a key is prose.
	same := []struct{ name, in, want, tmpl string }{
		{"aws key is the whole username", "s3://" + aws + ":secret@bucket",
			"s3://<TOKEN_1>:<TOKEN_2>@<HOST_1>", "s<NUM>://<TOKEN_1>:<TOKEN_2>@<HOST_1>"},
		{"aws key is the whole password", "s3://myaccount:" + aws + "@bucket",
			"s3://<TOKEN_2>:<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_2>:<TOKEN_1>@<HOST_1>"},
		{"sk- key is the whole username", "postgres://sk-livekeyabcdefghij0123456789XYZ:hunter2@db",
			"postgres://<TOKEN_1>:<TOKEN_2>@<HOST_1>", "postgres://<TOKEN_1>:<TOKEN_2>@<HOST_1>"},
		{"jwt is the whole username", "postgres://" + jwt + ":hunter2@redis",
			"postgres://<TOKEN_1>:<TOKEN_2>@<HOST_1>", "postgres://<TOKEN_1>:<TOKEN_2>@<HOST_1>"},
		{"jwt with a hyphen tail", "s3://" + jwt + "-prod@bucket", "s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>"},
		{"underscore before the key", "s3://x_" + aws + ".prod@bucket", "s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>"},
		{"no key", "postgres://u:p@db.acme.com/prod", "postgres://<TOKEN_1>@<HOST_1>/prod", "postgres://<TOKEN_1>@<HOST_1>/prod"},
		{"key alone in prose", "key " + aws + " used", "key <TOKEN_1> used", "key <TOKEN_1> used"},
		{"key glued to text in prose", "key " + aws + ".prod used", "key <TOKEN_1>.prod used", "key <TOKEN_1>.prod used"},
		{"bearer key", "hdr Bearer " + aws + " expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"key is a whole member (#67)", "mongodb://h1," + aws + ",host3/db",
			"mongodb://<HOST_1>,<TOKEN_1>,<HOST_2>/db", "mongodb://<HOST_1>,<TOKEN_1>,<HOST_2>/db"},
		{"key is the whole host", "https://" + aws + "/x", "https://<TOKEN_1>/x", "https://<TOKEN_1>/x"},
		{"key is the whole host, with a port", "https://" + aws + ":8443/x", "https://<TOKEN_1>:8443/x", "https://<TOKEN_1>:<NUM>/x"},
		{"key in a url query", "https://x.com/q?k=" + aws + ".prod@b",
			"https://<HOST_1>/q?k=<TOKEN_1>.prod@b", "https://<HOST_1>/q?k=<TOKEN_1>.prod@b"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("a shape outside #57 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}

	// Pinned, not targeted: a key as a label of a BARE host on a private suffix. 9b masks the suffix
	// and the label before the key is sent. 9e reads a host only after "://", '@' or ',', and reaching
	// a bare name would mean reading 9b's configurable suffixes in a rule that runs before 1, 2 and
	// 3b. Theoretical, as #54's key-label cell was; kept so a change to it shows up as a diff.
	t.Run("key label of a bare private-suffix host (residual)", func(t *testing.T) {
		const in, want = "peer worker-" + aws + ".corp.internal down", "peer worker-<TOKEN_1>.<HOST_1> down"
		tIn, _ := pipeline.Templatize(in)
		for _, p := range []string{in, tIn} {
			if got := mustMask(t, New(), p); got != want {
				t.Errorf("a pinned shape moved:\n in:   %q\n got:  %q\n want: %q", p, got, want)
			}
		}
	})

	// The fused value is ONE value: it recurs under one placeholder across the request, and the key
	// on its own is a different value with its own token.
	t.Run("the fused value recurs under one placeholder", func(t *testing.T) {
		m := New()
		mustMask(t, m, "mongodb://h1,"+aws+".prod,host3/db")
		for _, c := range []struct{ in, want string }{
			{"dial https://" + aws + ".prod/x failed", "dial https://<HOST_1>/x failed"},
			{"key " + aws + " used", "key <TOKEN_1> used"},
		} {
			if got := mustMask(t, m, c.in); got != c.want {
				t.Errorf("recurrence:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		}
		if got := m.Restore("<HOST_1>"); got != aws+".prod" {
			t.Errorf("Restore(<HOST_1>) = %q, want the whole member", got)
		}
		u := New()
		mustMask(t, u, "s3://"+aws+".prod:secret@bucket")
		if got := u.Restore("<TOKEN_1>"); got != aws+".prod:secret" {
			t.Errorf("Restore(<TOKEN_1>) = %q, want the whole userinfo", got)
		}
	})

	// residue() needs no change, and these say why. A key left raw ANYWHERE still mutes the
	// escalation -- in a userinfo by 4d, as a host label or member by detector 2, in prose by 2 --
	// and the old leftovers still read as clean, so the fix lives in the chain.
	for _, in := range []string{
		"s3://" + aws + ".prod@bucket",
		"mongodb://h1," + aws + ".prod,host3/db",
		"https://" + aws + ".prod.acme.com/x",
		"key " + aws + " used",
	} {
		if tag := New().residue(in); tag != tagToken {
			t.Errorf("a raw key no longer mutes: residue(%q) = %q, want %q", in, tag, tagToken)
		}
	}
	for _, in := range []string{
		"s3://<TOKEN_1>.prod@<HOST_1>",
		"s3://<TOKEN_1>.prod:<TOKEN_2>@<HOST_1>",
		"mongodb://<HOST_1>,<TOKEN_1>.prod,host3/db",
	} {
		if tag := New().residue(in); tag != "" {
			t.Errorf("residue() sees the old leftover %q as %q; the claim in the comment above is wrong", in, tag)
		}
	}
}

// TestGluedKeyRulesReadTheKeyDetectors holds the glued-key rules to the key detectors' own patterns,
// and to their place in the chain. "A key" is one definition: the source of each of 1, 2 and 3b -- the
// three TOKEN rules with group 0 -- sits inside exactly the rules named for it below, and every one of
// them runs before every key detector, because a key the key detectors tagged first is a key no rule can
// see inside a larger value any more (#57). Two copies of a pattern are how #52 was missed.
//
// The readers are NAMED rather than counted, so a new one fails naming itself. 4d (a userinfo) and 9e (a
// host) read all three keys. 3c (a bearer value, #110) reads JWT's and AWS's only: 3b runs after 3a, so
// a bearer value with an sk- key inside it has always been 3a's whole.
//
// #114's vendor keys are the other way round: no reader at all. Each format family's key is the group of
// exactly one detector, a verify-eligible TOKEN rule with group 1 among the chain's last, one per family,
// and no other detector carries its shape. A vendor shape that joined keyShapes would put it inside 4d, 9e
// and rule 11, and a vendor rule moved to 3b's slot would block the rules that mask such a key whole.
func TestGluedKeyRulesReadTheKeyDetectors(t *testing.T) {
	want := map[string][]string{
		jwtKey: {"3c", "4d", "9e"},
		awsKey: {"3c", "4d", "9e"},
		skKey:  {"4d", "9e"},
	}
	keyName := map[string]string{jwtKey: "JWT", awsKey: "AWS", skKey: "sk-"}
	for _, dets := range [][]detector{baseDetectors, buildDetectors([]string{"example"})} {
		name := func(i int) string {
			d := dets[i]
			src := d.re.String()
			switch {
			case d.tag == tagToken && src == gluedUserinfo:
				return "4d"
			case d.tag == tagHost && src == gluedHost:
				return "9e"
			case d.tag == tagToken && strings.HasPrefix(src, `\b(?i:bearer)\s+(`):
				return "3c"
			}
			return fmt.Sprintf("detector %d (%s)", i, d.tag)
		}
		var keys []int
		for i, d := range dets {
			if d.tag == tagToken && d.group == 0 {
				keys = append(keys, i)
			}
		}
		if len(keys) != 3 {
			t.Fatalf("want three key detectors (1, 2, 3b), found %d", len(keys))
		}
		for _, k := range keys {
			src := dets[k].re.String()
			key, ok := keyName[src]
			if !ok {
				t.Errorf("key detector %d is none of jwtKey, awsKey and skKey:\n pattern: %s", k, src)
				continue
			}
			var readers []string
			for i, d := range dets {
				if i == k || !strings.Contains(d.re.String(), src) {
					continue
				}
				readers = append(readers, name(i))
				if i > keys[0] {
					t.Errorf("%s reads the %s key but runs after detector %d: the key would already be a tag", name(i), key, keys[0])
				}
			}
			sort.Strings(readers)
			if strings.Join(readers, ",") != strings.Join(want[src], ",") {
				t.Errorf("the %s key is read by %v, want %v", key, readers, want[src])
			}
		}

		vendor := []struct{ family, key string }{
			{"Stripe", stripeLiveSecret},
			{"GitHub classic", githubClassicSecret},
			{"GitHub fine-grained", githubPATSecret},
			{"Slack", slackBotSecret},
			{"Google", googleAPISecret},
		}
		owner := map[int]string{}
		for _, v := range vendor {
			in := "key " + v.key + " used"
			start, end := len("key "), len("key ")+len(v.key)
			var hits []int
			for i, d := range dets {
				if loc := d.re.FindStringSubmatchIndex(in); loc != nil && loc[2*d.group] == start && loc[2*d.group+1] == end {
					hits = append(hits, i)
				}
			}
			if len(hits) != 1 {
				t.Errorf("the %s key is the group of %d detectors, want exactly one", v.family, len(hits))
				continue
			}
			i, d := hits[0], dets[hits[0]]
			if d.tag != tagToken || d.group != 1 || !d.verify || d.fixpoint {
				t.Errorf("the %s key's rule is detector %d (%s, group %d, verify %v), want a verify-eligible TOKEN rule with group 1",
					v.family, i, d.tag, d.group, d.verify)
			}
			if i < len(dets)-len(vendor) {
				t.Errorf("the %s key's rule is detector %d, want one of the last %d: a rule that runs earlier blocks the rules that mask such a key whole", v.family, i, len(vendor))
			}
			if o, dup := owner[i]; dup {
				t.Errorf("the %s and %s keys share detector %d, want one rule per family", o, v.family, i)
			}
			owner[i] = v.family
			src := d.re.String()
			at := strings.Index(src, "])(")
			if at < 0 || !strings.HasSuffix(src, ")") {
				t.Errorf("the %s key's rule is not a loose key, a context then the key as group 1:\n pattern: %s", v.family, src)
				continue
			}
			shape := src[at+len("])(") : len(src)-1]
			for j, o := range dets {
				if j != i && strings.Contains(o.re.String(), shape) {
					t.Errorf("%s reads the %s key, which is rule 12's alone", name(j), v.family)
				}
			}
		}
	}
}

// TestIssue108KeyGluedToAWordCharacter is the named regression for #108. Detectors 1, 2 and 3b find a
// key between two \b's, and '_' and letters are word characters, so a key glued to one was not a key at
// all on the raw line: "key AKIA...EXAMPLE_prod used" and "key sk-..._prod used" went to the configured
// model endpoint with THE KEY in them, and residue() returned "". On the Template field the mangled form
// carries no closing \b, so there the key was masked and only "_prod" was sent: the two paths disagreed.
// A '_' or a lowercase letter BEFORE the key was sent whole on both paths, and so was a JWT after a '_'.
// v0.4.0 onward.
//
// Teaching 1, 2 and 3b the new edges was rejected, measured on paper and pinned below: a key those
// detectors tag first blocks every rule after them, and main masks three shapes whole only because
// they do NOT see the key -- a bearer token (3a), an email local part (5) and a bare private-suffix host
// (9b). The fix runs LAST instead, after every rule that could own a value around the key, so it only
// ever reads text no rule claimed, and there the text beside a key is prose (#57's decision): the key
// is masked and the glue stays.
func TestIssue108KeyGluedToAWordCharacter(t *testing.T) {
	const (
		aws = "AKIAIOSFODNN7EXAMPLE"
		sk  = "sk-abcdefghij0123456789XYZ"
		jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u"
	)
	closed := []struct {
		name, in, want, tmpl string
		sent                 []string // what main sent on one path or both; none may survive
	}{
		// The issue's three shapes. The template already masked the key and sent the glue, so the two
		// paths agree now, on the template's output.
		{"aws key, '_' after (the issue's)", "key " + aws + "_prod used",
			"key <TOKEN_1>_prod used", "key <TOKEN_1>_prod used", []string{"IOSFODNN"}},
		{"aws key, letter after (the issue's)", "key " + aws + "x",
			"key <TOKEN_1>x", "key <TOKEN_1>x", []string{"IOSFODNN"}},
		// Since #51 '_' and '-' are sk key characters, so the glue after an sk- key is part of it and the
		// whole run is one token; an AWS key's alphabet still ends at '_', and its glue stays prose.
		{"sk- key, '_' after (the issue's)", "key " + sk + "_prod used",
			"key <TOKEN_1> used", "key <TOKEN_1> used", []string{"abcdefghij", "_prod"}},
		{"asia key, '_' after", "key ASIAIOSFODNN7EXAMPLE_prod used",
			"key <TOKEN_1>_prod used", "key <TOKEN_1>_prod used", []string{"IOSFODNN"}},
		// Glue BEFORE the key, sent whole on both paths. For an AWS key a lowercase letter is glue too:
		// its alphabet is [0-9A-Z], so a lowercase letter cannot be part of its run. For sk- and a JWT
		// only '_' is; a letter or a digit before them is a longer word (pinned below).
		{"aws key, '_' before", "key x_" + aws + " used",
			"key x_<TOKEN_1> used", "key x_<TOKEN_1> used", []string{"IOSFODNN"}},
		{"aws key, lowercase before", "key id" + aws + " used",
			"key id<TOKEN_1> used", "key id<TOKEN_1> used", []string{"IOSFODNN"}},
		{"sk- key, '_' before", "key my_" + sk + " used",
			"key my_<TOKEN_1> used", "key my_<TOKEN_1> used", []string{"abcdefghij"}},
		{"jwt, '_' before", "auth token_" + jwt + " rejected",
			"auth token_<TOKEN_1> rejected", "auth token_<TOKEN_1> rejected", []string{"dBjftJeZ", "eyJhbGci"}},
		{"aws key, glued on both sides", "key x_" + aws + "_prod used",
			"key x_<TOKEN_1>_prod used", "key x_<TOKEN_1>_prod used", []string{"IOSFODNN"}},
		{"sk- key, glued on both sides (the '_' after is the key's since #51)", "key my_" + sk + "_prod used",
			"key my_<TOKEN_1> used", "key my_<TOKEN_1> used", []string{"abcdefghij", "_prod"}},
		{"end of text after '_'", "creds=prod_" + aws,
			"creds=prod_<TOKEN_1>", "creds=prod_<TOKEN_1>", []string{"IOSFODNN"}},
		{"start of text", aws + "_prod used", "<TOKEN_1>_prod used", "<TOKEN_1>_prod used", []string{"IOSFODNN"}},
		// A run that continues the key's alphabet past its length is masked whole: it starts with a key
		// prefix and sixteen key characters, and masking a token beats sending a key. The template's
		// mangled form was always unbounded, so this is the raw line coming to agree with it.
		{"a longer run (decision 2)", "key AKIAIOSFODNN7EXAMPLEPROD used",
			"key <TOKEN_1> used", "key <TOKEN_1> used", []string{"IOSFODNN", "PROD"}},
		// The rule consumes one character of context before a key, so this row is the one that shows a
		// second key after the first is not starved of it. Main's template masked the first key only.
		{"two keys glued by one '_'", aws + "_AKIAIOSFODNN7EXAMPLF",
			"<TOKEN_1>_<TOKEN_2>", "<TOKEN_1>_<TOKEN_2>", []string{"IOSFODNN"}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				m := New()
				got := mustMask(t, m, p.in)
				if got != p.want {
					t.Errorf("a key glued to a word character not masked:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
				for _, frag := range c.sent {
					if strings.Contains(got, frag) {
						t.Errorf("a fragment main sent survived: %q still in %q", frag, got)
					}
				}
				if r := m.Restore(got); r != p.in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", p.in, r)
				}
			}
		})
	}

	// Byte-identical with main, raw and templatized.
	//
	// What a key's grammar refuses: a letter or a digit before sk- or a JWT, and an uppercase letter or
	// a digit before an AWS prefix, make a longer run that merely CONTAINS the prefix, which is no key.
	// The template masks the digit-before rows by an accident of NUM: the digit becomes <NUM>, and \b
	// holds after its '>'. An sk-proj key sat here at main's output, sent on both paths, until #51 closed
	// it; it is TestIssue51SkKeyWithASeparatorInItsTail's now.
	//
	// And the shapes main masks WHOLE because the key detectors do not see a glued key there: a bearer
	// token, an email local part, a bare private-suffix host, a userinfo, a host label, a list member.
	// Any rule that tagged the key before the rule owning the value would block that rule and send the
	// rest; this one runs after all of them. Two bearer rows sat here at main's output, ".prod" sent on
	// both paths and "_prod" on the template, a remainder beside a masked key rather than a key, until #110
	// closed them; they are TestIssue110KeyGluedInsideABearerValue's now.
	same := []struct{ name, in, want, tmpl string }{
		{"key alone", "key " + aws + " used", "key <TOKEN_1> used", "key <TOKEN_1> used"},
		{"aws key is the whole username", "s3://" + aws + ":secret@bucket",
			"s3://<TOKEN_1>:<TOKEN_2>@<HOST_1>", "s<NUM>://<TOKEN_1>:<TOKEN_2>@<HOST_1>"},
		{"bearer key", "hdr Bearer " + aws + " expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"jwt, '_' after (base64url)", "auth " + jwt + "_x rejected", "auth <TOKEN_1> rejected", "auth <TOKEN_1> rejected"},
		{"a word that looks like sk- (task)", "task-abcdefghijklmnop0123", "task-abcdefghijklmnop0123", "task-abcdefghijklmnop<NUM>"},
		{"a word that looks like sk- (ask)", "ask-abcdefghijklmnop0123", "ask-abcdefghijklmnop0123", "ask-abcdefghijklmnop<NUM>"},
		{"digit before sk-", "key 2" + sk + " used", "key 2" + sk + " used", "key <NUM><TOKEN_1> used"},
		{"uppercase before an aws prefix", "key X" + aws + " used", "key X" + aws + " used", "key XAKIAIOSFODNN<NUM>EXAMPLE used"},
		{"digit before an aws prefix", "key 7" + aws + " used", "key 7" + aws + " used", "key <NUM><TOKEN_1> used"},
		{"letter before a jwt", "auth x" + jwt + " rejected", "auth x" + jwt + " rejected",
			"auth xeyJhbGciOiJIUzI<NUM>NiJ<NUM>.eyJzdWIiOiIxIn<NUM>.dBjftJeZ<NUM>CVPmB<NUM>K<NUM>u rejected"},
		{"bearer, '_' before the key", "hdr Bearer x_" + aws + " expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"email, '_' before the key", "mail x_" + aws + "@example.com", "mail <EMAIL_1>", "mail <EMAIL_1>"},
		{"email, '_' after the key", "mail " + aws + "_prod@example.com", "mail <EMAIL_1>", "mail <TOKEN_1><EMAIL_1>"},
		{"bare private host, '_' before the key", "peer worker_" + aws + ".corp.internal down",
			"peer <HOST_1> down", "peer <HOST_1> down"},
		{"host label, '_' after the key", "https://" + aws + "_prod.acme.com/x", "https://<HOST_1>/x", "https://<HOST_1>/x"},
		{"list member, '_' after the key", "mongodb://h1," + aws + "_prod,host3/db",
			"mongodb://<HOST_1>,<HOST_2>,<HOST_3>/db", "mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db"},
		{"userinfo, '_' after the key", "s3://" + aws + "_prod:secret@bucket",
			"s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("a shape outside #108 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}

	// The key glued and the key alone are one value, under one placeholder.
	t.Run("a glued key recurs under the key's placeholder", func(t *testing.T) {
		m := New()
		if got, want := mustMask(t, m, "key x_"+aws+" used"), "key x_<TOKEN_1> used"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if got, want := mustMask(t, m, "creds "+aws+" denied"), "creds <TOKEN_1> denied"; got != want {
			t.Errorf("recurrence: got %q, want %q", got, want)
		}
		if got := m.Restore("<TOKEN_1>"); got != aws {
			t.Errorf("Restore(<TOKEN_1>) = %q, want the key alone", got)
		}
	})

	// residue() reads the same rule, so a raw glued key ANYWHERE mutes the escalation -- in prose, in a
	// query string, behind Bearer, in a userinfo -- and the old leftovers read as clean.
	for _, in := range []string{
		"key " + aws + "_prod used",
		"key " + aws + "x",
		"key " + sk + "_prod used",
		"key x_" + aws + " used",
		"key id" + aws + " used",
		"auth token_" + jwt + " rejected",
		"key x_" + aws + "_prod used",
		"key AKIAIOSFODNN7EXAMPLEPROD used",
		"https://x.com/q?k=" + aws + "_prod",
		"hdr Bearer " + aws + "_prod expired",
		"s3://" + aws + "_prod@bucket",
	} {
		if tag := New().residue(in); tag != tagToken {
			t.Errorf("a raw glued key does not mute: residue(%q) = %q, want %q", in, tag, tagToken)
		}
	}
	for _, in := range []string{
		"key <TOKEN_1>_prod used",
		"key x_<TOKEN_1> used",
		"auth token_<TOKEN_1> rejected",
	} {
		if tag := New().residue(in); tag != "" {
			t.Errorf("residue() sees the leftover %q as %q; the claim in the comment above is wrong", in, tag)
		}
	}
}

// TestIssue110KeyGluedInsideABearerValue is the named regression for #110, a remainder beside a masked
// key -- #57's class, not the key itself. 3a masks the whole value after "bearer" as one TOKEN, but it
// runs AFTER 1 (JWT) and 2 (AWS), so a key that was only PART of a bearer value was tagged on its own,
// and 3a's group, which excludes '<', could not claim what was left: "hdr Bearer AKIA...EXAMPLE.prod
// expired" sent ".prod" on both paths, and on the template, where the mangled key has no closing \b, a
// '_' or a lowercase letter after it was sent too. sk- was never affected: 3b runs after 3a. residue()
// read every leftover as clean. v0.4.0 onward.
//
// Running 3a before 1 and 2 closes the same cells and was rejected, measured: it mints every bearer
// value first, so a line with a bearer token and a key elsewhere swaps its placeholder numbers (the
// mixed rows below). The fix is #57's remedy, a MORE SPECIFIC rule run before the key detectors: 3c
// matches only a bearer value with a key glued inside it, and masks the value whole.
func TestIssue110KeyGluedInsideABearerValue(t *testing.T) {
	const (
		aws = "AKIAIOSFODNN7EXAMPLE"
		sk  = "sk-abcdefghij0123456789XYZ"
		jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u"
	)
	closed := []struct {
		name, in, want, tmpl string
		sent                 []string // what main sent on one path or both; none may survive
	}{
		// Text glued after an AWS key, sent on both paths. The two rows #108 pinned at main's output are
		// the first two here.
		{"aws key, '.' after", "hdr Bearer " + aws + ".prod expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired", []string{".prod"}},
		{"aws key, '_' after (template only)", "hdr Bearer " + aws + "_prod expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired", []string{"_prod"}},
		{"aws key, '~' after", "hdr Bearer " + aws + "~prod expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired", []string{"~prod"}},
		{"aws key, '-' after", "hdr Bearer " + aws + "-prod expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired", []string{"-prod"}},
		{"asia key, '.' after", "hdr Bearer ASIAIOSFODNN7EXAMPLE.prod expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired", []string{".prod"}},
		// A lowercase letter cannot continue an AWS key, so on the template, where the mangled key has no
		// closing \b, it was glue and was sent. On the raw line 2 saw no key and 3a took the value whole.
		{"aws key, letter after (template only)", "hdr Bearer " + aws + "xyz expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired", []string{"xyz"}},
		// A JWT: '-' and '_' are base64url and run on into its third segment; '.' and '~' are glue.
		{"jwt, '.' after", "hdr Bearer " + jwt + ".prod expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired", []string{".prod"}},
		{"jwt, '~' after", "hdr Bearer " + jwt + "~prod expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired", []string{"~prod"}},
		// The word, as 3a reads it: after a header name, and in any case.
		{"authorization header", "hdr Authorization: Bearer " + aws + ".prod expired",
			"hdr Authorization: Bearer <TOKEN_1> expired", "hdr Authorization: Bearer <TOKEN_1> expired", []string{".prod"}},
		{"lowercase bearer", "hdr bearer " + aws + ".prod expired",
			"hdr bearer <TOKEN_1> expired", "hdr bearer <TOKEN_1> expired", []string{".prod"}},
		{"uppercase bearer", "hdr BEARER " + aws + ".prod expired",
			"hdr BEARER <TOKEN_1> expired", "hdr BEARER <TOKEN_1> expired", []string{".prod"}},
		// Text glued BEFORE the key. On its own main sent nothing here -- 3a took "prod." as a token of its
		// own beside the key's, <TOKEN_2><TOKEN_1> -- so this row is layout; with text after the key as
		// well, that text was sent.
		{"text before the key", "hdr Bearer prod." + aws + " expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired", nil},
		{"text on both sides", "hdr Bearer prod." + aws + ".x expired",
			"hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired", []string{".x"}},
		// 3a reads "bearer" in prose as well, and so does 3c.
		{"jwt after bearer in prose", "the bearer " + jwt + ".prod was rejected",
			"the bearer <TOKEN_1> was rejected", "the bearer <TOKEN_1> was rejected", []string{".prod"}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				m := New()
				got := mustMask(t, m, p.in)
				if got != p.want {
					t.Errorf("a bearer value with a key glued inside it not masked whole:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
				for _, frag := range c.sent {
					if strings.Contains(got, frag) {
						t.Errorf("a fragment main sent survived: %q still in %q", frag, got)
					}
				}
				if r := m.Restore(got); r != p.in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", p.in, r)
				}
			}
		})
	}

	// Byte-identical with main, raw and templatized. A key that IS the whole bearer value is still 1's or
	// 2's; an sk- key, glued or not, is still 3a's whole; a bearer value with no key, or one where 2 sees no
	// key ('_' before it), is 3a's. "Bearer" without whitespace after it is no bearer value to 3a or 3c --
	// in a query string, as a username -- and #52's "Bearer https://..." is pinned in its own test.
	//
	// The mixed rows are what decided the mechanism: a bearer value beside a key elsewhere on the line.
	// Running 3a before 1 and 2 would mint the bearer value first and swap the numbers in each. The last
	// one also holds the case-fold to the word: a lowercase "akia..." is no key to 2, so it is 3a's.
	same := []struct{ name, in, want, tmpl string }{
		{"aws key is the whole value", "hdr Bearer " + aws + " expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"jwt is the whole value", "hdr Bearer " + jwt + " expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"sk- key is the whole value", "hdr Bearer " + sk + " expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"sk- key, '.' after", "hdr Bearer " + sk + ".prod expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"no key", "hdr Bearer abc123DEF456ghi789JKL expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"'_' before the key", "hdr Bearer x_" + aws + " expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"in a query string", "GET /x?h=Bearer+" + aws + ".prod", "GET /x?h=Bearer+<TOKEN_1>.prod", "GET /x?h=Bearer+<TOKEN_1>.prod"},
		{"as a username", "s3://bearer:" + aws + ".prod@bucket", "s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>"},
		{"jwt after bearer in prose", "the bearer " + jwt + " was rejected", "the bearer <TOKEN_1> was rejected", "the bearer <TOKEN_1> was rejected"},
		{"mixed: a bearer token, a key elsewhere", "hdr Bearer abc123DEF456ghi789JKL key " + aws + " used",
			"hdr Bearer <TOKEN_2> key <TOKEN_1> used", "hdr Bearer <TOKEN_2> key <TOKEN_1> used"},
		{"mixed: a glued sk- bearer value, a key elsewhere", "key " + aws + " used, hdr Bearer " + sk + ".prod",
			"key <TOKEN_1> used, hdr Bearer <TOKEN_2>", "key <TOKEN_1> used, hdr Bearer <TOKEN_2>"},
		{"mixed: a lowercase akia bearer value, a key elsewhere", "key " + aws + " used, hdr bearer akiaiosfodnn7example.prod",
			"key <TOKEN_1> used, hdr bearer <TOKEN_2>", "key <TOKEN_1> used, hdr bearer <TOKEN_2>"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("a shape outside #110 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}

	// The bearer value is ONE value: Restore gives it back whole, and the key on its own elsewhere is a
	// different value with its own token, as for #57's userinfo.
	t.Run("the bearer value is one value", func(t *testing.T) {
		m := New()
		if got, want := mustMask(t, m, "hdr Bearer "+aws+".prod expired"), "hdr Bearer <TOKEN_1> expired"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if got, want := mustMask(t, m, "key "+aws+" used"), "key <TOKEN_2> used"; got != want {
			t.Errorf("the key alone: got %q, want %q", got, want)
		}
		if got := m.Restore("<TOKEN_1>"); got != aws+".prod" {
			t.Errorf("Restore(<TOKEN_1>) = %q, want the whole bearer value", got)
		}
	})

	// residue(): a raw key left in a bearer value mutes, raw and in its template form, and the old
	// leftovers read as clean, so the fix lives in the chain.
	for _, in := range []string{
		"hdr Bearer " + aws + ".prod expired",
		"hdr Bearer AKIAIOSFODNN<NUM>EXAMPLE_prod expired",
		"hdr Bearer " + jwt + "~prod expired",
	} {
		if tag := New().residue(in); tag != tagToken {
			t.Errorf("a raw key in a bearer value does not mute: residue(%q) = %q, want %q", in, tag, tagToken)
		}
	}
	for _, in := range []string{
		"hdr Bearer <TOKEN_1>.prod expired",
		"hdr Bearer <TOKEN_1>_prod expired",
		"hdr Bearer <TOKEN_1>~prod expired",
		"hdr Bearer <TOKEN_2><TOKEN_1>.x expired",
	} {
		if tag := New().residue(in); tag != "" {
			t.Errorf("residue() sees the old leftover %q as %q; the claim in the comment above is wrong", in, tag)
		}
	}
}

// #51's invented keys, shared by its test and the tables. Each is written split at its vendor prefix so
// that no source line carries a whole key-shaped literal for a secret scanner to match; the strings
// are the same at run time.
const (
	skProjKey         = "sk-proj-" + "Rk4mQz8vLp2w_Xn6tYb9cHd3s-Jf7gKa5eTu1o"
	skSvcacctKey      = "sk-svcacct-" + "Tq5wEr8yUi2oPa6sDf9gHj3kLz7xCv4b"
	skAdminKey        = "sk-admin-" + "Mn8bVc6xZa4sDf2gHj0kLqWe5r"
	skAntKey          = "sk-ant-" + "api03-" + "Xy7zWv5uTs3rQp1oNm9lKj_hGf-Ed2cBa8ZyQw4eRt6y-Ui9oPa1sDfAA"
	skUnderscoreKey   = "sk-" + "AbCdEfGh_IjKlMnOpQrStUv"
	skDigitFirstKey   = "sk-" + "7AbCdEfGhIjKlMnOpQrSt"
	skDigitsInsideKey = "sk-" + "4Fh8KdLm2Qp9RsTv7Wx1Yz5"
	skHexKey          = "sk-or-" + "v1-" + "3f9a2c7e1b8d4f60a5e9c2b7d1f8a3e6c4b9d2f7a1e8c5b3d6f9a2e7c1b4d8f0"
)

// TestIssue51SkKeyWithASeparatorInItsTail is the named regression for #51. Detector 3b wanted sixteen
// unbroken letters and digits straight after "sk-", and every current key format puts a '-' or a '_' in
// its tail before that: OpenAI's sk-proj-, sk-svcacct- and sk-admin-, Anthropic's sk-ant-api03-, and a
// classic key with a '_' in it. Each was sent whole, raw and on the template, and residue() read the
// line as clean. On the template a classic key whose tail starts with a digit was sent too: NUM takes
// the '-' as a sign, "sk-7AbC..." becomes "sk<NUM>AbC...", and no "sk-" was left to anchor on.
//
// The tail is now a run of the key's alphabet, letters, digits, '-' and '_', carrying a witness: sixteen
// unbroken letters and digits anywhere in it, or an uppercase letter with the whole tail sixteen long. A
// kebab-case name that starts with a literal "sk-" word is the same alphabet with neither. On the template
// a pipeline placeholder is the evidence, as it always was, and after "sk<NUM>" a letter of each case.
//
// A '-' or '_' at the very end is the key's; a closer is not. Text glued AFTER an sk- key by '-' or '_' is
// the key's too, since it is the key's alphabet, which moves #108's sk rows. v0.4.0 onward.
func TestIssue51SkKeyWithASeparatorInItsTail(t *testing.T) {
	const (
		proj    = skProjKey
		svcacct = skSvcacctKey
		admin   = skAdminKey
		ant     = skAntKey
		under   = skUnderscoreKey
		digit   = skDigitFirstKey
		digit2  = skDigitsInsideKey
		hexv    = skHexKey
	)
	closed := []struct {
		name, in, want, tmpl string
		sent                 []string // what main sent on one path or both; none may survive
	}{
		// The issue's formats, in prose. The template is TemplatizeLine's, the real Template field.
		{"sk-proj (the issue's)", "key " + proj + " rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected",
			[]string{"sk-proj", "vLp", "gKa"}},
		{"sk-svcacct", "key " + svcacct + " rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected",
			[]string{"sk-svcacct", "wEr", "kLz"}},
		{"sk-admin", "key " + admin + " rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected",
			[]string{"sk-admin", "LqWe"}},
		{"anthropic sk-ant-api03", "key " + ant + " rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected",
			[]string{"sk-ant", "lKj_hGf", "sDfAA"}},
		{"classic, '_' inside", "key " + under + " rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected",
			[]string{"IjKlMn"}},
		// The hex vendor form carries the sixteen-run after its prefix. On the template its body is already
		// <HEX> and nothing of the key is left to send, so the template is main's.
		{"hex body after a vendor prefix", "key " + hexv + " rejected", "key <TOKEN_1> rejected",
			"key sk-or-v<NUM>-<HEX> rejected", []string{"3f9a2c7e"}},
		// The two witnesses at their floors: sixteen unbroken after a separator, and an uppercase tail of
		// exactly sixteen. One short of each is a must-stay below.
		{"a sixteen-run after a separator", "key sk-x-" + "abcdefghijklmnop used", "key <TOKEN_1> used",
			"key <TOKEN_1> used", []string{"abcdefghijklmnop"}},
		{"an uppercase tail of sixteen", "key sk-" + "AbCd-EfGh_IjKlmn used", "key <TOKEN_1> used",
			"key <TOKEN_1> used", []string{"EfGh"}},
		// A classic key whose tail starts with a digit. The raw line always masked it; the template, where
		// it is sk<NUM>, sent the rest of it.
		{"digit-first classic (template)", "key " + digit + " rejected", "key <TOKEN_1> rejected",
			"key <TOKEN_1> rejected", []string{"AbCdEf"}},
		{"digit-first classic, digits inside (template)", "key " + digit2 + " rejected", "key <TOKEN_1> rejected",
			"key <TOKEN_1> rejected", []string{"KdLm", "RsTv"}},
		{"digit-first, lowercase sixteen-run (template)", "key sk-" + "7abcdefghijklmnopq rejected",
			"key <TOKEN_1> rejected", "key <TOKEN_1> rejected", []string{"abcdefghijklmnop"}},
		{"digit-first, logfmt msg", `level=error msg="key ` + digit + ` rejected" module=x`,
			`level=error msg="key <TOKEN_1> rejected" module=x`, `level=error msg="key <TOKEN_1> rejected" module=x`,
			[]string{"AbCdEf"}},
		{"digit-first, json msg", `{"level":"error","msg":"key ` + digit + ` rejected"}`,
			`{"level":"error","msg":"key <TOKEN_1> rejected"}`, `{"level":<STR>,"msg":"key <TOKEN_1> rejected"}`,
			[]string{"AbCdEf"}},
		// Environment variables, at the end of the text.
		{"OPENAI_API_KEY (the issue's)", "OPENAI_API_KEY=" + proj, "OPENAI_API_KEY=<TOKEN_1>",
			"OPENAI_API_KEY=<TOKEN_1>", []string{"vLp"}},
		{"ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY=" + ant, "ANTHROPIC_API_KEY=<TOKEN_1>",
			"ANTHROPIC_API_KEY=<TOKEN_1>", []string{"sDfAA"}},
		// The structured wrappers: a logfmt msg and a JSON msg keep their text, re-quoted, and a JSON value
		// is <STR> on the template.
		{"logfmt msg", `level=error msg="key ` + proj + ` rejected" module=x`,
			`level=error msg="key <TOKEN_1> rejected" module=x`, `level=error msg="key <TOKEN_1> rejected" module=x`,
			[]string{"vLp"}},
		{"json msg", `{"level":"error","msg":"key ` + proj + ` rejected"}`,
			`{"level":"error","msg":"key <TOKEN_1> rejected"}`, `{"level":<STR>,"msg":"key <TOKEN_1> rejected"}`,
			[]string{"vLp"}},
		{"json msg, anthropic", `{"level":"error","msg":"key ` + ant + ` rejected"}`,
			`{"level":"error","msg":"key <TOKEN_1> rejected"}`, `{"level":<STR>,"msg":"key <TOKEN_1> rejected"}`,
			[]string{"sDfAA"}},
		{"json value", `{"api_key":"` + proj + `"}`, `{"api_key":"<TOKEN_1>"}`, `{"api_key":<STR>}`, []string{"vLp"}},
		// The edges: a closer stays outside the placeholder (#90's class), one row each.
		{"sentence dot", "key " + proj + ". Retrying", "key <TOKEN_1>. Retrying", "key <TOKEN_1>. Retrying", []string{"vLp"}},
		{"comma", "key " + proj + ", retrying", "key <TOKEN_1>, retrying", "key <TOKEN_1>, retrying", []string{"vLp"}},
		{"double quote", `rejected "` + proj + `"`, `rejected "<TOKEN_1>"`, `rejected <STR>`, []string{"vLp"}},
		{"single quote", "rejected '" + proj + "'", "rejected '<TOKEN_1>'", "rejected <STR>", []string{"vLp"}},
		{"paren", "(" + proj + ")", "(<TOKEN_1>)", "(<TOKEN_1>)", []string{"vLp"}},
		{"bracket", "[" + proj + "]", "[<TOKEN_1>]", "[<TOKEN_1>]", []string{"vLp"}},
		{"brace", "{" + proj + "}", "{<TOKEN_1>}", "{<TOKEN_1>}", []string{"vLp"}},
		{"colon", proj + ": rejected", "<TOKEN_1>: rejected", "<TOKEN_1>: rejected", []string{"vLp"}},
		// ...and a '-' or a '_' at the very end is the key's: a base64url body can end on either.
		{"trailing '-' is the key's", "key " + proj + "- used", "key <TOKEN_1> used", "key <TOKEN_1> used", []string{"vLp", "o-"}},
		{"trailing '_' is the key's", "key " + proj + "_ used", "key <TOKEN_1> used", "key <TOKEN_1> used", []string{"vLp", "o_"}},
		// A '_' after a lowercase classic key is the key's on the template too, where the placeholder in its
		// first segment is the evidence and the run goes on across the separator.
		{"lowercase classic, '_' after", "key sk-" + "abcdefghij0123456789xyz_prod used", "key <TOKEN_1> used",
			"key <TOKEN_1> used", []string{"abcdefghij", "_prod"}},
		// Rule 11 reads the same key: glued to '_' before it, the glue stays prose.
		{"rule 11, '_' before", "key x_" + proj + " used", "key x_<TOKEN_1> used", "key x_<TOKEN_1> used", []string{"vLp"}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: c.in}))
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				m := New()
				got := mustMask(t, m, p.in)
				if got != p.want {
					t.Errorf("an sk- key with a separator in its tail not masked:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
				for _, frag := range c.sent {
					if strings.Contains(got, frag) {
						t.Errorf("a fragment main sent survived: %q still in %q", frag, got)
					}
				}
				if r := m.Restore(got); r != p.in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", p.in, r)
				}
				// The new output is inert: masking it again changes nothing and the re-scan reads it clean.
				if again := mustMask(t, New(), p.want); again != p.want {
					t.Errorf("the masked output is not inert:\n out:   %q\n again: %q", p.want, again)
				}
				if tag := New().residue(p.want); tag != "" {
					t.Errorf("residue() reads the masked output %q as %q", p.want, tag)
				}
			}
		})
	}

	// Byte-identical with main, raw and templatized: the readers of skKey that already masked such a key
	// whole, because the value around it is a rule's -- a userinfo (4d, and 4 before it), a host label
	// (9e) and a bearer value (3a; 3c reads only JWT and AWS, TestGluedKeyRulesReadTheKeyDetectors).
	same := []struct{ name, in, want, tmpl string }{
		{"userinfo, '.' after the key (4d)", "s3://" + proj + ".prod@bucket", "s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>"},
		{"userinfo with a password (4d)", "s3://" + proj + ".prod:secret@bucket", "s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>"},
		{"host label (9e)", "https://" + proj + ".prod.acme.com/x", "https://<HOST_1>/x", "https://<HOST_1>/x"},
		{"bearer key (3a)", "hdr Bearer " + proj + " expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
		{"bearer key, '.' after (3a)", "hdr Bearer " + proj + ".prod expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: c.in}))
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("a shape outside #51 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}

	// LAYOUT ONLY: masked on main and masked now, no byte unmasked, the placeholders arranged differently.
	// A key the detectors now see is a key half, #46's design: in a userinfo the colon leaves the
	// placeholder, and in a host list the key's member is minted first, #57's numbering for a classic key.
	// A home directory, a host or a member that IS such a key is tagged TOKEN, not USER or HOST.
	layout := []struct{ name, in, want, tmpl string }{
		{"the #46 cell, the key is the username", "postgres://" + proj + ":password@localhost/db",
			"postgres://<TOKEN_1>:<TOKEN_2>@<HOST_1>/db", "postgres://<TOKEN_1>:<TOKEN_2>@<HOST_1>/db"},
		{"the #46 mirror, the key is the password", "postgres://appuser:" + proj + "@db",
			"postgres://<TOKEN_2>:<TOKEN_1>@<HOST_1>", "postgres://<TOKEN_2>:<TOKEN_1>@<HOST_1>"},
		{"list member, '.' after the key (9e)", "mongodb://h1," + proj + ".prod,host3/db",
			"mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db", "mongodb://<HOST_2>,<HOST_1>,<HOST_3>/db"},
		{"a home directory that is a key", "/home/" + proj + "/x", "/home/<TOKEN_1>/x", "/home/<TOKEN_1>/x"},
	}
	for _, c := range layout {
		t.Run("layout/"+c.name, func(t *testing.T) {
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: c.in}))
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				m := New()
				if got := mustMask(t, m, p.in); got != p.want {
					t.Errorf("layout moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				} else if r := m.Restore(got); r != p.in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", p.in, r)
				}
			}
		})
	}

	// THE RESIDUALS, pinned. A key the detectors now see blocks the rule that owned the value around it,
	// the inertness invariant's wall, and the rest of that value is sent: an email's domain, and the
	// labels of a bare private-suffix host before the key. Main masked both values whole, because 3b
	// did not see the key. And on the template a lowercase body with no uppercase letter and its digits
	// collapsed carries neither witness, so main's leak stays there; the raw line is closed.
	residuals := []struct{ name, in, want, tmpl string }{
		{"email local part", "mail " + proj + "@example.com", "mail <TOKEN_1>@example.com", "mail <TOKEN_1>@example.com"},
		{"bare private host, '-' before the key", "peer worker-" + proj + ".corp.internal down",
			"peer worker-<TOKEN_1>.<HOST_1> down", "peer worker-<TOKEN_1>.<HOST_1> down"},
		{"lowercase body on the template", "key sk-proj-" + "abcdefghij0123456789 used",
			"key <TOKEN_1> used", "key sk-proj-abcdefghij<NUM> used"},
	}
	for _, c := range residuals {
		t.Run("residual/"+c.name, func(t *testing.T) {
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: c.in}))
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("a pinned residual moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}

	// THE COSTS on the template, pinned; the raw ones are TestAcceptedOverMasking's. A placeholder in the
	// first segment is the evidence and the run goes on across separators, so a word after "sk-v<NUM>" is
	// absorbed; and after "sk<NUM>" a letter of each case is a key. The raw line is unchanged in both.
	costs := []struct{ name, in, tmpl string }{
		{"a word after sk-v<NUM>", "sk-v2-worker-pool", "<TOKEN_1>"},
		{"mixed case after sk<NUM>", "size sk-2-AbCd", "size <TOKEN_1>"},
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

	// What stays, raw and templatized. A kebab-case name after a literal "sk-" word carries no witness; a
	// word that merely ends in "sk" is refused by \b and rule 11's lead, not by the witness, so even a long
	// mixed-case tail after one stays; and one short of each floor is no key. After "sk<NUM>" a short
	// tail, or one of a single case, is a number with a word beside it.
	for _, s := range []string{
		"sk-learn-compatible-estimator",
		"sk-worker-pool-7f9c",
		"task-AbCdEfGhIjKlMnOpQr",
		"ask-AbCdEfGhIjKlMnOpQr",
		"disk-AbCdEfGhIjKlMnOpQr",
		"Accept-Language: sk-SK,sk;q=0.9",
		"sk-Ab_Cd",
		"key sk-AbCd-EfGh_IjKlm used",
		"key sk-x-abcdefghijklmno used",
		"sk-1 failed",
		"region sk-2",
		"sk-2XL",
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
	t.Run("a key recurs under one placeholder", func(t *testing.T) {
		m := New()
		if got, want := mustMask(t, m, "key "+proj+" rejected"), "key <TOKEN_1> rejected"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if got, want := mustMask(t, m, "OPENAI_API_KEY="+proj), "OPENAI_API_KEY=<TOKEN_1>"; got != want {
			t.Errorf("recurrence: got %q, want %q", got, want)
		}
		if got := m.Restore("rotate <TOKEN_1>"); got != "rotate "+proj {
			t.Errorf("Restore = %q, want the key alone", got)
		}
	})

	// residue() reads the same rule, so a raw key of any format mutes the escalation; main read every one
	// of these as clean.
	for _, in := range []string{
		"key " + proj + " rejected",
		"key " + svcacct + " rejected",
		"key " + admin + " rejected",
		"key " + ant + " rejected",
		"key " + under + " rejected",
		"key " + hexv + " rejected",
		"OPENAI_API_KEY=" + proj,
		"key x_" + proj + " used",
		"https://" + proj + ".prod.acme.com/x",
	} {
		if tag := New().residue(in); tag != tagToken {
			t.Errorf("a raw sk- key does not mute: residue(%q) = %q, want %q", in, tag, tagToken)
		}
	}
}

// #114's invented keys, one or more per format family, shared by its test and the tables. Each is written
// split at its vendor prefix, as #51's are, so that no source line carries a whole key-shaped literal for a
// secret scanner to match; the strings are the same at run time.
const (
	stripeLiveSecret       = "sk" + "_live_" + "Rk4mQz8vLp2wXn6tYb9cHd3s"
	stripeRestrictedSecret = "rk" + "_test_" + "Tq5wEr8yUi2oPa6sDf9gHj3kLz7x"
	stripeWebhookSecret    = "wh" + "sec_" + "Mn8bVc6xZa4sDf2gHj0kLqWe5rTy7uIo"
	githubClassicSecret    = "gh" + "p_" + "Xy7zWv5uTs3rQp1oNm9lKjHgFeDcBa8ZyQw4"
	githubServerSecret     = "gh" + "s_" + "Lp2wXn6tYb9cHd3sRk4mQz8vEr8yUi2oPa6s"
	githubPATSecret        = "github" + "_pat_" + "11ABCDE2F0aB3cD4eF5gH6" + "_" +
		"iJ7kL8mN9oP0qR1sT2uV3wX4yZ5aB6cD7eF8gH9iJ0kL1mN2oP3qR4sT5uV"
	slackBotSecret  = "xox" + "b-" + "2849163057-7340815926413-" + "Gk4Lm8Np2Qr6St0Uv3Wx7Yz1"
	slackUserSecret = "xox" + "p-" + "2849163057-5817304926-6150928374-" + "9f3a2c7e1b8d4f60a5e9c2b7d1f8a3e6"
	slackAppSecret  = "xa" + "pp-" + "1-A04BCDEF7GH-6283910475612-" +
		"3f9a2c7e1b8d4f60a5e9c2b7d1f8a3e6c4b9d2f7a1e8c5b3d6f9a2e7c1b4d8f0"
	googleAPISecret = "AI" + "za" + "SyD4k8Lm-2Qp9Rs_Tv7Wx1Yz5Bc3Fh6Jn0K"
)

// TestIssue114VendorSecretFormats is the named regression for #114. Stripe (sk_ and rk_ in live or test
// mode, whsec_), GitHub (ghp_, gho_, ghu_, ghs_, ghr_ and github_pat_), Slack (xoxb-, xoxp-, xoxa-, xoxr-,
// xoxs- and xapp-) and Google (AIza) secrets matched no detector -- 3b reads "sk-" with a hyphen only -- so
// each was sent whole, raw and on the template, and residue() read the line as clean. A rule that owns the
// value around one (a userinfo, a host, an email local part, a home directory, a bearer value) already
// masked it whole; prose, a query value, a JSON value and an environment variable were the leak. v0.4.0
// onward.
//
// Each format family is now one rule run LAST (rule 12, see vendorKeys), so it reads only text no rule
// claimed and every rule before it sees main's input: the reader rows below are main's bytes. Glue before
// a key is prose, #57's decision; after it the format's alphabet decides, as #108 and #51 decided it --
// '_' is a github_pat_ or Google key's, '-' a Slack or Google key's, and a closer is never the key's. On the
// template a pipeline placeholder is the evidence, over a floor in units, and after a Slack prefix's <NUM>
// a letter of each case, as after sk<NUM>.
func TestIssue114VendorSecretFormats(t *testing.T) {
	type vendorRow struct {
		name, key, env string
		sent           []string // what main sent on one path or both; none may survive
		tmplMains      bool     // the secret segment is <HEX> on the template, so the template is main's
	}
	keys := []vendorRow{
		{"stripe sk_live_", stripeLiveSecret, "STRIPE_SECRET_KEY", []string{"sk" + "_live_", "mQz", "cHd"}, false},
		{"stripe rk_test_", stripeRestrictedSecret, "STRIPE_SECRET_KEY", []string{"rk" + "_test_", "wEr", "kLz"}, false},
		{"stripe whsec_", stripeWebhookSecret, "STRIPE_WEBHOOK_SECRET", []string{"wh" + "sec_", "bVc", "kLqWe"}, false},
		{"github ghp_", githubClassicSecret, "GITHUB_TOKEN", []string{"gh" + "p_", "zWv", "lKjHgFeDcBa"}, false},
		{"github ghs_", githubServerSecret, "GITHUB_TOKEN", []string{"gh" + "s_", "wXn", "yUi"}, false},
		{"github github_pat_", githubPATSecret, "GITHUB_TOKEN", []string{"github" + "_pat_", "ABCDE", "_iJ"}, false},
		{"slack xoxb-", slackBotSecret, "SLACK_BOT_TOKEN", []string{"xox" + "b", "Gk", "Np"}, false},
		{"slack xoxp-", slackUserSecret, "SLACK_USER_TOKEN", []string{"xox" + "p-", "9f3a2c7e"}, true},
		{"slack xapp-", slackAppSecret, "SLACK_APP_TOKEN", []string{"xa" + "pp-", "3f9a2c7e"}, true},
		{"google AIza", googleAPISecret, "GOOGLE_API_KEY", []string{"AI" + "za", "SyD", "Rs_Tv"}, false},
	}
	fill := func(s string, k vendorRow) string {
		return strings.NewReplacer("{K}", k.key, "{ENV}", k.env).Replace(s)
	}

	// The template is the pipeline's handwriting, read for real: every key's digits are <NUM> there, so the
	// template rows below read a mangled key, the placeholder being the evidence.
	for _, k := range keys {
		if tKey, _ := pipeline.Templatize(k.key); !regexp.MustCompile(pipelinePH).MatchString(tKey) {
			t.Fatalf("premise broken: the %s key %q is not mangled by the pipeline masker", k.name, tKey)
		}
	}

	// The text around a key that no rule claims, the same for every family. The template is
	// TemplatizeLine's, the real Template field. The query values are the commonest real placement of a
	// Google key and a common one for the rest: there 9a masked the host on main and the key was sent.
	closed := []struct{ name, in, want, tmpl string }{
		{"prose", "key {K} rejected", "key <TOKEN_1> rejected", "key <TOKEN_1> rejected"},
		{"env var", "{ENV}={K}", "{ENV}=<TOKEN_1>", "{ENV}=<TOKEN_1>"},
		{"logfmt msg", `level=error msg="key {K} rejected" module=x`,
			`level=error msg="key <TOKEN_1> rejected" module=x`, `level=error msg="key <TOKEN_1> rejected" module=x`},
		{"json msg", `{"level":"error","msg":"key {K} rejected"}`,
			`{"level":"error","msg":"key <TOKEN_1> rejected"}`, `{"level":<STR>,"msg":"key <TOKEN_1> rejected"}`},
		{"json value", `{"api_key":"{K}"}`, `{"api_key":"<TOKEN_1>"}`, `{"api_key":<STR>}`},
		{"google geocode query value", "https://maps.googleapis.com/maps/api/geocode/json?address=x&key={K}",
			"https://<HOST_1>/maps/api/geocode/json?address=x&key=<TOKEN_1>",
			"https://<HOST_1>/maps/api/geocode/json?address=x&key=<TOKEN_1>"},
		{"stripe query value", "GET https://api.stripe.com/v1/charges?key={K} 401",
			"GET https://<HOST_1>/v1/charges?key=<TOKEN_1> 401", "GET https://<HOST_1>/v<NUM>/charges?key=<TOKEN_1> <NUM>"},
		{"github query value", "GET https://api.github.com/user?access_token={K} 401",
			"GET https://<HOST_1>/user?access_token=<TOKEN_1> 401", "GET https://<HOST_1>/user?access_token=<TOKEN_1> <NUM>"},
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
				t.Errorf("a fragment main sent survived: %q still in %q", frag, got)
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
				if k.tmplMains {
					return // the template is main's, pinned below
				}
				tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: in}))
				check(t, tIn, fill(c.tmpl, k), k.sent)
			})
		}
	}

	// After a key, the format's alphabet decides, raw and templatized alike: '_' is a github_pat_ or Google
	// key's and '-' a Slack or Google key's, so the word is absorbed; for the others the word is prose.
	after := []struct{ name, in, want string }{
		{"stripe, '_' after", "key " + stripeLiveSecret + "_prod used", "key <TOKEN_1>_prod used"},
		{"stripe, '-' after", "key " + stripeLiveSecret + "-prod used", "key <TOKEN_1>-prod used"},
		{"whsec, '_' after", "key " + stripeWebhookSecret + "_prod used", "key <TOKEN_1>_prod used"},
		{"whsec, '-' after", "key " + stripeWebhookSecret + "-prod used", "key <TOKEN_1>-prod used"},
		{"ghp, '_' after", "key " + githubClassicSecret + "_prod used", "key <TOKEN_1>_prod used"},
		{"ghp, '-' after", "key " + githubClassicSecret + "-prod used", "key <TOKEN_1>-prod used"},
		{"github_pat, '_' after is the key's", "key " + githubPATSecret + "_prod used", "key <TOKEN_1> used"},
		{"github_pat, '-' after", "key " + githubPATSecret + "-prod used", "key <TOKEN_1>-prod used"},
		{"xoxb, '_' after", "key " + slackBotSecret + "_prod used", "key <TOKEN_1>_prod used"},
		{"xoxb, '-' after is the key's", "key " + slackBotSecret + "-prod used", "key <TOKEN_1> used"},
		{"xapp, '_' after", "key " + slackAppSecret + "_prod used", "key <TOKEN_1>_prod used"},
		{"xapp, '-' after is the key's", "key " + slackAppSecret + "-prod used", "key <TOKEN_1> used"},
		{"google, '_' after is the key's", "key " + googleAPISecret + "_prod used", "key <TOKEN_1> used"},
		{"google, '-' after is the key's", "key " + googleAPISecret + "-prod used", "key <TOKEN_1> used"},
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

	// Byte-identical with main, raw and templatized, for every family: the rules that own a value around a
	// key already masked it whole, and run before rule 12, so it never sees one. Each row is a position a
	// reader of keyShapes would take if the vendor shapes joined it -- a userinfo (4d's), a host label and a
	// list member (9e's), a bearer value (3c's) -- or one a key rule run at 3b's slot would block: an email,
	// a bare private host, a home directory and #46's cell, where main masks the whole half.
	same := []struct{ name, in, want, tmpl string }{
		{"userinfo, '.' after the key (4d's position)", "s3://{K}.prod@bucket", "s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>"},
		{"userinfo with a password (4d's position)", "s3://{K}.prod:secret@bucket", "s3://<TOKEN_1>@<HOST_1>", "s<NUM>://<TOKEN_1>@<HOST_1>"},
		{"host label (9e's position)", "https://{K}.prod.acme.com/x", "https://<HOST_1>/x", "https://<HOST_1>/x"},
		{"list member (9e's position)", "mongodb://h1,{K}.prod,host3/db",
			"mongodb://<HOST_1>,<HOST_2>,<HOST_3>/db", "mongodb://<HOST_1>,<HOST_2>,<HOST_3>/db"},
		{"bearer value, '.' after (3c's position)", "hdr Bearer {K}.prod expired", "hdr Bearer <TOKEN_1> expired", "hdr Bearer <TOKEN_1> expired"},
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
						t.Errorf("a shape outside #114 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
					}
				}
			})
		}
	}

	// THE RESIDUALS, pinned: the raw line is closed and the template is main's. A user or app token's
	// secret segment is <HEX> on the template, so nothing of it is left to send; but with a word character
	// glued after the key the hex segment has no \b to end on, NUM takes its digits instead, and the letters
	// a-f between them are sent, as on main.
	residuals := []struct{ name, in, want, tmpl string }{
		{"xoxp, the secret segment is <HEX>", "key " + slackUserSecret + " rejected", "key <TOKEN_1> rejected",
			"key xox" + "p<NUM><NUM><NUM>-<HEX> rejected"},
		{"xapp, the secret segment is <HEX>", "key " + slackAppSecret + " rejected", "key <TOKEN_1> rejected",
			"key xa" + "pp<NUM>-A<NUM>BCDEF<NUM>GH<NUM>-<HEX> rejected"},
		{"xoxp, a word character glued after", "key " + slackUserSecret + "_prod used", "key <TOKEN_1>_prod used",
			"key xox" + "p<NUM><NUM><NUM><NUM>f<NUM>a<NUM>c<NUM>e<NUM>b<NUM>d<NUM>f<NUM>a<NUM>e<NUM>c<NUM>b<NUM>d<NUM>f<NUM>a<NUM>e<NUM>_prod used"},
	}
	for _, c := range residuals {
		t.Run("residual/"+c.name, func(t *testing.T) {
			check(t, c.in, c.want, []string{"9f3a2c7e", "3f9a2c7e"})
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: c.in}))
			if tIn != c.tmpl {
				t.Fatalf("premise broken: the template is not the pinned one:\n got:  %q\n want: %q", tIn, c.tmpl)
			}
			if got := mustMask(t, New(), tIn); got != tIn {
				t.Errorf("a pinned residual moved:\n in:   %q\n got:  %q", tIn, got)
			}
		})
	}

	// THE COSTS on the template, pinned; the raw one is TestAcceptedOverMasking's. A placeholder is the
	// evidence over a floor in units well below the raw one, so a short tail with a digit in it is a key
	// there; and after a Slack prefix's <NUM> a letter of each case is. The raw line is unchanged in each.
	costs := []struct{ name, in, tmpl string }{
		{"stripe, eleven units with a digit", "key sk" + "_test_Abc123defghij used", "key <TOKEN_1> used"},
		{"whsec, eleven units with a digit", "key wh" + "sec_Abc123defghij used", "key <TOKEN_1> used"},
		{"ghp, eighteen units with a digit", "key gh" + "p_AbCdEfGhIjKl1MnOp used", "key <TOKEN_1> used"},
		{"github_pat, twenty-seven units with a digit", "key github" + "_pat_AbCdEfGhIjKlMnOpQrStUvWx1Yz used", "key <TOKEN_1> used"},
		{"google, thirty characters with digits", "key AI" + "zaSyD4k8Lm2Qp9RsTv7Wx1Yz5Bc3Fh6J used", "key <TOKEN_1> used"},
		{"slack, mixed case after xoxb<NUM>", "size xox" + "b-12-Ab", "size <TOKEN_1>"},
		{"slack, fifteen letters after xoxb<NUM>", "key xox" + "b-1-AbCdEfGhIjKlMnO used", "key <TOKEN_1> used"},
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
	// the format's length, letters only so that no placeholder gives the template branch evidence either,
	// and a short tail with a digit is under the template floor. The alphabet: '_' ends a Stripe or ghp_
	// body, so a snake_case name after "sk_test_" is short pieces. The witness: a Slack prefix with no digit
	// after it -- every Slack body starts with a numeric segment -- or a kebab-case name with no sixteen-run. The lead: a letter or digit before the prefix makes
	// a longer word that merely contains it.
	for _, s := range []string{
		"sk" + "_test_helper",
		"call sk" + "_live_mode now",
		"sk" + "_test_create_customer_with_card_1",
		"sk" + "_test_run42",
		"wh" + "sec_rotated",
		"wh" + "sec_secret_rotation_v2",
		"wh" + "sec_v2_rotation",
		"prefix gh" + "p_ is classic",
		"gh" + "p_cache_dir",
		"gh" + "p_v2",
		"set gh" + "s_timeout=30s",
		"key gh" + "p_AbCdEfGh1IjKlMnOpQ used",
		"github" + "_pat_rotation_policy_v2_enabled",
		"key github" + "_pat_AbCdEfGhIjKl1MnOpQrStUvWxYz used",
		"AI" + "zaTest",
		"AI" + "za_test_1",
		"AI" + "za is the prefix",
		"xox" + "b token missing",
		"the xox" + "b- prefix",
		"xox" + "b-2024-rollout-plan",
		"set xox" + "b-1 retries",
		"see xox" + "b-authenticationfailures",
		"key sk" + "_live_" + "AbCdEfGhIjKlMnOpQrStUvW used",
		"key wh" + "sec_" + "AbCdEfGhIjKlMnOpQrStUvWxYzAbCdE used",
		"key gh" + "p_" + "AbCdEfGhIjKlMnOpQrStUvWxYzAbCdEfGhI used",
		"key github" + "_pat_" + strings.Repeat("AbCdEfGhIj", 8) + "K used",
		"key AI" + "za" + "AbCdEfGhIjKlMnOpQrStUvWxYzAbCdEfGh used",
		"disk" + "_test_" + "Rk4mQz8vLp2wXn6tYb9cHd3s",
		"ask" + "_live_" + "Rk4mQz8vLp2wXn6tYb9cHd3s",
		"x" + githubClassicSecret,
		"my" + githubPATSecret,
		"x" + slackBotSecret,
		"b" + googleAPISecret,
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

	// residue() reads the same rules, so a raw vendor secret mutes the escalation; main read every one of
	// these as clean.
	for _, k := range keys {
		for _, in := range []string{
			"key " + k.key + " rejected",
			k.env + "=" + k.key,
			"token_" + k.key + " used",
			"https://maps.googleapis.com/maps/api/geocode/json?address=x&key=" + k.key,
		} {
			if tag := New().residue(in); tag != tagToken {
				t.Errorf("a raw %s secret does not mute: residue(%q) = %q, want %q", k.name, in, tag, tagToken)
			}
		}
	}
}

// TestIssue90HomeDirClosingPunctuation is the named regression for #90. Detector 10's username class
// stopped at '/', whitespace, ':', '<' and '>', and at nothing a log writes AFTER a value, so a username at
// the end of a quoted or bracketed value took the closer into its placeholder: msg="cannot read
// /home/deploy2prod" went out as ".../home/<USER_1> module=x", {"path":"/home/bob"} lost its "}, and
// open("/home/bob") its "). Nothing was disclosed -- Restore round-tripped -- but the model was sent a
// broken payload, and an answer naming <USER_1> restored with the stray characters. Raw lines and the JSON
// and logfmt templates alike, which re-quote the message.
//
// The class stays for the body, so a closer INSIDE a name is still masked; only the name's LAST unit may
// not be one of the trailing set, " ' ` ) ] } , ; . and '\'. The backslash is there because inside a quoted
// value it escapes the closing quote ("open \"/home/bob\""). What may leave a placeholder is a trailing
// closer and nothing else.
func TestIssue90HomeDirClosingPunctuation(t *testing.T) {
	const trailing = "\"'`)]},;.\\"
	closed := []struct{ name, in, want, tmpl string }{
		// The issue's three lines. The template is TemplatizeLine's, the real Template field: a logfmt msg
		// and a JSON msg keep their text, re-quoted; a text-path quoted string is <STR>.
		{"logfmt msg (the issue's)", `level=error msg="cannot read /home/deploy2prod" module=x`,
			`level=error msg="cannot read /home/<USER_1>" module=x`, `level=error msg="cannot read /home/<USER_1>" module=x`},
		{"json value (the issue's)", `{"path":"/home/bob"}`, `{"path":"/home/<USER_1>"}`, `{"path":<STR>}`},
		{"call (the issue's)", `open("/home/bob")`, `open("/home/<USER_1>")`, `open(<STR>)`},
		{"json msg", `{"level":"error","msg":"cannot read /home/bob"}`,
			`{"level":"error","msg":"cannot read /home/<USER_1>"}`, `{"level":<STR>,"msg":"cannot read /home/<USER_1>"}`},
		// One row per closer.
		{"double quote, /Users/", `/Users/jane"`, `/Users/<USER_1>"`, `/Users/<USER_1>"`},
		{"single quote", `['/home/bob']`, `['/home/<USER_1>']`, `[<STR>]`},
		{"backtick", "`/home/bob` missing", "`/home/<USER_1>` missing", "`/home/<USER_1>` missing"},
		{"paren", `(see /home/bob)`, `(see /home/<USER_1>)`, `(see /home/<USER_1>)`},
		{"bracket", `list [/home/bob]`, `list [/home/<USER_1>]`, `list [/home/<USER_1>]`},
		{"brace", `{dir: /home/bob}`, `{dir: /home/<USER_1>}`, `{dir: /home/<USER_1>}`},
		{"comma", `x=/home/bob, y`, `x=/home/<USER_1>, y`, `x=/home/<USER_1>, y`},
		{"semicolon", `/home/bob;`, `/home/<USER_1>;`, `/home/<USER_1>;`},
		{"sentence dot", `cannot read /home/bob.`, `cannot read /home/<USER_1>.`, `cannot read /home/<USER_1>.`},
		// The backslash: an escaped closing quote inside a quoted value, on a raw JSON and a raw logfmt line.
		{"escaped quote, raw json", `{"msg":"open(\"/home/bob\")"}`, `{"msg":"open(\"/home/<USER_1>\")"}`, `{"msg":"open(<STR>)"}`},
		{"escaped quote, raw logfmt", `level=error msg="open \"/home/bob\": denied" module=x`,
			`level=error msg="open \"/home/<USER_1>\": denied" module=x`, `level=error msg="open <STR>: denied" module=x`},
		// A run of mixed closers leaves whole, not one character of it.
		{"dot then quote", `cannot read /home/bob."`, `cannot read /home/<USER_1>."`, `cannot read /home/<USER_1>."`},
		{"run of four closers", `x=["/home/bob"]), y`, `x=["/home/<USER_1>"]), y`, `x=[<STR>]), y`},
		{"run of closers, raw logfmt", `level=error msg="x=[\"/home/bob\"]), y" module=x`,
			`level=error msg="x=[\"/home/<USER_1>\"]), y" module=x`, `level=error msg="x=[<STR>]), y" module=x`},
		{"run of closers, logfmt template", `level=error msg="(see [/home/bob]);." module=x`,
			`level=error msg="(see [/home/<USER_1>]);." module=x`, `level=error msg="(see [/home/<USER_1>]);." module=x`},
		// A leading closer is inside the name and stays there; only the trailing one leaves.
		{"leading quote stays inside", `/home/'bob'`, `/home/<USER_1>'`, `/home/<USER_1>`},
		// #43's tolerant form: a pipeline placeholder may end the name, and a closer after it still leaves.
		{"tolerant end, text template", `(see /home/maxie7)`, `(see /home/<USER_1>)`, `(see /home/<USER_1>)`},
		{"tolerant end, logfmt template", `level=error msg="cannot read /home/maxie7" module=x`,
			`level=error msg="cannot read /home/<USER_1>" module=x`, `level=error msg="cannot read /home/<USER_1>" module=x`},
		// A JSON escape glued to the name: 'n' is a body character, so "bob\n" stays one name, as on main;
		// only the "} after it leaves. The template decodes the escape, and the newline ends the name there.
		{"json escape glued to the name", `{"msg":"cannot read /home/bob\n"}`,
			`{"msg":"cannot read /home/<USER_1>"}`, `{"msg":"cannot read /home/<USER_1>"}`},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: c.in}))
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				m := New()
				got := mustMask(t, m, p.in)
				if got != p.want {
					t.Errorf("a closer after a home-dir username not left outside it:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
				if r := m.Restore(got); r != p.in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", p.in, r)
				}
				// The answer side: what <USER_1> restores to never ends in a closer.
				if name := m.Restore("<USER_1>"); name != "<USER_1>" && strings.TrimRight(name, trailing) != name {
					t.Errorf("<USER_1> restores to %q, which ends in a closer", name)
				}
				// The new output is inert: masking it again changes nothing and the re-scan reads it clean.
				if again := mustMask(t, New(), p.want); again != p.want {
					t.Errorf("the masked output is not inert:\n out:   %q\n again: %q", p.want, again)
				}
				if tag := New().residue(p.want); tag != "" {
					t.Errorf("residue() reads the masked output %q as %q", p.want, tag)
				}
			}
		})
	}

	// Raw only: x= and y= carry no level or message key, so the line is not logfmt and its template is
	// "x=<STR> y=<NUM>", with no name left in it.
	t.Run("run of closers in a quoted non-logfmt value (raw only)", func(t *testing.T) {
		const in, want = `x="(see [/home/bob]);." y=1`, `x="(see [/home/<USER_1>]);." y=1`
		m := New()
		if got := mustMask(t, m, in); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if got := m.Restore("<USER_1>"); got != "bob" {
			t.Errorf("Restore(<USER_1>) = %q, want %q", got, "bob")
		}
	})

	// Byte-identical with main, raw and templatized. A closer INSIDE a name is part of it -- the body class
	// is unchanged -- and so is everything that already ended the name.
	same := []struct{ name, in, want, tmpl string }{
		{"quote inside, then a path", `/home/o'brien/x`, `/home/<USER_1>/x`, `/home/<USER_1>/x`},
		{"paren inside, then a path", `/home/a)b/x`, `/home/<USER_1>/x`, `/home/<USER_1>/x`},
		{"comma inside, /Users/", `/Users/a,b/x`, `/Users/<USER_1>/x`, `/Users/<USER_1>/x`},
		{"quote inside, end of text", `/home/o'brien`, `/home/<USER_1>`, `/home/<USER_1>`},
		{"a path after the name", `loaded from /home/maxie/go/pkg/mod`, `loaded from /home/<USER_1>/go/pkg/mod`, `loaded from /home/<USER_1>/go/pkg/mod`},
		{"trailing digit", `/home/maxie7`, `/home/<USER_1>`, `/home/<USER_1>`},
		{"a uuid home dir", `/home/550e8400-e29b-41d4-a716-446655440000/app.log`, `/home/<UUID_1>/app.log`, `/home/<USER_1>/app.log`},
		{"colon after", `/home/bob:`, `/home/<USER_1>:`, `/home/<USER_1>:`},
		{"angle bracket after", `/home/bob>`, `/home/<USER_1>>`, `/home/<USER_1>>`},
		{"end of text", `see /home/bob`, `see /home/<USER_1>`, `see /home/<USER_1>`},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: c.in}))
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("a shape outside #90 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}

	// THE COSTS, pinned. A name whose LAST character is a closer loses it even before a '/', since the rule
	// has no trailing context; and a "name" made only of closers is none, so it is left as it is. Only
	// closers leave a placeholder in either.
	costs := []struct{ name, in, want string }{
		{"a name ending in a dot, then a path", `/home/bob./x`, `/home/<USER_1>./x`},
		{"dot-dot", `/home/../etc`, `/home/../etc`},
		{"dot", `/home/./x`, `/home/./x`},
	}
	for _, c := range costs {
		t.Run("cost/"+c.name, func(t *testing.T) {
			if got := mustMask(t, New(), c.in); got != c.want {
				t.Errorf("a pinned cost moved:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		})
	}

	// The answer side, and recurrence. A model answer naming <USER_1> restores to the name alone, and the
	// same name bare elsewhere in the request is the same placeholder.
	t.Run("an answer restores the name alone", func(t *testing.T) {
		m := New()
		mustMask(t, m, `level=error msg="cannot read /home/bob" module=x`)
		if got, want := m.Restore("check permissions on /home/<USER_1>"), "check permissions on /home/bob"; got != want {
			t.Errorf("Restore = %q, want %q", got, want)
		}
		if got, want := mustMask(t, m, "cd /home/bob"), "cd /home/<USER_1>"; got != want {
			t.Errorf("recurrence: got %q, want %q", got, want)
		}
	})

	// residue() is unchanged in kind: a raw name before a closer is still a USER value, and mutes.
	if tag := New().residue(`cannot read /home/bob"`); tag != tagUser {
		t.Errorf("a raw name before a closer does not mute: residue = %q, want %q", tag, tagUser)
	}
}

// TestIssue52SchemeAnchor is the named regression for #52. 9a and 9c find a URL by its scheme,
// `\b[a-z][a-z0-9+.-]*://`, and that anchor is CONTEXT -- outside the group -- so #43, which made
// every group tolerant of the pipeline's placeholders, never touched it. It failed two ways:
//
//   - On the Template field a scheme with a digit templatizes to s<NUM>://, the scheme run could
//     not step over the placeholder, and the rule went quiet: url=s<NUM>://<TOKEN_1>:<TOKEN_2>@bucket
//     sent "bucket". The raw line was fine.
//   - On the raw line \b fails before a scheme glued to '_', a word character:
//     x_https://acme.com/x and __https://acme.com__ were sent whole. So was 2https://acme.com/x,
//     where \b fails after the digit -- on the raw line only, since the digit is <NUM> in the
//     template and \b holds after its '>'.
//
// Credentials were never affected: detector 4 anchors on a bare "://". Only hosts were sent. The
// lesson the issue drew is the one TestEveryDetectorsContextSurvivesTemplating now enforces: a
// detector's tolerance must cover its anchors, not only its group.
func TestIssue52SchemeAnchor(t *testing.T) {
	closed := []struct {
		name, in, want, tmpl string
		sent                 []string // what main sent on one path or both; none may survive
	}{
		// The issue's shapes: a digit in the scheme, the host on a PUBLIC suffix so 9b cannot rescue it.
		{"digit scheme", "get s3://bucket.example.com/key failed",
			"get s3://<HOST_1>/key failed", "get s<NUM>://<HOST_1>/key failed", []string{"bucket", "example.com"}},
		{"digit scheme, credential, bare host", "url=s3://AKIAIOSFODNN7EXAMPLE:wJalrXUtnFEMIKauth@bucket",
			"url=s3://<TOKEN_1>:<TOKEN_2>@<HOST_1>", "url=s<NUM>://<TOKEN_1>:<TOKEN_2>@<HOST_1>", []string{"bucket"}},
		{"digit scheme, credential, public suffix", "url=s3://AKIAIOSFODNN7EXAMPLE:secret@bucket.example.com/x",
			"url=s3://<TOKEN_1>:<TOKEN_2>@<HOST_1>/x", "url=s<NUM>://<TOKEN_1>:<TOKEN_2>@<HOST_1>/x",
			[]string{"bucket", "example.com", "secret"}},
		{"digit scheme, plain userinfo", "s3://u:p@bucket.example.com/x",
			"s3://<TOKEN_1>@<HOST_1>/x", "s<NUM>://<TOKEN_1>@<HOST_1>/x", []string{"bucket", "u:p"}},
		{"h2 scheme", "dial h2://api.acme.com/x", "dial h2://<HOST_1>/x", "dial h<NUM>://<HOST_1>/x", []string{"acme.com"}},
		// 9c reads the same anchor: a digit-scheme host list went quiet as a whole. Members that
		// templatize alike come back as one placeholder, as they always have.
		{"digit scheme host list", "s3://h1,h2,h3/x",
			"s3://<HOST_1>,<HOST_2>,<HOST_3>/x", "s<NUM>://<HOST_1>,<HOST_1>,<HOST_1>/x", []string{"h1", "h2", "h3"}},
		{"digit scheme host list, dotted members", "s3://h1.acme.com,h2.acme.com,h3.acme.com/x",
			"s3://<HOST_1>,<HOST_2>,<HOST_3>/x", "s<NUM>://<HOST_1>,<HOST_1>,<HOST_1>/x", []string{"acme.com"}},
		// A '_' before the scheme, on both paths. The trailing "__" goes into the host, which is
		// #66's pinned trailing-'_' cost, not a new one.
		{"underscore before the scheme", "see x_https://acme.com/x",
			"see x_https://<HOST_1>/x", "see x_https://<HOST_1>/x", []string{"acme.com"}},
		{"markdown bold", "__https://acme.com__", "__https://<HOST_1>", "__https://<HOST_1>", []string{"acme.com"}},
		{"identifier before the scheme", "svc_https://acme.com/x",
			"svc_https://<HOST_1>/x", "svc_https://<HOST_1>/x", []string{"acme.com"}},
		// A snake_case "scheme" is not a legal one; its last run after the '_' is read as the scheme,
		// and the host behind it is masked.
		{"snake_case scheme", "foo_bar://acme.com/x", "foo_bar://<HOST_1>/x", "foo_bar://<HOST_1>/x", []string{"acme.com"}},
		{"digit and underscore before the scheme", "x9_https://acme.com/x",
			"x9_https://<HOST_1>/x", "x<NUM>_https://<HOST_1>/x", []string{"acme.com"}},
		// A digit before the scheme: sent on the raw line, masked in the template. Both paths agree now.
		{"digit before the scheme", "2https://acme.com/x", "2https://<HOST_1>/x", "<NUM>https://<HOST_1>/x", []string{"acme.com"}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				m := New()
				got := mustMask(t, m, p.in)
				if got != p.want {
					t.Errorf("url host not masked:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
				for _, frag := range c.sent {
					if strings.Contains(got, frag) {
						t.Errorf("a fragment main sent survived: %q still in %q", frag, got)
					}
				}
				if r := m.Restore(got); r != p.in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", p.in, r)
				}
			}
		})
	}

	// Byte-identical with main, raw and templatized. A letter before a scheme is part of the
	// scheme -- "xhttps" -- which is how it was always read, and the punctuation that ordinarily
	// wraps a URL never stopped the anchor. k8s:// is the accident worth naming: in the template it
	// was masked on main only because the anchor restarted at the 's' after <NUM>, reading the
	// scheme as "s"; it reads as k<NUM>s now, and the output is the same. Scheme-like words in prose
	// with no "://" are no one's business.
	same := []struct{ name, in, want, tmpl string }{
		{"the address is the authority", "https://10.0.0.5/x", "https://<IP_1>/x", "https://<HOST_1>/x"},
		{"start of text", "https://acme.com/x", "https://<HOST_1>/x", "https://<HOST_1>/x"},
		{"letter before the scheme", "xhttps://acme.com/x", "xhttps://<HOST_1>/x", "xhttps://<HOST_1>/x"},
		{"parentheses", "(https://acme.com/x)", "(https://<HOST_1>/x)", "(https://<HOST_1>/x)"},
		{"double quotes", `"https://acme.com/x"`, `"https://<HOST_1>/x"`, "<STR>"},
		{"angle brackets", "<https://acme.com/x>", "<https://<HOST_1>/x>", "<https://<HOST_1>/x>"},
		{"k8s scheme", "ctx k8s://cluster.acme.com/ns", "ctx k8s://<HOST_1>/ns", "ctx k<NUM>s://<HOST_1>/ns"},
		{"scheme-like words in prose", "the s3 bucket and h2 frames over https",
			"the s3 bucket and h2 frames over https", "the s<NUM> bucket and h<NUM> frames over https"},
		{"scheme-like key in prose", "proto=s3 host=bucket.example.com",
			"proto=s3 host=bucket.example.com", "proto=s<NUM> host=bucket.example.com"},
		{"comma-separated urls", "peers=https://a.com,https://b.com",
			"peers=https://<HOST_1>,https://<HOST_2>", "peers=https://<HOST_1>,https://<HOST_2>"},
		{"plus in the scheme", "git+ssh://git@github.com/org/repo.git",
			"git+ssh://<TOKEN_1>@<HOST_1>/org/repo.git", "git+ssh://<TOKEN_1>@<HOST_1>/org/repo.git"},
		{"plus in a host-list scheme", "redis+sentinel://h1:26379,h2:26379/0",
			"redis+sentinel://<HOST_1>:26379,<HOST_2>:26379/0", "redis+sentinel://<HOST_1>:<NUM>,<HOST_1>:<NUM>/<NUM>"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(), p.in); got != p.want {
					t.Errorf("a shape outside #52 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}

	// Pinned, not targeted: 3a's cell from #54's sweep. With nothing between "Bearer " and a URL, 3a
	// takes the SCHEME as the token, and 9a finds "://" with no scheme letter before it, so the host
	// is sent on both paths. The scheme is inside OUR tag here, not the pipeline's, so a run tolerant
	// of the pipeline's placeholders cannot reach it; reading our own tag as a scheme would be an
	// interference fix, not this one. Recorded as a cell of the authority audit (#65).
	t.Run("bearer before a url (#65)", func(t *testing.T) {
		const in, want = "Bearer https://acme.com/x", "Bearer <TOKEN_1>://acme.com/x"
		tIn, _ := pipeline.Templatize(in)
		for _, p := range []string{in, tIn} {
			if got := mustMask(t, New(), p); got != want {
				t.Errorf("a pinned shape moved:\n in:   %q\n got:  %q\n want: %q", p, got, want)
			}
		}
	})

	// residue() needs no change: 9a and 9c stay verify:false, and the old leftover reads as clean.
	if tag := New().residue("url=s<NUM>://<TOKEN_1>:<TOKEN_2>@bucket"); tag != "" {
		t.Errorf("residue() sees the old leftover as %q; the claim in the comment above is wrong", tag)
	}
}

// TestSchemeAnchorIsShared holds 9a and 9c to ONE scheme anchor. Two copies of it are how #52 was
// missed: each rule carried its own \b[a-z][a-z0-9+.-]*://, and #43 widened the groups beside them.
// Exactly two rules compile schemeAnchor -- the URL host and the host list -- and no rule spells the
// scheme class out on its own, in the default set or with extra suffixes.
func TestSchemeAnchorIsShared(t *testing.T) {
	const schemeClass = `[a-z0-9+.-]`
	for _, dets := range [][]detector{baseDetectors, buildDetectors([]string{"example"})} {
		users, fixpoint := 0, 0
		for i, d := range dets {
			src := d.re.String()
			if rest := strings.ReplaceAll(src, schemeAnchor, ""); strings.Contains(rest, schemeClass) {
				t.Errorf("detector %d (%s) spells out its own scheme class instead of using schemeAnchor:\n pattern: %s", i, d.tag, src)
			}
			if !strings.Contains(src, schemeAnchor) {
				continue
			}
			users++
			if d.fixpoint {
				fixpoint++
			}
			if d.tag != tagHost || d.group != 1 || d.verify {
				t.Errorf("an unexpected rule uses schemeAnchor: detector %d, tag %s, group %d, verify %v", i, d.tag, d.group, d.verify)
			}
		}
		if users != 2 || fixpoint != 1 {
			t.Errorf("%d rules use schemeAnchor (%d of them iterating); want 9a and 9c, exactly one iterating", users, fixpoint)
		}
	}
}

// TestIssue105ConfiguredSuffix is #105: a suffix configured with --llm-anonymize-suffix was spliced
// into 9b verbatim, so on the Template field, where the pipeline had already rewritten it, 9b went
// quiet and the host was sent. The raw line was always masked.
//
// The rewrite is not only "a digit becomes <NUM>". The NUM pattern takes a sign and a decimal tail
// with it (eu-1 -> eu<NUM>, v1.2 -> v<NUM>), the HEX pattern rewrites a suffix with no digit at
// all (deadbeef, 0xcafe -> <HEX>), and a suffix that begins with a digit can lose the dot before it
// when the label before it ends in one (db1.2corp -> db<NUM>corp).
func TestIssue105ConfiguredSuffix(t *testing.T) {
	closed := []struct {
		name     string
		suffixes []string
		in, want string
		tmpl     string
		sent     []string // what main sent on the template; none may survive
	}{
		{"digit inside", []string{"k8s"}, "peer db.k8s down", "peer <HOST_1> down", "peer <HOST_1> down", []string{"db."}},
		// A digit at the end: a placeholder ends the suffix, and what may follow it is pinned
		// position by position -- end of text, a space, a dot, a port, another placeholder.
		{"digit at end, end of text", []string{"acme1"}, "dial db.acme1", "dial <HOST_1>", "dial <HOST_1>", []string{"db."}},
		{"digit at end, before a space", []string{"acme1"}, "dial db.acme1 down", "dial <HOST_1> down", "dial <HOST_1> down", []string{"db."}},
		{"digit at end, before a dot", []string{"acme1"}, "reach db.acme1.", "reach <HOST_1>.", "reach <HOST_1>.", []string{"db."}},
		{"digit at end, before a port", []string{"acme1"}, "db.acme1:5432 refused",
			"<HOST_1>:5432 refused", "<HOST_1>:<NUM> refused", []string{"db."}},
		{"digit at end, before a placeholder", []string{"acme1"}, "db.acme1-2 up", "<HOST_1>-2 up", "<HOST_1><NUM> up", []string{"db."}},
		{"all digits after a letter", []string{"az2"}, "peer db7.az2 down", "peer <HOST_1> down", "peer <HOST_1> down", []string{"db"}},
		{"hyphen-digit, the '-' eaten", []string{"eu-1"}, "peer db.eu-1 down", "peer <HOST_1> down", "peer <HOST_1> down", []string{"db."}},
		{"dotted digits, the '.' eaten", []string{"v1.2"}, "peer db.v1.2 down", "peer <HOST_1> down", "peer <HOST_1> down", []string{"db."}},
		{"multi-label, digit inside", []string{"eu1.acme"}, "peer a.eu1.acme down", "peer <HOST_1> down", "peer <HOST_1> down", []string{"a."}},
		// No digit at all: the HEX pattern rewrites an 8+ hex run with a letter, and 0x anywhere.
		{"all hex, no digit", []string{"deadbeef"}, "peer db.deadbeef down", "peer <HOST_1> down", "peer <HOST_1> down", []string{"db."}},
		{"0x prefix", []string{"0xcafe"}, "peer db.0xcafe down", "peer <HOST_1> down", "peer <HOST_1> down", []string{"db."}},
		// A leading digit: the dot survives after a plain label, and is eaten after a digit.
		{"leading digit", []string{"2corp"}, "peer db.2corp down", "peer <HOST_1> down", "peer <HOST_1> down", []string{"db."}},
		{"leading digit, dot eaten", []string{"2corp"}, "peer db1.2corp down", "peer <HOST_1> down", "peer <HOST_1> down", []string{"db"}},
		{"leading digit, dot and hyphen eaten", []string{"2corp"}, "peer db-1.2corp down", "peer <HOST_1> down", "peer <HOST_1> down", []string{"db"}},
		{"two configured suffixes", []string{"k8s", "acme1"}, "peer db.k8s and cache.acme1 down",
			"peer <HOST_1> and <HOST_2> down", "peer <HOST_1> and <HOST_2> down", []string{"db.", "cache."}},
	}
	for _, c := range closed {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				m := New(c.suffixes...)
				got := mustMask(t, m, p.in)
				if got != p.want {
					t.Errorf("host on a configured suffix not masked:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
				for _, frag := range c.sent {
					if strings.Contains(got, frag) {
						t.Errorf("a fragment main sent survived: %q still in %q", frag, got)
					}
				}
				if r := m.Restore(got); r != p.in {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", p.in, r)
				}
			}
		})
	}

	// The logfmt branch carries the msg's text templatized, so the same leak had a second wrapper.
	t.Run("logfmt msg", func(t *testing.T) {
		const raw = `time="2026-07-13T12:00:00Z" level=error msg="peer db.k8s down"`
		tIn, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: raw}))
		if tIn != `time=<STR> level=error msg="peer db.k<NUM>s down"` {
			t.Fatalf("premise broken: the logfmt template is %q", tIn)
		}
		m := New("k8s")
		if got, want := mustMask(t, m, tIn), `time=<STR> level=error msg="peer <HOST_1> down"`; got != want {
			t.Errorf("host on a configured suffix not masked in a logfmt template:\n got:  %q\n want: %q", got, want)
		}
	})

	// Moved, not closed: corp1 was masked on main, by the built-in "corp". On the template 9b read
	// db.corp<NUM> as a host on corp and left the <NUM> beside the tag, so no host text was sent;
	// the value is one host now, as on the raw line. A suffix with a built-in prefix is the one
	// shape where main's output was already safe.
	t.Run("built-in prefix (corp1)", func(t *testing.T) {
		tIn, _ := pipeline.Templatize("dial db.corp1 down")
		if got := mustMask(t, New("corp1"), tIn); got != "dial <HOST_1> down" {
			t.Errorf("corp1 on the template: got %q, want %q", got, "dial <HOST_1> down")
		}
	})

	// Byte-identical with main, raw and templatized. Every built-in suffix, under the default
	// Mapper and under one with a rewritten configured suffix beside it; a configured suffix the
	// pipeline leaves alone; a host on a configured suffix inside a URL, which 9a has always had;
	// and a configured suffix glued to a word character, which is no host on the raw line and must
	// not become one on the template -- the placeholder carries the same boundary the digit did.
	type sameRow struct {
		name     string
		suffixes []string
		in, want string
		tmpl     string
	}
	var same []sameRow
	for _, s := range defaultHostSuffixes {
		for _, extra := range [][]string{nil, {"k8s"}} {
			same = append(same, sameRow{fmt.Sprintf("built-in %s, extra %v", s, extra), extra,
				"peer db7." + s + " down", "peer <HOST_1> down", "peer <HOST_1> down"})
		}
	}
	same = append(same,
		sameRow{"configured, no rewrite", []string{"acmecloud"}, "peer db7.acmecloud down", "peer <HOST_1> down", "peer <HOST_1> down"},
		sameRow{"configured, inside a url", []string{"k8s"}, "get https://db.k8s/x", "get https://<HOST_1>/x", "get https://<HOST_1>/x"},
		sameRow{"configured, glued to a letter", []string{"acme1"}, "peer db.acme1x up", "peer db.acme1x up", "peer db.acme<NUM>x up"},
		sameRow{"configured, glued to '_'", []string{"acme1"}, "peer db.acme1_x up", "peer db.acme1_x up", "peer db.acme<NUM>_x up"},
		sameRow{"built-in prefix, glued to a letter", []string{"corp1"}, "peer db.corp1x up", "peer db.corp1x up", "peer <HOST_1><NUM>x up"},
		// An all-digit suffix is accepted and masks on the raw line. On the template a label that
		// ends in a digit merges with it into one <NUM>, and "word<NUM>" carries no suffix to
		// recognise, so it stays as on main rather than masking every word before a number.
		sameRow{"all-digit suffix, dot eaten", []string{"2"}, "peer v1.2 up", "peer <HOST_1> up", "peer v<NUM> up"},
	)
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(c.suffixes...), p.in); got != p.want {
					t.Errorf("a shape outside #105 moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}
}

// TestBareHostPatternUnchanged holds 9b to main's pattern, byte for byte, wherever no configured
// suffix is one the pipeline rewrites -- the built-ins alone, and a configured suffix with nothing
// for the pipeline to touch. #105 adds forms only for a suffix the pipeline damages. The expected
// source is main's construction, spelled out here rather than read from the package.
func TestBareHostPatternUnchanged(t *testing.T) {
	mainPattern := func(suffixes []string) string {
		label, inner := `(?:[a-z0-9_]|`+pipelinePH+`)`, `(?:[a-z0-9_-]|`+pipelinePH+`)`
		quoted := make([]string, len(suffixes))
		for i, s := range suffixes {
			quoted[i] = regexp.QuoteMeta(s)
		}
		return `(?i)\b(?:` + label + `(?:` + inner + `*` + label + `)?\.)+(?:` + strings.Join(quoted, "|") + `)\b`
	}
	bareHostOf := func(dets []detector) string {
		var found []string
		for _, d := range dets {
			if d.tag == tagHost && d.group == 0 {
				found = append(found, d.re.String())
			}
		}
		if len(found) != 1 {
			t.Fatalf("want exactly one HOST rule with group 0 (9b), found %d", len(found))
		}
		return found[0]
	}
	for _, c := range []struct {
		name  string
		dets  []detector
		extra []string
	}{
		{"baseDetectors", baseDetectors, nil},
		{"New()", New().dets, nil},
		{"New(acmecloud)", New("acmecloud").dets, []string{"acmecloud"}},
		{"New(acmecloud, my-cloud)", New("acmecloud", "my-cloud").dets, []string{"acmecloud", "my-cloud"}},
	} {
		want := mainPattern(append(append([]string{}, defaultHostSuffixes...), c.extra...))
		if got := bareHostOf(c.dets); got != want {
			t.Errorf("%s: 9b moved:\n got:  %s\n want: %s", c.name, got, want)
		}
	}
}

// TestUUIDOutsideAHostKeepsItsOwnTag is the other half of #48's tag change, and it is not
// optional.
//
// Moving detector 6 behind both host detectors means every other detector now gets first refusal
// on a UUID, and 9a's group class admits every character a UUID contains. Reasoning that
// (label\.)+suffix cannot reach a dotless value is not enough: without this test, "a UUID in a
// hostname became HOST" could quietly become "a UUID became HOST", and TestTypeTags — which
// checks Contains — would not notice.
//
// So these are exact outputs, one per position a UUID can occupy without being part of a host.
func TestUUIDOutsideAHostKeepsItsOwnTag(t *testing.T) {
	const uuid = "550e8400-e29b-41d4-a716-446655440000"
	cases := []struct {
		name, in, want string
	}{
		// Standing alone: no scheme for 9a, no dot or private suffix for 9b.
		{"bare", "request " + uuid + " timed out", "request <UUID_1> timed out"},
		// In a URL PATH: 9a's group class excludes '/', so it stops at the authority.
		{"url path", "GET https://api.acme.com/orders/" + uuid + " 404",
			"GET https://<HOST_1>/orders/<UUID_1> 404"},
		// In a home path: detector 6 still precedes detector 10, which is why it is placed
		// between 9b and 10 rather than at the end of the chain — at the end this would come
		// back <USER_1>.
		{"home path", "loaded from /home/" + uuid + "/app.log",
			"loaded from /home/<UUID_1>/app.log"},
		// Beside a real bare host, which now numbers first. Both keep their own tag.
		{"beside a host", "id " + uuid + " on worker.svc", "id <UUID_1> on <HOST_1>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			if got := mustMask(t, m, c.in); got != c.want {
				t.Errorf("a UUID outside a host changed tag:\n in:   %q\n got:  %q\n want: %q",
					c.in, got, c.want)
			}
		})
	}
}

// guaranteed to contain whatever placeholders today's detectors actually mint. A detector added
// later whose group can chew on our own tags goes red here without anyone thinking of the shape.
//
// It is deliberately stated on the GROUP rather than on the whole match. Both credential
// fallbacks read one of our tags on purpose, as context, to find the half detector 4 could not
// reach — that is safe, and a test phrased as "no detector may match placeholder syntax" would
// forbid the fix rather than guard it.
func TestNoDetectorGroupLandsOnOurOwnOutput(t *testing.T) {
	for _, r := range completenessRows {
		payload := payloadOf(r.prefix) + r.secret + payloadOf(r.suffix)
		masked, err := New().Mask(payload)
		if err != nil {
			t.Errorf("%s: Mask(%q) errored: %v", r.name, payload, err)
			continue
		}
		if !regexp.MustCompile(phPat).MatchString(masked) {
			t.Errorf("%s: masking %q produced no placeholder at all: %q", r.name, payload, masked)
			continue
		}
		// The placeholders actually present, as spans. The claim is about OVERLAP with these,
		// not about matching somewhere in a string that happens to contain them: residual
		// plaintext beside a placeholder is a coverage question, and a different test's.
		tags := regexp.MustCompile(phPat).FindAllStringIndex(masked, -1)
		for i, d := range baseDetectors {
			for _, loc := range d.re.FindAllStringSubmatchIndex(masked, -1) {
				gs, ge := loc[2*d.group], loc[2*d.group+1]
				if gs < 0 {
					continue
				}
				for _, tag := range tags {
					if gs < tag[1] && tag[0] < ge {
						t.Errorf("%s: detector %d (%s, group %d) landed on a placeholder this package minted:\n"+
							" masked: %q\n group:  %q\n tag:    %q\n pattern: %s",
							r.name, i, d.tag, d.group, masked,
							masked[gs:ge], masked[tag[0]:tag[1]], d.re.String())
					}
				}
			}
		}
	}
}

// TestRestoreHasNoNestedMapping asserts that Mask never produces a NESTED mapping — one whose
// VALUE contains another mapping's TOKEN.
//
// It used to guard Restore's correctness: Restore ranged over a Go map with one substitution per
// token, so a nested mapping resolved by chance. Since #50 Restore resolves nesting by rule
// (TestRestoreResolvesByRule), and that reason is gone. Three remain:
//
//   - A nested mapping is what a detector's target group landing on one of our tags looks like
//     in the mapping table, and the inertness invariant forbids exactly that. It is #46's first
//     ground for rejecting a wider detector 4, and it still stands.
//   - Restore expands a known token inside a value, and cannot tell a nesting Mask created from an
//     original that literally contained token-shaped text. With no nesting the question is moot.
//   - It reads the mapping a SHARED Mapper accumulates across lines, which
//     TestNoDetectorGroupLandsOnOurOwnOutput, checking one masked string at a time, does not.
//
// The property holds because every detector's target group excludes '<' and '>', so no captured
// value can contain one of our tags; this goes red the moment one starts capturing one.
func TestRestoreHasNoNestedMapping(t *testing.T) {
	tag := regexp.MustCompile(phPat)

	check := func(t *testing.T, name string, m *Mapper) {
		t.Helper()
		for token, original := range m.byToken {
			for _, nested := range tag.FindAllString(original, -1) {
				if _, ok := m.byToken[nested]; ok {
					t.Errorf("%s: mapping %s -> %q contains another mapping's token %s.\n"+
						"A detector's target group landed on one of our own tags, which the inertness invariant forbids.",
						name, token, original, nested)
				}
			}
		}
	}

	// Every completeness row, which is the corpus that covers every detector by construction.
	for _, r := range completenessRows {
		m := New()
		if _, err := m.Mask(payloadOf(r.prefix) + r.secret + payloadOf(r.suffix)); err != nil {
			continue // TestMaskingIsComplete reports this
		}
		check(t, r.name, m)
	}
	// And the templatized composition, where a detector reads a value the pipeline already
	// rewrote — the case where a class is likeliest to have been widened one character too far.
	for _, r := range templatizedRows {
		in, _ := pipeline.Templatize(payloadOf(r.text))
		m := New()
		if _, err := m.Mask(in); err != nil {
			continue // TestTemplatizedMaskingIsComplete reports this
		}
		check(t, r.name, m)
	}
	// One mapper reused across several lines, as a real request does: Explain masks the trigger,
	// the template and every context line through the SAME Mapper, so the map that Restore walks
	// is the union of all of them and nesting could straddle two fields.
	shared := New()
	for _, line := range []string{
		"FATAL pg connect failed dsn=postgres://sk-livekeyabcdefghij0123456789XYZ:hunter2@localhost:5432/app",
		"FATAL redis auth rejected dsn=redis://:s3cr3tpw@cache.acme.internal:6379/0",
		"FATAL peer 2001:db8::dead:beef unreachable from /home/deploy2prod",
		"FATAL mail to user01@corp.example.com bounced via api-gw7.prod.acme.com",
	} {
		if _, err := shared.Mask(line); err != nil {
			t.Fatalf("Mask(%q) errored: %v", line, err)
		}
	}
	check(t, "shared mapper", shared)
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
	// The leak-direction forms (#42). The pipeline drops some of them on purpose (#33), and any
	// narrowing here that drops one of them is disclosure. Each must stay one placeholder.
	{"ipv6/loopback", "bind ", "::1", " port"},
	{"ipv6/loopback-bracketed-port", "listen [", "::1", "]:8080"},
	{"ipv6/zone-id", "via ", "fe80::1", "%eth0 dev"},
	{"ipv6/prefix-one-group", "route ", "fe80::", "/64 dev"},
	{"ipv6/prefix-two-groups", "route ", "2001:db8::", "/32 dev"},
	{"ipv6/leading-elision", "from ", "::ffff:0:0", " y"},
	{"ipv6/bracketed-short-port", "peer [", "2001:db8::1", "]:8080 closed"},
	{"ipv6/glued-colon", "peer:", "2001:db8::1", " closed"},
	{"ipv6/glued-equals", "x=", "fe80::1", " up"},
	{"ipv6/glued-underscore-one-group", "id_", "fe80::", "/64 up"},
	{"ipv6/glued-underscore-letter-led", "id_", "fe80::a", " up"},
	{"ipv6/glued-underscore", "addr_", "2001:db8::1", " up"},
	{"ipv6/letter-led-last-group", "via ", "fe80::a", " dev"},
	{"ipv6/eight-groups", "peer ", "2001:0db8:85a3:0000:0000:8a2e:0370:7334", " down"},
	{"ipv6/eight-groups-glued", "peer:", "2001:0db8:85a3:0000:0000:8a2e:0370:7334", " down"},
	{"ipv6/upper-case", "peer ", "2001:DB8::DEAD:BEEF", " down"},
	{"ipv6/upper-case-short", "via ", "FE80::1", " dev"},
	{"ipv6/upper-case-prefix", "route ", "FE80::", "/64 dev"},
	{"ipv6/upper-case-leading-elision", "from ", "::FFFF:0:0", " y"},
	// #134: a "::"-led address glued on by ':' (rule 7a). The colon that glues it, and the hex
	// character of a hex-ending token, go with it (TestAcceptedOverMasking), so they are declared
	// as part of the secret. Each row ends at a different boundary, flush.
	{"ipv6/glued-colon-leading-elision", "peer", ":::ffff:0:0", ",down"},
	{"ipv6/glued-colon-after-hex", "sr", "c:::a:b:c", ")"},
	{"ipv6/glued-colon-loopback", "peer", ":::1", "]"},
	{"ipv6/glued-colon-end-of-text", "peer", ":::1", ""},
	{"ipv6/glued-equals-colon", "x=", ":::1", ";"},
	// #42's constraint: refusing the bare "::" alone would have sent the last group of this one.
	{"ipv6/glued-colon-eight-groups", "x", ":::1:2:3:4:5:6:7:8", `"`},
	{"ipv6/glued-colon-upper-case", "PEER", ":::FFFF:0:0", "'"},
	{"ipv6/netstat-port", "tcp6 0 0 ", ":::22", " \x00::\x00:* LISTEN"},
	// A prefix length, and a home path straight after: the home rule's value starts after the '/',
	// which is context to 7a and not masked.
	{"ipv6/glued-colon-prefix-length", "dst", ":::1", "/128 dev"},
	{"ipv6/glued-colon-before-a-home-path", "at", ":::1", "/home/\x00bob\x00/x"},
	// A zone id: '%' is in the userinfo and email classes, but those rules run before 7a.
	{"ipv6/glued-colon-zone-id", "peer", ":::1", "%lo up"},
	// A dot that ends a sentence: followed by a space or by nothing, it continues no address or name.
	{"ipv6/glued-colon-sentence-dot", "listening on ", ":::22", ". Retrying"},
	{"ipv6/glued-colon-sentence-dot-end-of-text", "peer", ":::1", "."},
	// The control: bracketed, the unspecified address was always masked whole.
	{"ipv6/unspecified-bracketed-port", "listen [", "::", "]:22"},
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
	// #46, both halves. Detector 4's group spans user:pass as ONE value, so a placeholder
	// minted anywhere inside that span blocks the whole detector and the OTHER half ships in
	// the clear. Which half survives depends only on which half was recognisable, so the defect
	// is symmetric and so are the rows: the declared secret is the half detector 4 can no longer
	// reach, and the phMark is the placeholder that blocked it.
	{"url-credentials/premasked-username",
		"dsn=postgres://" + phMark + "sk-abcdefghij0123456789XYZ" + phMark + ":", "hun2ter",
		"@" + phMark + "db.acme.com" + phMark + "/prod"},
	{"url-credentials/premasked-password",
		"dsn=postgres://", "app7user",
		":" + phMark + "sk-abcdefghij0123456789XYZ" + phMark + "@" + phMark + "db.acme.com" + phMark + "/prod"},
	// The empty halves, a DIFFERENT defect from #46 and closed alongside it: the credential
	// detector required at least one character on each side of the colon, and a Redis DSN has no
	// username (Redis had none before 6.0). Nothing is pre-masked in these two — the detector
	// simply never covered the shape.
	//
	// #55's merge moved the SPAN here rather than whether the value is masked. With the colon an
	// ordinary userinfo character, the rule captures it along with the half beside it, so the
	// declared secret carries the colon and the prefix/suffix no longer do. That is a traded
	// signal, not a tidy-up, and it is pinned as one in TestAcceptedOverMasking rather than left
	// to be inferred from these two rows.
	{"url-credentials/empty-username",
		"dsn=redis://", ":s3cr3tpw", "@" + phMark + "cache.acme.internal" + phMark + ":6379/0"},
	{"url-credentials/empty-password",
		"dsn=postgres://", "svc7acct:", "@" + phMark + "db.acme.com" + phMark + "/prod"},
	// #55. A userinfo with NO password, and therefore no colon for any credential detector to
	// anchor on. One row per authority shape, because whether the username actually left the
	// process depended entirely on what followed the '@' — #46's table inverted. The bare-host
	// and loopback rows are the shapes that leaked outright; the dotted and address rows were
	// rescued by the email and host detectors, under the wrong tag and only PARTLY (see
	// TestIssue55PasswordlessUserinfo for the one character that defeats each rescue).
	//
	// Text is flush against the value at both ends by construction: the prefix ends with "//"
	// and the suffix opens with the '@' the rule anchors on, so neither end has a separator to
	// hide an over-reach behind.
	{"url-credentials/no-password/bare-host",
		"dsn=postgres://", "appuser", "@" + phMark + "db" + phMark + ":5432/app"},
	{"url-credentials/no-password/loopback",
		"dsn=postgres://", "appuser", "@" + phMark + "localhost" + phMark + ":5432/app"},
	{"url-credentials/no-password/dotted-host",
		"dsn=postgres://", "appuser", "@" + phMark + "db.acme.com" + phMark + "/prod"},
	{"url-credentials/no-password/private-host",
		"dsn=postgres://", "appuser", "@" + phMark + "cache.acme.internal" + phMark + ":6379/0"},
	{"url-credentials/no-password/address-host",
		"dsn=postgres://", "appuser", "@" + phMark + "10.0.0.5" + phMark + ":5432/app"},
	// Green BEFORE and AFTER #55, which is the whole point of them. The merge makes the colon an
	// ordinary userinfo character, so the two shapes whose reading depends on a colon had to be
	// CHECKED rather than assumed to be unaffected.
	//
	// A multi-colon userinfo is legal — RFC 3986 §3.2.1 puts ':' in the production — and its span
	// is byte-identical before and after, because detector 4's PASSWORD class already admitted
	// ':'. Only the first colon was ever structural, and only as a required delimiter; the rest
	// were already just characters, which is why removing the first one's special status changes
	// nothing here.
	//
	// A percent-encoded '@' is what makes a password containing '@' legal at all, so
	// "postgres://user:p%40ss@host" is an ordinary DSN rather than an edge case. It works, and it
	// works by construction rather than by luck of example: the classes are NEGATED, so '%', '4'
	// and '0' are three ordinary characters and nothing anywhere in this package special-cases
	// pct-encoding. Nothing relied on that until this row — there was no test for it.
	{"url-credentials/multi-colon",
		"dsn=postgres://", "appuser:hun:ter2", "@" + phMark + "db.acme.com" + phMark + "/prod"},
	{"url-credentials/pct-encoded-at",
		"dsn=postgres://", "appuser:p%40ss", "@" + phMark + "db.acme.com" + phMark + "/prod"},
	// #58. A RAW '@' inside a half, and the credential is one value up to the LAST '@'. Flush at
	// both ends by construction -- "//" before, the delimiting '@' after -- so an over-reach past
	// the real delimiter and a stop at the wrong one both fail here. One row per position the '@'
	// can occupy, plus #46's two cells with a raw '@' in the half the fallback has to reach; the
	// 4c row is the one that needs detector 4 to DECLINE (see authorityTail), and is red under
	// "admit '@' and nothing else".
	{"url-credentials/raw-at-in-password",
		"dsn=postgres://", "user:p@ss", "@" + phMark + "db" + phMark + ":5432/app"},
	{"url-credentials/raw-at-in-username",
		"dsn=postgres://", "us@er:pass", "@" + phMark + "db.acme.com" + phMark + "/prod"},
	{"url-credentials/raw-at-no-password",
		"dsn=postgres://", "us@er", "@" + phMark + "db" + phMark + ":5432/app"},
	{"url-credentials/raw-at-two",
		"dsn=postgres://", "user:p@s@s", "@" + phMark + "db" + phMark + ":5432/app"},
	{"url-credentials/premasked-username/raw-at-password",
		"dsn=postgres://" + phMark + "sk-abcdefghij0123456789XYZ" + phMark + ":", "p@ss7",
		"@" + phMark + "db.acme.com" + phMark + "/prod"},
	{"url-credentials/premasked-password/raw-at-username",
		"dsn=postgres://", "us@er7",
		":" + phMark + "sk-abcdefghij0123456789XYZ" + phMark + "@" + phMark + "db.acme.com" + phMark + "/prod"},
	// A replica-set URI. This row proves the host list is NOT swallowed into the credential span
	// -- the '@' that delimits is the one before host1, and the suffix is returned intact. Until
	// #61 the second host was LITERAL in that suffix on purpose, as the reminder that it was sent;
	// it is a marked span now, and it is 9c's.
	{"url-credentials/raw-at-multi-host",
		"dsn=mongodb://", "user:p@ss",
		"@" + phMark + "host1" + phMark + ":27017," + phMark + "host2" + phMark + ":27017/db?replicaSet=rs0"},
	// #61. A later member of a comma-separated authority is detector 9c's, and since #67 it is ONE
	// member per match: the group is the second member, its port outside, which is the boundary 9a
	// draws for a single host. Mask re-applies 9c until nothing changes, so every member after it is
	// a marked span of its own in the suffix. The first member is context -- 9a's tag in the chain,
	// the raw run in this single-detector probe -- so the canonical row's prefix carries host1 as a
	// marked span. (Until #67 the group ran from the second member to the last, interior ports
	// inside, and the secret here was "host2:27017,host3".)
	//
	// Flush at both ends by construction: the prefix ends with the comma after member 1 and the
	// suffix opens with the member's own port, so an over-reach into the next member and a stop
	// short of the port both fail here.
	{"url-host/list-rest", "dsn=mongodb://" + phMark + "host1" + phMark + ":27017,",
		"host2", ":27017," + phMark + "host3" + phMark + ":27017/db?replicaSet=rs0"},
	{"url-host/list-rest/no-ports", "dsn=mysql://" + phMark + "host1" + phMark + ",", "host2",
		"," + phMark + "host3" + phMark + "/db"},
	// The first member is an address literal: detector 8 (or 7) tagged it before 9a ran, 9a
	// declined because its group cannot start on '<', and 9c's context admits the tag. These two
	// rows pass only through the full chain -- 9c alone cannot match the raw address -- which is
	// why the canonical row above comes first.
	{"url-host/list-rest/address-first", "dsn=mongodb://" + phMark + "10.0.0.5" + phMark + ":27017,",
		"host2", ":27017/db"},
	{"url-host/list-rest/ipv6-first", "dsn=mongodb://[" + phMark + "2001:db8::1" + phMark + "]:27017,",
		"host2", ":27017/db"},
	// Greed guard: the host must stop at "/", not eat the path.
	{"url-host", "call https://", "internal.acme.com", "/v1"},
	{"bare-host", "host ", "worker.svc", " failed"},
	// #48. A UUID used as a hostname LABEL. Detector 6 minted a <UUID_n> inside the host
	// detectors' span, and every host class excludes '<' and '>', so the host was masked in part
	// or not at all. One row per shape, because the three fail in three different ways:
	//
	//   leading label  -- 9a's group is required immediately after "://", so a tag in the FIRST
	//                     label kills the whole match and the entire domain shipped with nothing
	//                     behind it (a public suffix is not on the bare-host allowlist).
	//   middle label   -- 9a's group takes the labels before the tag and stops, sending the tail.
	//   bare host      -- 9b's \b re-anchors after the '>' and takes only the private suffix,
	//                     sending every label before the UUID.
	//
	// These rows are placed AFTER the canonical url-host and bare-host rows on purpose:
	// completenessSecretFor returns the FIRST row whose group lands exactly on the secret, and
	// TestEveryUncollapsedDetectorHasATemplatizedRow reads that choice.
	{"url-host/uuid-label-leading", "call https://",
		"550e8400-e29b-41d4-a716-446655440000.blob.core.windows.net", "/x"},
	{"url-host/uuid-label-mid", "call https://",
		"api.550e8400-e29b-41d4-a716-446655440000.acme.com", "/x"},
	{"bare-host/uuid-label", "host ",
		"worker-550e8400-e29b-41d4-a716-446655440000.corp.internal", " failed"},
	// #66. An underscore in a hostname: every host class stopped at it. After the canonical rows,
	// for the same reason as the #48 rows above.
	{"url-host/underscore", "call http://", "my_host", ":8080/x"},
	{"url-host/underscore/leading-label", "call http://", "_svc.acme.com", "/x"},
	{"url-host/list-rest/underscore", "dsn=mongodb://" + phMark + "host1" + phMark + ",", "host_2",
		"," + phMark + "my_db_3" + phMark + "/db"},
	{"bare-host/underscore", "peer ", "db_primary.corp.internal", " down"},
	{"email/underscore-domain", "mail to ", "user@my_host.example.com", " bounced"},
	// #54. An IPv4 address that is a LABEL inside a hostname: detector 8 tagged it alone and every
	// host rule was blocked behind the tag. After the canonical rows, for the same reason as the #48
	// rows. Flush at both ends: a path, a port, a space.
	{"url-host/ipv4-label", "call https://", "10.0.0.5.nip.io", "/x"},
	{"url-host/ipv4-label/label-2", "call https://", "api.10.0.0.5.nip.io", ":8443/x"},
	{"bare-host/ipv4-label", "peer ", "worker-10.0.0.5.corp.internal", " down"},
	// #57. A key glued inside a larger value: detectors 1, 2 and 3b tagged the key alone and every
	// later rule was blocked behind the tag. The userinfo rows are 4d's, one per side the remainder can
	// lie on, and the password side; the host rows are 9e's, a label and a later member. After the
	// canonical rows, for the same reason as the #48 rows.
	{"url-credentials/glued-key", "dsn=s3://", "AKIAIOSFODNN7EXAMPLE.prod", "@" + phMark + "bucket" + phMark + "/x"},
	{"url-credentials/glued-key/before", "dsn=s3://", "prod.sk-abcdefghij0123456789XYZ:secret",
		"@" + phMark + "bucket" + phMark + "/x"},
	{"url-credentials/glued-key/password", "dsn=s3://",
		"user:eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u.prod", "@" + phMark + "bucket" + phMark + "/x"},
	{"url-host/glued-key", "call https://", "AKIAIOSFODNN7EXAMPLE.prod.acme.com", "/x"},
	{"url-host/list-member/glued-key", "dsn=mongodb://" + phMark + "h1" + phMark + ",", "AKIAIOSFODNN7EXAMPLE.prod",
		"," + phMark + "host3" + phMark + "/db"},
	// #108. A key glued to a word character in prose: no detector saw it, and the key was sent. The key
	// is the value and the glue is prose, so the text is flush against the key on the glued side -- an
	// over-reach into the glue and a stop short of the key both fail here. One row per key and per side,
	// a lowercase letter before an AWS key, and the run longer than a key. After the canonical rows, for
	// the same reason as the #48 rows.
	{"aws-key/glued-underscore", "key ", "AKIAIOSFODNN7EXAMPLE", "_prod used"},
	{"aws-key/glued-letter", "key id", "AKIAIOSFODNN7EXAMPLE", " used"},
	{"aws-key/run", "key ", "AKIAIOSFODNN7EXAMPLEPROD", " used"},
	// Since #51 the '_prod' after an sk- key is the key's: '_' is in its alphabet.
	{"sk-key/glued", "key my_", "sk-" + "abcdefghij0123456789XYZ_prod", " used"},
	{"jwt/glued", "auth token_", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u", " rejected"},
	// #110. A key glued inside a bearer value: 1 or 2 tagged the key alone, and 3a could not claim the rest.
	// The whole value is the secret, flush against the word on one side and a space on the other. After the
	// canonical rows, for the same reason as the #48 rows.
	{"bearer/glued-key", "hdr Bearer ", "AKIAIOSFODNN7EXAMPLE.prod", " expired"},
	{"bearer/glued-jwt", "hdr Bearer ", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u~prod", " expired"},
	// #52. A scheme with a digit in it: masked on the raw line, and the row exists for
	// TestEveryDetectorsContextSurvivesTemplating, for which the '3' is a context digit -- the shape
	// whose templatized form silenced 9a.
	{"url-host/digit-scheme", "get s3://", "bucket.example.com", "/key failed"},
	{"home-dir", "loaded from /home/", "maxie", "/go/pkg/mod"},
	// The home-dir rule's second literal, so the context check proves it digit-free as well.
	{"home-dir/users", "path /Users/", "jane", "/app.log"},
	// #90. A closer after the name is the value's wrapper or the sentence's, not the name's, so it stays outside
	// the placeholder: flush against the name, a closing quote and a sentence dot. After the canonical rows, for
	// the same reason as the #48 rows.
	{"home-dir/closing-quote", `cannot read "/home/`, "bob", `" twice`},
	{"home-dir/sentence-dot", "cannot read /home/", "bob", ". Retrying"},
	// #51. The current key formats carry '-' and '_' in the tail, and a closer after one stays outside the
	// placeholder. After the canonical rows, for the same reason as the #48 rows.
	{"sk-key/proj", "key ", skProjKey, " rejected"},
	{"sk-key/svcacct", "key ", skSvcacctKey, " rejected"},
	{"sk-key/anthropic", "key ", skAntKey, " rejected"},
	{"sk-key/underscore-inside", "key ", skUnderscoreKey, " rejected"},
	{"sk-key/hex-after-prefix", "key ", skHexKey, " rejected"},
	{"sk-key/env-var-end", "OPENAI_API_KEY=", skProjKey, ""},
	{"sk-key/closing-quote", `rejected "`, skProjKey, `" twice`},
	{"sk-key/sentence-dot", "key ", skProjKey, ". Retrying"},
	// #114. One canonical row per format family first, in vendorKeys' order, then the rest of each family,
	// the query values where a Google key is commonest, and the edges. After the canonical rows, for the
	// same reason as the #48 rows.
	{"vendor-key/stripe", "key ", stripeLiveSecret, " rejected"},
	{"vendor-key/github", "key ", githubClassicSecret, " rejected"},
	{"vendor-key/github-pat", "key ", githubPATSecret, " rejected"},
	{"vendor-key/slack", "key ", slackBotSecret, " rejected"},
	{"vendor-key/google", "key ", googleAPISecret, " rejected"},
	{"vendor-key/stripe-restricted", "key ", stripeRestrictedSecret, " rejected"},
	{"vendor-key/stripe-webhook", "key ", stripeWebhookSecret, " rejected"},
	{"vendor-key/github-server", "key ", githubServerSecret, " rejected"},
	{"vendor-key/slack-user", "key ", slackUserSecret, " rejected"},
	{"vendor-key/slack-app", "key ", slackAppSecret, " rejected"},
	{"vendor-key/google-query-value", "https://\x00maps.googleapis.com\x00/maps/api/geocode/json?address=x&key=", googleAPISecret, ""},
	{"vendor-key/stripe-query-value", "GET https://\x00api.stripe.com\x00/v1/charges?key=", stripeLiveSecret, " 401"},
	{"vendor-key/github-query-value", "GET https://\x00api.github.com\x00/user?access_token=", githubClassicSecret, " 401"},
	{"vendor-key/env-var-end", "GOOGLE_API_KEY=", googleAPISecret, ""},
	{"vendor-key/underscore-before", "token_", slackBotSecret, " used"},
	{"vendor-key/closing-quote", `rejected "`, githubPATSecret, `" twice`},
	{"vendor-key/sentence-dot", "key ", stripeWebhookSecret, ". Retrying"},
	// #49: an IPv6 address whose last 32 bits are a dotted quad, every form RFC 3986's grammar allows
	// (rule 7b, and 7a's dotted-quad alternative when glued on by ':'). The compatible form is first, so
	// it is 7b's canonical row: its template, ::<IP>, keeps a shape 7b reads. Each row ends at a
	// different boundary, flush.
	{"ipv6/dotted-quad/compatible", "bind ", "::192.0.2.1", " port"},
	{"ipv6/dotted-quad/mapped", "peer ", "::ffff:192.0.2.1", " down"},
	{"ipv6/dotted-quad/siit", "via ", "::ffff:0:192.0.2.1", ","},
	{"ipv6/dotted-quad/nat64", "nat ", "64:ff9b::192.0.2.1", ")"},
	{"ipv6/dotted-quad/full", "peer ", "0:0:0:0:0:ffff:192.0.2.1", " down"},
	{"ipv6/dotted-quad/upper-case", "peer ", "::FFFF:192.0.2.1", `"`},
	{"ipv6/dotted-quad/bracketed-port", "peer [", "::ffff:192.0.2.1", "]:443 refused"},
	{"ipv6/dotted-quad/prefix-length", "route ", "::ffff:0.0.0.0", "/96 dev"},
	{"ipv6/dotted-quad/zone-id", "via ", "::ffff:192.0.2.1", "%eth0 dev"},
	{"ipv6/dotted-quad/sentence-dot", "from ", "::ffff:192.0.2.1", ". Retrying"},
	{"ipv6/dotted-quad/end-of-text", "from ", "::ffff:192.0.2.1", ""},
	{"ipv6/dotted-quad/after-equals", "addr=", "::ffff:192.0.2.1", ";"},
	{"ipv6/dotted-quad/after-underscore", "id_", "::ffff:192.0.2.1", " up"},
	// Unbracketed, the port goes with the address (TestAcceptedOverMasking), so it is declared as part
	// of the secret; so is the hex character of a hex-ending token, as on 7.
	{"ipv6/dotted-quad/unbracketed-port", "peer ", "::ffff:192.0.2.1:443", " refused"},
	{"ipv6/dotted-quad/after-hex", "sr", "c::ffff:192.0.2.1", ")"},
	// Glued on by ':' (7a). The first is the RESIDUAL #49 row of #134's right-boundary table.
	{"ipv6/dotted-quad/glued-mapped", "peer", ":::ffff:192.168.1.1", " down"},
	{"ipv6/dotted-quad/glued-compatible", "peer", ":::192.0.2.1", " down"},
	{"ipv6/dotted-quad/glued-upper-case", "PEER", ":::FFFF:192.0.2.1", "'"},
	{"ipv6/dotted-quad/glued-port", "peer", ":::ffff:192.0.2.1:443", " refused"},
	{"ipv6/dotted-quad/glued-after-hex", "sr", "c:::ffff:192.0.2.1", ")"},
	// #59: a raw '/' in a userinfo half. Rule 4e masks the authority whole, host and port included,
	// so the declared secret runs to the path.
	{"url-credentials/raw-slash-in-password", "dsn=postgres://", "svcuser:hun/ter2@db.acme.com", "/prod"},
	{"url-credentials/raw-slash-in-username", "dsn=postgres://", "svc/user:hunter2@db:5432", "/app"},
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
// more character. The two IPv6 patterns stay two on purpose (#42), and that row is now one of the
// prompt-cost rows in ipv6_compare_test.go, which says why no rule can separate it from an address.
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
		// The email detector carries no leading \b, and these four rows are the ENTIRE cost of
		// that, enumerated rather than sampled. A match must begin either with a character of
		// the local-part class or with a pipeline placeholder. If it begins with a WORD
		// character (a letter, a digit, or '_'), \b could only fail where the preceding
		// character is also a word character — which is itself in the class, so leftmost
		// matching would have started there and the span is identical either way. That leaves
		// exactly the four non-word members of the class as the shapes \b excluded, and a
		// leading run of them is absorbed the same way. Each costs one character inside a
		// placeholder that Restore puts back exactly; the shape \b also excluded, a local part
		// the pipeline collapsed whole, is not over-masking but the leak it was causing, and is
		// pinned in templatizedRows instead.
		{"email leading dot absorbed", "mail .alice@corp.example.com", "mail <EMAIL_1>"},
		{"email leading percent absorbed", "mail %alice@corp.example.com", "mail <EMAIL_1>"},
		{"email leading plus absorbed", "mail +alice@corp.example.com", "mail <EMAIL_1>"},
		{"email leading hyphen absorbed", "mail -alice@corp.example.com", "mail <EMAIL_1>"},
		{"email leading run absorbed", "mail ..-alice@corp.example.com", "mail <EMAIL_1>"},
		// INHERITED, not introduced. A URL with a query and no path separator reads as credentials,
		// because the userinfo class admits '?' and '=' and only '/' would have stopped it. Detector
		// 4 has done this since v0.4.0; the one-sided fallbacks added for #46 reuse the same class
		// and inherit it unchanged. Pinned here so that if they ever widen it, the diff shows up as a
		// behaviour change rather than passing as "detector 4 always did that".
		{"url query absorbed as credentials", "https://api.acme.com?q=a:b@c", "https://<TOKEN_1>@<HOST_1>"},
		// #55's widening, and it is the colon-LESS sibling of the row above rather than a change to
		// it: that input has a colon, so detector 4 has always owned it and its output is
		// byte-identical before and after the merge.
		//
		// This one was absent from the table because it was not over-masked — it was UNDER-masked.
		// Today the input sends "api.acme.com" in the clear: 9a is compiled leftmost-longest, so it
		// reads "api.acme.com?q=a@" as userinfo CONTEXT, steps over it, and masks only the "b"
		// behind it. So the widening CLOSES a host leak as well as costing an over-mask, and that
		// is what the trade rests on — the package's over-mask-rather-than-under-mask asymmetry is
		// the second argument here, not the only one.
		//
		// LEFT AS IT IS BY #58, and that was measured rather than assumed. Admitting '@' into the
		// class does not move this row: the run already reached the only '@' there is. Bounding
		// the run at '?' and '#' -- RFC 3986 §3.2's other half, which would have RETIRED this row
		// with the host still covered by 9a -- was measured and rejected, because it reopens a
		// leak: "postgres://user:p?ss@db" is fully masked today and would send "p?ss" and "db".
		// net/url errors on a raw '?', '#' and '/' in a password identically, so those two
		// belong to #59's class and not to this fix.
		{"url query absorbed as credentials, no colon",
			"https://api.acme.com?q=a@b", "https://<TOKEN_1>@<HOST_1>"},
		// #58's two costs, named. Reading to the LAST '@' reaches one query parameter further
		// when the query carries two of them -- before, "&r=c@d" stayed in the clear -- and a
		// credentialed URL whose QUERY carries an '@' now absorbs the host into the credential
		// token, with the query's tail tagged as the host. Both are over-masks: nothing that was
		// masked is unmasked, and Restore is exact. The second is the one new shape.
		{"url query with two '@'s absorbed", "https://api.acme.com?q=a@b&r=c@d", "https://<TOKEN_1>@<HOST_1>"},
		{"host absorbed when the query carries an '@'",
			"postgres://user:pass@db?x=a@b", "postgres://<TOKEN_1>@<HOST_1>"},
		// THE TRADED SIGNAL, recorded on purpose rather than arriving as a side effect of the
		// merge. Detector 4 now spans the whole userinfo, so a colon beside an ABSENT half is
		// captured with the half that is there: "redis://:<TOKEN_1>@" becomes "redis://<TOKEN_1>@".
		//
		// Restore is unaffected — it substitutes the exact matched text — so nothing is lost on the
		// way home. What is lost is a hint on the way OUT: the leading colon used to tell the model
		// the missing half was the USERNAME, which is what makes a Redis DSN recognisably a Redis
		// DSN. That is a real cost, judged the cheap half of "one rule instead of two", and written
		// down here so the judgement is visible rather than implied by two moved completeness rows.
		{"empty username: the colon is absorbed",
			"dsn=redis://:s3cr3tpw@cache.acme.internal:6379/0", "dsn=redis://<TOKEN_1>@<HOST_1>:6379/0"},
		{"empty password: the colon is absorbed",
			"postgres://svcacct:@db", "postgres://<TOKEN_1>@<HOST_1>"},
		// The degenerate end of the same widening: a userinfo that is nothing BUT a delimiter now
		// mints a placeholder for a value that does not exist, and tells the model there is a
		// credential where there is none. Nothing leaks either way — there is nothing there to
		// leak — so this is the cheapest cell in the trade, and the most obviously silly, which is
		// why it is stated rather than left for someone to find on a card.
		{"bare colon userinfo", "postgres://:@db", "postgres://<TOKEN_1>@<HOST_1>"},
		// A public git remote, which is ordinary in a clone log and a Go module stack frame. Before
		// #55 the EMAIL detector took "git@github.com" whole, so this was already masked — under a
		// tag saying "address" for a value that is a username and a host. Nothing is disclosed
		// either way and both tags are now right, but the OUTPUT moves, so it is pinned here rather
		// than left to surprise a reader.
		{"git remote username absorbed",
			"git+ssh://git@github.com/org/repo.git cloned",
			"git+ssh://<TOKEN_1>@<HOST_1>/org/repo.git cloned"},
		// #61's first cost, the port asymmetry -- interior ports inside one placeholder for hosts
		// 2..n -- sat here until #67 retired it: every member is its own placeholder now and every
		// port is visible. The row is TestIssue67LaterMemberTag's.
		//
		// THE CSV CELL, and it is a NEW KIND OF COST in this table. Every row above costs a
		// placeholder in a restatement that is already lossy; this one costs the PAYLOAD -- a status
		// code and a duration are what an explanation turns on. The asymmetry that decides this
		// package (over-mask rather than under-mask) still holds and the row stays, but the next
		// over-mask decision should not cite that asymmetry without asking which kind of cost it is
		// paying. The boundary is narrow: a comma with NO space directly after a path-less URL. A
		// space after the comma, a path before it, or no scheme on the line are all untouched (see
		// TestIssue61MultiHostAuthority's negatives).
		//
		// Since #67 each cell is its OWN placeholder, so the characters masked are exactly those
		// masked before and only the layout moved: equal cells now share one placeholder, which tells
		// the model they are equal, and a cell with a character outside the host class ends the run
		// where it always did. Past 64 cells Mask fails closed and the escalation is skipped (#67).
		{"csv cells after a path-less url absorbed as a host list",
			"https://api.acme.com,200,12ms", "https://<HOST_1>,<HOST_2>,<HOST_3>"},
		{"equal csv cells share a placeholder", "https://a.com,200,200", "https://<HOST_1>,<HOST_2>,<HOST_2>"},
		{"csv run ends at a space, as before", "https://a.com,200,12 ms,x", "https://<HOST_1>,<HOST_2>,<HOST_3> ms,x"},
		{"number after a path-less url absorbed as a host list",
			"https://a.com,42", "https://<HOST_1>,<HOST_2>"},
		// The same class with a bracketed address as the first member: URL-shaped, so 9c applies.
		// A bracketed address in PROSE is not reached -- 9c needs "scheme://" with no whitespace
		// anywhere before the group -- and those negatives are pinned in the #61 test.
		{"word after a url-shaped bracketed address absorbed",
			"https://x@[2001:db8::1],retrying", "https://<TOKEN_1>@[<IP_1>],<HOST_1>"},
		// #66's costs, and they are the whole of them. An over-mask from '_' needs a '_' to enter a
		// group it used to end, and there are exactly four ways in. 9b's \b now starts before a
		// word run glued to a host by '_' -- an identifier prefix, or a snake_case file name on a
		// private suffix, which was untouched before because no \b preceded its last part. A label
		// may now BEGIN with '_', which is what DNS service labels do, so an mDNS service type on
		// .local is a host to 9b. 9a's and 9c's group can END on a '_' glued after the host. And the
		// email domain class admits '_', so a systemd template unit whose instance carries one reads
		// as an address -- the fourth way, missed when this list said three and found by running the
		// change over real journald captures, where it was the ONLY one that fired. Each costs text
		// inside a placeholder that Restore puts back exactly; none sends anything.
		{"identifier glued to a private host by an underscore", "peer env_db.corp.internal down", "peer <HOST_1> down"},
		{"snake_case file name on a private suffix", "loaded app_settings.local", "loaded <HOST_1>"},
		{"mdns service type", "browse _ipp._tcp.local", "browse <HOST_1>"},
		{"trailing underscore after a url host", "see http://acme.com_", "see http://<HOST_1>"},
		{"systemd template unit with an underscore read as an email",
			"Started worker@job_runner.service", "Started <EMAIL_1>"},
		// INHERITED, not introduced, and pinned here for the first time: a systemd template unit is
		// unit@instance.service, which is an address to detector 5 whenever the instance fits its
		// domain class -- and it always has. This is the cost the row above extends to a '_'
		// instance, and it is a cost to TRIAGE rather than to a restatement: the model is told an
		// address started, not which unit did.
		{"systemd template unit read as an email", "Started backup@nightly.service", "Started <EMAIL_1>"},
		// #54's costs. The IPv4-label rule fuses an address with text joined to it by '.' or '-'
		// whenever that text carries a letter, because a name's top label is alphabetic and nothing
		// cheaper tells a name from a word glued to an address. Each row was <IP_1> plus the glued
		// text in the clear before; now the address and the text are one HOST placeholder, so the
		// model is told "a host" where the value was an address beside a word. Nothing is sent that
		// was masked before, and Restore is exact.
		{"file name carrying an address", "dump-10.0.0.5.log written", "<HOST_1> written"},
		{"missing space after a sentence dot", "connected to 10.0.0.5.Retrying", "connected to <HOST_1>"},
		{"word glued before an address", "peer worker-10.0.0.5 down", "peer <HOST_1> down"},
		// A four-part version with a pre-release tag was already an address (<IP_1>-beta, an
		// over-mask inherited from detector 8); it is a host now.
		{"version string with a pre-release tag", "release 1.2.3.4-beta", "release <HOST_1>"},
		// A reverse-DNS name is a real name, and it is masked whole; main sent ".in-addr.arpa" beside
		// the reversed address. Here because the tag moves, not because anything leaked. It was the
		// only shape that changed when this rule ran over one laptop's journal (70,783 records): ten
		// records, raw and templatized alike, all of them names like this one.
		{"reverse-dns name", "PTR 5.0.0.10.in-addr.arpa", "PTR <HOST_1>"},
		// #57's costs. A userinfo with a key glued inside a half is ONE token now, so the colon and
		// the "this half is a key" signal are inside it -- #55's traded-signal class again, and only
		// where a key is glued; a key that is a whole half keeps its own tag. Main sent ".prod" here.
		{"a glued key absorbs the userinfo's halves", "s3://AKIAIOSFODNN7EXAMPLE.prod:secret@bucket", "s3://<TOKEN_1>@<HOST_1>"},
		// 9e reads a host after "://", '@' or ',' and does not check that it is in an authority, so
		// a key glued to labels in those two positions in prose reads as a host: the model is told
		// "a host" where the line carried a key. Main sent ".prod" beside <TOKEN_1>; in the address
		// it sent the domain, and "ops" is sent in both -- detector 5 cannot read a tag, as before.
		{"a glued key after a comma reads as a host", "keys=a,AKIAIOSFODNN7EXAMPLE.prod", "keys=a,<HOST_1>"},
		{"a glued key in an email domain reads as a host", "mail ops@AKIAIOSFODNN7EXAMPLE.prod.example.com", "mail ops@<HOST_1>"},
		// #108's costs. A run that starts with an AWS prefix and continues the key's alphabet past twenty
		// characters is a key, so an uppercase identifier of that shape is a token on the raw line, as it
		// already was on the template wherever it carried a digit. And a lowercase letter before an AWS
		// prefix is glue, so a word ending in one does not stop the run behind it from being a key.
		{"an uppercase run that starts like an aws key is one", "region ASIAPACIFICDATACENTER01 up", "region <TOKEN_1> up"},
		{"a lowercase word glued before an aws-prefixed run", "southeastASIAPACIFICDATACENTER01", "southeast<TOKEN_1>"},
		// #110's cost. A '.' is a bearer character (RFC 6750's b64token, and 3a has always read it so), and
		// it cannot continue a key, so a sentence dot right after a key behind "Bearer" is glue: the value
		// with its dot is one token, and the key alone elsewhere gets a placeholder of its own.
		{"a sentence dot after a bearer key joins the value", "auth Bearer AKIAIOSFODNN7EXAMPLE. retry", "auth Bearer <TOKEN_1> retry"},
		// #51's costs. An sk- tail is the key's alphabet with a witness, so a mixed-case identifier sixteen
		// long after a literal "sk-" is a key, and so is a run of sixteen letters with the words after it.
		// The second was a token on main too, with "-svc" left beside it.
		{"a mixed-case name after sk- is a key", "deploy sk-" + "MyService-Deployment", "deploy <TOKEN_1>"},
		{"a sixteen-letter word after sk- takes the words after it", "sk-internationalization-svc", "<TOKEN_1>"},
		// #114's cost. A vendor body's floor is the format's shortest published length and nothing more, so a
		// camelCase identifier at least that long after a vendor prefix is a key: here twenty-four letters
		// after "sk_test_", where '_' would have ended a snake_case name in short pieces.
		{"a long camelCase name after a stripe prefix is a key", "run sk" + "_test_createCustomerWithCardAndRefund ok", "run <TOKEN_1> ok"},
	}
	for _, c := range append(append(append(cases, gluedRunOverMaskingRows...), dottedQuadOverMaskingRows...), slashUserinfoOverMaskingRows...) {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			if got := mustMask(t, m, c.in); got != c.want {
				t.Errorf("accepted over-masking moved:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		})
	}

	// #105's costs, all on the template and only under a configured suffix the pipeline rewrites.
	// The placeholder that stands for the suffix's digits, or for its hex run, stands for ANY run
	// the pipeline rewrites the same way, so a name the raw line does not read as a host on that
	// suffix is one on the template. The raw line is unchanged in every row.
	configured := []struct {
		name, suffix   string
		in, want, tmpl string
	}{
		{"a longer digit run", "acme1", "peer db.acme12 up", "peer db.acme12 up", "peer <HOST_1> up"},
		{"a different digit run", "k8s", "peer db.k88s up", "peer db.k88s up", "peer <HOST_1> up"},
		{"any hex label", "deadbeef", "blob.cafebabe up", "blob.cafebabe up", "<HOST_1> up"},
		{"a word glued to a number", "2corp", "build x12corp up", "build x12corp up", "build <HOST_1> up"},
	}
	for _, c := range configured {
		t.Run("configured "+c.suffix+"/"+c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				if got := mustMask(t, New(c.suffix), p.in); got != p.want {
					t.Errorf("accepted over-masking moved:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
				}
			}
		})
	}
}

// gluedRunOverMaskingRows are #134's accepted over-masking rows. TestAcceptedOverMasking runs them
// with its own; they are at package level so the superset gate (TestGluedRunMasksASupersetOfMain)
// runs their inputs too.
var gluedRunOverMaskingRows = []struct {
	name string
	in   string
	want string
}{
	// #134's costs. A "::"-led address glued on by ':' is masked from the run of colons on, so the
	// colon that glues it goes into the placeholder. netstat writes the unspecified address and a
	// port as ":::22", which is "::" and ":22" -- and reads exactly like ":" and "::22", the shape
	// #134 sent -- so the port goes with it, and ":::22" and the "::" of ":::*" are two values now.
	// ":::*" itself is 7's as before: '*' does not end a value. A ':::' operator between spaces is
	// one placeholder where main left its third colon beside one.
	{"the colon that glues an address goes with it", "peer:::1 down", "peer<IP_1> down"},
	{"a netstat port goes with the unspecified address", "tcp6 0 0 :::22 :::* LISTEN", "tcp6 0 0 <IP_1> <IP_2>:* LISTEN"},
	{"a ':::' operator is one placeholder", "xs ::: ys", "xs <IP_1> ys"},
}

// dottedQuadOverMaskingRows are #49's accepted over-masking rows, at package level for the gate's
// sake as #134's are. After a dotted quad nothing hex can follow, so an unbracketed ":443" before a
// boundary is a port and not a group; it goes into the placeholder anyway, glued or not, because a
// rule bounded on the right by a boundary could not fire on the line otherwise. A token ending in a
// hex character gives it to the address, as on 7, and a prefix longer than an address is one value.
var dottedQuadOverMaskingRows = []struct {
	name string
	in   string
	want string
}{
	{"the port after an unbracketed dotted quad goes with it", "peer ::ffff:192.0.2.1:443 refused", "peer <IP_1> refused"},
	{"the port after a glued dotted quad goes with it", "peer:::ffff:192.0.2.1:443 refused", "peer<IP_1> refused"},
	{"a hex character before a dotted-quad address goes with it", "src::ffff:192.0.2.1 up", "sr<IP_1> up"},
	{"seven groups before a quad are one value", "x 1:2:3:4:5:6:7:192.0.2.1 y", "x <IP_1> y"},
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
	// #46 through the composition. Three marked spans each: the secret that blocks detector 4,
	// the half it can no longer reach, and the host behind it. The blocking secret must still be
	// recognised in its TEMPLATIZED form for the row to reproduce the bug at all — if detector 3b
	// missed the mangled key there would be nothing in detector 4's way and the row would pass for
	// the wrong reason.
	{"url-credentials/premasked-username",
		"dsn=postgres://\x00sk-livekeyabcdefghij0123456789XYZ\x00:\x00hun2ter\x00@\x00db01.corp.internal\x00/prod"},
	{"url-credentials/premasked-password",
		"dsn=postgres://\x00app7user\x00:\x00sk-livekeyabcdefghij0123456789XYZ\x00@\x00db01.corp.internal\x00/prod"},
	{"url-credentials/empty-username",
		"dsn=redis://\x00:s3cr3tpw\x00@\x00cache01.corp.internal\x00:6379/0"},
	// #55 through the composition, and it is red before the fix for a reason worth naming: the
	// templatized line masks to "dsn=postgres://<EMAIL_1>:<NUM>/app", ONE placeholder swallowing
	// the username and the host together, so the derived expectation fails on the mark count
	// rather than on a surviving fragment. A userinfo is a value the pipeline does not collapse
	// — it breaks it with <NUM> and leaves the rest — so this row is required coverage under
	// TestEveryUncollapsedDetectorHasATemplatizedRow, not decoration.
	{"url-credentials/no-password",
		"dsn=postgres://\x00app7user\x00@\x00db01.corp.internal\x00:5432/app"},
	// #58 through the composition. Red before the fix on the SAME accident as the raw path: the
	// templatized line masked to "<TOKEN_1>@<EMAIL_1>/prod", the email detector taking the
	// password's tail and the host as one value. The second row is the 4c cell, where detector 4
	// must decline so the fallback can take "us@er<NUM>" whole.
	{"url-credentials/raw-at-middle-digit",
		"FATAL db postgres://\x00svc7user:p@ss7\x00@\x00db01.corp.internal\x00/prod lost"},
	{"url-credentials/premasked-password/raw-at-username",
		"dsn=postgres://\x00us@er7\x00:\x00sk-livekeyabcdefghij0123456789XYZ\x00@\x00db01.corp.internal\x00/prod"},
	{"email/digit-in-local", "mail to \x00user01@corp.example.com\x00 bounced"},
	{"email/digit-in-domain", "mail to \x00bob@db01.example.com\x00 bounced"},
	{"email/digit-mid-token-both-sides", "mail to \x00us3r@corp7.example.com\x00 bounced"},
	// The local part collapses ENTIRELY — an opaque or hashed address is an ordinary thing for
	// an application log to carry, and the pipeline's HEX masker takes the whole of it. This is
	// the row that decides the email detector's leading \b: with it, '<' is not a word
	// character, the assertion cannot hold at the start of <HEX>, and the WHOLE address
	// including its domain goes out unmatched.
	{"email/local-part-collapsed", "mail to \x00deadbeefcafe1234@corp.example.com\x00 bounced"},
	{"url-host/middle-digit", "call https://\x00api-gw7x.prod.acme.com\x00/v1"},
	// #61 through the composition. Every member carries a middle digit, and every port templatizes
	// to <NUM> exactly as the members' digits do, so 9c has to read the pipeline's handwriting in
	// the host runs and step over it in the ports. One marked span per member since #67.
	{"url-host/list-rest/middle-digit",
		"dsn=mongodb://\x00rs0a.acme.com\x00:27017,\x00rs0b.acme.com\x00:27017,\x00rs0c.acme.com\x00:27017/db?replicaSet=rs0"},
	// With a credential in front and a private suffix on the second member: 9c runs before 9b,
	// so each later member is 9c's regardless of which suffix it carries.
	{"url-host/list-rest/credential-mixed-suffix",
		"FATAL db mongodb://\x00svc7user:hun2ter\x00@\x00db01.acme.com\x00:27017,\x00db02.corp.internal\x00:27017,\x00db03.acme.com\x00:27017/db lost"},
	{"bare-host/middle-digit", "host \x00db01x.corp.internal\x00 failed"},
	{"bare-host/digit-in-middle-label", "host \x00db.eu1a.corp.internal\x00 failed"},
	// #48 through the composition, and this row is green BOTH BEFORE AND AFTER the fix -- which
	// is the point of it. The pipeline collapses a UUID to <UUID>, one of the six UNTAGGED
	// placeholders pipelinePH names, so 9a's tolerant group steps over it and has always masked
	// this host whole. The defect lived only on the RAW path -- Trigger.Raw and every context
	// line -- and this row pins that the reorder does not break the path that was already right.
	{"url-host/uuid-label", "call https://\x00550e8400-e29b-41d4-a716-446655440000.blob.core.windows.net\x00/x"},
	// #66 through the composition: a digit beside a '_' templatizes to <NUM>, so the host run has
	// to step over the pipeline's handwriting AND cross the underscore.
	{"url-host/underscore", "call http://\x00my_host_2\x00:8080/x"},
	{"url-host/list-rest/underscore", "dsn=mongodb://\x00host1\x00,\x00host_2\x00/db"},
	{"bare-host/underscore", "host \x00db_primary01.corp.internal\x00 failed"},
	{"email/underscore-domain", "mail to \x00ops@my_host7.example.com\x00 bounced"},
	// #54 through the composition. The address collapses to the pipeline's <IP>, which the tolerant
	// host runs already stepped over in a URL and on a private suffix -- the third row is green
	// before and after, as #48's was. A bare name on a public suffix was no host rule's business, so
	// the first two sent the labels around <IP> on the Template field too.
	{"bare-host/ipv4-label/public-suffix", "resolve \x0010.0.0.5.nip.io\x00 failed"},
	{"bare-host/ipv4-label/label-2", "peer \x00api.10.0.0.5.nip.io\x00 down"},
	{"url-host/ipv4-label", "call https://\x00api.10.0.0.5.nip.io\x00/x"},
	// #52 through the composition. A digit in the SCHEME templatizes to s<NUM>://, and the scheme
	// anchor -- context, outside every group -- could not step over it, so 9a and 9c went quiet and
	// the host was sent. The host is on a public suffix so 9b cannot rescue it.
	{"url-host/digit-scheme", "get s3://\x00bucket.example.com\x00/key failed"},
	{"url-host/digit-scheme/credential",
		"url=s3://\x00AKIAIOSFODNN7EXAMPLE\x00:\x00secret\x00@\x00bucket.example.com\x00/x"},
	{"url-host/list-rest/digit-scheme", "dsn=s3://\x00h1.acme.com\x00,\x00h2.acme.com\x00/x"},
	// #57 through the composition. The key templatizes to AKIAIOSFODNN<NUM>EXAMPLE, which detector 2
	// reads in its mangled form -- so main tagged it mid-value on this path too. The '_' row is green
	// on the raw line, where no key is seen and 4 masks the userinfo, and was sent on this path only.
	{"url-credentials/glued-key", "url=s3://\x00AKIAIOSFODNN7EXAMPLE.prod\x00@\x00bucket.example.com\x00/x"},
	{"url-credentials/glued-key/underscore", "url=s3://\x00sk-abcdefghij0123456789XYZ_prod\x00@\x00bucket.example.com\x00/x"},
	{"url-host/glued-key", "call https://\x00AKIAIOSFODNN7EXAMPLE.prod.acme.com\x00/x"},
	{"url-host/list-member/glued-key", "dsn=mongodb://\x00h1\x00,\x00AKIAIOSFODNN7EXAMPLE.prod\x00,\x00host3\x00/db"},
	// #108 through the composition. A '_' BEFORE a key defeats the key's \b on the template too, so these
	// three were sent there as well as on the raw line. The '_'-after row is green on main, where the
	// mangled form carries no closing \b, and is the control the raw line now agrees with. Since #51 the
	// "_prod" after an sk- key is the key's, so the sk row's second mark moved past it.
	{"aws-key/glued-underscore-before", "key x_\x00AKIAIOSFODNN7EXAMPLE\x00 used"},
	{"sk-key/glued-underscore-before", "key my_\x00sk-" + "abcdefghij0123456789XYZ_prod\x00 used"},
	{"jwt/glued-underscore-before", "auth token_\x00eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u\x00 rejected"},
	{"aws-key/glued-underscore-after", "key \x00AKIAIOSFODNN7EXAMPLE\x00_prod used"},
	// #110 through the composition. The key templatizes to AKIAIOSFODNN<NUM>EXAMPLE, which 2 reads with no
	// closing \b, so a '_' after it was glue on this path and was sent; a '.' was sent on both.
	{"bearer/glued-key", "hdr Bearer \x00AKIAIOSFODNN7EXAMPLE_prod\x00 expired"},
	{"bearer/glued-jwt", "hdr Bearer \x00eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u.prod\x00 expired"},
	{"home-dir/middle-digit", "loaded from /home/\x00deploy2prod\x00/go/pkg/mod"},
	{"home-dir/middle-digit-users", "path /Users/\x00jane7doe\x00/app.log missing"},
	// #90 through the composition: a closer after the name, which the text template keeps (a quote it folds into
	// <STR>). The second row ends the name on its digit, so the pipeline placeholder is the name's last unit and
	// the closer after it must still stay outside.
	{"home-dir/closing-paren", "(see /home/\x00deploy2prod\x00)"},
	{"home-dir/tolerant-end", "(see /home/\x00maxie7\x00)"},
	// The four widened at 2da31be, now held to completeness rather than to "a token appeared".
	{"jwt", "auth \x00eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u\x00 rejected"},
	{"aws-key", "creds \x00AKIAIOSFODNN7EXAMPLE\x00 denied"},
	{"bearer-jwt", "hdr Bearer \x00eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27u\x00 expired"},
	{"sk-key", "key \x00sk-abcdefghij0123456789XYZ\x00 rejected"},
	// #51 through the composition. Every digit in these keys is <NUM> on the template, which breaks both
	// witnesses' runs, so the placeholder is the evidence, as for the classic key; and a classic key whose
	// tail starts with a digit is sk<NUM>, the "-" taken as NUM's sign.
	{"sk-key/proj", "key \x00" + skProjKey + "\x00 rejected"},
	{"sk-key/svcacct", "key \x00" + skSvcacctKey + "\x00 rejected"},
	{"sk-key/anthropic", "key \x00" + skAntKey + "\x00 rejected"},
	{"sk-key/underscore-inside", "key \x00sk-" + "AbCd3fGh_IjKlMnOpQrStUv\x00 rejected"},
	{"sk-key/digit-first", "key \x00" + skDigitFirstKey + "\x00 rejected"},
	{"sk-key/digit-first/digits-inside", "key \x00" + skDigitsInsideKey + "\x00 rejected"},
	{"sk-key/proj/env-var-end", "OPENAI_API_KEY=\x00" + skProjKey + "\x00"},
	{"sk-key/proj/sentence-dot", "key \x00" + skProjKey + "\x00. Retrying"},
	// The userinfo cell the key's cont is exact for: a lowercase key's mangled run must go on across the '_',
	// or the key stops at it, 4d sees no glue, and the rest of the userinfo is left to the email detector.
	{"url-credentials/glued-key/underscore/lowercase", "url=s3://\x00sk-" + "abcdefghij0123456789xyz_prod\x00@\x00bucket.example.com\x00/x"},
	// #114 through the composition, one row per format family and the rest of each family's prefixes. Every
	// digit is <NUM> on the template, so the placeholder is the evidence; a Slack key's leading '-' and first
	// segment are one <NUM>, NUM's sign, and a Google key's '-' before a digit goes the same way.
	{"vendor-key/stripe", "key \x00" + stripeLiveSecret + "\x00 rejected"},
	{"vendor-key/stripe-restricted", "key \x00" + stripeRestrictedSecret + "\x00 rejected"},
	{"vendor-key/stripe-webhook", "key \x00" + stripeWebhookSecret + "\x00 rejected"},
	{"vendor-key/github", "key \x00" + githubClassicSecret + "\x00 rejected"},
	{"vendor-key/github-server", "key \x00" + githubServerSecret + "\x00 rejected"},
	{"vendor-key/github-pat", "key \x00" + githubPATSecret + "\x00 rejected"},
	{"vendor-key/slack", "key \x00" + slackBotSecret + "\x00 rejected"},
	{"vendor-key/google", "key \x00" + googleAPISecret + "\x00 rejected"},
	{"vendor-key/google-query-value", "https://\x00maps.googleapis.com\x00/maps/api/geocode/json?address=x&key=\x00" + googleAPISecret + "\x00"},
	{"vendor-key/stripe-query-value", "GET https://\x00api.stripe.com\x00/v1/charges?key=\x00" + stripeLiveSecret + "\x00 401"},
	{"vendor-key/github-query-value", "GET https://\x00api.github.com\x00/user?access_token=\x00" + githubClassicSecret + "\x00 401"},
	{"vendor-key/env-var-end", "SLACK_BOT_TOKEN=\x00" + slackBotSecret + "\x00"},
	{"vendor-key/sentence-dot", "key \x00" + googleAPISecret + "\x00. Retrying"},
	// #134: rule 7a reads the pipeline's placeholders after the run, so the digits the template
	// mask turned into <NUM> do not end the value there.
	{"ipv6/glued-colon-leading-elision", "peer\x00:::ffff:0:0\x00 down"},
	// #49: an address ending in a dotted quad. Most forms come back from the template mask as
	// <IP>.<NUM>.<NUM>, with no byte of the value left; these three keep a shape. The compatible form is
	// ::<IP> and the full form with letter groups a:b:c:d:e:f:<IP>, both rule 7b's; the glued form is
	// :::ffff:<IP>, which 7a's tail already read.
	{"ipv6/dotted-quad/compatible", "bind \x00::192.0.2.1\x00 port"},
	{"ipv6/dotted-quad/full-letter-groups", "peer \x00a:b:c:d:e:f:192.0.2.1\x00 down"},
	{"ipv6/dotted-quad/glued-mapped", "peer\x00:::ffff:192.0.2.1\x00 down"},
	// #59: a raw '/' in the password, a digit in each half and in the host. 4e's authority is one
	// placeholder on the template too.
	{"url-credentials/raw-slash/middle-digit", "FATAL db postgres://\x00svc7user:hun2/ter@db01.corp.internal:5432\x00/prod lost"},
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

// TestLogfmtTemplatizedMaskingIsComplete is TestTemplatizedMaskingIsComplete for the logfmt
// branch (#83). A logfmt line's template used to carry its msg as <STR>; it now carries the
// msg's text, and the error's, templatized and re-quoted — text the model is sent in the
// Masked template field. That is #43's exposure again in a new wrapper, so every templatized
// row is embedded in a logrus-shaped line, once as the msg and once as the error, and the
// value must still come back as exactly one placeholder with everything around it untouched.
//
// The raw-line suite needs no logfmt row: the trigger line was sent verbatim before this
// branch and still is. Only the template changed.
//
// The expectation is derived, as in the suite above. The fragment the logfmt branch emits is
// the text path's template re-quoted, so the marked expectation is the row's templatized
// marked text inside the line's own template wrapper. A premise check holds the branch to
// exactly that before anything is masked, and a row needing escapes is refused, since the
// marks could not ride through strconv.Quote.
func TestLogfmtTemplatizedMaskingIsComplete(t *testing.T) {
	const (
		head = `time="2026-07-13T12:00:00.250+02:00" level=error `
		tail = ` module=store namespace=default`
	)
	fields := []struct {
		name            string
		before, tBefore string
	}{
		{"msg", `msg=`, `msg=`},
		{"error", `msg="Failed to get event" error=`, `msg="Failed to get event" error=`},
	}
	// The one context this wrapper adds is a quote directly beside the value, which no
	// templatized row has: each of these puts the secret against the closing or the
	// opening quote.
	//
	// home-dir-last sat skipped here until #90: the username class admitted '"', so the
	// closing quote went into <USER_N>. The two rows after it end the name on a pipeline
	// placeholder, and on a run of closers before the wrapper's own quote.
	quoteEdgeRows := []templatizedRow{
		{"quote-edge/email-last", "login refused for \x00ops7@corp.example.com\x00"},
		{"quote-edge/email-first", "\x00ops7@corp.example.com\x00 refused"},
		{"quote-edge/home-dir-last", "cannot read /home/\x00deploy2prod\x00"},
		{"quote-edge/home-dir-last/tolerant-end", "cannot read /home/\x00maxie7\x00"},
		{"quote-edge/home-dir-last/closer-run", "(see [/home/\x00bob\x00]);."},
		{"quote-edge/url-host-last", "dial https://\x00db01.corp.internal\x00"},
		{"quote-edge/sk-key-last", "rejected key \x00sk-abcdefghij0123456789XYZ\x00"},
		{"quote-edge/sk-proj-last", "rejected key \x00" + skProjKey + "\x00"},
		{"quote-edge/sk-digit-first-first", "\x00" + skDigitFirstKey + "\x00 rejected"},
		{"quote-edge/aws-key-first", "\x00AKIAIOSFODNN7EXAMPLE\x00 denied"},
		{"quote-edge/stripe-key-last", "rejected key \x00" + stripeLiveSecret + "\x00"},
		{"quote-edge/github-pat-last", "rejected key \x00" + githubPATSecret + "\x00"},
		{"quote-edge/slack-key-first", "\x00" + slackBotSecret + "\x00 rejected"},
		{"quote-edge/google-key-first", "\x00" + googleAPISecret + "\x00 rejected"},
		{"quote-edge/github-key-last", "rejected key \x00" + githubClassicSecret + "\x00"},
	}
	rows := append(append([]templatizedRow{}, templatizedRows...), quoteEdgeRows...)
	for _, f := range fields {
		for _, r := range rows {
			t.Run(f.name+"/"+r.name, func(t *testing.T) {
				payload := payloadOf(r.text)
				if strconv.Quote(payload) != `"`+payload+`"` {
					t.Fatalf("premise broken: %q needs escaping inside a quoted value", payload)
				}
				raw := head + f.before + strconv.Quote(payload) + tail
				tPayload, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: raw}))

				tMarked, _ := pipeline.Templatize(r.text)
				prefix := `time=<STR> level=error ` + f.tBefore + `"`
				suffix := `"` + tail
				if want := prefix + payloadOf(tMarked) + suffix; tPayload != want {
					t.Fatalf("premise broken: the logfmt fragment is not the text path's template:\n line: %q\n got:  %q\n want: %q",
						raw, tPayload, want)
				}

				m := New()
				masked := mustMask(t, m, tPayload)

				want := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + expand(tMarked) + regexp.QuoteMeta(suffix) + `$`)
				if !want.MatchString(masked) {
					t.Errorf("templatized logfmt value not masked whole, or surrounding text disturbed:\n raw:  %q\n in:   %q\n got:  %q\n want: %v",
						raw, tPayload, masked, want)
				}
				if got := m.Restore(masked); got != tPayload {
					t.Errorf("round-trip mismatch:\n in:  %q\n out: %q", tPayload, got)
				}
			})
		}
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
// <NUM> before this package ever sees them, but NOT their hex letters, and a hex letter is a digit
// of the address: "peer:2001:db8::1" arrives as "peer:<NUM>:db<NUM>::<NUM>" and "db" is sent. That
// is a leak, not structure, and it is #135.
// The two IPv6 patterns stay two on purpose (#42); ipv6_compare_test.go compares them.
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

// TestEveryDetectorsContextSurvivesTemplating is #52's lesson, mechanized: a detector's tolerance
// has to cover its ANCHORS, not only its group. The test above asks whether each detector's GROUP
// reads a templatized value; nothing asked about the context around it -- 9a's scheme, 3a's
// "bearer", 10's /home/ -- and 9a's scheme was where the pipeline's <NUM> broke it (s3:// ->
// s<NUM>://) with every group check green.
//
// It is a companion rather than an extension of that test because the two rest on different
// exemptions. That one skips a detector whose value the pipeline collapses (IP, UUID); context
// damage does not care whether the value collapses, and this reads every row a detector's group
// lands on, not one canonical row.
//
// For every completeness row whose secret a detector's group lands on exactly, each CONTEXT
// position -- inside the match, outside the group -- is replaced by a digit:
//
//   - if the detector no longer matches with its group on the secret, that position admits no
//     digit and the pipeline cannot damage it there: proven, not skipped;
//   - if it still matches, the row is templatized for real and the detector must still land its
//     group on the templatized secret.
//
// The proof is per position, over the context the rows exercise. Literals inside a GROUP (eyJ,
// AKIA, sk-, 9b's suffixes) are the test above's business.
func TestEveryDetectorsContextSurvivesTemplating(t *testing.T) {
	groupOn := func(d detector, s string, start, end int) []int {
		for _, loc := range d.re.FindAllStringSubmatchIndex(s, -1) {
			if loc[2*d.group] == start && loc[2*d.group+1] == end {
				return loc
			}
		}
		return nil
	}
	for i, d := range baseDetectors {
		rows, digitFree, tolerant := 0, 0, 0
		for _, r := range completenessRows {
			prefix := payloadOf(r.prefix)
			payload := prefix + r.secret + payloadOf(r.suffix)
			start, end := len(prefix), len(prefix)+len(r.secret)
			loc := groupOn(d, payload, start, end)
			if loc == nil {
				continue
			}
			rows++
			for p := loc[0]; p < loc[1]; p++ {
				if p >= start && p < end {
					continue // the group: the test above's question
				}
				mut := payload[:p] + "7" + payload[p+1:]
				if groupOn(d, mut, start, end) == nil {
					digitFree++
					continue
				}
				tMarked, _ := pipeline.Templatize(mut[:start] + phMark + r.secret + phMark + mut[end:])
				tPayload, _ := pipeline.Templatize(mut)
				parts := strings.Split(tMarked, phMark)
				if len(parts) != 3 || payloadOf(tMarked) != tPayload {
					t.Fatalf("premise broken: the marks perturbed templating of %q:\n marked:   %q\n unmarked: %q",
						mut, payloadOf(tMarked), tPayload)
				}
				if groupOn(d, tPayload, len(parts[0]), len(parts[0])+len(parts[1])) == nil {
					t.Errorf("detector %d (%s), row %s: a digit its context admits at %d is lost once templatized:\n raw:  %q\n tmpl: %q\n pattern: %s",
						i, d.tag, r.name, p, mut, tPayload, d.re.String())
					continue
				}
				tolerant++
			}
		}
		t.Logf("detector %2d %-5s rows %2d, context positions: %3d digit-free, %3d tolerant", i, d.tag, rows, digitFree, tolerant)
	}
}

// TestConfiguredSuffixesSurviveTemplating is #105's class, mechanized: a CONFIGURED suffix must
// survive the pipeline as the built-in ones do. Neither test above can see it. The uncollapsed
// test reads each detector's canonical secret from baseDetectors, so it only ever meets the
// built-in suffixes, none of which the pipeline rewrites; the context test asks about CONTEXT,
// and a suffix is inside 9b's group. What both lack is the configuration as an input, and that is
// the dimension this class lives in.
//
// Each shape is one way the pipeline damages a suffix, and each is put in every position a host
// takes on a line. The row is marked, templatized for real, on the text path and inside a logfmt
// msg, and the host must come back as exactly one placeholder with the text around it untouched,
// and round-trip. The expectation is derived, as in TestTemplatizedMaskingIsComplete.
func TestConfiguredSuffixesSurviveTemplating(t *testing.T) {
	shapes := []struct{ name, suffix string }{
		{"digit inside", "k8s"},
		{"digit at end", "acme1"},
		{"hyphen-digit", "eu-1"},
		{"dotted digit", "v1.2"},
		{"all hex", "deadbeef"},
		{"leading digit", "2corp"},
		{"multi-label", "eu1.acme"},
	}
	positions := []struct{ name, text string }{
		{"before a space", "peer \x00db.%s\x00 down"},
		{"end of text", "dial \x00db.%s\x00"},
		{"before a dot", "reach \x00db.%s\x00. retrying"},
		{"before a port", "\x00db.%s\x00:5432 refused"},
		{"digit label", "peer \x00db7.%s\x00 down"},
	}
	const head = `time="2026-07-13T12:00:00Z" level=error msg=`
	for _, sh := range shapes {
		for _, pos := range positions {
			t.Run(sh.name+"/"+pos.name, func(t *testing.T) {
				marked := fmt.Sprintf(pos.text, sh.suffix)
				payload := payloadOf(marked)
				tMarked, _ := pipeline.Templatize(marked)
				tPayload, _ := pipeline.Templatize(payload)
				if payloadOf(tMarked) != tPayload || strings.Count(tMarked, phMark) != 2 {
					t.Fatalf("premise broken: the marks perturbed templating:\n marked:   %q\n unmarked: %q", tMarked, tPayload)
				}
				if tPayload == payload {
					t.Fatalf("premise broken: %q was not rewritten by the pipeline masker", payload)
				}
				logfmt, _ := pipeline.TemplatizeLine(pipeline.Normalize(model.LogLine{Raw: head + strconv.Quote(payload)}))
				prefix := `time=<STR> level=error msg="`
				if logfmt != prefix+tPayload+`"` {
					t.Fatalf("premise broken: the logfmt msg is not the text path's template:\n got:  %q\n want: %q", logfmt, prefix+tPayload+`"`)
				}
				for _, p := range []struct{ path, in, want string }{
					{"raw", payload, expand(marked)},
					{"template", tPayload, expand(tMarked)},
					{"logfmt", logfmt, regexp.QuoteMeta(prefix) + expand(tMarked) + regexp.QuoteMeta(`"`)},
				} {
					m := New(sh.suffix)
					got := mustMask(t, m, p.in)
					if !regexp.MustCompile(`^` + p.want + `$`).MatchString(got) {
						t.Errorf("%s: host on configured suffix %q not masked whole:\n in:   %q\n got:  %q\n want: %v", p.path, sh.suffix, p.in, got, p.want)
					}
					if r := m.Restore(got); r != p.in {
						t.Errorf("%s: round-trip mismatch:\n in:  %q\n out: %q", p.path, p.in, r)
					}
				}
			})
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
		// #51: sk-worker-pool<NUM>f<NUM>c carries a placeholder and no uppercase letter, and sk<NUM> with a
		// short tail is a number beside a word.
		"sk-learn-compatible-estimator",
		"sk-worker-pool-7f9c",
		"task-AbCdEfGhIjKlMnOpQr",
		"Accept-Language: sk-SK,sk;q=0.9",
		"sk-1 failed",
		"region sk-2",
		// #114: on the template a placeholder is the evidence, over a floor in units the short names below
		// stay under, and xoxb<NUM> needs a letter of each case after it.
		"sk" + "_test_create_customer_with_card_1",
		"gh" + "p_v2",
		"github" + "_pat_rotation_policy_v2_enabled",
		"xox" + "b-2024-rollout-plan",
		"set xox" + "b-1 retries",
		"AI" + "za_test_1",
	}
	for _, s := range survive {
		in, _ := pipeline.Templatize(s)
		m := New()
		if got := mustMask(t, m, in); got != in {
			t.Errorf("over-masked templatized prose:\n raw: %q\n in:  %q\n out: %q", s, in, got)
		}
	}
}
