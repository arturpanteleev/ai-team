package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

type canonicalQuestionAnswerTestProvider struct{ store ControllerQuestionAnswerStore }

func (p canonicalQuestionAnswerTestProvider) MaterializeQuestionAnswer(runID, approvalID string) (runtime.Artifact, error) {
	data, err := p.store.Read(runID, approvalID)
	if err != nil {
		return runtime.Artifact{}, err
	}
	path, err := QuestionAnswerMaterializationPath(p.store.TargetDir, runID, approvalID)
	if err != nil {
		return runtime.Artifact{}, err
	}
	if err := safeio.WriteRegularFileNoFollow(path, data, safeio.ReadOnlyFileMode); err != nil {
		if existing, readErr := safeio.ReadRegularFile(path, maxQuestionAnswerRecordBytes); readErr != nil || string(existing) != string(data) {
			return runtime.Artifact{}, err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return runtime.Artifact{}, err
	}
	return runtime.Artifact{Name: "clarification-answer", Path: path, Size: int64(len(data)), ModTime: info.ModTime()}, nil
}

func TestControllerQuestionAnswerStorePrepareIsExactRetryAndRejectsConflict(t *testing.T) {
	target := t.TempDir()
	store := ControllerQuestionAnswerStore{TargetDir: target}
	value := testResolvedQuestionApproval("answer-1", "B2B buyers")

	firstPath, err := store.Prepare(value)
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := store.Prepare(value)
	if err != nil || secondPath != firstPath {
		t.Fatalf("exact retry was not idempotent: first=%q second=%q err=%v", firstPath, secondPath, err)
	}
	data, err := store.Read(value.RunID, value.ID)
	if err != nil || string(data) != "# Ответ Product Owner\n\nB2B buyers\n" {
		t.Fatalf("unexpected canonical input: data=%q err=%v", data, err)
	}
	info, err := os.Lstat(firstPath)
	if err != nil || info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("canonical input must be read-only: info=%v err=%v", info, err)
	}

	conflicting := testResolvedQuestionApproval(value.ID, "different answer")
	if _, err := store.Prepare(conflicting); err == nil || !strings.Contains(err.Error(), "conflicting canonical") {
		t.Fatalf("conflicting retry must fail closed, got %v", err)
	}
	unchanged, err := store.Read(value.RunID, value.ID)
	if err != nil || string(unchanged) != string(data) {
		t.Fatalf("conflicting retry changed canonical bytes: data=%q err=%v", unchanged, err)
	}
}

func TestControllerQuestionAnswerStoreRejectsInvalidApprovals(t *testing.T) {
	base := testResolvedQuestionApproval("approval-valid", "a valid answer")
	cases := []struct {
		name   string
		change func(*approval.PendingApproval)
	}{
		{name: "pending", change: func(value *approval.PendingApproval) { value.Status = approval.StatusPending }},
		{name: "wrong stage", change: func(value *approval.PendingApproval) { value.ToStage = "coder" }},
		{name: "wrong route", change: func(value *approval.PendingApproval) { value.Targets["answer_questions"] = "coder" }},
		{name: "bad subject hash", change: func(value *approval.PendingApproval) { value.SubjectHash = "not-a-hash" }},
		{name: "no answer", change: func(value *approval.PendingApproval) { value.Decisions[0].Comment = "  " }},
		{name: "unauthorized role", change: func(value *approval.PendingApproval) { value.Decisions[0].ActorRole = "coder" }},
		{name: "wrong decision subject", change: func(value *approval.PendingApproval) { value.Decisions[0].SubjectHash = strings.Repeat("b", 64) }},
		{name: "decision not resolving", change: func(value *approval.PendingApproval) {
			value.Decisions[0].DecidedAt = value.ResolvedAt.Add(-time.Second)
		}},
		{name: "revision bound", change: func(value *approval.PendingApproval) {
			value.Decisions[0].ArtifactRevisions = map[string]string{"proposal.md": "revision-1"}
		}},
		{name: "artifact revision selection", change: func(value *approval.PendingApproval) {
			value.ArtifactRevisions = map[string]string{"proposal.md": "revision-1"}
		}},
		{name: "malformed question payload", change: func(value *approval.PendingApproval) { value.Payload = json.RawMessage(`{"kind":"other"}`) }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value := base
			value.Decisions = append([]approval.Decision(nil), base.Decisions...)
			value.Targets = map[string]string{"answer_questions": "analyst", "stop": "$stop"}
			testCase.change(&value)
			store := ControllerQuestionAnswerStore{TargetDir: t.TempDir()}
			if _, err := store.Prepare(value); err == nil {
				t.Fatal("invalid approval was accepted")
			}
		})
	}
}

