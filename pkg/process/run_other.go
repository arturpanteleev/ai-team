//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
)

func Run(ctx context.Context, command *exec.Cmd) error {
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		killErr := killTree(command.Process.Pid)
		waitErr := <-done
		return errors.Join(ctx.Err(), killErr, waitErr)
	}
}

// killTree best-effort terminates a process and its descendants on
// cancellation/timeout. On Windows, `taskkill /T /F` terminates the whole
// process tree — a plain Process.Kill only ever killed the direct child,
// leaving any descendants (e.g. a spawned shell script's own children)
// running past the timeout. Falls back to killing just the direct process
// if taskkill isn't available (e.g. on non-Windows platforms that still
// build this file, or if taskkill itself fails for any reason) — never
// worse than the previous behavior, only better when taskkill succeeds.
func killTree(pid int) error {
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run(); err == nil {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}
