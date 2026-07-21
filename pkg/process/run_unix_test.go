//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package process

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestRunKillsCommandProcessGroupOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	command := exec.Command("sh", "-c", "sleep 3 & wait")
	// A background child inherits this pipe. Killing only the shell leaves the
	// pipe open and exec.Cmd.Wait blocks until sleep exits; killing the process
	// group closes it immediately.
	var output bytes.Buffer
	command.Stdout = &output

	started := time.Now()
	err := Run(ctx, command)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("Run waited %s; descendant likely survived cancellation", elapsed)
	}
}
