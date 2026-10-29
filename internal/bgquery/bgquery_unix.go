// SPDX-License-Identifier: Apache-2.0

//go:build unix

package bgquery

import "os"

// init sets TERM=dumb when stdout may be a terminal: the only case in which termenv would ask
// it anything. A character device is a superset of a terminal (/dev/null is one too), which is
// fine here, since Restore undoes the change either way; a pipe or a file is left alone, and with
// it every test binary, whose stdout go test captures. Windows is never touched: termenv asks a
// Windows console nothing.
func init() {
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return
	}
	term, hadTerm = os.LookupEnv("TERM")
	swapped = os.Setenv("TERM", "dumb") == nil
}
