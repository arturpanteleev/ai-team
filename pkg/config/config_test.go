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

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.CLI != "opencode" {
		t.Errorf("expected opencode, got %s", cfg.CLI)
	}
	if len(cfg.PipelineAgents) != 7 {
		t.Errorf("expected 7 agents, got %d", len(cfg.PipelineAgents))
	}
	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatalf("default graph: %v", err)
	}
	if graph.Entry != "analyst" || len(graph.Edges) < len(cfg.PipelineAgents) {
		t.Fatalf("default graph неполон: %+v", graph)
	}
}

func TestSchemaV4RejectsUnknownAndUnboundedGraph(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`
schema_version: 4
pipeline: [a, b]
workflow:
  entry: a
  max_visits: {}
  edges:
    - from: a
      outcome: passed
      to: b
      approval:
        roles: [operator]
        quorum: any
        actions: {approve: b, reject: $stop}
    - from: b
      outcome: rejected
      to: a
      approval:
        roles: [operator]
        quorum: any
        actions: {approve: a, reject: $stop}
    - from: b
      outcome: passed
      to: $complete
`)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(nil); err == nil || !strings.Contains(err.Error(), "max_visits") {
		t.Fatalf("unbounded graph принят: %v", err)
	}
}

func TestSchemaV4RejectsUnknownMaxVisitAndDuplicateAction(t *testing.T) {
	for name, fragment := range map[string]string{
		"unknown max visit": "max_visits: {ghost: 2}\n",
		"duplicate action":  "max_visits: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			actions := "{approve: b, reject: $stop}"
			if name == "duplicate action" {
				actions = "{approve: b, approve: $stop}"
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := fmt.Sprintf(`schema_version: 4
pipeline: [a, b]
workflow:
  entry: a
  %s  edges:
    - from: a
      outcome: passed
      to: b
      approval:
        roles: [operator]
        quorum: any
        actions: %s
    - from: b
      outcome: passed
      to: $complete
`, fragment, actions)
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if name == "duplicate action" {
				if err == nil {
					t.Fatal("duplicate action принят")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(nil); err == nil || !strings.Contains(err.Error(), "ghost") {
				t.Fatalf("unknown max_visits принят: %v", err)
			}
		})
	}
}

func TestLegacySchemasRejected(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3, 5} {
		t.Run(fmt.Sprintf("schema_%d", version), func(t *testing.T) {
			cfg := &Config{
				SchemaVersion:  version,
				PipelineAgents: []AgentConfig{{Name: "analyst"}, {Name: "coder"}},
			}
			err := cfg.Validate(nil)
			if err == nil {
				t.Fatalf("schema_version %d должен быть отклонён", version)
			}
			if !strings.Contains(err.Error(), "легаси схемы") && version <= 3 {
				t.Fatalf("ожидалась подсказка про легаси схемы: %v", err)
			}
		})
	}
}

func TestLoadOldFormatRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("cli: claude\npipeline: [analyst, coder]\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("config без schema_version должен быть отклонён")
	}
}

func TestLoadNewFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(`
schema_version: 4
workflow:
  entry: analyst
  edges:
    - from: analyst
      outcome: passed
      to: coder
      approval:
        roles: [operator]
        quorum: any
        actions: {approve: coder, reject: $stop}
    - from: coder
      outcome: passed
      to: $complete
cli: opencode
model: claude-sonnet-4-20250514
pipeline:
  - name: analyst
    model: claude-sonnet-4-20250514
    effort: high
  - name: coder
    model: claude-opus-4-20250514
    cli: claude
`)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PipelineAgents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(cfg.PipelineAgents))
	}
	if cfg.PipelineAgents[0].Name != "analyst" {
		t.Errorf("expected analyst, got %s", cfg.PipelineAgents[0].Name)
	}
	if cfg.PipelineAgents[0].Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected sonnet model, got %s", cfg.PipelineAgents[0].Model)
	}
	if cfg.PipelineAgents[0].Effort != "high" {
		t.Errorf("expected high effort, got %s", cfg.PipelineAgents[0].Effort)
	}
	if _, err := cfg.CompiledGraph(); err != nil {
		t.Errorf("v4 config должен компилироваться: %v", err)
	}
}

