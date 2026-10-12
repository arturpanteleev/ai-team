package checks

import (
	"context"
	"os/exec"
)

// CommandSandbox wraps a check command in an OS-enforced untrusted-code
// boundary. Its receipt describes the mechanisms the wrapper actually uses;
// callers must not synthesize a receipt without a functioning sandbox.
type CommandSandbox interface {
	Command(ctx context.Context, workingDir, executable string, args ...string) (*exec.Cmd, error)
	Enforced() bool
	Close() error
}
