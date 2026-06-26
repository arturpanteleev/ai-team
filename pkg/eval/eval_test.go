package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewEval(t *testing.T) {
	e := New("test-agent", "/tmp/artifact.md", []string{"полнота", "качество"})
	if e.AgentName != "test-agent" {
		t.Errorf("expected test-agent, got %s", e.AgentName)
	}
	if e.ArtifactPath != "/tmp/artifact.md" {
		t.Errorf("expected /tmp/artifact.md, got %s", e.ArtifactPath)
	}
	if len(e.Criteria) != 2 {
		t.Errorf("expected 2 criteria, got %d", len(e.Criteria))
	}
}

func TestBuildJudgePrompt(t *testing.T) {
	e := New("coder", "/tmp/code.go", []string{"полнота", "тестируемость"})
	prompt := e.buildJudgePrompt("package main\n\nfunc main() {}")
	if !strings.Contains(prompt, "coder") {
		t.Error("prompt should contain agent name")
	}
	if !strings.Contains(prompt, "package main") {
		t.Error("prompt should contain artifact content")
	}
	if !strings.Contains(prompt, "полнота") {
		t.Error("prompt should contain criteria")
	}
}

func TestBuildJudgePromptDefaultCriteria(t *testing.T) {
	e := New("coder", "/tmp/code.go", nil)
	prompt := e.buildJudgePrompt("content")
	if !strings.Contains(prompt, "тестируемость") {
		t.Error("prompt should contain default criteria")
	}
}

func TestExtractScore(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"**Оценка:** 7", 7},
		{"Оценка: 5", 5},
		{"**Оценка:** 10\n**Комментарий:** good", 10},
		{"No score here", 0},
		{"**Оценка:** 0", 0},
		{"**Оценка:** 11", 0},
	}
	for _, tt := range tests {
		got := extractScore(tt.input)
		if got != tt.want {
			t.Errorf("extractScore(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestEvalRun_FileNotFound(t *testing.T) {
	e := New("test", "/nonexistent/path.md", nil)
	_, err := e.Run(nil)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "не удалось прочитать") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEvalRun_OpenCodeNotFound(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "artifact.md")
	os.WriteFile(artifactPath, []byte("test content"), 0644)

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir)
	defer os.Setenv("PATH", oldPath)

	e := New("test", artifactPath, nil)
	_, err := e.Run(nil)
	if err == nil {
		t.Error("expected error when opencode not on PATH")
	}
	if !strings.Contains(err.Error(), "не найден") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}
