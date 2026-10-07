package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeBriefDocumentRejectsInvalidControllerData(t *testing.T) {
	validContent := []byte("# intention\n")
	digestVersion, _, err := writeInitialBrief(t.TempDir(), "source-run", "intention")
	if err != nil {
		t.Fatal(err)
	}
	digestVersion.Path = "brief/0001-intention.md"

	cases := []struct {
		name      string
		version   BriefVersion
		content   []byte
		workspace string
	}{
		{name: "path traversal", version: BriefVersion{Path: "../outside.md"}, content: validContent, workspace: t.TempDir()},
		{name: "wrong root", version: BriefVersion{Path: "other/0001-intention.md"}, content: validContent, workspace: t.TempDir()},
		{name: "wrong extension", version: BriefVersion{Path: "brief/0001-intention.txt"}, content: validContent, workspace: t.TempDir()},
		{name: "empty content", version: digestVersion, workspace: t.TempDir()},
		{name: "oversized content", version: digestVersion, content: make([]byte, maxBriefBytes+1), workspace: t.TempDir()},
		{name: "identity mismatch", version: func() BriefVersion { v := digestVersion; v.ID = "brief-wrong"; return v }(), content: validContent, workspace: t.TempDir()},
		{name: "unwritable workspace", version: digestVersion, content: validContent, workspace: func() string {
			p := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := materializeBriefDocument(tc.workspace, BriefDocument{Version: tc.version, Content: tc.content}); err == nil {
				t.Fatal("invalid controller brief data was accepted")
			}
		})
	}
}

func TestFileBriefStoreRejectsInvalidAndConflictingBriefs(t *testing.T) {
	target := t.TempDir()
	store := NewFileBriefStore(target)
	for _, runID := range []string{"", "..", "../escape"} {
		if _, err := store.CreateInitial(runID, "intention"); err == nil {
			t.Fatalf("CreateInitial accepted unsafe run id %q", runID)
		}
		if _, err := store.List(runID); err == nil {
			t.Fatalf("List accepted unsafe run id %q", runID)
		}
		if _, err := store.Read(runID, "brief-id"); err == nil {
			t.Fatalf("Read accepted unsafe run id %q", runID)
		}
	}
	for _, intention := range []string{"", " \n\t ", strings.Repeat("x", maxBriefBytes+1)} {
		if _, err := store.CreateInitial("run-invalid-brief", intention); err == nil {
			t.Fatal("CreateInitial accepted empty or oversized intention")
		}
	}
	if _, err := store.CreateInitial("run-conflict", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateInitial("run-conflict", "different immutable intention"); err == nil {
		t.Fatal("CreateInitial replaced an immutable initial brief")
	}
}

func TestFileBriefStoreAppendClarificationRejectsMissingAndOversizedHistory(t *testing.T) {
	target := t.TempDir()
	store := NewFileBriefStore(target)
	if _, err := store.AppendClarification("missing-run", "approval", "question", "answer"); err == nil {
		t.Fatal("clarification appended before an initial brief existed")
	}
	if _, err := store.CreateInitial("run-answer-limits", "intention"); err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{"", " \n ", strings.Repeat("a", maxAnswerBytes+1)} {
		if _, err := store.AppendClarification("run-answer-limits", "approval", "question", answer); err == nil {
			t.Fatal("AppendClarification accepted an empty or oversized answer")
		}
	}
	if _, err := store.Read("run-answer-limits", "brief-not-found"); !os.IsNotExist(err) {
		t.Fatalf("unknown brief version error=%v, want not-exist", err)
	}

	// Leave enough room for the initial file, but force the appended snapshot
	// over the immutable per-version limit.
	large := strings.Repeat("x", maxBriefBytes-80)
	if _, err := store.CreateInitial("run-history-limit", large); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendClarification("run-history-limit", "approval", "question", "answer"); err == nil {
		t.Fatal("AppendClarification exceeded the maximum version size")
	}
}

func TestFileBriefStoreRejectsSymlinkedRunDirectory(t *testing.T) {
	target, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team", "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, ".ai-team", "runs", "run-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewFileBriefStore(target).CreateInitial("run-link", "intention"); err == nil {
		t.Fatal("brief store followed a symlinked run directory")
	}
}
