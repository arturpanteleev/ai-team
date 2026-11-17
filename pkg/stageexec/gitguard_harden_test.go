//go:build !windows

package stageexec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// The original bypass: WalkDir on a symlinked root does not descend, so a
// nested .git planted in the target was never checked even though the daemon
// bind-mounts the target. The root is now rejected before any walk.
func TestCheckRepoGitMetadataRejectsSymlinkedRoot(t *testing.T) {
	target := t.TempDir()
	writeTree(t, target, map[string]string{"sub/keep": "x"})
	if err := os.Symlink(t.TempDir(), filepath.Join(target, "sub", ".git")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "workdir")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := checkRepoGitMetadata(link); err == nil {
		t.Fatal("a symlinked WorkDir with a planted nested .git was accepted")
	}
}

// A symlink in an ancestor directory is resolved before the walk: the guard
// must inspect the real tree, not stop at the link.
func TestCheckRepoGitMetadataWalksThroughAncestorSymlink(t *testing.T) {
	real := t.TempDir()
	repo := filepath.Join(real, "repo")
	writeTree(t, repo, map[string]string{"sub/keep": "x"})
	if err := os.Symlink(t.TempDir(), filepath.Join(repo, "sub", ".git")); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if err := os.Symlink(real, filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}
	if err := checkRepoGitMetadata(filepath.Join(parent, "link", "repo")); err == nil {
		t.Fatal("nested .git behind an ancestor symlink was not checked")
	}
}

func TestCheckRepoGitMetadataRejectsGitShortNames(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"short-name symlink": {"sub/keep": "x"},
		"short-name gitfile": {"sub/GIT~2": "garbage"},
		"mixed-case alias":   {"sub/Git~10": "garbage"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, files)
			if name == "short-name symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "sub", "GIT~1")); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkRepoGitMetadata(root); err == nil {
				t.Fatal("a GIT~1-style alias was accepted")
			}
		})
	}
}

func TestCheckRepoGitMetadataAcceptsLegitimatePath(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".git/HEAD": "ref: x", "src/main.go": "package main"})
	if err := checkRepoGitMetadata(root); err != nil {
		t.Fatalf("legitimate repository rejected: %v", err)
	}
}

// The check and the launch are two moments: pin the resolved root, run the
// guard, then re-verify immediately before starting the container.
func TestRepoRootPinDetectsSwapAfterTheCheck(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workdir")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{".git/HEAD": "ref: x"})
	pin, err := safeio.PinRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkRepoGitMetadata(pin.Real()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Fatal(err)
	}
	if err := pin.Verify(); err == nil {
		t.Fatal("a root swapped after the check was accepted")
	}
}

func TestHasGitElementRecognisesShortNames(t *testing.T) {
	for path, want := range map[string]bool{"GIT~1/config": true, "a/.git~2/hooks": true, "a/Git~9": true, "a/github~1": false, "a/src": false} {
		if got := hasGitElement(path); got != want {
			t.Errorf("hasGitElement(%q) = %v, want %v", path, got, want)
		}
	}
}
