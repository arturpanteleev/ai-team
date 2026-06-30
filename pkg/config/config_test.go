package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.CLI != "opencode" {
		t.Errorf("expected opencode, got %s", cfg.CLI)
	}
	if len(cfg.PipelineAgents) != 6 {
		t.Errorf("expected 6 agents, got %d", len(cfg.PipelineAgents))
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

func TestAgentConfigFallback(t *testing.T) {
	cfg := &Config{
		PipelineAgents: []AgentConfig{
			{Name: "analyst", Effort: "high"},
			{Name: "coder"},
		},
		CLI:   "opencode",
		Model: "auto",
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
