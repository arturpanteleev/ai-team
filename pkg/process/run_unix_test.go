//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package process

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
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

func TestTrackAndCleanupKillsProcessGroup(t *testing.T) {
	command := exec.Command("sh", "-c", "sleep 30 & wait")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	childPID := command.Process.Pid

	// Ждём завершения shell в фоне (reap), чтобы PID не оставался zombie.
	waitCh := make(chan error, 1)
	go func() { waitCh <- command.Wait() }()

	// Cleanup должен убить и shell, и его потомка (sleep) через process group.
	receipt := TrackAndCleanup(childPID, []int{childPID})
	if receipt.Timeout {
		t.Fatalf("cleanup should not time out, got %+v", receipt)
	}
	if !receipt.Verified {
		t.Fatalf("expected verified cleanup, got %+v", receipt)
	}
	// Shell и потомок должны завершиться из-за SIGKILL группе — Wait вернётся
	// быстро (не ждём оставшиеся ~30 секунд sleep).
	select {
	case <-waitCh:
		// ок — процесс завершён
	case <-time.After(2 * time.Second):
		t.Fatalf("process tree survived cleanup")
	}
}
