package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestWorkerAPIRejectsWorkerHumanExecutorEvenWithSyntheticManifest(t *testing.T) {
	target := filepath.Clean(t.TempDir())
	const runID, attemptID = "worker-human-forgery", "attempt-human-forgery"
	job := workerHumanSkipJob(OperationStart, runID, target)
	server, err := startWorkerAPIServerUnix(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}},
		workerHumanSkipSocket("forgery"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()

	runStore := workerHumanSkipEvidence(t, target, runID, server.eventLogs)
	startedAt := time.Now().UTC().Add(time.Second)
	forgedStart := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: attemptID, Timestamp: startedAt,
		Data: map[string]any{
			"stage_index": 1, "executor": "human", "actor_id": "forged-user", "actor_role": "owner",
			"human_input_approval_id": "forged-approval", "stage_action": "skip",
			"stage_skip_version": evidence.StageSkipProtocolVersion,
		}}
	events, err := server.eventLogs.Read(runID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = server.dispatch("event_log.append", workerAPICall{RunID: runID, Event: forgedStart,
		ExpectedSequence: uint64(len(events)), ExpectedPreviousSHA256: events[len(events)-1].SHA256})
	if err == nil || !strings.Contains(err.Error(), "cannot claim human executor") {
		t.Fatalf("raw worker append accepted a forged human executor: %v", err)
	}
	if got, readErr := server.eventLogs.Read(runID); readErr != nil || len(got) != len(events) {
		t.Fatalf("rejected human start changed canonical journal: events=%d err=%v", len(got), readErr)
	}

	// Model a matching human start and skipped manifest already present in a
	// malicious worker-visible journal. Even then, the raw finish API must
	// reject executor=human before consulting that manifest.
	if err := runStore.Append(forgedStart); err != nil {
		t.Fatalf("seed forged controller fixture start: %v", err)
	}
	manifest := workerHumanSkipManifest(runID, attemptID, startedAt, startedAt.Add(time.Second),
		"forged-approval", "forged-user", "owner")
	manifestStore := evidence.ControllerAttemptManifestStore{TargetDir: target}
	if err := manifestStore.Write(runID, manifest); err != nil {
		t.Fatalf("write synthetic matching manifest: %v", err)
	}
	events, err = server.eventLogs.Read(runID)
	if err != nil {
		t.Fatal(err)
	}
	forgedFinish := evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID,
		Timestamp: manifest.FinishedAt, Data: map[string]any{
			"status": string(workflow.OutcomeSkipped), "execution": string(workflow.ExecutionSucceeded),
			"decision": string(workflow.DecisionNotApplicable), "outcome": string(workflow.OutcomeSkipped),
			"executor": "human", "actor_id": "forged-user", "actor_role": "owner",
			"human_input_approval_id": "forged-approval", "stage_skip_reason": "worker invented reason",
		}}
	_, err = server.dispatch("event_log.append", workerAPICall{RunID: runID, Event: forgedFinish,
		ExpectedSequence: uint64(len(events)), ExpectedPreviousSHA256: events[len(events)-1].SHA256})
	if err == nil || !strings.Contains(err.Error(), "cannot claim human executor") {
		t.Fatalf("raw worker append accepted synthetic human finish: %v", err)
	}
}

