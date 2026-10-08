//go:build linux || darwin

package worker

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openQuestionAnswerMountFileNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func questionAnswerMountFileLinkCount(file *os.File) (uint64, error) {
	if file == nil {
		return 0, errors.New("clarification input file is unavailable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Nlink), nil
}
