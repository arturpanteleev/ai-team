package config

import (
	"strings"
	"testing"
)

// entry_human_validation_test.go — finding D: an entry human_approval is a
// valid library reference but an invalid gate for the entry wave. Validation
// must fail with the entry_gate_groups[i][j] path and a clear message.

func TestValidateRejectsHumanGateAsStageEntryGate(t *testing.T) {
	cfg := mustParse(t, v7Doc(defaultWorkflowV7, `    entry_gate_groups:
      - [human_approval@1]
`))
	err := cfg.Validate(gateAgents())
	if err == nil || !strings.Contains(err.Error(), "entry_gate_groups[0][0]") ||
		!strings.Contains(err.Error(), "не может быть входным гейтом") {
		t.Fatalf("a human entry gate must be rejected with the entry path: %v", err)
	}
}

func TestValidateRejectsHumanGateAsWorkflowEntryGate(t *testing.T) {
	workflows := `workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    entry_gate_groups:
      - [human_approval@1]
`
	cfg := mustParse(t, v7Doc(workflows, ""))
	err := cfg.Validate(gateAgents())
	if err == nil || !strings.Contains(err.Error(), "не может быть входным гейтом") {
		t.Fatalf("a workflow human entry gate must be rejected: %v", err)
	}
}
