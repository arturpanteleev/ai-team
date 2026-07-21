//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package process

import (
	"context"
	"errors"
	"os/exec"
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
		killErr := command.Process.Kill()
		waitErr := <-done
		return errors.Join(ctx.Err(), killErr, waitErr)
	}
}
