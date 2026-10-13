package lifecycle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func validState(target string) State {
	return State{
		RunID: "run-1", Feature: "feature", TargetDir: target, Task: "задача",
		Phase: PhaseRunning, NextStage: "analyst", ConfigSHA256: string(make([]byte, 64)),
		WorkflowSHA256: string(make([]byte, 64)), CreatedAt: time.Now().UTC(),
	}
}

func TestStoreLifecycleTransitions(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state := validState(target)
	if err := store.Create(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	next := loaded
	next.Phase = PhaseResumable
	if err := store.Save(loaded, next); err != nil {
		t.Fatal(err)
	}
	resumable, _ := store.Load(state.RunID)
	running := resumable
	running.Phase = PhaseRunning
	if err := store.Save(resumable, running); err != nil {
		t.Fatal(err)
	}
	terminal := running
	terminal.Phase, terminal.NextStage = PhaseTerminal, ""
	if err := store.Save(running, terminal); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(terminal, running); err == nil {
		t.Fatal("terminal state не должен возобновляться")
	}
}

func TestStoreRejectsCorruptionAndSymlink(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target, ".ai-team", "state", "runs", "run-1.json")
	if err := os.WriteFile(path, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("run-1"); err == nil {
		t.Fatal("повреждённый state должен быть отклонён")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(target, "actual")
	if err := os.WriteFile(actual, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, path); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(validState(target)); err == nil {
		t.Fatal("symlink state path должен быть отклонён")
	}
}

func TestExecutorOverrideRequiresReadyApprovalVisitIdentity(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state := validState(target)
	state.Phase = PhaseWaiting
	state.PendingApprovalID = "approval-a"
	state.ExecutorOverrides = map[string]ExecutorOverride{
		"analyst": {Executor: "human", PreviousExecutor: "agent", ActorID: "alice", ApprovalID: "approval-a", VisitID: "approval-a", ChangedAt: time.Now().UTC()},
	}
	if err := store.Create(state); err != nil {
		t.Fatalf("valid visit-bound override should persist: %v", err)
	}
	loaded, err := store.Load(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	running := loaded
	running.Phase = PhaseRunning
	running.PendingApprovalID = ""
	running.ActiveApprovalID = "approval-a"
	if err := store.Save(loaded, running); err != nil {
		t.Fatalf("running stage should retain its resolved dispatch approval: %v", err)
	}
	loaded, err = store.Load(state.RunID)
	if err != nil || loaded.ActiveApprovalID != "approval-a" {
		t.Fatalf("active stage approval was not preserved: state=%+v err=%v", loaded, err)
	}
	loaded.ExecutorOverrides["analyst"] = ExecutorOverride{Executor: "agent", PreviousExecutor: "human", ActorID: "alice", ChangedAt: time.Now().UTC()}
	if err := store.Save(loaded, loaded); err == nil {
		t.Fatal("override without approval visit identity must be rejected")
	}
}

func TestStageEnteredAtSurvivesSameStageSaves(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state := validState(target)
	state.CreatedAt = time.Now().UTC().Add(-time.Hour)
	if err := store.Create(state); err != nil {
		t.Fatal(err)
	}
	created, err := store.Load(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !created.StageEnteredAt.Equal(created.CreatedAt) {
		t.Fatalf("create stamps stage entry: %v want %v", created.StageEnteredAt, created.CreatedAt)
	}
	// Same stage, different phase and a forged timestamp: the store keeps the entry time.
	sameStage := created
	sameStage.Phase = PhaseResumable
	sameStage.StageEnteredAt = time.Unix(1, 0).UTC()
	if err := store.Save(created, sameStage); err != nil {
		t.Fatal(err)
	}
	resumable, err := store.Load(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !resumable.StageEnteredAt.Equal(created.CreatedAt) || !resumable.UpdatedAt.After(created.CreatedAt) {
		t.Fatalf("same-stage save: entered=%v updated=%v created=%v", resumable.StageEnteredAt, resumable.UpdatedAt, created.CreatedAt)
	}
	nextStage := resumable
	nextStage.Phase, nextStage.NextStage = PhaseRunning, "architect"
	if err := store.Save(resumable, nextStage); err != nil {
		t.Fatal(err)
	}
	moved, err := store.Load(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !moved.StageEnteredAt.Equal(moved.UpdatedAt) || !moved.StageEnteredAt.After(created.CreatedAt) {
		t.Fatalf("stage change stamps entry: entered=%v updated=%v", moved.StageEnteredAt, moved.UpdatedAt)
	}
}

func TestLegacyStateWithoutStageEnteredAtLoads(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	legacy := `{"schema_version":1,"run_id":"run-1","feature":"feature","target_dir":` + strconv.Quote(target) +
		`,"task":"задача","phase":"running","next_stage":"analyst","attempt_ordinal":0,"config_sha256":"` + hash +
		`","workflow_sha256":"` + hash + `","created_at":"2026-10-01T10:00:00Z","updated_at":"2026-10-01T11:00:00Z"}`
	path := filepath.Join(target, ".ai-team", "state", "runs", "run-1.json")
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("run-1")
	if err != nil {
		t.Fatalf("legacy checkpoint must load: %v", err)
	}
	updated := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)
	if !loaded.StageEnteredAt.IsZero() || !loaded.StageEnteredSince().Equal(updated) {
		t.Fatalf("legacy fallback: entered=%v since=%v", loaded.StageEnteredAt, loaded.StageEnteredSince())
	}
	next := loaded
	next.Phase = PhaseResumable
	if err := store.Save(loaded, next); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if !saved.StageEnteredAt.Equal(updated) {
		t.Fatalf("legacy same-stage save carries last update as entry: %v", saved.StageEnteredAt)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"stage_entered_at"`) {
		t.Fatalf("saved checkpoint must persist stage_entered_at: %s", data)
	}
}