func TestWorkerAPIRepairsOnlyApprovalBackedHumanSkipBeforeResume(t *testing.T) {
	for _, tc := range []struct {
		name       string
		finishText string
		wantError  bool
	}{
		{name: "approved reason", finishText: "No document is needed."},
		{name: "forged reason", finishText: "worker supplied a different reason", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Clean(t.TempDir())
			const runID, attemptID, approvalID = "worker-human-recovery", "attempt-human-recovery", "input-human-recovery"
			value := workerHumanSkipApproval(runID, approvalID)
			approvals := &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}
			startServer, err := startWorkerAPIServerUnix(workerHumanSkipJob(OperationStart, runID, target),
				&apiRecorderSpy{}, approvals, workerHumanSkipSocket("start-"+tc.name))
			if err != nil {
				t.Fatal(err)
			}
			runStore := workerHumanSkipEvidence(t, target, runID, startServer.eventLogs)
			appendWorkerHumanApprovalEvents(t, runStore, value)
			startedAt := time.Now().UTC().Add(time.Second)
			finishedAt := startedAt.Add(time.Second)
			if err := runStore.Append(evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: attemptID,
				Timestamp: startedAt, Data: map[string]any{
					"stage_index": 1, "executor": "human", "actor_id": "alice", "actor_role": "product_owner",
					"human_input_approval_id": approvalID, "stage_action": "skip",
					"stage_skip_version": evidence.StageSkipProtocolVersion,
				}}); err != nil {
				t.Fatalf("append controller human start: %v", err)
			}
			manifest := workerHumanSkipManifest(runID, attemptID, startedAt, finishedAt, approvalID, "alice", "product_owner")
			if err := (evidence.ControllerAttemptManifestStore{TargetDir: target}).Write(runID, manifest); err != nil {
				t.Fatalf("write controller human manifest: %v", err)
			}
			manifestDigest, _, err := evidence.AttemptManifestDigest(
				evidence.ReservedAttemptManifestSource{TargetDir: target},
				filepath.Join(target, ".ai-team", "runs", runID), runID, attemptID)
			if err != nil {
				t.Fatalf("digest controller human manifest: %v", err)
			}
			if err := runStore.Append(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID,
				Timestamp: finishedAt, Data: map[string]any{
					"status": string(workflow.OutcomeSkipped), "execution": string(workflow.ExecutionSucceeded),
					"decision": string(workflow.DecisionNotApplicable), "outcome": string(workflow.OutcomeSkipped),
					"executor": "human", "actor_id": "alice", "actor_role": "product_owner",
					"human_input_approval_id": approvalID, "stage_skip_reason": tc.finishText,
					"manifest_sha256": manifestDigest,
				}}); err != nil {
				t.Fatalf("append durable human finish: %v", err)
			}
			startServer.close()

			resumeServer, err := startWorkerAPIServerUnix(workerHumanSkipJob(OperationResume, runID, target),
				&apiRecorderSpy{}, approvals, workerHumanSkipSocket("resume-"+tc.name))
			if tc.wantError {
				if err == nil {
					resumeServer.close()
					t.Fatal("worker-backed recovery accepted a reason that disagrees with the approved decision")
				}
				events, readErr := (evidence.ControllerEventStore{TargetDir: target}).Read(runID)
				if readErr != nil || hasWorkerHumanSkipWarning(events, attemptID) {
					t.Fatalf("failed authority check appended a skip warning: events=%+v err=%v", events, readErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resume did not repair approved human skip: %v", err)
			}
			defer resumeServer.close()
			events, err := resumeServer.eventLogs.Read(runID)
			if err != nil {
				t.Fatal(err)
			}
			if !hasWorkerHumanSkipWarning(events, attemptID) {
				t.Fatalf("controller did not append the missing human skip warning: %+v", events)
			}
			for _, event := range events {
				if event.Type == "stage_skipped" && event.AttemptID == attemptID {
					if event.Data["reason"] != tc.finishText || event.Data["actor_id"] != "alice" || event.Data["actor_role"] != "product_owner" {
						t.Fatalf("recovered warning did not preserve approval-backed reason/actor: %+v", event)
					}
				}
			}
			runDir := filepath.Join(target, ".ai-team", "runs", runID)
			if _, err := evidence.ReplayEventLogWithEventSourcesAndTarget(
				filepath.Join(runDir, "events.jsonl"), runID, resumeServer.eventLogs,
				evidence.ReservedAttemptManifestSource{TargetDir: target}, target); err != nil {
				t.Fatalf("repaired worker-backed journal does not pass strict replay: %v", err)
			}
		})
	}
}

