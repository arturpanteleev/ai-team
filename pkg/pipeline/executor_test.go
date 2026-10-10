package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestStageExecutorOverrideIsScopedToReadyApprovalVisit(t *testing.T) {
	p := &Pipeline{cfg: &config.Config{Stages: []config.TemplateStage{{ID: "review", Executor: "agent"}}}}
	rs := &runState{
		p: p,
		lifecycleState: lifecycle.State{
			ActiveApprovalID: "approval-b",
			ExecutorOverrides: map[string]lifecycle.ExecutorOverride{
				"review": {Executor: "human", PreviousExecutor: "agent", ActorID: "alice", ApprovalID: "approval-a", VisitID: "approval-a", ChangedAt: time.Now().UTC()},
			},
		},
		resumedApproval: &approval.PendingApproval{ID: "approval-b", Kind: approval.KindApprove, Status: approval.StatusResolved},
	}
	if got := rs.stageExecutorForRun("review"); got != "agent" {
		t.Fatalf("stale approval A override leaked into new visit B: executor=%s", got)
	}
	rs.resumedApproval.ID = "approval-a"
	rs.lifecycleState.ActiveApprovalID = "approval-a"
	if got := rs.stageExecutorForRun("review"); got != "human" {
		t.Fatalf("matching ready approval should apply override: executor=%s", got)
	}
	rs.resumedApproval = nil
	rs.lifecycleState.ActiveApprovalID = "approval-a"
	if got := rs.stageExecutorForRun("review"); got != "human" {
		t.Fatalf("recovered active approval should preserve the executor override: executor=%s", got)
	}
	rs.clearExecutorOverride("review")
	if got := rs.stageExecutorForRun("review"); got != "agent" {
		t.Fatalf("cleared override should restore template executor: executor=%s", got)
	}
}

