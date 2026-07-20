package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
}

func TestLoadOldFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("cli: claude\npipeline: [analyst, coder]\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CLI != "claude" {
		t.Errorf("expected claude, got %s", cfg.CLI)
	}
	if len(cfg.PipelineAgents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(cfg.PipelineAgents))
	}
	if cfg.PipelineAgents[0].Name != "analyst" {
		t.Errorf("expected analyst, got %s", cfg.PipelineAgents[0].Name)
	}
}

func TestLoadMigratesV2GoTestToTypedFreshEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte("schema_version: 2\npipeline:\n  - name: tester\n    checks:\n      - name: go-test\n        class: unit\n        command: [go, test, ./...]\n        policy: required\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	check := cfg.PipelineAgents[0].Checks[0]
	joined := strings.Join(check.Command, " ")
	if cfg.SchemaVersion != CurrentSchemaVersion || check.Adapter != "go-test-json" || !strings.Contains(joined, "-json") || !strings.Contains(joined, "-count=1") {
		t.Fatalf("v2 migration incomplete: schema=%d check=%+v", cfg.SchemaVersion, check)
	}
}

func TestLoadNewFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(`
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
}

func TestLoadRejectsUnknownDuplicateAndExtraDocuments(t *testing.T) {
	tests := map[string]string{
		"unknown top-level":   "pipeline: [analyst]\ngate_afer: true\n",
		"unknown agent field": "pipeline:\n  - name: analyst\n    gate_afer: true\n",
		"duplicate field":     "pipeline: [analyst]\ncli: one\ncli: two\n",
		"extra document":      "pipeline: [analyst]\n---\npipeline: [coder]\n",
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

	ac3 := cfg.AgentConfig("analyst")
	if ac3.Transition != "auto" {
		t.Errorf("expected auto transition fallback, got %s", ac3.Transition)
	}
}

func TestLoadNewFormatWithTransitions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(`
pipeline:
  - name: coder
    transition: by_confirm
    max_retries: 2
  - name: reviewer
    transition: auto
    max_retries: 2
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
	if cfg.PipelineAgents[0].Transition != "by_confirm" {
		t.Errorf("expected by_confirm transition, got %s", cfg.PipelineAgents[0].Transition)
	}
	if cfg.PipelineAgents[0].MaxRetries != 2 {
		t.Errorf("expected max_retries=2, got %d", cfg.PipelineAgents[0].MaxRetries)
	}
}

func TestLoadNewFormatWithGates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(`
pipeline:
  - name: analyst
    gate_after: true
  - name: architect
    gate_after: true
  - name: coder
  - name: deployer
    gate_before: true
`)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PipelineAgents) != 4 {
		t.Fatalf("expected 4 agents, got %d", len(cfg.PipelineAgents))
	}
	if !cfg.PipelineAgents[0].GateAfter {
		t.Errorf("expected gate_after=true for analyst")
	}
	if !cfg.PipelineAgents[1].GateAfter {
		t.Errorf("expected gate_after=true for architect")
	}
	if cfg.PipelineAgents[2].GateAfter {
		t.Errorf("expected gate_after=false for coder")
	}
	if !cfg.PipelineAgents[3].GateBefore {
		t.Errorf("expected gate_before=true for deployer")
	}
}

func TestDefaultWithGates(t *testing.T) {
	cfg := Default()
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("default schema_version=%d", cfg.SchemaVersion)
	}
	if cfg.PipelineAgents[0].CheckpointAfter != CheckpointRequireExplicit {
		t.Errorf("expected default analyst checkpoint_after=require_explicit")
	}
	if cfg.PipelineAgents[1].CheckpointAfter != CheckpointRequireExplicit {
		t.Errorf("expected default architect checkpoint_after=require_explicit")
	}
	if cfg.PipelineAgents[6].CheckpointBeforePolicy() != CheckpointAuto {
		t.Errorf("delivery имеет отдельный mandatory approval и не должен дублировать checkpoint_before")
	}
}

