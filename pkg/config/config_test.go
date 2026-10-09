package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"gopkg.in/yaml.v3"
)

func TestDefaultProfileWritesOneV5Template(t *testing.T) {
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != 5 || cfg.Template != "idea-to-prod" || len(cfg.Stages) != 10 {
		t.Fatalf("unexpected standard template: schema=%d template=%q stages=%d", cfg.SchemaVersion, cfg.Template, len(cfg.Stages))
	}
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("default template invalid: %v", err)
	}

	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("v5 round-trip failed: %v\n%s", err, data)
	}
	if loaded.Template != cfg.Template || len(loaded.Stages) != len(cfg.Stages) || len(loaded.PipelineAgents) != len(cfg.Stages) {
		t.Fatalf("template lost in round-trip: template=%q stages=%d runtime projection=%d", loaded.Template, len(loaded.Stages), len(loaded.PipelineAgents))
	}
	if strings.Contains(string(data), "pipeline:") || strings.Contains(string(data), "workflow:") {
		t.Fatalf("legacy v4 sections serialized in v5 output:\n%s", data)
	}
}

func TestSchemaV4RejectedWithMigrationInstructions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("schema_version: 4\npipeline: [analyst]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "ai-team init --force") || !strings.Contains(err.Error(), "manual") && !strings.Contains(err.Error(), "вручную") {
		t.Fatalf("expected actionable v4 migration error, got %v", err)
	}
}

func TestV5RejectsUnknownDuplicateAndLegacyFields(t *testing.T) {
	cases := map[string]string{
		"noninteger schema version": "schema_version: v5\ntemplate: x\ntitle: X\nstages: []\n",
		"unknown top-level":         "schema_version: 5\ntemplate: x\ntitle: X\nstages: []\nworkflow: {}\n",
		"duplicate top-level":       "schema_version: 5\ntemplate: x\ntemplate: y\ntitle: X\nstages: []\n",
		"unknown stage field":       "schema_version: 5\ntemplate: x\ntitle: X\nstages:\n  - id: x\n    title: X\n    function: bo\n    result: approve\n    model: bad\n",
		"duplicate stage field":     "schema_version: 5\ntemplate: x\ntitle: X\nstages:\n  - id: x\n    id: y\n    title: X\n    function: bo\n    result: approve\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte(content), &cfg); err == nil {
				t.Fatal("invalid schema accepted")
			}
		})
	}
}

