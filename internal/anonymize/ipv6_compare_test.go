// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/pipeline"
)

// There are two IPv6 patterns and they stay two: ipv6Pattern here, and ipv6Pat in
// internal/pipeline/template.go. Each package fails in the opposite direction. In the pipeline,
// matching too much corrupts a template (#33). Here, matching too little sends part of an
// address (#41). This file is where the two are compared. It runs the REAL pipeline through
// pipeline.Templatize, the same way bareHostPattern reads the pipeline's handwriting (#105), so
// it needs no copy of the other pattern and no export.

// ipv6Row is one line run through both maskers. pipe is pipeline.Templatize's pattern and anon
// is New().Mask's output, both exact. Whether the two AGREE is derived from them, never declared
// (see ipv6Agree).
type ipv6Row struct {
	name string
	in   string
	pipe string
	anon string
}

var ourIPRe = regexp.MustCompile(`<IP_\d+>`)

// ipv6Agree reports whether the two maskers took the same IPv6 span: the anonymizer's output,
// with its <IP_n> read as the pipeline's <IP>, templatizes to the pipeline's own pattern. The
// rest of the line goes through the same real templatizer on both sides, so a port or an
// interface number cannot make two equal spans look different.
func ipv6Agree(r ipv6Row) bool {
	p, _ := pipeline.Templatize(ourIPRe.ReplaceAllString(r.anon, "<IP>"))
	return p == r.pipe
}

// ipv6MustAgree are lines on which both maskers take the same span, or both leave it alone.
var ipv6MustAgree = []ipv6Row{
	{"compressed", "peer 2001:db8::dead:beef down", "peer <IP> down", "peer <IP_1> down"},
	{"eight groups", "peer 2001:0db8:85a3:0000:0000:8a2e:0370:7334 down", "peer <IP> down", "peer <IP_1> down"},
	{"upper case", "peer 2001:DB8::DEAD:BEEF down", "peer <IP> down", "peer <IP_1> down"},
	{"bracketed with a port", "peer [2001:db8::1]:8080 closed", "peer [<IP>]:<NUM> closed", "peer [<IP_1>]:8080 closed"},
	{"two-group prefix", "route 2001:db8::/32 dev", "route <IP>/<NUM> dev", "route <IP_1>/32 dev"},
	{"leading elision, three groups", "from ::ffff:0:0 y", "from <IP> y", "from <IP_1> y"},
	{"after '='", "x=fe80::1 up", "x=<IP> up", "x=<IP_1> up"},
	{"after '_', which is a boundary to the pipeline", "addr_2001:db8::1 up", "addr_<IP> up", "addr_<IP_1> up"},
	{"letter-led last group", "via fe80::a dev", "via <IP> dev", "via <IP_1> dev"},
	{"letter-led last group after '_'", "id_fe80::a up", "id_<IP> up", "id_<IP_1> up"},
	{"zone id", "via fe80::1%eth0 dev", "via <IP>%eth<NUM> dev", "via <IP_1>%eth0 dev"},
	// Both absorb the port: unbracketed, ":443" is a legal last group (TestAcceptedOverMasking).
	{"unbracketed port absorbed by both", "peer 2001:db8::dead:beef:443 refused", "peer <IP> refused", "peer <IP_1> refused"},
	// No "::" and fewer than eight groups: neither IPv6 pattern takes these.
	{"MAC address", "link 00:1b:44:11:3a:b7 up", "link <NUM>:<NUM>b:<NUM>:<NUM>:<NUM>a:b<NUM> up", "link 00:1b:44:11:3a:b7 up"},
	{"clock time", "Tue Aug 18 15:02:04 2026 done", "Tue Aug <NUM> <NUM>:<NUM>:<NUM> <NUM> done", "Tue Aug 18 15:02:04 2026 done"},
	{"PCI path", "/devices/pci0000:00/0000:00:14.0/usb3", "/devices/pci<NUM>:<NUM>/<NUM>:<NUM>:<NUM>/usb<NUM>", "/devices/pci0000:00/0000:00:14.0/usb3"},
	// Hex-only segments of four characters or fewer on both sides ARE an address shape. Both
	// mask it, and no textual rule can tell it apart.
	{"KNOWN_RESIDUAL hex-only qualified name", "dec::add failed", "<IP> failed", "<IP_1> failed"},
	// #49: neither pattern has a v4-mapped alternative. Both stop at the first octet, which is a
	// legal hex group, and this side SENDS the other three. When #49 adds the alternative here,
	// the anonymizer returns <IP_1>, the relation flips, and this row has to move.
	{"KNOWN_GAP v4-mapped stops at the first octet (#49)", "peer ::ffff:192.168.1.1 down",
		"peer <IP>.<NUM>.<NUM> down", "peer <IP_1>.168.1.1 down"},
}

