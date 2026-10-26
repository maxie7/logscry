// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"testing"

	"github.com/maxie7/logscry/internal/pipeline"
)

// withoutSlashUserinfo is dets as v0.12.25 built them: #59 added rule 4e and edited no other rule, so
// the chain without that one entry is v0.12.25's. It drops exactly the entry whose pattern is
// slashUserinfo's and fails unless every other entry is still there, in the same order.
func withoutSlashUserinfo(t *testing.T, dets []detector) []detector {
	t.Helper()
	pat := mustLongest(slashUserinfo).String()
	var out []detector
	for _, d := range dets {
		if d.re.String() != pat {
			out = append(out, d)
		}
	}
	if len(out) != len(dets)-1 {
		t.Fatalf("the reference dropped %d entries, want exactly one (4e)", len(dets)-len(out))
	}
	for i, j := 0, 0; i < len(dets); i++ {
		if dets[i].re.String() == pat {
			continue
		}
		if out[j] != dets[i] {
			t.Fatalf("reference entry %d is not chain entry %d", j, i)
		}
		j++
	}
	return out
}

// slashUserinfoGenerated is the seeded half of #59's corpus: a scheme, a userinfo whose halves may
// carry ':', '/', '@', "%2F", '?', '#', digits or a key, or no userinfo at all, a host (bare, dotted,
// private, an address, bracketed, a list, empty), a port, a path or query, and a tail, in framings a
// log writes a URI in: alone, after '=', quoted, angle-bracketed, inside JSON with an address after
// it, in prose, and behind "Bearer".
func slashUserinfoGenerated(n int) []string {
	r := rand.New(rand.NewPCG(59, 0))
	pick := func(xs []string) string { return xs[r.IntN(len(xs))] }
	schemes := []string{"postgres", "redis", "mongodb", "https", "http", "s3", "file", "ssh", "docker", "x_https", "h2"}
	users := []string{"", "user", "us/er", "us@er", "u%2Fs", "1234", "svc_user", slashUserinfoAWS, "a.b", "u?x"}
	passes := []string{"", "p", "p/ss", "p@ss", "p%2Fss", "1234", "12/34", "p?ss", "p#ss", "p/s@s", ":x", "p/", "/ss", "p:8/x"}
	hosts := []string{"db", "db.acme.com", "10.0.0.5", "[2001:db8::1]", "h1,h2", "db.corp.internal", "lab-1.lab", "", "[::1]"}
	ports := []string{"", ":5432", ":x", ":"}
	paths := []string{"", "/app", "/a@b", "/users/bob@corp.com", "?sslmode=x", "/x:y@z", "#f", "/a/b"}
	tails := []string{"", " next", `"`, ">", ",", "'", " bob@corp.com", ";"}
	frames := []string{"%s", "dsn=%s", `"%s"`, "<%s>", `{"dsn":"%s","m":"bob@corp.com"}`, "err: %s failed", "Bearer %s"}
	out := make([]string, 0, n)
	for range n {
		uri := pick(schemes) + "://"
		switch r.IntN(5) {
		case 0: // no userinfo
		case 1: // a password-less userinfo
			uri += pick(users) + "@"
		default:
			uri += pick(users) + ":" + pick(passes) + "@"
		}
		uri += pick(hosts) + pick(ports) + pick(paths) + pick(tails)
		out = append(out, fmt.Sprintf(pick(frames), uri))
	}
	return out
}

// slashUserinfoCorpus is every input the gate runs: #134's and #49's corpus (completeness, templatized,
// the IPv6 comparison tables, their own rows, probes and generators), #59's rows and the generator.
func slashUserinfoCorpus() []string {
	var raw []string
	for _, r := range slashUserinfoRows {
		raw = append(raw, r.in)
	}
	for _, r := range slashUserinfoSameRows {
		raw = append(raw, r.in)
	}
	for _, r := range slashUserinfoResidualRows {
		raw = append(raw, r.in)
	}
	for _, r := range slashUserinfoOverMaskingRows {
		raw = append(raw, r.in)
	}
	raw = append(raw, slashUserinfoGenerated(500)...)
	out := supersetCorpus()
	for _, s := range raw {
		tpl, _ := pipeline.Templatize(s)
		out = append(out, s, tpl)
	}
	return out
}

// noSlashBeforeAt finds the one thing 4e needs: a "://" whose run carries a '/' and then an '@'. An
// input without it is one 4e cannot touch.
var noSlashBeforeAt = regexp.MustCompile(`://\S*/\S*@`)

// TestSlashUserinfoMasksASupersetOfV01225 is the gate #59's rule stands on. For every input, through
// New() and a Mapper with an extra suffix, the bytes masked now include every byte v0.12.25 masked; an
// input v0.12.25 masked is not refused now; an input with no '/' and then '@' after a "://" comes back
// byte for byte; and the shapes 4e must not reach, and the ones it cannot, come back exactly as
// v0.12.25 returns them, raw and templatized.
// Listed in ci.yml's SUPERSET_GATES; a new superset gate must follow this name pattern and be added there.
func TestSlashUserinfoMasksASupersetOfV01225(t *testing.T) {
	corpus := slashUserinfoCorpus()
	for _, c := range []struct {
		name string
		dets []detector
	}{
		{"default", baseDetectors},
		{"extra suffix", buildDetectors([]string{"lab"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			ref := withoutSlashUserinfo(t, c.dets)
			more := 0
			for _, in := range corpus {
				refOut, refMasked, refErr := maskedBytes(t, ref, in)
				out, masked, err := maskedBytes(t, c.dets, in)
				if refErr != nil {
					continue // v0.12.25 sent nothing
				}
				if err != nil {
					t.Errorf("refused now, masked on v0.12.25:\n in:  %q\n ref: %q\n err: %v", in, refOut, err)
					continue
				}
				if !noSlashBeforeAt.MatchString(in) && out != refOut {
					t.Errorf("an input 4e cannot touch, yet the output moved:\n in:  %q\n ref: %q\n got: %q", in, refOut, out)
				}
				strictly := false
				for i := range in {
					if refMasked[i] && !masked[i] {
						t.Errorf("byte %d (%q) was masked on v0.12.25 and is sent now:\n in:  %q\n ref: %q\n got: %q",
							i, in[i], in, refOut, out)
						break
					}
					if masked[i] && !refMasked[i] {
						strictly = true
					}
				}
				if strictly {
					more++
				}
			}
			var same []string
			for _, r := range slashUserinfoSameRows {
				same = append(same, r.in)
			}
			for _, r := range slashUserinfoResidualRows {
				same = append(same, r.in)
			}
			for _, in := range same {
				tpl, _ := pipeline.Templatize(in)
				for _, s := range []string{in, tpl} {
					refOut, _, _ := maskedBytes(t, ref, s)
					if out, _, _ := maskedBytes(t, c.dets, s); out != refOut {
						t.Errorf("a shape 4e must not reach moved from v0.12.25:\n in:  %q\n ref: %q\n got: %q", s, refOut, out)
					}
				}
			}
			t.Logf("%d inputs, %d mask strictly more than v0.12.25", len(corpus), more)
		})
	}
}
