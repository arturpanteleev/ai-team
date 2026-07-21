package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestEvalQualityIsStrictStatisticalAdvisoryAndIsolated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "artifact.md")
	if err := os.WriteFile(artifactPath, []byte("artifact"), 0644); err != nil {
		t.Fatal(err)
	}
	pwdPath := filepath.Join(dir, "judge-pwd")
	t.Setenv("AI_TEAM_EVAL_PWD", pwdPath)
	judge := filepath.Join(dir, "judge")
	if err := os.WriteFile(judge, []byte("#!/bin/sh\npwd > \"$AI_TEAM_EVAL_PWD\"\necho '**Оценка:** 8'\necho '**Комментарий:** хорошо'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	evaluation := New("analyst", artifactPath, nil)
	evaluation.CLI = judge
	evaluation.Dir = dir
	quality, err := evaluation.RunQuality(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if !quality.Advisory || quality.Layer != LayerLLMQuality || quality.Median != 8 || quality.Mean != 8 || len(quality.Samples) != 3 {
		t.Fatalf("quality result: %+v", quality)
	}
	judgePWD, err := os.ReadFile(pwdPath)
	if err != nil || strings.TrimSpace(string(judgePWD)) == dir {
		t.Fatalf("judge должен работать в isolated dir: pwd=%q err=%v", judgePWD, err)
	}
	outputPath := filepath.Join(dir, "evidence", "eval.json")
	if err := WriteQualityResult(outputPath, quality); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(outputPath); err != nil || !strings.Contains(string(data), `"advisory": true`) {
		t.Fatalf("JSON evidence: %s err=%v", data, err)
	}
}

func TestStrictJudgeOutputRejectsAmbiguity(t *testing.T) {
	for name, output := range map[string]string{
		"missing comment": "**Оценка:** 8\n",
		"multiple scores": "**Оценка:** 8\n**Оценка:** 9\n**Комментарий:** x\n",
		"empty comment":   "**Оценка:** 8\n**Комментарий:**\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseJudgeOutput(output); err == nil {
				t.Fatal("ambiguous output должен быть отклонён")
			}
		})
	}
}

func TestLayeredSuiteAndLLMAdvisoryInvariant(t *testing.T) {
	suite, err := RunSuite(context.Background(), []Case{
		{Name: "contract", Layer: LayerDeterministic, Run: func(context.Context) error { return nil }},
		{Name: "behavior", Layer: LayerBehavioral, Run: func(context.Context) error { return errors.New("fixture failed") }},
		{Name: "fault", Layer: LayerFault, Run: func(context.Context) error { return nil }},
		{Name: "judge", Layer: LayerLLMQuality, Advisory: true, Run: func(context.Context) error { return nil }},
	})
	if err == nil || len(suite.Cases) != 4 || suite.Cases[1].Passed || !suite.Cases[3].Advisory {
		t.Fatalf("layered suite: %+v err=%v", suite, err)
	}
	if _, err := RunSuite(context.Background(), []Case{{
		Name: "unsafe-hard-gate", Layer: LayerLLMQuality, Advisory: false, Run: func(context.Context) error { return nil },
	}}); err == nil {
		t.Fatal("uncalibrated LLM score cannot be a hard gate")
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
