package safeio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// OpenRegularNoFollow opens path with flag and perm and refuses to follow a
// symlink at the leaf. An existing file must be a regular file; it is opened
// and then compared with the earlier lstat so a replacement between the two
// steps is detected. A missing file is created only when flag has O_CREATE, and
// then always with O_EXCL, so a symlink planted after the lstat fails the open
// instead of being followed. It is meant for small exchange files that another
// local process may also touch (the fake tracker bridge), not for evidence.
func OpenRegularNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	before, err := os.Lstat(path)
	switch {
	case err == nil:
		if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
			return nil, fmt.Errorf("%s должен быть regular file без symlink", path)
		}
		file, err := os.OpenFile(path, flag&^os.O_CREATE&^os.O_EXCL, perm)
		if err != nil {
			return nil, err
		}
		after, err := file.Stat()
		if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
			_ = file.Close()
			return nil, fmt.Errorf("%s изменён во время безопасного открытия", path)
		}
		return file, nil
	case errors.Is(err, fs.ErrNotExist) && flag&os.O_CREATE != 0:
		return os.OpenFile(path, flag|os.O_CREATE|os.O_EXCL, perm)
	default:
		return nil, err
	}
}
