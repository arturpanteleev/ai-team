package evidence

import (
	"os"
	"path/filepath"
	"strings"
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

func TestArtifactDigestAtRejectsSymlinkParentForAttemptInputs(t *testing.T) {
	root := t.TempDir()
	inputRoot := filepath.Join(root, "attempts", "a-001", "inputs")
	if err := os.MkdirAll(inputRoot, 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "payload"), []byte("outside input"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(inputRoot, "001-source")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, _, _, err := ArtifactDigestAt(root, "attempts/a-001/inputs/001-source/payload"); err == nil {
		t.Fatal("ArtifactDigestAt followed a symlink parent for attempt inputs")
	}
}

func TestArtifactTraversalRejectsSymlinkSwapAfterInitialStat(t *testing.T) {
	root := t.TempDir()
	artifactDir := filepath.Join(root, "attempts", "a-001", "artifacts")
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(artifactDir, "payload.md")
	replacement := filepath.Join(artifactDir, "replacement.md")
	if err := os.WriteFile(payload, []byte("approved bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("replacement bytes"), 0644); err != nil {
		t.Fatal(err)
	}

	swapped := false
	_, _, _, err := artifactAtWithOpenHook(root, "attempts/a-001/artifacts", "", false, func(name string) {
		if name != "payload.md" || swapped {
			return
		}
		swapped = true
		if renameErr := os.Rename(payload, filepath.Join(artifactDir, "approved.md")); renameErr != nil {
			t.Fatalf("move original artifact out of the way: %v", renameErr)
		}
		if linkErr := os.Symlink("replacement.md", payload); linkErr != nil {
			t.Fatalf("replace artifact with in-root symlink: %v", linkErr)
		}
	})
	if !swapped {
		t.Fatal("test hook did not replace the artifact during traversal")
	}
	if err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("artifact traversal must reject a symlink swapped after lstat, got %v", err)
	}
}
