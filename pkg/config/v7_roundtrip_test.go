package config

import (
	"reflect"
	"strings"
	"testing"
)

// cfg01V7YAML is a schema-v7 template as published from the workflow card:
// two workflows, one with a real two-graph chain and explicit stage subsets,
// workflow/stage gate chains and a stage without any gate chain at all.
const cfg01V7YAML = `schema_version: 7
template: idea-to-prod
title: Восемь этапов
workflow: bugfix
workflows:
  - id: idea-to-prod
    title: От идеи до продакшена
    version: 1
    graphs:
      - id: sdlc
        title: SDLC
        template: idea-to-prod
    exit_gate_groups:
      - [human_approval@1]
  - id: bugfix
    title: Исправление дефекта
    description: Короткий путь через ревью и реализацию
    version: 2
    extends: idea-to-prod
    graphs:
      - id: bug-fix
        title: Исправление
        template: idea-to-prod
        stages: [implementation, qa]
        entry: implementation
      - id: bug-triage
        title: Триаж
        template: idea-to-prod
        stages: [code_review]
        transitions: [{from: bug-triage, to: bug-fix, on: passed}]
    exit_gate_overrides:
      human_approval: {gate: human_approval, version: 1, name: bugfix_approval}
stages:
  - id: code_review
    title: Код-ревью
    function: reviewer
    result: md
    executor: human
    input_artifacts: [md]
    entry_gate_groups:
      - [verdict_marker@1]
    exit_gate_groups:
      - - gate: required_sections
          version: 1
          params: {sections: [Решение]}
      - [verdict_marker@1, human_approval@1]
  - id: implementation
    title: Реализация
    function: developer
    result: link
    link_kind: pr
    executor: agent
    agent: coder
    exit_gate_groups:
      - [pr_exists@1, pr_open@1]
      - [human_approval@1]
  - id: qa
    title: Тестирование
    function: qa
    result: md
    executor: human
`

func TestV7MarshalRoundTripDropsLegacyStageKeys(t *testing.T) {
	cfg := mustParse(t, cfg01V7YAML)
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatalf("source v7 config invalid: %v", err)
	}

	// The workflow-card process patch edits metadata in memory and rewrites
	// the whole file. It may leave derived v5 fields on stages; marshalling a
	// v7 config must never emit them.
	for i := range cfg.Stages {
		cfg.Stages[i].RequiredSections = []string{"Устаревшая секция"}
		cfg.Stages[i].Check = &TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}}
		cfg.Stages[i].Confirm = "required"
	}

	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, legacy := range []string{"required_sections:", "check:", "confirm:"} {
		if strings.Contains(text, "    "+legacy) {
			t.Fatalf("v7 output still has legacy stage key %q:\n%s", legacy, text)
		}
	}
	for _, want := range []string{"schema_version: 7", "workflows:", "stages:", "entry_gate_groups:", "exit_gate_groups:", "- id: bug-triage", "- code_review", "transitions:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("v7 output lost %q:\n%s", want, text)
		}
	}

	reparsed, err := ParseYAML(data)
	if err != nil {
		t.Fatalf("ParseYAML of marshalled v7 config: %v\n%s", err, data)
	}
	if err := reparsed.Validate(gateAgents()); err != nil {
		t.Fatalf("marshalled v7 config invalid: %v\n%s", err, data)
	}
	assertCfg01V7Equal(t, cfg, reparsed)

	// Golden stability: a second cycle must produce byte-identical YAML.
	again, err := reparsed.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != text {
		t.Fatalf("v7 round-trip is not stable:\nfirst:\n%s\nsecond:\n%s", text, again)
	}
}

func TestV7MarshalRoundTripAfterProcessPatchFieldStripping(t *testing.T) {
	cfg := mustParse(t, cfg01V7YAML)
	// The workflow card's structured process patch edits only a workflow's
	// title/description in memory and publishes the whole config again.
	cfg.Workflows[1].Title = "Исправление дефекта (обновлено)"
	cfg.Workflows[1].Description = "Новое описание короткого пути"
	// This is exactly what the web process patch does before Marshal: it drops
	// the derived Confirm so the v7 document has no legacy spelling. A stage
	// without a gate chain must stay gate-free; stripping Confirm must not
	// synthesize a human_approval from the result type.
	for i := range cfg.Stages {
		cfg.Stages[i].RequiredSections = nil
		cfg.Stages[i].Check = nil
		cfg.Stages[i].Confirm = ""
	}
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "    confirm:") {
		t.Fatalf("v7 output still has confirm:\n%s", data)
	}
	reparsed, err := ParseYAML(data)
	if err != nil {
		t.Fatalf("ParseYAML after process patch: %v\n%s", err, data)
	}
	if got := refsOf(reparsed.Stages[2].EffectiveGateGroups()); len(got) != 0 {
		t.Fatalf("gate-free stage grew gates after round-trip: %v", got)
	}
	if err := reparsed.Validate(gateAgents()); err != nil {
		t.Fatalf("process patch config invalid: %v\n%s", err, data)
	}
	if got := reparsed.Workflows[1]; got.Title != cfg.Workflows[1].Title || got.Description != cfg.Workflows[1].Description {
		t.Fatalf("published process patch lost workflow metadata: %+v", got)
	}
}

func assertCfg01V7Equal(t *testing.T, want, got *Config) {
	t.Helper()
	if got.SchemaVersion != want.SchemaVersion || got.Template != want.Template || got.Title != want.Title || got.ActiveWorkflow != want.ActiveWorkflow {
		t.Fatalf("top-level fields differ: want %d/%q/%q/%q got %d/%q/%q/%q",
			want.SchemaVersion, want.Template, want.Title, want.ActiveWorkflow,
			got.SchemaVersion, got.Template, got.Title, got.ActiveWorkflow)
	}
	if !reflect.DeepEqual(got.Workflows, want.Workflows) {
		t.Fatalf("workflows differ:\nwant %+v\ngot  %+v", want.Workflows, got.Workflows)
	}
	if len(got.Stages) != len(want.Stages) {
		t.Fatalf("stages count: want %d got %d", len(want.Stages), len(got.Stages))
	}
	for i := range want.Stages {
		a, b := want.Stages[i], got.Stages[i]
		if a.ID != b.ID || a.Result != b.Result || a.Executor != b.Executor {
			t.Fatalf("stages[%d] identity differs: %+v vs %+v", i, a, b)
		}
		if !reflect.DeepEqual(a.EffectiveGateGroups(), b.EffectiveGateGroups()) {
			t.Fatalf("stages[%d] (%s) exit chain differs: %v vs %v", i, a.ID, a.EffectiveGateGroups(), b.EffectiveGateGroups())
		}
		if !reflect.DeepEqual(a.EntryGateGroups, b.EntryGateGroups) || !reflect.DeepEqual(a.InputArtifacts, b.InputArtifacts) {
			t.Fatalf("stages[%d] (%s) entry contract differs: %+v/%v vs %+v/%v",
				i, a.ID, a.EntryGateGroups, a.InputArtifacts, b.EntryGateGroups, b.InputArtifacts)
		}
	}
	// The compiled plan must still be the two-graph bugfix chain.
	plan, err := got.workflowGraphPlan(*got.workflowByID(BugfixWorkflowID))
	if err != nil {
		t.Fatalf("workflow plan: %v", err)
	}
	ids := append([]string(nil), plan.stageIDs...)
	if !reflect.DeepEqual(ids, []string{"code_review", "implementation", "qa"}) {
		t.Fatalf("graph stage subsets lost in round-trip: %v", ids)
	}
}
