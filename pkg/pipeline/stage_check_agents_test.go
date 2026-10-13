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
	p := New(builtinCheckerConfig(t, registry, checker), registry,
		WithRuntimeFactory(rt.factory),
		WithNotifier(&captureNotifier{}),
		WithPrompter(&scriptedPrompter{}),
	)
	return p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true,
	})
}

// builtinCheckerConfig — одноэтапный конфиг: этап tech проверяется встроенным
// агентом-проверяющим checker.
func builtinCheckerConfig(t *testing.T, registry *agent.Registry, checker string) *config.Config {
	t.Helper()
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
	return cfg
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

// observedInput — вход агента так, как его увидел runtime в момент запуска:
// путь относительно artifact root и содержимое файла (для каталогов пусто).
type observedInput struct {
	path    string
	content string
}

func observeInputs(t *testing.T, artifactRoot string, inputs []runtime.Artifact) map[string]observedInput {
	t.Helper()
	observed := make(map[string]observedInput, len(inputs))
	for _, input := range inputs {
		if _, duplicate := observed[input.Name]; duplicate {
			t.Errorf("вход %q передан дважды: %+v", input.Name, inputs)
		}
		relative, err := filepath.Rel(artifactRoot, input.Path)
		if err != nil {
			t.Fatalf("вход %q вне artifact root: %v", input.Name, err)
		}
		value := observedInput{path: filepath.ToSlash(relative)}
		info, err := os.Stat(input.Path)
		if err != nil {
			t.Fatalf("вход %q недоступен в момент запуска: %v", input.Name, err)
		}
		if !info.IsDir() {
			data, err := os.ReadFile(input.Path)
			if err != nil {
				t.Fatal(err)
			}
			value.content = string(data)
		}
		observed[input.Name] = value
	}
	return observed
}

// BL-10: реальные def-файлы проверяющих в сценарии loopback. Первый прогон —
// FAILED на версии v1, затем ответственный выбирает new_version, resume
// перезапускает этап, и проверяющий видит уже v2. На каждом раунде
// проверяющий обязан получить ровно свои декларированные входы (по путям из
// def.yaml) плюс выход проверяемого этапа как loopback-вход с актуальным
// содержимым — без устаревшей версии, без дублей и без собственных
// loopback-входов этапа (brief), которые предназначены только ему.
func TestRun_BuiltinCheckerAgentsReceiveLoopbackInputsAcrossNewVersion(t *testing.T) {
	cases := []struct {
		checker  string
		output   string
		declared map[string]string
	}{
		{"spec-checker", "spec-check", map[string]string{"task": "tasks/feat/task.md"}},
		{"design-checker", "design-check", map[string]string{"specs": "feat/specs"}},
		{"qa-checker", "qa-check", map[string]string{"specs": "feat/specs", "design": "feat/design.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.checker, func(t *testing.T) {
			dir := env(t)
			// Пайплайн оперирует канонизированным путём (на macOS TempDir
			// лежит за симлинком /var → /private/var).
			canonicalDir, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			artifactRoot := filepath.Join(canonicalDir, ".ai-team", "artifacts")
			prepareCheckerInputs(t, dir, tc.checker)
			registry := builtinCheckerRegistry()
			cfg := builtinCheckerConfig(t, registry, tc.checker)

			versions := []string{"# Решение v1\n\nпервая версия\n", "# Решение v2\n\nисправленная версия\n"}
			verdicts := []string{"FAILED", "PASSED"}
			rt := newScripted()
			rt.contentFn["tech"] = func(call int) map[string]string {
				return map[string]string{"result": versions[call-1]}
			}
			rt.contentFn[tc.checker] = func(call int) map[string]string {
				return map[string]string{tc.output: builtinCheckerReport(verdicts[call-1])}
			}
			var checkerRounds []map[string]observedInput
			var stageRounds []map[string]observedInput
			rt.onExec = func(name string, inputs []runtime.Artifact) {
				switch name {
				case tc.checker:
					checkerRounds = append(checkerRounds, observeInputs(t, artifactRoot, inputs))
				case "tech":
					stageRounds = append(stageRounds, observeInputs(t, artifactRoot, inputs))
				}
			}
			newPipeline := func() *Pipeline {
				return New(cfg, registry,
					WithRuntimeFactory(rt.factory),
					WithNotifier(&captureNotifier{}),
					WithPrompter(&scriptedPrompter{}),
				)
			}

			err = newPipeline().Run(context.Background(), RunConfig{
				Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true,
			})
			if err == nil || !errors.Is(err, ErrUserStopped) {
				t.Fatalf("FAILED первой версии должен остановить прогон: %v", err)
			}
			pending := findCheckDecision(t, dir, approval.StageCheckTrigger)
			store, err := approval.NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Decide(pending.RunID, pending.ID, approval.Decision{
				ActorID: "local-user", ActorRole: "architect", Action: "new_version",
				SubjectHash: pending.SubjectHash, DecidedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatalf("new_version: %v", err)
			}
			if err := newPipeline().Run(context.Background(), RunConfig{TargetDir: dir, ResumeRunID: pending.RunID}); err != nil {
				t.Fatalf("resume новой версии должен пройти проверку: %v", err)
			}

			if len(stageRounds) != 2 || len(checkerRounds) != 2 {
				t.Fatalf("ожидали два раунда этапа и проверяющего: stage=%d checker=%d executed=%v",
					len(stageRounds), len(checkerRounds), rt.executed)
			}
			// Без этого проверка утечки ниже была бы пустой: входной этап
			// обязан получать brief loopback-входом.
			if _, ok := stageRounds[0]["business-brief"]; !ok {
				t.Fatalf("входной этап должен получить business-brief loopback-входом: %+v", stageRounds[0])
			}
			for round, inputs := range checkerRounds {
				want := len(tc.declared) + 1
				if len(inputs) != want {
					t.Errorf("раунд %d: проверяющий получил %d входов, want %d (декларированные + loopback result): %+v",
						round+1, len(inputs), want, inputs)
				}
				for name, path := range tc.declared {
					if got, ok := inputs[name]; !ok || got.path != path {
						t.Errorf("раунд %d: декларированный вход %q = %+v, want путь %q", round+1, name, got, path)
					}
				}
				result, ok := inputs["result"]
				if !ok {
					t.Fatalf("раунд %d: выход проверяемого этапа не поступил loopback-входом: %+v", round+1, inputs)
				}
				if result.path != "feat/tech.md" || result.content != versions[round] {
					t.Errorf("раунд %d: loopback result = %+v, want feat/tech.md с версией %q", round+1, result, versions[round])
				}
				// Собственные loopback-входы этапа (brief на входном этапе)
				// проверяющему не передаются.
				for name := range stageRounds[round] {
					if _, declared := tc.declared[name]; declared || name == "result" {
						continue
					}
					if _, leaked := inputs[name]; leaked {
						t.Errorf("раунд %d: loopback-вход этапа %q утёк проверяющему: %+v", round+1, name, inputs)
					}
				}
			}
			events := readEvents(t, dir)
			if countEvent(events, "check_failed") != 1 || countEvent(events, "check_passed") != 1 {
				t.Fatalf("ожидали ровно один check_failed и один check_passed:\n%s", events)
			}
		})
	}
}
