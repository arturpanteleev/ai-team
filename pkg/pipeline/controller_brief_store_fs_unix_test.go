//go:build linux || darwin

package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadBriefFileAtRejectsUnsafeFilesAndEnforcesLimit(t *testing.T) {
	root := t.TempDir()
	directoryFD, err := openBriefDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeBriefFD(directoryFD); err != nil {
			t.Errorf("close brief test directory: %v", err)
		}
	})

	writeReadOnly := func(name string, content []byte) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o444); err != nil {
			t.Fatal(err)
		}
		return path
	}

	validPath := writeReadOnly("valid.md", []byte("brief"))
	if got, err := readBriefFileAt(directoryFD, filepath.Base(validPath), 5); err != nil || string(got) != "brief" {
		t.Fatalf("read immutable brief got=%q err=%v", got, err)
	}
	if _, err := readBriefFileAt(directoryFD, filepath.Base(validPath), 4); err == nil {
		t.Fatal("read accepted a brief larger than the caller's limit")
	}

	mutablePath := filepath.Join(root, "mutable.md")
	if err := os.WriteFile(mutablePath, []byte("mutable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBriefFileAt(directoryFD, filepath.Base(mutablePath), 64); err == nil {
		t.Fatal("read accepted a writable brief file")
	}

	hardlinkPath := writeReadOnly("hardlinked.md", []byte("linked"))
	if err := os.Link(hardlinkPath, filepath.Join(root, "hardlink-alias.md")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := readBriefFileAt(directoryFD, filepath.Base(hardlinkPath), 64); err == nil {
		t.Fatal("read accepted a brief file with multiple hard links")
	}

	if err := os.Symlink(validPath, filepath.Join(root, "linked.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readBriefFileAt(directoryFD, "linked.md", 64); err == nil {
		t.Fatal("read followed a symlinked brief file")
	}

	if err := os.Mkdir(filepath.Join(root, "directory.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readBriefFileAt(directoryFD, "directory.md", 64); err == nil {
		t.Fatal("read accepted a directory as a brief file")
	}
	if _, err := readBriefFileAt(directoryFD, "missing.md", 64); err == nil {
		t.Fatal("read accepted a missing brief file")
	}
}
