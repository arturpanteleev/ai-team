package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestNew_CreatesTables(t *testing.T) {
	s := newTestStore(t)

	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('pipeline_runs', 'stages')").Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tables, got %d", count)
	}
}

func TestNewAppliesVersionedMigrations(t *testing.T) {
	s := newTestStore(t)
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version <= ?", SchemaVersion).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != SchemaVersion {
		t.Fatalf("applied migrations=%d want %d", count, SchemaVersion)
	}
}

func TestNewRejectsNewerSchemaVersion(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "future.db")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (999, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := New(databasePath)
	if store != nil {
		store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("expected future schema rejection, got %v", err)
	}
}

func TestCreatePipelineRun(t *testing.T) {
	s := newTestStore(t)

	now := time.Now()
	run := &PipelineRun{
		RunID:     "run-create",
		Feature:   "test-feature",
		Status:    "running",
		StartedAt: now,
	}

	if err := s.CreatePipelineRun(run); err != nil {
		t.Fatalf("CreatePipelineRun: %v", err)
	}
	if run.ID == 0 {
		t.Error("expected ID to be set after insert")
	}
}

func TestRunIdentityPaginationEventsAndReconciliation(t *testing.T) {
	s := newTestStore(t)
	first := &PipelineRun{RunID: "run-old", Feature: "first", Status: "running", StartedAt: time.Now().Add(-time.Hour)}
	second := &PipelineRun{RunID: "run-new", Feature: "second", Status: "completed", StartedAt: time.Now()}
	if err := s.CreatePipelineRun(first); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePipelineRun(second); err != nil {
		t.Fatal(err)
	}
	stage := &Stage{PipelineRunID: first.ID, AttemptID: "001-a", StageIndex: 1, AgentName: "a", Status: "running", StartedAt: first.StartedAt}
	if err := s.CreateStage(stage); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(&Event{RunID: first.RunID, Sequence: 1, Type: "run_started", Timestamp: first.StartedAt}); err != nil {
		t.Fatal(err)
	}
	page, err := s.GetPipelineRunsPage(1, 0)
	if err != nil || len(page) != 1 || page[0].RunID != "run-new" {
		t.Fatalf("pagination: %+v err=%v", page, err)
	}
	byUID, err := s.GetPipelineRunByRunID("run-old")
	if err != nil || byUID.ID != first.ID {
		t.Fatalf("run identity lookup: %+v err=%v", byUID, err)
	}
	finished := time.Now().UTC()
	if err := s.ReconcileInterrupted(finished); err != nil {
		t.Fatal(err)
	}
	reconciled, _ := s.GetPipelineRunByRunID("run-old")
	stages, _ := s.GetStagesByPipelineRunID(first.ID)
	if reconciled.Status != "interrupted" || reconciled.CompletedAt == nil || len(stages) != 1 || stages[0].Status != "interrupted" {
		t.Fatalf("reconciliation: run=%+v stages=%+v", reconciled, stages)
	}
}

