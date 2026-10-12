package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

// stage_check_agents_test.go — B-43: реальные def-файлы встроенных
// агентов-проверяющих (spec-checker, design-checker, qa-checker) прогоняются
// через mock-runtime, чтобы связка def → collectInputs → отчёт → вердикт
// PASSED/FAILED была проверена на настоящих артефактах, а не на заглушках.

// builtinCheckerRegistry — тестовый stage-агент tech поверх встроенных
// агентов-проверяющих из agents/.
func builtinCheckerRegistry() *agent.Registry {
	return agent.NewLayered(
		agent.Layer{Name: "test", FS: fstest.MapFS{
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
		}},
		agent.Layer{Name: "builtin", FS: os.DirFS("../../agents")},
	)
}

// prepareCheckerInputs создаёт входы, которые каждый встроенный
// агент-проверяющий обязан найти на диске до запуска (task.md уже создаёт
// env(t)).
func prepareCheckerInputs(t *testing.T, dir, checker string) {
	t.Helper()
	artifacts := filepath.Join(dir, ".ai-team", "artifacts", "feat")
	switch checker {
	case "design-checker":
		if err := os.MkdirAll(filepath.Join(artifacts, "specs"), 0755); err != nil {
			t.Fatal(err)
		}
	case "qa-checker":
		if err := os.MkdirAll(filepath.Join(artifacts, "specs"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(artifacts, "design.md"), []byte("# Решение\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// runBuiltinCheckerPipeline запускает одноэтапный пайплайн, где этап tech
// проверяется встроенным агентом-проверяющим из agents/.
func runBuiltinCheckerPipeline(t *testing.T, dir string, checker string, rt *scriptedRuntime) error {
	t.Helper()
	registry := builtinCheckerRegistry()
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "builtin-checker-test",
		Title:         "Builtin checker test",
		PipelineAgents: []config.AgentConfig{
			{Name: "tech"},
			{Name: checker},
		},
		Stages: []config.TemplateStage{
			{ID: "tech", Title: "Техническое решение", Function: "architect", Result: "md",
				Executor: "agent", Agent: "tech", Confirm: "auto",
				Check: &config.TemplateCheck{Kind: "agent", Agent: checker}},
		},
		CLI: "opencode",
	}
	if err := cfg.Validate(registry); err != nil {
		t.Fatalf("конфиг с агентом-проверяющим %s должен валидироваться: %v", checker, err)
	}
	p := New(cfg, registry,
		WithRuntimeFactory(rt.factory),
		WithNotifier(&captureNotifier{}),
		WithPrompter(&scriptedPrompter{}),
	)
	return p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true,
	})
}

// builtinCheckerReport собирает md-отчёт проверяющего так, как его обязывает
// писать prompt.md: ровно одна итоговая строка с маркером вердикта.
func builtinCheckerReport(verdict string) string {
	return "# Заключение\n\nПроверка выполнена по чек-листу.\n\n**Verdict:** " + verdict + "\n"
}

func TestRun_BuiltinCheckerAgentsPass(t *testing.T) {
	checkers := map[string]string{
		"spec-checker":   "spec-check",
		"design-checker": "design-check",
		"qa-checker":     "qa-check",
	}
	for checker, output := range checkers {
		t.Run(checker, func(t *testing.T) {
			dir := env(t)
			prepareCheckerInputs(t, dir, checker)
			rt := newScripted()
			rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
			rt.content[checker] = map[string]string{output: builtinCheckerReport("PASSED")}
			var checkerInputs []runtime.Artifact
			rt.onExec = func(name string, inputs []runtime.Artifact) {
				if name == checker {
					checkerInputs = inputs
				}
			}
			if err := runBuiltinCheckerPipeline(t, dir, checker, rt); err != nil {
				t.Fatalf("агент-проверяющий PASSED должен пропустить этап дальше: %v", err)
			}
			if got := strings.Join(rt.executed, ","); got != "tech,"+checker {
				t.Fatalf("проверяющий не запущен: executed=%v", rt.executed)
			}
			// Входы проверяющего: декларированные def-ом плюс выходы
			// проверяемого этапа (loopback extraInputs).
			names := make(map[string]bool, len(checkerInputs))
			for _, input := range checkerInputs {
				names[input.Name] = true
			}
			if !names["result"] {
				t.Fatalf("выходы проверяемого этапа должны поступить проверяющему как loopback-входы: %+v", checkerInputs)
			}
			events := readEvents(t, dir)
			if !strings.Contains(events, "check_passed") {
				t.Fatalf("ожидали событие check_passed:\n%s", events)
			}
			rendered := checkReportFile(t, dir)
			if !strings.Contains(rendered, "- Итог: passed") || !strings.Contains(rendered, "- Вердикт: PASSED") {
				t.Fatalf("отчёт проверки должен фиксировать passed/PASSED: %s", rendered)
			}
		})
	}
}

func TestRun_BuiltinCheckerAgentsFail(t *testing.T) {
	checkers := map[string]string{
		"spec-checker":   "spec-check",
		"design-checker": "design-check",
		"qa-checker":     "qa-check",
	}
	for checker := range checkers {
		t.Run(checker, func(t *testing.T) {
			dir := env(t)
			prepareCheckerInputs(t, dir, checker)
			rt := newScripted()
			rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
			rt.content[checker] = map[string]string{
				checkers[checker]: builtinCheckerReport("FAILED") + "\nНайденные проблемы обязательны к исправлению.\n",
			}
			err := runBuiltinCheckerPipeline(t, dir, checker, rt)
			if err == nil || !errors.Is(err, ErrUserStopped) {
				t.Fatalf("агент-проверяющий FAILED должен заблокировать переход: %v", err)
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
			if !strings.Contains(rendered, "- Итог: failed") || !strings.Contains(rendered, "- Вердикт: FAILED") {
				t.Fatalf("отчёт проверки должен фиксировать failed/FAILED: %s", rendered)
			}
		})
	}
}

// Отчёт без контрольной строки — check_error, а не молчаливый проход: у
// встроенных проверяющих маркер задаёт prompt.md, но парсеру он обязан
// подчиниться в любом случае.
func TestRun_BuiltinCheckerAgentWithoutVerdictRaisesCheckError(t *testing.T) {
	dir := env(t)
	prepareCheckerInputs(t, dir, "spec-checker")
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
	rt.content["spec-checker"] = map[string]string{"spec-check": "# Заключение\n\nОтчёт без контрольной строки вердикта.\n"}
	err := runBuiltinCheckerPipeline(t, dir, "spec-checker", rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("отчёт без маркера должен поднять check_error: %v", err)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_error") {
		t.Fatalf("ожидали событие check_error:\n%s", events)
	}
}
