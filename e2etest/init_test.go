package e2etest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSampleProjectStructure(t *testing.T) {
	dir := "sample-project"
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatal("sample project not found")
	}
	checkFile(t, filepath.Join(dir, "main.go"))
	checkFile(t, filepath.Join(dir, "go.mod"))
}

func TestAiTeamInit(t *testing.T) {
	dir := t.TempDir()
	artifacts := []string{
		filepath.Join(dir, ".ai-team", "artifacts", "product"),
		filepath.Join(dir, ".ai-team", "artifacts", "tech"),
		filepath.Join(dir, ".ai-team", "artifacts", "reviews"),
		filepath.Join(dir, ".ai-team", "artifacts", "tasks"),
	}
	for _, d := range artifacts {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, ".ai-team", "config.yaml")
	cfgContent := []byte("pipeline: [analyst, architect, coder, reviewer, tester, deployer]\ncli: opencode\nmodel: auto\n")
	if err := os.WriteFile(cfgPath, cfgContent, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Error("config should exist")
	}
}
