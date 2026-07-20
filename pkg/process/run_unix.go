//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// Package process supervises controller child processes and their descendants.
package process

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

func Run(ctx context.Context, command *exec.Cmd) error {
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
		killErr := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(killErr, syscall.ESRCH) {
			killErr = nil
		}
		waitErr := <-done
		return errors.Join(ctx.Err(), killErr, waitErr)
	}
}
