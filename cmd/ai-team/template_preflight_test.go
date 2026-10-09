package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
)

func TestProjectTemplatePreflightReadsPublishedConfigEachTime(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.Mkdir(controlDir, 0755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(controlDir, "config.yaml")
	initial := []byte("schema_version: 5\ntemplate: x\ntitle: X\nstages:\n  - id: gate\n    title: Gate\n    function: unknown-cloud-role\n    result: approve\n    confirm: required\n  - id: finish\n    title: Finish\n    function: developer\n    result: md\n")
	if err := os.WriteFile(configPath, initial, 0644); err != nil {
		t.Fatal(err)
	}
	checker := projectTemplatePreflight{target: target, reg: agent.NewFS(os.DirFS("../../agents"))}
	first := checker.Check(context.Background())
	if first.Ready || len(first.Checks) != 1 || first.Checks[0].ID != "template" ||
		!strings.Contains(first.Checks[0].Message, "недоступна в cloud") {
		t.Fatalf("invalid current template was not surfaced: %+v", first)
	}

	if err := os.WriteFile(configPath, []byte("schema_version: ["), 0644); err != nil {
		t.Fatal(err)
	}
	second := checker.Check(context.Background())
	if second.Ready || len(second.Checks) != 1 || second.Checks[0].ID != "template" ||
		!strings.Contains(second.Checks[0].Message, "yaml:") {
		t.Fatalf("preflight reused a stale template: %+v", second)
	}
}
