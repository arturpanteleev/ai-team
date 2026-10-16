package web

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func TestStoreRecorder_FullLifecycle(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	r := NewStoreRecorder(s)

	started := time.Now().UTC()
	r.ReconcileInterrupted(started)
	r.RunStarted("run-1", "my-feature", "pipeline: [analyst]", started)
	if r.runID == 0 {
		t.Fatal("runID должен быть установлен")
	}

	r.StageStarted("run-1", "001-analyst", "analyst", 1, started)
	r.StageFinished(notifier.StageResult{
		RunID:     "run-1",
		AttemptID: "001-analyst",
		Name:      "analyst",
		Status:    notifier.StatusPassed,
		Duration:  2 * time.Second,
	})

	r.StageStarted("run-1", "002-reviewer", "reviewer", 2, started)
	r.StageFinished(notifier.StageResult{
		RunID:     "run-1",
		AttemptID: "002-reviewer",
		Name:      "reviewer",
		Status:    notifier.StatusPassed,
		Verdict:   verdict.Approved,
		Duration:  time.Second,
	})

	r.StageStarted("run-1", "003-coder", "coder", 3, started)
	r.StageFinished(notifier.StageResult{
		RunID:     "run-1",
		AttemptID: "003-coder",
		Name:      "coder",
		Status:    notifier.StatusFailed,
		Err:       errors.New("boom"),
		Duration:  time.Second,
	})

	r.RunFinished("run-1", "failed", started.Add(4*time.Second))

	runs, err := s.GetPipelineRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %v, %v", runs, err)
	}
	if runs[0].Status != "failed" {
		t.Errorf("run status = %q", runs[0].Status)
	}
	if runs[0].CompletedAt == nil {
		t.Error("completed_at должен быть установлен")
	}

	stages, err := s.GetStagesByPipelineRunID(runs[0].ID)
	if err != nil || len(stages) != 3 {
		t.Fatalf("stages: %d, %v", len(stages), err)
	}
	if stages[0].Status != "passed" || stages[0].AgentName != "analyst" {
		t.Errorf("stage[0]: %+v", stages[0])
	}
	if stages[1].Verdict != "APPROVED" {
		t.Errorf("verdict не записан: %+v", stages[1])
	}
	if stages[2].Status != "failed" || stages[2].Error != "boom" {
		t.Errorf("stage[2]: %+v", stages[2])
	}
}

func TestStoreRecorder_ReplaysWarningEvidenceIdempotently(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	r := NewStoreRecorder(s)
	at := time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC)
	r.RunStarted("run-warning-replay", "feature", "", at)
	warning := evidence.Event{
		Type: "check_error", Stage: "qa", AttemptID: "attempt-qa-1", Timestamp: at.Add(time.Second),
		Data: map[string]any{"status": "error", "error": "checker unavailable"},
	}

	// The first call models replay from the durable evidence log after the
	// process stopped before creating its SQLite projection. Further resumes
	// must keep the projection to one row.
	r.EventRecorded("run-warning-replay", warning)
	r.EventRecorded("run-warning-replay", warning)
	events, err := s.GetRunEventsAfter("run-warning-replay", 0, 10)
	if err != nil || len(events) != 2 || events[1].Type != "check_error" {
		t.Fatalf("warning projection count=%d events=%+v err=%v", len(events), events, err)
	}
	if events[1].Stage != "qa" || events[1].AttemptID != warning.AttemptID || events[1].Severity != "warning" {
		t.Fatalf("warning projection is incomplete: %+v", events[1])
	}
}

