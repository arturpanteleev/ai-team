package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestHumanAgentQuestionAnswerSurvivesExecutorChoice(t *testing.T) {
	dir := env(t)
	registry := testRegistry()
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "question-human-input-test",
		Title:         "Question human input test",
		Stages: []config.TemplateStage{{
			ID: "product_spec", Title: "Product specification", Function: "product_owner",
			Result: "md", Executor: "human", Agent: "analyst", Confirm: "auto",
		}},
	}
	rt := newScripted()
	rt.blocked["analyst"] = "нужен ответ Product Owner"
	rt.content["analyst"] = map[string]string{"proposal": "# Proposal\n"}
	var foundAnswer, foundBrief, foundQuestions bool
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name != "analyst" {
			return
		}
		if rt.calls[name] == 1 {
			path := filepath.Join(rt.targetDir, ".ai-team", "artifacts", "tasks", "feat", "questions.md")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("Кто целевой клиент?\n"), 0644); err != nil {
				t.Fatal(err)
			}
			return
		}
		delete(rt.blocked, name)
		for _, input := range inputs {
			data, err := os.ReadFile(input.Path)
			if err != nil {
				t.Errorf("read %s: %v", input.Name, err)
				continue
			}
			switch input.Name {
			case "questions":
				foundQuestions = strings.Contains(string(data), "целевой клиент")
			case "clarification-answer":
				foundAnswer = strings.Contains(string(data), "B2B-клиенты")
			case "business-brief":
				foundBrief = strings.Contains(string(data), "B2B-клиенты")
			}
		}
	}
	p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	resume := func(runID string) (RunResult, approval.PendingApproval) {
		t.Helper()
		result, runErr := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: runID, TargetDir: dir})
		var required *ApprovalRequiredError
		if !errors.As(runErr, &required) {
			t.Fatalf("expected approval: result=%+v err=%v", result, runErr)
		}
		pending, loadErr := store.Load(runID, required.ApprovalID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		return result, pending
	}
	decide := func(runID string, pending approval.PendingApproval, action, comment string) {
		t.Helper()
		if _, err := store.Decide(runID, pending.ID, approval.Decision{
			ActorID: "product-1", ActorRole: "product_owner", Action: action,
			Comment: comment, SubjectHash: pending.SubjectHash,
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "Google login", TargetDir: dir})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("initial human input: result=%+v err=%v", first, err)
	}
	input, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	decide(first.RunID, input, "run_agent", "")
	_, questions := resume(first.RunID)
	if questions.Kind != approval.KindQuestions {
		t.Fatalf("expected questions, got %s", questions.Kind)
	}
	decide(first.RunID, questions, "answer_questions", "B2B-клиенты")
	_, secondInput := resume(first.RunID)
	if secondInput.Kind != approval.KindInput {
		t.Fatalf("expected executor choice after answer, got %s", secondInput.Kind)
	}
	decide(first.RunID, secondInput, "run_agent", "")
	finished, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil || finished.Outcome != workflow.RunCompleted || rt.calls["analyst"] != 2 {
		t.Fatalf("agent did not continue after answer: result=%+v calls=%v err=%v", finished, rt.calls, err)
	}
	if !foundQuestions || !foundAnswer || !foundBrief {
		t.Fatalf("second agent attempt lost context: questions=%v answer=%v brief=%v", foundQuestions, foundAnswer, foundBrief)
	}
}
