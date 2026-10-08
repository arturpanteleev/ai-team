//go:build !linux

package worker

import (
	"context"
	"errors"
	"os/exec"
)

func checkBubblewrapAvailable() error {
	return errors.New("Linux bubblewrap worker isolation is only supported on Linux")
}

func bubblewrapWorkerCommand(context.Context, *exec.Cmd, string, string, string, []string, []string) (*exec.Cmd, error) {
	return nil, errors.New("Linux bubblewrap worker isolation is only supported on Linux")
}

func bubblewrapWorkerCommandWithInputs(context.Context, *exec.Cmd, string, string, string, []string, []string, []workerReadOnlyInputMount) (*exec.Cmd, error) {
	return nil, errors.New("Linux bubblewrap worker isolation is only supported on Linux")
}
