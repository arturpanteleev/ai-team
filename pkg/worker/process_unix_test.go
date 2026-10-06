//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package worker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestConfigureWorkerProcessCancelHandlesMissingAndFinishedProcess(t *testing.T) {
	t.Run("process not started", func(t *testing.T) {
		command := exec.CommandContext(context.Background(), "true")
		configureWorkerProcess(command)
		if err := command.Cancel(); !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("cancel without process should report it is done: %v", err)
		}
	})

	t.Run("process group already exited", func(t *testing.T) {
		command := exec.CommandContext(context.Background(), "true")
		configureWorkerProcess(command)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
		if err := command.Cancel(); !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("cancel after process exit should be treated as done: %v", err)
		}
	})
}
