//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// Package process supervises controller child processes and their descendants.
package process

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func Run(ctx context.Context, command *exec.Cmd) error {
	return run(ctx, command, 0)
}

// RunGraceful requests shutdown of the supervised process group before force
// killing it. Codex owns MCP child lifecycles and needs a chance to close its
// server processes when an agent stage reaches its deadline.
func RunGraceful(ctx context.Context, command *exec.Cmd, grace time.Duration) error {
	if grace < 0 {
		grace = 0
	}
	return run(ctx, command, grace)
}

func run(ctx context.Context, command *exec.Cmd, grace time.Duration) error {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if grace > 0 {
			termErr := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
			if errors.Is(termErr, syscall.ESRCH) {
				termErr = nil
			}
			timer := time.NewTimer(grace)
			defer timer.Stop()
			var waitErr error
			select {
			case waitErr = <-done:
				// Codex may exit before an MCP child does. Keep the group under
				// supervision for the full grace window, then kill any survivor.
				<-timer.C
			case <-timer.C:
				waitErr = <-done
			}
			killErr := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			if errors.Is(killErr, syscall.ESRCH) {
				killErr = nil
			}
			return errors.Join(ctx.Err(), termErr, killErr, waitErr)
		}
		killErr := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(killErr, syscall.ESRCH) {
			killErr = nil
		}
		waitErr := <-done
		return errors.Join(ctx.Err(), killErr, waitErr)
	}
}

// CleanupReceipt фиксирует результат уничтожения process tree при отмене run
// (containment receipt axis proc). Verified=true означает, что все отслеживаемые
// PIDs завершились в течение timeout.
type CleanupReceipt struct {
	Verified bool  `json:"verified"`
	PIDs     []int `json:"pids,omitempty"`
	Timeout  bool  `json:"timeout,omitempty"`
}

// cleanupWaitTimeout — максимальное время ожидания завершения process tree.
const cleanupWaitTimeout = 2 * time.Second

// TrackAndCleanup отправляет SIGKILL процессной группе pgid и проверяет, что
// отслеживаемые PIDs завершились. Возвращает receipt для evidence. best-effort:
// при недоступности PID receipt честно сообщает Verified=false.
func TrackAndCleanup(pgid int, trackedPIDs []int) CleanupReceipt {
	receipt := CleanupReceipt{PIDs: append([]int(nil), trackedPIDs...)}

	// SIGKILL по всей process group + fallback на индивидуальные PIDs.
	if pgid > 0 {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			for _, pid := range trackedPIDs {
				if perr := syscall.Kill(pid, syscall.SIGKILL); perr != nil && !errors.Is(perr, syscall.ESRCH) {
					break
				}
			}
		}
	} else {
		for _, pid := range trackedPIDs {
			// Ошибка Kill здесь не влияет ни на что: ESRCH означает, что процесс
			// уже завершился (нормальный случай), а остальные ошибки всё равно
			// не дают способа убить процесс — ниже идёт цикл ожидания с
			// таймаутом, который и определяет итог.
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}

	deadline := time.Now().Add(cleanupWaitTimeout)
	for {
		allGone := true
		for _, pid := range trackedPIDs {
			if err := syscall.Kill(pid, 0); err == nil {
				allGone = false
				break
			}
		}
		if allGone {
			// Верно даже если kill вернул ESRCH (процессы уже завершились) —
			// Verified=true если ни один процесс не жив по завершении.
			receipt.Verified = true
			return receipt
		}
		if time.Now().After(deadline) {
			receipt.Timeout = true
			receipt.Verified = false
			return receipt
		}
		time.Sleep(20 * time.Millisecond)
	}
}
