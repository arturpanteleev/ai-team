package config

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// trusted_commands_stage_override_test.go — final review P1: a stage-level gate
// ref with the same instance name as a workflow gate replaces that gate in the
// effective merged chain (ExitGateGroupsFor/EntryGateGroupsFor). Comparing only
// the workflow's own chains (workflowTightenProblems) and only the stage's own
// chains (stageTightenProblems) misses a web submission that weakens a workflow
// gate through such an override.

// stageOverrideWorkflowRef is human_approval@2 with the strictest on_reject:
// stay, declared on the workflow exit chain and inherited by every stage.
func stageOverrideWorkflowRef() workflow.GateRef {
	return workflow.GateRef{
		Gate:    gatelib.GateHumanApproval,
		Version: 2,
		Params:  map[string]any{"on_reject": gatelib.OnRejectStay},
	}
}

// stageOverrideConfig mirrors the reproduction: the default workflow declares
// human_approval@2/on_reject:stay on its exit chain, and "tech" has a forward
// stage so the approval compiles into a transition decision.
func stageOverrideConfig() *Config {
	return &Config{
		SchemaVersion:  WorkflowSchemaVersion,
		Template:       "gates",
		Title:          "Gates",
		ActiveWorkflow: DefaultWorkflowID,
		Workflows: []WorkflowMeta{{
			ID:             DefaultWorkflowID,
			Title:          "SDLC",
			Version:        1,
			Graphs:         []WorkflowGraph{{ID: DefaultGraphID, Title: "SDLC", Template: "gates"}},
			ExitGateGroups: workflow.GateGroups{{stageOverrideWorkflowRef()}},
		}},
		Stages: []TemplateStage{
			{ID: "tech", Title: "Tech", Function: "architect", Result: "md", Executor: "agent", Agent: "tech", Confirm: "auto"},
			{ID: "next", Title: "Next", Function: "architect", Result: "md", Executor: "agent", Agent: "verifier", Confirm: "auto"},
		},
	}
}

// The web overrides the workflow gate at stage level with a weaker ref that has
// the same instance name ("human_approval"): version 1 has no on_reject, so the
// effective chain of "tech" silently loses on_reject: stay and the transition
// graph builds reject as $stop instead of staying at "tech".
func TestStageOverrideCannotWeakenWorkflowGate(t *testing.T) {
	trusted := stageOverrideConfig()
	submitted := stageOverrideConfig()
	submitted.Stages[0].ExitGateGroups = workflow.GateGroups{{
		{Gate: gatelib.GateHumanApproval, Version: 1},
	}}

	for name, problems := range map[string][]string{
		"publish": submitted.UntrustedCommandProblems(trusted),
		"run":     submitted.untrustedRunProblems(trusted),
	} {
		joined := strings.Join(problems, "\n")
		if !strings.Contains(joined, "нельзя убрать или ослабить") || !strings.Contains(joined, gatelib.GateHumanApproval) {
			t.Fatalf("%s: a stage override of a workflow gate must be refused with trusted_config_only: %v", name, problems)
		}
	}
}

// An identical ref at stage level (no weakening) and an additional gate
// (stricter) are both accepted.
func TestStageOverrideAcceptsIdenticalAndStricterRefs(t *testing.T) {
	trusted := stageOverrideConfig()

	identical := stageOverrideConfig()
	identical.Stages[0].ExitGateGroups = workflow.GateGroups{{stageOverrideWorkflowRef()}}
	if problems := identical.UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Fatalf("an identical stage ref must be accepted: %v", problems)
	}
	if problems := identical.untrustedRunProblems(trusted); len(problems) != 0 {
		t.Fatalf("an identical stage ref must be accepted for a run: %v", problems)
	}

	stricter := stageOverrideConfig()
	stricter.Stages[0].ExitGateGroups = workflow.GateGroups{
		{stageOverrideWorkflowRef()},
		{{Gate: gatelib.GateVerdictMarker, Version: 1}},
	}
	if problems := stricter.UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Fatalf("an added gate must be accepted: %v", problems)
	}
	if problems := stricter.untrustedRunProblems(trusted); len(problems) != 0 {
		t.Fatalf("an added gate must be accepted for a run: %v", problems)
	}
}
