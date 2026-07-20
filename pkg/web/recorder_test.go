package web

import (
	"errors"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func TestStoreRecorder_FullLifecycle(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

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

func TestStoreRecorder_BlockedStage(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

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
