package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

// active_workflow_bugfix_test.go — WF-07 finding 1: the second workflow must
// be runnable, not only present in the catalog. A task pinned to bugfix travels
// the stage subset declared by its graph chain; the full SDLC path stays the
// default, and no workflow-wide entry gate blocks the first stage.
func bugfixPipelineConfig(t *testing.T) *config.Config {
	t.Helper()
	stage := func(id, agent, function string) config.TemplateStage {
		return config.TemplateStage{
			ID: id, Title: "Этап " + id, Function: function, Result: "md",
			Executor: "agent", Agent: agent, Confirm: "auto",
		}
	}
	cfg := &config.Config{
		SchemaVersion:  config.WorkflowSchemaVersion,
		Template:       "bugfix-pipeline-test",
		Title:          "Bugfix pipeline test",
		CLI:            "opencode",
		PipelineAgents: []config.AgentConfig{{Name: "tech"}, {Name: "verifier"}},
		Stages: []config.TemplateStage{
			stage("triage", "tech", "architect"),
			stage("fix", "tech", "developer"),
			stage("verify", "verifier", "qa"),
			stage("outside", "tech", "architect"),
		},
		Workflows: []config.WorkflowMeta{
			{
				ID: config.DefaultWorkflowID, Title: "SDLC", Version: 1,
				Graphs: []config.WorkflowGraph{{ID: config.DefaultGraphID, Template: "bugfix-pipeline-test"}},
			},
			{
				ID: config.BugfixWorkflowID, Title: "Bugfix", Version: 1,
				Extends: config.DefaultWorkflowID,
				Graphs: []config.WorkflowGraph{
					{
						ID: config.BugfixFixGraphID, Title: "Fix", Template: "bugfix-pipeline-test",
						Entry: "fix", Stages: []string{"fix", "verify"},
					},
					{
						ID: config.BugfixTriageGraphID, Title: "Triage", Template: "bugfix-pipeline-test",
						Entry: "triage", Stages: []string{"triage"},
						Transitions: []config.WorkflowGraphTransition{{
							From: config.BugfixTriageGraphID, To: config.BugfixFixGraphID, On: "passed",
						}},
					},
				},
			},
		},
	}
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("bugfix pipeline test config must validate: %v", err)
	}
	return cfg
}

func runBugfixScenario(t *testing.T, cfg *config.Config) ([]string, error) {
	t.Helper()
	dir := env(t)
	rt := newScripted()
	notifications := &captureNotifier{}
	p := New(cfg, stageCheckRegistry(true),
		WithRuntimeFactory(rt.factory),
		WithNotifier(notifications),
		WithPrompter(&scriptedPrompter{}),
	)
	err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true,
	})
	visited := make([]string, 0, len(notifications.calls))
	for _, call := range notifications.calls {
		visited = append(visited, call.Name)
	}
	return visited, err
}

func TestBugfixActiveWorkflowRunsItsShortGraph(t *testing.T) {
	base := bugfixPipelineConfig(t)
	bugfix, err := base.WithActiveWorkflow(config.BugfixWorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := bugfix.CompiledGraph()
	if err != nil {
		t.Fatalf("bugfix graph must compile: %v", err)
	}
	if graph.Entry != "triage" || len(graph.Nodes) != 3 {
		t.Fatalf("compiled bugfix graph must start at triage and have 3 stages: %+v", graph)
	}
	if _, exists := graph.Node("outside"); exists {
		t.Fatal("a stage outside the bugfix graph subset must not be in the run graph")
	}
	visited, err := runBugfixScenario(t, bugfix)
	if err != nil {
		t.Fatalf("bugfix run must finish: %v", err)
	}
	if got := fmt.Sprint(visited); got != "[triage fix verify]" {
		t.Fatalf("bugfix run must travel only its stage subset: %s", got)
	}
}

func TestIdeaToProdStillRunsAllStages(t *testing.T) {
	visited, err := runBugfixScenario(t, bugfixPipelineConfig(t))
	if err != nil {
		t.Fatalf("default workflow run must finish: %v", err)
	}
	if got := fmt.Sprint(visited); got != "[triage fix verify outside]" {
		t.Fatalf("default workflow must keep the full stage list: %s", got)
	}
}
