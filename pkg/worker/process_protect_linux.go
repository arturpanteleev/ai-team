//go:build linux

package worker

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// ProtectWorkerProcess prevents same-UID child runtimes from reading this
// process's initial environment through /proc/<pid>/environ or attaching with
// ptrace. The controller capabilities are captured before this is called and
// remain usable by the worker's in-memory API clients.
func ProtectWorkerProcess() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("protect worker process from procfs inspection: %w", err)
	}
	return nil
}