func TestExecutorOverrideSurvivesRunningCheckpoint(t *testing.T) {
	dir := t.TempDir()
	store, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := lifecycle.State{
		RunID: "executor-checkpoint", Feature: "feat", TargetDir: dir, Task: "task",
		Phase: lifecycle.PhaseWaiting, NextStage: "review", PendingApprovalID: "approval-a",
		ConfigSHA256: strings.Repeat("a", 64), WorkflowSHA256: strings.Repeat("b", 64), CreatedAt: now,
		ExecutorOverrides: map[string]lifecycle.ExecutorOverride{
			"review": {Executor: "human", PreviousExecutor: "agent", ActorID: "alice", ApprovalID: "approval-a", VisitID: "approval-a", ChangedAt: now},
		},
	}
	if err := store.Create(state); err != nil {
		t.Fatal(err)
	}
	state, err = store.Load(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	rs := &runState{
		p:              &Pipeline{cfg: &config.Config{Stages: []config.TemplateStage{{ID: "review", Executor: "agent"}}}},
		lifecycleStore: store, lifecycleState: state,
		resumedApproval: &approval.PendingApproval{
			ID: "approval-a", Status: approval.StatusResolved, Trigger: "graph_outcome:passed", ResolvedAction: "approve",
			Targets: map[string]string{"approve": "review"},
		},
	}
	if err := rs.saveLifecycle(lifecycle.PhaseRunning, "review"); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Load(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ActiveApprovalID != "approval-a" || recovered.PendingApprovalID != "" {
		t.Fatalf("running checkpoint did not retain active approval identity: %+v", recovered)
	}
	resumed := &runState{p: rs.p, lifecycleState: recovered}
	if executor := resumed.stageExecutorForRun("review"); executor != "human" {
		t.Fatalf("recovered visit lost its human executor override: %s", executor)
	}
}

func TestLatestAgentStageResultUsesHumanContractOutputPath(t *testing.T) {
	artifactRoot := t.TempDir()
	store, err := evidence.Start(filepath.Join(t.TempDir(), "runs"), evidence.RunManifest{
		RunID: "agent-output-provenance-test", ConfigSnapshot: json.RawMessage(`{"schema_version":1}`),
		WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(artifactRoot, "features", "feat", "spec.md")
	otherPath := filepath.Join(artifactRoot, "features", "feat", "debug.log")
	for path, content := range map[string]string{
		contractPath: "current specification",
		otherPath:    "diagnostic output must not be treated as the result",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now().UTC()
	finished := started.Add(time.Second)
	attemptID := store.NewAttemptID("product_spec", 1)
	if err := store.Append(evidence.Event{Type: "run_started", Timestamp: started.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "attempt_started", Stage: "product_spec", AttemptID: attemptID, Timestamp: started,
		Data: map[string]any{"stage_index": 1, "executor": "agent"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "agent_started", Stage: "product_spec", AttemptID: attemptID, Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(evidence.AttemptManifest{
		AttemptID: attemptID, Stage: "product_spec", Executor: "agent", StageIndex: 1,
		StartedAt: started, FinishedAt: finished, Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed",
	}, artifactRoot, nil, []evidence.Artifact{{Name: "spec", Path: contractPath}, {Name: "debug", Path: otherPath}}); err != nil {
		t.Fatal(err)
	}
	manifestDigest, _, err := evidence.AttemptManifestDigest(nil, store.RunDir(), store.RunID(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "attempt_finished", Stage: "product_spec", AttemptID: attemptID, Timestamp: finished,
		Data: map[string]any{"status": "passed", "execution": "succeeded", "decision": "approved", "outcome": "passed", "manifest_sha256": manifestDigest}}); err != nil {
		t.Fatal(err)
	}
	_, manifest, err := evidence.ReadAttemptManifest(nil, store.RunDir(), store.RunID(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	contractEvidencePath := filepath.Join(store.RunDir(), filepath.FromSlash(manifest.Outputs[0].EvidencePath))
	rs := &runState{
		p:        &Pipeline{},
		runID:    store.RunID(),
		evidence: store,
		task:     &runtime.Task{ArtifactRoot: artifactRoot},
		results: []notifier.StageResult{{
			Name: "product_spec", Executor: "agent", AttemptID: attemptID, FinishedAt: finished,
			Outputs: []runtime.Artifact{
				{Name: "spec", Path: contractPath},
				{Name: "debug", Path: otherPath},
			},
		}},
	}

	result, attemptID, path, err := rs.latestAgentStageResult("product_spec", "features/feat/spec.md")
	if err != nil {
		t.Fatal(err)
	}
	if result != "current specification" || attemptID != rs.results[0].AttemptID || path != contractEvidencePath {
		t.Fatalf("selected result=%q attempt=%q path=%q; want immutable contract output %q", result, attemptID, path, contractEvidencePath)
	}
	if err := os.WriteFile(contractPath, []byte("tampered after agent attempt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := rs.latestAgentStageResult("product_spec", "features/feat/spec.md"); err == nil {
		t.Fatal("mutated live output was accepted as the prior agent result")
	}
}

type executorCrashEvidenceFactory struct {
	delegate               EvidenceStoreFactory
	panicOnExecutorChange  bool
	panicOnAgentFinished   bool
	failAgentFinishedCount int
	failedAgentFinished    int
}

func (f *executorCrashEvidenceFactory) Start(root string, manifest evidence.RunManifest) (EvidenceStore, error) {
	store, err := f.delegate.Start(root, manifest)
	if err != nil {
		return nil, err
	}
	return &executorCrashEvidenceStore{EvidenceStore: store, factory: f}, nil
}

func (f *executorCrashEvidenceFactory) Resume(root, runID string) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error) {
	store, manifest, replayed, err := f.delegate.Resume(root, runID)
	if err != nil {
		return nil, evidence.RunManifest{}, evidence.ReplayedRun{}, err
	}
	return &executorCrashEvidenceStore{EvidenceStore: store, factory: f}, manifest, replayed, nil
}

type executorCrashEvidenceStore struct {
	EvidenceStore
	factory *executorCrashEvidenceFactory
}

func (s *executorCrashEvidenceStore) Append(event evidence.Event) error {
	if event.Type == "executor_changed" && s.factory.panicOnExecutorChange {
		s.factory.panicOnExecutorChange = false
		panic("simulated process crash after running checkpoint")
	}
	if event.Type == "agent_finished" && s.factory.panicOnAgentFinished {
		s.factory.panicOnAgentFinished = false
		panic("simulated process crash after attempt_finished")
	}
	if event.Type == "agent_finished" && s.factory.failAgentFinishedCount > 0 {
		s.factory.failAgentFinishedCount--
		s.factory.failedAgentFinished++
		return errors.New("simulated transient agent_finished append error")
	}
	return s.EvidenceStore.Append(event)
}

func (s *executorCrashEvidenceStore) ReadEvents() ([]evidence.Event, error) {
	reader, ok := s.EvidenceStore.(interface {
		ReadEvents() ([]evidence.Event, error)
	})
	if !ok {
		return nil, errors.New("underlying evidence store does not expose events")
	}
	return reader.ReadEvents()
}

func TestResolvedAgentActionSurvivesCrashAfterRunningCheckpoint(t *testing.T) {
	for _, test := range []struct {
		name    string
		action  string
		comment string
	}{
		{name: "run_agent", action: "run_agent"},
		{name: "refine_agent", action: "refine_agent", comment: "Current version to refine."},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := env(t)
			approvals, err := approval.NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			factory := &executorCrashEvidenceFactory{delegate: filesystemEvidenceStoreFactory{}}
			registry := agent.NewFS(fstest.MapFS{
				"analyst/def.yaml": def(`name: analyst
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  spec: '{feature}/spec.md'
`),
				"analyst/prompt.md": def("test agent"),
			})
			cfg := &config.Config{
				SchemaVersion: config.CurrentSchemaVersion,
				Template:      "executor-crash-recovery-test",
				Title:         "Executor crash recovery test",
				Stages: []config.TemplateStage{{
					ID: "product_spec", Title: "Product specification", Function: "product_owner",
					Result: "md", Executor: "human", Agent: "analyst", Confirm: "auto",
				}},
			}
			rt := newScripted()
			rt.content["analyst"] = map[string]string{"spec": "# Agent result\n"}
			var refinementInput string
			rt.onExec = func(_ string, inputs []runtime.Artifact) {
				for _, input := range inputs {
					if input.Name == "current-result" {
						data, readErr := os.ReadFile(input.Path)
						if readErr != nil {
							t.Errorf("read current-result input: %v", readErr)
							return
						}
						refinementInput = string(data)
					}
				}
			}
			newPipeline := func() *Pipeline {
				return New(cfg, registry, WithRuntimeFactory(rt.factory), WithApprovalStore(approvals),
					WithEvidenceStoreFactory(factory), WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))
			}

			first, runErr := newPipeline().RunWithResult(context.Background(), RunConfig{
				Feature: "feat", TaskDesc: "Write a product specification", TargetDir: dir,
			})
			var required *ApprovalRequiredError
			if !errors.As(runErr, &required) {
				t.Fatalf("human stage should wait for an executor choice: result=%+v err=%v", first, runErr)
			}
			pending, err := approvals.Load(first.RunID, required.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := approvals.Decide(first.RunID, pending.ID, approval.Decision{
				ActorID: "alice", ActorRole: "product_owner", Action: test.action,
				Comment: test.comment, SubjectHash: pending.SubjectHash,
			}); err != nil {
				t.Fatal(err)
			}

			factory.panicOnExecutorChange = true
			crashed := false
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						crashed = strings.Contains(fmt.Sprint(recovered), "simulated process crash")
					}
				}()
				_, _ = newPipeline().RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
			}()
			if !crashed {
				t.Fatal("expected simulated crash immediately after the running checkpoint")
			}
			lifecycleStore, err := lifecycle.NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := lifecycleStore.Load(first.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if checkpoint.Phase != lifecycle.PhaseRunning || checkpoint.PendingApprovalID != "" || checkpoint.ActiveApprovalID != pending.ID {
				t.Fatalf("running checkpoint lost the resolved approval: %+v", checkpoint)
			}
			events, err := evidence.VerifyEventLog(filepath.Join(dir, ".ai-team", "runs", first.RunID, "events.jsonl"), first.RunID)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Stage == "product_spec" && event.Type == "agent_started" {
					t.Fatal("agent dispatch happened before the simulated crash")
				}
			}

			result, resumeErr := newPipeline().RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
			if resumeErr != nil || result.Outcome != workflow.RunCompleted {
				t.Fatalf("recovered action should complete the stage: result=%+v err=%v", result, resumeErr)
			}
			if len(rt.executed) != 1 || rt.executed[0] != "analyst" {
				t.Fatalf("recovered action did not execute the agent exactly once: %v", rt.executed)
			}
			if test.action == "refine_agent" && refinementInput != test.comment {
				t.Fatalf("recovered refinement input = %q; want %q", refinementInput, test.comment)
			}
		})
	}
}

func TestHumanReadyStageCanRunOrRefineWithAgent(t *testing.T) {
	for _, test := range []struct {
		name                   string
		action                 string
		comment                string
		wantInput              string
		failAgentFinishedCount int
		panicOnAgentFinished   bool
		secondStage            bool
	}{
		{name: "Сделай", action: "run_agent", failAgentFinishedCount: 1},
		{name: "Доработай агентом", action: "refine_agent", comment: "## Current result\nHuman draft to refine.\n"},
		{name: "Восстановление после потери agent_finished", action: "run_agent", panicOnAgentFinished: true},
		{name: "Постоянная ошибка agent_finished останавливает граф", action: "run_agent", failAgentFinishedCount: 2, secondStage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := env(t)
			approvals, err := approval.NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			registry := agent.NewFS(fstest.MapFS{
				"analyst/def.yaml": def(`name: analyst
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  spec: '{feature}/specs/product/spec.md'
`),
				"analyst/prompt.md": def("test agent"),
			})
			if test.secondStage {
				registry = agent.NewFS(fstest.MapFS{
					"analyst/def.yaml": def(`name: analyst
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  spec: '{feature}/specs/product/spec.md'
`),
					"analyst/prompt.md": def("test agent"),
					"reviewer/def.yaml": def(`name: reviewer
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  review: '{feature}/review.md'
`),
					"reviewer/prompt.md": def("test reviewer"),
				})
			}
			stages := []config.TemplateStage{{
				ID: "product_spec", Title: "Product specification", Function: "product_owner",
				Result: "md", Executor: "human", Agent: "analyst", Confirm: "auto",
			}}
			if test.secondStage {
				stages = append(stages, config.TemplateStage{
					ID: "code_review", Title: "Code review", Function: "reviewer",
					Result: "md", Executor: "agent", Agent: "reviewer", Confirm: "auto",
				})
			}
			cfg := &config.Config{
				SchemaVersion: config.CurrentSchemaVersion,
				Template:      "executor-switch-test",
				Title:         "Executor switch test",
				Stages:        stages,
			}
			rt := newScripted()
			rt.content["analyst"] = map[string]string{"spec": "# Agent result\n"}
			rt.content["reviewer"] = map[string]string{"review": "# Review result\n"}
			evidenceFactory := &executorCrashEvidenceFactory{
				delegate: filesystemEvidenceStoreFactory{}, failAgentFinishedCount: test.failAgentFinishedCount,
				panicOnAgentFinished: test.panicOnAgentFinished,
			}
			var refinementInput string
			rt.onExec = func(_ string, inputs []runtime.Artifact) {
				for _, input := range inputs {
					if input.Name == "current-result" {
						data, readErr := os.ReadFile(input.Path)
						if readErr != nil {
							t.Errorf("read current-result input: %v", readErr)
							return
						}
						refinementInput = string(data)
					}
				}
			}
			p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithApprovalStore(approvals),
				WithEvidenceStoreFactory(evidenceFactory), WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))
			first, runErr := p.RunWithResult(context.Background(), RunConfig{
				Feature: "feat", TaskDesc: "Write a product specification", TargetDir: dir,
			})
			var required *ApprovalRequiredError
			if !errors.As(runErr, &required) {
				t.Fatalf("human stage should wait for an executor choice: result=%+v err=%v", first, runErr)
			}
			pending, err := approvals.Load(first.RunID, required.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			if !containsString(pending.Actions, "run_agent") || !containsString(pending.Actions, "refine_agent") {
				t.Fatalf("agent-capable human stage is missing action routes: %+v", pending.Actions)
			}
			if pending.Payload == nil {
				t.Fatal("typed approval must expose its current-result contract")
			}
			if _, err := approvals.Decide(first.RunID, pending.ID, approval.Decision{
				ActorID: "alice", ActorRole: "product_owner", Action: test.action,
				Comment: test.comment, SubjectHash: pending.SubjectHash,
			}); err != nil {
				t.Fatal(err)
			}
			if test.panicOnAgentFinished {
				crashed := false
				func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							crashed = strings.Contains(fmt.Sprint(recovered), "simulated process crash")
						}
					}()
					_, _ = p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
				}()
				if !crashed {
					t.Fatal("expected simulated crash after attempt_finished and before agent_finished")
				}
				preRecovery, err := evidence.VerifyEventLog(filepath.Join(dir, ".ai-team", "runs", first.RunID, "events.jsonl"), first.RunID)
				if err != nil {
					t.Fatal(err)
				}
				var sawAttemptFinished, sawAgentFinished bool
				for _, event := range preRecovery {
					if event.Stage == "product_spec" && event.AttemptID != "" {
						sawAttemptFinished = sawAttemptFinished || event.Type == "attempt_finished"
						sawAgentFinished = sawAgentFinished || event.Type == "agent_finished"
					}
				}
				if !sawAttemptFinished || sawAgentFinished {
					t.Fatalf("crash fixture did not stop in attempt_finished/agent_finished gap: %+v", preRecovery)
				}
				runDir := filepath.Join(dir, ".ai-team", "runs", first.RunID)
				if err := evidence.VerifyResumeEvidence(runDir); err != nil {
					t.Fatalf("resume preflight should backfill the missing agent_finished event: %v", err)
				}
			}

			result, resumeErr := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
			if test.failAgentFinishedCount > 1 {
				if !errors.Is(resumeErr, ErrAgentFinishedEvidence) || result.Outcome != workflow.RunStopped {
					t.Fatalf("persistent agent_finished append failure should stop resumably: result=%+v err=%v", result, resumeErr)
				}
				if len(rt.executed) != 1 || rt.executed[0] != "analyst" {
					t.Fatalf("downstream stage started after unrecorded agent completion: %v", rt.executed)
				}
				stateStore, err := lifecycle.NewStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				state, err := stateStore.Load(first.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if state.Phase != lifecycle.PhaseResumable || state.NextStage != "product_spec" {
					t.Fatalf("persistent evidence failure should retain a resumable current stage: %+v", state)
				}
				events, err := evidence.VerifyEventLog(filepath.Join(dir, ".ai-team", "runs", first.RunID, "events.jsonl"), first.RunID)
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range events {
					if event.Type == "attempt_started" && event.Stage == "code_review" {
						t.Fatalf("downstream attempt_started was recorded after persistent agent_finished failure: %+v", event)
					}
				}
				if err := evidence.VerifyResumeEvidence(filepath.Join(dir, ".ai-team", "runs", first.RunID)); err != nil {
					t.Fatalf("resumable attempt should reconcile once event persistence returns: %v", err)
				}
			} else if resumeErr != nil || result.Outcome != workflow.RunCompleted {
				t.Fatalf("agent action should complete the stage: result=%+v err=%v", result, resumeErr)
			}
			if test.failAgentFinishedCount <= 1 && (len(rt.executed) < 1 || rt.executed[0] != "analyst") {
				t.Fatalf("agent was not invoked once: %v", rt.executed)
			}
			if test.action == "refine_agent" && refinementInput != test.comment {
				t.Fatalf("refinement input = %q; want %q", refinementInput, test.comment)
			}
			events, err := evidence.VerifyEventLog(filepath.Join(dir, ".ai-team", "runs", first.RunID, "events.jsonl"), first.RunID)
			if err != nil {
				t.Fatalf("verify evidence event log: %v", err)
			}
			seen := map[string]bool{}
			for _, event := range events {
				seen[event.Type] = true
			}
			for _, eventType := range []string{"executor_changed", "agent_started", "agent_finished"} {
				if !seen[eventType] {
					t.Fatalf("missing %s event: %v", eventType, seen)
				}
			}
			if test.failAgentFinishedCount == 1 && evidenceFactory.failedAgentFinished != 1 {
				t.Fatalf("agent_finished append was not retried after transient error: failures=%d", evidenceFactory.failedAgentFinished)
			}
		})
	}
}
