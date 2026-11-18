package evidence

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestGatePinsAppendIsControllerOwned(t *testing.T) {
	const runID = "gates-pins-append"
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if err := store.Append(Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	payload := map[string]any{
		"template": "fixture",
		"pins": []any{map[string]any{
			"gate": "required_sections", "version": float64(1), "kind": "builtin", "digest": digest,
		}},
	}
	candidate := Event{Type: "gate_pins_recorded", Timestamp: started.Add(time.Second), Data: payload}
	sequence, previous := uint64(len(events)), events[len(events)-1].SHA256
	if _, _, err := ValidateEventAppend(events, runID, store.RunDir(), candidate, sequence, previous, FilesystemAttemptManifestSource()); err == nil ||
		!strings.Contains(err.Error(), "not allowed through worker API") {
		t.Fatalf("worker append accepted controller-owned gate pins: %v", err)
	}
	if _, _, err := ValidateControllerEventAppend(events, runID, store.RunDir(), candidate, sequence, previous, FilesystemAttemptManifestSource()); err != nil {
		t.Fatalf("controller gate pins append rejected: %v", err)
	}
	// A legacy config has no template; the honest empty string is accepted as
	// long as the key is present and typed as a string.
	legacy := candidate
	legacy.Data = map[string]any{"template": "", "pins": payload["pins"]}
	if _, _, err := ValidateControllerEventAppend(events, runID, store.RunDir(), legacy, sequence, previous, FilesystemAttemptManifestSource()); err != nil {
		t.Fatalf("controller rejected legacy gate pins with empty template: %v", err)
	}
	for name, data := range map[string]map[string]any{
		"missing template":    {"pins": payload["pins"]},
		"non-string template": {"template": 123, "pins": payload["pins"]},
		"empty pins":          {"template": "fixture", "pins": []any{}},
		"bad digest": {"template": "fixture", "pins": []any{map[string]any{
			"gate": "required_sections", "version": float64(1), "kind": "builtin", "digest": "short",
		}}},
		"extra field": {"template": "fixture", "pins": payload["pins"], "unexpected": true},
	} {
		bad := candidate
		bad.Data = data
		if _, _, err := ValidateControllerEventAppend(events, runID, store.RunDir(), bad, sequence, previous, FilesystemAttemptManifestSource()); err == nil {
			t.Fatalf("controller accepted malformed gate pins: %s", name)
		}
	}
}

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
