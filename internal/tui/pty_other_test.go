// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package tui

import (
	"os"
	"testing"
)

// newPTY skips off Linux: the pty harness in pty_test.go opens /dev/ptmx with Linux-only
// ioctls. The tests that call it from untagged files still compile everywhere, and say why they
// did not run.
func newPTY(t *testing.T, _ int) (master, slave *os.File) {
	t.Helper()
	t.Skip("no pty harness off Linux: newPTY uses Linux-only ioctls")
	return nil, nil
}
