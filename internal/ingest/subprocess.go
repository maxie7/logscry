// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"errors"
	"os/exec"
	"sync"

	"github.com/maxie7/logscry/internal/model"
)

// SubprocessSource runs a command and captures its stdout+stderr as log lines.
type SubprocessSource struct {
	// Argv is the command and its arguments (Argv[0] is the executable).
	Argv []string
}

// NewSubprocessSource returns a Source that runs the given command.
func NewSubprocessSource(argv []string) *SubprocessSource {
	return &SubprocessSource{Argv: argv}
}

// Name implements Source. It follows the RDI convention of "proc:<executable>".
func (s *SubprocessSource) Name() string {
	if len(s.Argv) == 0 {
		return "proc"
	}
	return "proc:" + s.Argv[0]
}

// Lines implements Source: it runs Argv, reading its stdout and stderr
// concurrently and emitting one LogLine per line, tagged with the originating
// stream. It returns once the process exits or ctx is cancelled (which kills the
// process). A non-zero exit is reported as an error — expected for a crashing
// target — so callers should treat it as informational, not fatal.
func (s *SubprocessSource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	if len(s.Argv) == 0 {
		return errors.New("subprocess: empty argv")
	}

	cmd := exec.CommandContext(ctx, s.Argv[0], s.Argv[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var (
		wg             sync.WaitGroup
		outErr, errErr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		outErr = readLines(ctx, stdout, s.Name(), model.Stdout, out)
	}()
	go func() {
		defer wg.Done()
		errErr = readLines(ctx, stderr, s.Name(), model.Stderr, out)
	}()

	// All reads from the pipes must complete before Wait (os/exec requirement).
	wg.Wait()
	waitErr := cmd.Wait()

	return errors.Join(outErr, errErr, waitErr)
}
