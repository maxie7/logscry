// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"errors"
	"os"
	"testing"
)

// fakeTTY returns a real pseudo-terminal: the only honest way to test terminal
// detection is against an actual terminal.
func fakeTTY(t *testing.T) (*os.File, func()) {
	t.Helper()
	_, slave := newPTY(t)
	return slave, func() {}
}

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

// TestIsTerminalRejectsCharDevices is the bug in one line: /dev/null is a character
// device, so the old os.ModeCharDevice check called it a terminal. Anything that
// misidentifies a non-terminal as a terminal ends with Bubble Tea holding an input it
// can never read a key from, which is a TUI that renders and ignores the keyboard.
func TestIsTerminalRejectsCharDevices(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()

	if isTerminal(devNull) {
		t.Error("isTerminal(/dev/null) = true; a character device is not a terminal")
	}

	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pipeR.Close(); _ = pipeW.Close() }()

	if isTerminal(pipeR) {
		t.Error("isTerminal(pipe) = true")
	}
}

// TestResolveTakesTheKeyboardFromDevTTY covers the wiring the mode matrix never did.
// Deciding "TUI" is only half the job; the other half is handing Bubble Tea an input
// it can actually read. In every TUI case with a controlling terminal — logs on stdin,
// from a subprocess, or from Docker — that input is /dev/tty, so there is exactly one
// keyboard path instead of two that can drift apart.
func TestResolveTakesTheKeyboardFromDevTTY(t *testing.T) {
	fake, cleanup := fakeTTY(t)
	defer cleanup()

	// The test binary has no controlling terminal of its own, so stand a real pty in
	// for /dev/tty. What is under test is the choice, not the device.
	ctty, _ := fakeTTY(t)
	restore := openControllingTTY
	openControllingTTY = func() (*os.File, error) { return ctty, nil }
	defer func() { openControllingTTY = restore }()

	// stdin is a free terminal (the Docker / subprocess case: stdin carries no logs).
	term := resolve(false, fake, fake)
	if term.Mode != ModeTUI {
		t.Fatalf("mode = %v, want ModeTUI", term.Mode)
	}
	if term.in == nil {
		t.Fatal("keyboard source is nil: Bubble Tea would fall back to os.Stdin, " +
			"which is the path that was leaving the keyboard dead")
	}
}

// TestResolveFallsBackToStdin: with no controlling terminal to open, a terminal on
// stdin is still a usable keyboard, and nil tells Run to let Bubble Tea read it.
func TestResolveFallsBackToStdin(t *testing.T) {
	fake, cleanup := fakeTTY(t)
	defer cleanup()

	restore := openControllingTTY
	openControllingTTY = func() (*os.File, error) { return nil, errors.New("no controlling terminal") }
	defer func() { openControllingTTY = restore }()

	term := resolve(false, fake, fake)
	if term.Mode != ModeTUI || term.in != nil {
		t.Errorf("resolve = (%v, %v), want (ModeTUI, nil): stdin is the fallback keyboard", term.Mode, term.in)
	}
}

// TestResolvePlainWhenOutputIsPiped: escape codes must never be written into a pipe,
// and any /dev/tty opened along the way must be handed back closed.
func TestResolvePlainWhenOutputIsPiped(t *testing.T) {
	fake, cleanup := fakeTTY(t)
	defer cleanup()

	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pipeR.Close(); _ = pipeW.Close() }()

	if term := resolve(false, fake, pipeW); term.Mode != ModePlain || term.in != nil {
		t.Errorf("resolve with piped stdout = (%v, %v), want (ModePlain, nil)", term.Mode, term.in)
	}
}