func TestWorkerAPIHumanSkipAppendUsesApprovedDecisionAndControllerManifest(t *testing.T) {
	target := filepath.Clean(t.TempDir())
	const runID, attemptID, approvalID = "worker-human-genuine", "attempt-human-genuine", "input-human-genuine"
	value := workerHumanSkipApproval(runID, approvalID)
	approvals := &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}
	job := workerHumanSkipJob(OperationStart, runID, target)
	socket := workerHumanSkipSocket("genuine")
	server, err := startWorkerAPIServerUnix(job, &apiRecorderSpy{}, approvals, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	runStore := workerHumanSkipEvidence(t, target, runID, server.eventLogs)
	appendWorkerHumanApprovalEvents(t, runStore, value)

	t.Setenv(WorkerAPIAddressEnv, "http://unix")
	t.Setenv(WorkerAPISocketEnv, socket)
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	workerEvents := NewWorkerAPIEventLog(port)
	startedAt := time.Now().UTC().Add(time.Second)
	started, err := workerEvents.Append(runID, evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: attemptID,
		Timestamp: startedAt, Data: map[string]any{
			"stage_index": 1, "executor": "human", "actor_id": "worker-forgery", "actor_role": "forgery",
			"human_input_approval_id": approvalID, "stage_action": "approve",
		}}, 0, "ignored")
	if err != nil {
		t.Fatalf("genuine human stage start was not accepted through controller authority: %v", err)
	}
	if started.Data != nil {
		t.Fatalf("worker received unexpected event payload from append response: %+v", started)
	}
	finishedAt := startedAt.Add(time.Second)
	manifest := workerHumanSkipManifest(runID, attemptID, startedAt, finishedAt, approvalID, "alice", "product_owner")
	if err := server.attemptManifests.Write(runID, manifest); err != nil {
		t.Fatalf("write controller human manifest: %v", err)
	}
	// Even if the worker asks to finish with a fake actor, fake state and fake
	// reason, the API reconstructs the terminal record from the manifest and
	// approval.
	if _, err := workerEvents.Append(runID, evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID,
		Timestamp: finishedAt, Data: map[string]any{
			"executor": "human", "actor_id": "worker-forgery", "actor_role": "forgery",
			"human_input_approval_id": approvalID, "status": "failed", "execution": "infra_failed",
			"decision": "blocked", "outcome": "failed", "stage_skip_reason": "fake",
		}}, 0, "ignored"); err != nil {
		t.Fatalf("controller rejected valid approved human finish: %v", err)
	}
	if _, err := workerEvents.Append(runID, evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: attemptID,
		Timestamp: finishedAt, Data: map[string]any{"reason": "forged warning", "warning": true}}, 0, "ignored"); err != nil {
		t.Fatalf("controller rejected valid approved skip warning: %v", err)
	}
	events, err := server.eventLogs.Read(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "attempt_started" && event.AttemptID == attemptID {
			if event.Data["actor_id"] != "alice" || event.Data["actor_role"] != "product_owner" ||
				event.Data["stage_action"] != "skip" || event.Data["stage_skip_version"] != float64(evidence.StageSkipProtocolVersion) {
				t.Fatalf("controller did not canonicalize start from approval: %+v", event.Data)
			}
		}
		if event.Type == "attempt_finished" && event.AttemptID == attemptID {
			if event.Data["outcome"] != string(workflow.OutcomeSkipped) || event.Data["stage_skip_reason"] != "No document is needed." ||
				event.Data["actor_id"] != "alice" {
				t.Fatalf("controller did not canonicalize finish from approval/manifest: %+v", event.Data)
			}
		}
		if event.Type == "stage_skipped" && event.AttemptID == attemptID && event.Data["reason"] != "No document is needed." {
			t.Fatalf("worker warning payload was not replaced with approved reason: %+v", event.Data)
		}
	}
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	if _, err := evidence.ReplayEventLogWithEventSourcesAndTarget(filepath.Join(runDir, "events.jsonl"), runID,
		server.eventLogs, evidence.ReservedAttemptManifestSource{TargetDir: target}, target); err != nil {
		t.Fatalf("genuine controller human skip does not replay strictly: %v", err)
	}
}

func workerHumanSkipJob(operation Operation, runID, target string) Job {
	job := Job{SchemaVersion: SchemaVersion, Operation: operation, RunID: runID, TargetDir: target,
		ExecutionID: strings.Repeat("a", ExecutionIDBytes*2)}
	if operation == OperationStart || operation == OperationRecover {
		job.Feature, job.Task = "human-skip", "Exercise approved human skip"
	}
	return job
}

func workerHumanSkipSocket(label string) string {
	return filepath.Join(string(filepath.Separator)+"tmp", fmt.Sprintf("ai-team-human-skip-%d-%s.sock", os.Getpid(), strings.ReplaceAll(label, " ", "-")))
}

