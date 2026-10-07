//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package evidence

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

func openLockedEventLog(path string, write bool, closeFile func(*os.File) error) (*os.File, func() error, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	lockMode := unix.LOCK_SH
	if write {
		flags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
		lockMode = unix.LOCK_EX
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("event log: failed to create file handle")
	}
	if err := validateOpenedEventLogPath(file, path); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if err := unix.Flock(fd, lockMode); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if err := validateOpenedEventLogPath(file, path); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = file.Close()
		return nil, nil, err
	}
	var releaseOnce sync.Once
	var closeErr error
	unlock := func() error {
		releaseOnce.Do(func() {
			_ = unix.Flock(fd, unix.LOCK_UN)
			closeErr = closeEventLogFile(file, closeFile)
		})
		return closeErr
	}
	return file, unlock, nil
}
