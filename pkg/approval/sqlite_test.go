package approval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteStorePersistsQuorumAndRevisionBindingAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "sqlite-run", AttemptID: "attempt-1", FromStage: "reviewer", ToStage: "coder",
		Trigger: "stage_completed", SubjectHash: testSubject, Quorum: QuorumAll,
		RequiredRoles: []string{"qa", "security"}, Actions: []string{"approve", "return_to_coder"},
		FeedbackActions: []string{"return_to_coder"}, Targets: map[string]string{"approve": "coder", "return_to_coder": "coder"},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := map[string]string{"review.md": "rev-000002"}
	first, err := store.Decide(value.RunID, value.ID, Decision{ActorID: "qa-1", ActorRole: "qa", Action: "approve", SubjectHash: testSubject, ArtifactRevisions: selection})
	if err != nil || first.Status != StatusPending {
		t.Fatalf("first quorum vote: %+v, %v", first, err)
	}
	if _, err := store.Decide(value.RunID, value.ID, Decision{ActorID: "sec-1", ActorRole: "security", Action: "approve", SubjectHash: testSubject, ArtifactRevisions: map[string]string{"review.md": "rev-other"}}); err == nil {
		t.Fatal("different artifact revisions were accepted")
	}
	resolved, err := store.Decide(value.RunID, value.ID, Decision{ActorID: "sec-1", ActorRole: "security", Action: "approve", SubjectHash: testSubject, ArtifactRevisions: selection})
	if err != nil || resolved.Status != StatusResolved || resolved.ArtifactRevisionBindingSHA256 != revisionBindingHash(testSubject, selection) {
		t.Fatalf("resolved quorum: %+v, %v", resolved, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	reloaded, err := store.Load(value.RunID, value.ID)
	if err != nil || reloaded.Status != StatusResolved || !sameRevisionSelection(reloaded.ArtifactRevisions, selection) || len(reloaded.Decisions) != 2 {
		t.Fatalf("approval didn't survive reopen: %+v, %v", reloaded, err)
	}
	values, err := store.List(value.RunID)
	if err != nil || len(values) != 1 {
		t.Fatalf("list approvals: %+v, %v", values, err)
	}
}

func TestSQLiteStoreIsIdempotentAndResolvesDeferredOnlyThroughDelivery(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	input := PendingApproval{RunID: "deferred-run", AttemptID: "a1", FromStage: "coder", ToStage: "reviewer", Trigger: "stage_completed", SubjectHash: testSubject, RequiredRoles: []string{"developer"}, Actions: []string{"approve", "reject"}, Targets: map[string]string{"approve": "reviewer", "reject": ".ai-team"}, Deferred: true}
	first, err := store.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.Create(input)
	if err != nil || duplicate.ID != first.ID {
		t.Fatalf("idempotent create: %+v %v", duplicate, err)
	}
	if _, err = store.Decide(first.RunID, first.ID, Decision{ActorID: "worker", ActorRole: "developer", Action: "approve", SubjectHash: testSubject}); err == nil {
		t.Fatal("deferred approval accepted ordinary decision")
	}
	resolved, err := store.ResolveDeferred(first.RunID, first.ID, Decision{ActorID: "release", ActorRole: "release_manager", Action: "approve", SubjectHash: testSubject})
	if err != nil || resolved.Status != StatusResolved || len(resolved.Decisions) != 1 {
		t.Fatalf("deferred resolution: %+v %v", resolved, err)
	}
}

func TestImportLegacyApprovalsIsIdempotentAndKeepsDBProgress(t *testing.T) {
	target := t.TempDir()
	legacy, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value, err := legacy.Create(PendingApproval{
		RunID: "migrate-run", AttemptID: "attempt-1", FromStage: "reviewer", ToStage: "coder",
		Trigger: "stage_completed", SubjectHash: testSubject, RequiredRoles: []string{"reviewer"},
		Actions: []string{"approve"}, Targets: map[string]string{"approve": "coder"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// File-backed stores leave this known regular lock file beside approvals.
	lockPath := filepath.Join(target, ".ai-team", "state", "approvals", value.RunID, ".decide.lock")
	if info, statErr := os.Stat(lockPath); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("expected regular legacy lock file: info=%v err=%v", info, statErr)
	}
	db, err := NewSQLiteStore(filepath.Join(target, ".ai-team", "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	root := filepath.Join(target, ".ai-team", "state", "approvals")
	if err := db.ImportLegacy(root); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if _, err := db.Decide(value.RunID, value.ID, Decision{ActorID: "human", ActorRole: "reviewer", Action: "approve", SubjectHash: testSubject}); err != nil {
		t.Fatalf("decision after import: %v", err)
	}
	if err := db.ImportLegacy(root); err != nil {
		t.Fatalf("idempotent import after DB advancement: %v", err)
	}
	loaded, err := db.Load(value.RunID, value.ID)
	if err != nil || loaded.Status != StatusResolved || len(loaded.Decisions) != 1 {
		t.Fatalf("import overwrote evolved DB decision: %+v err=%v", loaded, err)
	}
	if _, err := os.Stat(filepath.Join(root, value.RunID, value.ID+".json")); err != nil {
		t.Fatalf("legacy source was removed: %v", err)
	}
}

func TestImportLegacyWaitsForPerRunDecisionAndImportsCommittedState(t *testing.T) {
	target := t.TempDir()
	legacy, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value, err := legacy.Create(PendingApproval{
		RunID: "migrate-locked-run", AttemptID: "attempt-1", FromStage: "reviewer", ToStage: "coder",
		Trigger: "stage_completed", SubjectHash: testSubject, RequiredRoles: []string{"reviewer"},
		Actions: []string{"approve"}, Targets: map[string]string{"approve": "coder"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(target, ".ai-team", "state", "approvals")
	db, err := NewSQLiteStore(filepath.Join(target, ".ai-team", "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// Hold the exact inter-process lock used by Store.Decide. The importer must
	// wait before reading this run, then include the decision published under
	// the lock in the same snapshot it commits to SQLite.
	unlock, err := legacy.lockRun(value.RunID)
	if err != nil {
		t.Fatal(err)
	}
	type importResult struct{ err error }
	lockAttempted := make(chan struct{})
	done := make(chan importResult, 1)
	go func() {
		done <- importResult{err: db.importLegacy(root, func(runID string) (func(), error) {
			close(lockAttempted)
			return legacy.lockRun(runID)
		})}
	}()
	<-lockAttempted
	select {
	case result := <-done:
		unlock()
		t.Fatalf("ImportLegacy completed while a decision held the run lock: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}

	decided, err := applyDecision(value, value.ID, Decision{
		ActorID: "human", ActorRole: "reviewer", Action: "approve", SubjectHash: testSubject,
	})
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	path, err := legacy.path(value.RunID, value.ID)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := legacy.write(path, decided); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("ImportLegacy after decision: %v", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ImportLegacy did not resume after per-run decision lock was released")
	}
	imported, err := db.Load(value.RunID, value.ID)
	if err != nil || imported.Status != StatusResolved || len(imported.Decisions) != 1 {
		t.Fatalf("import did not preserve the committed human decision: %+v, %v", imported, err)
	}
	if _, err := os.Stat(filepath.Join(root, value.RunID, value.ID+".json")); err != nil {
		t.Fatal("ImportLegacy removed the legacy source file")
	}
}

func TestImportLegacyApprovalsRejectsConflictWithoutOverwriting(t *testing.T) {
	target := t.TempDir()
	root := filepath.Join(target, ".ai-team", "state", "approvals")
	legacy, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	base := PendingApproval{RunID: "conflict-run", ID: "fixed-id", AttemptID: "a1", FromStage: "reviewer", ToStage: "coder", Trigger: "stage_completed", SubjectHash: testSubject, RequiredRoles: []string{"reviewer"}, Actions: []string{"approve"}, Targets: map[string]string{"approve": "coder"}}
	fileValue, err := legacy.Create(base)
	if err != nil {
		t.Fatal(err)
	}
	db, err := NewSQLiteStore(filepath.Join(target, ".ai-team", "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conflicting := base
	conflicting.SubjectHash = strings.Repeat("b", 64)
	if _, err := db.Create(conflicting); err != nil {
		t.Fatal(err)
	}
	if err := db.ImportLegacy(root); err == nil {
		t.Fatal("conflicting existing DB row accepted")
	}
	loaded, err := db.Load(fileValue.RunID, fileValue.ID)
	if err != nil || loaded.SubjectHash != strings.Repeat("b", 64) {
		t.Fatalf("conflict overwrote existing DB row: %+v err=%v", loaded, err)
	}
}