// ipv6MustDiffer are lines on which the two take different spans ON PURPOSE. Each row's name is
// the reason.
var ipv6MustDiffer = []ipv6Row{
	// Leak-direction forms. The pipeline's boundary and two-group rules drop them, which is
	// fragmentation in a template. Here dropping them would be disclosure, so they stay masked.
	{"leak direction: the loopback has one group", "bind ::1 port", "bind ::<NUM> port", "bind <IP_1> port"},
	{"leak direction: a one-group prefix", "route fe80::/64 dev", "route fe<NUM>::/<NUM> dev", "route <IP_1>/64 dev"},
	{"leak direction: an address glued on by ':'", "peer:2001:db8::1 closed", "peer:<NUM>:db<NUM>::<NUM> closed", "peer:<IP_1> closed"},
	{"leak direction: a one-group prefix glued on by '_'", "id_fe80::/64 up", "id_fe<NUM>::/<NUM> up", "id_<IP_1>/64 up"},

	// The accepted prompt cost. A C++ scope operator is redacted as an address here, and the
	// match eats the hex characters beside it. The pipeline refuses all of these (#33), because
	// there it corrupts the card. Here they stay, for one of two reasons, and each row names its
	// own:
	//   - "e::C": in CNSSCertStore::CNSSCertStore, e::C IS a valid address, glued to a word
	//     character on both sides. A rule that refuses it also refuses an address glued the same
	//     way, and that would be disclosure.
	//   - "bare ::": the match is "::" alone, the unspecified address, which carries no bits.
	//     Refusing it was measured (#42) and not taken: on a run of three colons the match then
	//     starts one colon later, and ":::1:2:3:4:5:6:7:8" sends its last group. That
	//     belongs to ISSUE-TBD's fix.
	{"prompt cost: bare ::, see ISSUE-TBD (issue's line)", "CDBusNMHelper::GetDNSConfig failed",
		"CDBusNMHelper::GetDNSConfig failed", "CDBusNMHelper<IP_1>GetDNSConfig failed"},
	{"prompt cost: e::C (constructor)", "CNSSCertStore::CNSSCertStore entered",
		"CNSSCertStore::CNSSCertStore entered", "CNSSCertStor<IP_1>NSSCertStore entered"},
	{"prompt cost: bare ::, see ISSUE-TBD (plain)", "CWidgetLoader::instantiate failed",
		"CWidgetLoader::instantiate failed", "CWidgetLoader<IP_1>instantiate failed"},
	{"prompt cost: e::C (eats left)", "CNetHelperBase::CNetHelperBase entered",
		"CNetHelperBase::CNetHelperBase entered", "CNetHelperBas<IP_1>NetHelperBase entered"},
	{"prompt cost: e::C (eats right)", "CFacadeTlv::Assign failed",
		"CFacadeTlv::Assign failed", "CFacadeTlv<IP_1>ssign failed"},
	{"prompt cost: e::C (eats right, caps)", "CWidgetLoader::CreateAll failed",
		"CWidgetLoader::CreateAll failed", "CWidgetLoader<IP_1>reateAll failed"},
	{"prompt cost: e::C (eats left only)", "CScopeBase::getHelper failed",
		"CScopeBase::getHelper failed", "CScopeBas<IP_1>getHelper failed"},
	{"prompt cost: e::C (eats a pair)", "CDrawTarget::begin failed",
		"CDrawTarget::begin failed", "CDrawTarget<IP_1>gin failed"},
	{"prompt cost: e::C (eats both)", "CGroupCertStore::addCert failed",
		"CGroupCertStore::addCert failed", "CGroupCertStor<IP_1>ert failed"},
	{"prompt cost: e::C, and a bare :: (three levels)", "Log::Dec::init failed",
		"Log::Dec::init failed", "Log<IP_1><IP_2>init failed"},
	{"prompt cost: e::C (global scope)", "call ::AddRef now",
		"call ::AddRef now", "call <IP_1>Ref now"},
	{"prompt cost: e::C (at line start)", "Cafe::draw failed",
		"Cafe::draw failed", "<IP_1>raw failed"},
	{"prompt cost: bare ::, see ISSUE-TBD (bare operator)", "operator :: used",
		"operator :: used", "operator <IP_1> used"},
	{"prompt cost: e::C (hex member)", "call ::Add(x)",
		"call ::Add(x)", "call <IP_1>(x)"},
	{"prompt cost: bare ::, see ISSUE-TBD (unspecified address)", "listen [::]:8080",
		"listen [::]:<NUM>", "listen [<IP_1>]:8080"},

	// ISSUE-TBD: an address that begins with "::" and is glued on by ':' is SENT. A match ending
	// in "::" starts one colon early and takes the first colon of the address's own "::"; the
	// rest no longer matches. These rows pin today's output, leak included, so the fix shows up
	// here as a diff.
	{"KNOWN_GAP ISSUE-TBD: a leading-elision address glued on by ':' is sent", "peer:::ffff:0:0 down",
		"peer:::ffff:<NUM>:<NUM> down", "peer<IP_1>:ffff:0:0 down"},
	{"KNOWN_GAP ISSUE-TBD: the same after a token ending in a hex character", "src:::a:b:c lost",
		"src:::a:b:c lost", "sr<IP_1>:a:b:c lost"},
}

