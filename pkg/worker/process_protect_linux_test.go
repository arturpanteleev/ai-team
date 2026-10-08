//go:build linux

package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProtectWorkerProcessMakesWorkerNonDumpable(t *testing.T) {
	previous, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("read initial process dumpable flag: %v", err)
	}
	t.Cleanup(func() {
		if err := unix.Prctl(unix.PR_SET_DUMPABLE, uintptr(previous), 0, 0, 0); err != nil {
			t.Errorf("restore initial process dumpable flag: %v", err)
		}
	})

	if err := ProtectWorkerProcess(); err != nil {
		t.Fatalf("protect worker process: %v", err)
	}
	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("read process dumpable flag: %v", err)
	}
	if dumpable != 0 {
		t.Fatalf("process dumpable flag = %d, want 0", dumpable)
	}
}

func TestControllerSocketDirectoryRejectsWorkerWritableFilesystemRoot(t *testing.T) {
	if dir, err := createControllerWorkerSocketDir(string(filepath.Separator), "", ""); err == nil {
		t.Fatalf("socket directory %q must not be placed under a filesystem-wide writable bind", dir)
	} else if !strings.Contains(err.Error(), "no private socket directory") {
		t.Fatalf("expected fail-closed socket placement error, got %v", err)
	}
}

func TestBubblewrapRejectsUnsafeWorkerCapabilityAndAuthorityPaths(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(t *testing.T, target string)
		env      func(target string) string
		wantText string
	}{
		{
			name: "worker API socket inside writable target",
			env: func(target string) string {
				return workerAPISocketEnv + "=" + filepath.Join(target, "worker-api.sock")
			},
			wantText: "outside worker-writable bind",
		},
		{
			name: "candidate metadata authority is a symlink",
			setup: func(t *testing.T, target string) {
				symlinkStateDirectory(t, target, "candidates")
			},
			wantText: "prepare candidate metadata mount",
		},
		{
			name: "candidate evidence authority is a symlink",
			setup: func(t *testing.T, target string) {
				symlinkStateDirectory(t, target, "evidence")
			},
			wantText: "prepare candidate evidence mount",
		},
		{
			name: "attempt manifest authority is a symlink",
			setup: func(t *testing.T, target string) {
				symlinkStateDirectory(t, target, "attempt-manifests")
			},
			wantText: "prepare attempt manifest authority mount",
		},
		{
			name: "event authority is a symlink",
			setup: func(t *testing.T, target string) {
				symlinkStateDirectory(t, target, "events")
			},
			wantText: "prepare controller event authority mount",
		},
		{
			name: "delivery authority is a symlink",
			setup: func(t *testing.T, target string) {
				symlinkStateDirectory(t, target, "delivery")
			},
			wantText: "prepare terminal delivery mount",
		},
		{
			name: "usage authority is a symlink",
			setup: func(t *testing.T, target string) {
				symlinkStateDirectory(t, target, "usage")
			},
			wantText: "prepare usage envelope mount",
		},
		{
			name: "business brief authority is a symlink",
			setup: func(t *testing.T, target string) {
				symlinkStateDirectory(t, target, "briefs")
			},
			wantText: "prepare controller business brief mount",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeBin := t.TempDir()
			if err := os.WriteFile(filepath.Join(fakeBin, "bwrap"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeBin)
			target := makeBubblewrapTarget(t)
			environment := []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
			if tc.setup != nil {
				tc.setup(t, target)
			}
			if tc.env != nil {
				environment = append(environment, tc.env(target))
			}
			_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
				filepath.Join(target, "controller.db"), "sandbox-test", nil, environment)
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("unsafe worker path must fail closed with %q, got %v", tc.wantText, err)
			}
		})
	}
}

func TestBubblewrapRejectsDirectoryControllerEvidenceLeafs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pathParts []string
		wantText  string
	}{
		{
			name:      "candidate evidence leaf",
			pathParts: []string{"evidence", "sandbox-test", "review-candidate.json"},
			wantText:  "controller candidate evidence path",
		},
		{
			name:      "delivery record leaf",
			pathParts: []string{"delivery", "sandbox-test.json"},
			wantText:  "delivery record path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeBin := t.TempDir()
			if err := os.WriteFile(filepath.Join(fakeBin, "bwrap"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeBin)
			target := makeBubblewrapTarget(t)
			leaf := filepath.Join(append([]string{target, ".ai-team", "state"}, tc.pathParts...)...)
			if err := os.MkdirAll(filepath.Dir(leaf), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(leaf, 0700); err != nil {
				t.Fatal(err)
			}
			_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
				filepath.Join(target, "controller.db"), "sandbox-test", nil,
				[]string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), tc.wantText) || !strings.Contains(err.Error(), "regular file") {
				t.Fatalf("directory at a controller file path must fail closed with %q, got %v", tc.wantText, err)
			}
		})
	}
}

func symlinkStateDirectory(t *testing.T, target, name string) {
	t.Helper()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, ".ai-team", "state", name)); err != nil {
		t.Fatal(err)
	}
}
