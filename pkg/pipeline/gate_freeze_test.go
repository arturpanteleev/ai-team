package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

func TestResultVersionIsStableAndContentSensitive(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.md")
	second := filepath.Join(dir, "b.md")
	if err := os.WriteFile(first, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	outputs := []runtime.Artifact{{Name: "a", Path: first}, {Name: "b", Path: second}}
	before, err := resultVersionOf(outputs)
	if err != nil || before == "" {
		t.Fatalf("version = %q err %v", before, err)
	}
	reordered, _ := resultVersionOf([]runtime.Artifact{outputs[1], outputs[0]})
	if reordered != before {
		t.Fatal("the version must not depend on the order of outputs")
	}
	if err := os.WriteFile(second, []byte("two!"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, _ := resultVersionOf(outputs)
	if after == before {
		t.Fatal("a one-byte edit must give a new version")
	}
}

func TestResultFreezerDetectsAnEditDuringTheWave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.md")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	outputs := []runtime.Artifact{{Name: "result", Path: path}}
	sha, _ := resultVersionOf(outputs)
	freezer := resultFreezer{outputs: outputs}
	lease, err := freezer.Freeze(context.Background(), gatesched.WaveKey{ArtifactSHA256: sha})
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	defer lease.Release()
	if err := lease.Verify(); err != nil {
		t.Fatalf("unchanged result must verify: %v", err)
	}
	if err := os.WriteFile(path, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lease.Verify(); !errors.Is(err, gatesched.ErrResultChanged) {
		t.Fatalf("an edit during the wave must be detected, got %v", err)
	}
	// A wave started for a hash that is no longer current is refused outright.
	if _, err := freezer.Freeze(context.Background(), gatesched.WaveKey{ArtifactSHA256: sha}); !errors.Is(err, gatesched.ErrResultChanged) {
		t.Fatalf("freeze of a stale hash must fail, got %v", err)
	}
}
