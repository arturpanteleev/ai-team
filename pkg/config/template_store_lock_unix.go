//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"golang.org/x/sys/unix"
)

// publishLockSupported reports whether lockPublish provides real cross-process
// serialization on this platform. It lets tests skip the contention scenario
// where the fallback is a no-op.
const publishLockSupported = true

// lockPublish serializes the read-modify-write cycle of Publish across
// processes (the web control plane and a CLI/worker can publish against the
// same project). The blocking flock is safe: the kernel releases it when a
// process dies, so a crashed publisher cannot leave the store wedged.
//
// The lock file is a regular, single-link file owned by the current user, opened
// with O_NOFOLLOW. Its contents are never read; it exists only to carry the
// advisory lock. It lives next to the published versions so every store that
// targets the same project resolves the same path.
func (s *TemplateStore) lockPublish() (func(), error) {
	directory, err := safeio.EnsureDir(s.target, ".ai-team", "templates")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, ".publish.lock")
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
		return nil, fmt.Errorf("template publish lock должен быть single-link regular file текущего пользователя")
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("template publish lock: не удалось создать file handle")
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("template publish lock: %w", err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
