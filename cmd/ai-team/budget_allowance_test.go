package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/tokenbudget"
	"github.com/arturpanteleev/ai-team/pkg/worker"
)

func TestWorkerResumeConfigCarriesBudgetAllowance(t *testing.T) {
	job := worker.Job{Operation: worker.OperationResume, RunID: "run-1", ApproveGates: true, ApprovePlanHash: "abc"}
	if config := workerResumeConfig(job, "/target"); config.BudgetAllowance != nil || config.RunID != "run-1" ||
		config.TargetDir != "/target" || !config.ApproveGates || config.ApprovePlanHash != "abc" {
		t.Fatalf("resume without a grant = %+v", config)
	}
	job = job.WithBudgetAllowance(&pipeline.BudgetAllowanceGrant{ActorID: "alice", Reason: "why"})
	if grant := workerResumeConfig(job, "/target").BudgetAllowance; grant == nil || grant.ActorID != "alice" || grant.Reason != "why" {
		t.Fatalf("worker resume must pass the controller grant to the pipeline: %+v", grant)
	}
}

func TestRequireUncoveredTaskBudgetStop(t *testing.T) {
	target := t.TempDir()
	const runID = "run-budget"
	if err := os.MkdirAll(filepath.Join(target, ".ai-team", "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := evidence.Start(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "feat", TargetDir: target, StartedAt: time.Now().UTC(),
		ConfigSnapshot: []byte(`{}`), WorkflowSnapshot: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(event evidence.Event) {
		t.Helper()
		event.Timestamp = time.Now().UTC()
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(evidence.Event{Type: "run_started"})
	if err := requireUncoveredTaskBudgetStop(target, runID); err == nil || !strings.Contains(err.Error(), "нет остановки") {
		t.Fatalf("grant without a stop must be refused before the run: %v", err)
	}
	appendEvent(evidence.Event{Type: tokenbudget.EventBudgetExceeded, Data: map[string]any{"scope": tokenbudget.ScopeDaily, "tokens_used": 9}})
	if err := requireUncoveredTaskBudgetStop(target, runID); err == nil {
		t.Fatal("a daily stop must not be grantable")
	}
	appendEvent(evidence.Event{Type: tokenbudget.EventBudgetExceeded, Data: map[string]any{"scope": tokenbudget.ScopeTask, "tokens_used": 7}})
	if err := requireUncoveredTaskBudgetStop(target, runID); err != nil {
		t.Fatalf("uncovered task stop must be grantable: %v", err)
	}
	appendEvent(evidence.Event{Type: tokenbudget.EventBudgetAllowanceGranted, Data: map[string]any{"actor_id": "alice", "reason": "why", "baseline_tokens": 7}})
	if err := requireUncoveredTaskBudgetStop(target, runID); err == nil {
		t.Fatal("a covered stop must not be granted twice")
	}
	if err := requireUncoveredTaskBudgetStop(target, "../escape"); err == nil {
		t.Fatal("invalid run id must be refused")
	}
}
