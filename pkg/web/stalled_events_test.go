package web

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func TestStalledStageWarningIsDeduplicatedAfterServerRestart(t *testing.T) {
	target := t.TempDir()
	database := filepath.Join(t.TempDir(), "events.db")
	enteredAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state := lifecycle.State{
		RunID: "stalled-run", Feature: "feature", TargetDir: target, Task: "test stall events",
		Phase: lifecycle.PhaseWaiting, NextStage: "qa", PendingApprovalID: "approval-qa",
		ConfigSHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkflowSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CreatedAt:      enteredAt,
	}
	if err := stateStore.Create(state); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(database, "", target, WithTargetDir(target), WithStallAfter(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.store.CreatePipelineRun(&store.PipelineRun{
		RunID: state.RunID, Feature: state.Feature, Status: "waiting_for_approval", StartedAt: enteredAt,
	}); err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	checkAt := time.Now().UTC()
	if err := server.checkStalledRuns(checkAt); err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewServer(database, "", target, WithTargetDir(target), WithStallAfter(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close() }()
	if err := restarted.checkStalledRuns(checkAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	events, err := restarted.store.GetRunEventsAfter(state.RunID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("stall warning count after restart=%d err=%v events=%+v", len(events), err, events)
	}
	if events[0].Type != "stage_stalled" || events[0].Stage != "qa" || events[0].Severity != "warning" || events[0].Actor != "system" {
		t.Fatalf("stall warning fields missing: %+v", events[0])
	}
}
