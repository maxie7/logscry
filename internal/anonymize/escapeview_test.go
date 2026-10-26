// SPDX-License-Identifier: Apache-2.0

package anonymize

import (
	"slices"
	"testing"
)

// TestEscapeViewOffsets pins the view and its offsets for each form, at the start, in the middle and at
// the end of a line, in runs, and next to text that is no form.
func TestEscapeViewOffsets(t *testing.T) {
	for _, c := range []struct {
		name, in, view string
		offs           []int
	}{
		{"one slash", `a\/b`, "a/b", []int{0, 1, 3, 4}},
		{"at the start", `\/x`, "/x", []int{0, 2, 3}},
		{"at the end", `x\/`, "x/", []int{0, 1, 3}},
		{"u002f", `\u002fx`, "/x", []int{0, 6, 7}},
		{"u002F", `x\u002F`, "x/", []int{0, 1, 7}},
		{"a run of three backslashes", `x\\\/y`, "x/y", []int{0, 1, 5, 6}},
		{"a run before u002f", `\\u002f`, "/", []int{0, 7}},
		{"colon and at", `\u003a\u003A\u0040`, "::@", []int{0, 6, 12, 18}},
		{"the template of u002f", `p\u<NUM>fq`, "p/q", []int{0, 1, 9, 10}},
		{"the template of u002F", `\u<NUM>F`, "/", []int{0, 8}},
		{"two in a row", `\/\/`, "//", []int{0, 2, 4}},
		{"beside a form it does not read", `\u0041\/`, `\u0041/`, []int{0, 1, 2, 3, 4, 5, 6, 8}},
	} {
		t.Run(c.name, func(t *testing.T) {
			view, offs := escapeView(c.in)
			if view != c.view || !slices.Equal(offs, c.offs) {
				t.Errorf("escapeView(%q) = %q, %v; want %q, %v", c.in, view, offs, c.view, c.offs)
			}
		})
	}
	// No form, no view: the line takes the path v0.12.26 took. The template of the "u" escape of ':' or
	// '@' and any other "u" escape are not read.
	for _, in := range []string{"plain", `C:\Users\bob`, `\u0041`, `\u<NUM>a`, `\u<NUM>`, `\u002e`, `\\n`} {
		if view, offs := escapeView(in); view != in || offs != nil {
			t.Errorf("escapeView(%q) = %q, %v; want the line and no offsets", in, view, offs)
		}
	}
}

// TestEscapedResidueReadsTheView pins that the fail-closed re-scan reads the view of an escaped line's
// output, not only the line. An escaped DSN is no shape on the line as written, so a value an escape hides
// would read as clean there; in its view the credential rule sees it.
func TestEscapedResidueReadsTheView(t *testing.T) {
	m := New()
	in := `dsn=postgres:\/\/svcuser:pwsecret@pghost\/db`
	view, _ := escapeView(in)
	if tag := m.residue(in); tag != "" {
		t.Fatalf("the line as written already trips the re-scan (%s); the row no longer shows anything", tag)
	}
	if tag := m.residue(view); tag == "" {
		t.Fatalf("the view of %q does not trip the re-scan", in)
	}

	// A chain that leaves a value behind, the way #46's did: a later host rule's placeholder completes the
	// shape an earlier verify-eligible rule looks for, and only the view of the output shows it.
	host := detector{tag: tagHost, re: mustLongest(`//(\w+):`), group: 1}
	pw := detector{tag: tagToken, re: mustLongest(`//<HOST_\d+>:(\w+)@`), group: 1, verify: true}
	for _, in := range []string{`//db:pwsecret@x`, `\/\/db:pwsecret@x`} {
		if out, err := mapperOn([]detector{pw, host}).Mask(in); err == nil {
			t.Errorf("a value the re-scan sees in the view was sent:\n in:  %q\n out: %q", in, out)
		}
	}
}
