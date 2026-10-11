package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
)

func TestTaskAutoConfigChangesRunCopyOnly(t *testing.T) {
	cfg, err := config.ParseYAML([]byte(`schema_version: 5
template: task-test
title: Task test
stages:
  - id: implementation
    title: Implementation
    function: developer
    result: md
    executor: human
    agent: coder
    confirm: required
  - id: acceptance
    title: Acceptance
    function: product_owner
    result: approve
    executor: human
`))
	if err != nil {
		t.Fatal(err)
	}
	auto, err := taskAutoConfig(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auto.Stages[0].Executor != "agent" || auto.Stages[0].Confirm != "auto" {
		t.Fatalf("agent stage not switched to auto: %+v", auto.Stages[0])
	}
	if auto.Stages[1].Executor != "human" || auto.Stages[1].Confirm != "auto" {
		t.Fatalf("human-only stage must remain human and auto-confirm: %+v", auto.Stages[1])
	}
	if cfg.Stages[0].Executor != "human" || cfg.Stages[0].Confirm != "required" {
		t.Fatalf("auto mode mutated active template: %+v", cfg.Stages[0])
	}
}

func TestTaskNextActionUsesInputContractAndDecisionIdentity(t *testing.T) {
	target := filepath.Join(string(filepath.Separator), "tmp", "project with space")
	state := lifecycle.State{Phase: lifecycle.PhaseWaiting}
	payload, err := json.Marshal(approval.InputPayload{Kind: string(approval.KindInput), StageID: "spec", Result: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	pending := approval.PendingApproval{ID: "approval-1", Kind: approval.KindInput, Payload: payload}
	got := taskNextAction(target, "run-1", state, pending)
	for _, part := range []string{"ai-team stage submit 'run-1'", "--stage 'spec'", "--approve", "--target '" + target + "'"} {
		if !strings.Contains(got, part) {
			t.Fatalf("next action missing %q: %s", part, got)
		}
	}

	got = taskNextAction(target, "run-1", state, approval.PendingApproval{ID: "approval-2", Kind: approval.KindApprove})
	for _, part := range []string{"ai-team decision", "--approval 'approval-2'", "--subject '<subject-sha256>'"} {
		if !strings.Contains(got, part) {
			t.Fatalf("decision next action missing %q: %s", part, got)
		}
	}
}

func TestListTaskRunStatesValidatesAndSortsLifecycle(t *testing.T) {
	target := newControlRoot(t)
	store, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id string
		at time.Time
	}{{"run-old", base}, {"run-new", base.Add(time.Minute)}} {
		state := lifecycle.State{
			RunID: item.id, Feature: "feature", TargetDir: target, Task: "task",
			Phase: lifecycle.PhaseResumable, NextStage: "analyst", ConfigSHA256: strings.Repeat("a", 64),
			WorkflowSHA256: strings.Repeat("b", 64), CreatedAt: item.at,
		}
		if err := store.Create(state); err != nil {
			t.Fatal(err)
		}
	}
	states, err := listTaskRunStates(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0].RunID != "run-new" || states[1].RunID != "run-old" {
		t.Fatalf("runs not sorted newest first: %+v", states)
	}
}

func TestLoadTaskRunViewVerifiesEvidenceAndShowsResumeAction(t *testing.T) {
	target := newControlRoot(t)
	runID := "run-show"
	createTaskRunFixture(t, target, runID, lifecycle.PhaseResumable, "analyst", "")
	view, err := loadTaskRunView(target, runID)
	if err != nil {
		t.Fatal(err)
	}
	if view.NextAction != "ai-team run --resume 'run-show' --target '"+target+"'" {
		t.Fatalf("wrong resume action: %q", view.NextAction)
	}
	if view.TokenUsage.Known || view.TokenUsage.SubscriptionShare != "нет данных" {
		t.Fatalf("missing usage must remain unknown: %+v", view.TokenUsage)
	}
}

func TestLoadTaskRunViewShowsUnknownUsageForNonterminalReservedRun(t *testing.T) {
	target := newControlRoot(t)
	const runID = "run-reserved-usage"
	createTaskRunFixture(t, target, runID, lifecycle.PhaseRunning, "analyst", "")
	eventAuthority := evidence.ControllerEventStore{TargetDir: target}
	if err := eventAuthority.MigrateLegacy(runID, filepath.Join(target, ".ai-team", "runs", runID)); err != nil {
		t.Fatalf("prepare controller-backed running event log: %v", err)
	}
	if err := (metrics.FileUsageEnvelopeStore{}).Reserve(target, runID); err != nil {
		t.Fatal(err)
	}

	view, err := loadTaskRunView(target, runID)
	if err != nil {
		t.Fatalf("running run should remain inspectable while controller usage is pending: %v", err)
	}
	if view.RunID != runID || view.Phase != lifecycle.PhaseRunning || view.NextStage != "analyst" {
		t.Fatalf("show lost nonterminal run details: %+v", view)
	}
	if view.TokenUsage.Known || view.TokenUsage.SubscriptionShare != "нет данных" {
		t.Fatalf("missing usage must remain unknown until controller publishes its envelope: %+v", view.TokenUsage)
	}
	stdout, code, stderr := runCLI(t, "show", runID, "--json", "--target", target)
	if code != 0 {
		t.Fatalf("show should return the running task while usage is pending: code=%d stderr=%s", code, stderr)
	}
	records := parseJSONL(t, stdout)
	if len(records) != 1 || records[0].Command != "show" {
		t.Fatalf("show JSON record missing: %+v", records)
	}
	runView, ok := records[0].Data["run"].(map[string]any)
	if !ok || runView["phase"] != string(lifecycle.PhaseRunning) || runView["next_stage"] != "analyst" {
		t.Fatalf("show JSON lost the active run details: %#v", records[0].Data)
	}
	usage, ok := runView["token_usage"].(map[string]any)
	if !ok || usage["known"] != false || usage["approximate_subscription_share"] != "нет данных" {
		t.Fatalf("show JSON should keep pending usage unknown: %#v", runView["token_usage"])
	}
}

func TestRunResumeUsesPinnedTemplatePreflightAfterActiveTemplateChanges(t *testing.T) {
	target := newControlRoot(t)
	pinnedConfig, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	pinnedConfig.Title = "Pinned template"
	pinnedConfig.CLI = "opencode"
	pinnedYAML, err := pinnedConfig.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), pinnedYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	templateStore, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	const runID = "run-resume-pinned-config"
	if _, err := templateStore.PinCurrentForRun(runID, runID); err != nil {
		t.Fatal(err)
	}
	activeConfig := *pinnedConfig
	activeConfig.Title = "Changed active template"
	activeConfig.CLI = "claude"
	activeYAML, err := activeConfig.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templateStore.Publish(activeYAML, config.TemplateVersionID(pinnedYAML)); err != nil {
		t.Fatal(err)
	}
	registry, err := newAgentRegistry(target)
	if err != nil {
		t.Fatal(err)
	}
	loadedPin, found, err := loadPinnedResumeConfig(target, runID, registry)
	if err != nil || !found || loadedPin.Title != "Pinned template" {
		t.Fatalf("resume config did not come from the immutable task pin: found=%v title=%q err=%v", found, loadedPin.Title, err)
	}

	createTaskRunFixture(t, target, runID, lifecycle.PhaseResumable, "analyst", "")
	taskPath := filepath.Join(target, ".ai-team", "artifacts", "tasks", "feature", "task.md")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte("resume fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	opencode := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(opencode, []byte("#!/bin/sh\nprintf 'opencode test\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, code, stderr := runCLI(t, "run", "--target", target, "--resume", runID)
	if code != exitFailed || !strings.Contains(stderr, "Пайплайн остановлен") || strings.Contains(stderr, "Preflight") {
		t.Fatalf("resume should use pinned CLI preflight and reach pipeline recovery, got code=%d stderr=%q", code, stderr)
	}
}

func TestTaskCommandsJSONAndCancelReason(t *testing.T) {
	target := newControlRoot(t)
	runID := "run-cli-view"
	createTaskRunFixture(t, target, runID, lifecycle.PhaseResumable, "analyst", "")

	stdout, code, stderr := runCLI(t, "runs", "--json", "--target", target)
	if code != 0 {
		t.Fatalf("runs --json: code=%d stderr=%s", code, stderr)
	}
	records := parseJSONL(t, stdout)
	if len(records) != 1 || records[0].Command != "runs" || records[0].Data["count"] != float64(1) {
		t.Fatalf("runs JSON record: %+v", records)
	}

	stdout, code, stderr = runCLI(t, "show", runID, "--json", "--target", target)
	if code != 0 {
		t.Fatalf("show --json: code=%d stderr=%s", code, stderr)
	}
	records = parseJSONL(t, stdout)
	if len(records) != 1 || records[0].Command != "show" {
		t.Fatalf("show JSON record: %+v", records)
	}
	view, ok := records[0].Data["run"].(map[string]any)
	if !ok || view["next_action"] != "ai-team run --resume 'run-cli-view' --target '"+target+"'" {
		t.Fatalf("show must include a ready resume command: %#v", records[0].Data)
	}

	cancelStdout, code, stderr := runCLI(t, "cancel", runID, "--target", target, "--reason", "operator")
	if code != 0 {
		t.Fatalf("cancel command: code=%d stderr=%s", code, stderr)
	}
	if got := strings.Count(cancelStdout, "Run отменён: "+runID); got != 1 || stderr != "" {
		t.Fatalf("human cancel should print one success line, got stdout=%q stderr=%q", cancelStdout, stderr)
	}
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state, err := stateStore.Load(runID)
	if err != nil || state.Phase != lifecycle.PhaseTerminal {
		t.Fatalf("cancel did not terminalize run: state=%+v err=%v", state, err)
	}
	events, err := evidence.VerifyEventLog(filepath.Join(target, ".ai-team", "runs", runID, "events.jsonl"), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "run_canceled" && event.Data["reason"] == "operator" {
			return
		}
	}
	t.Fatal("run_canceled event did not preserve the CLI reason")
}

func TestTaskCancelOutputModes(t *testing.T) {
	for _, mode := range []string{"json", "quiet"} {
		t.Run(mode, func(t *testing.T) {
			target := newControlRoot(t)
			runID := "run-cancel-" + mode
			createTaskRunFixture(t, target, runID, lifecycle.PhaseResumable, "analyst", "")
			args := []string{"cancel", runID, "--target", target, "--reason", "operator"}
			if mode == "json" {
				args = append(args, "--json")
			} else {
				args = append(args, "--quiet")
			}
			stdout, code, stderr := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("cancel command: code=%d stderr=%s", code, stderr)
			}
			if mode == "json" {
				records := parseJSONL(t, stdout)
				if len(records) != 1 || records[0].Command != "cancel" || records[0].Message != "Run отменён: "+runID {
					t.Fatalf("JSON cancel should emit one structured record, got %+v", records)
				}
				if stderr != "" {
					t.Fatalf("JSON cancel should not add human output to stderr, got %q", stderr)
				}
			} else if stdout != "" || stderr != "" {
				t.Fatalf("quiet cancel should suppress success output, got stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

func createTaskRunFixture(t *testing.T, target, runID string, phase lifecycle.Phase, nextStage, approvalID string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	configSnapshot := json.RawMessage(`{"config":"snapshot"}`)
	workflowSnapshot := json.RawMessage(`{"workflow":"snapshot"}`)
	evidenceStore, err := evidence.Start(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "feature", TargetDir: target, StartedAt: now,
		ConfigSnapshot: configSnapshot, WorkflowSnapshot: workflowSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "run_started", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Create(lifecycle.State{
		RunID: runID, Feature: "feature", TargetDir: target, Task: "task",
		Phase: phase, NextStage: nextStage, PendingApprovalID: approvalID, AttemptOrdinal: 0,
		ConfigSHA256: digest(configSnapshot), WorkflowSHA256: digest(workflowSnapshot), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskShowReadsSQLiteApprovalStoreWhenPresent(t *testing.T) {
	target := newControlRoot(t)
	database := filepath.Join(target, ".ai-team", "web.db")
	store, err := approval.NewSQLiteStore(database)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(approval.InputPayload{
		Kind: string(approval.KindInput), StageID: "spec", Result: "md", OutputName: "spec", OutputPath: "spec.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(approval.PendingApproval{
		Kind: approval.KindInput, RunID: "run-sqlite", AttemptID: "attempt-1",
		FromStage: "spec", ToStage: "spec", Trigger: "human_input", SubjectHash: strings.Repeat("a", 64),
		RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny,
		Actions: []string{"submit", "reject"}, Targets: map[string]string{"submit": "spec", "reject": "spec"}, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	selected, closeStore, err := openTaskApprovalStore(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeStore() }()
	loaded, err := selected.Load("run-sqlite", created.ID)
	if err != nil || loaded.ID != created.ID || loaded.Kind != approval.KindInput {
		t.Fatalf("show backend did not load web approval: %+v err=%v", loaded, err)
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestInterspersedTaskArgsKeepsPositionalAndTarget(t *testing.T) {
	got := interspersedTaskArgs([]string{"run-1", "--target", "/tmp/project"}, map[string]bool{"--target": true})
	if strings.Join(got, "|") != "--target|/tmp/project|run-1" {
		t.Fatalf("interspersed args = %#v", got)
	}
}
