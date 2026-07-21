package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckCLI_RejectsUnknownAdapter(t *testing.T) {
	err := CheckCLI("nonexistent-cli-12345")
	if err == nil {
		t.Fatal("expected error for unsupported CLI")
	}
	if !strings.Contains(err.Error(), "не поддерживается") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckCLI_OpenCodeNotFound(t *testing.T) {
	err := CheckCLI(filepath.Join(t.TempDir(), "opencode"))
	if err == nil || !strings.Contains(err.Error(), "не найдена") {
		t.Fatalf("expected missing opencode error, got %v", err)
	}
}

func TestNewRuntime(t *testing.T) {
	r, err := NewRuntime("agentcli")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*AgentCLIRuntime); !ok {
		t.Error("expected AgentCLIRuntime")
	}

	r, err = NewRuntime("llm")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*LLMRuntime); !ok {
		t.Error("expected LLMRuntime")
	}

	_, err = NewRuntime("unknown")
	if err == nil {
		t.Error("expected error for unknown runtime")
	}
}

func TestLLMRuntime_ReturnsNotImplemented(t *testing.T) {
	r := &LLMRuntime{}
	err := r.Execute(context.Background(), &Agent{}, &Task{}, nil)
	if err != ErrNotImplemented {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestReplaceVars(t *testing.T) {
	result := ReplaceVars("tasks/{feature}/task.md", "auth")
	expected := "tasks/auth/task.md"
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}

	result = ReplaceVars("no-vars", "auth")
	if result != "no-vars" {
		t.Errorf("expected no-vars, got %s", result)
	}
}

func TestBuildPrompt(t *testing.T) {
	dir := t.TempDir()
	inputFile := filepath.Join(dir, "input.md")
	os.WriteFile(inputFile, []byte("hello world"), 0644)

	r := &AgentCLIRuntime{}
	agent := &Agent{
		Name:   "test-agent",
		Prompt: "You are a test agent.",
		Inputs: map[string]string{"task": "input.md"},
	}
	task := &Task{
		Feature:      "test-feature",
		TaskDesc:     "Test task",
		ArtifactRoot: dir,
	}

	inputs := []Artifact{
		{Name: "task", Path: inputFile},
	}

	prompt, err := r.buildPrompt(agent, task, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "test-agent") {
		t.Error("prompt should contain agent name")
	}
	if !strings.Contains(prompt, "hello world") {
		t.Error("prompt should contain input file content")
	}
}

func TestAgentCLIArgsUsesPromptFileAndRejectsUnknownAdapters(t *testing.T) {
	args, err := AgentCLIArgs("/usr/local/bin/opencode", "provider/model", "/tmp/prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "run -m provider/model --file /tmp/prompt.md") || strings.Contains(joined, "prompt contents") {
		t.Fatalf("unexpected args: %v", args)
	}
	if _, err := AgentCLIArgs("claude", "", "/tmp/prompt.md"); err == nil {
		t.Fatal("unknown CLI must not receive guessed OpenCode arguments")
	}
}

func TestPromptFilePermissionsAndCleanup(t *testing.T) {
	path, cleanup, err := writePromptFile(strings.Repeat("large prompt\n", 10000))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("prompt file mode: info=%v err=%v", info, err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("prompt file must be removed: %v", err)
	}
}

func TestOpenCodeIsolationDeniesEffectsAndNarrowsEdits(t *testing.T) {
	target := t.TempDir()
	task := &Task{TargetDir: target, ArtifactRoot: filepath.Join(target, ".ai-team", "artifacts"), Feature: "feat"}
	agent := &Agent{
		Name: "tester", Mutation: "tests", AllowedPaths: []string{"**/*_test.go"},
		Outputs: map[string]string{"report": "{feature}/test-report.md"},
	}
	inputPath := filepath.Join(target, ".ai-team", "runs", "run-1", "inflight-inputs", "001")
	if err := os.MkdirAll(inputPath, 0o755); err != nil {
		t.Fatal(err)
	}
	environment, cleanup, err := OpenCodeIsolationEnvironment(agent, task, Artifact{Name: "input", Path: inputPath})
	if err != nil {
		t.Fatal(err)
	}
	configHome := environmentValue(environment, "XDG_CONFIG_HOME")
	cleanup()
	if _, err := os.Stat(configHome); !os.IsNotExist(err) {
		t.Fatalf("isolated config directory must be removed: %v", err)
	}
	var permission map[string]any
	if err := json.Unmarshal([]byte(environmentValue(environment, "OPENCODE_PERMISSION")), &permission); err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{"bash", "task", "webfetch", "websearch", "external_directory"} {
		if permission[denied] != "deny" {
			t.Fatalf("%s must be denied: %#v", denied, permission[denied])
		}
	}
	edits, ok := permission["edit"].(map[string]any)
	if !ok {
		t.Fatalf("edit rules missing: %#v", permission["edit"])
	}
	if edits["**/*_test.go"] != "allow" || edits[".ai-team/**"] != "deny" || edits[".ai-team/artifacts/feat/test-report.md"] != "allow" {
		t.Fatalf("unexpected edit rules: %#v", edits)
	}
	reads, ok := permission["read"].(map[string]any)
	if !ok || reads[".env"] != "deny" || reads[".git/**"] != "deny" || reads[".ai-team/**"] != "deny" ||
		reads[filepath.ToSlash(inputPath)+"/**"] != "allow" {
		t.Fatalf("unexpected read rules: %#v", permission["read"])
	}
	if environmentValue(environment, "OPENCODE_DISABLE_DEFAULT_PLUGINS") != "true" {
		t.Fatal("default plugins must be disabled")
	}
}

func TestOpenCodeIsolationRejectsProjectExecutionSurfaces(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".opencode", "plugins"), 0755); err != nil {
		t.Fatal(err)
	}
	_, _, err := OpenCodeIsolationEnvironment(&Agent{Name: "analyst", Mutation: "none"}, &Task{
		TargetDir: target, ArtifactRoot: filepath.Join(target, ".ai-team", "artifacts"), Feature: "feat",
	})
	if err == nil || !strings.Contains(err.Error(), "execution surface") {
		t.Fatalf("custom plugins must fail closed: %v", err)
	}
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, item := range environment {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}
