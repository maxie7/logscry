// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/maxie7/logscry/internal/pipeline"
)

// maskAtV01226 is Mask as v0.12.26 shipped it: the chain, 9c to its fixpoint, and the re-scan, on the
// line as written. GHSA-p47p-xrhj-3x7p's fix edited no detector, so v0.12.26 is this body over the same
// chain. It is the reference the gate below measures against; do not update it.
func maskAtV01226(m *Mapper, s string) (string, error) {
	out := s
	for _, d := range m.dets {
		if !d.fixpoint {
			out = m.apply(out, d)
			continue
		}
		var err error
		if out, err = m.applyToFixpoint(out, d); err != nil {
			return "", err
		}
	}
	if tag := m.residue(out); tag != "" {
		return "", fmt.Errorf("anonymize: a %s value survived masking", tag)
	}
	return out, nil
}

// maskedBytesWith is maskedBytes with the masking body chosen: it runs mask with a fresh Mapper over in,
// returns which of in's bytes went into a placeholder, and fails if the output and the mapping do not
// rebuild the input.
func maskedBytesWith(t *testing.T, mask func(*Mapper, string) (string, error), dets []detector, in string) (string, []bool, error) {
	t.Helper()
	m := mapperOn(dets)
	out, err := mask(m, in)
	if err != nil {
		return "", nil, err
	}
	masked := make([]bool, len(in))
	var rebuilt strings.Builder
	prev := 0
	for _, loc := range ourPHRe.FindAllStringIndex(out, -1) {
		rebuilt.WriteString(out[prev:loc[0]])
		orig, ok := m.byToken[out[loc[0]:loc[1]]]
		if !ok {
			t.Fatalf("output %q carries %q, which this Mapper never issued", out, out[loc[0]:loc[1]])
		}
		for i := rebuilt.Len(); i < min(rebuilt.Len()+len(orig), len(in)); i++ {
			masked[i] = true
		}
		rebuilt.WriteString(orig)
		prev = loc[1]
	}
	rebuilt.WriteString(out[prev:])
	if rebuilt.String() != in {
		t.Fatalf("output and mapping do not rebuild the input:\n in:      %q\n out:     %q\n rebuilt: %q", in, out, rebuilt.String())
	}
	return out, masked, nil
}

