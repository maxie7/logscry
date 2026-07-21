// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"regexp"
	"strings"
	"testing"
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
	placeholders := "<IP_1> <EMAIL_2> <TOKEN_3> <UUID_4> <HOST_5> <USER_6>"
	for _, d := range baseDetectors {
		if loc := d.re.FindStringSubmatchIndex(placeholders); loc != nil && loc[2*d.group] >= 0 {
			t.Errorf("detector %s matched placeholder syntax %q", d.tag, placeholders)
		}
	}
	// Belt and suspenders: the tag pattern the detectors must avoid.
	if regexp.MustCompile(`<[A-Z]+_\d+>`).FindString(placeholders) == "" {
		t.Fatal("test placeholder string is malformed")
	}
}
