package evidence

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestReplayRejectsFinishedAgentAttemptWithoutAgentFinished(t *testing.T) {
	const runID = "agent-finished-missing"
	store, err := Start(filepath.Join(t.TempDir(), "runs"), testRunManifest(runID))
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
		{Type: "attempt_finished", Stage: "analyst", AttemptID: "agent-attempt-1", Timestamp: finished,
			Data: map[string]any{
				"status": string(workflow.OutcomeFailed), "execution": string(workflow.ExecutionInfraFailed),
				"decision": string(workflow.DecisionNotApplicable), "outcome": string(workflow.OutcomeFailed),
				"error": "runtime failed",
			}},
	} {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	_, err = ReplayEventLog(filepath.Join(store.RunDir(), "events.jsonl"), runID)
	if err == nil || !strings.Contains(err.Error(), "missing agent_finished") {
		t.Fatalf("replay error = %v; want a missing agent_finished error", err)
	}
}
