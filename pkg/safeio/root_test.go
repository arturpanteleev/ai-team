//go:build !windows

package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinRootAcceptsRealDirectory(t *testing.T) {
	root := t.TempDir()
	pin, err := PinRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Real() != want {
		t.Fatalf("Real() = %q, want %q", pin.Real(), want)
	}
	if pin.Shown() == "" {
		t.Fatal("empty Shown()")
	}
	if err := pin.Verify(); err != nil {
		t.Fatalf("Verify on an unchanged root: %v", err)
	}
}

func TestPinRootRejectsSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "workdir")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := PinRoot(link); err == nil {
		t.Fatal("a symlinked root was accepted")
	}
}

func TestPinnedRootVerifyDetectsSwapToSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workdir")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	pin, err := PinRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := pin.Verify(); err != nil {
		t.Fatalf("Verify before the swap: %v", err)
	}
	// The attacker replaces the checked directory with a link to another tree
	// after the guard ran but before the process/container starts.
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Fatal(err)
	}
	if err := pin.Verify(); err == nil {
		t.Fatal("a symlink planted after the check was accepted")
	}
}

func TestPinnedRootVerifyDetectsSamePathReplacement(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workdir")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	pin, err := PinRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	// Allocate the replacement while the original still exists: some filesystems
	// reuse the inode when a directory is removed and immediately recreated at
	// the same path, which would make os.SameFile unable to tell them apart.
	replacement := root + ".replacement"
	if err := os.Mkdir(replacement, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, root); err != nil {
		t.Fatal(err)
	}
	if err := pin.Verify(); err == nil {
		t.Fatal("a different directory at the same path was accepted")
	}
}

func TestIsGitMetadataName(t *testing.T) {
	for name, want := range map[string]bool{
		".git": true, ".GIT": true, ".Git": true,
		"GIT~1": true, ".git~2": true, "git~12": true, "Git~9": true,
		"GIT~": false, "GIT~X": false, "abc~1": false,
		"GITHUB~1": false, ".github": false, ".gitignore": false, "b.git": false,
		"": false,
	} {
		if got := IsGitMetadataName(name); got != want {
			t.Errorf("IsGitMetadataName(%q) = %v, want %v", name, got, want)
		}
	}
}
