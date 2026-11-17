package gatelib

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// entry_human_test.go — finding D: a human gate is the transition approval of
// a stage, not an input check. There is no runner for it in the entry wave, so
// validation must reject it with a clear message and the entry path.

func TestResolveGateRejectsHumanGateInEntryContext(t *testing.T) {
	lib := Default()
	ref := workflow.GateRef{Gate: GateHumanApproval, Version: 1}
	_, err := lib.ResolveGate(ref, GateContext{Artifact: workflow.ArtifactTypeMD, VerdictOnly: true})
	if err == nil || !strings.Contains(err.Error(), "не может быть входным гейтом") {
		t.Fatalf("a human gate must be rejected as an entry gate: %v", err)
	}
	// The exit path still resolves it: VerdictOnly false is not an entry chain.
	if _, err := lib.ResolveGate(ref, GateContext{Artifact: workflow.ArtifactTypeMD}); err != nil {
		t.Fatalf("a human gate must remain valid as an exit gate: %v", err)
	}
}

func TestResolveGateGroupsNamesHumanEntryPath(t *testing.T) {
	lib := Default()
	groups := workflow.GateGroups{{{Gate: GateHumanApproval, Version: 1}}}
	_, errs := lib.ResolveGateGroups(groups, GateContext{VerdictOnly: true})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "entry_gate_groups[0][0]") ||
		!strings.Contains(errs[0].Error(), "не может быть входным гейтом") {
		t.Fatalf("the entry path and a clear message are required: %v", errs)
	}
}
