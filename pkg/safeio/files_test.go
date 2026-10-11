package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceRegularFileNoFollowReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "result.md")
	if err := os.WriteFile(target, []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceRegularFileNoFollow(target, []byte("edited"), 0o640); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "edited" {
		t.Fatalf("replacement content=%q, err=%v", got, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("replacement mode=%v, err=%v", info.Mode(), err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "result.md" {
		t.Fatalf("temporary replacement file left behind: entries=%v err=%v", entries, err)
	}
}

func TestReplaceRegularFileNoFollowCreatesMissingFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "nested", "result.md")
	if err := ReplaceRegularFileNoFollow(target, []byte("first"), 0o600); err != nil {
		t.Fatalf("create missing target: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "first" {
		t.Fatalf("created content=%q, err=%v", got, err)
	}
}

func TestReplaceRegularFileNoFollowRejectsSymlinkAndDirectory(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(sentinel, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := ReplaceRegularFileNoFollow(link, []byte("changed"), 0o600); err == nil {
		t.Fatal("leaf symlink must be rejected")
	}
	if err := ReplaceRegularFileNoFollow(root, []byte("changed"), 0o600); err == nil {
		t.Fatal("directory target must be rejected")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil || string(got) != "keep" {
		t.Fatalf("sentinel changed: content=%q err=%v", got, err)
	}
}

func TestReplaceRegularFileNoFollowRejectsParentSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := ReplaceRegularFileNoFollow(filepath.Join(link, "result.md"), []byte("changed"), 0o600); err == nil {
		t.Fatal("parent symlink must be rejected")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("replacement escaped through parent symlink: entries=%v err=%v", entries, err)
	}
}
