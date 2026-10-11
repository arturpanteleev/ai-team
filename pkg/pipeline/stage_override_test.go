package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// stage_override_test.go — сквозной обход проверки этапа (B-41).

func twoStageOverrideConfig(check *config.TemplateCheck) *config.Config {
	return &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "stage-override-test",
		Title:         "Stage override test",
		PipelineAgents: []config.AgentConfig{
			{Name: "tech"},
			{Name: "qa"},
		},
		Stages: []config.TemplateStage{
			{ID: "tech", Title: "Техническое решение", Function: "architect", Result: "md", Executor: "agent", Agent: "tech", Confirm: "auto", Check: check},
			{ID: "qa", Title: "QA", Function: "qa", Result: "md", Executor: "agent", Agent: "qa", Confirm: "auto"},
		},
		CLI: "opencode",
	}
}

func twoStageOverrideRegistry() *agent.Registry {
	return agent.NewFS(fstest.MapFS{
		"tech/def.yaml": def(`name: tech
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  result: '{feature}/tech.md'
`),
		"tech/prompt.md": def("test"),
		"qa/def.yaml": def(`name: qa
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  result: '{feature}/qa.md'
`),
		"qa/prompt.md": def("test"),
	})
}

func runTwoStageOverridePipeline(t *testing.T, dir string, cfg *config.Config, rt *scriptedRuntime) error {
	t.Helper()
	p := New(cfg, twoStageOverrideRegistry(),
		WithRuntimeFactory(rt.factory),
		WithNotifier(&captureNotifier{}),
		WithPrompter(&scriptedPrompter{}),
	)
	return p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true,
	})
}

func findOverrideDecision(t *testing.T, dir string) *approval.PendingApproval {
	t.Helper()
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	values, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	for index := range values {
		if values[index].Kind == approval.KindCheckDecision {
			return &values[index]
		}
	}
	t.Fatalf("check_decision approval не найден: %+v", values)
	return nil
}

func decideOverride(t *testing.T, dir string, decision *approval.PendingApproval, reason string) {
	t.Helper()
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(decision.RunID, decision.ID, approval.Decision{
		ActorID: "local-user", ActorRole: "architect", Action: "override",
		Comment: reason, SubjectHash: decision.SubjectHash, DecidedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("resolve override: %v", err)
	}
}

func resumeTwoStageOverridePipeline(t *testing.T, dir string, cfg *config.Config, rt *scriptedRuntime, runID string) (workflow.RunOutcome, error) {
	t.Helper()
	p := New(cfg, twoStageOverrideRegistry(),
		WithRuntimeFactory(rt.factory),
		WithNotifier(&captureNotifier{}),
		WithPrompter(&scriptedPrompter{}),
	)
	result, err := p.RunWithResult(context.Background(), RunConfig{Feature: "", TargetDir: dir, ResumeRunID: runID})
	return result.Outcome, err
}

func TestRun_CheckOverrideAllowsTransition(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Решение\n\nбез маркера\n"}
	rt.content["qa"] = map[string]string{"result": "# QA\n\nок\n"}
	cfg := twoStageOverrideConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	err := runTwoStageOverridePipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("первый запуск должен заблокироваться: %v", err)
	}
	decision := findOverrideDecision(t, dir)
	decideOverride(t, dir, decision, "риск принят владельцем")

	runID := filepath.Base(onlyRunDir(t, dir))
	outcome, err := resumeTwoStageOverridePipeline(t, dir, cfg, rt, runID)
	if err != nil {
		t.Fatalf("resume после обхода должен завершиться: %v", err)
	}
	if outcome != workflow.RunCompleted {
		t.Fatalf("ожидали RunCompleted, got %q", outcome)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_overridden") {
		t.Fatalf("ожидали событие check_overridden:\n%s", events)
	}
	if !strings.Contains(events, `"severity":"warning"`) {
		t.Fatalf("check_overridden должен иметь severity=warning:\n%s", events)
	}
	if strings.Join(rt.executed, ",") != "tech,qa" {
		t.Fatalf("этап qa должен выполниться после обхода: executed=%v", rt.executed)
	}
}

func TestRun_CheckOverrideWithoutReasonFails(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Решение\n\nбез маркера\n"}
	cfg := twoStageOverrideConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	err := runTwoStageOverridePipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("первый запуск должен заблокироваться: %v", err)
	}
	decision := findOverrideDecision(t, dir)
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(decision.RunID, decision.ID, approval.Decision{
		ActorID: "local-user", ActorRole: "architect", Action: "override",
		Comment: "", SubjectHash: decision.SubjectHash, DecidedAt: time.Now().UTC(),
	}); err == nil || !strings.Contains(err.Error(), "причину") {
		t.Fatalf("обход без причины должен быть отклонён на записи решения: %v", err)
	}
}

