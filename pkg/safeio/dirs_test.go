package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDirRejectsSymlinkComponent(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".ai-team")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := EnsureDir(root, ".ai-team", "runs"); err == nil {
		t.Fatal("symlink component must fail closed")
	}
}

func TestExistingDirDoesNotCreateAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if _, err := ExistingDir(root, ".ai-team"); err == nil {
		t.Fatal("missing control root must not be created")
	}
	if _, err := os.Stat(filepath.Join(root, ".ai-team")); !os.IsNotExist(err) {
		t.Fatalf("ExistingDir unexpectedly mutated target: %v", err)
	}
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(root, ".ai-team")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ExistingDir(root, ".ai-team"); err == nil {
		t.Fatal("symlink control root must fail")
	}
}

func TestValidateTreeRejectsNestedSymlink(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "agents")
	if err := os.MkdirAll(filepath.Join(tree, "coder"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(tree, "coder", "prompt.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := ValidateTree(tree); err == nil {
		t.Fatal("nested symlink must be rejected")
	}
}

func TestReadRegularFileRejectsSymlinkAndLimit(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("12345"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(file, 4); err == nil {
		t.Fatal("oversized file must fail")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ReadRegularFile(link, 10); err == nil {
		t.Fatal("symlink must fail")
	}
}
