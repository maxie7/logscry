// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/pipeline"
)

// slashUserinfoAWS is an access-key id for the rows below, built in pieces so the source carries no
// whole key.
const slashUserinfoAWS = "AKIA" + "IOSFODNN7" + "EXAMPLE"

// slashUserinfoRows are #59's shapes: a userinfo carrying a raw '/', which ends the authority to
// RFC 3986 and to net/url, so every credential rule stopped there and 9a tagged the USERNAME as the
// host: "postgres://user:p/ss@db" went out as "postgres://<HOST_1>:p/ss@db", the password remainder
// and the real host in the clear, and residue() read it as clean. v0.4.0 onward.
//
// The text cannot say which parse is meant, so no rule chooses one: 4e masks the authority whole,
// from after "://" to where the author's authority ends -- the last '@', then the host and port --
// and that span holds what is sensitive under either parse. want and tmpl are the raw line's and
// the template's output; sent are fragments of the password and the host, none of which may
// survive.
var slashUserinfoRows = []struct {
	name, in, want, tmpl string
	sent                 []string
}{
	{"bare host", "postgres://user:p/ss@db", "postgres://<TOKEN_1>", "postgres://<TOKEN_1>", []string{"p/ss", "db"}},
	// Main sent ":p/" here: the email rule took "ss@db.acme.com" by accident.
	{"dotted host and path", "postgres://user:p/ss@db.acme.com/prod", "postgres://<TOKEN_1>/prod", "postgres://<TOKEN_1>/prod",
		[]string{"p/", "acme"}},
	{"address host, port and path", "postgres://user:p/ss@10.0.0.5:5432/app", "postgres://<TOKEN_1>/app", "postgres://<TOKEN_1>/app",
		[]string{"p/ss", "10.0.0.5", "5432"}},
	// Main sent this one whole: 9a cannot start a host on ':'.
	{"empty username", "redis://:p/ss@redis:6379/0", "redis://<TOKEN_1>/0", "redis://<TOKEN_1>/<NUM>", []string{"p/ss", "@redis"}},
	// A legal URI to net/url (host "us", path "/er:pass@db"), and the password went out with the host.
	{"'/' in the username", "postgres://us/er:pass@db", "postgres://<TOKEN_1>", "postgres://<TOKEN_1>", []string{"pass", "er:", "db"}},
	{"'/' in both halves", "postgres://us/er:p/ss@db", "postgres://<TOKEN_1>", "postgres://<TOKEN_1>", []string{"p/ss", "er:", "db"}},
	// The last '@' delimits, as #58 reads it.
	{"'/' then a raw '@' in the password", "postgres://user:p/s@s@db", "postgres://<TOKEN_1>", "postgres://<TOKEN_1>",
		[]string{"p/s", "s@", "db"}},
	{"host list", "mongodb://user:p/ss@h1.acme.com,host2/db", "mongodb://<TOKEN_1>/db", "mongodb://<TOKEN_1>/db",
		[]string{"p/", "acme", "host2", "host<"}},
	{"bracketed ipv6 host", "postgres://user:p/ss@[2001:db8::1]:5432/app", "postgres://<TOKEN_1>/app", "postgres://<TOKEN_1>/app",
		[]string{"p/ss", "2001", "5432"}},
	// A key that is a whole half is inside the token: 4e runs before the key rules, as 4d does.
	{"aws key as the username", "s3://" + slashUserinfoAWS + ":p/ss@bucket", "s3://<TOKEN_1>", "s<NUM>://<TOKEN_1>",
		[]string{"p/ss", "bucket"}},
	// The closing quote stays outside the token. The pipeline collapses the quoted value to <STR>.
	{"quoted", `dsn="postgres://user:p/ss@db"`, `dsn="postgres://<TOKEN_1>"`, "dsn=<STR>", []string{"p/ss", "db"}},
	// Whitespace bounds the run: the address after it is the email rule's, as on main.
	{"an email after whitespace stays its own", "connect postgres://user:p/ss@db failed; owner bob@corp.com",
		"connect postgres://<TOKEN_1> failed; owner <EMAIL_1>", "connect postgres://<TOKEN_1> failed; owner <EMAIL_1>",
		[]string{"p/ss", "@db", "bob"}},
	{"the query is kept", "postgres://user:p/ss@db?sslmode=disable", "postgres://<TOKEN_1>?sslmode=disable",
		"postgres://<TOKEN_1>?sslmode=disable", []string{"p/ss", "@db"}},
	// '?' is admitted before the '/', as detector 4 admits it in a password.
	{"'?' then '/' in the password", "postgres://user:p?s/s@db", "postgres://<TOKEN_1>", "postgres://<TOKEN_1>",
		[]string{"p?s", "db"}},
	// net/url reads the LAST colon, so it takes ":8080" for a port and parses this; the first colon's
	// right side is not a port, which is what 4e reads.
	{"a colon and digits in the password", "postgres://user:p:8080/ss@db", "postgres://<TOKEN_1>", "postgres://<TOKEN_1>",
		[]string{"p:", "ss@", "db"}},
	{"angle-bracketed", "<postgres://user:p/ss@db>", "<postgres://<TOKEN_1>>", "<postgres://<TOKEN_1>>", []string{"p/ss", "db"}},
	// 3a takes the scheme, which carries no ':' in its alphabet; main sent "://user:p/ss@db" behind it.
	{"behind Bearer", "Bearer postgres://user:p/ss@db", "Bearer <TOKEN_2>://<TOKEN_1>", "Bearer <TOKEN_2>://<TOKEN_1>",
		[]string{"p/ss", "db"}},
}

