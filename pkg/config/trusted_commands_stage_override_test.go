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

// runStageOverrideWorkflowRef is human_approval@1 with the default on_reject
// ($stop): the workflow gate of the P2 reproduction. The trusted file tightens
// it at stage level with human_approval@2/on_reject:stay; a saved run that puts
// human_approval@1 on the stage silently switches reject from stay to $stop.
func runStageOverrideWorkflowRef() workflow.GateRef {
	return workflow.GateRef{Gate: gatelib.GateHumanApproval, Version: 1}
}

// runStageOverrideConfig declares human_approval@1 on the workflow exit chain
// and overrides it at stage "tech" with the stricter human_approval@2/on_reject:stay.
func runStageOverrideConfig() *Config {
	cfg := &Config{
		SchemaVersion:  WorkflowSchemaVersion,
		Template:       "gates",
		Title:          "Gates",
		ActiveWorkflow: DefaultWorkflowID,
		Workflows: []WorkflowMeta{{
			ID:             DefaultWorkflowID,
			Title:          "SDLC",
			Version:        1,
			Graphs:         []WorkflowGraph{{ID: DefaultGraphID, Title: "SDLC", Template: "gates"}},
			ExitGateGroups: workflow.GateGroups{{runStageOverrideWorkflowRef()}},
		}},
		Stages: []TemplateStage{
			{ID: "tech", Title: "Tech", Function: "architect", Result: "md", Executor: "agent", Agent: "tech", Confirm: "auto"},
			{ID: "next", Title: "Next", Function: "architect", Result: "md", Executor: "agent", Agent: "verifier", Confirm: "auto"},
		},
	}
	// The trusted file tightens the workflow gate at stage level.
	cfg.Stages[0].ExitGateGroups = workflow.GateGroups{{stageOverrideWorkflowRef()}}
	return cfg
}

// TestRunStageOverrideCannotWeakenWorkflowGateByInstance is P2 from the final
// review: for a run, only refs of the trusted active workflow are protected in
// the merged stage chain. The stage ref that replaces a workflow gate must stay
// protected by that gate's instance name even when its version/params differ;
// otherwise a pinned run can silently turn on_reject:stay back into $stop.
func TestRunStageOverrideCannotWeakenWorkflowGateByInstance(t *testing.T) {
	trusted := runStageOverrideConfig()
	submitted := runStageOverrideConfig()
	// The saved run places the weaker workflow-version gate on the stage.
	submitted.Stages[0].ExitGateGroups = workflow.GateGroups{{runStageOverrideWorkflowRef()}}

	for name, problems := range map[string][]string{
		"publish": submitted.UntrustedCommandProblems(trusted),
		"run":     submitted.untrustedRunProblems(trusted),
	} {
		joined := strings.Join(problems, "\n")
		if !strings.Contains(joined, "нельзя убрать или ослабить") || !strings.Contains(joined, gatelib.GateHumanApproval) {
			t.Fatalf("%s: weakening a workflow gate through a stage override must be refused: %v", name, problems)
		}
	}
}

// TestRunUniqueStageProcessGateStaysEditable is the other side of the P2 fix: a
// stage gate whose instance name is not one of the workflow gates may differ
// between a pinned run and a later file edit, so the run must stay clean.
func TestRunUniqueStageProcessGateStaysEditable(t *testing.T) {
	trusted := runStageOverrideConfig()
	trusted.Stages[0].ExitGateGroups = workflow.GateGroups{
		{stageOverrideWorkflowRef()},
		{{Gate: gatelib.GateVerdictMarker, Version: 1, Name: "unique_marker"}},
	}
	submitted := runStageOverrideConfig()
	// The pinned run keeps the workflow-gate override but drops the unique-name
	// stage process gate: a legal process edit of the saved run.
	if problems := submitted.untrustedRunProblems(trusted); len(problems) != 0 {
		t.Fatalf("a unique-name stage process gate may differ in a pinned run: %v", problems)
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
