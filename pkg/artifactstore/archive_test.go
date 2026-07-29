package artifactstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalCASDetectsCorruption(t *testing.T) {
	store, err := NewLocalCAS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blob, err := store.Put(bytes.NewBufferString("immutable"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.blobPath(blob.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupted"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteTo(blob.SHA256, bytes.NewBuffer(nil)); err == nil {
		t.Fatal("corrupted CAS blob должен быть отклонён")
	}
}

func TestRunArchiveRestoreExactFiles(t *testing.T) {
	target := t.TempDir()
	runID := "run-archive"
	runRoot := filepath.Join(target, "runs")
	source := filepath.Join(runRoot, runID)
	if err := os.MkdirAll(filepath.Join(source, "attempts", "one"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "events.jsonl"), []byte("event\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "attempts", "one", "manifest.json"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewLocalCAS(filepath.Join(target, "cas"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := NewRunArchive(runRoot, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Archive(runID); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(target, "restored")
	if err := archive.Restore(runID, destination); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"events.jsonl", filepath.Join("attempts", "one", "manifest.json")} {
		expected, _ := os.ReadFile(filepath.Join(source, relative))
		actual, err := os.ReadFile(filepath.Join(destination, relative))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("restore %s: actual=%q expected=%q err=%v", relative, actual, expected, err)
		}
	}
}

func TestRunArchiveRejectsSymlink(t *testing.T) {
	target := t.TempDir()
	runID := "run-symlink"
	source := filepath.Join(target, "runs", runID)
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "outside"), filepath.Join(source, "unsafe")); err != nil {
		t.Skipf("symlink недоступен: %v", err)
	}
	store, _ := NewLocalCAS(filepath.Join(target, "cas"))
	archive, _ := NewRunArchive(filepath.Join(target, "runs"), store)
	if err := archive.Archive(runID); err == nil {
		t.Fatal("symlink в immutable run должен быть отклонён")
	}
}
