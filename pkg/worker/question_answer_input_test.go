//go:build linux || darwin

package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestWorkerAPIQuestionAnswerReturnsOnlyPreparedPathFromDurableApproval(t *testing.T) {
	target := t.TempDir()
	runID, approvalID := "run-question-api", "approval-question-api"
	value := workerAnalystQuestionApproval(runID, approvalID, "Who is the buyer?", "B2B buyers", approval.StatusResolved)
	answerStore := pipeline.ControllerQuestionAnswerStore{TargetDir: target}
	source, err := answerStore.Prepare(value)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := pipeline.QuestionAnswerMaterializationPath(target, runID, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := pipeline.CanonicalQuestionAnswerContent(value)
	if err != nil {
		t.Fatal(err)
	}
	_, mountInfo, err := prepareQuestionAnswerMountpoint(destination, expected)
	if err != nil {
		t.Fatal(err)
	}
	replay := evidence.ReplayedRun{RunID: runID, Attempts: []evidence.ReplayedAttempt{{
		AttemptID: value.AttemptID, Stage: "analyst", StageIndex: 1,
		StartedAt: value.CreatedAt.Add(-time.Minute), FinishedAt: time.Now(),
		State: workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionBlocked, Outcome: workflow.OutcomeBlocked},
	}}}
	store := &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}
	server := &workerAPIServer{
		scope:     workerAPIScope{RunID: runID, Operation: OperationResume, TargetDir: target},
		approvals: store, usageAllowed: true, questionAnswerStore: answerStore,
		questionAnswerID: approvalID, questionAnswerPath: destination,
		questionAnswerMountInfo: mountInfo,
		questionAnswerMount:     &workerReadOnlyInputMount{SourcePath: source, TargetPath: destination, TargetInfo: mountInfo},
		questionAnswerReplay:    replay,
	}
	response, err := server.dispatch("handoff.question_answer.path", workerAPICall{
		RunID: runID, A: approvalID, BriefContent: []byte("worker-forged-answer-is-ignored"),
	})
	if err != nil || response != destination {
		t.Fatalf("typed request should return only the controller-mounted path: response=%v err=%v", response, err)
	}
	if _, err := server.dispatch("handoff.question_answer.path", workerAPICall{RunID: runID, A: "another-approval"}); err == nil {
		t.Fatal("cross-approval materialization request was accepted")
	}
	if _, err := server.dispatch("handoff.question_answer.path", workerAPICall{RunID: "another-run", A: approvalID}); err == nil {
		t.Fatal("cross-run materialization request was accepted")
	}

	changed := value
	changed.Decisions = append([]approval.Decision(nil), value.Decisions...)
	changed.Decisions[0].Comment = "changed after preparation"
	store.values[runID+"/"+approvalID] = changed
	if _, err := server.dispatch("handoff.question_answer.path", workerAPICall{RunID: runID, A: approvalID}); err == nil || !strings.Contains(err.Error(), "disagrees with durable approval") {
		t.Fatalf("canonical bytes not rebound to current durable approval: %v", err)
	}
	stored, err := answerStore.Read(runID, approvalID)
	if err != nil || string(stored) != "# Ответ Product Owner\n\nB2B buyers\n" {
		t.Fatalf("changed durable approval overwrote canonical bytes: data=%q err=%v", stored, err)
	}
	store.values[runID+"/"+approvalID] = value
	replacement := destination + ".replacement"
	if err := os.WriteFile(replacement, expected, 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := server.dispatch("handoff.question_answer.path", workerAPICall{RunID: runID, A: approvalID}); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("controller accepted a same-content replacement with a different file identity: %v", err)
	}
}