func TestGetPipelineRuns_Empty(t *testing.T) {
	s := newTestStore(t)

	runs, err := s.GetPipelineRuns()
	if err != nil {
		t.Fatalf("GetPipelineRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}
}

func TestGetPipelineRuns_Ordered(t *testing.T) {
	s := newTestStore(t)

	s.CreatePipelineRun(&PipelineRun{Feature: "first", Status: "completed", StartedAt: time.Now().Add(-time.Hour)})
	s.CreatePipelineRun(&PipelineRun{Feature: "second", Status: "running", StartedAt: time.Now()})

	runs, err := s.GetPipelineRuns()
	if err != nil {
		t.Fatalf("GetPipelineRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].Feature != "second" {
		t.Errorf("expected newest first, got %s", runs[0].Feature)
	}
	if runs[1].Feature != "first" {
		t.Errorf("expected oldest second, got %s", runs[1].Feature)
	}
}

func TestGetPipelineRunByID(t *testing.T) {
	s := newTestStore(t)

	run := &PipelineRun{Feature: "my-feature", Status: "running", StartedAt: time.Now()}
	s.CreatePipelineRun(run)

	got, err := s.GetPipelineRunByID(run.ID)
	if err != nil {
		t.Fatalf("GetPipelineRunByID: %v", err)
	}
	if got.Feature != "my-feature" {
		t.Errorf("expected feature 'my-feature', got %s", got.Feature)
	}
	if got.Status != "running" {
		t.Errorf("expected status 'running', got %s", got.Status)
	}
}

func TestGetPipelineRunByID_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetPipelineRunByID(999)
	if err == nil {
		t.Error("expected error for non-existent ID")
	}
}

func TestUpdatePipelineRun(t *testing.T) {
	s := newTestStore(t)

	run := &PipelineRun{Feature: "update-test", Status: "running", StartedAt: time.Now()}
	s.CreatePipelineRun(run)

	now := time.Now()
	run.Status = "completed"
	run.CompletedAt = &now
	if err := s.UpdatePipelineRun(run); err != nil {
		t.Fatalf("UpdatePipelineRun: %v", err)
	}

	got, _ := s.GetPipelineRunByID(run.ID)
	if got.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

func TestCreateStage(t *testing.T) {
	s := newTestStore(t)

	run := &PipelineRun{Feature: "stage-test", Status: "running", StartedAt: time.Now()}
	s.CreatePipelineRun(run)

	stage := &Stage{
		PipelineRunID: run.ID,
		AgentName:     "analyst",
		Status:        "running",
		StartedAt:     time.Now(),
	}
	if err := s.CreateStage(stage); err != nil {
		t.Fatalf("CreateStage: %v", err)
	}
	if stage.ID == 0 {
		t.Error("expected ID to be set")
	}
}

func TestGetStagesByPipelineRunID(t *testing.T) {
	s := newTestStore(t)

	run := &PipelineRun{Feature: "stages-test", Status: "running", StartedAt: time.Now()}
	s.CreatePipelineRun(run)

	s.CreateStage(&Stage{PipelineRunID: run.ID, AgentName: "analyst", Status: "passed", StartedAt: time.Now(), Error: "", InputsJSON: "", OutputsJSON: ""})
	s.CreateStage(&Stage{PipelineRunID: run.ID, AgentName: "architect", Status: "running", StartedAt: time.Now(), Error: "", InputsJSON: "", OutputsJSON: ""})

	stages, err := s.GetStagesByPipelineRunID(run.ID)
	if err != nil {
		t.Fatalf("GetStagesByPipelineRunID: %v", err)
	}
	if len(stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(stages))
	}
	if stages[0].AgentName != "analyst" {
		t.Errorf("expected first stage 'analyst', got %s", stages[0].AgentName)
	}
}

func TestUpdateStage(t *testing.T) {
	s := newTestStore(t)

	run := &PipelineRun{Feature: "update-stage", Status: "running", StartedAt: time.Now()}
	s.CreatePipelineRun(run)

	stage := &Stage{PipelineRunID: run.ID, AgentName: "coder", Status: "running", StartedAt: time.Now()}
	s.CreateStage(stage)

	now := time.Now()
	stage.Status = "passed"
	stage.CompletedAt = &now
	stage.DurationMs = 5000
	stage.Error = ""
	if err := s.UpdateStage(stage); err != nil {
		t.Fatalf("UpdateStage: %v", err)
	}

	stages, _ := s.GetStagesByPipelineRunID(run.ID)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	if stages[0].Status != "passed" {
		t.Errorf("expected status 'passed', got %s", stages[0].Status)
	}
	if stages[0].DurationMs != 5000 {
		t.Errorf("expected duration_ms 5000, got %d", stages[0].DurationMs)
	}
}

func TestCreateStage_WithJSONFields(t *testing.T) {
	s := newTestStore(t)

	run := &PipelineRun{Feature: "json-test", Status: "running", StartedAt: time.Now()}
	s.CreatePipelineRun(run)

	stage := &Stage{
		PipelineRunID: run.ID,
		AgentName:     "reviewer",
		Status:        "passed",
		StartedAt:     time.Now(),
		InputsJSON:    `["proposal.md","specs/"]`,
		OutputsJSON:   `["review.md"]`,
	}
	if err := s.CreateStage(stage); err != nil {
		t.Fatalf("CreateStage: %v", err)
	}

	stages, _ := s.GetStagesByPipelineRunID(run.ID)
	if stages[0].InputsJSON != `["proposal.md","specs/"]` {
		t.Errorf("inputs_json mismatch: %s", stages[0].InputsJSON)
	}
	if stages[0].OutputsJSON != `["review.md"]` {
		t.Errorf("outputs_json mismatch: %s", stages[0].OutputsJSON)
	}
}
