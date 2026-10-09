package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestStageExecutorOverrideIsScopedToReadyApprovalVisit(t *testing.T) {
	p := &Pipeline{cfg: &config.Config{Stages: []config.TemplateStage{{ID: "review", Executor: "agent"}}}}
	rs := &runState{
		p: p,
		lifecycleState: lifecycle.State{ExecutorOverrides: map[string]lifecycle.ExecutorOverride{
			"review": {Executor: "human", PreviousExecutor: "agent", ActorID: "alice", ApprovalID: "approval-a", VisitID: "approval-a", ChangedAt: time.Now().UTC()},
		}},
		resumedApproval: &approval.PendingApproval{ID: "approval-b", Kind: approval.KindApprove, Status: approval.StatusResolved},
	}
	if got := rs.stageExecutorForRun("review"); got != "agent" {
		t.Fatalf("stale approval A override leaked into new visit B: executor=%s", got)
	}
	rs.resumedApproval.ID = "approval-a"
	if got := rs.stageExecutorForRun("review"); got != "human" {
		t.Fatalf("matching ready approval should apply override: executor=%s", got)
	}
	rs.clearExecutorOverride("review")
	if got := rs.stageExecutorForRun("review"); got != "agent" {
		t.Fatalf("cleared override should restore template executor: executor=%s", got)
	}
}

func TestHumanReadyStageCanRunOrRefineWithAgent(t *testing.T) {
	for _, test := range []struct {
		name      string
		action    string
		comment   string
		wantInput string
	}{
		{name: "Сделай", action: "run_agent"},
		{name: "Доработай агентом", action: "refine_agent", comment: "## Current result\nHuman draft to refine.\n"},
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
			cfg := &config.Config{
				SchemaVersion: config.CurrentSchemaVersion,
				Template:      "executor-switch-test",
				Title:         "Executor switch test",
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
			p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithApprovalStore(approvals),
				WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))
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
			result, resumeErr := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
			if resumeErr != nil || result.Outcome != workflow.RunCompleted {
				t.Fatalf("agent action should complete the stage: result=%+v err=%v", result, resumeErr)
			}
			if len(rt.executed) != 1 || rt.executed[0] != "analyst" {
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
		})
	}
}
