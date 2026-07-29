package approval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSubject = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestStoreAnyQuorumAndIdempotentDecision(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "run-1", AttemptID: "attempt-1", FromStage: "analyst", ToStage: "architect",
		Trigger: "stage_completed", SubjectHash: testSubject,
		RequiredRoles: []string{"product_owner"}, Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "architect", "reject": "architect"},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := Decision{
		ActorID: "user-1", ActorRole: "product_owner", Action: "approve", SubjectHash: testSubject,
	}
	resolved, err := store.Decide(value.RunID, value.ID, decision)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != StatusResolved || resolved.ResolvedAction != "approve" {
		t.Fatalf("неожиданный результат: %+v", resolved)
	}
	again, err := store.Decide(value.RunID, value.ID, decision)
	if err != nil || len(again.Decisions) != 1 {
		t.Fatalf("точный повтор должен быть идемпотентным: value=%+v err=%v", again, err)
	}
	decision.Action = "reject"
	if _, err := store.Decide(value.RunID, value.ID, decision); err == nil ||
		!strings.Contains(err.Error(), "конфликтующее") {
		t.Fatalf("ожидался конфликт решения, got %v", err)
	}
}

func TestStoreRejectsStaleSubjectAndWrongRole(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "run-2", AttemptID: "attempt-2", FromStage: "coder", ToStage: "reviewer",
		Trigger: "stage_completed", SubjectHash: testSubject,
		RequiredRoles: []string{"developer"}, Actions: []string{"approve"},
		Targets: map[string]string{"approve": "reviewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(value.RunID, value.ID, Decision{
		ActorID: "user", ActorRole: "developer", Action: "approve",
		SubjectHash: strings.Repeat("b", 64),
	}); err == nil || !strings.Contains(err.Error(), "subject hash") {
		t.Fatalf("ожидался stale subject, got %v", err)
	}
	if _, err := store.Decide(value.RunID, value.ID, Decision{
		ActorID: "user", ActorRole: "qa", Action: "approve", SubjectHash: testSubject,
	}); err == nil {
		t.Fatal("решение с неверной ролью принято")
	}
}

func TestStoreAllQuorum(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "run-all", AttemptID: "attempt-all", FromStage: "tester", ToStage: "verifier",
		Trigger: "stage_completed", SubjectHash: testSubject, Quorum: QuorumAll,
		RequiredRoles: []string{"qa", "security"}, Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "verifier", "reject": "verifier"},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err = store.Decide(value.RunID, value.ID, Decision{
		ActorID: "qa-user", ActorRole: "qa", Action: "approve", SubjectHash: testSubject,
	})
	if err != nil || value.Status != StatusPending {
		t.Fatalf("первое решение не должно закрывать all quorum: %+v %v", value, err)
	}
	if _, err := store.Decide(value.RunID, value.ID, Decision{
		ActorID: "sec-user", ActorRole: "security", Action: "reject", SubjectHash: testSubject,
	}); err == nil {
		t.Fatal("конфликтующие actions при all quorum приняты")
	}
	value, err = store.Decide(value.RunID, value.ID, Decision{
		ActorID: "sec-user", ActorRole: "security", Action: "approve", SubjectHash: testSubject,
	})
	if err != nil || value.Status != StatusResolved {
		t.Fatalf("all quorum не разрешён: %+v %v", value, err)
	}
}

func TestStoreFailsClosedOnCorruption(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "run-bad", AttemptID: "attempt-bad", FromStage: "a", ToStage: "b",
		Trigger: "stage_completed", SubjectHash: testSubject,
		RequiredRoles: []string{"owner"}, Actions: []string{"approve"},
		Targets: map[string]string{"approve": "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target, ".ai-team", "state", "approvals", value.RunID, value.ID+".json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(value.RunID, value.ID); err == nil {
		t.Fatal("повреждённый approval принят")
	}
}

func TestStoreListOrdersApprovalsAndReturnsEmpty(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	empty, err := store.List("run-list")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty list: %v %v", empty, err)
	}
	for index, stage := range []string{"a", "b"} {
		if _, err := store.Create(PendingApproval{
			RunID: "run-list", AttemptID: "attempt-" + stage, FromStage: stage, ToStage: "next",
			Trigger: "stage_completed", SubjectHash: testSubject,
			RequiredRoles: []string{"owner"}, Actions: []string{"approve"},
			Targets:   map[string]string{"approve": "next"},
			CreatedAt: time.Unix(int64(index+1), 0).UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	values, err := store.List("run-list")
	if err != nil || len(values) != 2 || values[0].FromStage != "a" || values[1].FromStage != "b" {
		t.Fatalf("ordered list: %+v err=%v", values, err)
	}
}
