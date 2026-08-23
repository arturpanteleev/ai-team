//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package approval

// lockRun на платформах без flock возвращает no-op: сериализация между
// процессами остаётся за внешней дисциплиной single-writer (см. README,
// граница безопасности).
func (s *Store) lockRun(runID string) (func(), error) {
	return func() {}, nil
}
