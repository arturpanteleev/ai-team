package humanartifact

import (
	"sync"
	"testing"
)

func TestAppendIsVersionedAndCompetingWritesConflict(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Append("run-1", "attempts/a/artifacts/spec.md", "", "source-hash", "# v1\n", "initial edit", "product")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.BaseSHA256 != "source-hash" {
		t.Fatalf("first version: %+v", first)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, content := range []string{"# v2-a\n", "# v2-b\n"} {
		wg.Add(1)
		go func(content string) {
			defer wg.Done()
			_, appendErr := store.Append("run-1", "attempts/a/artifacts/spec.md", first.ID, first.SHA256, content, "review requested", "product")
			results <- appendErr
		}(content)
	}
	wg.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for result := range results {
		if result == nil {
			succeeded++
		} else if contains(result.Error(), "conflict") {
			conflicted++
		} else {
			t.Fatal(result)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("expected one success and one conflict; got %d/%d", succeeded, conflicted)
	}
	history, err := store.List("run-1", "attempts/a/artifacts/spec.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Content != "# v1\n" || history[1].Revision != 2 || history[1].BaseRevision != first.ID {
		t.Fatalf("history lost or overwritten: %+v", history)
	}
}

func TestAppendRejectsUnsafeArtifactAndMissingInitialContent(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("run-1", "../secret", "", "", "x", "", "actor"); err == nil {
		t.Fatal("traversal path accepted")
	}
	if _, err := store.Append("run-1", "brief/scope.md", "", "", "", "comment only", "actor"); err == nil {
		t.Fatal("comment-only first revision accepted without a base snapshot")
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
