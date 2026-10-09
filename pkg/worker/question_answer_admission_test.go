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

func TestPrepareQuestionAnswerMountSkipsWaitingStateWithoutRequestableAnswer(t *testing.T) {
	t.Run("another next stage", func(t *testing.T) {
		api, store, _ := newStaleQuestionAdmissionFixture(t, lifecycle.PhaseWaiting)
		state, err := store.Load(api.scope.RunID)
		if err != nil {
			t.Fatal(err)
		}
		next := state
		next.NextStage = "coder"
		if err := store.Save(state, next); err != nil {
			t.Fatal(err)
		}
		if mount, err := api.prepareQuestionAnswerMount(context.Background()); mount != nil || err == nil || !strings.Contains(err.Error(), "stage does not match") {
			t.Fatalf("waiting approval bound to another stage must fail closed: mount=%+v err=%v", mount, err)
		}
	})

	t.Run("approval no longer pending", func(t *testing.T) {
		api, _, _ := newStaleQuestionAdmissionFixture(t, lifecycle.PhaseWaiting)
		store := api.approvals.(*apiApprovalStore)
		for key, value := range store.values {
			value.Status = approval.StatusPending
			store.values[key] = value
		}
		if mount, err := api.prepareQuestionAnswerMount(context.Background()); mount != nil || err != nil {
			t.Fatalf("unresolved approval should not materialize an answer: mount=%+v err=%v", mount, err)
		}
	})

	t.Run("run replay failure", func(t *testing.T) {
		api, _, _ := newStaleQuestionAdmissionFixture(t, lifecycle.PhaseRunning)
		replayErr := errors.New("controller event replay unavailable")
		api.eventLogs = questionAnswerEventLog{err: replayErr}
		if mount, err := api.prepareQuestionAnswerMount(context.Background()); mount != nil || !errors.Is(err, replayErr) {
			t.Fatalf("broken controller event authority must fail before admission: mount=%+v err=%v", mount, err)
		}
	})

	t.Run("running checkpoint advanced beyond analyst", func(t *testing.T) {
		api, store, _ := newQuestionAnswerAdmissionFixture(t, false, true)
		state, err := store.Load(api.scope.RunID)
		if err != nil {
			t.Fatal(err)
		}
		next := state
		next.NextStage = "coder"
		if err := store.Save(state, next); err != nil {
			t.Fatal(err)
		}
		if mount, err := api.prepareQuestionAnswerMount(context.Background()); mount != nil || err != nil {
			t.Fatalf("advanced lifecycle must not materialize a stale clarification: mount=%+v err=%v", mount, err)
		}
	})

	t.Run("pending approval authority failure", func(t *testing.T) {
		api, _, _ := newStaleQuestionAdmissionFixture(t, lifecycle.PhaseWaiting)
		loadErr := errors.New("approval authority unavailable")
		api.approvals.(*apiApprovalStore).loadErr = loadErr
		if mount, err := api.prepareQuestionAnswerMount(context.Background()); mount != nil || !errors.Is(err, loadErr) {
			t.Fatalf("approval authority failure must block admission: mount=%+v err=%v", mount, err)
		}
	})

	t.Run("approval request event is missing", func(t *testing.T) {
		api, _, _ := newQuestionAnswerAdmissionFixture(t, false, true)
		api.eventLogs = &questionAnswerEventLogDropOnSecondRead{EventLog: api.eventLogs}
		if mount, err := api.prepareQuestionAnswerMount(context.Background()); mount != nil || err == nil || !strings.Contains(err.Error(), "no matching controller event request") {
			t.Fatalf("approval without its controller request event was admitted: mount=%+v err=%v", mount, err)
		}
	})
}

func TestPrepareQuestionAnswerMountAdmitsVerifiedIncompleteClarification(t *testing.T) {
	api, _, _ := newQuestionAnswerAdmissionFixture(t, false, true)
	mount, err := api.prepareQuestionAnswerMount(context.Background())
	if err != nil || mount == nil {
		t.Fatalf("verified answer for an incomplete analyst target should be admitted: mount=%+v err=%v", mount, err)
	}
	if api.questionAnswerID != "approval-old-clarification" || api.questionAnswerPath != mount.TargetPath {
		t.Fatalf("admitted answer identity was not retained for the scoped API: id=%q path=%q mount=%+v", api.questionAnswerID, api.questionAnswerPath, mount)
	}
	if err := validateQuestionAnswerMount(api.scope.TargetDir, api.scope.RunID, *mount); err != nil {
		t.Fatalf("admitted controller projection should pass mount validation: %v", err)
	}
	canonical, err := api.questionAnswerStore.Read(api.scope.RunID, api.questionAnswerID)
	if err != nil || !strings.Contains(string(canonical), "B2B buyers") {
		t.Fatalf("admission should expose only the durable controller answer: %q err=%v", canonical, err)
	}
}

