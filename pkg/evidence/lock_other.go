//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package evidence

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

type WorkspaceLock struct {
	path string
}

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
		return nil, fmt.Errorf("workspace уже занят другим run: %w", err)
	}
	return &WorkspaceLock{path: path}, nil
}

func (l *WorkspaceLock) Close() error {
	if l == nil || l.path == "" {
		return nil
	}
	return os.Remove(l.path)
}
