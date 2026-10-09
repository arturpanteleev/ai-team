//go:build linux

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const questionAnswerProbeEnv = "AI_TEAM_QUESTION_ANSWER_PROBE"

type questionAnswerProbeReport struct {
	CanonicalHidden       bool `json:"canonical_hidden"`
	InputReadable         bool `json:"input_readable"`
	InputWriteBlocked     bool `json:"input_write_blocked"`
	InputRemountSucceeded bool `json:"input_remount_succeeded"`
	WorkspaceReadable     bool `json:"workspace_readable"`
	WorkspaceWritable     bool `json:"workspace_writable"`
	ScopedAPIReadable     bool `json:"scoped_api_readable"`
}

func TestBubblewrapQuestionAnswerInputBoundary(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatal("Linux CI must install bubblewrap before running worker sandbox tests:", err)
	}
	target := t.TempDir()
	runID, approvalID := "question-input-probe", "approval-question-probe"
	job := Job{
		SchemaVersion: SchemaVersion, Operation: OperationRecover, RunID: runID, TargetDir: target,
		Feature: "probe", Task: "boundary probe", ExecutionID: strings.Repeat("c", ExecutionIDBytes*2),
	}
	if err := (evidence.ControllerEventStore{TargetDir: target}).Reserve(runID); err != nil {
		t.Fatal(err)
	}
	manifestAuthority := evidence.ControllerAttemptManifestStore{TargetDir: target}
	if err := manifestAuthority.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	approvalStore, err := approval.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	events := evidence.ControllerEventStore{TargetDir: target}
	runStarted := time.Now().UTC().Add(-time.Minute)
	started, finished := runStarted.Add(time.Second), runStarted.Add(2*time.Second)
	runEvidence, err := evidence.StartWithEventLog(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "question-answer-probe", TargetDir: target, StartedAt: runStarted,
		ConfigSnapshot: json.RawMessage(`{}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	}, events)
	if err != nil {
		t.Fatal(err)
	}
	if err := runEvidence.Append(evidence.Event{Type: "run_started", Timestamp: runStarted}); err != nil {
		t.Fatal(err)
	}
	const sourceAttemptID = "attempt-question-source"
	if err := runEvidence.Append(evidence.Event{
		Type: "attempt_started", Stage: "analyst", AttemptID: sourceAttemptID, Timestamp: started,
		Data: map[string]any{"stage_index": 1},
	}); err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(runEvidence.RunDir(), "artifacts")
	if err := os.MkdirAll(artifactRoot, 0700); err != nil {
		t.Fatal(err)
	}
	questionsPath := filepath.Join(artifactRoot, "questions.md")
	if err := os.WriteFile(questionsPath, []byte("Which buyer?\n"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked := workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionBlocked, Outcome: workflow.OutcomeBlocked}
	manifest := evidence.AttemptManifest{
		SchemaVersion: evidence.SchemaVersion, RunID: runID, AttemptID: sourceAttemptID,
		Stage: "analyst", StageIndex: 1, StartedAt: started, FinishedAt: finished,
		Status: blocked.LegacyStatus(), Execution: string(blocked.Execution), Decision: string(blocked.Decision), Outcome: string(blocked.Outcome),
	}
	if err := runEvidence.PublishAttempt(manifest, artifactRoot, nil, []evidence.Artifact{{Name: "questions", Path: questionsPath}}); err != nil {
		t.Fatal(err)
	}
	localManifestPath := filepath.Join(runEvidence.RunDir(), "attempts", sourceAttemptID, "manifest.json")
	localManifest, err := os.ReadFile(localManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(localManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := manifestAuthority.Write(runID, manifest); err != nil {
		t.Fatal(err)
	}
	digest, _, err := evidence.AttemptManifestDigest(evidence.ReservedAttemptManifestSource{TargetDir: target}, runEvidence.RunDir(), runID, sourceAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runEvidence.Append(evidence.Event{
		Type: "attempt_finished", Stage: "analyst", AttemptID: sourceAttemptID, Timestamp: finished,
		Data: map[string]any{
			"status": blocked.LegacyStatus(), "execution": string(blocked.Execution), "decision": string(blocked.Decision),
			"outcome": string(blocked.Outcome), "manifest_sha256": digest,
		},
	}); err != nil {
		t.Fatal(err)
	}

	pending := workerAnalystQuestionApproval(runID, approvalID, "Which buyer?", "B2B buyer", approval.StatusPending)
	pending.AttemptID = sourceAttemptID
	pending.CreatedAt = finished
	created, err := approvalStore.Create(pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := runEvidence.Append(evidence.Event{
		Type: "approval_requested", AttemptID: sourceAttemptID, Timestamp: finished.Add(time.Second),
		Data: map[string]any{
			"approval_id": created.ID, "subject_hash": created.SubjectHash, "status": string(approval.StatusPending),
			"from_stage": created.FromStage, "to_stage": created.ToStage, "trigger": created.Trigger,
		},
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := approvalStore.Decide(runID, approvalID, approval.Decision{
		ActorID: "qa@example.com", ActorRole: "qa", Action: "answer_questions",
		Comment: "B2B buyer", SubjectHash: created.SubjectHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runEvidence.Append(evidence.Event{
		Type: "approval_decided", AttemptID: sourceAttemptID, Timestamp: resolved.ResolvedAt,
		Data: map[string]any{
			"approval_id": resolved.ID, "subject_hash": resolved.SubjectHash, "status": string(approval.StatusResolved),
			"resolved_action": resolved.ResolvedAction, "from_stage": resolved.FromStage,
			"to_stage": resolved.ToStage, "trigger": resolved.Trigger,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := runEvidence.Append(evidence.Event{
		Type: "transition_selected", Stage: "analyst", AttemptID: sourceAttemptID,
		Timestamp: resolved.ResolvedAt.Add(time.Millisecond),
		Data: map[string]any{
			"from": "analyst", "outcome": "blocked", "edge_target": "analyst",
			"action": "answer_questions", "target": "analyst",
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Model a crash after the target analyst started but before it recorded a
	// successful finish. Recovery may reuse this same durable decision.
	if err := runEvidence.Append(evidence.Event{
		Type: "attempt_started", Stage: "analyst", AttemptID: "attempt-analyst-target-incomplete",
		Timestamp: resolved.ResolvedAt.Add(time.Second), Data: map[string]any{"stage_index": 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := lifecycleStore.Create(lifecycle.State{
		RunID: runID, Feature: "question-answer-probe", TargetDir: target, Task: job.Task,
		Phase: lifecycle.PhaseRunning, NextStage: "analyst", AttemptOrdinal: 2,
		ConfigSHA256: strings.Repeat("a", 64), WorkflowSHA256: strings.Repeat("b", 64), CreatedAt: runStarted,
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate the durable, exact target-side answer file left by a previous
	// invocation that crashed before the analyst target completed.
	canonicalBytes, err := pipeline.CanonicalQuestionAnswerContent(resolved)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := pipeline.QuestionAnswerMaterializationPath(target, runID, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, canonicalBytes, 0444); err != nil {
		t.Fatal(err)
	}

	home, temp := t.TempDir(), t.TempDir()
	apiSocketDir, err := createControllerWorkerSocketDir(target, home, temp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(apiSocketDir) }()
	api, err := startWorkerAPIServerUnixForTask(job, &apiRecorderSpy{}, approvalStore, filepath.Join(apiSocketDir, "controller-api.sock"), job.Task)
	if err != nil {
		t.Fatal(err)
	}
	defer api.close()
	api.lifecycle = lifecycleStore
	mount, err := api.prepareQuestionAnswerMount(context.Background())
	if err != nil || mount == nil {
		t.Fatalf("production admission rejected the exact crash-left answer: mount=%+v err=%v", mount, err)
	}
	if err := validateQuestionAnswerMount(target, runID, *mount); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "visible.txt"), []byte("workspace-visible"), 0600); err != nil {
		t.Fatal(err)
	}
	controlDir := filepath.Join(target, ".ai-team", "controller")
	if err := os.MkdirAll(controlDir, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(controlDir, "controller.db")
	if err := os.WriteFile(dbPath, []byte("controller-db"), 0600); err != nil {
		t.Fatal(err)
	}
	probePath := filepath.Join(target, "question-answer-probe.json")
	worker := exec.Command(os.Args[0], "-test.run=^TestBubblewrapQuestionAnswerProbeHelper$", "--",
		"--probe-target", target, "--probe-canonical", mount.SourcePath, "--probe-input", mount.TargetPath,
		"--probe-approval", approvalID, "--probe-output", probePath)
	environment := []string{
		"HOME=" + home, "TMPDIR=" + temp,
		workerAPIAddressEnv + "=http://unix", workerAPISocketEnv + "=" + api.socketPath,
		workerAPITokenEnv + "=" + api.token, questionAnswerProbeEnv + "=1",
	}
	jobBytes, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	worker.Env = environment
	wrapped, err := bubblewrapWorkerCommandWithInputs(context.Background(), worker, target, dbPath, runID, nil, environment, []workerReadOnlyInputMount{*mount})
	if err != nil {
		t.Fatal(err)
	}
	wrapped.Stdin = bytes.NewReader(jobBytes)
	wrapped.Env = environment
	if output, err := wrapped.CombinedOutput(); err != nil {
		t.Fatalf("sandboxed clarification boundary probe failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatal(err)
	}
	var report questionAnswerProbeReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("invalid question answer probe report %q: %v", data, err)
	}
	if !report.CanonicalHidden || !report.InputReadable || !report.InputWriteBlocked || report.InputRemountSucceeded ||
		!report.WorkspaceReadable || !report.WorkspaceWritable || !report.ScopedAPIReadable {
		t.Fatalf("clarification input namespace boundary failed: %+v", report)
	}
	stored, err := (pipeline.ControllerQuestionAnswerStore{TargetDir: target}).Read(runID, approvalID)
	if err != nil || string(stored) != string(canonicalBytes) {
		t.Fatalf("canonical controller answer changed: data=%q err=%v", stored, err)
	}
	projection, err := os.ReadFile(destination)
	if err != nil || string(projection) != string(canonicalBytes) {
		t.Fatalf("crash-left projection changed after read-only overlay: data=%q err=%v", projection, err)
	}
}

func TestBubblewrapQuestionAnswerProbeHelper(t *testing.T) {
	if os.Getenv(questionAnswerProbeEnv) != "1" {
		return
	}
	args := argsAfterDoubleDash(os.Args)
	value := func(name string) string {
		for index := 0; index+1 < len(args); index++ {
			if args[index] == name {
				return args[index+1]
			}
		}
		return ""
	}
	job, err := DecodeJob(os.Stdin, value("--probe-target"))
	if err != nil {
		t.Fatalf("decode question answer probe job: %v", err)
	}
	_, canonicalErr := os.ReadFile(value("--probe-canonical"))
	port, apiErr := NewWorkerAPIPort(job)
	var answerPath string
	var input []byte
	var inputErr error
	if apiErr == nil {
		artifact, materializeErr := NewWorkerAPIQuestionAnswerInputs(port).MaterializeQuestionAnswer(job.RunID, value("--probe-approval"))
		if materializeErr != nil {
			apiErr = materializeErr
		} else {
			answerPath = artifact.Path
			input, inputErr = os.ReadFile(answerPath)
		}
	}
	remountErr := syscall.Mount("", answerPath, "", syscall.MS_BIND|syscall.MS_REMOUNT, "rw")
	var writeInputErr error
	if remountErr != nil {
		writeInputErr = os.WriteFile(answerPath, []byte("forged-answer"), 0600)
	}
	visible, visibleErr := os.ReadFile(filepath.Join(job.TargetDir, "visible.txt"))
	workspaceWriteErr := os.WriteFile(filepath.Join(job.TargetDir, "worker-write.txt"), []byte("workspace-write"), 0600)
	if apiErr == nil {
		apiErr = port.call("approval.list", workerAPICall{RunID: job.RunID}, new([]approval.PendingApproval))
	}
	report := questionAnswerProbeReport{
		CanonicalHidden:       errors.Is(canonicalErr, os.ErrNotExist),
		InputReadable:         inputErr == nil && string(input) == "# Ответ на вопросы\n\nB2B buyer\n",
		InputWriteBlocked:     remountErr != nil && (errors.Is(writeInputErr, os.ErrPermission) || errors.Is(writeInputErr, syscall.EROFS)),
		InputRemountSucceeded: remountErr == nil,
		WorkspaceReadable:     visibleErr == nil && string(visible) == "workspace-visible",
		WorkspaceWritable:     workspaceWriteErr == nil,
		ScopedAPIReadable:     apiErr == nil,
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(value("--probe-output"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