func TestPrepareQuestionAnswerMountAdmitsNonAnalystQuestionCycle(t *testing.T) {
	for _, phase := range []lifecycle.Phase{lifecycle.PhaseWaiting, lifecycle.PhaseRunning, lifecycle.PhaseResumable} {
		t.Run(string(phase), func(t *testing.T) {
			api, _, replayed := newQuestionAnswerAdmissionFixtureForStage(t, phase, false, true, "questioner")
			if phase != lifecycle.PhaseWaiting {
				selected, err := pipeline.RecoveredQuestionApproval(api.approvals, api.scope.RunID, "questioner", replayed)
				if err != nil || selected == nil || selected.FromStage != "questioner" || selected.ToStage != "questioner" {
					t.Fatalf("strict recovery did not select the stage-bound answer: approval=%+v err=%v", selected, err)
				}
			}
			mount, err := api.prepareQuestionAnswerMount(context.Background())
			if err != nil || mount == nil {
				t.Fatalf("verified questioner clarification should be admitted: mount=%+v err=%v", mount, err)
			}
			if err := validateQuestionAnswerMount(api.scope.TargetDir, api.scope.RunID, *mount); err != nil {
				t.Fatalf("questioner answer projection failed mount validation: %v", err)
			}
			canonical, err := api.questionAnswerStore.Read(api.scope.RunID, api.questionAnswerID)
			if err != nil || !strings.Contains(string(canonical), "B2B buyers") {
				t.Fatalf("questioner mount did not expose the durable controller answer: %q err=%v", canonical, err)
			}
		})
	}
}

type questionAnswerEventLogDropOnSecondRead struct {
	evidence.EventLog
	reads int
}

func (l *questionAnswerEventLogDropOnSecondRead) Read(runID string) ([]evidence.Event, error) {
	events, err := l.EventLog.Read(runID)
	if err != nil {
		return nil, err
	}
	l.reads++
	if l.reads < 2 {
		return events, nil
	}
	filtered := events[:0]
	for _, event := range events {
		if event.Type != "approval_requested" {
			filtered = append(filtered, event)
		}
	}
	return filtered, nil
}