func TestLoadRejectsLegacyAgentFields(t *testing.T) {
	tests := map[string]string{
		"transition":       "schema_version: 4\npipeline:\n  - name: analyst\n    transition: by_confirm\n",
		"gate_after":       "schema_version: 4\npipeline:\n  - name: analyst\n    gate_after: true\n",
		"checkpoint_after": "schema_version: 4\npipeline:\n  - name: analyst\n    checkpoint_after: interactive\n",
		"loopback_to":      "schema_version: 4\npipeline:\n  - name: analyst\n    loopback_to: coder\n",
		"approval_roles":   "schema_version: 4\npipeline:\n  - name: analyst\n    approval_roles: [operator]\n",
		"on_negative":      "schema_version: 4\npipeline:\n  - name: analyst\n    on_negative_verdict: ask\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("legacy поле должно быть отклонено как unknown field")
			}
		})
	}
}

func TestLoadRejectsUnknownDuplicateAndExtraDocuments(t *testing.T) {
	tests := map[string]string{
		"unknown top-level":   "schema_version: 4\npipeline: [analyst]\ngate_afer: true\n",
		"unknown agent field": "schema_version: 4\npipeline:\n  - name: analyst\n    gate_afer: true\n",
		"duplicate field":     "schema_version: 4\npipeline: [analyst]\ncli: one\ncli: two\n",
		"extra document":      "schema_version: 4\npipeline: [analyst]\n---\npipeline: [coder]\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("невалидный YAML должен быть отклонён")
			}
		})
	}
}

func TestAgentConfigFallback(t *testing.T) {
	cfg := &Config{
		SchemaVersion: CurrentSchemaVersion,
		PipelineAgents: []AgentConfig{
			{Name: "analyst", Effort: "high"},
			{Name: "coder"},
		},
		CLI:    "opencode",
		Model:  "auto",
		Effort: "medium",
	}

	ac := cfg.AgentConfig("analyst")
	if ac.Model != "auto" {
		t.Errorf("expected auto model fallback, got %s", ac.Model)
	}
	if ac.Effort != "high" {
		t.Errorf("expected high effort, got %s", ac.Effort)
	}
	if ac.CLI != "opencode" {
		t.Errorf("expected opencode CLI fallback, got %s", ac.CLI)
	}

	ac2 := cfg.AgentConfig("coder")
	if ac2.Effort != "medium" {
		t.Errorf("expected medium effort fallback, got %s", ac2.Effort)
	}
}

func TestDefaultWithGraphApprovals(t *testing.T) {
	cfg := Default()
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("default schema_version=%d", cfg.SchemaVersion)
	}
	if cfg.Workflow == nil || cfg.Workflow.Edges[0].Approval == nil ||
		cfg.Workflow.Edges[0].Approval.Roles[0] != "product_owner" {
		t.Errorf("expected graph edge approval role product_owner, got %+v", cfg.Workflow)
	}
	rejectedEdges := 0
	for _, edge := range cfg.Workflow.Edges {
		if edge.Outcome == "rejected" && edge.To == "coder" {
			rejectedEdges++
		}
	}
	if rejectedEdges == 0 {
		t.Error("default workflow должен содержать rejected→coder loopback рёбра")
	}
}

