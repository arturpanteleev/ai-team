package config

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// trusted_commands_workflow_gate_override_test.go — review P1: a workflow
// command gate (external_program) can be replaced in the effective merged chain
// by a stage ref with the same instance name but a different kind (for example
// human_approval). ExitGateGroupsFor/EntryGateGroupsFor drop the command gate in
// that case, so the workflow-only, stage-only and command-introduction checks
// all miss the weakening; the merged-chain tighten check must catch it.

// workflowCommandGateRef is external_program@1 with a fixed instance name
// ("wf_cmd"): the trusted workflow exit gate of the reproduction.
func workflowCommandGateRef() workflow.GateRef {
	return workflow.GateRef{
		Gate:    gatelib.GateExternalProgram,
		Version: 1,
		Name:    "wf_cmd",
		Params:  map[string]any{"command": []string{"sh", "-c", "id"}},
	}
}

// workflowCommandGateConfig declares the external_program gate on the default
// workflow's exit chain and inherits it to every stage.
func workflowCommandGateConfig() *Config {
	cfg := v7TrustedBase()
	// Confirm: auto keeps the legacy human_approval@1 translation out of the
	// stage's own chain, so the test isolates the workflow command gate.
	cfg.Stages[0].Confirm = "auto"
	cfg.Workflows[0].ExitGateGroups = workflow.GateGroups{{workflowCommandGateRef()}}
	return cfg
}

// TestWorkflowCommandGateCannotBeReplacedByStageOverride: a web submission
// that overrides the workflow's external_program gate at stage level with a
// human_approval ref of the same instance name must be refused by publish and
// run: the stage ref replaces the command gate in the effective chain.
func TestWorkflowCommandGateCannotBeReplacedByStageOverride(t *testing.T) {
	trusted := workflowCommandGateConfig()
	submitted := workflowCommandGateConfig()
	submitted.Stages[0].ExitGateGroups = workflow.GateGroups{{
		{Gate: gatelib.GateHumanApproval, Version: 1, Name: "wf_cmd"},
	}}

	for name, problems := range map[string][]string{
		"publish": submitted.UntrustedCommandProblems(trusted),
		"run":     submitted.untrustedRunProblems(trusted),
	} {
		joined := strings.Join(problems, "\n")
		want := "гейт wf_cmd (external_program@1) файлового конфига нельзя убрать или ослабить"
		if !strings.Contains(joined, want) {
			t.Fatalf("%s: replacing a workflow command gate through a stage override must be refused by the merged-chain tighten check (%q): %v", name, want, problems)
		}
	}
}

// TestStageOwnCommandGateWithUniqueNameStaysAllowed is the positive side of the
// fix: a stage-own command gate whose instance name is not one of the workflow
// gates is not protected by the merged-chain tighten check, so a pinned run may
// still legitimately differ there (name-only references stay accepted).
func TestStageOwnCommandGateWithUniqueNameStaysAllowed(t *testing.T) {
	trusted := v7TrustedBase()
	trusted.Stages[0].Confirm = "auto"
	trusted.Stages[0].ExitGateGroups = workflow.GateGroups{{
		{
			Gate:    gatelib.GateExternalProgram,
			Version: 1,
			Name:    "stage_only_check",
			Params:  map[string]any{"command": []string{"sh", "-c", "id"}},
		},
	}}
	submitted := v7TrustedBase()
	submitted.Stages[0].Confirm = "auto"
	// The pinned run drops the unique-name stage command gate: a legal process
	// edit, since it is not part of the workflow chain.
	if problems := submitted.untrustedRunProblems(trusted); len(problems) != 0 {
		t.Fatalf("a unique-name stage command gate must stay exempt from the merged-chain tighten check: %v", problems)
	}
}
