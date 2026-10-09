package pipeline

import (
	"errors"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

type questionApprovalList struct{ values []approval.PendingApproval }

func (s questionApprovalList) Load(string, string) (approval.PendingApproval, error) {
	return approval.PendingApproval{}, errors.New("unexpected approval load")
}

func (s questionApprovalList) List(string) ([]approval.PendingApproval, error) {
	return append([]approval.PendingApproval(nil), s.values...), nil
}

func TestQuestionAnswerApprovalRecoveryRequiresOriginalIncompleteTarget(t *testing.T) {
	value := testResolvedQuestionApproval("approval-recovery", "same durable answer")
	source := evidence.ReplayedAttempt{
		AttemptID: value.AttemptID, Stage: "analyst", StageIndex: 1,
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(),
		State: workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionBlocked, Outcome: workflow.OutcomeBlocked},
	}
	run := evidence.ReplayedRun{
		RunID: value.RunID, Attempts: []evidence.ReplayedAttempt{source},
		ApprovalDecisions: []evidence.ReplayedApprovalDecision{{
			Sequence: 1,
			ID:       value.ID, SubjectHash: value.SubjectHash, AttemptID: value.AttemptID,
			FromStage: value.FromStage, ToStage: value.ToStage, Trigger: value.Trigger, Action: value.ResolvedAction,
		}},
		Transitions: []evidence.ReplayedTransition{{
			Sequence:  2,
			AttemptID: value.AttemptID, From: "analyst", Outcome: "blocked", EdgeTarget: "analyst",
			Action: "answer_questions", Target: "analyst",
		}},
	}
	if err := ValidateQuestionAnswerApproval(value, run); err != nil {
		t.Fatalf("blocked source with no target yet should remain recoverable: %v", err)
	}

	activeTarget := evidence.ReplayedAttempt{
		AttemptID: "attempt-analyst-target", Stage: "analyst", StageIndex: 1,
		StartedAt: time.Now(), State: workflow.AttemptState{Execution: workflow.ExecutionRunning, Outcome: workflow.OutcomePending},
	}
	run.Attempts = append(run.Attempts, activeTarget)
	if err := ValidateQuestionAnswerApproval(value, run); err != nil {
		t.Fatalf("active incomplete target should reuse the immutable answer: %v", err)
	}

	completedTarget := activeTarget
	completedTarget.FinishedAt = time.Now().Add(time.Minute)
	completedTarget.Status = "passed"
	completedTarget.State = workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionNotApplicable, Outcome: workflow.OutcomePassed}
	run.Attempts[1] = completedTarget
	if err := ValidateQuestionAnswerApproval(value, run); !errors.Is(err, ErrStaleQuestionApproval) {
		t.Fatalf("completed target should make its old answer stale, got %v", err)
	}
	selected, err := RecoveredQuestionApproval(questionApprovalList{values: []approval.PendingApproval{value}}, value.RunID, "analyst", run)
	if selected != nil || !errors.Is(err, ErrStaleQuestionApproval) {
		t.Fatalf("recovery must fail closed after a completed target: selected=%+v err=%v", selected, err)
	}

	// A target attempt abandoned by controller restart did not complete its
	// stage. The same durable answer remains usable for that retry boundary.
	completedTarget.State = workflow.AttemptState{Execution: workflow.ExecutionCanceled, Decision: workflow.DecisionNotApplicable, Outcome: workflow.OutcomeCanceled}
	completedTarget.Status = "canceled"
	run.Attempts[1] = completedTarget
	if err := ValidateQuestionAnswerApproval(value, run); err != nil {
		t.Fatalf("abandoned target should remain recoverable: %v", err)
	}
}

