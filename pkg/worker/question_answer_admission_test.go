package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
)

func TestWorkerAPIQuestionAnswerAdmissionDefersStaleRunningClarificationToPipeline(t *testing.T) {
	for _, phase := range []lifecycle.Phase{lifecycle.PhaseRunning, lifecycle.PhaseResumable} {
		t.Run(string(phase)+" checkpoint after verified outgoing transition", func(t *testing.T) {
			api, _, replayed := newStaleQuestionAdmissionFixture(t, phase)
			if len(replayed.Transitions) < 2 || replayed.Transitions[len(replayed.Transitions)-1].Target != "coder" {
				t.Fatalf("fixture must end at the verified analyst-to-coder transition: %+v", replayed.Transitions)
			}
			if _, err := pipeline.RecoveredQuestionApproval(api.approvals, api.scope.RunID, "analyst", replayed); !errors.Is(err, pipeline.ErrStaleQuestionApproval) {
				t.Fatalf("fixture must expose the older clarification as stale before admission: %v", err)
			}
			mount, err := api.prepareQuestionAnswerMount(context.Background())
			if err != nil || mount != nil {
				t.Fatalf("stale answer must defer to pipeline reconciliation: mount=%+v err=%v", mount, err)
			}
			if api.questionAnswerID != "" || api.questionAnswerPath != "" {
				t.Fatalf("stale clarification was partially admitted: id=%q path=%q", api.questionAnswerID, api.questionAnswerPath)
			}
		})
	}

	t.Run("waiting checkpoint remains fail closed", func(t *testing.T) {
		api, _, _ := newStaleQuestionAdmissionFixture(t, lifecycle.PhaseWaiting)
		mount, err := api.prepareQuestionAnswerMount(context.Background())
		if mount != nil || !errors.Is(err, pipeline.ErrStaleQuestionApproval) {
			t.Fatalf("stale waiting approval must remain fatal: mount=%+v err=%v", mount, err)
		}
	})

	t.Run("non-stale approval-store error remains fatal", func(t *testing.T) {
		api, _, _ := newStaleQuestionAdmissionFixture(t, lifecycle.PhaseRunning)
		api.approvals = &apiApprovalStore{listErr: errors.New("approval list backend unavailable")}
		mount, err := api.prepareQuestionAnswerMount(context.Background())
		if mount != nil || err == nil || errors.Is(err, pipeline.ErrStaleQuestionApproval) ||
			!strings.Contains(err.Error(), "approval list backend unavailable") {
			t.Fatalf("non-stale recovery failure must remain fatal: mount=%+v err=%v", mount, err)
		}
	})
}

