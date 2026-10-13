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

func TestResumeRepairsAgentFinishedAfterAttemptFinishedCrash(t *testing.T) {
	const runID = "agent-finished-crash-recovery"
	store, err := Start(filepath.Join(t.TempDir(), "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	finished := started.Add(time.Second)
	for _, event := range []Event{
		{Type: "run_started", Timestamp: started},
		{Type: "attempt_started", Stage: "product_spec", AttemptID: "agent-attempt-1", Timestamp: started,
			Data: map[string]any{"stage_index": 1, "executor": "agent"}},
		{Type: "agent_started", Stage: "product_spec", AttemptID: "agent-attempt-1", Timestamp: started,
			Data: map[string]any{"action": "run_agent", "approval_id": "approval-1"}},
		{Type: "attempt_finished", Stage: "product_spec", AttemptID: "agent-attempt-1", Timestamp: finished,
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

	// Resume verification must reconcile the durable attempt completion before
	// strict replay rejects the missing agent_finished event.
	if err := VerifyResumeEvidence(store.RunDir()); err != nil {
		t.Fatalf("verify resumable crash window: %v", err)
	}
	events, err := VerifyEventLog(filepath.Join(store.RunDir(), "events.jsonl"), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("recovered event count=%d; want one appended agent_finished: %v", len(events), events)
	}
	recovered := events[4]
	if recovered.Type != "agent_finished" || recovered.AttemptID != "agent-attempt-1" || recovered.Stage != "product_spec" ||
		!recovered.Timestamp.Equal(finished) || recovered.Data["status"] != string(workflow.OutcomeFailed) ||
		recovered.Data["action"] != "run_agent" || recovered.Data["approval_id"] != "approval-1" || recovered.Data["error"] != "runtime failed" {
		t.Fatalf("reconstructed agent_finished lost its durable action/completion context: %+v", recovered)
	}
	if _, err := ReplayEventLog(filepath.Join(store.RunDir(), "events.jsonl"), runID); err != nil {
		t.Fatalf("repaired event log should pass strict replay: %v", err)
	}
}

// BL-06: the append-mode replay must carry the in-flight agent_finished window
// itself, but only while the finished attempt is the trailing durable event.
func TestReplayEventsForAppendCarriesOnlyTrailingPendingAgentFinishedWindow(t *testing.T) {
	const runID = "append-replay-pending-window"
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
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if got := trailingPendingAgentFinishedAttemptID(events); got != "agent-attempt-1" {
		t.Fatalf("trailing pending attempt = %q; want agent-attempt-1", got)
	}
	if _, err := replayEventsForAppend(events, runID, store.RunDir(), nil); err != nil {
		t.Fatalf("append replay rejected the trailing attempt_finished window: %v", err)
	}

	// Once another event follows, the missing agent_finished is a real gap.
	if err := store.Append(Event{
		Type: "attempt_started", Stage: "analyst", AttemptID: "agent-attempt-2", Timestamp: finished,
		Data: map[string]any{"stage_index": 1, "executor": "agent"},
	}); err != nil {
		t.Fatal(err)
	}
	later, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if got := trailingPendingAgentFinishedAttemptID(later); got != "" {
		t.Fatalf("trailing pending attempt after a later event = %q; want none", got)
	}
	if _, err := replayEventsForAppend(later, runID, store.RunDir(), nil); err == nil ||
		!strings.Contains(err.Error(), "missing agent_finished") {
		t.Fatalf("append replay error = %v; want missing agent_finished outside the trailing window", err)
	}
	if got := trailingPendingAgentFinishedAttemptID(nil); got != "" {
		t.Fatalf("empty chain pending attempt = %q; want none", got)
	}
}
