//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package evidence

import (
	"os"
	"sync"
)

var eventLogPathLocks sync.Map // map[string]*sync.Mutex

func openLockedEventLog(path string, write bool, closeFile func(*os.File) error) (*os.File, func() error, error) {
	value, _ := eventLogPathLocks.LoadOrStore(path, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	flags := os.O_RDONLY
	if write {
		flags = os.O_RDWR
	}
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		lock.Unlock()
		return nil, nil, err
	}
	if err := validateOpenedEventLogPath(file, path); err != nil {
		_ = file.Close()
		lock.Unlock()
		return nil, nil, err
	}
	var releaseOnce sync.Once
	var closeErr error
	return file, func() error {
		releaseOnce.Do(func() {
			closeErr = closeEventLogFile(file, closeFile)
			lock.Unlock()
		})
		return closeErr
	}, nil
}
