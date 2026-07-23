//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package evidence

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

type WorkspaceLock struct {
	path string
}

const pidFileName = "pid"

func AcquireWorkspaceLock(target string) (*WorkspaceLock, error) {
	lockDir, err := safeio.EnsureDir(target, ".ai-team", "locks")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(lockDir, "workspace.lock.d")
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace lock path не может быть symlink")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	if err := os.Mkdir(path, 0755); err != nil {
		if !reclaimStaleLock(path) {
			return nil, fmt.Errorf("workspace уже занят другим run: %w", err)
		}
		if retryErr := os.Mkdir(path, 0755); retryErr != nil {
			return nil, fmt.Errorf("workspace уже занят другим run: %w", retryErr)
		}
	}
	_ = os.WriteFile(filepath.Join(path, pidFileName), []byte(strconv.Itoa(os.Getpid())), 0644)
	return &WorkspaceLock{path: path}, nil
}

// reclaimStaleLock removes an existing lock directory only when there is
// strong positive evidence its owning process no longer exists — never
// based merely on absence of evidence. A killed/crashed process previously
// left this lock stuck forever with no recovery path (independent audit
// Finding 6). This intentionally only recognizes definitive process-absence
// signals; anything inconclusive (missing/unreadable pid file, a liveness
// check that can't prove death) leaves the lock in place, matching prior
// behavior exactly. Not verified on a real Windows machine — cross-compiled
// and reasoned through, not run.
func reclaimStaleLock(path string) bool {
	data, err := os.ReadFile(filepath.Join(path, pidFileName))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	// On Windows, os.FindProcess itself opens a handle to the process and
	// fails if it doesn't exist (unlike POSIX, where FindProcess always
	// succeeds regardless of whether the pid is live) — this is the
	// strongest portable liveness signal available here without a
	// Windows-specific syscall dependency. A successful FindProcess is
	// treated as "cannot disprove liveness" and leaves the lock alone.
	if _, err := os.FindProcess(pid); err != nil {
		return os.RemoveAll(path) == nil
	}
	return false
}

func (l *WorkspaceLock) Close() error {
	if l == nil || l.path == "" {
		return nil
	}
	return os.RemoveAll(l.path)
}