func TestValidate(t *testing.T) {
	v4Workflow := func() *WorkflowConfig {
		return &WorkflowConfig{
			Entry: "a",
			Edges: []WorkflowEdgeConfig{{From: "a", Outcome: "passed", To: "$complete"}},
		}
	}
	valid := &Config{
		SchemaVersion:  CurrentSchemaVersion,
		PipelineAgents: []AgentConfig{{Name: "a", Effort: "high", Timeout: "45m"}},
		StageTimeout:   "30m",
		Workflow:       v4Workflow(),
	}
	if err := valid.Validate(nil); err != nil {
		t.Errorf("валидный конфиг не должен давать ошибку: %v", err)
	}

	cases := []struct {
		name string
		cfg  *Config
	}{
		{"empty pipeline", &Config{}},
		{"bad effort", &Config{SchemaVersion: 4, PipelineAgents: []AgentConfig{{Name: "a", Effort: "max"}}}},
		{"bad global cli", &Config{SchemaVersion: 4, CLI: "claude", PipelineAgents: []AgentConfig{{Name: "a"}}}},
		{"bad agent cli", &Config{SchemaVersion: 4, PipelineAgents: []AgentConfig{{Name: "a", CLI: "claude"}}}},
		{"bad timeout", &Config{SchemaVersion: 4, PipelineAgents: []AgentConfig{{Name: "a", Timeout: "30 minutes"}}}},
		{"bad stage_timeout", &Config{SchemaVersion: 4, PipelineAgents: []AgentConfig{{Name: "a"}}, StageTimeout: "later"}},
		{"unsupported schema", &Config{SchemaVersion: 99, PipelineAgents: []AgentConfig{{Name: "a"}}}},
		{"nonpositive global timeout", &Config{SchemaVersion: 4, StageTimeout: "0s", PipelineAgents: []AgentConfig{{Name: "a"}}}},
		{"nonpositive stage timeout", &Config{SchemaVersion: 4, PipelineAgents: []AgentConfig{{Name: "a", Timeout: "-1s"}}}},
		{"duplicate stage", &Config{SchemaVersion: 4, PipelineAgents: []AgentConfig{{Name: "a"}, {Name: "a"}}}},
		{"missing workflow", &Config{SchemaVersion: 4, PipelineAgents: []AgentConfig{{Name: "a"}}}},
		{"bad check", &Config{SchemaVersion: 4, PipelineAgents: []AgentConfig{{Name: "a", Checks: []checks.Definition{{Name: "", Class: "unit", Policy: "required"}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(nil); err == nil {
				t.Error("ожидалась ошибка валидации")
			}
		})
	}
}

type fakeLookup map[string]bool

func (f fakeLookup) Exists(name string) bool { return f[name] }

func TestValidate_UnknownAgent(t *testing.T) {
	cfg := &Config{PipelineAgents: []AgentConfig{{Name: "analyst"}, {Name: "ghost"}}}
	err := cfg.Validate(fakeLookup{"analyst": true})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("ожидалась ошибка про ghost, got: %v", err)
	}
}

func TestAgentConfig_Defaults(t *testing.T) {
	cfg := &Config{
		PipelineAgents: []AgentConfig{{Name: "a"}},
		StageTimeout:   "30m",
	}
	ac := cfg.AgentConfig("a")
	if ac.Timeout != "30m" {
		t.Errorf("timeout должен наследоваться из stage_timeout, got %q", ac.Timeout)
	}
	d, err := ac.StageTimeoutFor()
	if err != nil || d.Minutes() != 30 {
		t.Errorf("StageTimeoutFor() = %v, %v", d, err)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	src := Default()
	data, err := src.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("сериализованный Default не парсится: %v\n%s", err, data)
	}
	if len(loaded.PipelineAgents) != len(src.PipelineAgents) {
		t.Fatalf("агентов после round-trip: %d, ожидалось %d", len(loaded.PipelineAgents), len(src.PipelineAgents))
	}
	if loaded.Workflow == nil || loaded.Workflow.Edges[0].Approval == nil ||
		loaded.Workflow.Edges[0].Approval.Roles[0] != "product_owner" {
		t.Errorf("edge approval у analyst потерян при round-trip: %+v", loaded.Workflow)
	}
	if loaded.Workflow.MaxVisits["coder"] != 3 {
		t.Error("max_visits у coder потерян при round-trip")
	}
	if loaded.StageTimeout != "30m" {
		t.Errorf("stage_timeout после round-trip: %q", loaded.StageTimeout)
	}
}