func TestValidate(t *testing.T) {
	valid := &Config{
		PipelineAgents: []AgentConfig{
			{Name: "a", Transition: "by_confirm", Effort: "high", OnNegativeVerdict: "ask", Timeout: "45m"},
			{Name: "b"},
		},
		StageTimeout: "30m",
	}
	if err := valid.Validate(nil); err != nil {
		t.Errorf("валидный конфиг не должен давать ошибку: %v", err)
	}

	cases := []struct {
		name string
		cfg  *Config
	}{
		{"empty pipeline", &Config{}},
		{"bad transition", &Config{PipelineAgents: []AgentConfig{{Name: "a", Transition: "confrim"}}}},
		{"bad effort", &Config{PipelineAgents: []AgentConfig{{Name: "a", Effort: "max"}}}},
		{"bad global cli", &Config{CLI: "claude", PipelineAgents: []AgentConfig{{Name: "a"}}}},
		{"bad agent cli", &Config{PipelineAgents: []AgentConfig{{Name: "a", CLI: "claude"}}}},
		{"bad on_negative_verdict", &Config{PipelineAgents: []AgentConfig{{Name: "a", OnNegativeVerdict: "ignore"}}}},
		{"bad timeout", &Config{PipelineAgents: []AgentConfig{{Name: "a", Timeout: "30 minutes"}}}},
		{"bad stage_timeout", &Config{PipelineAgents: []AgentConfig{{Name: "a"}}, StageTimeout: "later"}},
		{"negative retries", &Config{PipelineAgents: []AgentConfig{{Name: "a", MaxRetries: -1}}}},
		{"bad checkpoint", &Config{PipelineAgents: []AgentConfig{{Name: "a", CheckpointAfter: "sometimes"}}}},
		{"overlapping checkpoint", &Config{PipelineAgents: []AgentConfig{{Name: "a", CheckpointAfter: CheckpointInteractive, GateAfter: true}}}},
		{"v2 legacy checkpoint", &Config{SchemaVersion: CurrentSchemaVersion, PipelineAgents: []AgentConfig{{Name: "a", GateAfter: true}}}},
		{"unsupported schema", &Config{SchemaVersion: 99, PipelineAgents: []AgentConfig{{Name: "a"}}}},
		{"nonpositive global timeout", &Config{StageTimeout: "0s", PipelineAgents: []AgentConfig{{Name: "a"}}}},
		{"nonpositive stage timeout", &Config{PipelineAgents: []AgentConfig{{Name: "a", Timeout: "-1s"}}}},
		{"duplicate stage", &Config{PipelineAgents: []AgentConfig{{Name: "a"}, {Name: "a"}}}},
		{"loopback future stage", &Config{PipelineAgents: []AgentConfig{{Name: "reviewer", LoopbackTo: "coder"}, {Name: "coder"}}}},
		{"loopback partial name", &Config{PipelineAgents: []AgentConfig{{Name: "go-coder"}, {Name: "reviewer", LoopbackTo: "coder"}}}},
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
	if ac.OnNegativeVerdict != OnNegativeStop {
		t.Errorf("default on_negative_verdict = %q, want stop", ac.OnNegativeVerdict)
	}
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
	// Гейты и retries переживают сериализацию (главный баг старого init)
	if loaded.PipelineAgents[0].CheckpointAfter != CheckpointRequireExplicit {
		t.Error("checkpoint_after у analyst потерян при round-trip")
	}
	if loaded.PipelineAgents[6].CheckpointBeforePolicy() != CheckpointAuto {
		t.Error("у deployer появился дублирующий checkpoint_before")
	}
	if loaded.PipelineAgents[2].MaxRetries != 2 {
		t.Error("max_retries у coder потерян при round-trip")
	}
	if loaded.StageTimeout != "30m" {
		t.Errorf("stage_timeout после round-trip: %q", loaded.StageTimeout)
	}
}
