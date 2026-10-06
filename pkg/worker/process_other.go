//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package worker

import "os/exec"

// Other platforms retain os/exec's standard single-process cancellation.
func configureWorkerProcess(*exec.Cmd) {}