func (l *questionAnswerEventLogDropOnSecondRead) Close() error {
	if closer, ok := l.EventLog.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func TestPrepareQuestionAnswerMountFailsClosedBeforeReturningUntrustedInputs(t *testing.T) {
	t.Run("candidate metadata is unavailable", func(t *testing.T) {
		api, _, _ := newQuestionAnswerAdmissionFixture(t, false, true)
		store := api.approvals.(*apiApprovalStore)
		for key, value := range store.values {
			value.CandidateSHA256 = strings.Repeat("a", 64)
			store.values[key] = value
		}
		if mount, err := api.prepareQuestionAnswerMount(context.Background()); mount != nil || err == nil || !strings.Contains(err.Error(), "load clarification candidate identity") {
			t.Fatalf("answer with unavailable candidate authority was admitted: mount=%+v err=%v", mount, err)
		}
	})

	t.Run("source attempt did not publish questions", func(t *testing.T) {
		api, _, _ := newQuestionAnswerAdmissionFixture(t, false, false)
		if mount, err := api.prepareQuestionAnswerMount(context.Background()); mount != nil || err == nil || !strings.Contains(err.Error(), "no questions output") {
			t.Fatalf("approval without its question artifact was admitted: mount=%+v err=%v", mount, err)
		}
	})

	t.Run("canonical answer store is redirected", func(t *testing.T) {
		api, _, _ := newQuestionAnswerAdmissionFixture(t, false, true)
		root := filepath.Join(api.scope.TargetDir, ".ai-team", "state", "handoff-inputs")
		if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.Symlink(outside, root); err != nil {
			t.Fatal(err)
		}
		if mount, err := api.prepareQuestionAnswerMount(context.Background()); mount != nil || err == nil || !strings.Contains(err.Error(), "prepare canonical clarification answer") {
			t.Fatalf("redirected answer authority was accepted: mount=%+v err=%v", mount, err)
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Fatalf("failed admission wrote through the redirected authority: entries=%v err=%v", entries, err)
		}
	})

	t.Run("existing symlink mountpoint is preserved and rejected", func(t *testing.T) {
		api, _, _ := newQuestionAnswerAdmissionFixture(t, false, true)
		destination, err := pipeline.QuestionAnswerMaterializationPath(api.scope.TargetDir, api.scope.RunID, "approval-old-clarification")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(t.TempDir(), "answer.md")
		if err := os.WriteFile(sentinel, []byte("sentinel"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, destination); err != nil {
			t.Fatal(err)
		}
		if mount, err := api.prepareQuestionAnswerMount(context.Background()); mount != nil || err == nil {
			t.Fatalf("pre-existing symlink mountpoint was admitted: mount=%+v err=%v", mount, err)
		}
		info, err := os.Lstat(destination)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("failed admission replaced the pre-existing symlink: info=%v err=%v", info, err)
		}
	})
}

// newStaleQuestionAdmissionFixture creates the production controller-owned
// event/manifest authority and a lifecycle checkpoint left on analyst after a
// successful target analyst attempt and its outgoing graph transition. This
// is the crash boundary before Pipeline can persist NextStage=coder.
func newStaleQuestionAdmissionFixture(t *testing.T, phase lifecycle.Phase) (*workerAPIServer, *lifecycle.Store, evidence.ReplayedRun) {
	return newQuestionAnswerAdmissionFixtureForPhase(t, phase, true, true)
}

func newQuestionAnswerAdmissionFixture(t *testing.T, targetAttemptCompleted, sourceHasQuestions bool) (*workerAPIServer, *lifecycle.Store, evidence.ReplayedRun) {
	return newQuestionAnswerAdmissionFixtureForPhase(t, lifecycle.PhaseRunning, targetAttemptCompleted, sourceHasQuestions)
}

func newQuestionAnswerAdmissionFixtureForPhase(t *testing.T, phase lifecycle.Phase, targetAttemptCompleted, sourceHasQuestions bool) (*workerAPIServer, *lifecycle.Store, evidence.ReplayedRun) {
	return newQuestionAnswerAdmissionFixtureForStage(t, phase, targetAttemptCompleted, sourceHasQuestions, "analyst")
}

func newQuestionAnswerAdmissionFixtureForStage(t *testing.T, phase lifecycle.Phase, targetAttemptCompleted, sourceHasQuestions bool, stage string) (*workerAPIServer, *lifecycle.Store, evidence.ReplayedRun) {
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
	appendAttempt(sourceAttempt, stage, 1, startedAt.Add(time.Millisecond), questionFinished,
		"blocked", "succeeded", "blocked", "blocked", sourceHasQuestions)
	approvalValue := workerAnalystQuestionApproval(runID, approvalID, "Which buyer?", "B2B buyers", approval.StatusResolved)
	approvalValue.AttemptID = sourceAttempt
	approvalValue.FromStage = stage
	approvalValue.ToStage = stage
	approvalValue.Targets["answer_questions"] = stage
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
		Type: "transition_selected", Stage: stage, AttemptID: sourceAttempt, Timestamp: answerResolvedAt.Add(time.Millisecond),
		Data: map[string]any{"from": stage, "outcome": "blocked", "edge_target": stage, "action": "answer_questions", "target": stage},
	}); err != nil {
		t.Fatal(err)
	}
	if err := runEvidence.Append(evidence.Event{Type: "attempts_invalidated", Timestamp: answerResolvedAt.Add(2 * time.Millisecond), Data: map[string]any{
		"attempt_ids": []string{sourceAttempt}, "reason": "approved_loopback",
	}}); err != nil {
		t.Fatal(err)
	}
	if targetAttemptCompleted {
		appendAttempt(targetAttempt, stage, 1, targetStartedAt, targetFinishedAt,
			"passed", "succeeded", "not_applicable", "passed", false)
		if err := runEvidence.Append(evidence.Event{
			Type: "transition_selected", Stage: stage, AttemptID: targetAttempt, Timestamp: targetFinishedAt.Add(time.Millisecond),
			Data: map[string]any{"from": stage, "outcome": "passed", "edge_target": "coder", "target": "coder"},
		}); err != nil {
			t.Fatal(err)
		}
	} else if phase != lifecycle.PhaseWaiting {
		if err := runEvidence.Append(evidence.Event{
			Type: "attempt_started", Stage: stage, AttemptID: targetAttempt, Timestamp: targetStartedAt,
			Data: map[string]any{"stage_index": 1},
		}); err != nil {
			t.Fatal(err)
		}
	}

	lifecycleStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state := lifecycle.State{
		RunID: runID, Feature: "stale-question-admission", TargetDir: target, Task: "test stale clarification recovery",
		Phase: phase, NextStage: stage, ConfigSHA256: strings.Repeat("a", 64),
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