func TestRun_CheckOverrideHumanStage(t *testing.T) {
	dir := env(t)
	store, runID, pending := startHumanStageCheckRun(t, dir)
	submitHumanStage(t, store, pending, "## Спека\n\nбез маркера\n")

	factory := &humanCrashEvidenceFactory{point: "after-human-attempt-finished"}
	crashing := newHumanStageCheckPipeline(WithApprovalStore(store), WithEvidenceStoreFactory(factory))
	expectHumanCrash(t, func() { _, _ = resumeHumanCrashRun(t, crashing, dir, runID) })
	if !factory.crashed {
		t.Fatal("crash was not injected after the durable human attempt_finished")
	}

	result, err := resumeHumanCrashRun(t, newHumanStageCheckPipeline(WithApprovalStore(store)), dir, runID)
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("a failed check must block after crash-before-check resume: result=%+v err=%v", result, err)
	}
	check, err := store.Load(runID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if check.Kind != approval.KindCheckDecision {
		t.Fatalf("expected check_decision, got %+v", check)
	}
	if _, err := store.Decide(runID, check.ID, approval.Decision{
		ActorID: "alice", ActorRole: "product_owner", Action: "override",
		Comment: "владелец принял риск", SubjectHash: check.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := resumeHumanCrashRun(t, newHumanStageCheckPipeline(WithApprovalStore(store)), dir, runID); err != nil {
		t.Fatalf("resume after override must complete: %v", err)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_overridden") {
		t.Fatalf("ожидали событие check_overridden:\n%s", events)
	}
}

func TestRun_CheckOverrideCrashRecovery(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Решение\n\nбез маркера\n"}
	rt.content["qa"] = map[string]string{"result": "# QA\n\nок\n"}
	cfg := twoStageOverrideConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	err := runTwoStageOverridePipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("первый запуск должен заблокироваться: %v", err)
	}
	decision := findOverrideDecision(t, dir)
	decideOverride(t, dir, decision, "риск принят")

	runID := filepath.Base(onlyRunDir(t, dir))
	outcome, err := resumeTwoStageOverridePipeline(t, dir, cfg, rt, runID)
	if err != nil {
		t.Fatalf("resume после обхода должен завершиться: %v", err)
	}
	if outcome != workflow.RunCompleted {
		t.Fatalf("ожидали RunCompleted, got %q", outcome)
	}

	// Повторный resume не должен быть возможен (run terminal).
	store, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != lifecycle.PhaseTerminal {
		t.Fatalf("run должен быть terminal: %+v", state)
	}
}

func TestRun_CheckOverrideSubsequentStageSeesOverriddenResult(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Решение\n\nбез маркера\n"}
	rt.content["qa"] = map[string]string{"result": "# QA\n\nок\n"}
	cfg := twoStageOverrideConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	err := runTwoStageOverridePipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("первый запуск должен заблокироваться: %v", err)
	}
	decision := findOverrideDecision(t, dir)
	decideOverride(t, dir, decision, "риск принят")

	runID := filepath.Base(onlyRunDir(t, dir))
	_, err = resumeTwoStageOverridePipeline(t, dir, cfg, rt, runID)
	if err != nil {
		t.Fatalf("resume после обхода должен завершиться: %v", err)
	}

	// Проверяем, что результат попытки tech помечен как overridden.
	// Это видно через событие check_overridden и отсутствие повторной проверки.
	events := readEvents(t, dir)
	if countEvent(events, "check_overridden") != 1 {
		t.Fatalf("ровно одно событие check_overridden ожидается:\n%s", events)
	}
	if countEvent(events, "check_failed") != 1 {
		t.Fatalf("событие check_failed должно остаться в истории:\n%s", events)
	}
	// Повторный запуск этапа tech не должен произойти (обход = без перезапуска).
	if strings.Count(events, `"stage":"tech"`) < 2 {
		t.Fatalf("этап tech должен иметь attempt_started и attempt_finished:\n%s", events)
	}
	// Убеждаемся, что отчёт проверки существует (от первоначального прогона).
	if _, err := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", ".stage-check", "tech.md")); err != nil {
		t.Fatalf("отчёт проверки должен сохраниться: %v", err)
	}
}

// deliveryOverrideReviewContent — детерминированный результат этапа review:
// вердикт положительный, но обязательной секции нет, поэтому hard-проверка
// этапа падает и обходится решением человека.
const deliveryOverrideReviewContent = "**Verdict:** APPROVED\n"
