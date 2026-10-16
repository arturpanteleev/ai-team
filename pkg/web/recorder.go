package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
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
	// Проеция переживает транзиентные ошибки SQLite (busy lock): запись не
	// отключается навсегда, предупреждения троттлятся.
	lastWarn time.Time
}

func NewStoreRecorder(s *store.Store) *StoreRecorder {
	return &StoreRecorder{store: s, current: make(map[string]*store.Stage)}
}

func (r *StoreRecorder) warn(op string, err error) {
	if !r.lastWarn.IsZero() && time.Since(r.lastWarn) < 30*time.Second {
		return
	}
	r.lastWarn = time.Now()
	fmt.Fprintf(os.Stderr, "⚠ web store (%s): %v\n", op, err)
}

func (r *StoreRecorder) ReconcileInterrupted(at time.Time) {
	if err := r.store.ReconcileInterrupted(at); err != nil {
		r.warn("reconcile interrupted", err)
	}
}

func (r *StoreRecorder) RunStarted(runID, feature, configSnapshot string, startedAt time.Time) {
	run := &store.PipelineRun{
		RunID: runID, Feature: feature, Status: "running", StartedAt: startedAt, ConfigSnapshot: configSnapshot,
	}
	if err := r.store.StartAdmittedRun(run); err != nil {
		r.warn("create run", err)
		return
	}
	sequence, err := r.store.LatestRunEventSequence(runID)
	if err != nil {
		r.warn("start event sequence", err)
		return
	}
	r.runID, r.runUID, r.sequence = run.ID, runID, sequence
	r.event("run_started", "", startedAt, map[string]any{"feature": feature})
}

func (r *StoreRecorder) RunResumed(runID string, resumedAt time.Time) {
	run, err := r.store.ResumePipelineRun(runID)
	if err != nil {
		r.warn("resume run", err)
		return
	}
	sequence, err := r.store.LatestRunEventSequence(runID)
	if err != nil {
		r.warn("resume event sequence", err)
		return
	}
	r.runID, r.runUID, r.sequence = run.ID, runID, sequence
	r.event("run_resumed", "", resumedAt, nil)
}

func (r *StoreRecorder) RunAttached(runID string) {
	run, err := r.store.GetPipelineRunByRunID(runID)
	if err != nil {
		r.warn("attach run", err)
		return
	}
	sequence, err := r.store.LatestRunEventSequence(runID)
	if err != nil {
		r.warn("attach event sequence", err)
		return
	}
	r.runID, r.runUID, r.sequence = run.ID, runID, sequence
}

func (r *StoreRecorder) RunPaused(runID, status string, pausedAt time.Time) {
	if r.runID == 0 || runID != r.runUID {
		return
	}
	run := &store.PipelineRun{ID: r.runID, RunID: runID, Status: status, CompletedAt: &pausedAt}
	if err := r.store.UpdatePipelineRun(run); err != nil {
		r.warn("pause run", err)
		return
	}
	r.event("run_paused", "", pausedAt, map[string]any{"status": status})
}

func (r *StoreRecorder) RunCanceled(runID string, canceledAt time.Time) {
	if r.runID == 0 || runID != r.runUID {
		return
	}
	r.event("run_canceled", "", canceledAt, nil)
}

func (r *StoreRecorder) ApprovalRequested(runID, approvalID, attemptID string, at time.Time, data map[string]any) {
	if r.runID == 0 || runID != r.runUID {
		return
	}
	r.event("approval_requested", attemptID, at, data)
}

func (r *StoreRecorder) ApprovalDecided(runID, approvalID, attemptID string, at time.Time, data map[string]any) {
	if r.runID == 0 || runID != r.runUID {
		return
	}
	r.event("approval_decided", attemptID, at, data)
}

// RecordEvent projects task-specific evidence such as grill completion metrics
// into the same run event stream shown by the task API.
func (r *StoreRecorder) RecordEvent(runID, eventType, attemptID string, at time.Time, data map[string]any) {
	if r.runID == 0 || runID != r.runUID {
		return
	}
	r.event(eventType, attemptID, at, data)
}

func (r *StoreRecorder) TransitionSelected(runID, attemptID string, at time.Time, data map[string]any) {
	if r.runID == 0 || runID != r.runUID {
		return
	}
	r.event("transition_selected", attemptID, at, data)
}

