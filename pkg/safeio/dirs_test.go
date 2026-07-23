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

func TestRejectSymlink(t *testing.T) {
	root := t.TempDir()

	missing := filepath.Join(root, "does-not-exist")
	if err := RejectSymlink(missing); err != nil {
		t.Fatalf("missing path must be valid (nothing to reject yet), got: %v", err)
	}

	regular := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(regular, []byte("schema_version: 3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RejectSymlink(regular); err != nil {
		t.Fatalf("regular file must be accepted, got: %v", err)
	}

	link := filepath.Join(root, "config-link.yaml")
	if err := os.Symlink(regular, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := RejectSymlink(link); err == nil {
		t.Fatal("symlink must be rejected")
	}

	dir := filepath.Join(root, "somedir")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := RejectSymlink(dir); err == nil {
		t.Fatal("non-regular file (directory) must be rejected")
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
