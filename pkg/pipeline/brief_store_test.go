package pipeline

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMaterializeBriefDocumentRejectsInvalidControllerData(t *testing.T) {
	validContent := []byte("# intention\n")
	initial, err := NewFileBriefStore(t.TempDir()).CreateInitial("source-run", "intention")
	if err != nil {
		t.Fatal(err)
	}
	digestVersion := initial.Version

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

func TestWithBusinessBriefStoreInjectsStoreThroughNew(t *testing.T) {
	store := NewFileBriefStore(t.TempDir())
	p := New(nil, nil, WithBusinessBriefStore(store))
	if p.briefs != store {
		t.Fatalf("pipeline business brief store=%T, want injected %T", p.briefs, store)
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

func TestReadBriefTreeRejectsHardlinkedFiles(t *testing.T) {
	target, aliasDir := t.TempDir(), t.TempDir()
	const runID = "hardlinked-brief-run"
	created, err := NewFileBriefStore(target).CreateInitial(runID, "linked intention")
	if err != nil {
		t.Fatal(err)
	}
	briefPath := filepath.Join(target, ".ai-team", "runs", runID, filepath.FromSlash(created.Version.Path))
	alias := filepath.Join(aliasDir, "brief-copy.md")
	if err := os.Link(briefPath, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := readBriefTree(filepath.Dir(briefPath)); err == nil {
		t.Fatal("brief tree containing a hard-linked file was accepted")
	}
}

func TestControllerBriefStoreMigratesLegacyVersionsWithoutChangingIdentity(t *testing.T) {
	target := t.TempDir()
	const runID = "legacy-brief-run"
	legacy := NewFileBriefStore(target)
	initial, err := legacy.CreateInitial(runID, "legacy intention")
	if err != nil {
		t.Fatal(err)
	}
	clarified, err := legacy.AppendClarification(runID, "approval-legacy", "question", "answer")
	if err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(target, ".ai-team", "runs", runID, "brief")
	initialBytes, err := os.ReadFile(filepath.Join(legacyDir, "0001-intention.md"))
	if err != nil {
		t.Fatal(err)
	}
	initialMetadata, err := os.ReadFile(filepath.Join(legacyDir, "0001-intention.json"))
	if err != nil {
		t.Fatal(err)
	}
	clarifiedName := filepath.Base(clarified.Version.Path)
	clarifiedBytes, err := os.ReadFile(filepath.Join(legacyDir, clarifiedName))
	if err != nil {
		t.Fatal(err)
	}

	controller := NewControllerBriefStore(target)
	t.Cleanup(func() { _ = controller.Close() })
	if err := controller.PrepareRun(runID); err != nil {
		t.Fatal(err)
	}
	if err := controller.PrepareRun(runID); err != nil {
		t.Fatalf("repeated legacy recovery was not idempotent: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".ai-team", "runs", runID)); !os.IsNotExist(err) {
		t.Fatalf("legacy run directory was not removed after moving its only brief: err=%v", err)
	}
	versions, err := controller.List(runID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("migrated brief versions=%+v err=%v", versions, err)
	}
	if versions[0].ID != initial.Version.ID || versions[0].SHA256 != initial.Version.SHA256 ||
		versions[1].ID != clarified.Version.ID || versions[1].SHA256 != clarified.Version.SHA256 ||
		versions[1].ParentID != initial.Version.ID || versions[1].ApprovalID != "approval-legacy" {
		t.Fatalf("migration changed brief version identities or relationships: %+v", versions)
	}
	canonicalDir := filepath.Join(target, ".ai-team", "state", "briefs", runID)
	gotInitial, err := os.ReadFile(filepath.Join(canonicalDir, "0001-intention.md"))
	if err != nil || string(gotInitial) != string(initialBytes) {
		t.Fatalf("initial brief content changed during migration: %q err=%v", gotInitial, err)
	}
	gotMetadata, err := os.ReadFile(filepath.Join(canonicalDir, "0001-intention.json"))
	if err != nil || string(gotMetadata) != string(initialMetadata) {
		t.Fatalf("initial brief hash metadata changed during migration: %q err=%v", gotMetadata, err)
	}
	gotClarified, err := os.ReadFile(filepath.Join(canonicalDir, clarifiedName))
	if err != nil || string(gotClarified) != string(clarifiedBytes) {
		t.Fatalf("clarified brief content changed during migration: %q err=%v", gotClarified, err)
	}
	loaded, err := controller.Read(runID, clarified.Version.ID)
	if err != nil || string(loaded.Content) != string(clarified.Content) || loaded.Version.SHA256 != clarified.Version.SHA256 {
		t.Fatalf("controller read did not preserve the migrated version: document=%+v err=%v", loaded, err)
	}
}

func TestControllerBriefStoreFailsClosedOnLegacyConflictAndSymlink(t *testing.T) {
	t.Run("conflict", func(t *testing.T) {
		target := t.TempDir()
		const runID = "brief-conflict"
		controller := NewControllerBriefStore(target)
		t.Cleanup(func() { _ = controller.Close() })
		if err := controller.PrepareRun(runID); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.CreateInitial(runID, "canonical intention"); err != nil {
			t.Fatal(err)
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileBriefStore(target).CreateInitial(runID, "different legacy intention"); err != nil {
			t.Fatal(err)
		}
		controller = NewControllerBriefStore(target)
		t.Cleanup(func() { _ = controller.Close() })
		if err := controller.PrepareRun(runID); err == nil {
			t.Fatal("conflicting canonical and legacy brief data was accepted")
		}
		for _, path := range []string{
			filepath.Join(target, ".ai-team", "state", "briefs", runID, "0001-intention.md"),
			filepath.Join(target, ".ai-team", "runs", runID, "brief", "0001-intention.md"),
		} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("conflicting source data was lost at %q: %v", path, err)
			}
		}
	})

	t.Run("symlinked brief directory", func(t *testing.T) {
		target, outside := t.TempDir(), t.TempDir()
		runRoot := filepath.Join(target, ".ai-team", "runs", "brief-symlink")
		if err := os.MkdirAll(runRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(runRoot, "brief")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		controller := NewControllerBriefStore(target)
		t.Cleanup(func() { _ = controller.Close() })
		if err := controller.PrepareRun("brief-symlink"); err == nil {
			t.Fatal("controller brief migration followed a symlinked legacy directory")
		}
	})
}

func TestEmptyLegacyBriefMountIsRemovedAndLocalStoreKeepsItsLayout(t *testing.T) {
	target := t.TempDir()
	const runID = "empty-legacy-brief"
	legacyBrief := filepath.Join(target, ".ai-team", "runs", runID, "brief")
	if err := os.MkdirAll(legacyBrief, 0o700); err != nil {
		t.Fatal(err)
	}
	controller := NewControllerBriefStore(target)
	t.Cleanup(func() { _ = controller.Close() })
	if err := controller.PrepareRun(runID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".ai-team", "runs", runID)); !os.IsNotExist(err) {
		t.Fatalf("empty legacy mountpoint still blocks a fresh evidence Start: err=%v", err)
	}
	if _, err := NewFileBriefStore(target).CreateInitial("local-brief-run", "local intention"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, ".ai-team", "runs", "local-brief-run", "brief", "0001-intention.md")); err != nil {
		t.Fatalf("local FileBriefStore layout changed: %v", err)
	}
}

func TestControllerBriefStoreReplacesEmptyCanonicalMountpointAndRemovesIdenticalLegacyTree(t *testing.T) {
	t.Run("empty canonical mountpoint", func(t *testing.T) {
		target := t.TempDir()
		const runID = "empty-canonical-brief"
		legacy, err := NewFileBriefStore(target).CreateInitial(runID, "legacy intention")
		if err != nil {
			t.Fatal(err)
		}
		canonicalDir := filepath.Join(target, ".ai-team", "state", "briefs", runID)
		if err := os.MkdirAll(canonicalDir, 0o700); err != nil {
			t.Fatal(err)
		}
		store := NewControllerBriefStore(target)
		t.Cleanup(func() { _ = store.Close() })
		if err := store.PrepareRun(runID); err != nil {
			t.Fatal(err)
		}
		got, err := store.Read(runID, legacy.Version.ID)
		if err != nil || !bytes.Equal(got.Content, legacy.Content) {
			t.Fatalf("legacy brief was not migrated after removing empty mountpoint: document=%+v err=%v", got, err)
		}
		if _, err := os.Lstat(filepath.Join(target, ".ai-team", "runs", runID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy run root remains after migration: err=%v", err)
		}
	})

	t.Run("identical legacy tree", func(t *testing.T) {
		target := t.TempDir()
		const runID = "identical-legacy-brief"
		controller := NewControllerBriefStore(target)
		if err := controller.PrepareRun(runID); err != nil {
			t.Fatal(err)
		}
		canonical, err := controller.CreateInitial(runID, "same intention")
		if err != nil {
			t.Fatal(err)
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		legacy, err := NewFileBriefStore(target).CreateInitial(runID, "same intention")
		if err != nil {
			t.Fatal(err)
		}
		if legacy.Version.ID != canonical.Version.ID {
			t.Fatalf("fixture versions differ: canonical=%+v legacy=%+v", canonical.Version, legacy.Version)
		}
		store := NewControllerBriefStore(target)
		t.Cleanup(func() { _ = store.Close() })
		if err := store.PrepareRun(runID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(target, ".ai-team", "runs", runID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("identical legacy files were not removed: err=%v", err)
		}
		got, err := store.Read(runID, canonical.Version.ID)
		if err != nil || !bytes.Equal(got.Content, canonical.Content) {
			t.Fatalf("canonical brief changed while identical legacy data was removed: document=%+v err=%v", got, err)
		}
	})
}

func TestControllerBriefStoreCleansLegacySubsetAfterInterruptedCleanup(t *testing.T) {
	target := t.TempDir()
	const runID = "partial-legacy-cleanup"
	controller := NewControllerBriefStore(target)
	if err := controller.PrepareRun(runID); err != nil {
		t.Fatal(err)
	}
	initial, err := controller.CreateInitial(runID, "same intention")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.AppendClarification(runID, "approval-one", "question", "answer"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	legacy, err := NewFileBriefStore(target).CreateInitial(runID, "same intention")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Version.ID != initial.Version.ID {
		t.Fatalf("fixture legacy version differs from canonical initial version: legacy=%+v canonical=%+v", legacy.Version, initial.Version)
	}

	reopened := NewControllerBriefStore(target)
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.PrepareRun(runID); err != nil {
		t.Fatalf("prepare after an interrupted duplicate cleanup: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".ai-team", "runs", runID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy subset remained after cleanup: err=%v", err)
	}
	versions, err := reopened.List(runID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("canonical brief changed after legacy subset cleanup: versions=%+v err=%v", versions, err)
	}
}

func TestControllerBriefStoreCRUDStaysOnPinnedDirectoryAfterAncestorReplacement(t *testing.T) {
	target := t.TempDir()
	const runID = "pinned-brief-run"
	store := NewControllerBriefStore(target)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.PrepareRun(runID); err != nil {
		t.Fatal(err)
	}
	initial, err := store.CreateInitial(runID, "pinned intention")
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(target, ".ai-team", "state")
	stateBefore, err := os.Stat(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	movedState := filepath.Join(target, ".ai-team", "state-pinned-probe")
	if err := os.Rename(stateRoot, movedState); err != nil {
		t.Fatalf("replace store ancestor: %v", err)
	}
	if err := store.PrepareRun(runID); err != nil {
		t.Fatalf("repeated preflight reopened the replaced ancestor: %v", err)
	}
	restored := false
	defer func() {
		if restored {
			return
		}
		_ = os.RemoveAll(stateRoot)
		_ = os.Rename(movedState, stateRoot)
	}()
	redirectedRun := filepath.Join(stateRoot, "briefs", runID)
	if err := os.MkdirAll(redirectedRun, 0o700); err != nil {
		t.Fatal(err)
	}
	redirectedSentinel := filepath.Join(redirectedRun, "redirected-sentinel")
	if err := os.WriteFile(redirectedSentinel, []byte("redirected"), 0o600); err != nil {
		t.Fatal(err)
	}
	clarified, err := store.AppendClarification(runID, "approval-pinned", "where?", "here")
	if err != nil {
		t.Fatal(err)
	}
	if clarified.Version.ParentID != initial.Version.ID {
		t.Fatalf("pinned append parent=%q, want %q", clarified.Version.ParentID, initial.Version.ID)
	}
	if _, err := os.Lstat(filepath.Join(redirectedRun, filepath.Base(clarified.Version.Path))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("brief operation wrote through the replaced ancestor: err=%v", err)
	}
	if err := os.RemoveAll(stateRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(movedState, stateRoot); err != nil {
		t.Fatal(err)
	}
	restored = true
	stateAfter, err := os.Stat(stateRoot)
	if err != nil || !os.SameFile(stateBefore, stateAfter) {
		t.Fatalf("canonical state inode changed after restore: err=%v", err)
	}
	loaded, err := store.Read(runID, clarified.Version.ID)
	if err != nil || !bytes.Equal(loaded.Content, clarified.Content) {
		t.Fatalf("brief API did not use the pinned canonical inode: document=%+v err=%v", loaded, err)
	}
	versions, err := store.List(runID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("pinned list returned %+v err=%v", versions, err)
	}
}

func TestControllerBriefStoreRequiresPrepareRunBeforeCRUD(t *testing.T) {
	target := t.TempDir()
	store := NewControllerBriefStore(target)
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateInitial("unprepared-run", "intention"); err == nil || !strings.Contains(err.Error(), "prepared before CRUD") {
		t.Fatalf("unprepared CRUD returned %v, want explicit pre-spawn prepare error", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".ai-team", "runs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unprepared CRUD created worker-visible run state: err=%v", err)
	}
}

func TestControllerBriefStoreCanCloseBeforePrepare(t *testing.T) {
	store := NewControllerBriefStore(t.TempDir())
	if err := store.Close(); err != nil {
		t.Fatalf("closing an unprepared store failed: %v", err)
	}
	if err := store.PrepareRun("closed-before-prepare"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("PrepareRun after Close returned %v, want closed-store error", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close after an unprepared close was not idempotent: %v", err)
	}
}

func TestControllerBriefStoreSerializesCRUDWithClose(t *testing.T) {
	target := t.TempDir()
	const runID = "brief-close-run"
	store := NewControllerBriefStore(target)
	if err := store.PrepareRun(runID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateInitial(runID, "close test"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for j := 0; j < 8; j++ {
				_, err := store.List(runID)
				if err != nil && !strings.Contains(err.Error(), "closed") {
					t.Errorf("List during Close failed unexpectedly: %v", err)
					return
				}
			}
		}()
	}
	close(start)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if err := store.Close(); err != nil {
		t.Fatalf("Close is not idempotent: %v", err)
	}
	if _, err := store.List(runID); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("CRUD after Close returned %v, want closed-store error", err)
	}
}
