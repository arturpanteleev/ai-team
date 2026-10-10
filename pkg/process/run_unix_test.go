//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRunKillsCommandProcessGroupOnTimeout(t *testing.T) {
	const deadlineDelay = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadlineDelay)
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
	// Bound derived from the configured context deadline plus a wide margin:
	// the process group must die at cancellation, not after `sleep 3`.
	if elapsed := time.Since(started); elapsed >= deadlineDelay+time.Second {
		t.Fatalf("Run waited %s; descendant likely survived cancellation", elapsed)
	}
}

func TestRunGracefulSignalsProcessGroupBeforeForceKill(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "mcp-cleaned")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	command := exec.Command("sh", "-c", `trap 'printf stopped > "$MCP_CLEANUP_MARKER"; exit 0' TERM; while :; do sleep 30; done`)
	command.Env = append(os.Environ(), "MCP_CLEANUP_MARKER="+marker)
	err := RunGraceful(ctx, command, time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if contents, readErr := os.ReadFile(marker); readErr != nil || string(contents) != "stopped" {
		t.Fatalf("Codex-style owner must receive shutdown signal before force kill: contents=%q err=%v", contents, readErr)
	}
}

func TestRunGracefulKeepsSupervisingAfterCodexExits(t *testing.T) {
	const deadlineDelay = 100 * time.Millisecond
	const grace = 300 * time.Millisecond
	marker := filepath.Join(t.TempDir(), "orphan-mcp-survived")
	ctx, cancel := context.WithTimeout(context.Background(), deadlineDelay)
	defer cancel()
	command := exec.Command("sh", "-c", `(trap '' TERM; sleep 1; printf leaked > "$MCP_ORPHAN_MARKER") & trap 'exit 0' TERM; while :; do sleep 30; done`)
	command.Env = append(os.Environ(), "MCP_ORPHAN_MARKER="+marker)
	started := time.Now()
	err := RunGraceful(ctx, command, grace)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	// Lower bound derived from the context deadline plus the cleanup grace:
	// the supervisor must not return before both windows elapsed.
	if elapsed := time.Since(started); elapsed < deadlineDelay+grace-50*time.Millisecond {
		t.Fatalf("supervisor returned before the child cleanup grace elapsed: %s", elapsed)
	}
	// The orphaned MCP child would leak the marker roughly one second after
	// its own start. Poll the whole write window instead of sleeping once
	// and checking afterwards, so a leak fails the test immediately.
	for window := started.Add(1100 * time.Millisecond); time.Now().Before(window); {
		if contents, readErr := os.ReadFile(marker); !os.IsNotExist(readErr) {
			t.Fatalf("MCP child outlived Codex cancellation cleanup: contents=%q err=%v", contents, readErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Regression: a leader that ignores SIGTERM used to make run block forever in
// `waitErr = <-done` because the SIGKILL was only sent after the wait. The
// force kill must happen when the grace timer expires, and the supervisor must
// return within context deadline + grace + ε.
func TestRunGracefulForceKillsLeaderIgnoringSIGTERM(t *testing.T) {
	const deadlineDelay = 100 * time.Millisecond
	const grace = 300 * time.Millisecond
	const margin = 250 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadlineDelay)
	defer cancel()
	command := exec.Command("sh", "-c", `trap '' TERM; while :; do sleep 30; done`)

	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- RunGraceful(ctx, command, grace) }()

	bound := deadlineDelay + grace + margin
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline error, got %v", err)
		}
		if elapsed := time.Since(started); elapsed > bound {
			t.Fatalf("RunGraceful took %s, want at most %s (deadline + grace + ε)", elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("RunGraceful did not return within %s: force kill unreachable while the leader ignores SIGTERM", bound)
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
