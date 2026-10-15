package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
	"testing/fstest"
)

// A human who submits a Markdown result without a required section does not end
// the task: the required_sections gate records a `fail` for that version and the
// task waits for a decision at the stage (S17: the demo found the run failing
// at the submission itself, which made "a new version" impossible).
func TestHumanResultWithoutRequiredSectionIsAGateFailNotAFailedRun(t *testing.T) {
	dir := env(t)
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	humanArtifacts, err := humanartifact.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := agent.NewFS(fstest.MapFS{})
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "human-gate-fail-test",
		Title:         "Human gate fail",
		Stages: []config.TemplateStage{{
			ID: "product_spec", Title: "Product specification", Function: "product_owner", Result: "md", Executor: "human",
			ExitGateGroups: workflow.GateGroups{{
				codeGate(gatelib.GateRequiredSections, map[string]any{"sections": []any{"Критерии приёмки"}}),
			}},
		}},
	}
	p := New(cfg, registry, WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}), WithApprovalStore(store))

	started, runErr := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "spec", TargetDir: dir, ApproveGates: true})
	var required *ApprovalRequiredError
	if !errors.As(runErr, &required) {
		t.Fatalf("the human stage must wait for the result: %+v %v", started, runErr)
	}
	input, err := store.Load(started.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := humanArtifacts.Submit(humanStageSubmissionController{store: store}, started.RunID, humanartifact.SubmissionCommand{
		StageID: "product_spec", Result: "md", Content: "# Спека\n\nбез критериев\n", ActorID: "bob", ActorRole: "product_owner",
	}); err != nil {
		t.Fatal(err)
	}
	_ = input

	_, runErr = p.RunWithResult(context.Background(), RunConfig{ResumeRunID: started.RunID, TargetDir: dir})
	if runErr == nil || !errors.Is(runErr, ErrUserStopped) {
		t.Fatalf("a failed gate must park the task for a decision, not fail it: %v", runErr)
	}
	if runErr != nil && strings.Contains(runErr.Error(), "missing required section") {
		t.Fatalf("the submission must not be rejected as a run failure: %v", runErr)
	}
	attempts, err := gatesched.AllAttempts(gateEvents(t, dir), "product_spec")
	if err != nil || len(attempts) != 1 || attempts[0].Verdict != workflow.GateVerdictFail {
		t.Fatalf("the gate must record a fail for the submitted version: %+v %v", attempts, err)
	}
	pending := findCheckDecision(t, dir, approval.StageCheckTrigger)
	if pending.Kind != approval.KindCheckDecision {
		t.Fatalf("a check decision must wait: %+v", pending)
	}
}
