package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckCLI_NotFound(t *testing.T) {
	err := CheckCLI("nonexistent-cli-12345")
	if err == nil {
		t.Fatal("expected error for nonexistent CLI")
	}
	if !strings.Contains(err.Error(), "не найдена") {
		t.Errorf("unexpected error: %v", err)
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
