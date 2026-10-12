package evidence

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestValidateEventAppendAllowsPendingAgentFinishedForCandidateAttemptFinished(t *testing.T) {
	const runID = "append-pending-agent-finished"
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	finished := started.Add(time.Second)
	for _, event := range []Event{
		{Type: "run_started", Timestamp: started},
		{Type: "attempt_started", Stage: "analyst", AttemptID: "agent-attempt-1", Timestamp: started,
			Data: map[string]any{"stage_index": 1, "executor": "agent"}},
		{Type: "agent_started", Stage: "analyst", AttemptID: "agent-attempt-1", Timestamp: started},
	} {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PublishAttempt(AttemptManifest{
		RunID: runID, AttemptID: "agent-attempt-1", Stage: "analyst", Executor: "agent",
		StageIndex: 1, TotalStages: 1, StartedAt: started, FinishedAt: finished,
		Status: string(workflow.OutcomePassed), Execution: string(workflow.ExecutionSucceeded),
		Decision: string(workflow.DecisionApproved), Outcome: string(workflow.OutcomePassed),
	}, target, nil, nil); err != nil {
		t.Fatal(err)
	}
	manifestDigest, _, err := AttemptManifestDigest(nil, store.RunDir(), runID, "agent-attempt-1")
	if err != nil {
		t.Fatal(err)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	candidate := Event{
		Type: "attempt_finished", Stage: "analyst", AttemptID: "agent-attempt-1", Timestamp: finished,
		Data: map[string]any{
			"status": string(workflow.OutcomePassed), "execution": string(workflow.ExecutionSucceeded),
			"decision": string(workflow.DecisionApproved), "outcome": string(workflow.OutcomePassed),
			"manifest_sha256": manifestDigest,
		},
	}
	appended, exactRetry, err := ValidateEventAppend(events, runID, store.RunDir(), candidate,
		uint64(len(events)), events[len(events)-1].SHA256, FilesystemAttemptManifestSource())
	if err != nil {
		t.Fatalf("candidate attempt_finished with its matching agent_finished still pending was rejected: %v", err)
	}
	if exactRetry || appended.Type != "attempt_finished" || appended.AttemptID != candidate.AttemptID || appended.Sequence != uint64(len(events)+1) {
		t.Fatalf("unexpected validated append: event=%+v exactRetry=%v", appended, exactRetry)
	}
}
