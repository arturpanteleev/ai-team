package config

import (
	"os"
	"path/filepath"
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
			if profile := cfg.ApplyDetectedChecks(dir); profile != test.profile {
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
			if profile := cfg.ApplyDetectedChecks(dir); profile != "" || len(cfg.findAgent("tester").Checks) != 0 {
				t.Fatalf("unsupported stack must fail closed instead of emitting untyped evidence: profile=%q", profile)
			}
		})
	}
}

func TestApplyDetectedChecksDoesNotGuessUnknownProject(t *testing.T) {
	cfg := Default()
	if profile := cfg.ApplyDetectedChecks(t.TempDir()); profile != "" || len(cfg.findAgent("tester").Checks) != 0 {
		t.Fatalf("unknown stack не должен получать guessed command: profile=%q", profile)
	}
}