func TestControllerQuestionAnswerStoreSupportsAnyBoundStage(t *testing.T) {
	value := testResolvedQuestionApproval("approval-questioner", "answer for questioner")
	value.FromStage = "questioner"
	value.ToStage = "questioner"
	value.Targets["answer_questions"] = "questioner"
	store := ControllerQuestionAnswerStore{TargetDir: t.TempDir()}
	path, err := store.Prepare(value)
	if err != nil {
		t.Fatalf("valid non-analyst clarification should be materialized: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stage-bound canonical answer was not created: %v", err)
	}
	data, err := store.Read(value.RunID, value.ID)
	if err != nil || !strings.Contains(string(data), "answer for questioner") {
		t.Fatalf("stage-bound canonical answer was not readable: data=%q err=%v", data, err)
	}

	for _, mutate := range []func(*approval.PendingApproval){
		func(value *approval.PendingApproval) { value.ToStage = "analyst" },
		func(value *approval.PendingApproval) { value.Targets["answer_questions"] = "analyst" },
	} {
		mismatched := value
		mismatched.Targets = map[string]string{"answer_questions": value.Targets["answer_questions"], "stop": "$stop"}
		mutate(&mismatched)
		if _, err := CanonicalQuestionAnswerContent(mismatched); err == nil {
			t.Fatalf("stage-mismatched clarification was accepted: %+v", mismatched)
		}
	}
}

func TestControllerQuestionAnswerStoreReadRejectsSymlinkParents(t *testing.T) {
	target := t.TempDir()
	store := ControllerQuestionAnswerStore{TargetDir: target}
	value := testResolvedQuestionApproval("approval-symlink", "known answer")
	path, err := store.Prepare(value)
	if err != nil {
		t.Fatal(err)
	}
	approvalDir := filepath.Dir(path)
	backupDir := approvalDir + ".saved"
	if err := os.Rename(approvalDir, backupDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backupDir, approvalDir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(value.RunID, value.ID); err == nil {
		t.Fatal("canonical read followed a symlink parent")
	}
	data, err := os.ReadFile(filepath.Join(backupDir, "answer.md"))
	if err != nil || string(data) != "# Ответ Product Owner\n\nknown answer\n" {
		t.Fatalf("symlink probe altered the original answer: data=%q err=%v", data, err)
	}
}

func TestControllerQuestionAnswerStoreValidatesCanonicalBytesAgainstApproval(t *testing.T) {
	target := t.TempDir()
	store := ControllerQuestionAnswerStore{TargetDir: target}
	value := testResolvedQuestionApproval("approval-validate-canonical", "durable answer")
	path, err := store.Prepare(value)
	if err != nil {
		t.Fatal(err)
	}

	validatedPath, err := store.ValidateCanonicalQuestionAnswer(value)
	if err != nil || validatedPath != path {
		t.Fatalf("exact canonical record should validate: path=%q want=%q err=%v", validatedPath, path, err)
	}

	changed := value
	changed.Decisions = append([]approval.Decision(nil), value.Decisions...)
	changed.Decisions[0].Comment = "answer changed after approval"
	if _, err := store.ValidateCanonicalQuestionAnswer(changed); err == nil || !strings.Contains(err.Error(), "disagrees with durable approval") {
		t.Fatalf("canonical record must be rebound to the current approval: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateCanonicalQuestionAnswer(value); err == nil {
		t.Fatal("missing canonical record was accepted")
	}
}

func testResolvedQuestionApproval(id, answer string) approval.PendingApproval {
	resolvedAt := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(questionPayload{Kind: "questions", Markdown: "Who is the buyer?"})
	return approval.PendingApproval{
		SchemaVersion: approval.SchemaVersion, Kind: approval.KindQuestions, ID: id, RunID: "run-clarification-test",
		AttemptID: "attempt-analyst-source", FromStage: "analyst", ToStage: "analyst",
		Trigger: "graph_outcome:blocked", SubjectHash: strings.Repeat("a", 64),
		RequiredRoles: []string{"product_owner"}, Quorum: approval.QuorumAny,
		Actions: []string{"answer_questions", "stop"},
		Targets: map[string]string{"answer_questions": "analyst", "stop": "$stop"},
		Status:  approval.StatusResolved, ResolvedAction: "answer_questions", CreatedAt: resolvedAt.Add(-time.Minute), ResolvedAt: resolvedAt,
		Decisions: []approval.Decision{{
			ApprovalID: id, ActorID: "owner@example.com", ActorRole: "product_owner",
			Action: "answer_questions", Comment: answer, SubjectHash: strings.Repeat("a", 64), DecidedAt: resolvedAt,
		}}, Payload: payload,
	}
}
