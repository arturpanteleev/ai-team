//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package approval

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// checkKeyFileAccess требует, чтобы ключ принадлежал текущему пользователю и
// был закрыт от группы и остальных. MAC защищает ровно до тех пор, пока
// секрет не прочитан посторонним процессом, поэтому слабые права — это отказ,
// а не предупреждение.
func checkKeyFileAccess(path string) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return fmt.Errorf("approval key %s: %w", path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("approval key %s должен быть regular file", path)
	}
	if uint64(stat.Uid) != uint64(os.Geteuid()) {
		return fmt.Errorf("approval key %s принадлежит другому пользователю (uid %d)", path, stat.Uid)
	}
	if stat.Mode&0o077 != 0 {
		return fmt.Errorf("approval key %s доступен группе или остальным (mode %04o): chmod 400 %s",
			path, stat.Mode&0o777, path)
	}
	return nil
}
