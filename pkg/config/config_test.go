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
	if len(cfg.Pipeline) != 6 {
		t.Errorf("expected 6 agents, got %d", len(cfg.Pipeline))
	}
}

func TestLoad(t *testing.T) {
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
	if len(cfg.Pipeline) != 2 {
		t.Errorf("expected 2 agents, got %d", len(cfg.Pipeline))
	}
}