func (r *StoreRecorder) StageStarted(runID, attemptID, agentName string, index int, startedAt time.Time) {
	if r.runID == 0 || runID != r.runUID {
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

// storedStage returns the persisted row of an attempt of this run, or nil. It
// covers a recorder that did not see StageStarted (another process, a repeated
// report) so the row is updated instead of inserted twice.
func (r *StoreRecorder) storedStage(attemptID string) *store.Stage {
	stages, err := r.store.GetStagesByPipelineRunID(r.runID)
	if err != nil {
		return nil
	}
	for index := range stages {
		if stages[index].AttemptID == attemptID {
			return &stages[index]
		}
	}
	return nil
}

func (r *StoreRecorder) StageFinished(stage notifier.StageResult) {
	if r.runID == 0 || stage.RunID != r.runUID {
		return
	}
	stored := r.current[stage.AttemptID]
	// refinal is a repeated report of an attempt whose row is already final: a
	// human attempt is reported by runHumanStage and once more by the stage loop
	// after its gates and delivery ran. The second report refreshes the row
	// (checks, delivery) but is not a second attempt_finished event.
	refinal := false
	if stored == nil {
		if existing := r.storedStage(stage.AttemptID); existing != nil {
			stored, refinal = existing, existing.CompletedAt != nil
		} else {
			stored = &store.Stage{
				PipelineRunID: r.runID, AttemptID: stage.AttemptID, StageIndex: stage.StageIndex,
				AgentName: stage.Name, Status: "running", StartedAt: stage.StartedAt,
			}
			if err := r.store.CreateStage(stored); err != nil {
				r.warn("create missing stage", err)
				return
			}
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
	if refinal {
		return
	}
	finished := map[string]any{
		"stage": stage.Name, "agent": stage.Name, "actor_id": stage.ActorID, "executor": stage.Executor,
		"status": stage.Status, "execution": stored.Execution,
		"decision": stored.Decision, "outcome": stored.Outcome,
		"human_edit_of_agent_attempt_id": stage.HumanEditOfAgentAttemptID,
	}
	if stage.ExecutorInfo != nil {
		// The actual executor of the attempt (internal agent runtime or named
		// external adapter); `executor` above stays the human/agent class.
		finished["executor_kind"] = stage.ExecutorInfo.Kind
		finished["executor_name"] = stage.ExecutorInfo.Name
	}
	r.event("attempt_finished", stage.AttemptID, completedAt, finished)
}

// EventRecorded projects selected immutable warning events to the task event
// feed. The pipeline calls this only after the evidence append succeeds.
func (r *StoreRecorder) EventRecorded(runID string, source evidence.Event) {
	if r.runID == 0 || runID != r.runUID {
		return
	}
	actor, _ := source.Data["actor_id"].(string)
	if actor == "" {
		actor, _ = source.Data["actor"].(string)
	}
	executor, _ := source.Data["executor"].(string)
	severity, _ := source.Data["severity"].(string)
	if severity == "" {
		severity = "warning"
	}
	event := &store.Event{
		RunID: runID, Type: source.Type, Stage: source.Stage, Actor: actor,
		Executor: executor, Severity: severity, AttemptID: source.AttemptID,
		Timestamp: source.Timestamp, DataJSON: marshalJSON(source.Data),
		DedupeKey: typedEvidenceEventKey(source),
	}
	if _, err := r.store.AppendEventNextIfAbsent(event); err != nil {
		r.warn("append typed event", err)
	}
}

func typedEvidenceEventKey(event evidence.Event) string {
	identity := strings.Join([]string{
		event.Type, event.Stage, event.AttemptID, event.Timestamp.UTC().Format(time.RFC3339Nano),
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return "evidence:" + hex.EncodeToString(digest[:])
}

func (r *StoreRecorder) AttemptsInvalidated(runID string, attemptIDs []string, at time.Time) {
	if r.runID == 0 || runID != r.runUID || len(attemptIDs) == 0 {
		return
	}
	if err := r.store.InvalidateAttempts(r.runID, attemptIDs, at); err != nil {
		r.warn("invalidate attempts", err)
		return
	}
	r.event("attempts_invalidated", "", at, map[string]any{"attempt_ids": attemptIDs, "reason": "loopback"})
}

func (r *StoreRecorder) RunFinished(runID, status string, completedAt time.Time) {
	if r.runID == 0 || runID != r.runUID {
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
	event := &store.Event{
		RunID: r.runUID, Type: eventType, AttemptID: attemptID,
		Timestamp: timestamp, DataJSON: marshalJSON(data),
	}
	if err := r.store.AppendEventNext(event); err != nil {
		r.warn("append event", err)
	} else {
		r.sequence = event.Sequence
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
