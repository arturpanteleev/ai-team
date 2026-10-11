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
	"github.com/arturpanteleev/ai-team/pkg/report"
)

// stage_check_test.go — интегрированные проверки результата этапа (B-40).

func stageCheckRegistry(withVerifier bool) *agent.Registry {
	files := fstest.MapFS{
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
	}
	if withVerifier {
		files["verifier/def.yaml"] = def(`name: verifier
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs:
  verification: '{feature}/verification.md'
`)
		files["verifier/prompt.md"] = def("test")
	}
	return agent.NewFS(files)
}

func stageCheckConfig(check *config.TemplateCheck) *config.Config {
	return &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "stage-check-test",
		Title:         "Stage check test",
		PipelineAgents: []config.AgentConfig{
			{Name: "tech"},
			{Name: "verifier"},
		},
		Stages: []config.TemplateStage{
			{ID: "tech", Title: "Техническое решение", Function: "architect", Result: "md", Executor: "agent", Agent: "tech", Confirm: "auto", Check: check},
		},
		CLI: "opencode",
	}
}

func currentSchemaVersionForTest() int {
	return config.CurrentSchemaVersion
}

func runStageCheckPipeline(t *testing.T, dir string, cfg *config.Config, rt *scriptedRuntime) error {
	t.Helper()
	withVerifier := cfg.Stages[0].Check != nil && cfg.Stages[0].Check.Agent != ""
	p := New(cfg, stageCheckRegistry(withVerifier),
		WithRuntimeFactory(rt.factory),
		WithNotifier(&captureNotifier{}),
		WithPrompter(&scriptedPrompter{}),
	)
	return p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true,
	})
}

func readEvents(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(onlyRunDir(t, dir), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func checkReportFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, ".ai-team", "artifacts", "feat", ".stage-check", "tech.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("отчёт проверки не сохранён: %v", err)
	}
	return string(data)
}

func findCheckDecision(t *testing.T, dir string, kind string) *approval.PendingApproval {
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
		if values[index].Kind == approval.KindCheckDecision && (kind == "" || values[index].Trigger == kind) {
			return &values[index]
		}
	}
	t.Fatalf("check_decision approval не найден: %+v", values)
	return nil
}

func TestRun_HardCheckPassAllowsTransition(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Решение\n\nвсё ок\n\n**Verdict:** APPROVED\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	if err := runStageCheckPipeline(t, dir, cfg, rt); err != nil {
		t.Fatalf("проверка должна пройти и не блокировать переход: %v", err)
	}
	events := readEvents(t, dir)
	for _, want := range []string{"check_started", "check_passed"} {
		if !strings.Contains(events, want) {
			t.Fatalf("ожидали событие %s:\n%s", want, events)
		}
	}
	rendered := checkReportFile(t, dir)
	if !strings.Contains(rendered, "- Итог: passed") {
		t.Fatalf("отчёт должен фиксировать passed: %s", rendered)
	}
	if got := report.ReadStageCheckReport(filepath.Join(dir, ".ai-team", "artifacts"), "feat", "tech"); got == "" {
		t.Fatal("отчёт проверки должен читаться через report.ReadStageCheckReport")
	}
}

func TestRun_HardCheckFailureBlocksWithCheckDecision(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Решение\n\nбез маркера вердикта\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	err := runStageCheckPipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("ожидали блокировку (ErrUserStopped), got: %v", err)
	}
	value := findCheckDecision(t, dir, approval.StageCheckTrigger)
	if value.Status != approval.StatusPending {
		t.Fatalf("check_decision должен оставаться pending, got %q", value.Status)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_failed") {
		t.Fatalf("ожидали событие check_failed:\n%s", events)
	}
	rendered := checkReportFile(t, dir)
	if !strings.Contains(rendered, "- Итог: failed") {
		t.Fatalf("отчёт должен фиксировать failed: %s", rendered)
	}
	store, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(filepath.Base(onlyRunDir(t, dir)))
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" {
		t.Fatalf("run должен ждать решения check_decision: %+v", state)
	}
}

