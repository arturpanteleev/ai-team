//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package approval

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"golang.org/x/sys/unix"
)

// lockRun сериализует read-modify-write циклы Decide/Create между процессами
// (CLI decision и web control plane пишут в один store). Блокирующий flock
// безопасен: ядро снимает его при смерти процесса.
func (s *Store) lockRun(runID string) (func(), error) {
	directory, err := safeio.EnsureDir(s.root, runID)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, ".decide.lock")
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
		return nil, fmt.Errorf("approval lock должен быть single-link regular file текущего пользователя")
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("approval lock: не удалось создать file handle")
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("approval lock: %w", err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
