package evidence

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactDigestAtAndCopyArtifactAtSupportNestedDirectories(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "attempts", "a-001", "artifacts", "nested", "deeper")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload.json"), []byte(`{"ok":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(source), "root.txt"), []byte("root\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rel := filepath.ToSlash(filepath.Join("attempts", "a-001", "artifacts", "nested"))
	wantType, wantSize, wantDigest, err := ArtifactDigest(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	gotType, gotSize, gotDigest, err := ArtifactDigestAt(root, rel)
	if err != nil {
		t.Fatalf("ArtifactDigestAt: %v", err)
	}
	if gotType != wantType || gotSize != wantSize || gotDigest != wantDigest {
		t.Fatalf("ArtifactDigestAt = (%s, %d, %s), want (%s, %d, %s)", gotType, gotSize, gotDigest, wantType, wantSize, wantDigest)
	}
	destination := filepath.Join(t.TempDir(), "copied")
	copyType, copySize, copyDigest, err := CopyArtifactAt(root, rel, destination)
	if err != nil {
		t.Fatalf("CopyArtifactAt: %v", err)
	}
	if copyType != wantType || copySize != wantSize || copyDigest != wantDigest {
		t.Fatalf("CopyArtifactAt = (%s, %d, %s), want (%s, %d, %s)", copyType, copySize, copyDigest, wantType, wantSize, wantDigest)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "deeper", "payload.json")); err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("copied nested file: data=%q err=%v", data, err)
	}
}

func TestArtifactDigestAtRejectsSymlinkParent(t *testing.T) {
	root := t.TempDir()
	attemptDir := filepath.Join(root, "attempts", "a-001")
	if err := os.MkdirAll(attemptDir, 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "payload"), []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(attemptDir, "artifacts")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ArtifactDigestAt(root, "attempts/a-001/artifacts/payload"); err == nil {
		t.Fatal("ArtifactDigestAt followed a symlink parent")
	}
}
