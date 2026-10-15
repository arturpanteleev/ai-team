package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
	"gopkg.in/yaml.v3"
)

func gateAgents() fakeLookup {
	return fakeLookup{"analyst": true, "verifier": true, "spec-checker": true, "architect": true, "design-reviewer": true, "reviewer": true, "coder": true, "tester": true, "observer": true}
}

// v6Doc builds a two-stage v6 document around one stage body.
func v6Doc(stageBody string) string {
	return `schema_version: 6
template: gates
title: Gates
stages:
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

func mustParse(t *testing.T, data string) *Config {
	t.Helper()
	cfg, err := ParseYAML([]byte(data))
	if err != nil {
		t.Fatalf("ParseYAML: %v\n%s", err, data)
	}
	return cfg
}

func TestV6StageChainMatchesTheSpecExample(t *testing.T) {
	cfg := mustParse(t, `schema_version: 6
template: gates
title: Gates
stages:
  - id: product_spec
    title: Spec
    function: po
    result: md
    exit_gate_groups:
      - - gate: required_sections
          version: 1
          params:
            sections: ["Критерии приёмки"]
      - - gate: agent_review
          version: 1
          name: spec_llm_review
          params: {agent: spec-checker, mode: grill, max_rounds: 3}
      - [human_approval@1]
  - id: done
    title: Done
    function: bo
    result: approve
`)
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatalf("valid v6 chain rejected: %v", err)
	}
	stage := cfg.Stages[0]
	if len(stage.ExitGateGroups) != 3 || stage.ExitGateGroups[1][0].InstanceName() != "spec_llm_review" {
		t.Fatalf("groups not decoded: %+v", stage.ExitGateGroups)
	}
	// Engine projection equals the legacy product_spec shape.
	if !reflect.DeepEqual(stage.RequiredSections, []string{"Критерии приёмки"}) ||
		stage.Check == nil || stage.Check.Kind != "agent" || stage.Check.Agent != "spec-checker" || stage.Check.Mode != "grill" || stage.Check.MaxRounds != 3 ||
		stage.Confirm != "required" {
		t.Fatalf("projection differs from legacy shape: %+v", stage)
	}
	if cfg.Stages[1].Confirm != "auto" {
		t.Fatalf("a stage without a human gate must not require confirmation, got %q", cfg.Stages[1].Confirm)
	}
}

func TestV6RejectsInvalidGateReferencesBeforeRun(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"unknown id", "    exit_gate_groups:\n      - [no_such_gate@1]\n", `неизвестный гейт "no_such_gate"`},
		{"unknown version", "    exit_gate_groups:\n      - [verdict_marker@9]\n", `нет версии 9`},
		{"missing version scalar", "    exit_gate_groups:\n      - [verdict_marker]\n", "без версии"},
		{"missing version mapping", "    exit_gate_groups:\n      - - gate: verdict_marker\n", "версия обязательна"},
		{"unknown param", "    exit_gate_groups:\n      - - gate: verdict_marker\n          version: 1\n          params: {strict: true}\n", `не принимает параметров, получен "strict"`},
		{"missing required param", "    exit_gate_groups:\n      - - gate: required_sections\n          version: 1\n", `обязательный параметр "sections"`},
		{"wrong param type", "    exit_gate_groups:\n      - - gate: required_sections\n          version: 1\n          params: {sections: text}\n", "списком строк"},
		{"duplicate section", "    exit_gate_groups:\n      - - gate: required_sections\n          version: 1\n          params: {sections: [A, A]}\n", "повторяющееся"},
		{"bad enum", "    exit_gate_groups:\n      - - gate: agent_review\n          version: 1\n          params: {agent: reviewer, mode: other}\n", `параметр "mode"`},
		{"empty group", "    exit_gate_groups:\n      - []\n", "группа не может быть пустой"},
		{"result mismatch", "    exit_gate_groups:\n      - [pr_exists@1]\n", "применим только к result link"},
		{"duplicate instance", "    exit_gate_groups:\n      - [verdict_marker@1]\n      - [verdict_marker@1]\n", "повторяется на этапе"},
		{"bad mode", "    exit_gate_groups:\n      - - gate: verdict_marker\n          version: 1\n          mode: optional\n", "mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseYAML([]byte(v6Doc(tc.body)))
			if err == nil {
				err = cfg.Validate(gateAgents())
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "stages[0] (product_spec)") && !strings.Contains(err.Error(), "строка") {
				t.Fatalf("error must say where the problem is: %v", err)
			}
		})
	}
}

func TestV6ErrorNamesPathOfNestedGate(t *testing.T) {
	cfg := mustParse(t, v6Doc("    exit_gate_groups:\n      - [verdict_marker@1]\n      - [verdict_marker@2]\n"))
	err := cfg.Validate(gateAgents())
	if err == nil || !strings.Contains(err.Error(), "stages[0] (product_spec): exit_gate_groups[1][0]") {
		t.Fatalf("expected full YAML path, got %v", err)
	}
}

func TestV6RejectsLegacyGateKeysWithMigrationHint(t *testing.T) {
	for key, body := range map[string]string{
		"required_sections": "    required_sections: [A]\n",
		"check":             "    check: {kind: hard, rules: [verdict_marker]}\n",
		"confirm":           "    confirm: auto\n",
	} {
		_, err := ParseYAML([]byte(v6Doc(body)))
		if err == nil || !strings.Contains(err.Error(), `"`+key+`" удалён в schema_version 6`) || !strings.Contains(err.Error(), "stages[0] (product_spec)") {
			t.Fatalf("%s: unexpected error %v", key, err)
		}
	}
}

func TestV5RejectsExitGateGroups(t *testing.T) {
	data := strings.Replace(v6Doc("    exit_gate_groups:\n      - [verdict_marker@1]\n"), "schema_version: 6", "schema_version: 5", 1)
	_, err := ParseYAML([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "exit_gate_groups доступен со schema_version 6") {
		t.Fatalf("got %v", err)
	}
}

func TestUnsupportedSchemaVersionMentionsBothSupported(t *testing.T) {
	_, err := ParseYAML([]byte("schema_version: 7\ntemplate: x\ntitle: X\nstages: []\n"))
	if err == nil || !strings.Contains(err.Error(), "поддерживаются 5 и 6") {
		t.Fatalf("got %v", err)
	}
}

func TestGateGroupSyntaxErrorsAreExplained(t *testing.T) {
	for name, body := range map[string]string{
		"group is scalar":   "    exit_gate_groups:\n      - verdict_marker@1\n",
		"unknown ref key":   "    exit_gate_groups:\n      - - gate: verdict_marker\n          version: 1\n          optional: true\n",
		"bad version":       "    exit_gate_groups:\n      - [verdict_marker@x]\n",
		"zero version text": "    exit_gate_groups:\n      - [verdict_marker@0]\n",
	} {
		if _, err := ParseYAML([]byte(v6Doc(body))); err == nil {
			t.Fatalf("%s: expected decode error", name)
		}
	}
}

func TestSeveralGroupsAndOnlyTheNeededGates(t *testing.T) {
	// Stage 1: only the gates this stage wants, in two sequential groups.
	// Stage 2: one group with two independent gates.
	cfg := mustParse(t, `schema_version: 6
template: gates
title: Gates
stages:
  - id: review
    title: Review
    function: reviewer
    result: md
    exit_gate_groups:
      - [verdict_marker@1]
      - [human_approval@1]
  - id: impl
    title: Impl
    function: developer
    result: link
    link_kind: pr
    executor: agent
    agent: coder
    exit_gate_groups:
      - [pr_exists@1, pr_open@1]
  - id: plain
    title: Plain
    function: qa
    result: md
`)
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatal(err)
	}
	if got := len(cfg.Stages[0].ExitGateGroups); got != 2 {
		t.Fatalf("review groups = %d", got)
	}
	if got := len(cfg.Stages[1].ExitGateGroups[0]); got != 2 {
		t.Fatalf("impl first group size = %d", got)
	}
	if len(cfg.Stages[2].EffectiveGateGroups()) != 0 {
		t.Fatalf("a stage selects gates explicitly: none selected means no gates")
	}
	if cfg.Stages[1].Confirm != "auto" || cfg.Stages[0].Confirm != "required" {
		t.Fatalf("confirm projection: review=%s impl=%s", cfg.Stages[0].Confirm, cfg.Stages[1].Confirm)
	}
}

func TestSameDefinitionTwiceNeedsDistinctNames(t *testing.T) {
	cfg := mustParse(t, v6Doc(`    exit_gate_groups:
      - - {gate: required_sections, version: 1, name: core, params: {sections: [A]}}
        - {gate: required_sections, version: 1, name: extra, params: {sections: [B]}}
`))
	err := cfg.Validate(gateAgents())
	// Valid shape, but the pre-scheduler engine cannot run two section gates.
	if err == nil || !strings.Contains(err.Error(), "не более одного required_sections") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "повторяется на этапе") {
		t.Fatalf("distinct names must not be reported as duplicates: %v", err)
	}
}

func TestAdvisoryAndUnexecutableChainsFailClosedUntilTheScheduler(t *testing.T) {
	cfg := mustParse(t, v6Doc("    exit_gate_groups:\n      - - {gate: verdict_marker, version: 1, mode: advisory}\n"))
	err := cfg.Validate(gateAgents())
	if err == nil || !strings.Contains(err.Error(), "advisory пока не исполняется") {
		t.Fatalf("advisory must not silently run as required: %v", err)
	}
	cfg = mustParse(t, v6Doc("    exit_gate_groups:\n      - [human_approval@1]\n      - [verdict_marker@1]\n"))
	if err := cfg.Validate(gateAgents()); err == nil || !strings.Contains(err.Error(), "порядок групп пока фиксирован") {
		t.Fatalf("got %v", err)
	}
}

func TestOutOfSyncProjectionIsRejected(t *testing.T) {
	cfg := mustParse(t, v6Doc("    exit_gate_groups:\n      - [verdict_marker@1]\n"))
	cfg.Stages[0].Confirm = "required"
	if err := cfg.Validate(gateAgents()); err == nil || !strings.Contains(err.Error(), "рассогласованы") {
		t.Fatalf("got %v", err)
	}
	cfg.NormalizeGates()
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatalf("NormalizeGates must restore consistency: %v", err)
	}
}

// A published template and a task started from it keep the exact definitions
// they were validated with after the library publishes a newer version.
func TestPinnedDefinitionsSurviveNewLibraryVersion(t *testing.T) {
	data := v6Doc(`    exit_gate_groups:
      - - gate: required_sections
          version: 1
          params: {sections: [A]}
`)
	cfg := mustParse(t, data)
	before, err := cfg.GatePins()
	if err != nil || len(before) != 1 || before[0].Gate != "required_sections" || before[0].Version != 1 {
		t.Fatalf("pins before: %+v %v", before, err)
	}

	newer := gatelib.Default().Clone()
	newer.MustRegister(workflow.GateDefinition{
		ID: gatelib.GateRequiredSections, Version: 2, Kind: workflow.GateKindBuiltin, Title: "Обязательные разделы v2",
		ResultTypes: []string{"md"},
		Params: []workflow.GateParam{
			{Name: "sections", Type: workflow.GateParamStringList, Required: true},
			{Name: "case_sensitive", Type: workflow.GateParamBool, Required: true},
		},
	})
	if err := cfg.ValidateWithLibrary(gateAgents(), newer); err != nil {
		t.Fatalf("v1 reference must keep validating against the grown library: %v", err)
	}
	cfg.gateLibrary = newer
	after, err := cfg.GatePins()
	cfg.gateLibrary = nil
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("pins changed after a new library version: before=%+v after=%+v err=%v", before, after, err)
	}

	// The new version is only reachable by naming it, with its own params.
	upgraded := mustParse(t, strings.Replace(data, "version: 1", "version: 2", 1))
	if err := upgraded.ValidateWithLibrary(gateAgents(), newer); err == nil || !strings.Contains(err.Error(), `"case_sensitive"`) {
		t.Fatalf("v2 must demand its new parameter: %v", err)
	}
	// And a library that does not have it rejects the reference.
	if err := upgraded.Validate(gateAgents()); err == nil || !strings.Contains(err.Error(), "нет версии 2") {
		t.Fatalf("built-in library has no v2: %v", err)
	}
}

func TestMarshalWritesSchemaSpecificSpelling(t *testing.T) {
	v6, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	data, err := v6.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, legacy := range []string{"required_sections:", "check:", "confirm:"} {
		if strings.Contains(text, "\n    "+legacy) {
			t.Fatalf("v6 output still has %q:\n%s", legacy, text)
		}
	}
	if !strings.Contains(text, "exit_gate_groups:") || !strings.Contains(text, "schema_version: 6") {
		t.Fatalf("v6 output lacks gate chain:\n%s", text)
	}

	legacy := *v6
	legacy.SchemaVersion = LegacySchemaVersion
	data, err = legacy.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "exit_gate_groups") || !strings.Contains(string(data), "required_sections:") {
		t.Fatalf("v5 output must use legacy spelling:\n%s", data)
	}
}

// The central compatibility claim: for every built-in profile the v5 template
// and its explicit v6 translation drive the engine identically — same engine
// fields per stage and same compiled graph.
func TestIdeaToProdV5AndV6AreEquivalent(t *testing.T) {
	for _, profile := range []string{ProfileStandard, ProfileFast, ProfileRegulated} {
		t.Run(profile, func(t *testing.T) {
			base, err := DefaultProfile(profile)
			if err != nil {
				t.Fatal(err)
			}
			v5 := *base
			v5.SchemaVersion = LegacySchemaVersion
			v5Data, err := v5.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			fromV5 := mustParse(t, string(v5Data))
			if err := fromV5.Validate(gateAgents()); err != nil {
				t.Fatalf("v5 template must keep validating: %v", err)
			}

			migrated := fromV5.MigrateToCurrent()
			v6Data, err := migrated.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			fromV6 := mustParse(t, string(v6Data))
			if err := fromV6.Validate(gateAgents()); err != nil {
				t.Fatalf("migrated v6 template invalid: %v\n%s", err, v6Data)
			}
			if fromV6.SchemaVersion != CurrentSchemaVersion || fromV5.SchemaVersion != LegacySchemaVersion {
				t.Fatalf("schema versions: v5=%d v6=%d", fromV5.SchemaVersion, fromV6.SchemaVersion)
			}
			for i := range fromV5.Stages {
				a, b := fromV5.Stages[i], fromV6.Stages[i]
				if !reflect.DeepEqual(a.RequiredSections, b.RequiredSections) || !reflect.DeepEqual(a.Check, b.Check) || a.Confirm != b.Confirm {
					t.Fatalf("stage %s differs:\n v5 sections=%v check=%+v confirm=%s\n v6 sections=%v check=%+v confirm=%s",
						a.ID, a.RequiredSections, a.Check, a.Confirm, b.RequiredSections, b.Check, b.Confirm)
				}
				if !reflect.DeepEqual(a.EffectiveGateGroups(), b.EffectiveGateGroups()) {
					t.Fatalf("stage %s effective gate groups differ: %+v vs %+v", a.ID, a.EffectiveGateGroups(), b.EffectiveGateGroups())
				}
			}
			graph5, err := fromV5.TemplateGraph()
			if err != nil {
				t.Fatal(err)
			}
			graph6, err := fromV6.TemplateGraph()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(graph5, graph6) {
				t.Fatalf("graphs differ between v5 and v6 spellings")
			}
		})
	}
}

func TestIdeaToProdV6MatchesTheDocumentedChains(t *testing.T) {
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	summary := map[string]string{}
	for _, stage := range cfg.Stages {
		var groups []string
		for _, group := range stage.EffectiveGateGroups() {
			var keys []string
			for _, ref := range group {
				keys = append(keys, ref.Key())
			}
			groups = append(groups, "["+strings.Join(keys, ",")+"]")
		}
		summary[stage.ID] = strings.Join(groups, "")
	}
	want := map[string]string{
		"intent":         "",
		"product_spec":   "[required_sections@1][agent_review@1][human_approval@1]",
		"tech_design":    "[agent_review@1][human_approval@1]",
		"implementation": "[pr_exists@1,pr_open@1,pr_base_branch@1][human_approval@1]",
		"code_review":    "[verdict_marker@1][human_approval@1]",
		"qa":             "[human_approval@1]",
		"deploy":         "[human_approval@1]",
		"observation":    "[human_approval@1]",
		"acceptance":     "",
	}
	if !reflect.DeepEqual(summary, want) {
		t.Fatalf("idea-to-prod gate chains:\n got %v\nwant %v", summary, want)
	}
}

func TestMigratedRoundTripThroughYAMLIsStable(t *testing.T) {
	cfg, _ := DefaultProfile(ProfileStandard)
	first, _ := cfg.Marshal()
	loaded := mustParse(t, string(first))
	second, err := yaml.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("v6 YAML is not a fixed point:\n%s\n---\n%s", first, second)
	}
}

func TestLegacyUnknownRuleIsRejectedByNameBeforeRun(t *testing.T) {
	data := `schema_version: 5
template: legacy
title: Legacy
stages:
  - id: review
    title: Review
    function: reviewer
    result: md
    check: {kind: hard, rules: [made_up_rule]}
`
	cfg := mustParse(t, data)
	err := cfg.Validate(gateAgents())
	if err == nil || !strings.Contains(err.Error(), `неизвестный гейт "made_up_rule"`) {
		t.Fatalf("got %v", err)
	}
}
