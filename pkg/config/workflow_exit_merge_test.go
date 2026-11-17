package config

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// workflow_exit_merge_test.go — finding 6 and 7: ExitGateGroupsFor always merges
// the workflow exit chain with the stage's effective chain, and self-extends is
// reported as a clear cycle.

func TestExitGateGroupsForMergesWorkflowIntoStageWithoutChain(t *testing.T) {
	workflows := `workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    exit_gate_groups:
      - [human_approval@1]
`
	cfg := mustParse(t, v7Doc(workflows, ""))
	// The `done` stage (result: approve) has no exit_gate_groups and its legacy
	// translation is empty; the workflow human_approval must still be present.
	merged, err := cfg.ExitGateGroupsFor(cfg.Stages[1])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range merged.Refs() {
		if ref.Gate == "human_approval" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the workflow exit gate must apply to a stage without its own chain: %+v", refsOf(merged))
	}
}

func TestExitGateGroupsForMergesWorkflowWithLegacyStageChain(t *testing.T) {
	// A stage that still carries the legacy v5 fields (required_sections) has a
	// translated effective chain; the workflow exit gates must be prepended to it.
	cfg := &Config{
		SchemaVersion: WorkflowSchemaVersion,
		Template:      "gates",
		Title:         "Gates",
		Workflows: []WorkflowMeta{{
			ID: DefaultWorkflowID, Title: "SDLC", Version: 1,
			Graphs:         []WorkflowGraph{{ID: "sdlc", Template: "gates"}},
			ExitGateGroups: workflow.GateGroups{{workflow.GateRef{Gate: gatelib.GateVerdictMarker, Version: 1}}},
		}},
		Stages: []TemplateStage{
			{ID: "product_spec", Title: "Spec", Function: "po", Result: "md", RequiredSections: []string{"Критерии приёмки"}},
		},
	}
	merged, err := cfg.ExitGateGroupsFor(cfg.Stages[0])
	if err != nil {
		t.Fatal(err)
	}
	refs := merged.Refs()
	if len(refs) == 0 || refs[0].Gate != "verdict_marker" {
		t.Fatalf("workflow exit gates must prepend the legacy translation: %+v", refsOf(merged))
	}
	foundSections := false
	for _, ref := range refs {
		if ref.Gate == "required_sections" {
			foundSections = true
		}
	}
	if !foundSections {
		t.Fatalf("the legacy required_sections translation must survive the merge: %+v", refsOf(merged))
	}
}

func TestWorkflowSelfExtendsIsRejected(t *testing.T) {
	for _, tt := range []struct{ name, workflows string }{
		{
			"default workflow",
			`workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    extends: idea-to-prod
`,
		},
		{
			"child workflow",
			`workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
  - id: child
    title: Child
    version: 1
    extends: child
    graphs: [{id: write, template: gates}]
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustParse(t, v7Doc(tt.workflows, ""))
			err := cfg.Validate(gateAgents())
			if err == nil || !strings.Contains(err.Error(), "самого себя") {
				t.Fatalf("self-extends must be rejected with a clear cycle error: %v", err)
			}
		})
	}
}

func TestExitGateGroupsForStillResolvesStageOverride(t *testing.T) {
	workflows := `workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    exit_gate_groups:
      - - gate: required_sections
          version: 1
          name: sections
          params: {sections: [Workflow]}
`
	cfg := mustParse(t, v7Doc(workflows, `    exit_gate_groups:
      - - gate: required_sections
          version: 1
          name: sections
          params: {sections: [Stage]}
`))
	merged, err := cfg.ExitGateGroupsFor(cfg.Stages[0])
	if err != nil {
		t.Fatal(err)
	}
	var sections workflow.GateRef
	for _, ref := range merged.Refs() {
		if ref.InstanceName() == "sections" {
			sections = ref
		}
	}
	if got := sectionParam(t, sections); got != "Stage" {
		t.Fatalf("the stage ref must override the workflow ref by name: %v", got)
	}
}