// TestIssue59SlashInUserinfo is the named regression for #59, with exact outputs for the raw line and
// the template, a round trip, and the absence of every fragment of the password and the host.
func TestIssue59SlashInUserinfo(t *testing.T) {
	for _, c := range slashUserinfoRows {
		t.Run(c.name, func(t *testing.T) {
			tIn, _ := pipeline.Templatize(c.in)
			for _, p := range []struct{ in, want string }{{c.in, c.want}, {tIn, c.tmpl}} {
				m := New()
				got := mustMask(t, m, p.in)
				if got != p.want {
					t.Errorf("a raw '/' in a userinfo not masked whole:\n in:   %q\n got:  %q\n want: %q", p.in, got, p.want)
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

	// 4e is verify-eligible, as every credential rule: a recognised authority that ever survived
	// masking whole would mute the escalation. Main's leftover is a shape the re-scan reads as clean,
	// and it no longer meets it.
	t.Run("verify-eligible", func(t *testing.T) {
		if tag := New().residue("postgres://user:p/ss@db"); tag != tagToken {
			t.Errorf("a raw-'/' userinfo is not verify-eligible: residue = %q, want %q", tag, tagToken)
		}
		if tag := New().residue("postgres://<TOKEN_1>"); tag != "" {
			t.Errorf("residue() fires on 4e's own output: %q", tag)
		}
	})
}

// slashUserinfoSameRows are the shapes nearest 4e that it must not reach, each masked exactly as
// v0.12.25 masks it: a port, a bracketed address, an email in a path, scp and ssh forms, a Go module
// path, a Go stack trace, a Windows drive path, prose with a colon and an '@' after whitespace, and a
// percent-encoded '/', which is no raw '/' and stays detector 4's.
var slashUserinfoSameRows = []struct{ name, in, want string }{
	{"a port before the path", "GET https://host:8080/a@b", "GET https://<HOST_1>:8080/a@b"},
	{"a bracketed address", "http://[::1]/u@x", "http://[<IP_1>]/u@x"},
	{"a bracketed address and a port", "http://[2001:db8::1]:80/p@q", "http://[<IP_1>]:80/p@q"},
	{"an email in a path", "https://h/users/bob@corp.com", "https://<HOST_1>/users/<EMAIL_1>"},
	{"scp form", "git@github.com:org/repo.git", "<EMAIL_1>:org/repo.git"},
	{"ssh with a port", "ssh://git@host:22/x", "ssh://<TOKEN_1>@<HOST_1>:22/x"},
	{"a go module path", "example.com/m/v2@v2.1.0", "example.com/m/v2@v2.1.0"},
	{"a go stack trace", "goroutine 1 [running]:\nmain.main()\n\t/home/bob/go/pkg/mod/example.com/m/v2@v2.1.0/x.go:12 +0x1d",
		"goroutine 1 [running]:\nmain.main()\n\t/home/<USER_1>/go/pkg/mod/example.com/m/v2@v2.1.0/x.go:12 +0x1d"},
	{"a windows drive path", "file:///C:/x@y", "file:///C:/x@y"},
	{"a colon and an '@' after whitespace", "GET https://h/x failed: owner bob@corp.com", "GET https://<HOST_1>/x failed: owner <EMAIL_1>"},
	{"a percent-encoded '/'", "postgres://user:p%2Fss@db", "postgres://<TOKEN_1>@<HOST_1>"},
}

// slashUserinfoResidualRows are #59's shapes the text cannot tell from a legal URI, sent exactly as
// v0.12.25 sends them, by design: a password whose head before the '/' is all digits reads as a port,
// a password-less username with a '/' as an email in a path, an '@' before the '/' as a legal
// userinfo with an '@' in its path, and a password that starts with '/' as an empty port. leak is a
// fragment that is sent; the row goes red if one is ever masked, and README's Known limitations
// names all four.
var slashUserinfoResidualRows = []struct{ name, in, want, leak string }{
	{"a password with a numeric head", "postgres://user:1234/5@db", "postgres://<HOST_1>:1234/5@db", "1234/5@db"},
	{"a password-less username", "postgres://us/er@db", "postgres://<HOST_1>/er@db", "/er@db"},
	{"an '@' before the '/'", "postgres://user:p@s/s@db", "postgres://<TOKEN_1>@<HOST_1>/s@db", "/s@db"},
	{"a password that starts with '/'", "postgres://user:/ss@db", "postgres://<HOST_1>:/ss@db", "/ss@db"},
}

// TestIssue59ShapesNotReached pins both tables above: the rows 4e must not reach, and the ones it
// cannot. The superset gate also holds each to the chain without 4e.
func TestIssue59ShapesNotReached(t *testing.T) {
	for _, c := range slashUserinfoSameRows {
		t.Run(c.name, func(t *testing.T) {
			if got := mustMask(t, New(), c.in); got != c.want {
				t.Errorf("a shape outside #59 moved:\n in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		})
	}
	for _, c := range slashUserinfoResidualRows {
		t.Run("by design/"+c.name, func(t *testing.T) {
			got := mustMask(t, New(), c.in)
			if got != c.want || !strings.Contains(got, c.leak) {
				t.Errorf("a shape sent by design moved -- update this test and README's Known limitations:\n"+
					" in:   %q\n got:  %q\n want: %q", c.in, got, c.want)
			}
		})
	}
}

// slashUserinfoOverMaskingRows are #59's accepted over-masking rows, at package level for the gate's
// sake as #134's and #49's are. TestAcceptedOverMasking runs them.
//
// 4e reads a URI that a '/' before the '@' makes illegal, or a '/' then a ':' then an '@', and some
// legal URIs carry that shape: a container image reference with a tag and a digest, a path with a
// colon in it, a query with a '/' and an '@' after a port, a URL quoted inside another URL's path.
// Each is one token through the host, where main masked the host and sent the rest. A later '@' on
// the line with no whitespace before it extends the token, across JSON fields too, as detector 4
// already does since #58. An unbracketed IPv6 authority -- illegal, the RFC wants brackets -- goes
// into the token rather than under its own tag.
var slashUserinfoOverMaskingRows = []struct {
	name string
	in   string
	want string
}{
	{"an image reference with a tag and a digest", "docker://quay.io/ns/img:1.2@sha256:abc123", "docker://<TOKEN_1>"},
	{"a colon in the path before an '@'", "https://h/a:b@c", "https://<TOKEN_1>"},
	{"a query with a '/' and an '@' after a port", "https://host:8080?next=/a@b", "https://<TOKEN_1>"},
	{"a url inside a url's path", "https://web.archive.org/web/2020/https://u:p@h/x", "https://<TOKEN_1>/x"},
	{"a later '@' across json fields", `{"dsn":"postgres://u:p/ss@db","mail":"bob@corp.com"}`, `{"dsn":"postgres://<TOKEN_1>"}`},
	{"an unbracketed ipv6 authority", "http://2001:db8::1/x@y", "http://<TOKEN_1>"},
}
