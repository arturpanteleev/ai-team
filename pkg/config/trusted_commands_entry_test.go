package config

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// trusted_commands_entry_test.go — finding 2: the web must not introduce a
// command gate (external_program/docker_program) through a stage entry chain, a
// workflow chain or a point override, and it must not drop a file-level entry
// gate.

func externalCommandRef() workflow.GateRef {
	return workflow.GateRef{Gate: gatelib.GateExternalProgram, Version: 1, Params: map[string]any{"command": []string{"sh", "-c", "id"}}}
}

func dockerCommandRef() workflow.GateRef {
	return workflow.GateRef{Gate: gatelib.GateDockerProgram, Version: 1, Params: map[string]any{"image": "alpine@sha256:" + strings.Repeat("a", 64), "command": []string{"id"}}}
}

func v7TrustedBase() *Config {
	return &Config{
		SchemaVersion: WorkflowSchemaVersion,
		Template:      "v7-trusted",
		Title:         "V7 trusted",
		Workflows: []WorkflowMeta{{
			ID: DefaultWorkflowID, Title: "SDLC", Version: 1,
			Graphs: []WorkflowGraph{{ID: "sdlc", Template: "v7-trusted"}},
		}},
		Stages: []TemplateStage{
			{ID: "spec", Title: "Spec", Function: "po", Result: "md", Executor: "human", InputArtifacts: []workflow.ArtifactType{workflow.ArtifactTypeMD}},
		},
	}
}

func assertRefusesCommandGate(t *testing.T, name string, problems []string, gate string) {
	t.Helper()
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), gate) {
		t.Fatalf("%s: a web-set %s command gate must be refused (trusted_config_only): %v", name, gate, problems)
	}
}

func TestWebSubmissionRefusesCommandGateInStageEntryChain(t *testing.T) {
	trusted := v7TrustedBase()
	submitted := v7TrustedBase()
	submitted.Stages[0].EntryGateGroups = workflow.GateGroups{{externalCommandRef()}}
	assertRefusesCommandGate(t, "stage entry", submitted.UntrustedCommandProblems(trusted), "external_program")
	assertRefusesCommandGate(t, "stage entry run", submitted.untrustedRunProblems(trusted), "external_program")
}

func TestWebSubmissionRefusesCommandGateInWorkflowChain(t *testing.T) {
	trusted := v7TrustedBase()
	submitted := v7TrustedBase()
	submitted.Workflows[0].ExitGateGroups = workflow.GateGroups{{dockerCommandRef()}}
	assertRefusesCommandGate(t, "workflow exit", submitted.UntrustedCommandProblems(trusted), "docker_program")
	assertRefusesCommandGate(t, "workflow exit run", submitted.untrustedRunProblems(trusted), "docker_program")
}

func TestWebSubmissionRefusesCommandGateInOverride(t *testing.T) {
	trusted := v7TrustedBase()
	trusted.Workflows[0].EntryGateGroups = workflow.GateGroups{{workflow.GateRef{Gate: gatelib.GateVerdictMarker, Version: 1}}}
	submitted := v7TrustedBase()
	submitted.Workflows[0].EntryGateGroups = workflow.GateGroups{{workflow.GateRef{Gate: gatelib.GateVerdictMarker, Version: 1}}}
	submitted.Workflows[0].EntryGateOverrides = map[string]workflow.GateRef{"verdict_marker": externalCommandRef()}
	assertRefusesCommandGate(t, "entry override", submitted.UntrustedCommandProblems(trusted), "external_program")
	assertRefusesCommandGate(t, "entry override run", submitted.untrustedRunProblems(trusted), "external_program")
}

func TestWebSubmissionCannotRemoveFileEntryGate(t *testing.T) {
	trusted := v7TrustedBase()
	trusted.Stages[0].EntryGateGroups = workflow.GateGroups{{workflow.GateRef{Gate: gatelib.GateVerdictMarker, Version: 1}}}
	submitted := v7TrustedBase() // dropped the entry gate
	problems := submitted.UntrustedCommandProblems(trusted)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "нельзя убрать") {
		t.Fatalf("removing a file-level entry gate must be refused: %v", problems)
	}
}
