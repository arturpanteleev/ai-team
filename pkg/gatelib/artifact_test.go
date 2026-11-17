package gatelib

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// artifact_test.go — ARC-01/ARC-02: the gate contract types its input artifact
// (md or link) and distinguishes verdict-only gates from artifact-producing
// ones. Entry gates may only use verdict-only definitions.

func TestBuiltinGatesDeclareInputArtifactTypes(t *testing.T) {
	lib := Default()
	tests := []struct {
		id      string
		accepts workflow.ArtifactType
		rejects workflow.ArtifactType
	}{
		{GateRequiredSections, workflow.ArtifactTypeMD, workflow.ArtifactTypeLink},
		{GateVerdictMarker, workflow.ArtifactTypeMD, workflow.ArtifactTypeLink},
		{GateRequiredPhrases, workflow.ArtifactTypeMD, workflow.ArtifactTypeLink},
		{GatePRExists, workflow.ArtifactTypeLink, workflow.ArtifactTypeMD},
		{GateAgentReview, workflow.ArtifactTypeMD, ""},
	}
	for _, tt := range tests {
		def, err := lib.Lookup(tt.id, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !def.AcceptsArtifact(tt.accepts) {
			t.Errorf("%s must accept %s", tt.id, tt.accepts)
		}
		if tt.rejects != "" && def.AcceptsArtifact(tt.rejects) {
			t.Errorf("%s must reject %s", tt.id, tt.rejects)
		}
		if def.Output.ProducesArtifact() {
			t.Errorf("%s must be verdict-only in the first iteration", tt.id)
		}
	}
}

func TestResolveGateChecksArtifactAndVerdictOnly(t *testing.T) {
	lib := NewRegistry()
	lib.MustRegister(workflow.GateDefinition{
		ID: "mk_artifact", Version: 1, Kind: workflow.GateKindBuiltin, Title: "Artifact maker",
		InputArtifacts: []workflow.ArtifactType{workflow.ArtifactTypeMD},
		Output:         workflow.GateOutputArtifact,
	})
	ref := workflow.GateRef{Gate: "mk_artifact", Version: 1}
	if _, err := lib.ResolveGate(ref, GateContext{Artifact: workflow.ArtifactTypeMD, VerdictOnly: true}); err == nil ||
		!strings.Contains(err.Error(), "не может быть входным") {
		t.Fatalf("artifact-producing gate must be rejected as entry gate: %v", err)
	}
	if _, err := lib.ResolveGate(ref, GateContext{Artifact: workflow.ArtifactTypeMD}); err != nil {
		t.Fatalf("artifact output is valid for an exit gate: %v", err)
	}
	if _, err := lib.ResolveGate(ref, GateContext{Artifact: workflow.ArtifactTypeLink}); err == nil ||
		!strings.Contains(err.Error(), "не принимает артефакт типа link") {
		t.Fatalf("artifact type must be checked: %v", err)
	}
}

func TestRegisterRejectsUnknownArtifactTypeAndOutput(t *testing.T) {
	lib := NewRegistry()
	for _, def := range []workflow.GateDefinition{
		{ID: "bad_artifact", Version: 1, Kind: workflow.GateKindBuiltin, Title: "Bad", InputArtifacts: []workflow.ArtifactType{"png"}},
		{ID: "bad_output", Version: 1, Kind: workflow.GateKindBuiltin, Title: "Bad", Output: "image"},
	} {
		if err := lib.Register(def); err == nil {
			t.Fatalf("definition %s must be rejected", def.ID)
		}
	}
}

func TestResolveGateGroupsNamesEntryPath(t *testing.T) {
	lib := Default()
	groups := workflow.GateGroups{{{Gate: GateVerdictMarker, Version: 99}}}
	_, errs := lib.ResolveGateGroups(groups, GateContext{Artifact: workflow.ArtifactTypeMD, VerdictOnly: true})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "entry_gate_groups[0][0]") {
		t.Fatalf("entry path missing: %v", errs)
	}
}

func TestNewArtifactTypesAreImmutableValues(t *testing.T) {
	var phase workflow.GatePhase
	if phase.Effective() != workflow.GatePhaseExit {
		t.Fatalf("empty phase must mean exit, got %q", phase.Effective())
	}
	if !workflow.GatePhaseEntry.Valid() || workflow.GatePhase("later").Valid() {
		t.Fatal("phase validation broken")
	}
	if !workflow.ArtifactTypeMD.Valid() || workflow.ArtifactType("png").Valid() {
		t.Fatal("artifact validation broken")
	}
}