// TestIPv6MasksAgreeOrDifferOnPurpose is the comparison #42 asked for. It asserts each row's
// two outputs exactly, and that the derived relation matches the table the row sits in. An edit
// to either pattern that changes an output, or turns an agreement into a difference, fails here
// by name.
func TestIPv6MasksAgreeOrDifferOnPurpose(t *testing.T) {
	check := func(t *testing.T, r ipv6Row, wantAgree bool) {
		t.Helper()
		if got, _ := pipeline.Templatize(r.in); got != r.pipe {
			t.Errorf("pipeline:\n in:   %q\n got:  %q\n want: %q", r.in, got, r.pipe)
		}
		if got := mustMask(t, New(), r.in); got != r.anon {
			t.Errorf("anonymizer:\n in:   %q\n got:  %q\n want: %q", r.in, got, r.anon)
		}
		if ipv6Agree(r) != wantAgree {
			t.Errorf("the two maskers %s on %q, but the row is in the other table", map[bool]string{true: "agree", false: "differ"}[!wantAgree], r.in)
		}
	}
	for _, r := range ipv6MustAgree {
		t.Run("agree/"+r.name, func(t *testing.T) { check(t, r, true) })
	}
	for _, r := range ipv6MustDiffer {
		t.Run("differ/"+r.name, func(t *testing.T) { check(t, r, false) })
	}
}

// TestIPv6MasksAgreeOnEveryElisionArity guards the grammar that the hand-written rows cannot
// cover one by one. Every way of eliding at least two groups (L groups, "::", R groups, with
// 2 <= L+R <= 7), and the full eight, must be one placeholder on both sides. The rows are
// generated, from hand-chosen groups that alternate letter-led and digit-led. A form one pattern
// drops and the other keeps fails here by its arity. One- and zero-group forms differ on
// purpose and live in ipv6MustDiffer.
func TestIPv6MasksAgreeOnEveryElisionArity(t *testing.T) {
	groups := []string{"db8", "1", "a2", "30", "f", "4c", "b5", "6"}
	join := func(g []string) string { return strings.Join(g, ":") }
	forms := map[string]string{"full": join(groups)}
	for l := 0; l <= 7; l++ {
		for r := 0; l+r <= 7; r++ {
			if l+r < 2 {
				continue
			}
			forms[fmt.Sprintf("L%d R%d", l, r)] = join(groups[:l]) + "::" + join(groups[8-r:])
		}
	}
	for name, addr := range forms {
		t.Run(name, func(t *testing.T) {
			r := ipv6Row{in: "peer " + addr + " down", pipe: "peer <IP> down", anon: "peer <IP_1> down"}
			if got, _ := pipeline.Templatize(r.in); got != r.pipe {
				t.Errorf("pipeline: %q -> %q", r.in, got)
			}
			if got := mustMask(t, New(), r.in); got != r.anon {
				t.Errorf("anonymizer: %q -> %q", r.in, got)
			}
		})
	}
}
