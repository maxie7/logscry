// SPDX-License-Identifier: Apache-2.0

package bgquery

import (
	"go/build"
	"os"
	"slices"
	"testing"
)

// TestImportsOnlyOS is the structural guard on the init order this package depends on (see the
// package comment). Any import besides "os" can be initialized after termenv, and this package
// would then wait for it: bubbletea's init would ask the terminal its background first, and every
// start would again wait 5 s on a terminal that never answers (#155). It reads the imports for
// each platform logscry ships, so a build-tagged file cannot slip one in.
func TestImportsOnlyOS(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		ctxt := build.Default
		ctxt.GOOS = goos
		pkg, err := ctxt.ImportDir(".", 0)
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if !slices.Equal(pkg.Imports, []string{"os"}) {
			t.Errorf("%s: bgquery imports %q, want exactly [\"os\"]: its init must run before termenv's, "+
				"which the Go spec guarantees only while every import of bgquery is one termenv also has. "+
				"Another import can delay it past bubbletea's init, which then waits on the terminal again (#155)",
				goos, pkg.Imports)
		}
	}
}

// TestRestore puts TERM back exactly, set or unset, and only once.
func TestRestore(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		t.Setenv("TERM", "dumb")
		swapped, term, hadTerm = true, "xterm-256color", true
		Restore()
		if got := os.Getenv("TERM"); got != "xterm-256color" {
			t.Errorf("TERM = %q, want xterm-256color", got)
		}
		_ = os.Setenv("TERM", "screen")
		Restore() // nothing to undo any more
		if got := os.Getenv("TERM"); got != "screen" {
			t.Errorf("a second Restore changed TERM to %q", got)
		}
	})
	t.Run("unset", func(t *testing.T) {
		t.Setenv("TERM", "dumb")
		swapped, term, hadTerm = true, "", false
		Restore()
		if _, ok := os.LookupEnv("TERM"); ok {
			t.Error("TERM is set; it was unset before init, so Restore must unset it")
		}
	})
	t.Run("untouched", func(t *testing.T) {
		t.Setenv("TERM", "xterm")
		swapped = false
		Restore()
		if got := os.Getenv("TERM"); got != "xterm" {
			t.Errorf("Restore changed TERM to %q though init changed nothing", got)
		}
	})
}
