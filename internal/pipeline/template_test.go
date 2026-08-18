// SPDX-License-Identifier: Apache-2.0

package pipeline

import "testing"

func TestTemplatizeMaskers(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"num int", "user 4821 failed", "user <NUM> failed"},
		{"num float", "took 12.5 seconds", "took <NUM> seconds"},
		{"num signed", "delta -3 offset", "delta <NUM> offset"},
		{"hex 0x", "addr 0x1f3a mapped", "addr <HEX> mapped"},
		{"hex run with letter", "sha deadbeefcafe done", "sha <HEX> done"},
		{"uuid", "id 550e8400-e29b-41d4-a716-446655440000 ok", "id <UUID> ok"},
		{"ipv4", "from 192.168.1.1 accepted", "from <IP> accepted"},
		{"ipv6 full", "from 2001:0db8:0000:0000:0000:ff00:0042:8329 x", "from <IP> x"},
		{"ipv6 compressed", "from 2001:db8::1 x", "from <IP> x"},
		{"double quoted str", `path "/var/log/app.log" opened`, "path <STR> opened"},
		{"single quoted str", "path '/tmp/x' opened", "path <STR> opened"},
		{"iso timestamp", "at 2026-07-10T14:51:00Z boot", "at <TS> boot"},
		{"whitespace collapsed", "a    b\t c", "a b c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := Templatize(tt.in)
			if got != tt.want {
				t.Errorf("Templatize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestTemplatizeIPBoundaries is the table #33 was fixed against. The IP masker used to
// read a C++ scope resolution operator as an IPv6 address — "::" alone and "::" with a
// single adjacent hex group are both valid IPv6 by the letter of the grammar — and the
// match ate the hex characters next to it, so a card named a function that does not exist.
//
// Every row is a SHAPE observed in a real journald capture, rebuilt with neutral values:
// invented class names, documentation-range addresses (RFC 3849 / RFC 5737). The row names
// say which real system each shape came from, so the table records what the pattern was set
// against rather than a list of strings someone once liked.
//
// Assertions are end-to-end (whole Templatize pipeline, not the IP masker alone), because
// what the IP masker declines does not stay literal: it falls through to HEX and NUM.
func TestTemplatizeIPBoundaries(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// --- C++ scope resolution operators: MUST NOT match (#33) -----------------
		// Ten of fifteen escalated patterns in a one-hour capture were these, and none
		// was an address. Each row reproduces a distinct way the old pattern chewed the
		// identifier up; the "was" comment is what it produced before the fix.
		{"cpp_scope_plain", "CWidgetLoader::instantiate failed",
			"CWidgetLoader::instantiate failed"}, // was: CWidgetLoader<IP>instantiate
		{"cpp_scope_ctor_eats_left", "CNetHelperBase::CNetHelperBase entered",
			"CNetHelperBase::CNetHelperBase entered"}, // was: CNetHelperBas<IP>NetHelperBase
		{"cpp_scope_eats_right_upper", "CFacadeTlv::Assign failed",
			"CFacadeTlv::Assign failed"}, // was: CFacadeTlv<IP>ssign
		{"cpp_scope_eats_right_caps", "CWidgetLoader::CreateAll failed",
			"CWidgetLoader::CreateAll failed"}, // was: CWidgetLoader<IP>reateAll
		{"cpp_scope_eats_left_only", "CScopeBase::getHelper failed",
			"CScopeBase::getHelper failed"}, // was: CScopeBas<IP>getHelper
		// Two more from the same capture, and they eat MORE than one character: the
		// hex run after "::" is taken whole, up to four characters, and the "e" before
		// it goes too. A GUI toolkit and a certificate store, so the table is not one
		// subsystem's worth of evidence.
		{"cpp_scope_eats_right_pair", "CDrawTarget::begin failed",
			"CDrawTarget::begin failed"}, // was: CDrawTarget<IP>gin (ate "be")
		{"cpp_scope_eats_both_sides", "CGroupCertStore::addCert failed",
			"CGroupCertStore::addCert failed"}, // was: CGroupCertStor<IP>ert (ate "e" and "addC")
		{"cpp_scope_three_level", "Log::Dec::init failed",
			"Log::Dec::init failed"}, // was: Log<IP><IP>init
		{"cpp_global_scope_after_space", "call ::AddRef now",
			"call ::AddRef now"}, // was: call <IP>Ref now
		{"cpp_operator_bare", "operator :: used",
			"operator :: used"}, // was: operator <IP> used

		// --- real addresses: MUST keep matching -----------------------------------
		// The capture's addresses are all fully expanded, and they arrive with CIDR
		// suffixes; the compressed forms below are absent from it and are here because
		// the rule has to hold off the corpus, not just on it.
		{"ipv6_expanded_eight_groups", "addr FE80:0:0:0:B071:F3FF:FE0C:22AD/126 up",
			"addr <IP>/<NUM> up"},
		{"ipv6_netmask_all_f", "mask FFFF:FFFF:FFFF:FFFF:FFFF:FFFF:FFFF:FFFC",
			"mask <IP>"},
		{"ipv6_doc_full", "from 2001:0db8:0000:0000:0000:ff00:0042:8329 x",
			"from <IP> x"},
		{"ipv6_compressed", "from 2001:db8::1 x", "from <IP> x"},
		{"ipv6_link_local_compressed", "via fe80::1 dev eth0", "via <IP> dev eth<NUM>"},
		{"ipv6_zone_id", "via fe80::1%eth0 dev", "via <IP>%eth<NUM> dev"},
		{"ipv6_bracketed_port", "peer [2001:db8::1]:8080 closed", "peer [<IP>]:<NUM> closed"},
		{"ipv6_prefix_two_groups", "route 2001:db8::/32 dev", "route <IP>/<NUM> dev"},
		{"ipv6_mapped_hex_groups", "from ::ffff:0:0 y", "from <IP> y"},
		{"ipv6_after_equals_sign", "host=2001:db8::1;port=443", "host=<IP>;port=<NUM>"},
		{"ipv6_at_message_start", "2001:db8::1 connected", "<IP> connected"},
		{"ipv6_two_addresses_one_line", "from 2001:db8::1 to 2001:db8::2",
			"from <IP> to <IP>"},
		{"ipv4_plain", "from 192.0.2.1 accepted", "from <IP> accepted"},
		{"ipv4_cidr", "local 192.0.2.251/25 ok", "local <IP>/<NUM> ok"},
		{"ipv4_network_pair", "route 192.0.2.0 via 192.0.2.1", "route <IP> via <IP>"},

		// --- hex-and-colon shapes that are NOT addresses ---------------------------
		// None of these ever matched the IP masker (they carry no "::", which is the
		// only way into the IPv6 branch), so they are a boundary this fix must not
		// cross rather than new scope. Pinned end-to-end so a later widening shows up
		// as a diff here. What NUM does to them is #36's business, not this test's.
		{"pci_device_path", "/devices/pci0000:00/0000:00:14.0/usb3",
			"/devices/pci<NUM>:<NUM>/<NUM>:<NUM>:<NUM>/usb<NUM>"},
		{"usb_vendor_product", "hid-generic 0003:258A:0049.000B input",
			"hid-generic <NUM>:<NUM>A:<NUM>B input"},
		{"clock_time_in_body", "Tue Aug 18 15:02:04 2026 done",
			"Tue Aug <NUM> <NUM>:<NUM>:<NUM> <NUM> done"},
		{"mac_address", "link 00:1b:44:11:3a:b7 up",
			"link <NUM>:<NUM>b:<NUM>:<NUM>:<NUM>a:b<NUM> up"},

		// --- deliberate losses: pinned so widening the mask back is a visible diff ---
		// The fix requires two hex groups and a boundary. Three shapes pay for that.
		// All three fragment rather than corrupt — no identifier is altered — which is
		// the trade #33 exists to make. See "Known limitations" in README.md.

		// One-group compressed prefix. Indistinguishable from a hex-only C++ segment
		// ("Cafe::draw"), and absent from the capture, so no exception was added.
		{"ipv6_prefix_single_group_KNOWN_GAP", "route fe80::/64 dev",
			"route fe<NUM>::/<NUM> dev"},
		// The loopback. A C++ identifier cannot begin with a digit, so "::" plus an
		// all-decimal group would be a sound discriminator — deliberately not taken,
		// because it widens the match for a shape this capture does not measure.
		{"ipv6_loopback_bare_KNOWN_GAP", "bind ::1 port", "bind ::<NUM> port"},
		// An address glued straight onto the preceding token: the boundary class
		// excludes ':' and '.', so "peer:" (and "upstream.") leaves no legal start
		// position. This would be the costliest of the three — a fully formed address
		// stops masking, and the NUM fallback keeps the hex groups, so these lines
		// fragment per-address instead of collapsing. Measured before accepting it: all
		// 38 address-shaped tokens in a 612-record journald capture are preceded by a
		// space, bracket or comma, and not one is glued to a preceding token. So this
		// is a loss no run has yet shown us paying, not one we weighed and swallowed.
		{"ipv6_colon_glued_KNOWN_GAP", "peer:2001:db8::1 closed",
			"peer:<NUM>:db<NUM>::<NUM> closed"},
		// The residual false positive, and it is not fixable by any textual rule:
		// hex-only segments of four characters or fewer on both sides of "::" ARE an
		// address shape. No such name appears in the capture (they are all CamelCase
		// multi-word), which is the corpus being convenient rather than the rule being
		// complete.
		{"cpp_hex_only_segments_KNOWN_RESIDUAL", "dec::add failed", "<IP> failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := Templatize(tt.in)
			if got != tt.want {
				t.Errorf("Templatize(%q) =\n  got  %q\n  want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestTemplatizeOrdering is the load-bearing watch-out: IPs and UUIDs must survive
// intact and NOT be chewed up by the NUM (or HEX) masker.
func TestTemplatizeOrdering(t *testing.T) {
	if got, _ := Templatize("192.168.1.1"); got != "<IP>" {
		t.Errorf("IPv4 fragmented by NUM: got %q, want <IP>", got)
	}
	if got, _ := Templatize("550e8400-e29b-41d4-a716-446655440000"); got != "<UUID>" {
		t.Errorf("UUID chewed up: got %q, want <UUID>", got)
	}
}

// TestTemplatizeHexRequiresLetter pins the confirmed rule: a hex run needs an
// a–f letter to become <HEX>; long pure-decimals stay <NUM>.
func TestTemplatizeHexRequiresLetter(t *testing.T) {
	if got, _ := Templatize("id deadbeef00 x"); got != "id <HEX> x" {
		t.Errorf("lettered hex run: got %q, want id <HEX> x", got)
	}
	if got, _ := Templatize("id 12345678 x"); got != "id <NUM> x" {
		t.Errorf("pure-decimal run should be NUM: got %q, want id <NUM> x", got)
	}
}

func TestTemplatizeHashStability(t *testing.T) {
	// Two lines differing only in variable parts collapse to the same hash.
	_, h1 := Templatize("user 4821 failed")
	_, h2 := Templatize("user 9933 failed")
	if h1 != h2 {
		t.Errorf("variable-only difference split templates: %s vs %s", h1, h2)
	}

	// Genuinely different lines hash differently.
	_, h3 := Templatize("disk full")
	if h1 == h3 {
		t.Errorf("distinct messages collided: both %s", h1)
	}

	// Hash is non-empty and deterministic across calls.
	_, again := Templatize("user 4821 failed")
	if again != h1 {
		t.Errorf("hash not deterministic: %s vs %s", again, h1)
	}
	if h1 == "" {
		t.Error("hash is empty")
	}
}

// TestTemplatizeIdempotent: templating an already-masked pattern yields the same
// pattern and hash (placeholders contain no maskable tokens).
func TestTemplatizeIdempotent(t *testing.T) {
	pattern, hash := Templatize("user 4821 from 10.0.0.1 failed")
	pattern2, hash2 := Templatize(pattern)
	if pattern2 != pattern {
		t.Errorf("not idempotent: %q -> %q", pattern, pattern2)
	}
	if hash2 != hash {
		t.Errorf("hash changed on re-templatize: %s -> %s", hash, hash2)
	}
}