func TestRun_HardCheckRequiredSections(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\n## Критерии приёмки\n\nвсё ок\n\n**Verdict:** APPROVED\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	cfg.Stages[0].RequiredSections = []string{"Критерии приёмки"}
	if err := runStageCheckPipeline(t, dir, cfg, rt); err != nil {
		t.Fatalf("needed sections present: %v", err)
	}
	dir2 := env(t)
	rt2 := newScripted()
	rt2.content["tech"] = map[string]string{"result": "# Решение\n\nбез раздела\n"}
	err := runStageCheckPipeline(t, dir2, cfg, rt2)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("missing required section should block: %v", err)
	}
}

func TestRun_AgentCheckPassAllowsTransition(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
	rt.content["verifier"] = map[string]string{"verification": "# Отчёт проверки\n\n**Verdict:** PASSED\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "agent", Agent: "verifier"})
	if err := runStageCheckPipeline(t, dir, cfg, rt); err != nil {
		t.Fatalf("агент-проверяющий PASSED должен пропустить этап дальше: %v", err)
	}
	if strings.Join(rt.executed, ",") != "tech,verifier" {
		t.Fatalf("проверяющий не запущен: executed=%v", rt.executed)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_passed") {
		t.Fatalf("ожидали check_passed:\n%s", events)
	}
}

func TestRun_AgentCheckFailureBlocksWithCheckDecision(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
	rt.content["verifier"] = map[string]string{"verification": "# Отчёт проверки\n\n**Verdict:** FAILED\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "agent", Agent: "verifier"})
	err := runStageCheckPipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("агент-проверяющий FAILED должен заблокировать переход: %v", err)
	}
	value := findCheckDecision(t, dir, approval.StageCheckTrigger)
	if value.Status != approval.StatusPending {
		t.Fatalf("check_decision должен оставаться pending, got %q", value.Status)
	}
}

func TestRun_CheckerAgentErrorRaisesCheckError(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
	rt.content["verifier"] = map[string]string{"verification": "# Отчёт\n\nбез вердикта\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "agent", Agent: "verifier"})
	err := runStageCheckPipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("сбой агента-проверяющего должен поднять check_decision: %v", err)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_error") {
		t.Fatalf("ожидали событие check_error:\n%s", events)
	}
	value := findCheckDecision(t, dir, approval.StageCheckTrigger)
	if value.Payload == nil {
		t.Fatal("check_decision должен содержать payload")
	}
}

func TestRun_CheckDecisionNewVersionResumesStage(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nбез маркера\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	err := runStageCheckPipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("первый запуск должен заблокироваться: %v", err)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	values, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	pending := values[0]
	if pending.Kind != approval.KindCheckDecision || len(pending.Decisions) != 0 {
		t.Fatalf("ожидали pending check_decision, got %+v", pending)
	}
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "local-user", ActorRole: "architect", Action: "new_version",
		SubjectHash: pending.SubjectHash, DecidedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("resolve new_version: %v", err)
	}

	// Новая версия результата — маркер на месте; resume перезапускает этап.
	rt.content["tech"] = map[string]string{"result": "# Решение\n\n**Verdict:** APPROVED\n"}
	p := New(cfg, stageCheckRegistry(false),
		WithRuntimeFactory(rt.factory),
		WithNotifier(&captureNotifier{}),
		WithPrompter(&scriptedPrompter{}),
	)
	if err := p.Run(context.Background(), RunConfig{Feature: "", TargetDir: dir, ResumeRunID: runID}); err != nil {
		t.Fatalf("resume новой версии должен пройти проверку: %v", err)
	}
	runDir := onlyRunDir(t, dir)
	events, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), "check_passed") {
		t.Fatalf("повторная проверка должна пройти:\n%s", events)
	}
}
