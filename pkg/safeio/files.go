package safeio

import (
	"fmt"
	"io"
	"os"
)

// ReadRegularFile reads a bounded regular file and rejects symlinks, devices,
// FIFOs and path replacement between lstat and open.
func ReadRegularFile(path string, maxBytes int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s должен быть regular file без symlink", path)
	}
	if maxBytes <= 0 || before.Size() > maxBytes {
		return nil, fmt.Errorf("%s превышает лимит %d bytes", path, maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("%s изменён во время безопасного открытия", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s превышает лимит %d bytes", path, maxBytes)
	}
	return data, nil
}