// newStaleQuestionAdmissionFixture creates the production controller-owned
// event/manifest authority and a lifecycle checkpoint left on analyst after a
// successful target analyst attempt and its outgoing graph transition. This
// is the crash boundary before Pipeline can persist NextStage=coder.
func newStaleQuestionAdmissionFixture(t *testing.T, phase lifecycle.Phase) (*workerAPIServer, *lifecycle.Store, evidence.ReplayedRun) {
	t.Helper()
	target, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const (
		runID         = "stale-question-admission"
		approvalID    = "approval-old-clarification"
		sourceAttempt = "attempt-analyst-source"
		targetAttempt = "attempt-analyst-target"
	)
	startedAt := time.Now().UTC().Add(-time.Minute)
	questionFinished := startedAt.Add(time.Second)
	answerResolvedAt := questionFinished.Add(time.Second)
	targetStartedAt := answerResolvedAt.Add(time.Second)
	targetFinishedAt := targetStartedAt.Add(time.Second)

	eventAuthority := evidence.ControllerEventStore{TargetDir: target}
	if err := eventAuthority.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	manifestAuthority := evidence.ControllerAttemptManifestStore{TargetDir: target}
	if err := manifestAuthority.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	runEvidence, err := evidence.StartWithEventLog(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "stale-question-admission", TargetDir: target, StartedAt: startedAt,
		ConfigSnapshot: json.RawMessage(`{}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	}, eventAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if err := runEvidence.Append(evidence.Event{Type: "run_started", Timestamp: startedAt}); err != nil {
		t.Fatal(err)
	}
	appendAttempt := func(attemptID, stage string, stageIndex int, attemptStarted, attemptFinished time.Time, status, execution, decision, outcome string, questions bool) {
		t.Helper()
		if err := runEvidence.Append(evidence.Event{
			Type: "attempt_started", Stage: stage, AttemptID: attemptID, Timestamp: attemptStarted,
			Data: map[string]any{"stage_index": stageIndex},
		}); err != nil {
			t.Fatal(err)
		}
		artifactRoot := filepath.Join(runEvidence.RunDir(), "artifacts")
		if err := os.MkdirAll(artifactRoot, 0700); err != nil {
			t.Fatal(err)
		}
		var outputs []evidence.Artifact
		if questions {
			questionsPath := filepath.Join(artifactRoot, "questions.md")
			if err := os.WriteFile(questionsPath, []byte("Which buyer?\n"), 0600); err != nil {
				t.Fatal(err)
			}
			outputs = append(outputs, evidence.Artifact{Name: "questions", Path: questionsPath})
		}
		manifest := evidence.AttemptManifest{
			SchemaVersion: evidence.SchemaVersion, RunID: runID, AttemptID: attemptID,
			Stage: stage, StageIndex: stageIndex, StartedAt: attemptStarted, FinishedAt: attemptFinished,
			Status: status, Execution: execution, Decision: decision, Outcome: outcome,
		}
		if err := runEvidence.PublishAttempt(manifest, artifactRoot, nil, outputs); err != nil {
			t.Fatal(err)
		}
		localManifest, err := os.ReadFile(filepath.Join(runEvidence.RunDir(), "attempts", attemptID, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(localManifest, &manifest); err != nil {
			t.Fatal(err)
		}
		if err := manifestAuthority.Write(runID, manifest); err != nil {
			t.Fatal(err)
		}
		digest, _, err := evidence.AttemptManifestDigest(evidence.ReservedAttemptManifestSource{TargetDir: target}, runEvidence.RunDir(), runID, attemptID)
		if err != nil {
			t.Fatal(err)
		}
		if err := runEvidence.Append(evidence.Event{
			Type: "attempt_finished", Stage: stage, AttemptID: attemptID, Timestamp: attemptFinished,
			Data: map[string]any{
				"status": status, "execution": execution, "decision": decision,
				"outcome": outcome, "manifest_sha256": digest,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendAttempt(sourceAttempt, "analyst", 1, startedAt.Add(time.Millisecond), questionFinished,
		"blocked", "succeeded", "blocked", "blocked", true)
	approvalValue := workerAnalystQuestionApproval(runID, approvalID, "Which buyer?", "B2B buyers", approval.StatusResolved)
	approvalValue.AttemptID = sourceAttempt
	approvalValue.CreatedAt = questionFinished
	approvalValue.ResolvedAt = answerResolvedAt
	approvalValue.Decisions[0].DecidedAt = answerResolvedAt
	approvalStore := &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: approvalValue}}
	if err := runEvidence.Append(evidence.Event{
		Type: "approval_requested", AttemptID: sourceAttempt, Timestamp: questionFinished.Add(time.Millisecond),
		Data: map[string]any{"approval_id": approvalID, "subject_hash": approvalValue.SubjectHash, "status": string(approval.StatusPending)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := runEvidence.Append(evidence.Event{
		Type: "approval_decided", AttemptID: sourceAttempt, Timestamp: answerResolvedAt,
		Data: map[string]any{
			"approval_id": approvalID, "subject_hash": approvalValue.SubjectHash, "status": string(approval.StatusResolved),
			"resolved_action": approvalValue.ResolvedAction, "from_stage": approvalValue.FromStage,
			"to_stage": approvalValue.ToStage, "trigger": approvalValue.Trigger,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := runEvidence.Append(evidence.Event{
		Type: "transition_selected", Stage: "analyst", AttemptID: sourceAttempt, Timestamp: answerResolvedAt.Add(time.Millisecond),
		Data: map[string]any{"from": "analyst", "outcome": "blocked", "edge_target": "analyst", "action": "answer_questions", "target": "analyst"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := runEvidence.Append(evidence.Event{Type: "attempts_invalidated", Timestamp: answerResolvedAt.Add(2 * time.Millisecond), Data: map[string]any{
		"attempt_ids": []string{sourceAttempt}, "reason": "approved_loopback",
	}}); err != nil {
		t.Fatal(err)
	}
	appendAttempt(targetAttempt, "analyst", 1, targetStartedAt, targetFinishedAt,
		"passed", "succeeded", "not_applicable", "passed", false)
	if err := runEvidence.Append(evidence.Event{
		Type: "transition_selected", Stage: "analyst", AttemptID: targetAttempt, Timestamp: targetFinishedAt.Add(time.Millisecond),
		Data: map[string]any{"from": "analyst", "outcome": "passed", "edge_target": "coder", "target": "coder"},
	}); err != nil {
		t.Fatal(err)
	}

	lifecycleStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state := lifecycle.State{
		RunID: runID, Feature: "stale-question-admission", TargetDir: target, Task: "test stale clarification recovery",
		Phase: phase, NextStage: "analyst", ConfigSHA256: strings.Repeat("a", 64),
		WorkflowSHA256: strings.Repeat("b", 64), CreatedAt: startedAt,
	}
	if phase == lifecycle.PhaseWaiting {
		state.PendingApprovalID = approvalID
	}
	if err := lifecycleStore.Create(state); err != nil {
		t.Fatal(err)
	}

	job := Job{SchemaVersion: SchemaVersion, Operation: OperationResume, RunID: runID, TargetDir: target,
		ExecutionID: strings.Repeat("d", ExecutionIDBytes*2)}
	socketDir, err := os.MkdirTemp("/tmp", "ai-q-answer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, fmt.Sprintf("controller-%d.sock", os.Getpid()))
	api, err := startWorkerAPIServerUnix(job, &apiRecorderSpy{}, approvalStore, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.close)
	api.lifecycle = lifecycleStore
	_, _, replayed, err := evidence.ResumeWithEventLog(
		filepath.Join(target, ".ai-team", "runs"), runID, api.eventLogs,
		evidence.ReservedAttemptManifestSource{TargetDir: target},
	)
	if err != nil {
		t.Fatalf("fixture must pass production evidence replay: %v", err)
	}
	return api, lifecycleStore, replayed
}
