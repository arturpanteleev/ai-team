package config

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// v7Doc wraps a stage body in a minimal, valid schema-v7 config. A v7 file
// must declare the workflow block it is named after.
func v7Doc(workflows, stageBody string) string {
	return `schema_version: 7
template: gates
title: Gates
workflow: idea-to-prod
` + workflows + `stages:
  - id: product_spec
    title: Spec
    function: po
    result: md
` + stageBody + `  - id: done
    title: Done
    function: bo
    result: approve
`
}

const defaultWorkflowV7 = `workflows:
  - id: idea-to-prod
    title: Default SDLC
    description: Standard flow
    version: 1
    graphs:
      - id: sdlc
        title: SDLC
        template: gates
`

func refsOf(groups workflow.GateGroups) []string {
	var out []string
	for _, ref := range groups.Refs() {
		out = append(out, ref.Key()+":"+ref.InstanceName())
	}
	return out
}

func TestEntryGateGroupsNeedV7AndValidate(t *testing.T) {
	legacy := v6Doc("    input_artifacts: [md]\n    entry_gate_groups:\n      - [verdict_marker@1]\n")
	if _, err := ParseYAML([]byte(legacy)); err == nil || !strings.Contains(err.Error(), "entry_gate_groups доступен со schema_version 7") {
		t.Fatalf("v6 entry gates must be rejected: %v", err)
	}

	cfg := mustParse(t, v7Doc(defaultWorkflowV7, `    input_artifacts: [md]
    entry_gate_groups:
      - - gate: required_sections
          version: 1
          name: intro_sections
          params: {sections: ["Критерии приёмки"]}
      - [verdict_marker@1]
`))
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatalf("valid v7 entry chain rejected: %v", err)
	}
	if got := refsOf(cfg.Stages[0].EntryGateGroups); strings.Join(got, ",") != "required_sections@1:intro_sections,verdict_marker@1:verdict_marker" {
		t.Fatalf("entry refs: %v", got)
	}
}

