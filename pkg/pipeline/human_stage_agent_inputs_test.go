package pipeline

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
)

// Этап `executor: human` со встроенным агентом (tester, analyst, observer)
// не падает, если объявленного входа агента нет: человек ждёт typed input.
func TestHumanStageWithAgentContractDoesNotFailOnMissingAgentInputs(t *testing.T) {
	dir := env(t)
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := agent.NewFS(fstest.MapFS{
		"tester/def.yaml": def(`name: tester
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  design: '{feature}/design.md'
outputs:
  test-report: '{feature}/test-report.md'
`),
		"tester/prompt.md": def("fixture"),
	})
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "human-agent-inputs-test",
		Title:         "Human with agent contract",
		Stages: []config.TemplateStage{
			{ID: "qa", Title: "QA", Function: "qa", Result: "md", Executor: "human", Agent: "tester", Confirm: "auto"},
		},
	}
	p := New(cfg, registry, WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}), WithApprovalStore(store))
	result, runErr := p.RunWithResult(context.Background(), RunConfig{TargetDir: dir, Feature: "feat", TaskDesc: "task"})
	var required *ApprovalRequiredError
	if !errors.As(runErr, &required) {
		t.Fatalf("human stage must wait for typed input, got result=%+v err=%v", result, runErr)
	}
	pending, loadErr := store.Load(result.RunID, required.ApprovalID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if pending.Kind != approval.KindInput || pending.FromStage != "qa" {
		t.Fatalf("expected typed input for qa, got %+v", pending)
	}
}