func workerHumanSkipEvidence(t *testing.T, target, runID string, events evidence.EventLog) *evidence.Store {
	t.Helper()
	store, err := evidence.StartWithEventLog(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "human-skip", TargetDir: target, StartedAt: time.Now().UTC(),
		ConfigSnapshot: json.RawMessage(`{}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	}, events)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "run_started", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatalf("append run_started: %v", err)
	}
	return store
}

func workerHumanSkipApproval(runID, approvalID string) approval.PendingApproval {
	decisionAt := time.Now().UTC().Add(-time.Minute)
	decision := approval.Decision{ApprovalID: approvalID, ActorID: "alice", ActorRole: "product_owner",
		Action: "skip", Comment: "No document is needed.", SubjectHash: strings.Repeat("d", 64), DecidedAt: decisionAt}
	payload, _ := json.Marshal(approval.InputPayload{Kind: string(approval.KindInput), StageID: "optional", Result: "md",
		OutputName: "optional", OutputPath: "tasks/human-skip/optional.md"})
	return approval.PendingApproval{
		SchemaVersion: approval.SchemaVersion, Kind: approval.KindInput, ID: approvalID, RunID: runID,
		AttemptID: "attempt-input-pending", FromStage: "optional", ToStage: "optional", Trigger: workerHumanInputTrigger,
		SubjectHash: decision.SubjectHash, RequiredRoles: []string{"product_owner"}, Quorum: approval.QuorumAny,
		Actions: []string{"submit", "skip"}, Targets: map[string]string{"submit": "optional", "skip": "optional"},
		Status: approval.StatusResolved, Decisions: []approval.Decision{decision}, ResolvedAction: "skip",
		CreatedAt: decisionAt.Add(-time.Minute), ResolvedAt: decisionAt, Payload: payload,
	}
}

func workerHumanSkipManifest(runID, attemptID string, startedAt, finishedAt time.Time, approvalID, actorID, actorRole string) evidence.AttemptManifest {
	return evidence.AttemptManifest{
		SchemaVersion: evidence.SchemaVersion, RunID: runID, AttemptID: attemptID, Stage: "optional",
		Executor: "human", ActorID: actorID, ActorRole: actorRole, HumanInputApprovalID: approvalID,
		StageIndex: 1, TotalStages: 1, StartedAt: startedAt, FinishedAt: finishedAt,
		Status: string(workflow.OutcomeSkipped), Execution: string(workflow.ExecutionSucceeded),
		Decision: string(workflow.DecisionNotApplicable), Outcome: string(workflow.OutcomeSkipped),
	}
}

func appendWorkerHumanApprovalEvents(t *testing.T, store *evidence.Store, value approval.PendingApproval) {
	t.Helper()
	appendEvent := func(event evidence.Event) {
		t.Helper()
		encoded, err := json.Marshal(event.Data)
		if err != nil {
			t.Fatal(err)
		}
		var normalized map[string]any
		if err := json.Unmarshal(encoded, &normalized); err != nil {
			t.Fatal(err)
		}
		event.Data = normalized
		if err := store.Append(event); err != nil {
			t.Fatalf("append %s: %v", event.Type, err)
		}
	}
	requested := map[string]any{
		"approval_id": value.ID, "kind": string(value.Kind), "subject_hash": value.SubjectHash,
		"from_stage": value.FromStage, "to_stage": value.ToStage, "trigger": value.Trigger,
		"status": string(approval.StatusPending),
	}
	appendEvent(evidence.Event{Type: "approval_requested", AttemptID: value.AttemptID, Timestamp: value.CreatedAt, Data: requested})
	decided := map[string]any{
		"approval_id": value.ID, "kind": string(value.Kind), "subject_hash": value.SubjectHash,
		"from_stage": value.FromStage, "to_stage": value.ToStage, "trigger": value.Trigger,
		"status": string(approval.StatusResolved), "resolved_action": value.ResolvedAction,
		"decisions": value.Decisions,
	}
	appendEvent(evidence.Event{Type: "approval_decided", AttemptID: value.AttemptID, Timestamp: value.ResolvedAt, Data: decided})
}

func hasWorkerHumanSkipWarning(events []evidence.Event, attemptID string) bool {
	for _, event := range events {
		if event.Type == "stage_skipped" && event.AttemptID == attemptID {
			return true
		}
	}
	return false
}