// phpJSON is a line as PHP's json_encode writes it into a message field with its defaults: '/' as "\/",
// every character past ASCII as "\uXXXX", '"' and '\' escaped.
func phpJSON(s string) string {
	var b strings.Builder
	b.WriteString(`{"msg":"`)
	for _, r := range s {
		switch {
		case r == '"' || r == '\\' || r == '/':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == utf8.RuneError:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r < 0x80:
			b.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	b.WriteString(`"}`)
	return b.String()
}

// jsonEscapeGenerated is the seeded half of the gate's corpus: a scheme, a userinfo whose halves may carry
// ':', '/', '@', "%2F" or a key, or none, a host (bare, dotted, private, an address, bracketed, a list), a
// port and a path, with each '/', ':' and '@' written as itself or as an encoder escapes it, one level or
// two, in a JSON message, another JSON field, a JSON string inside a JSON string, a logfmt message, a bare
// value and prose; and escaped emails and home paths.
func jsonEscapeGenerated(n int) []string {
	r := rand.New(rand.NewPCG(12, 27))
	pick := func(xs []string) string { return xs[r.IntN(len(xs))] }
	schemes := []string{"postgres", "redis", "mongodb", "https", "http", "s3", "ssh", "x_https"}
	users := []string{"", "user", "us/er", "us@er", "u%2Fs", "1234", "svc_user", slashUserinfoAWS, "a.b"}
	passes := []string{"", "p", "p/ss", "p@ss", "p%2Fss", "1234", "12/34", "p:x", "p/"}
	hosts := []string{"db", "db.acme.com", "10.0.0.5", "[2001:db8::1]", "h1,h2", "db.corp.internal", "lab-1.lab", ""}
	ports := []string{"", ":5432", ":x"}
	paths := []string{"", "/app", "/a@b", "/users/bob@corp.com", "?sslmode=x", "/a/b"}
	slash := []string{"/", `\/`, `\u002f`, `\u002F`, `\\/`, `\\\/`}
	colon := []string{":", ":", `\u003a`, `\u003A`}
	at := []string{"@", "@", `\u0040`}
	esc := func(s string) string {
		var b strings.Builder
		for _, c := range s {
			switch c {
			case '/':
				b.WriteString(pick(slash))
			case ':':
				b.WriteString(pick(colon))
			case '@':
				b.WriteString(pick(at))
			default:
				b.WriteRune(c)
			}
		}
		return b.String()
	}
	// level2 escapes an already escaped value once more, as a JSON string inside a JSON string does.
	level2 := strings.NewReplacer(`\`, `\\`, `/`, `\/`, `"`, `\"`).Replace
	frames := []string{
		`{"level":"error","msg":"connect %s failed"}`, `{"level":"error","dsn":"%s","msg":"retry"}`,
		`{"inner":"{\"dsn\":\"%s\"}"}`, `level=error msg="connect %s failed"`, `level=error dsn=%s`,
		`connect %s failed`, `%s`,
	}
	out := make([]string, 0, n)
	for range n {
		var v string
		switch r.IntN(8) {
		case 0:
			v = esc(pick([]string{"bob@corp.com", "/home/bob/x", "/Users/alice/y", "ops@db.corp.internal"}))
		default:
			uri := pick(schemes) + "://"
			switch r.IntN(4) {
			case 0:
			case 1:
				uri += pick(users) + "@"
			default:
				uri += pick(users) + ":" + pick(passes) + "@"
			}
			v = esc(uri + pick(hosts) + pick(ports) + pick(paths))
		}
		if r.IntN(4) == 0 {
			v = level2(v)
		}
		out = append(out, fmt.Sprintf(pick(frames), v))
	}
	return out
}

// jsonEscapeCorpus is every input the gate runs: #59's corpus (#134's, #49's, the completeness rows,
// templatized, the IPv6 tables, #59's rows and generator) as written, in each of jsonEscapeForms and as
// PHP writes it, this fix's rows, and the generator, the last two raw, through Templatize and through
// TemplatizeLine, deduplicated.
func jsonEscapeCorpus() []string {
	var out []string
	seen := make(map[string]bool)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range slashUserinfoCorpus() {
		add(s)
		for _, f := range jsonEscapeForms {
			add(f.esc(s))
		}
		add(phpJSON(s))
	}
	var rows []string
	for _, c := range jsonEscapeRows {
		rows = append(rows, c.in)
	}
	for _, c := range jsonEscapeWitnessRows {
		rows = append(rows, c.in)
	}
	for _, c := range jsonEscapeSameRows {
		rows = append(rows, c.in)
	}
	for _, c := range jsonEscapeResidualRows {
		rows = append(rows, c.in)
	}
	for _, c := range jsonEscapeOverMaskingRows {
		rows = append(rows, c.in)
	}
	rows = append(rows, jsonEscapeGenerated(1000)...)
	for _, s := range rows {
		tpl, _ := pipeline.Templatize(s)
		add(s)
		add(tpl)
		add(templateOf(s))
	}
	return out
}

// jsonEscapePlaceholderTextRows carry one of our placeholders as text and no escape, beside a value the
// chain mints that same placeholder for. A line with no escape is masked on v0.12.26's path, so each comes
// back exactly as v0.12.26 returns it; on the two-pass path its spans could not be read back, and it would
// be refused. Their bytes cannot be read back from the output, so the gate compares them whole.
var jsonEscapePlaceholderTextRows = []string{
	"login as <HOST_1> from db.corp.internal",
	"replay of postgres://svcuser:pwsecret@pghost/db: <TOKEN_1> rejected",
	"mail <EMAIL_1> to bob@corp.com",
}

// anyEscape is an over-reach of every form the view reads: a '\' before '/' or 'u'. It is written
// independently of the view's pattern, so an input it does not find is one the view cannot touch.
var anyEscape = regexp.MustCompile(`\\[/u]`)

// TestJSONEscapeViewMasksASupersetOfV01226 is the gate GHSA-p47p-xrhj-3x7p's fix stands on. For every
// input, through New() and a Mapper with an extra suffix: the bytes masked now include every byte
// v0.12.26 masked; an input v0.12.26 masked is not refused now; an input with no '\' before '/' or 'u'
// comes back byte for byte; and the witness and same rows, and a line with no escape that carries one of
// our placeholders as text, come back exactly as v0.12.26 returns them.
func TestJSONEscapeViewMasksASupersetOfV01226(t *testing.T) {
	corpus := jsonEscapeCorpus()
	live := func(m *Mapper, s string) (string, error) { return m.Mask(s) }
	for _, c := range []struct {
		name string
		dets []detector
	}{
		{"default", baseDetectors},
		{"extra suffix", buildDetectors([]string{"lab"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			more, escaped := 0, 0
			for _, in := range corpus {
				refOut, refMasked, refErr := maskedBytesWith(t, maskAtV01226, c.dets, in)
				out, masked, err := maskedBytesWith(t, live, c.dets, in)
				if anyEscape.MatchString(in) {
					escaped++
				}
				if refErr != nil {
					continue // v0.12.26 sent nothing
				}
				if err != nil {
					t.Errorf("refused now, masked on v0.12.26:\n in:  %q\n ref: %q\n err: %v", in, refOut, err)
					continue
				}
				if !anyEscape.MatchString(in) && out != refOut {
					t.Errorf("an input the view cannot touch, yet the output moved:\n in:  %q\n ref: %q\n got: %q", in, refOut, out)
				}
				strictly := false
				for i := range in {
					if refMasked[i] && !masked[i] {
						t.Errorf("byte %d (%q) was masked on v0.12.26 and is sent now:\n in:  %q\n ref: %q\n got: %q",
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
			for _, r := range jsonEscapeWitnessRows {
				same = append(same, r.in)
			}
			for _, r := range jsonEscapeSameRows {
				same = append(same, r.in, templateOf(r.in))
			}
			for _, in := range same {
				refOut, _, _ := maskedBytesWith(t, maskAtV01226, c.dets, in)
				if out, _, _ := maskedBytesWith(t, live, c.dets, in); out != refOut {
					t.Errorf("a line the view must leave as v0.12.26 masks it moved:\n in:  %q\n ref: %q\n got: %q", in, refOut, out)
				}
			}
			for _, in := range jsonEscapePlaceholderTextRows {
				refOut, refErr := maskAtV01226(mapperOn(c.dets), in)
				out, err := mapperOn(c.dets).Mask(in)
				if out != refOut || (err == nil) != (refErr == nil) {
					t.Errorf("a line with no escape left v0.12.26's path:\n in:  %q\n ref: %q (%v)\n got: %q (%v)", in, refOut, refErr, out, err)
				}
			}
			t.Logf("%d inputs, %d with an escape, %d mask strictly more than v0.12.26", len(corpus), escaped, more)
		})
	}
}
