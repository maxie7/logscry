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
