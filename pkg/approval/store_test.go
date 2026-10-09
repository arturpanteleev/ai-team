package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestInputApprovalBindsSubmissionContentAndActor(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(InputPayload{
		Kind: string(KindInput), StageID: "product_spec", Result: "md",
		OutputName: "spec", OutputPath: "feature/specs/product/spec.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		Kind: KindInput, RunID: "run-human", AttemptID: "run-human-001-product_spec",
		FromStage: "product_spec", ToStage: "product_spec", Trigger: "human_input",
		SubjectHash: testSubject, RequiredRoles: []string{"product_owner"}, Quorum: QuorumAny,
		Actions: []string{"submit", "reject"},
		Targets: map[string]string{"submit": "product_spec", "reject": "product_spec"},
		Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := Decision{ActorID: "alice", ActorRole: "product_owner", Action: "submit", SubjectHash: testSubject}
	if _, err := store.Decide(value.RunID, value.ID, base); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("empty input submission must be rejected: %v", err)
	}
	base.Comment = "\n# Product spec\n\n- exact whitespace  \n"
	resolved, err := store.Decide(value.RunID, value.ID, base)
	if err != nil || resolved.Status != StatusResolved || resolved.Decisions[0].ActorID != "alice" || resolved.Decisions[0].Comment != base.Comment {
		t.Fatalf("human submission did not resolve with actor identity: value=%+v err=%v", resolved, err)
	}
	if _, err := store.Decide(value.RunID, value.ID, base); err != nil {
		t.Fatalf("exact same content must be idempotent: %v", err)
	}
	base.Comment = "# Different content"
	if _, err := store.Decide(value.RunID, value.ID, base); err == nil || !strings.Contains(err.Error(), "конфликтующее") {
		t.Fatalf("same actor must not replace a submitted result: %v", err)
	}
}

func TestInputApprovalPersistsBoundaryWhitespaceAndSubmissionMetadata(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(InputPayload{Kind: string(KindInput), StageID: "spec", Result: "md", OutputName: "spec", OutputPath: "feature/spec.md"})
	value, err := store.Create(PendingApproval{
		Kind: KindInput, RunID: "run-exact-input", AttemptID: "attempt-exact-input", FromStage: "spec", ToStage: "spec",
		Trigger: "human_input", SubjectHash: testSubject, RequiredRoles: []string{"architect"}, Quorum: QuorumAny,
		Actions: []string{"submit", "reject"}, Targets: map[string]string{"submit": "spec", "reject": "spec"}, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := " \n\n# Exact bytes  \n\n "
	digest := sha256.Sum256([]byte(content))
	decision := Decision{ActorID: "architect-1", ActorRole: "architect", Action: "submit", Comment: content,
		Description: "  complete  \n", SubmissionVersion: 1, ContentSHA256: hex.EncodeToString(digest[:]), SubjectHash: testSubject}
	resolved, err := store.Decide(value.RunID, value.ID, decision)
	if err != nil || resolved.Decisions[0].Comment != content {
		t.Fatalf("input boundary whitespace was changed: decision=%+v err=%v", resolved.Decisions, err)
	}
	reloadedStore, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := reloadedStore.Load(value.RunID, value.ID)
	if err != nil || reloaded.Decisions[0].Comment != content || reloaded.Decisions[0].Description != decision.Description ||
		reloaded.Decisions[0].ContentSHA256 != decision.ContentSHA256 || reloaded.Decisions[0].SubmissionVersion != 1 {
		t.Fatalf("durable submission changed after reload: decision=%+v err=%v", reloaded.Decisions, err)
	}
	if retried, err := reloadedStore.Decide(value.RunID, value.ID, decision); err != nil || retried.Decisions[0].Comment != content {
		t.Fatalf("exact boundary-whitespace retry was not idempotent: decision=%+v err=%v", retried.Decisions, err)
	}
	decision.Comment = "# changed"
	if _, err := reloadedStore.Decide(value.RunID, value.ID, decision); err == nil {
		t.Fatal("changed bytes must not replace the boundary-whitespace submission")
	}
}

func TestInputApprovalRequiresHumanInputTrigger(t *testing.T) {
	payload, err := json.Marshal(InputPayload{
		Kind: string(KindInput), StageID: "product_spec", Result: "md",
		OutputName: "spec", OutputPath: "feature/specs/product/spec.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	value := PendingApproval{
		SchemaVersion: SchemaVersion, Kind: KindInput, ID: "approval-input-trigger", RunID: "run-input-trigger",
		AttemptID: "attempt-1", FromStage: "product_spec", ToStage: "product_spec", Trigger: "graph_outcome:passed",
		SubjectHash: testSubject, RequiredRoles: []string{"product_owner"}, Quorum: QuorumAny,
		Actions: []string{"submit", "reject"},
		Targets: map[string]string{"submit": "product_spec", "reject": "product_spec"},
		Payload: payload, CreatedAt: time.Now().UTC(), Status: StatusPending,
	}
	if err := validate(value); err == nil {
		t.Fatal("input approval with a graph transition trigger should be rejected")
	}
}

func TestConcurrentIdenticalInputSubmissionIsIdempotent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(InputPayload{
		Kind: string(KindInput), StageID: "product_spec", Result: "md",
		OutputName: "spec", OutputPath: "feature/spec.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		Kind: KindInput, RunID: "run-input-concurrent", AttemptID: "attempt-1",
		FromStage: "product_spec", ToStage: "product_spec", Trigger: "human_input",
		SubjectHash: testSubject, RequiredRoles: []string{"product_owner"}, Quorum: QuorumAny,
		Actions: []string{"submit", "reject"},
		Targets: map[string]string{"submit": "product_spec", "reject": "product_spec"}, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := Decision{ActorID: "owner", ActorRole: "product_owner", Action: "submit", SubjectHash: testSubject, Comment: "# Exact bytes\n"}
	const writers = 8
	start := make(chan struct{})
	results := make(chan error, writers)
	var wait sync.WaitGroup
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, decideErr := store.Decide(value.RunID, value.ID, decision)
			results <- decideErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for resultErr := range results {
		if resultErr != nil {
			t.Fatalf("identical concurrent submission must be idempotent: %v", resultErr)
		}
	}
	reloaded, err := store.Load(value.RunID, value.ID)
	if err != nil || reloaded.Status != StatusResolved || len(reloaded.Decisions) != 1 || reloaded.Decisions[0].Comment != decision.Comment {
		t.Fatalf("concurrent submit changed the immutable result: value=%+v err=%v", reloaded, err)
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

func TestStoreExplainsForbiddenReturnRouteAndRequiresReason(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "run-return", AttemptID: "attempt-return", FromStage: "reviewer", ToStage: "coder",
		Trigger: "graph_outcome:rejected", SubjectHash: testSubject,
		RequiredRoles: []string{"reviewer"}, Actions: []string{"return_to_coder"}, FeedbackActions: []string{"return_to_coder"},
		Targets: map[string]string{"return_to_coder": "coder"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(value.RunID, value.ID, Decision{
		ActorID: "reviewer-1", ActorRole: "reviewer", Action: "return_to_analyst",
		SubjectHash: testSubject, Comment: "requirements unclear",
	}); err == nil || !strings.Contains(err.Error(), "запрещён возврат") {
		t.Fatalf("expected explicit forbidden-route error, got %v", err)
	}
	if _, err := store.Decide(value.RunID, value.ID, Decision{
		ActorID: "reviewer-1", ActorRole: "reviewer", Action: "return_to_coder",
		SubjectHash: testSubject,
	}); err == nil || !strings.Contains(err.Error(), "требуется причина") {
		t.Fatalf("expected return-reason validation, got %v", err)
	}
}

func TestResolvedDecisionBindsSelectedArtifactRevision(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "run-revision", AttemptID: "attempt-revision", FromStage: "reviewer", ToStage: "coder",
		Trigger: "graph_outcome:rejected", SubjectHash: testSubject,
		RequiredRoles: []string{"reviewer"}, Actions: []string{"return_to_coder"}, FeedbackActions: []string{"return_to_coder"},
		Targets: map[string]string{"return_to_coder": "coder"},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := map[string]string{"attempts/attempt-revision/review.md": "rev-000002-example"}
	resolved, err := store.Decide(value.RunID, value.ID, Decision{
		ActorID: "reviewer-1", ActorRole: "reviewer", Action: "return_to_coder", Comment: "please fix",
		SubjectHash: testSubject, ArtifactRevisions: selection,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameRevisionSelection(resolved.ArtifactRevisions, selection) || resolved.ArtifactRevisionBindingSHA256 == "" ||
		resolved.ArtifactRevisionBindingSHA256 != revisionBindingHash(testSubject, selection) {
		t.Fatalf("resolved handoff is not bound to its chosen artifact revision: %+v", resolved)
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

func TestStoreConcurrentDecisionsLoseNoUpdate(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "run-race", AttemptID: "attempt-1", FromStage: "reviewer", ToStage: "tester",
		Trigger: "stage_completed", SubjectHash: testSubject,
		RequiredRoles: []string{"product_owner"}, Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "tester", "reject": "tester"},
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			action := "approve"
			if index%2 == 1 {
				action = "reject"
			}
			_, decideErr := store.Decide(value.RunID, value.ID, Decision{
				ActorID: fmt.Sprintf("user-%d", index), ActorRole: "product_owner",
				Action: action, SubjectHash: testSubject,
			})
			results <- decideErr
		}(index)
	}
	wg.Wait()
	close(results)

	succeeded := 0
	for decideErr := range results {
		if decideErr == nil {
			succeeded++
		}
	}
	if succeeded == 0 {
		t.Fatal("ни одно решение не записалось")
	}

	final, loadErr := store.Load(value.RunID, value.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if final.Status != StatusResolved || len(final.Decisions) != succeeded {
		t.Fatalf("рассинхронизация decisions: записано=%d успешно=%d status=%s",
			len(final.Decisions), succeeded, final.Status)
	}
	for _, decision := range final.Decisions {
		if decision.Action != final.ResolvedAction && final.Quorum == QuorumAny {
			t.Fatalf("чужое action после разрешения: %+v", final)
		}
	}
}

func TestStoreDeferredResolvedByConsolidatedDeliveryDecision(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "run-deferred", AttemptID: "attempt-1", FromStage: "coder", ToStage: "reviewer",
		Trigger: "stage_completed", SubjectHash: testSubject,
		RequiredRoles: []string{"developer"}, Actions: []string{"approve", "reject"},
		Targets:  map[string]string{"approve": "reviewer", "reject": ".ai-team"},
		Deferred: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !value.Deferred {
		t.Fatal("deferred flag не сохранён")
	}
	// per-gate Decide запрещён для deferred.
	if _, err := store.Decide(value.RunID, value.ID, Decision{
		ActorID: "user-1", ActorRole: "developer", Action: "approve", SubjectHash: testSubject,
	}); err == nil || !strings.Contains(err.Error(), "deferred") {
		t.Fatalf("Decide не должен разрешать deferred approval, got %v", err)
	}
	// Consolidated delivery-решение: роль delivery-контрпоинта не обязана быть
	// в RequiredRoles гейта, subject и action проверяются строго.
	resolved, err := store.ResolveDeferred(value.RunID, value.ID, Decision{
		ActorID: "release-1", ActorRole: "release_manager", Action: "approve",
		SubjectHash: testSubject, Comment: "consolidated delivery decision",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != StatusResolved || resolved.ResolvedAction != "approve" ||
		len(resolved.Decisions) != 1 || resolved.Decisions[0].ActorRole != "release_manager" {
		t.Fatalf("неожиданный consolidated результат: %+v", resolved)
	}
	reloaded, err := store.Load(value.RunID, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != StatusResolved || reloaded.Deferred != true ||
		reloaded.Decisions[0].ActorRole != "release_manager" {
		t.Fatalf("resolved deferred не пережил reload: %+v", reloaded)
	}
	// Повторное разрешение запрещено.
	if _, err := store.ResolveDeferred(value.RunID, value.ID, Decision{
		ActorID: "release-2", ActorRole: "release_manager", Action: "approve",
		SubjectHash: testSubject,
	}); err == nil || !strings.Contains(err.Error(), "уже разрешён") {
		t.Fatalf("двойное разрешение deferred принято: %v", err)
	}
}

func TestStoreDeferredStrictSubjectAndAction(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "run-deferred-strict", AttemptID: "attempt-1", FromStage: "a", ToStage: "b",
		Trigger: "stage_completed", SubjectHash: testSubject,
		RequiredRoles: []string{"owner"}, Actions: []string{"approve", "reject"},
		Targets:  map[string]string{"approve": "b", "reject": "b"},
		Deferred: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveDeferred(value.RunID, value.ID, Decision{
		ActorID: "release-1", ActorRole: "release_manager", Action: "approve",
		SubjectHash: strings.Repeat("b", 64),
	}); err == nil || !strings.Contains(err.Error(), "subject hash") {
		t.Fatalf("несовпадающий subject должен быть отклонён: %v", err)
	}
	if _, err := store.ResolveDeferred(value.RunID, value.ID, Decision{
		ActorID: "release-1", ActorRole: "release_manager", Action: "override_approve",
		SubjectHash: testSubject,
	}); err == nil || !strings.Contains(err.Error(), "action") {
		t.Fatalf("недопустимый action должен быть отклонён: %v", err)
	}
	// ResolveDeferred не применяется к обычным (не deferred) approvals.
	plain, err := store.Create(PendingApproval{
		RunID: "run-plain", AttemptID: "attempt-1", FromStage: "a", ToStage: "b",
		Trigger: "stage_completed", SubjectHash: testSubject,
		RequiredRoles: []string{"owner"}, Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "b", "reject": "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveDeferred(plain.RunID, plain.ID, Decision{
		ActorID: "release-1", ActorRole: "release_manager", Action: "approve",
		SubjectHash: testSubject,
	}); err == nil || !strings.Contains(err.Error(), "deferred approval") {
		t.Fatalf("ResolveDeferred не должен применяться к обычному approval: %v", err)
	}
}

func TestStorePayloadRoundTripAndValidation(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(PendingApproval{
		RunID: "run-payload", AttemptID: "attempt-1", FromStage: "deployer", ToStage: "deployer",
		Trigger: "delivery_plan", SubjectHash: testSubject,
		RequiredRoles: []string{"release_manager"}, Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "deployer", "reject": "deployer"},
		Payload: json.RawMessage(`{"commit_message":"x"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, loadErr := store.Load(value.RunID, value.ID)
	var payload map[string]any
	if loadErr != nil || json.Unmarshal(reloaded.Payload, &payload) != nil ||
		payload["commit_message"] != "x" {
		t.Fatalf("payload потерян: %q err=%v", reloaded.Payload, loadErr)
	}
	if _, err := store.Create(PendingApproval{
		RunID: "run-payload-bad", AttemptID: "attempt-1", FromStage: "deployer", ToStage: "deployer",
		Trigger: "delivery_plan", SubjectHash: testSubject,
		RequiredRoles: []string{"release_manager"}, Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "deployer", "reject": "deployer"},
		Payload: json.RawMessage(`{broken`),
	}); err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("битый payload должен быть отклонён, got %v", err)
	}
}
