package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// createControllerWorkerSocketDir keeps capability-bearing sockets outside
// every worker-writable bind. Bubblewrap exposes this directory only through
// its read-only host-root mount; the worker can connect but cannot unlink or
// replace either socket pathname.
func createControllerWorkerSocketDir(target, home, temp string) (string, error) {
	for _, parent := range []string{"/tmp", "/var/tmp"} {
		canonicalParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			continue
		}
		canonicalParent = filepath.Clean(canonicalParent)
		if isWithinPath("/run", canonicalParent) || isWithinPath("/dev", canonicalParent) {
			continue
		}
		dir, err := os.MkdirTemp(canonicalParent, "ai-team-worker-control-*")
		if err != nil {
			continue
		}
		unsafe := false
		for _, writable := range []string{target, home, temp} {
			if writable != "" && isWithinPath(filepath.Clean(writable), dir) {
				unsafe = true
				break
			}
		}
		if unsafe {
			_ = os.RemoveAll(dir)
			continue
		}
		if err := os.Chmod(dir, 0700); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
		return dir, nil
	}
	return "", errors.New("no private socket directory outside worker-writable binds")
}

func isWithinPath(parent, path string) bool {
	if parent == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !filepath.IsAbs(rel) && !startsWithParentTraversal(rel))
}

func startsWithParentTraversal(path string) bool {
	return path == ".." || len(path) > 3 && path[:3] == ".."+string(filepath.Separator)
}

func validateWorkerSocketPath(path, role, target, home, temp string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%s must be an absolute clean path", role)
	}
	for _, writable := range []string{target, home, temp} {
		if writable != "" && isWithinPath(filepath.Clean(writable), path) {
			return fmt.Errorf("%s must be outside worker-writable bind %s", role, writable)
		}
	}
	if isWithinPath("/run", path) || isWithinPath("/dev", path) {
		return fmt.Errorf("%s must not be under a bubblewrap masked path", role)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", role, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not a Unix socket", role)
	}
	return nil
}
