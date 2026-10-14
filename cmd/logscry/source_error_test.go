// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stderrCapture swaps os.Stderr for a pipe and keeps what is written to it, readable while
// the code under test is still running. runPlain reports source errors straight to the real
// stderr, which is the thing under test here.
type stderrCapture struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
	stop func()
}

func captureStderr(t *testing.T) *stderrCapture {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w
	c := &stderrCapture{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			c.mu.Lock()
			c.buf.Write(b[:n])
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	var once sync.Once
	c.stop = func() {
		once.Do(func() {
			os.Stderr = saved
			_ = w.Close()
			<-c.done
			_ = r.Close()
		})
	}
	t.Cleanup(c.stop)
	return c
}

// final restores os.Stderr and returns everything written to it, once the pipe has been
// read to the end.
func (c *stderrCapture) final() string {
	c.stop()
	return c.String()
}

func (c *stderrCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// waitFor polls until stderr holds want, and reports false once the bound has passed.
func (c *stderrCapture) waitFor(want string, bound time.Duration) bool {
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if strings.Contains(c.String(), want) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// endlessJournalctl puts a journalctl first on PATH that prints nothing and never exits: a
// source that is still running when another one fails.
func endlessJournalctl(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nexec sleep 3600\n"
	if err := os.WriteFile(filepath.Join(dir, "journalctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runInBackground runs logscry with args, returning a channel that delivers run's result.
func runInBackground(ctx context.Context, args ...string) <-chan error {
	done := make(chan error, 1)
	go func() { done <- run(ctx, args) }()
	return done
}

// TestPlainReportsASourceErrorWhileOthersRun is #97's second layer, as the user sees it. One
// source fails while another is still running: the failure must be on stderr at once, not
// when every source has ended, and the run goes on. Ctrl-C then reports nothing more.
func TestPlainReportsASourceErrorWhileOthersRun(t *testing.T) {
	endlessJournalctl(t)
	stderr := captureStderr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := runInBackground(ctx, "--plain", "--explain-dry-run", "--journald", "--", "sh", "-c", "exit 3")

	const want = "logscry: ingest: exit status 3"
	if !stderr.waitFor(want, 2*time.Second) {
		cancel()
		<-done
		t.Fatalf("stderr had no %q 2s after the subprocess failed, with journald still running:\n%s", want, stderr)
	}
	select {
	case err := <-done:
		t.Fatalf("run returned %v while journald was still running: the failure ended the run", err)
	default:
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run = %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	if got := stderr.final(); strings.Count(got, "ingest:") != 1 {
		t.Errorf("stderr has %d ingest: lines, want exactly 1 (none for the cancel):\n%s", strings.Count(got, "ingest:"), got)
	}
}

// TestPlainSourceErrorExitStatus pins what a source error does to the exit status: it is
// reported once on stderr, and logscry still exits 0 once every source has ended. A source's
// outcome — a watched app crashing included — is reported, not exit-coded.
func TestPlainSourceErrorExitStatus(t *testing.T) {
	stderr := captureStderr(t)
	select {
	case err := <-runInBackground(context.Background(), "--plain", "--explain-dry-run", "--", "sh", "-c", "exit 3"):
		if err != nil {
			t.Errorf("run = %v, want nil (exit status 0)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return once its only source had ended")
	}
	got := stderr.final()
	if n := strings.Count(got, "logscry: ingest: exit status 3"); n != 1 {
		t.Errorf("stderr has the source error %d times, want exactly 1:\n%s", n, got)
	}
	if n := strings.Count(got, "ingest:"); n != 1 {
		t.Errorf("stderr has %d ingest: lines, want exactly 1:\n%s", n, got)
	}
}
