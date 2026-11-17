package config

import (
	"strings"
	"testing"
)

// workflow_meta_validation_test.go — finding 5: the controller validates the
// effective chains (after inheritance, overrides and the merge with the stage),
// not only the declared ones. Each rejected example names the workflow/stage
// path.

func TestWorkflowOverrideMissingRequiredParamIsRejected(t *testing.T) {
	workflows := `workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    entry_gate_groups:
      - - gate: required_sections
          version: 1
          name: sections
          params: {sections: [A]}
    entry_gate_overrides:
      sections: {gate: required_sections, version: 1}
`
	cfg := mustParse(t, v7Doc(workflows, ""))
	err := cfg.Validate(gateAgents())
	if err == nil || !strings.Contains(err.Error(), "обязательный параметр") || !strings.Contains(err.Error(), "sections") {
		t.Fatalf("an override dropping a required parameter must be rejected naming it: %v", err)
	}
}

func TestWorkflowOverrideUnknownGateIsRejected(t *testing.T) {
	workflows := `workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    entry_gate_groups:
      - - gate: verdict_marker
          version: 1
          name: marker
    entry_gate_overrides:
      marker: {gate: ghost_gate, version: 1}
`
	cfg := mustParse(t, v7Doc(workflows, ""))
	err := cfg.Validate(gateAgents())
	if err == nil || !strings.Contains(err.Error(), "ghost_gate") {
		t.Fatalf("an override naming an unknown gate must be rejected: %v", err)
	}
}

func TestWorkflowEntryGateWrongInputArtifactIsRejected(t *testing.T) {
	workflows := `workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    entry_gate_groups:
      - [pr_exists@1]
`
	cfg := mustParse(t, v7Doc(workflows, "    input_artifacts: [md]\n"))
	err := cfg.Validate(gateAgents())
	if err == nil || !strings.Contains(err.Error(), "не принимает артефакт типа md") {
		t.Fatalf("a workflow entry gate inapplicable to the stage input must be rejected: %v", err)
	}
}

func TestMergedStageExitHumanApprovalNotLastIsRejected(t *testing.T) {
	workflows := `workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    exit_gate_groups:
      - [human_approval@1]
`
	cfg := mustParse(t, v7Doc(workflows, "    exit_gate_groups:\n      - [verdict_marker@1]\n"))
	err := cfg.Validate(gateAgents())
	if err == nil || !strings.Contains(err.Error(), "human_approval допускается только в последней группе") {
		t.Fatalf("a merged chain with human_approval before an automatic gate must be rejected: %v", err)
	}
}
