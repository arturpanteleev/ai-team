//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package config

// publishLockSupported reports whether lockPublish provides real cross-process
// serialization on this platform. It lets tests skip the contention scenario
// where the fallback is a no-op.
const publishLockSupported = false

// lockPublish on platforms without flock is a no-op: cross-process
// serialization remains the responsibility of an external single-writer
// discipline. Within one process, Publish is still serialized by s.mu.
func (s *TemplateStore) lockPublish() (func(), error) {
	return func() {}, nil
}
