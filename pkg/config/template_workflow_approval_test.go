package config

import (
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// template_workflow_approval_test.go — finding A: the forward transition
// approval is built from the effective exit chain (active workflow merged with
// the stage), not from stage-only NeedsConfirmation/RejectStays.

// workflowHumanConfig declares the only human_approval gate on the workflow's
// exit chain; neither stage has a gate of its own. "tech" has a forward stage,
// so its transition is a real decision.
func workflowHumanConfig(onReject string) *Config {
	ref := workflow.GateRef{Gate: gatelib.GateHumanApproval, Version: 1}
	if onReject != "" {
		ref = workflow.GateRef{Gate: gatelib.GateHumanApproval, Version: 2,
			Params: map[string]any{"on_reject": onReject}}
	}
	return &Config{
		SchemaVersion:  WorkflowSchemaVersion,
		Template:       "gates",
		Title:          "Gates",
		ActiveWorkflow: DefaultWorkflowID,
		Workflows: []WorkflowMeta{{
			ID:      DefaultWorkflowID,
			Title:   "SDLC",
			Version: 1,
			Graphs: []WorkflowGraph{{
				ID: DefaultGraphID, Title: "SDLC", Template: "gates",
			}},
			ExitGateGroups: workflow.GateGroups{{ref}},
		}},
		Stages: []TemplateStage{
			{ID: "tech", Title: "Tech", Function: "architect", Result: "md", Executor: "agent", Agent: "tech", Confirm: "auto"},
			{ID: "next", Title: "Next", Function: "architect", Result: "md", Executor: "agent", Agent: "verifier", Confirm: "auto"},
		},
	}
}

func passedEdgeFrom(t *testing.T, graph workflow.Graph, stage string) workflow.Edge {
	t.Helper()
	for _, edge := range graph.Edges {
		if edge.From == stage && edge.Outcome == workflow.OutcomePassed {
			return edge
		}
	}
	t.Fatalf("no passed edge from %s in %+v", stage, graph.Edges)
	return workflow.Edge{}
}

func TestTemplateGraphWorkflowHumanGateRequiresApproval(t *testing.T) {
	cfg := workflowHumanConfig("")
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("valid v7 config rejected: %v", err)
	}
	graph, err := cfg.TemplateGraph()
	if err != nil {
		t.Fatal(err)
	}
	edge := passedEdgeFrom(t, graph, "tech")
	if edge.Approval == nil {
		t.Fatalf("a workflow-only human_approval must compile a transition approval: %+v", edge)
	}
	if edge.Approval.Actions["approve"] != "next" {
		t.Fatalf("approve must go forward: %+v", edge.Approval.Actions)
	}
	if got := edge.Approval.Actions["reject"]; got != workflow.TerminalStop {
		t.Fatalf("a workflow human gate without on_reject:stay must stop: %q", got)
	}
}

func TestTemplateGraphWorkflowHumanGateStayOnReject(t *testing.T) {
	cfg := workflowHumanConfig(gatelib.OnRejectStay)
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("valid v7 config rejected: %v", err)
	}
	graph, err := cfg.TemplateGraph()
	if err != nil {
		t.Fatal(err)
	}
	edge := passedEdgeFrom(t, graph, "tech")
	if edge.Approval == nil || edge.Approval.Actions["reject"] != "tech" {
		t.Fatalf("workflow human_approval@2 on_reject:stay must keep the task at tech: %+v", edge.Approval)
	}
}

// A stage-level human gate and no workflow gate keeps the historical behavior.
func TestTemplateGraphStageHumanGateStillRequiresApproval(t *testing.T) {
	cfg := waveConfigForTemplate(workflow.GateGroups{{workflow.GateRef{
		Gate: gatelib.GateHumanApproval, Version: 1,
	}}})
	graph, err := cfg.TemplateGraph()
	if err != nil {
		t.Fatal(err)
	}
	edge := passedEdgeFrom(t, graph, cfg.Stages[0].ID)
	if edge.Approval == nil {
		t.Fatal("a stage-level human gate must still compile a transition approval")
	}
}

func waveConfigForTemplate(groups workflow.GateGroups) *Config {
	return &Config{
		SchemaVersion: GateSchemaVersion,
		Template:      "gates",
		Title:         "Gates",
		Stages: []TemplateStage{
			{ID: "tech", Title: "Tech", Function: "architect", Result: "md", Executor: "agent", ExitGateGroups: groups},
			{ID: "next", Title: "Next", Function: "architect", Result: "md", Executor: "agent"},
		},
	}
}
