// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"fmt"
	"strings"
	"testing"
)

// placeholderTextRows carry one of our placeholders as text beside a value the chain masks (#142). Until
// the fix the chain minted that same placeholder for the value, so the model read one value where the
// log had two things, and Restore put the value on the card in the literal's place too:
// "login as <HOST_1> from db.corp.internal" went as "login as <HOST_1> from <HOST_1>" and came back as
// "login as db.corp.internal from db.corp.internal". A placeholder the text carries is now never minted, so
// it reaches the model as the log wrote it and Restore, which keeps a token it never issued, returns it.
//
// One row per tag, with the literal before, after, between and glued to the values; the rest pin how the
// numbering moves: only past a number the text holds, and only within that number's tag.
var placeholderTextRows = []struct{ name, in, want string }{
	{"host, the issue's line", "login as <HOST_1> from db.corp.internal", "login as <HOST_1> from <HOST_2>"},
	{"token, the issue's dsn", "replay of postgres://svcuser:pwsecret@pghost/db: <TOKEN_1> rejected",
		"replay of postgres://<TOKEN_2>@<HOST_1>/db: <TOKEN_1> rejected"},
	{"ip, before", "<IP_1> then 10.0.0.5 refused", "<IP_1> then <IP_2> refused"},
	{"email, after", "mail bob@corp.com as <EMAIL_1>", "mail <EMAIL_2> as <EMAIL_1>"},
	{"uuid, between", "req 550e8400-e29b-41d4-a716-446655440000 <UUID_1> 6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		"req <UUID_2> <UUID_1> <UUID_3>"},
	{"user, glued", "loaded /home/deploy/x for x<USER_1>y", "loaded /home/<USER_2>/x for x<USER_1>y"},
	{"hosts around literals", "<HOST_1> db.corp.internal <HOST_2> cache.corp.internal <HOST_3>",
		"<HOST_1> <HOST_4> <HOST_2> <HOST_5> <HOST_3>"},
	{"a literal read as a credential's context", "postgres://<TOKEN_1>:pwsecret@pghost/db",
		"postgres://<TOKEN_1>:<TOKEN_2>@<HOST_1>/db"},
	{"the same value twice keeps one placeholder", "<IP_1> 10.0.0.5 and 10.0.0.5", "<IP_1> <IP_2> and <IP_2>"},
	{"a gap below the literal is used", "<HOST_2> db.corp.internal", "<HOST_2> <HOST_1>"},
	{"another tag's number is free", "<HOST_1> from 10.0.0.5", "<HOST_1> from <IP_1>"},
	{"text ourPH reads but no tag mints", "<HOST_01> db.corp.internal <FOO_1>", "<HOST_01> <HOST_1> <FOO_1>"},
}

// placeholderTextEscapedLine is the line TestJSONEscapePlaceholderShapedLiteralFailsClosed refused from
// v0.12.27 until the fix: an escaped line whose pass minted, for its credential, the placeholder it
// carries as text, so the pass could not be read back from its output. Nothing is minted that the line
// carries now, and it is sent.
const placeholderTextEscapedLine = `connect postgres:\/\/svcuser:pwsecret@pghost\/db as <TOKEN_1>`

// assertPlaceholderTextKept checks one masked line: no error, the exact output, and Restore in both
// directions -- a token the input carries as text comes back as that text, a minted one as its value.
func assertPlaceholderTextKept(t *testing.T, in, want string) {
	t.Helper()
	m := New()
	out, err := m.Mask(in)
	if err != nil {
		t.Fatalf("refused, want sent:\n in:  %q\n err: %v", in, err)
	}
	if out != want {
		t.Errorf("wrong mask:\n in:   %q\n got:  %q\n want: %q", in, out, want)
	}
	if got := m.Restore(out); got != in {
		t.Errorf("Restore(Mask(in)) is not in:\n in:       %q\n restored: %q", in, got)
	}
	for _, tok := range ourPHRe.FindAllString(want, -1) {
		got := m.Restore("see " + tok)
		switch {
		case strings.Contains(in, tok) && got != "see "+tok:
			t.Errorf("%s is text of the input, but Restore made it %q", tok, got)
		case !strings.Contains(in, tok) && (got == "see "+tok || ourPHRe.MatchString(got)):
			t.Errorf("%s was minted, but Restore left %q", tok, got)
		}
	}
}

func TestPlaceholderTextIsNeverMinted(t *testing.T) {
	for _, r := range placeholderTextRows {
		t.Run(r.name, func(t *testing.T) { assertPlaceholderTextKept(t, r.in, r.want) })
	}
}

// TestEscapedPlaceholderTextIsSent: the same rows in every escaped form the view resolves. From v0.12.27
// such a line was refused whenever a pass minted a placeholder it carries; it is masked as its unescaped
// form is, and sent.
func TestEscapedPlaceholderTextIsSent(t *testing.T) {
	t.Run("the refused line", func(t *testing.T) {
		assertPlaceholderTextKept(t, placeholderTextEscapedLine, `connect postgres:\/\/<TOKEN_2>@<HOST_1>\/db as <TOKEN_1>`)
	})
	for _, r := range placeholderTextRows {
		for _, f := range jsonEscapeForms {
			in := f.esc(r.in)
			if in == r.in {
				continue // no '/', ':' or '@' to escape
			}
			t.Run(r.name+"/"+f.name, func(t *testing.T) { assertPlaceholderTextKept(t, in, f.esc(r.want)) })
		}
	}
}

// TestRestoreKeepsAPlaceholderCarriedAsText is the issue's line read back from the model: a reply that
// echoes the literal gets the literal, and a reply that uses the minted placeholder gets the real value.
func TestRestoreKeepsAPlaceholderCarriedAsText(t *testing.T) {
	m := New()
	if out, err := m.Mask("login as <HOST_1> from db.corp.internal"); err != nil || out != "login as <HOST_1> from <HOST_2>" {
		t.Fatalf("Mask = %q, %v; want %q", out, err, "login as <HOST_1> from <HOST_2>")
	}
	for reply, want := range map[string]string{
		"user <HOST_1> logged in":                   "user <HOST_1> logged in",
		"the login came from <HOST_2>":              "the login came from db.corp.internal",
		"user <HOST_1> came in from <HOST_2>":       "user <HOST_1> came in from db.corp.internal",
		"<HOST_2> rejected <HOST_1>, then <HOST_2>": "db.corp.internal rejected <HOST_1>, then db.corp.internal",
	} {
		if got := m.Restore(reply); got != want {
			t.Errorf("Restore(%q) = %q, want %q", reply, got, want)
		}
	}
}

// placeholderTextTemplateRows are what the Template field makes of a literal placeholder (#142). The text
// masker turns its digits into <NUM> -- numPat has no \b, so "_1" is a number -- and "<HOST_<NUM>>" is no
// placeholder of ours, so nothing can collide through a plain line, a JSON or logfmt message or a value. A
// JSON key is kept as written, so a JSON line with a placeholder for a key carries it to the Template field.
// tmpl pins what the pipeline does; want is the masked Template field.
var placeholderTextTemplateRows = []struct{ name, raw, tmpl, want string }{
	{"plain", "ERROR login as <HOST_1> from db.corp.internal",
		"ERROR login as <HOST_<NUM>> from db.corp.internal", "ERROR login as <HOST_<NUM>> from <HOST_1>"},
	{"json message", `{"level":"error","msg":"login as <HOST_1> from db.corp.internal"}`,
		`{"level":<STR>,"msg":"login as <HOST_<NUM>> from db.corp.internal"}`,
		`{"level":<STR>,"msg":"login as <HOST_<NUM>> from <HOST_1>"}`},
	{"logfmt message", `level=error msg="login as <HOST_1> from db.corp.internal"`,
		`level=error msg="login as <HOST_<NUM>> from db.corp.internal"`,
		`level=error msg="login as <HOST_<NUM>> from <HOST_1>"`},
	{"json key", `{"level":"error","msg":"login from db.corp.internal","<HOST_1>":"x"}`,
		`{"<HOST_1>":<STR>,"level":<STR>,"msg":"login from db.corp.internal"}`,
		`{"<HOST_1>":<STR>,"level":<STR>,"msg":"login from <HOST_2>"}`},
}

func TestTemplateFieldPlaceholderText(t *testing.T) {
	for _, r := range placeholderTextTemplateRows {
		t.Run(r.name, func(t *testing.T) {
			if got := templateOf(r.raw); got != r.tmpl {
				t.Fatalf("the pipeline's template moved:\n raw:  %q\n got:  %q\n want: %q", r.raw, got, r.tmpl)
			}
			assertPlaceholderTextKept(t, r.tmpl, r.want)
		})
	}
}

// TestOurPHFindsEveryMintedToken holds the reservation to the one pattern it reads by. A token the chain can
// mint must be found by ourPHRe wherever it sits in the text, or a literal of it would go unreserved and be
// minted again. Two ourPH matches cannot overlap -- each starts at '<' and holds no other -- so a scan finds
// every occurrence whatever is glued to it; this checks that for every tag in the detector tables, one- to
// three-digit numbers, and every kind of neighbour.
func TestOurPHFindsEveryMintedToken(t *testing.T) {
	sides := []string{"", " ", "x", "X", "9", "_", "<", ">", "<<", ">>", "<X_", "_1>"}
	for _, dets := range [][]detector{baseDetectors, buildDetectors([]string{"example"})} {
		for _, d := range dets {
			m := New()
			var toks []string
			for i := 1; i <= 123; i++ {
				tok := m.placeholder(d.tag, fmt.Sprint("value", i))
				if i == 1 || i == 9 || i == 10 || i == 123 {
					toks = append(toks, tok)
				}
			}
			for _, tok := range toks {
				for _, pre := range sides {
					for _, post := range sides {
						found := false
						for _, got := range ourPHRe.FindAllString(pre+tok+post, -1) {
							found = found || got == tok
						}
						if !found {
							t.Errorf("tag %q: ourPHRe does not find %q in %q", d.tag, tok, pre+tok+post)
						}
					}
				}
			}
		}
	}
}

// TestMaskRefusesATokenItAlreadyMinted is the one collision reservation cannot undo. A Mapper that minted
// <HOST_1> on one line and then meets "<HOST_1>" as text on a later one can no longer give the two distinct
// placeholders, and the one it gave the value is already in what was masked. Rather than send a line Restore
// would read back wrongly, Mask refuses, and so does every later Mask on that Mapper. Explain reserves every
// field before its first Mask (llm.NewAnonymizing), so it never reaches this.
func TestMaskRefusesATokenItAlreadyMinted(t *testing.T) {
	m := New()
	if out, err := m.Mask("connect db.corp.internal refused"); err != nil || out != "connect <HOST_1> refused" {
		t.Fatalf("Mask = %q, %v", out, err)
	}
	out, err := m.Mask("retry as <HOST_1>")
	if err == nil {
		t.Fatalf("a line carrying an already-minted token as text was sent: %q", out)
	}
	if strings.Contains(err.Error(), "retry") || strings.Contains(err.Error(), "corp") {
		t.Errorf("the refusal carries log content: %v", err)
	}
	if out, err := m.Mask("peer 10.0.0.5 down"); err == nil {
		t.Errorf("a later Mask on the same Mapper was sent after the conflict: %q", out)
	}
}

// TestUnreadableEscapedPassFailsClosed keeps maskEscaped's rebuild check honest. Mask reserves every
// placeholder its input carries, and the view resolves only to '/', ':' and '@', none of them in ourPH, so
// no pass can mint a token the line carries and the check cannot fire through Mask. It guards that
// argument: a pass whose output does not rebuild its input would project wrong spans. Called here with
// nothing reserved, it must refuse, and say nothing of the line.
func TestUnreadableEscapedPassFailsClosed(t *testing.T) {
	view, offs := escapeView(placeholderTextEscapedLine)
	if offs == nil {
		t.Fatal("the line has no escape")
	}
	out, err := New().maskEscaped(placeholderTextEscapedLine, view, offs)
	if err == nil {
		t.Fatalf("an unreadable pass was sent: %q", out)
	}
	if strings.Contains(err.Error(), "pwsecret") || strings.Contains(err.Error(), "svcuser") {
		t.Errorf("the refusal carries log content: %v", err)
	}
}

// TestReserveAfterAMintFailsClosed is Reserve's own conflict. Told, after it minted <HOST_1> for a host,
// that the request carries "<HOST_1>" as text, a Mapper can no longer keep the two apart: the host has
// gone out under it. Reserve cannot undo that, so every later Mask refuses, even on a line that carries no
// placeholder at all. Reserved before the first Mask, as Explain does it, the same texts mask apart.
func TestReserveAfterAMintFailsClosed(t *testing.T) {
	late := New()
	if _, err := late.Mask("connect db.corp.internal refused"); err != nil {
		t.Fatalf("Mask refused: %v", err)
	}
	late.Reserve("retry as <HOST_1>")
	if out, err := late.Mask("peer 10.0.0.5 down"); err == nil {
		t.Errorf("Reserve found <HOST_1> already minted, yet the next Mask sent %q", out)
	} else if strings.Contains(err.Error(), "retry") || strings.Contains(err.Error(), "10.0.0.5") {
		t.Errorf("the refusal carries log content: %v", err)
	}

	early := New()
	early.Reserve("connect db.corp.internal refused", "retry as <HOST_1>")
	for _, c := range []struct{ in, want string }{
		{"connect db.corp.internal refused", "connect <HOST_2> refused"},
		{"retry as <HOST_1>", "retry as <HOST_1>"},
	} {
		if out, err := early.Mask(c.in); err != nil || out != c.want {
			t.Errorf("reserved first: Mask(%q) = %q, %v; want %q", c.in, out, err, c.want)
		}
	}
}
