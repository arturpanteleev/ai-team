package config

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// trusted_workflow_tighten_test.go — finding B: web publish and run must not
// drop or weaken workflow gates, change their params/mode, or switch to a
// weaker active workflow; finding C: exit and entry gates of a stage compared
// separately, so moving a gate between them is a weakening.

func requiredSectionsRef(section string) workflow.GateRef {
	return workflow.GateRef{
		Gate:    gatelib.GateRequiredSections,
		Version: 1,
		Params:  map[string]any{"sections": []string{section}},
	}
}

func assertWorkflowTightenRefused(t *testing.T, submitted, trusted *Config, want string) {
	t.Helper()
	for name, problems := range map[string][]string{
		"publish": submitted.UntrustedCommandProblems(trusted),
		"run":     submitted.untrustedRunProblems(trusted),
	} {
		joined := strings.Join(problems, "\n")
		if !strings.Contains(joined, "нельзя убрать или ослабить") || !strings.Contains(joined, want) {
			t.Fatalf("%s: want a workflow gate problem naming %q, got: %v", name, want, problems)
		}
	}
}

func TestWorkflowTightenAcceptsIdenticalAndStricter(t *testing.T) {
	trusted := v7TrustedBase()
	trusted.Workflows[0].ExitGateGroups = workflow.GateGroups{{requiredSectionsRef("A")}}
	submitted := v7TrustedBase()
	submitted.Workflows[0].ExitGateGroups = workflow.GateGroups{{requiredSectionsRef("A")}}
	if problems := submitted.UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Fatalf("an identical workflow block must be accepted: %v", problems)
	}
	// Adding a gate only tightens.
	submitted.Workflows[0].ExitGateGroups = workflow.GateGroups{
		{requiredSectionsRef("A")},
		{{Gate: gatelib.GateVerdictMarker, Version: 1}},
	}
	if problems := submitted.UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Fatalf("adding a workflow gate must be accepted: %v", problems)
	}
}

func TestWorkflowTightenRefusesRemovedExitGate(t *testing.T) {
	trusted := v7TrustedBase()
	trusted.Workflows[0].ExitGateGroups = workflow.GateGroups{{requiredSectionsRef("A")}}
	submitted := v7TrustedBase() // dropped the workflow exit gate
	assertWorkflowTightenRefused(t, submitted, trusted, gatelib.GateRequiredSections)
}

func TestWorkflowTightenRefusesWeakenedParams(t *testing.T) {
	trusted := v7TrustedBase()
	trusted.Workflows[0].ExitGateGroups = workflow.GateGroups{{requiredSectionsRef("A")}}
	submitted := v7TrustedBase()
	submitted.Workflows[0].ExitGateGroups = workflow.GateGroups{{requiredSectionsRef("B")}}
	assertWorkflowTightenRefused(t, submitted, trusted, gatelib.GateRequiredSections)
}

func TestWorkflowTightenRefusesWeakenedOverride(t *testing.T) {
	trusted := v7TrustedBase()
	trusted.Workflows[0].ExitGateGroups = workflow.GateGroups{{requiredSectionsRef("A")}}
	submitted := v7TrustedBase()
	submitted.Workflows[0].ExitGateGroups = workflow.GateGroups{{requiredSectionsRef("A")}}
	submitted.Workflows[0].ExitGateOverrides = map[string]workflow.GateRef{
		gatelib.GateRequiredSections: requiredSectionsRef("B"),
	}
	assertWorkflowTightenRefused(t, submitted, trusted, gatelib.GateRequiredSections)
}

func TestWorkflowTightenRefusesWeakerActiveWorkflow(t *testing.T) {
	// The default workflow has no gates; "review" declares its own exit gate
	// (non-default workflows inherit from the default, so a weaker workflow is
	// the one without the declared gate). Switching active from "review" to the
	// default drops the file's gate for every stage of the run.
	trusted := v7TrustedBase()
	trusted.Workflows = append(trusted.Workflows, WorkflowMeta{
		ID: "review", Title: "Review", Version: 1,
		Graphs:         []WorkflowGraph{{ID: "review", Template: "v7-trusted"}},
		ExitGateGroups: workflow.GateGroups{{requiredSectionsRef("A")}},
	})
	trusted.ActiveWorkflow = "review"

	submitted := v7TrustedBase()
	submitted.Workflows = append([]WorkflowMeta(nil), trusted.Workflows...)
	submitted.ActiveWorkflow = DefaultWorkflowID // weaker chain: no exit gate
	assertWorkflowTightenRefused(t, submitted, trusted, gatelib.GateRequiredSections)
}

func TestStageGateMoveBetweenChainsIsRefused(t *testing.T) {
	// Finding C: exit→entry and entry→exit moves must not count as preserving a
	// file-level gate — they check a different artifact.
	for _, tc := range []struct {
		name  string
		build func(*Config)
		move  func(*Config)
	}{
		{
			name:  "exit to entry",
			build: func(c *Config) { c.Stages[0].ExitGateGroups = workflow.GateGroups{{requiredSectionsRef("A")}} },
			move: func(c *Config) {
				c.Stages[0].ExitGateGroups = nil
				c.Stages[0].EntryGateGroups = workflow.GateGroups{{requiredSectionsRef("A")}}
			},
		},
		{
			name:  "entry to exit",
			build: func(c *Config) { c.Stages[0].EntryGateGroups = workflow.GateGroups{{requiredSectionsRef("A")}} },
			move: func(c *Config) {
				c.Stages[0].EntryGateGroups = nil
				c.Stages[0].ExitGateGroups = workflow.GateGroups{{requiredSectionsRef("A")}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trusted := v7TrustedBase()
			tc.build(trusted)
			submitted := v7TrustedBase()
			tc.move(submitted)
			problems := submitted.UntrustedCommandProblems(trusted)
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, "нельзя убрать или ослабить") || !strings.Contains(joined, gatelib.GateRequiredSections) {
				t.Fatalf("moving a gate between exit and entry must be refused: %v", problems)
			}
		})
	}
}

func TestInputArtifactsTightenRefusesRemovedType(t *testing.T) {
	trusted := v7TrustedBase()
	trusted.Stages[0].InputArtifacts = []workflow.ArtifactType{workflow.ArtifactTypeMD, workflow.ArtifactTypeLink}
	submitted := v7TrustedBase()
	submitted.Stages[0].InputArtifacts = []workflow.ArtifactType{workflow.ArtifactTypeMD}
	problems := submitted.UntrustedCommandProblems(trusted)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "input_artifacts") {
		t.Fatalf("removing a declared input artifact type must be refused: %v", problems)
	}
}
