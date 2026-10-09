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

func TestAppendSubmissionVersionsByStageAndApprovalIsIdempotent(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AppendSubmission("run-1", "product_spec", "approval-1", "md", "", "# v1\n", "first", "", "owner")
	if err != nil || first.Revision != 1 || first.SHA256 != Digest([]byte("# v1\n")) {
		t.Fatalf("first submission: %+v err=%v", first, err)
	}
	retry, err := store.AppendSubmission("run-1", "product_spec", "approval-1", "md", "", "# v1\n", "first", "", "owner")
	if err != nil || retry.ID != first.ID || retry.Revision != first.Revision {
		t.Fatalf("exact retry must return the original immutable version: %+v err=%v", retry, err)
	}
	if _, err := store.AppendSubmission("run-1", "product_spec", "approval-1", "md", "", "# changed\n", "first", "", "owner"); err == nil {
		t.Fatal("changed retry for the same approval must conflict")
	}
	second, err := store.AppendSubmission("run-1", "product_spec", "approval-2", "md", "", "# v2\n", "second", "", "owner")
	if err != nil || second.Revision != 2 || second.BaseRevision != first.ID {
		t.Fatalf("next stage result should append a version: %+v err=%v", second, err)
	}
	history, err := store.List("run-1", "stages/product_spec/result.md")
	if err != nil || len(history) != 2 || history[0].Content != "# v1\n" || history[1].Content != "# v2\n" {
		t.Fatalf("stage history lost a version: %+v err=%v", history, err)
	}
}

func TestConcurrentSubmissionsDoNotOverwrite(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan Revision, 2)
	errorsFound := make(chan error, 2)
	for _, value := range []struct{ approval, content string }{{"approval-a", "# A\n"}, {"approval-b", "# B\n"}} {
		wg.Add(1)
		go func(approvalID, content string) {
			defer wg.Done()
			revision, appendErr := store.AppendSubmission("run-1", "spec", approvalID, "md", "", content, "", "", "owner")
			if appendErr != nil {
				errorsFound <- appendErr
				return
			}
			results <- revision
		}(value.approval, value.content)
	}
	wg.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	versions := make([]Revision, 0, 2)
	for value := range results {
		versions = append(versions, value)
	}
	if len(versions) != 2 || versions[0].Revision == versions[1].Revision || versions[0].SHA256 == versions[1].SHA256 {
		t.Fatalf("concurrent submissions must persist distinct versions: %+v", versions)
	}
	history, err := store.List("run-1", "stages/spec/result.md")
	if err != nil || len(history) != 2 || history[0].Content == history[1].Content {
		t.Fatalf("concurrent history was overwritten: %+v err=%v", history, err)
	}
}

func TestValidateSubmissionEnforcesMarkdownSizeAndLinkContract(t *testing.T) {
	if err := ValidateSubmission("md", "", "# spec\n"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSubmission("md", "", string(make([]byte, MaxContentBytes+1))); err == nil {
		t.Fatal("oversized markdown accepted")
	}
	for _, value := range []struct {
		kind, url string
		valid     bool
	}{
		{"pr", "https://example.test/pull/1", true},
		{"build", "http://ci.example.test/job/9", true},
		{"other", "https://example.test/docs", true},
		{"unknown", "https://example.test/", false},
		{"pr", "file:///tmp/pull", false},
		{"pr", "https://", false},
	} {
		err := ValidateSubmission("link", value.kind, value.url)
		if (err == nil) != value.valid {
			t.Fatalf("ValidateSubmission(link,%q,%q) err=%v valid=%v", value.kind, value.url, err, value.valid)
		}
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
