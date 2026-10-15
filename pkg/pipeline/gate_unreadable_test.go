package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

func TestUnreadableOutputChangesVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.md")
	if err := os.WriteFile(path, make([]byte, maxArtifactFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	outputs := []runtime.Artifact{{Name: "big", Path: path}}
	before, _ := resultVersionOf(outputs)
	if err := os.WriteFile(path, make([]byte, maxArtifactFileBytes+2), 0o644); err != nil {
		t.Fatal(err)
	}
	after, _ := resultVersionOf(outputs)
	if before == after {
		t.Fatal("an oversized output that changed must change the version")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing, _ := resultVersionOf(outputs)
	if missing == after || missing == before {
		t.Fatal("a missing output must change the version")
	}
}
