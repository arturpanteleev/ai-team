//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func Run(ctx context.Context, command *exec.Cmd) error {
	return run(ctx, command, 0)
}

// RunGraceful gives a supervised command a bounded opportunity to stop owned
// descendants before the process-tree force kill.
func RunGraceful(ctx context.Context, command *exec.Cmd, grace time.Duration) error {
	if grace < 0 {
		grace = 0
	}
	return run(ctx, command, grace)
}

func run(ctx context.Context, command *exec.Cmd, grace time.Duration) error {
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		var signalErr error
		if grace > 0 {
			// Graceful attempt: `taskkill /T` (no /F) asks the tree to stop on
			// its own. Process.Signal(os.Interrupt) is unimplemented on
			// Windows (EWINDOWS) and delivered nothing.
			signalErr = stopTree(command.Process.Pid)
			timer := time.NewTimer(grace)
			defer timer.Stop()
			var waitErr, killErr error
			select {
			case waitErr = <-done:
				// The command may exit before a descendant does. Force-kill
				// the tree now instead of waiting out the full grace window
				// on an already reaped PID, then observe the window.
				killErr = killTree(command.Process.Pid)
				<-timer.C
			case <-timer.C:
				// Grace expired: force-kill BEFORE waiting so a command that
				// ignores the graceful attempt cannot block <-done forever.
				killErr = killTree(command.Process.Pid)
				waitErr = <-done
			}
			return errors.Join(ctx.Err(), signalErr, killErr, waitErr)
		}
		killErr := killTree(command.Process.Pid)
		waitErr := <-done
		return errors.Join(ctx.Err(), signalErr, killErr, waitErr)
	}
}

// stopTree requests a graceful stop of a process tree without force
// (`taskkill /T`, no /F). Best-effort: the caller force-kills via killTree
// when the grace window expires, and a command that cannot be stopped
// gracefully is reported as part of the joined result.
func stopTree(pid int) error {
	return exec.Command("taskkill", "/T", "/PID", strconv.Itoa(pid)).Run()
}

// killTree best-effort force-terminates a process and its descendants on
// cancellation/timeout. On Windows, `taskkill /T /F` terminates the whole
// process tree — a plain Process.Kill only ever killed the direct child,
// leaving any descendants (e.g. a spawned shell script's own children)
// running past the timeout. Falls back to killing just the direct process
// if taskkill isn't available (e.g. on non-Windows platforms that still
// build this file, or if taskkill itself fails for any reason) — never
// worse than the previous behavior, only better when taskkill succeeds.
// An already finished tree is success (the ESRCH equivalent): taskkill
// fails when the PID is gone and os.FindProcess reports a missing process.
func killTree(pid int) error {
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run(); err == nil {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// CleanupReceipt фиксирует результат уничтожения process tree при отмене run.
// См. run_unix.go для деталей полей.
type CleanupReceipt struct {
	Verified bool  `json:"verified"`
	PIDs     []int `json:"pids,omitempty"`
	Timeout  bool  `json:"timeout,omitempty"`
}

// TrackAndCleanup — best-effort процессный cleanup для non-Unix платформ
// (Windows). Использует taskkill /T /F; верификация по проверке недоступности
// PID — честно best-effort (Verified может быть false при недоступности).
func TrackAndCleanup(pgid int, trackedPIDs []int) CleanupReceipt {
	receipt := CleanupReceipt{PIDs: append([]int(nil), trackedPIDs...)}
	for _, pid := range trackedPIDs {
		_ = killTree(pid)
	}
	// На non-Unix не можем надёжно проверить завершение дерева — честный
	// best-effort без утверждения Verified.
	receipt.Verified = false
	receipt.Timeout = true
	return receipt
}