func TestRecoveredQuestionApprovalRestoresGenericStageAnswer(t *testing.T) {
	value := testResolvedQuestionApproval("approval-questioner-recovery", "resume answer")
	value.FromStage = "questioner"
	value.ToStage = "questioner"
	value.Targets["answer_questions"] = "questioner"
	source := evidence.ReplayedAttempt{
		AttemptID: value.AttemptID, Stage: "questioner", StageIndex: 1,
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(),
		State: workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionBlocked, Outcome: workflow.OutcomeBlocked},
	}
	replayed := evidence.ReplayedRun{
		RunID: value.RunID, Attempts: []evidence.ReplayedAttempt{source},
		ApprovalDecisions: []evidence.ReplayedApprovalDecision{{
			Sequence: 1, ID: value.ID, SubjectHash: value.SubjectHash, AttemptID: value.AttemptID,
			FromStage: "questioner", ToStage: "questioner", Trigger: value.Trigger, Action: value.ResolvedAction,
		}},
		Transitions: []evidence.ReplayedTransition{{
			Sequence: 2, AttemptID: value.AttemptID, From: "questioner", Outcome: "blocked", EdgeTarget: "questioner",
			Action: "answer_questions", Target: "questioner",
		}},
	}
	if err := ValidateQuestionAnswerApproval(value, replayed); err != nil {
		t.Fatalf("non-analyst question loop should validate against its source stage: %v", err)
	}
	completedTarget := evidence.ReplayedAttempt{
		AttemptID: "attempt-questioner-target", Stage: "questioner", StageIndex: 1,
		StartedAt: time.Now(), FinishedAt: time.Now().Add(time.Second),
		State: workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionNotApplicable, Outcome: workflow.OutcomePassed},
	}
	replayed.Attempts = append(replayed.Attempts, completedTarget)
	if err := ValidateQuestionAnswerApproval(value, replayed); !errors.Is(err, ErrStaleQuestionApproval) {
		t.Fatalf("completed non-analyst target should make the old answer stale, got %v", err)
	}
	if selected, err := RecoveredQuestionApproval(questionApprovalList{values: []approval.PendingApproval{value}}, value.RunID, "questioner", replayed); selected != nil || !errors.Is(err, ErrStaleQuestionApproval) {
		t.Fatalf("strict recovery must reject a completed non-analyst target: selected=%+v err=%v", selected, err)
	}
	replayed.Attempts = replayed.Attempts[:1]
	selectedStrict, err := RecoveredQuestionApproval(questionApprovalList{values: []approval.PendingApproval{value}}, value.RunID, "questioner", replayed)
	if err != nil || selectedStrict == nil || selectedStrict.ID != value.ID {
		t.Fatalf("strict recovery did not restore the questioner answer: selected=%+v err=%v", selectedStrict, err)
	}

	if selected, err := RecoveredQuestionApproval(questionApprovalList{values: []approval.PendingApproval{value}}, value.RunID, "", replayed); err != nil || selected != nil {
		t.Fatalf("empty stage should not recover an answer: selected=%+v err=%v", selected, err)
	}
	wrongKind := value
	wrongKind.Kind = approval.KindApprove
	invalidPayload := value
	invalidPayload.ID = "approval-invalid-question-payload"
	invalidPayload.Payload = []byte(`{"kind":"other"}`)
	selected, err := RecoveredQuestionApproval(questionApprovalList{values: []approval.PendingApproval{invalidPayload, wrongKind, value}}, value.RunID, "questioner", replayed)
	if err != nil || selected == nil || selected.ID != value.ID || questionAnswer(selected.Decisions) != "resume answer" {
		t.Fatalf("generic questioner answer was not recovered: selected=%+v err=%v", selected, err)
	}
	if selected, err := RecoveredQuestionApproval(questionApprovalList{values: []approval.PendingApproval{value}}, value.RunID, "analyst", replayed); err != nil || selected != nil {
		t.Fatalf("questioner answer was recovered into a different stage: selected=%+v err=%v", selected, err)
	}
}

func TestQuestionAnswerApprovalRejectsForeignAndCompletedRuns(t *testing.T) {
	value := testResolvedQuestionApproval("approval-run-binding", "answer")
	source := evidence.ReplayedAttempt{
		AttemptID: value.AttemptID, Stage: "analyst", StageIndex: 1,
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(),
		State: workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionBlocked, Outcome: workflow.OutcomeBlocked},
	}
	run := evidence.ReplayedRun{RunID: "another-run", Attempts: []evidence.ReplayedAttempt{source}}
	if err := ValidateQuestionAnswerApproval(value, run); err == nil {
		t.Fatal("foreign replay run was accepted")
	}
	run.RunID = value.RunID
	run.FinishedAt = time.Now()
	if err := ValidateQuestionAnswerApproval(value, run); err == nil {
		t.Fatal("terminal replay was accepted for clarification recovery")
	}
}
