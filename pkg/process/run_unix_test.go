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
	marker := filepath.Join(t.TempDir(), "orphan-mcp-survived")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	command := exec.Command("sh", "-c", `(trap '' TERM; sleep 1; printf leaked > "$MCP_ORPHAN_MARKER") & trap 'exit 0' TERM; while :; do sleep 30; done`)
	command.Env = append(os.Environ(), "MCP_ORPHAN_MARKER="+marker)
	started := time.Now()
	err := RunGraceful(ctx, command, 300*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed < 350*time.Millisecond {
		t.Fatalf("supervisor returned before the child cleanup grace elapsed: %s", elapsed)
	}
	time.Sleep(1100 * time.Millisecond)
	if contents, readErr := os.ReadFile(marker); !os.IsNotExist(readErr) {
		t.Fatalf("MCP child outlived Codex cancellation cleanup: contents=%q err=%v", contents, readErr)
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