func TestWorkerAPIQuestionAnswerRejectsCompletedTargetAndWrongOperation(t *testing.T) {
	target := t.TempDir()
	runID, approvalID := "run-question-stale", "approval-question-stale"
	value := workerAnalystQuestionApproval(runID, approvalID, "Which buyer?", "B2B buyers", approval.StatusResolved)
	answerStore := pipeline.ControllerQuestionAnswerStore{TargetDir: target}
	source, err := answerStore.Prepare(value)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := pipeline.QuestionAnswerMaterializationPath(target, runID, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := pipeline.CanonicalQuestionAnswerContent(value)
	if err != nil {
		t.Fatal(err)
	}
	_, mountInfo, err := prepareQuestionAnswerMountpoint(destination, expected)
	if err != nil {
		t.Fatal(err)
	}
	completed := evidence.ReplayedAttempt{
		AttemptID: "target-analyst", Stage: "analyst", StageIndex: 1,
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(), Status: "passed",
		State: workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionNotApplicable, Outcome: workflow.OutcomePassed},
	}
	replay := evidence.ReplayedRun{RunID: runID, Attempts: []evidence.ReplayedAttempt{
		{AttemptID: value.AttemptID, Stage: "analyst", StageIndex: 1, StartedAt: value.CreatedAt.Add(-time.Minute), FinishedAt: value.CreatedAt,
			State: workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionBlocked, Outcome: workflow.OutcomeBlocked}},
		completed,
	}}
	server := &workerAPIServer{
		scope:        workerAPIScope{RunID: runID, Operation: OperationResume, TargetDir: target},
		approvals:    &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}},
		usageAllowed: true, questionAnswerStore: answerStore, questionAnswerID: approvalID,
		questionAnswerPath: destination, questionAnswerMountInfo: mountInfo,
		questionAnswerMount:  &workerReadOnlyInputMount{SourcePath: source, TargetPath: destination, TargetInfo: mountInfo},
		questionAnswerReplay: replay,
	}
	if _, err := server.dispatch("handoff.question_answer.path", workerAPICall{RunID: runID, A: approvalID}); err == nil {
		t.Fatal("completed analyst target received a stale answer")
	}
	server.questionAnswerReplay.Attempts = server.questionAnswerReplay.Attempts[:1]
	server.scope.Operation = OperationStart
	if _, err := server.dispatch("handoff.question_answer.path", workerAPICall{RunID: runID, A: approvalID}); err == nil {
		t.Fatal("Start operation was allowed to request a clarification input")
	}
}

func TestPrepareQuestionAnswerMountpointRecoversOnlyExactSingleLinkProjection(t *testing.T) {
	expected := []byte("# Ответ Product Owner\n\nDurable answer\n")
	path := filepath.Join(t.TempDir(), ".ai-team", "runs", "run-recovery", "inputs", "approval-recovery-answer.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, expected, 0444); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := prepareQuestionAnswerMountpoint(path, expected)
	if err != nil || created {
		t.Fatalf("exact crash-left projection should be safely overlaid on retry: created=%v err=%v", created, err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("safe recovery should preserve the verified projection identity: before=%v after=%v err=%v", before, after, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(expected) {
		t.Fatalf("recovery changed the durable projection: data=%q err=%v", data, err)
	}

	tests := []struct {
		name  string
		setup func(string) error
	}{
		{name: "mismatch", setup: func(path string) error { return os.WriteFile(path, []byte("different answer\n"), 0444) }},
		{name: "symlink", setup: func(path string) error {
			sentinel := path + ".sentinel"
			if err := os.WriteFile(sentinel, expected, 0444); err != nil {
				return err
			}
			return os.Symlink(sentinel, path)
		}},
		{name: "hardlink", setup: func(path string) error {
			if err := os.WriteFile(path, expected, 0444); err != nil {
				return err
			}
			return os.Link(path, path+".alias")
		}},
		{name: "non-regular special object", setup: func(path string) error { return os.Mkdir(path, 0700) }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "approval-answer.md")
			if err := testCase.setup(path); err != nil {
				t.Fatal(err)
			}
			if _, _, err := prepareQuestionAnswerMountpoint(path, expected); err == nil {
				t.Fatal("unsafe or conflicting previous input was accepted")
			}
			if testCase.name == "mismatch" {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "different answer\n" {
					t.Fatalf("failed recovery altered mismatched input: data=%q err=%v", data, err)
				}
			}
			if testCase.name == "symlink" {
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("failed recovery replaced the symlink: info=%v err=%v", info, err)
				}
			}
		})
	}
}

func TestPrepareQuestionAnswerMountpointReusesExistingEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answer.md")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	created, info, err := prepareQuestionAnswerMountpoint(path, []byte("answer"))
	if err != nil || created || !os.SameFile(before, info) {
		t.Fatalf("existing empty mountpoint should be reused without claiming ownership: created=%v info=%v err=%v", created, info, err)
	}
}

func TestPrepareQuestionAnswerMountpointRejectsSymlinkedParent(t *testing.T) {
	outside := t.TempDir()
	parent := filepath.Join(t.TempDir(), "inputs")
	if err := os.Symlink(outside, parent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(parent, "answer.md")
	if _, _, err := prepareQuestionAnswerMountpoint(path, []byte("answer")); err == nil {
		t.Fatal("mountpoint preparation followed a symlinked parent directory")
	}
	if _, err := os.Lstat(filepath.Join(outside, "answer.md")); !os.IsNotExist(err) {
		t.Fatalf("failed preparation wrote through the symlinked parent: %v", err)
	}
}