func TestEntryGateValidationRejectsBadRefs(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"unknown version", "    entry_gate_groups:\n      - [verdict_marker@9]\n", "нет версии 9"},
		{"floating version", "    entry_gate_groups:\n      - [verdict_marker]\n", "без версии"},
		{"unknown param", "    entry_gate_groups:\n      - - gate: verdict_marker\n          version: 1\n          params: {sections: [A]}\n", "не принимает параметров"},
		{"wrong artifact type", "    input_artifacts: [md]\n    entry_gate_groups:\n      - [pr_exists@1]\n", "не принимает артефакт типа md"},
		{"bad artifact type", "    input_artifacts: [png]\n    entry_gate_groups:\n      - [verdict_marker@1]\n", "неизвестный тип"},
		{"empty group", "    entry_gate_groups:\n      - []\n", "группа не может быть пустой"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := v7Doc(defaultWorkflowV7, tt.body)
			cfg, err := ParseYAML([]byte(data))
			if err == nil {
				err = cfg.Validate(gateAgents())
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

func TestTwoWorkflowsWithDifferentGatesLoadTogether(t *testing.T) {
	workflows := `workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    entry_gate_groups:
      - [verdict_marker@1]
    exit_gate_groups:
      - [human_approval@1]
  - id: content-factory
    title: Content
    version: 1
    graphs: [{id: write, template: gates}]
    entry_gate_groups:
      - - gate: required_sections
          version: 1
          name: brief_sections
          params: {sections: ["Критерии приёмки"]}
`
	cfg := mustParse(t, v7Doc(workflows, ""))
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatalf("two workflows must load together: %v", err)
	}
	sdlc, err := cfg.WorkflowGateGroups("idea-to-prod", true)
	if err != nil {
		t.Fatal(err)
	}
	content, err := cfg.WorkflowGateGroups("content-factory", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(refsOf(sdlc), ",") != "verdict_marker@1:verdict_marker" {
		t.Fatalf("sdlc entry gates: %v", refsOf(sdlc))
	}
	if strings.Join(refsOf(content), ",") != "required_sections@1:brief_sections" {
		t.Fatalf("content entry gates: %v", refsOf(content))
	}
	cfg.ActiveWorkflow = "content-factory"
	effective, err := cfg.EntryGateGroupsFor(cfg.Stages[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(refsOf(effective), ",") != "required_sections@1:brief_sections" {
		t.Fatalf("active workflow entry gates: %v", refsOf(effective))
	}
	pins, err := cfg.GatePins()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, pin := range pins {
		seen[pin.Gate] = true
	}
	if !seen["verdict_marker"] || !seen["required_sections"] {
		t.Fatalf("pins must cover both workflows: %+v", pins)
	}
}

func TestWorkflowGateInheritanceAndPointOverride(t *testing.T) {
	workflows := `workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    entry_gate_groups:
      - - gate: required_sections
          version: 1
          name: sections
          params: {sections: ["Критерии приёмки"]}
      - [verdict_marker@1]
  - id: inherited
    title: Child
    version: 1
    extends: idea-to-prod
    entry_gate_overrides:
      sections:
        gate: required_sections
        version: 1
        name: sections
        params: {sections: ["Другое"]}
  - id: replaced
    title: Replaced
    version: 1
    graphs: [{id: write, template: gates}]
    entry_gate_groups:
      - [verdict_marker@1]
`
	cfg := mustParse(t, v7Doc(workflows, ""))
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatalf("inheritance config invalid: %v", err)
	}
	inherited, err := cfg.WorkflowGateGroups("inherited", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(refsOf(inherited), ","); got != "required_sections@1:sections,verdict_marker@1:verdict_marker" {
		t.Fatalf("inherited chain: %v", got)
	}
	sections := inherited.Refs()[0]
	if got := sectionParam(t, sections); got != "Другое" {
		t.Fatalf("point override not applied: %v", got)
	}
	replaced, err := cfg.WorkflowGateGroups("replaced", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(refsOf(replaced), ","); got != "verdict_marker@1:verdict_marker" {
		t.Fatalf("declared chain must replace inherited: %v", got)
	}
}

func sectionParam(t *testing.T, ref workflow.GateRef) string {
	t.Helper()
	items, _ := ref.Params["sections"].([]any)
	if len(items) == 0 {
		t.Fatalf("sections param missing: %+v", ref.Params)
	}
	text, _ := items[0].(string)
	return text
}

func TestWorkflowVersionConflictsAreExplained(t *testing.T) {
	tests := []struct {
		name, workflows, want string
	}{
		{
			"same gate two versions",
			`workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    entry_gate_groups:
      - - {gate: agent_review, version: 1, name: review_v1, params: {agent: spec-checker}}
      - - {gate: agent_review, version: 2, name: review_v2, params: {agent: spec-checker}}
`,
			"конфликт версий гейта agent_review: 1 и 2",
		},
		{
			"duplicate workflow id",
			`workflows:
  - id: idea-to-prod
    title: First
    version: 1
    graphs: [{id: sdlc, template: gates}]
  - id: idea-to-prod
    title: Second
    version: 2
    graphs: [{id: sdlc, template: gates}]
`,
			"id \"idea-to-prod\" повторяется (версии 1 и 2)",
		},
		{
			"unknown override target",
			`workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    entry_gate_overrides:
      ghost: {gate: verdict_marker, version: 1}
`,
			"переопределение гейта \"ghost\" не найдено",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustParse(t, v7Doc(tt.workflows, ""))
			err := cfg.Validate(gateAgents())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

func TestOldV5AndV6ConfigsStillLoadWithDefaultWorkflow(t *testing.T) {
	v5 := strings.Replace(v6Doc("    check:\n      kind: hard\n      rules: [verdict_marker]\n"), "schema_version: 6", "schema_version: 5", 1)
	for name, doc := range map[string]string{"v5": v5, "v6": v6Doc("    exit_gate_groups:\n      - [verdict_marker@1]\n")} {
		t.Run(name, func(t *testing.T) {
			cfg := mustParse(t, doc)
			if err := cfg.Validate(gateAgents()); err != nil {
				t.Fatalf("old config must keep validating: %v", err)
			}
			wf := cfg.ActiveWorkflowMeta()
			if wf == nil || wf.ID != DefaultWorkflowID || wf.Version != 1 {
				t.Fatalf("default workflow not synthesized: %+v", wf)
			}
			if len(cfg.Workflows) != 0 {
				t.Fatalf("old config must not be rewritten at parse time: %+v", cfg.Workflows)
			}
			entry, err := cfg.EntryGateGroupsFor(cfg.Stages[0])
			if err != nil || len(entry) != 0 {
				t.Fatalf("old config has no entry gates: %v %v", entry, err)
			}
		})
	}
}

func TestV6ToV7MigrationRoundTrip(t *testing.T) {
	base := mustParse(t, v6Doc("    exit_gate_groups:\n      - [verdict_marker@1]\n"))
	if err := base.Validate(gateAgents()); err != nil {
		t.Fatal(err)
	}
	v7 := base.MigrateToWorkflowSchema()
	if v7.SchemaVersion != WorkflowSchemaVersion || len(v7.Workflows) == 0 {
		t.Fatalf("migration did not materialize the workflow: %+v", v7)
	}
	data, err := v7.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "schema_version: 7") || !strings.Contains(string(data), "workflows:") {
		t.Fatalf("migrated YAML lacks v7 block:\n%s", data)
	}
	reparsed := mustParse(t, string(data))
	if err := reparsed.Validate(gateAgents()); err != nil {
		t.Fatalf("migrated v7 config invalid: %v\n%s", err, data)
	}
	if got := refsOf(reparsed.Stages[0].EffectiveGateGroups()); strings.Join(got, ",") != "verdict_marker@1:verdict_marker" {
		t.Fatalf("stage chain lost in migration: %v", got)
	}
}

func TestStageEntryGatesOverrideWorkflowGatesByName(t *testing.T) {
	workflows := `workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs: [{id: sdlc, template: gates}]
    entry_gate_groups:
      - - gate: required_sections
          version: 1
          name: sections
          params: {sections: ["Workflow"]}
      - [verdict_marker@1]
`
	cfg := mustParse(t, v7Doc(workflows, `    input_artifacts: [md]
    entry_gate_groups:
      - - gate: required_sections
          version: 1
          name: sections
          params: {sections: ["Stage"]}
`))
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatal(err)
	}
	effective, err := cfg.EntryGateGroupsFor(cfg.Stages[0])
	if err != nil {
		t.Fatal(err)
	}
	refs := effective.Refs()
	if len(refs) != 2 {
		t.Fatalf("stage override and workflow inheritance: %+v", refs)
	}
	byName := map[string]workflow.GateRef{}
	for _, ref := range refs {
		byName[ref.InstanceName()] = ref
	}
	if got := sectionParam(t, byName["sections"]); got != "Stage" {
		t.Fatalf("stage ref must override workflow ref by name: %v", got)
	}
	if byName["verdict_marker"].Gate != "verdict_marker" {
		t.Fatalf("unrelated workflow gate must be inherited: %+v", refs)
	}
}
