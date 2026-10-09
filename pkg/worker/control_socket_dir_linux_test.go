//go:build linux

package worker

import (
	"os"
	"testing"
)

func TestControllerSocketDirectoryFallsBackOutsidePrimaryWritableParent(t *testing.T) {
	dir, err := createControllerWorkerSocketDir("/tmp", "", "")
	if err != nil {
		t.Fatalf("create socket directory outside /tmp worker bind: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if isWithinPath("/tmp", dir) {
		t.Fatalf("socket directory %q remained inside worker-writable /tmp", dir)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("fallback socket directory is not private: info=%v err=%v", info, err)
	}
}
