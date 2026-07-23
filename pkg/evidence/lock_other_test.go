//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package evidence

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Note: this file shares its build tag with lock_other.go, so it cannot be
// compiled or run on this project's development machine (darwin) or in its
// current CI (ubuntu-only runners) — verified instead by cross-compilation
// (`GOOS=windows go build ./...` and `GOOS=plan9 go build ./...`) and code
// review. It will run for real the moment either environment adds a
// matching runner.

func TestReclaimStaleLockLeavesLiveProcessAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, pidFileName), []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		t.Fatal(err)
	}
	if reclaimStaleLock(dir) {
		t.Fatal("must not reclaim a lock whose pid file names the current (definitely alive) process")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("lock directory must be untouched: %v", err)
	}
}

func TestReclaimStaleLockLeavesMissingPidFileAlone(t *testing.T) {
	dir := t.TempDir()
	if reclaimStaleLock(dir) {
		t.Fatal("must not reclaim a lock with no pid file — inconclusive, not evidence of death")
	}
}

func TestReclaimStaleLockLeavesUnparseablePidAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, pidFileName), []byte("not-a-pid"), 0644); err != nil {
		t.Fatal(err)
	}
	if reclaimStaleLock(dir) {
		t.Fatal("must not reclaim a lock with an unparseable pid — inconclusive, not evidence of death")
	}
}

func TestAcquireWorkspaceLockWritesPidAndRefusesConcurrentAcquire(t *testing.T) {
	target := t.TempDir()
	lock, err := AcquireWorkspaceLock(target)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	pidData, err := os.ReadFile(filepath.Join(lock.path, pidFileName))
	if err != nil || string(pidData) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock must record the current process pid: data=%q err=%v", pidData, err)
	}

	if _, err := AcquireWorkspaceLock(target); err == nil {
		t.Fatal("a second acquire while the current process (definitely alive) holds the lock must fail, not reclaim it")
	}
}

func TestAcquireWorkspaceLockRecoversAfterClose(t *testing.T) {
	target := t.TempDir()
	lock, err := AcquireWorkspaceLock(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock.path); !os.IsNotExist(err) {
		t.Fatalf("Close must remove the lock directory entirely (including the pid file inside it): %v", err)
	}
	if _, err := AcquireWorkspaceLock(target); err != nil {
		t.Fatalf("re-acquiring after a clean Close must succeed: %v", err)
	}
}
