package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
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

func TestBuiltInTemplateAgentContracts(t *testing.T) {
	registry := agent.NewFS(os.DirFS(filepath.Join("..", "..", "agents")))
	wantAgents := map[string]string{
		"product_spec":   "analyst",
		"tech_design":    "architect",
		"design_review":  "design-reviewer",
		"implementation": "coder",
		"code_review":    "reviewer",
		"qa":             "tester",
		"observation":    "observer",
	}
	noAgent := map[string]bool{"intent": true, "deploy": true, "acceptance": true}

	for _, profile := range []string{ProfileStandard, ProfileFast, ProfileRegulated} {
		t.Run(profile, func(t *testing.T) {
			cfg, err := DefaultProfile(profile)
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(registry); err != nil {
				t.Fatalf("built-in template and its agent artifact ownership must validate: %v", err)
			}
			seen := make(map[string]bool, len(cfg.Stages))
			for _, stage := range cfg.Stages {
				seen[stage.ID] = true
				if want, exists := wantAgents[stage.ID]; exists {
					if stage.Agent != want {
						t.Errorf("stage %q agent=%q, want %q", stage.ID, stage.Agent, want)
					}
					loaded, err := registry.Load(want)
					if err != nil {
						t.Errorf("stage %q references unavailable agent %q: %v", stage.ID, want, err)
					} else if strings.TrimSpace(loaded.Prompt) == "" {
						t.Errorf("agent %q has an empty prompt", want)
					}
				} else if noAgent[stage.ID] {
					if stage.Agent != "" {
						t.Errorf("stage %q is intentionally human-only, got agent %q", stage.ID, stage.Agent)
					}
				} else {
					t.Errorf("unclassified built-in stage %q", stage.ID)
				}
			}
			for stageID := range wantAgents {
				if !seen[stageID] {
					t.Errorf("built-in profile lacks stage %q", stageID)
				}
			}
			for stageID := range noAgent {
				if !seen[stageID] {
					t.Errorf("built-in profile lacks intentionally human-only stage %q", stageID)
				}
			}
		})
	}

	assertContract := func(name string, wantInputs, wantOutputs map[string]string, marker string, values []string) {
		t.Helper()
		loaded, err := registry.Load(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		for key, want := range wantInputs {
			if got := loaded.Inputs[key]; got != want {
				t.Errorf("%s input %q=%q, want %q", name, key, got, want)
			}
		}
		for key, want := range wantOutputs {
			if got := loaded.Outputs[key]; got != want {
				t.Errorf("%s output %q=%q, want %q", name, key, got, want)
			}
		}
		if marker == "" {
			if loaded.Verdict != nil {
				t.Errorf("%s unexpected verdict contract: %+v", name, loaded.Verdict)
			}
			return
		}
		if loaded.Verdict == nil || loaded.Verdict.Marker != marker {
			t.Errorf("%s verdict=%+v, want marker %q", name, loaded.Verdict, marker)
			return
		}
		if got := loaded.Verdict.Values; len(got) != len(values) {
			t.Errorf("%s verdict values=%v, want %v", name, got, values)
		} else {
			for i := range values {
				if string(got[i]) != values[i] {
					t.Errorf("%s verdict values=%v, want %v", name, got, values)
					break
				}
			}
		}
	}
	assertContract("design-reviewer",
		map[string]string{"specs": "{feature}/specs", "design": "{feature}/design.md", "tasks": "{feature}/tasks.md"},
		map[string]string{"design-review": "{feature}/design-review.md"},
		"Verdict", []string{"APPROVED", "CHANGES_REQUESTED", "REJECTED"})
	assertContract("reviewer",
		map[string]string{"specs": "{feature}/specs", "design": "{feature}/design.md", "candidate": "{feature}/.control/review-candidate.json"},
		map[string]string{"review": "{feature}/review.md"},
		"Verdict", []string{"APPROVED", "CHANGES_REQUESTED", "REJECTED"})
	assertContract("tester",
		map[string]string{"specs": "{feature}/specs", "design": "{feature}/design.md", "review": "{feature}/review.md", "reviewed-candidate": "{feature}/.control/review-candidate.json"},
		map[string]string{"test-report": "{feature}/test-report.md"},
		"Result", []string{"PASS", "FAIL"})
	assertContract("observer",
		map[string]string{"task": "tasks/{feature}/task.md"},
		map[string]string{"observation": "{feature}/observation.md"}, "", nil)
	assertContract("analyst",
		map[string]string{"task": "tasks/{feature}/task.md"},
		map[string]string{"proposal": "{feature}/proposal.md", "spec": "{feature}/specs/product/spec.md"}, "", nil)
	assertContract("architect",
		map[string]string{"specs": "{feature}/specs"},
		map[string]string{"design": "{feature}/design.md", "tasks": "{feature}/tasks.md"}, "", nil)
	assertContract("coder",
		map[string]string{"design": "{feature}/design.md", "tasks": "{feature}/tasks.md"}, map[string]string{}, "", nil)
	for _, name := range []string{"design-reviewer", "reviewer", "tester"} {
		loaded, err := registry.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		marker := "Verdict"
		if name == "tester" {
			marker = "Result"
		}
		prompt := strings.Join(strings.Fields(loaded.Prompt), " ")
		if !strings.Contains(prompt, "ровно одной итоговой строкой") ||
			!strings.Contains(prompt, "Не повторяй этот маркер") ||
			!strings.Contains(prompt, "**"+marker+":**") {
			t.Errorf("%s prompt must request one final control marker compatible with runtime verdict parsing", name)
		}
		if name == "tester" && (!strings.Contains(prompt, "по доступным данным нет явного нарушения") ||
			!strings.Contains(prompt, "FAIL обязателен")) {
			t.Errorf("tester prompt must require FAIL when the reviewed candidate violates a criterion")
		}
	}

	reviewer, err := registry.Load("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"controller evidence", "не используй сеть", "gh pr diff"} {
		if !strings.Contains(reviewer.Prompt, phrase) {
			t.Errorf("reviewer prompt must define the candidate/diff boundary (%q missing)", phrase)
		}
	}
	observer, err := registry.Load("observer")
	if err != nil {
		t.Fatal(err)
	}
	if observer.ReadScope != "inputs-only" {
		t.Fatalf("observer runtime read scope = %q, want inputs-only", observer.ReadScope)
	}
	for _, phrase := range []string{"не используй shell", "Не открывай ссылки", "не добавляй результат от себя"} {
		if !strings.Contains(observer.Prompt, phrase) {
			t.Errorf("observer prompt must limit reports to supplied human evidence (%q missing)", phrase)
		}
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

func TestV5StagesWithoutTemplateFailValidation(t *testing.T) {
	for name, delivery := range map[string]string{
		"unknown required check":         `require_checks: [not-configured]`,
		"unknown required verdict stage": `require_verdicts: [not-a-stage]`,
	} {
		t.Run(name, func(t *testing.T) {
			data := fmt.Sprintf(`schema_version: 5
title: Missing template
stages:
  - id: implementation
    title: Implementation
    function: developer
    result: link
    link_kind: pr
    executor: agent
    agent: coder
    delivery:
      %s
`, delivery)
			var cfg Config
			if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
				t.Fatalf("malformed v5 YAML should parse before semantic validation: %v", err)
			}
			if len(cfg.PipelineAgents) != 1 || cfg.PipelineAgents[0].Name != "implementation" {
				t.Fatalf("expected YAML compatibility projection from stages, got %+v", cfg.PipelineAgents)
			}
			err := cfg.Validate(nil)
			if err == nil || !strings.Contains(err.Error(), "template обязателен") {
				t.Fatalf("stage-list v5 config without template must fail validation before delivery/runtime: %v", err)
			}
		})
	}
}

func TestTemplateValidationRules(t *testing.T) {
	base, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	validAgents := fakeLookup{"analyst": true, "verifier": true, "architect": true, "design-reviewer": true, "reviewer": true, "coder": true, "tester": true, "observer": true}
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
		{"confirm none unsupported", func(c *Config) { c.Stages[2].Confirm = "none" }, "confirm"},
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
	cfg.Stages[2].Skippable = true
	for _, route := range cfg.Returns {
		for index := range cfg.Stages {
			if cfg.Stages[index].ID == route.From {
				cfg.Stages[index].Confirm = "auto"
			}
		}
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
		if stage.Skippable {
			skipped, found := graph.Edge(stage.ID, workflow.OutcomeSkipped)
			if !found || skipped.To != want || skipped.Approval != nil {
				t.Fatalf("skippable stage %s must route skipped to %s without another approval: %+v", stage.ID, want, skipped)
			}
		} else if _, found := graph.Edge(stage.ID, workflow.OutcomeSkipped); found {
			t.Fatalf("non-skippable stage %s must not expose a skipped edge", stage.ID)
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
		passed, passedOK := graph.Edge(route.From, workflow.OutcomePassed)
		if !passedOK || passed.Approval == nil || passed.Approval.Actions["return_to_"+route.To] != route.To {
			t.Fatalf("configured return %s → %s must remain available on the passed stage action even with confirm:auto: %+v", route.From, route.To, passed)
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

func TestTemplateGraphRespectsStageConfirmation(t *testing.T) {
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Stages {
		cfg.Stages[i].Confirm = "auto"
	}

	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatalf("all-auto template with explicit returns should compile: %v", err)
	}
	for i, stage := range cfg.Stages {
		edge, ok := graph.Edge(stage.ID, "passed")
		hasReturns := false
		for _, route := range cfg.Returns {
			if route.From == stage.ID {
				hasReturns = true
				if !ok || edge.Approval == nil || edge.Approval.Actions["return_to_"+route.To] != route.To {
					t.Fatalf("confirm:auto stage %q must expose configured return %s on its forward action gate: %+v", stage.ID, route.To, edge)
				}
			}
		}
		if !hasReturns && (!ok || edge.Approval != nil) {
			t.Fatalf("confirm:auto stage %q without returns should have an unguarded forward edge: %+v", stage.ID, edge)
		}
		want := workflow.TerminalComplete
		if i+1 < len(cfg.Stages) {
			want = cfg.Stages[i+1].ID
		}
		if edge.To != want {
			t.Fatalf("stage %q proceeds to %q, want %q", stage.ID, edge.To, want)
		}
	}
	for _, route := range cfg.Returns {
		edge, ok := graph.Edge(route.From, "rejected")
		if !ok || edge.Approval == nil {
			t.Fatalf("return %s → %s must retain its approval: %+v", route.From, route.To, edge)
		}
	}

	cfg.Stages[0].Confirm = "required"
	graph, err = cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	if edge, ok := graph.Edge(cfg.Stages[0].ID, "passed"); !ok || edge.Approval == nil {
		t.Fatalf("confirm:required stage %q must compile an approval: %+v", cfg.Stages[0].ID, edge)
	}
	if edge, ok := graph.Edge(cfg.Stages[1].ID, "passed"); !ok || edge.Approval != nil {
		t.Fatalf("confirm:auto stage %q must not compile an approval: %+v", cfg.Stages[1].ID, edge)
	}
}

func TestTemplateGraphDefaultsConfirmationForProgrammaticStages(t *testing.T) {
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Stages) < 2 {
		t.Fatalf("standard profile has %d stages, need at least two", len(cfg.Stages))
	}
	// Direct Go callers can construct or edit Config without YAML unmarshalling.
	// An omitted mode must preserve the documented result-based defaults.
	cfg.Stages[0].Confirm = ""
	cfg.Stages[1].Confirm = ""
	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	for i, wantApproval := range []bool{false, true} {
		stage := cfg.Stages[i]
		edge, ok := graph.Edge(stage.ID, "passed")
		if !ok || (edge.Approval != nil) != wantApproval {
			t.Fatalf("omitted confirm for stage %q result=%q: approval=%v, want %v", stage.ID, stage.Result, edge.Approval != nil, wantApproval)
		}
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
	var required []string
	for _, stage := range projectConfig.Stages {
		if stage.Delivery != nil {
			required = stage.Delivery.RequireChecks
		}
	}
	if strings.Join(required, ",") != "go-test,go-vet" {
		t.Fatalf("detected delivery checks not attached to implementation stage: %v", required)
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

func TestConfigMCPServersAreSelectedPerAgentStage(t *testing.T) {
	data := []byte(`schema_version: 5
template: idea-to-prod
title: Process
cli: codex
mcp_servers:
  knowledge:
    command: /opt/ai-team/bin/knowledge-mcp
    args: [--readonly, /srv/knowledge]
  monitoring:
    command: /opt/ai-team/bin/monitoring-mcp
    args: [--readonly]
stages:
  - id: product_spec
    title: Product spec
    function: operator
    result: md
    executor: agent
    agent: analyst
    confirm: auto
    mcp_servers: [knowledge]
  - id: observation
    title: Observation
    function: operator
    result: md
    executor: human
    agent: observer
    confirm: auto
    mcp_servers: [monitoring]
`)
	cfg, err := ParseYAML(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("valid per-stage MCP allowlist rejected: %v", err)
	}
	product := cfg.AgentConfig("product_spec")
	observation := cfg.AgentConfig("observation")
	if product == nil || len(product.MCPServers) != 1 || product.MCPServers[0].Name != "knowledge" {
		t.Fatalf("product-spec runtime must receive only knowledge MCP: %+v", product)
	}
	if observation == nil || len(observation.MCPServers) != 1 || observation.MCPServers[0].Name != "monitoring" {
		t.Fatalf("observation runtime must receive only monitoring MCP: %+v", observation)
	}
	if leaked := cfg.AgentConfig("analyst"); leaked != nil && len(leaked.MCPServers) > 0 {
		t.Fatalf("registry agent lookup must not broaden a stage-specific allowlist: %+v", leaked.MCPServers)
	}
}

func TestConfigRejectsMCPOutsideCodexOrWithoutStageAllowlist(t *testing.T) {
	base := `schema_version: 5
template: idea-to-prod
title: Process
CLI_PLACEHOLDER
mcp_servers:
  knowledge:
    command: /opt/ai-team/bin/knowledge-mcp
stages:
  - id: product_spec
    title: Product spec
    function: operator
    result: md
    executor: agent
    agent: analyst
    confirm: auto
    mcp_servers: [knowledge]
`
	for _, test := range []struct {
		name   string
		cli    string
		want   string
		mutate func(string) string
	}{
		{name: "non Codex", cli: "cli: opencode", want: "только при cli: codex"},
		{
			name: "unknown selected server", cli: "cli: codex", want: "неизвестный сервер",
			mutate: func(text string) string {
				return strings.Replace(text, "mcp_servers: [knowledge]", "mcp_servers: [missing]", 1)
			},
		},
		{
			name: "too many stage servers", cli: "cli: codex", want: "не более",
			mutate: func(text string) string {
				return strings.Replace(text, "mcp_servers: [knowledge]", "mcp_servers: [knowledge, monitoring, audit, metrics, tracing]", 1)
			},
		},
		{
			name: "duplicate stage server id", cli: "cli: codex", want: "пустой или повторяющийся",
			mutate: func(text string) string {
				return strings.Replace(text, "mcp_servers: [knowledge]", "mcp_servers: [knowledge, knowledge]", 1)
			},
		},
		{
			name: "empty stage server id", cli: "cli: codex", want: "пустой или повторяющийся",
			mutate: func(text string) string {
				return strings.Replace(text, "mcp_servers: [knowledge]", `mcp_servers: [knowledge, ""]`, 1)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := strings.Replace(base, "CLI_PLACEHOLDER", test.cli, 1)
			if test.mutate != nil {
				text = test.mutate(text)
			}
			cfg, err := ParseYAML([]byte(text))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(nil); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid MCP config should fail with %q, got %v", test.want, err)
			}
		})
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