func TestTemplateValidationRules(t *testing.T) {
	base, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	validAgents := fakeLookup{"analyst": true, "verifier": true, "architect": true, "reviewer": true, "coder": true, "tester": true}
	if err := base.Validate(validAgents); err != nil {
		t.Fatalf("built-in refs should exist: %v", err)
	}

	cases := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"duplicate stage id", func(c *Config) { c.Stages[1].ID = c.Stages[0].ID }, "id повторяется"},
		{"missing agent for agent executor", func(c *Config) { c.Stages[2].Agent = "" }, "executor agent требует agent"},
		{"missing registry agent", func(c *Config) { c.Stages[2].Agent = "ghost" }, "не найден в registry"},
		{"invalid result", func(c *Config) { c.Stages[0].Result = "json" }, "result"},
		{"invalid link kind", func(c *Config) { c.Stages[4].LinkKind = "issue" }, "link_kind"},
		{"link kind on md", func(c *Config) { c.Stages[5].LinkKind = "pr" }, "link_kind допустим только"},
		{"sections on approve", func(c *Config) { c.Stages[0].RequiredSections = []string{"Decision"} }, "required_sections допустим только"},
		{"forward return", func(c *Config) { c.Returns[0].To = "acceptance" }, "должен вести только назад"},
		{"unknown return stage", func(c *Config) { c.Returns[0].From = "ghost" }, "существующие stages"},
		{"unknown max visits stage", func(c *Config) { c.MaxVisits["ghost"] = 2 }, "неизвестный stage"},
		{"max visits without return", func(c *Config) { c.MaxVisits["intent"] = 2 }, "только для stage, в который ведёт возврат"},
		{"nonpositive max visits", func(c *Config) { c.MaxVisits["implementation"] = 0 }, "должен быть положительным"},
		{"conflicting route limits", func(c *Config) {
			c.Returns[0].MaxVisits = 4
			c.MaxVisits["product_spec"] = 3
		}, "не совпадает с лимитом возврата"},
		{"bad check agent", func(c *Config) { c.Stages[1].Check.Agent = "ghost" }, "агент проверки"},
		{"invalid executor", func(c *Config) { c.Stages[0].Executor = "robot" }, "executor"},
		{"delivery without PR", func(c *Config) { c.Stages[4].LinkKind = "build" }, "delivery допустим"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := cloneConfig(t, base)
			tc.edit(copy)
			err := copy.Validate(validAgents)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestTemplateGraphUsesOrderAndBackwardReturns(t *testing.T) {
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	if graph.Entry != "intent" || len(graph.Nodes) != len(cfg.Stages) {
		t.Fatalf("unexpected graph entry/nodes: %s/%d", graph.Entry, len(graph.Nodes))
	}
	for i, stage := range cfg.Stages {
		if graph.Nodes[i].Key() != stage.ID {
			t.Fatalf("node %d = %q, want stage %q", i, graph.Nodes[i].Key(), stage.ID)
		}
		want := "$complete"
		if i+1 < len(cfg.Stages) {
			want = cfg.Stages[i+1].ID
		}
		edge, ok := graph.Edge(stage.ID, "passed")
		if !ok || edge.To != want {
			t.Fatalf("stage-order edge %s → %s missing: %+v", stage.ID, want, edge)
		}
	}
	for _, route := range cfg.Returns {
		node, ok := graph.Node(route.To)
		if !ok || node.MaxVisits != 3 {
			t.Fatalf("return target %q max_visits=%d, want 3", route.To, node.MaxVisits)
		}
		edge, ok := graph.Edge(route.From, "rejected")
		if !ok || edge.Approval == nil || edge.Approval.Actions["return_to_"+route.To] != route.To {
			t.Fatalf("return route %s → %s missing: %+v", route.From, route.To, edge)
		}
	}
	cfg.Returns[0].MaxVisits = 4
	cfg.MaxVisits["product_spec"] = 4
	graph, err = cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	if node, ok := graph.Node("product_spec"); !ok || node.MaxVisits != 4 {
		t.Fatalf("explicit max_visits not compiled: node=%+v exists=%v", node, ok)
	}
}

func TestProfilePresetsAreDistinctTemplates(t *testing.T) {
	standard, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := DefaultProfile(ProfileFast)
	if err != nil {
		t.Fatal(err)
	}
	regulated, err := DefaultProfile(ProfileRegulated)
	if err != nil {
		t.Fatal(err)
	}
	if standard.Template != "idea-to-prod" || fast.Template == standard.Template || regulated.Template == standard.Template || fast.Template == regulated.Template {
		t.Fatalf("profiles must materialize distinct templates: %q %q %q", standard.Template, fast.Template, regulated.Template)
	}
	for _, cfg := range []*Config{standard, fast, regulated} {
		if err := cfg.Validate(nil); err != nil {
			t.Errorf("%s: %v", cfg.Template, err)
		}
		if _, err := cfg.CompiledGraph(); err != nil {
			t.Errorf("%s graph: %v", cfg.Template, err)
		}
	}
	if fast.Stages[0].Confirm != "auto" || regulated.Stages[0].Confirm != "required" || standard.Stages[0].Confirm != "auto" {
		t.Fatalf("profile confirm defaults differ: standard=%s fast=%s regulated=%s", standard.Stages[0].Confirm, fast.Stages[0].Confirm, regulated.Stages[0].Confirm)
	}
	if _, err := DefaultProfile("unknown"); err == nil {
		t.Fatal("unknown profile accepted")
	}
}

func TestStageDefaultsAndProjectChecks(t *testing.T) {
	cfg := &Config{Stages: []TemplateStage{
		{ID: "brief", Title: "Brief", Function: "po", Result: "md"},
		{ID: "gate", Title: "Gate", Function: "bo", Result: "approve"},
	}}
	if err := yaml.Unmarshal([]byte("schema_version: 5\ntemplate: x\ntitle: X\nstages:\n  - id: brief\n    title: Brief\n    function: po\n    result: md\n  - id: gate\n    title: Gate\n    function: bo\n    result: approve\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Stages[0].Executor != "human" || cfg.Stages[0].Confirm != "required" || cfg.Stages[1].Confirm != "auto" {
		t.Fatalf("stage defaults not applied: %+v %+v", cfg.Stages[0], cfg.Stages[1])
	}
	goTarget := t.TempDir()
	if err := os.WriteFile(filepath.Join(goTarget, "go.mod"), []byte("module example.test\n\ngo 1.27\n"), 0600); err != nil {
		t.Fatal(err)
	}
	projectConfig, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	detected, warning := projectConfig.ApplyDetectedChecks(goTarget)
	if detected != "go" || warning != "" || len(projectConfig.Checks) != 2 {
		t.Fatalf("detected project checks missing: profile=%q warning=%q checks=%d", detected, warning, len(projectConfig.Checks))
	}
	if err := projectConfig.Validate(nil); err != nil {
		t.Fatalf("detected config invalid: %v", err)
	}
}

func TestAgentConfigFallbackAndLegacyRuntimeDefault(t *testing.T) {
	cfg := &Config{
		SchemaVersion:  CurrentSchemaVersion,
		PipelineAgents: []AgentConfig{{Name: "analyst", Effort: "high"}, {Name: "coder"}},
		CLI:            "opencode", Model: "auto", Effort: "medium", StageTimeout: "30m",
	}
	ac := cfg.AgentConfig("analyst")
	if ac.Model != "auto" || ac.Effort != "high" || ac.CLI != "opencode" || ac.Timeout != "30m" {
		t.Fatalf("agent fallback mismatch: %+v", ac)
	}
	defaultConfig := Default()
	if err := defaultConfig.Validate(nil); err != nil {
		t.Fatalf("legacy runtime default graph invalid: %v", err)
	}
	if graph, err := defaultConfig.CompiledGraph(); err != nil || graph.Entry != "analyst" {
		t.Fatalf("legacy in-memory graph: entry=%q err=%v", graph.Entry, err)
	}
}

func TestLegacyRuntimeConfigValidationRemainsAvailableInMemory(t *testing.T) {
	base := &Config{
		SchemaVersion:  CurrentSchemaVersion,
		PipelineAgents: []AgentConfig{{Name: "a"}},
		Workflow:       &WorkflowConfig{Entry: "a", Edges: []WorkflowEdgeConfig{{From: "a", Outcome: "passed", To: "$complete"}}},
	}
	if err := base.Validate(nil); err != nil {
		t.Fatalf("in-memory runtime fixture invalid: %v", err)
	}
	cases := []struct {
		name string
		edit func(*Config)
	}{
		{"bad effort", func(c *Config) { c.Effort = "max" }},
		{"bad global cli", func(c *Config) { c.CLI = "missing-cli" }},
		{"bad stage timeout", func(c *Config) { c.StageTimeout = "later" }},
		{"zero preflight timeout", func(c *Config) { c.PreflightTimeout = "0s" }},
		{"bad delivery timeout", func(c *Config) { c.DeliveryTimeout = "soon" }},
		{"duplicate runtime stage", func(c *Config) { c.PipelineAgents = append(c.PipelineAgents, AgentConfig{Name: "a"}) }},
		{"missing runtime workflow", func(c *Config) { c.Workflow = nil }},
		{"bad runtime check", func(c *Config) {
			c.PipelineAgents[0].Checks = []checks.Definition{{Name: "broken", Class: "unit", Policy: "required"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := *base
			copy.PipelineAgents = append([]AgentConfig(nil), base.PipelineAgents...)
			copy.Workflow = base.Workflow
			tc.edit(&copy)
			if err := copy.Validate(nil); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

type fakeProductSpecLookup struct{ hasContract bool }

func (fakeProductSpecLookup) Exists(string) bool { return true }
func (f fakeProductSpecLookup) HasProductSpecContract(name string) (bool, error) {
	return name == "analyst" && f.hasContract, nil
}

func TestLegacyApproveSpecContractIsOnlyForInMemoryFixture(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(fakeProductSpecLookup{}); err == nil || !strings.Contains(err.Error(), "outputs proposal и spec") {
		t.Fatalf("approve_spec without registry contract should fail, got %v", err)
	}
	if err := cfg.Validate(fakeProductSpecLookup{hasContract: true}); err != nil {
		t.Fatalf("in-memory legacy fixture with contract should pass: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("schema_version: 4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "ai-team init --force") {
		t.Fatalf("the public parser must still reject v4: %v", err)
	}
}

func TestLegacyConfigRejectsUnknownAgent(t *testing.T) {
	cfg := &Config{
		SchemaVersion:  CurrentSchemaVersion,
		PipelineAgents: []AgentConfig{{Name: "analyst"}, {Name: "ghost"}},
		Workflow:       &WorkflowConfig{Entry: "analyst", Edges: []WorkflowEdgeConfig{{From: "analyst", Outcome: "passed", To: "$complete"}}},
	}
	if err := cfg.Validate(fakeLookup{"analyst": true}); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown in-memory runtime agent should fail, got %v", err)
	}
}

func TestSchemaVersionsAndRuntimeTimeoutValidation(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3, 4, 99} {
		t.Run(fmt.Sprintf("schema_%d", version), func(t *testing.T) {
			cfg := &Config{SchemaVersion: version, Template: "x", Title: "X", Stages: []TemplateStage{{ID: "x", Title: "X", Function: "bo", Result: "approve"}}}
			err := cfg.Validate(nil)
			if err == nil {
				t.Fatalf("unsupported schema %d accepted", version)
			}
		})
	}
}

func cloneConfig(t *testing.T, source *Config) *Config {
	t.Helper()
	data, err := yaml.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result Config
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

type fakeLookup map[string]bool

func (f fakeLookup) Exists(name string) bool { return f[name] }
