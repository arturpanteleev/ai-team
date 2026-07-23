package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/checks"
)

func TestApplyDetectedChecks(t *testing.T) {
	tests := []struct{ name, manifest, content, profile, command string }{
		{"go", "go.mod", "module example.test/x\n", "go", "go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, test.manifest), []byte(test.content), 0644); err != nil {
				t.Fatal(err)
			}
			cfg := Default()
			profile, warning := cfg.ApplyDetectedChecks(dir)
			if profile != test.profile || warning != "" {
				t.Fatalf("profile=%q want %q", profile, test.profile)
			}
			tester := cfg.findAgent("tester")
			if tester == nil || len(tester.Checks) == 0 || tester.Checks[0].Policy != checks.PolicyRequired || tester.Checks[0].Command[0] != test.command {
				t.Fatalf("checks не настроены: %+v", tester)
			}
		})
	}
}

func TestApplyDetectedChecksDoesNotOverclaimUnsupportedTypedEvidence(t *testing.T) {
	tests := []struct{ name, manifest, content string }{
		{"rust", "Cargo.toml", "[package]\nname='x'\n"},
		{"python", "pyproject.toml", "[project]\nname='x'\n"},
		{"node", "package.json", `{"scripts":{"test":"vitest"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, test.manifest), []byte(test.content), 0644); err != nil {
				t.Fatal(err)
			}
			cfg := Default()
			profile, _ := cfg.ApplyDetectedChecks(dir)
			if profile != "" || len(cfg.findAgent("tester").Checks) != 0 {
				t.Fatalf("unsupported stack must fail closed instead of emitting untyped evidence: profile=%q", profile)
			}
		})
	}
}

func TestApplyDetectedChecksWarnsWhenNoTesterStage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	// Simulate a pipeline that renamed (or removed) the "tester" stage.
	for i := range cfg.PipelineAgents {
		if cfg.PipelineAgents[i].Name == "tester" {
			cfg.PipelineAgents[i].Name = "test-runner"
		}
	}
	profile, warning := cfg.ApplyDetectedChecks(dir)
	if profile != "go" {
		t.Fatalf("stack detection itself must still succeed: profile=%q", profile)
	}
	if warning == "" || !strings.Contains(warning, "tester") {
		t.Fatalf("a detected profile with no eligible stage must produce a specific warning, got %q", warning)
	}
}

func TestApplyDetectedChecksDoesNotGuessUnknownProject(t *testing.T) {
	cfg := Default()
	profile, _ := cfg.ApplyDetectedChecks(t.TempDir())
	if profile != "" || len(cfg.findAgent("tester").Checks) != 0 {
		t.Fatalf("unknown stack не должен получать guessed command: profile=%q", profile)
	}
}