func TestStoreRecorder_DoesNotDuplicateLegacyWarningProjection(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	r := NewStoreRecorder(s)
	at := time.Date(2026, 10, 12, 2, 3, 4, 0, time.UTC)
	r.RunStarted("run-warning-legacy", "feature", "", at)
	warning := evidence.Event{
		Type: "check_error", Stage: "qa", AttemptID: "attempt-qa-1", Timestamp: at.Add(time.Second),
		Data: map[string]any{"status": "error", "error": "checker unavailable"},
	}
	// Rows projected by the earlier B-50 implementation have no DedupeKey.
	if err := s.AppendEventNext(&store.Event{
		RunID: "run-warning-legacy", Type: warning.Type, Stage: warning.Stage,
		AttemptID: warning.AttemptID, Timestamp: warning.Timestamp,
		DataJSON: marshalJSON(warning.Data),
	}); err != nil {
		t.Fatal(err)
	}
	r.EventRecorded("run-warning-legacy", warning)

	events, err := s.GetRunEventsAfter("run-warning-legacy", 0, 10)
	if err != nil || len(events) != 2 || events[1].Type != "check_error" {
		t.Fatalf("legacy projection was duplicated: events=%+v err=%v", events, err)
	}
}

func TestStoreRecorder_PreservesHumanEditLinkInStageEvent(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	r := NewStoreRecorder(s)
	at := time.Date(2026, 10, 12, 3, 4, 5, 0, time.UTC)
	r.RunStarted("run-human-edit", "feature", "", at)
	r.StageStarted("run-human-edit", "agent-attempt", "implementation", 1, at)
	r.StageFinished(notifier.StageResult{RunID: "run-human-edit", AttemptID: "agent-attempt", Name: "implementation",
		Status: notifier.StatusPassed, StartedAt: at, FinishedAt: at.Add(time.Minute)})
	r.StageStarted("run-human-edit", "human-attempt", "implementation", 2, at.Add(time.Minute))
	r.StageFinished(notifier.StageResult{RunID: "run-human-edit", AttemptID: "human-attempt", Name: "implementation",
		Status: notifier.StatusPassed, HumanEditOfAgentAttemptID: "agent-attempt", StartedAt: at.Add(time.Minute), FinishedAt: at.Add(2 * time.Minute)})

	events, err := s.GetRunEventsAfter("run-human-edit", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, event := range events {
		if event.Type != "attempt_finished" || event.AttemptID != "human-attempt" {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(event.DataJSON), &data); err != nil {
			t.Fatal(err)
		}
		if data["human_edit_of_agent_attempt_id"] != "agent-attempt" {
			t.Fatalf("human edit link was lost from attempt_finished: %v", data)
		}
		found = true
	}
	if !found {
		t.Fatal("missing human attempt_finished event")
	}
}

// A human attempt is reported by runHumanStage and again by the stage loop
// once its gates ran. The second report used to insert the row a second time
// (UNIQUE constraint failed: stages.attempt_uid) and lose the final checks.
func TestStoreRecorder_RepeatedStageFinishedUpdatesTheRowOnce(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	r := NewStoreRecorder(s)
	at := time.Date(2026, 10, 16, 3, 4, 5, 0, time.UTC)
	r.RunStarted("run-twice", "feature", "", at)
	r.StageStarted("run-twice", "human-attempt", "spec", 1, at)
	finished := notifier.StageResult{RunID: "run-twice", AttemptID: "human-attempt", Name: "spec",
		Status: notifier.StatusPassed, StartedAt: at, FinishedAt: at.Add(time.Minute)}
	r.StageFinished(finished)
	finished.Mutations = []string{"gate-mutation"}
	r.StageFinished(finished)

	stages, err := s.GetStagesByPipelineRunID(r.runID)
	if err != nil || len(stages) != 1 {
		t.Fatalf("one row expected: %+v %v", stages, err)
	}
	if !strings.Contains(stages[0].MutationsJSON, "gate-mutation") {
		t.Fatalf("the second report must refresh the row: %q", stages[0].MutationsJSON)
	}
	events, err := s.GetRunEventsAfter("run-twice", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == "attempt_finished" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("exactly one attempt_finished event expected, got %d", count)
	}
}

func TestStoreRecorder_BlockedStage(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	r := NewStoreRecorder(s)
	started := time.Now().UTC()
	r.RunStarted("run-blocked", "f", "", started)
	r.StageStarted("run-blocked", "001-analyst", "analyst", 1, started)
	r.StageFinished(notifier.StageResult{
		RunID:     "run-blocked",
		AttemptID: "001-analyst",
		Name:      "analyst",
		Status:    notifier.StatusBlocked,
		Blocker:   "противоречие",
	})
	r.RunFinished("run-blocked", "blocked", started)

	stages, _ := s.GetStagesByPipelineRunID(r.runID)
	if len(stages) != 1 || stages[0].Status != "blocked" || stages[0].Error != "противоречие" {
		t.Errorf("blocked stage: %+v", stages)
	}
}

func TestStoreRecorder_ResumeUsesExistingRunAndSequence(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	started := time.Now().UTC()
	first := NewStoreRecorder(s)
	first.RunStarted("run-resume", "feature", "", started)
	first.ApprovalRequested("run-resume", "approval-1", "001-analyst", started.Add(time.Second),
		map[string]any{"approval_id": "approval-1"})
	first.RunPaused("run-resume", "waiting_for_approval", started.Add(time.Second))

	resumed := NewStoreRecorder(s)
	resumed.ReconcileInterrupted(started.Add(2 * time.Second))
	decisionAt := started.Add(1500 * time.Millisecond)
	resumeAt := started.Add(2 * time.Second)
	resumed.RunAttached("run-resume")
	resumed.ApprovalDecided("run-resume", "approval-1", "001-analyst", decisionAt,
		map[string]any{"approval_id": "approval-1", "resolved_action": "approve"})
	resumed.RunResumed("run-resume", resumeAt)
	resumed.TransitionSelected("run-resume", "001-analyst", resumeAt,
		map[string]any{"from": "analyst", "outcome": "passed", "target": "architect"})
	runs, err := s.GetPipelineRuns()
	if err != nil || len(runs) != 1 || runs[0].Status != "running" || runs[0].CompletedAt != nil {
		t.Fatalf("resume projection: runs=%+v err=%v", runs, err)
	}
	events, err := s.GetEventsAfter(0, 10)
	if err != nil || len(events) != 6 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if events[1].Type != "approval_requested" || events[3].Type != "approval_decided" ||
		!events[3].Timestamp.Equal(decisionAt) || events[3].Sequence != 4 ||
		events[4].Type != "run_resumed" || !events[4].Timestamp.Equal(resumeAt) || events[4].Sequence != 5 ||
		events[5].Type != "transition_selected" || events[5].Sequence != 6 {
		t.Fatalf("approval/resume sequence or timestamps disagree: %+v", events)
	}
}

// A repeated report that carries a different final result (gates failed after
// the human stage reported success) must not leave attempt_finished with the
// first status: the stream is append-only, so a revision event follows.
func TestStoreRecorder_RepeatedStageFinishedWithChangedResultAddsRevisionEvent(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	r := NewStoreRecorder(s)
	at := time.Date(2026, 10, 16, 3, 4, 5, 0, time.UTC)
	r.RunStarted("run-revised", "feature", "", at)
	r.StageStarted("run-revised", "human-attempt", "spec", 1, at)
	finished := notifier.StageResult{RunID: "run-revised", AttemptID: "human-attempt", Name: "spec",
		Status: notifier.StatusPassed, StartedAt: at, FinishedAt: at.Add(time.Minute)}
	r.StageFinished(finished)
	finished.Status = notifier.StatusFailed
	r.StageFinished(finished)
	r.StageFinished(finished) // identical to the revision: nothing new

	events, err := s.GetRunEventsAfter("run-revised", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var payloads []map[string]any
	for _, event := range events {
		if event.Type != "attempt_finished" {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(event.DataJSON), &data); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, data)
	}
	if len(payloads) != 2 {
		t.Fatalf("original and one revision expected, got %d: %+v", len(payloads), payloads)
	}
	if payloads[0]["status"] != string(notifier.StatusPassed) || payloads[0]["revision"] != nil {
		t.Fatalf("original event must stay untouched: %+v", payloads[0])
	}
	if payloads[1]["status"] != string(notifier.StatusFailed) || payloads[1]["revision"] != float64(2) {
		t.Fatalf("revision event must carry the final status: %+v", payloads[1])
	}
}
