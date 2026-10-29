// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// startupChildEnv carries the arguments of the logscry run TestStartupChild makes.
const startupChildEnv = "LOGSCRY_STARTUP_CHILD_ARGS"

// The bounds below are what the startup tests allow logscry, against the 5 s a terminal query
// waits for an answer that never comes (#155). Both are many times what a start takes, so a slow
// runner cannot fail them; neither is anywhere near 5 s, so the wait cannot pass them.
const (
	startupBound = 2 * time.Second         // --version and --plain, which ask the terminal nothing
	tuiBound     = 2500 * time.Millisecond // the TUI's first frame, after at most a 1 s question
	killAfter    = 3 * time.Second         // a child still running then is waiting on the terminal
)

// silentStartup is one logscry process started on a terminal that never answers anything: a
// pty that is the child's controlling terminal and its stdout, with nobody playing terminal at
// the other end. That is `script` with no terminal behind it, where #155 was found.
type silentStartup struct {
	master *os.File
	cmd    *exec.Cmd
	start  time.Time
	exited chan error

	mu   sync.Mutex
	out  strings.Builder
	done chan struct{} // closed once the master has been read to its end
}

// startSilent re-executes this test binary as TestStartupChild, which runs logscry's main with
// args. Package init runs in the child exactly as in the real binary, which is where the query
// was made. stdin is the child's standard input; nil means /dev/null.
func startSilent(t *testing.T, stdin io.Reader, args ...string) *silentStartup {
	t.Helper()
	master, slave := openPTY(t)

	cmd := exec.Command(os.Args[0], "-test.run=^TestStartupChild$", "-test.count=1")
	cmd.Env = append(childEnv(), startupChildEnv+"="+strings.Join(args, " "))
	cmd.Stdin = stdin
	cmd.Stdout = slave
	cmd.Stderr = slave
	// A session of its own, with the pty as its controlling terminal: the child is then in the
	// terminal's foreground, which is the only case in which termenv asks it anything.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 1}

	s := &silentStartup{master: master, cmd: cmd, exited: make(chan error, 1), done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	s.start = time.Now()
	_ = slave.Close() // the child holds it now; the master reads EIO once the child is gone
	go func() { s.exited <- cmd.Wait() }()
	go func() {
		defer close(s.done)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.out.Write(buf[:n])
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-s.exited
		}
	})
	return s
}

// output is everything the child has written to its terminal so far.
func (s *silentStartup) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// wait returns how long the child ran, or false if it was still running at killAfter, in which
// case it is killed.
func (s *silentStartup) wait() (time.Duration, bool) {
	select {
	case <-s.exited:
		elapsed := time.Since(s.start)
		select {
		case <-s.done:
		case <-time.After(time.Second):
		}
		return elapsed, true
	case <-time.After(killAfter - time.Since(s.start)):
		_ = s.cmd.Process.Kill()
		<-s.exited
		return 0, false
	}
}

// childEnv is this process's environment for the child, with TERM a terminal termenv would
// query and without CI: termenv treats any non-empty CI as "not a terminal" and asks nothing,
// so on GitHub's runners the bug would hide and the test would pass on main.
func childEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "CI="), strings.HasPrefix(kv, "TERM="),
			strings.HasPrefix(kv, "COLORFGBG="), strings.HasPrefix(kv, startupChildEnv+"="):
			continue
		}
		env = append(env, kv)
	}
	return append(env, "TERM=xterm-256color")
}

// terminalQueries are what termenv writes to ask a terminal for its background colour: OSC 11,
// then a cursor-position report, which every terminal answers, as a sentinel.
var terminalQueries = []string{"\x1b]11;?", "\x1b[6n"}

// TestStartupAsksTheTerminalNothing is #155's regression test. On a terminal that never answers,
// --version and --plain must neither ask it anything nor wait, and the TUI must be drawn
// without waiting out termenv's 5 s. On the affected releases bubbletea's package init asked,
// for every invocation, before main ran.
func TestStartupAsksTheTerminalNothing(t *testing.T) {
	cases := []struct {
		name  string
		stdin io.Reader
		args  []string
		want  string // in the output, to show the run did what it was asked
	}{
		{"version", nil, []string{"--version"}, "logscry "},
		{"plain", strings.NewReader("ERROR first\nINFO second\nWARN third\n"),
			[]string{"--plain", "--explain-dry-run"}, "first"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := startSilent(t, tc.stdin, tc.args...)
			elapsed, exited := s.wait()
			out := s.output()
			for _, q := range terminalQueries {
				if strings.Contains(out, q) {
					t.Errorf("logscry %s sent the terminal query %q; it must ask a terminal nothing (#155)\noutput: %q",
						strings.Join(tc.args, " "), q, out)
				}
			}
			if !exited {
				t.Fatalf("logscry %s was still running after %v on a terminal that never answers: "+
					"it is waiting for an answer (#155)\noutput: %q", strings.Join(tc.args, " "), killAfter, out)
			}
			if elapsed > startupBound {
				t.Errorf("logscry %s took %v on a terminal that never answers, want under %v (#155)",
					strings.Join(tc.args, " "), elapsed, startupBound)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("logscry %s wrote no %q, so the run proves nothing\noutput: %q",
					strings.Join(tc.args, " "), tc.want, out)
			}
		})
	}

	t.Run("tui", func(t *testing.T) {
		// stdin carries logs, so the keyboard is /dev/tty: the pty, the child's controlling
		// terminal. The TUI may ask the terminal its background, once and bounded at 1 s.
		s := startSilent(t, strings.NewReader("ERROR first\nINFO second\nWARN third\n"), "--explain-dry-run")
		var drawn time.Duration
		for deadline := s.start.Add(killAfter); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if strings.Contains(s.output(), "Watching") {
				drawn = time.Since(s.start)
				break
			}
		}
		if drawn == 0 {
			t.Fatalf("the TUI drew nothing within %v on a terminal that never answers: "+
				"it is waiting for an answer (#155)\noutput: %q", killAfter, s.output())
		}
		if drawn > tuiBound {
			t.Errorf("the TUI drew its first frame after %v on a terminal that never answers, want under %v (#155)",
				drawn, tuiBound)
		}
		_, _ = s.master.WriteString("q")
		if _, exited := s.wait(); !exited {
			t.Errorf("the TUI did not quit on q\noutput: %q", s.output())
		}
	})
}

// TestStartupChild is the child half of TestStartupAsksTheTerminalNothing. Run directly, it
// SKIPS rather than passes: a PASS that did nothing would be vacuous.
func TestStartupChild(t *testing.T) {
	args := os.Getenv(startupChildEnv)
	if args == "" {
		t.Skip("child of TestStartupAsksTheTerminalNothing; runs only when re-executed by it")
	}
	os.Args = append([]string{"logscry"}, strings.Fields(args)...)
	main()
	// Exit here, so that the testing package writes nothing to the terminal after logscry.
	os.Exit(0)
}

// openPTY returns the two ends of a fresh pseudo-terminal, 120x24. The slave is opened without
// becoming this process's controlling terminal: it becomes the child's.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil { // unlockpt
		_ = m.Close()
		t.Fatalf("unlockpt: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN) // ptsname
	if err != nil {
		_ = m.Close()
		t.Fatalf("ptsname: %v", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		t.Fatalf("open pts: %v", err)
	}
	_ = unix.IoctlSetWinsize(int(s.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 120})
	t.Cleanup(func() { _ = s.Close(); _ = m.Close() })
	return m, s
}
