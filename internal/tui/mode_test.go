// SPDX-License-Identifier: Apache-2.0

package tui

import "testing"

// TestDecide walks the whole stdin/stdout/TTY matrix documented on Decide. The
// cases that matter most: never write escape codes into a pipe, and never start
// the TUI when there is no keyboard to read.
func TestDecide(t *testing.T) {
	tests := []struct {
		name                               string
		plain, stdinTTY, stdoutTTY, devTTY bool
		want                               Mode
	}{
		{name: "--plain forces plain even on a full terminal",
			plain: true, stdinTTY: true, stdoutTTY: true, devTTY: true, want: ModePlain},
		{name: "--plain forces plain when piped",
			plain: true, want: ModePlain},

		{name: "stdout piped: plain, or the pipe fills with escape codes",
			stdinTTY: true, stdoutTTY: false, devTTY: true, want: ModePlain},
		{name: "stdout redirected to a file: plain",
			stdinTTY: false, stdoutTTY: false, devTTY: true, want: ModePlain},

		{name: "interactive terminal: TUI reading stdin",
			stdinTTY: true, stdoutTTY: true, want: ModeTUI},

		{name: "logs piped in, terminal out, /dev/tty available: TUI on /dev/tty",
			stdinTTY: false, stdoutTTY: true, devTTY: true, want: ModeTUI},
		{name: "logs piped in, terminal out, no controlling terminal (CI): plain",
			stdinTTY: false, stdoutTTY: true, devTTY: false, want: ModePlain},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.plain, tt.stdinTTY, tt.stdoutTTY, tt.devTTY)
			if got != tt.want {
				t.Errorf("Decide(plain=%v, stdinTTY=%v, stdoutTTY=%v, devTTY=%v) = %v, want %v",
					tt.plain, tt.stdinTTY, tt.stdoutTTY, tt.devTTY, got, tt.want)
			}
		})
	}
}
