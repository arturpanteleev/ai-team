//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package evidence

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"golang.org/x/sys/unix"
)

type WorkspaceLock struct {
	file *os.File
}

func AcquireWorkspaceLock(target string) (*WorkspaceLock, error) {
	lockDir, err := safeio.EnsureDir(target, ".ai-team", "locks")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(lockDir, "workspace.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0644)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || uint64(stat.Uid) != uint64(os.Geteuid()) {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("workspace lock должен быть single-link regular file текущего пользователя")
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("workspace lock: не удалось создать file handle")
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("workspace уже занят другим run: %w", err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
		_ = f.Sync()
	}
	return &WorkspaceLock{file: f}, nil
}

func (l *WorkspaceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
