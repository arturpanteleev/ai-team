//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package evidence

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceLockRejectsHardLinkWithoutTruncatingTarget(t *testing.T) {
	target := t.TempDir()
	initial, err := AcquireWorkspaceLock(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(target, ".ai-team", "locks", "workspace.lock")
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	victim := filepath.Join(target, "victim-hardlink")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, lockPath); err != nil {
		t.Skipf("hard links unsupported: %v", err)
	}
	if _, err := AcquireWorkspaceLock(target); err == nil {
		t.Fatal("hard-linked lock file must fail closed")
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "keep" {
		t.Fatalf("hard-link victim must not be truncated: %q err=%v", data, err)
	}
}
