package web

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// StoreRecorder projects run-aware lifecycle events into SQLite. The immutable
// filesystem event log remains the source of truth; projection failures do not
// change the pipeline outcome.
type StoreRecorder struct {
	store    *store.Store
	runID    int64
	runUID   string
	sequence int64
	current  map[string]*store.Stage
	disabled bool
}

func NewStoreRecorder(s *store.Store) *StoreRecorder {
	return &StoreRecorder{store: s, current: make(map[string]*store.Stage)}
}

func (r *StoreRecorder) warn(op string, err error) {
	fmt.Fprintf(os.Stderr, "⚠ web store (%s): %v — запись отключена\n", op, err)
	r.disabled = true
}

func (r *StoreRecorder) ReconcileInterrupted(at time.Time) {
	if r.disabled {
		return
	}
	if err := r.store.ReconcileInterrupted(at); err != nil {
		r.warn("reconcile interrupted", err)
	}
}

func (r *StoreRecorder) RunStarted(runID, feature, configSnapshot string, startedAt time.Time) {
	if r.disabled {
		return
	}
	run := &store.PipelineRun{
		RunID: runID, Feature: feature, Status: "running", StartedAt: startedAt, ConfigSnapshot: configSnapshot,
	}
	if err := r.store.CreatePipelineRun(run); err != nil {
		r.warn("create run", err)
		return
	}
	r.runID, r.runUID, r.sequence = run.ID, runID, 0
	r.event("run_started", "", startedAt, map[string]any{"feature": feature})
}

func (r *StoreRecorder) StageStarted(runID, attemptID, agentName string, index int, startedAt time.Time) {
	if r.disabled || r.runID == 0 || runID != r.runUID {
		return
	}
	stage := &store.Stage{
		PipelineRunID: r.runID, AttemptID: attemptID, StageIndex: index,
		AgentName: agentName, Status: "running", Execution: "running", StartedAt: startedAt,
	}
	if err := r.store.CreateStage(stage); err != nil {
		r.warn("create stage", err)
		return
	}
	r.current[attemptID] = stage
	r.event("attempt_started", attemptID, startedAt, map[string]any{"agent": agentName, "stage_index": index})
}

func (r *StoreRecorder) StageFinished(stage notifier.StageResult) {
	if r.disabled || r.runID == 0 || stage.RunID != r.runUID {
		return
	}
	stored := r.current[stage.AttemptID]
	if stored == nil {
		stored = &store.Stage{
			PipelineRunID: r.runID, AttemptID: stage.AttemptID, StageIndex: stage.StageIndex,
			AgentName: stage.Name, Status: "running", StartedAt: stage.StartedAt,
		}
		if err := r.store.CreateStage(stored); err != nil {
			r.warn("create missing stage", err)
			return
		}
	}
	delete(r.current, stage.AttemptID)

	completedAt := stage.FinishedAt
	if completedAt.IsZero() {
		completedAt = stage.StartedAt.Add(stage.Duration)
	}
	stored.Status = stage.Status
	stored.Execution = string(stage.State.Execution)
	stored.Decision = string(stage.State.Decision)
	stored.Outcome = string(stage.State.Outcome)
	stored.CompletedAt = &completedAt
	stored.DurationMs = stage.Duration.Milliseconds()
	stored.Verdict = string(stage.Verdict)
	if stage.Err != nil {
		stored.Error = stage.Err.Error()
	} else if stage.Status == "blocked" {
		stored.Error = stage.Blocker
	}
	stored.InputsJSON = marshalJSON(stage.Inputs)
	stored.OutputsJSON = marshalJSON(stage.Outputs)
	stored.ChecksJSON = marshalJSON(stage.Checks)
	stored.MutationsJSON = marshalJSON(stage.Mutations)
	stored.DeliveryJSON = marshalJSON(stage.Delivery)
	if err := r.store.UpdateStage(stored); err != nil {
		r.warn("update stage", err)
		return
	}
	r.event("attempt_finished", stage.AttemptID, completedAt, map[string]any{
		"agent": stage.Name, "status": stage.Status, "execution": stored.Execution,
		"decision": stored.Decision, "outcome": stored.Outcome,
	})
}

func (r *StoreRecorder) AttemptsInvalidated(runID string, attemptIDs []string, at time.Time) {
	if r.disabled || r.runID == 0 || runID != r.runUID || len(attemptIDs) == 0 {
		return
	}
	if err := r.store.InvalidateAttempts(r.runID, attemptIDs, at); err != nil {
		r.warn("invalidate attempts", err)
		return
	}
	r.event("attempts_invalidated", "", at, map[string]any{"attempt_ids": attemptIDs, "reason": "loopback"})
}

func (r *StoreRecorder) RunFinished(runID, status string, completedAt time.Time) {
	if r.disabled || r.runID == 0 || runID != r.runUID {
		return
	}
	run := &store.PipelineRun{ID: r.runID, RunID: runID, Status: status, CompletedAt: &completedAt}
	if err := r.store.UpdatePipelineRun(run); err != nil {
		r.warn("update run", err)
		return
	}
	r.event("run_finished", "", completedAt, map[string]any{"status": status})
}

func (r *StoreRecorder) event(eventType, attemptID string, timestamp time.Time, data any) {
	if r.disabled {
		return
	}
	r.sequence++
	event := &store.Event{
		RunID: r.runUID, Sequence: r.sequence, Type: eventType, AttemptID: attemptID,
		Timestamp: timestamp, DataJSON: marshalJSON(data),
	}
	if err := r.store.AppendEvent(event); err != nil {
		r.warn("append event", err)
	}
}

func marshalJSON(value any) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}
