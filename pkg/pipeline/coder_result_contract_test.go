package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

// COD-02: контракт результата coder-а — diff, отчёт о unit-тестах и
// controller-owned PR-ссылка на этапе implementation.
const coderContractDiff = "diff --git a/coder.go b/coder.go\n--- a/coder.go\n+++ b/coder.go\n@@ -1 +1 @@\n-old\n+new\n"

func coderContractRegistry() *agent.Registry {
	return agent.NewFS(fstest.MapFS{
		"coder/def.yaml": def(`name: coder
runtime: agentcli
prompt_file: prompt.md
mutation: source
allowed_paths: ['**']
require_diff: true
inputs:
  task: tasks/{feature}/task.md
outputs:
  diff: '{feature}/coder.diff'
  unit-test-report: '{feature}/unit-test-report.md'
`),
		"coder/prompt.md": def("test"),
		"reviewer/def.yaml": def(`name: reviewer
runtime: agentcli
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED, REJECTED]
inputs:
  task: tasks/{feature}/task.md
outputs:
  review: '{feature}/review.md'
`),
		"reviewer/prompt.md": def("test"),
		"deployer/def.yaml": def(`name: deployer
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs: {}
`),
		"deployer/prompt.md": def("test"),
	})
}

func coderContractEnv(t *testing.T) string {
	t.Helper()
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "coder.go"), []byte("package coder\nconst Revision = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", args, commandErr, output)
		}
	}
	return dir
}

func coderContractRuntime() *scriptedRuntime {
	rt := newScripted()
	rt.content["coder"] = map[string]string{
		"diff":             coderContractDiff,
		"unit-test-report": "**Result:** PASS\n",
	}
	rt.content["reviewer"] = map[string]string{"review": "ok\n\n**Verdict:** APPROVED\n"}
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name != "coder" {
			return
		}
		_ = os.WriteFile(filepath.Join(rt.targetDir, "coder.go"),
			[]byte(fmt.Sprintf("package coder\nconst Revision = %d\n", rt.calls["coder"])), 0o644)
	}
	return rt
}

func runCoderContract(t *testing.T, dir string, cfg *config.Config, rt *scriptedRuntime, pr Prompter) (error, *captureNotifier) {
	t.Helper()
	n := &captureNotifier{}
	p := New(cfg, coderContractRegistry(),
		WithNotifier(n), WithRuntimeFactory(rt.factory), WithPrompter(pr))
	err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true,
	})
	return err, n
}

func coderContractConfig() *config.Config {
	return cfgFor(
		config.AgentConfig{Name: "coder"},
		config.AgentConfig{Name: "reviewer"},
		config.AgentConfig{Name: "deployer"},
	)
}

func TestCoderResultContract_Positive(t *testing.T) {
	dir := coderContractEnv(t)
	rt := coderContractRuntime()
	err, n := runCoderContract(t, dir, coderContractConfig(), rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("ожидался успех по контракту результата: %v", err)
	}
	if got := strings.Join(rt.executed, ","); got != "coder,reviewer,deployer" {
		t.Fatalf("порядок выполнения: %s", got)
	}
	for _, name := range []string{"coder.diff", "unit-test-report.md", "review.md"} {
		if _, statErr := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", name)); statErr != nil {
			t.Errorf("артефакт %s не создан: %v", name, statErr)
		}
	}
	if len(n.calls) != 3 || len(n.calls[0].Outputs) != 2 {
		t.Fatalf("coder должен зафиксировать 2 выхода результата, calls=%d outputs=%v", len(n.calls), n.calls[0].Outputs)
	}
}

func TestCoderResultContract_FailsWithoutExpectedResult(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(*scriptedRuntime)
		want           string
		wantValidation bool
	}{
		{
			name: "дифф есть, отчёта о тестах нет",
			setup: func(rt *scriptedRuntime) {
				rt.content["coder"] = map[string]string{
					"diff":             coderContractDiff,
					"unit-test-report": "отчёт без итоговой строки",
				}
			},
			want:           "unit-test-report",
			wantValidation: true,
		},
		{
			name:  "выходы результата не созданы",
			setup: func(rt *scriptedRuntime) { rt.skipWrite["coder"] = true },
			want:  "не создан",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := coderContractEnv(t)
			rt := coderContractRuntime()
			tc.setup(rt)
			err, n := runCoderContract(t, dir, coderContractConfig(), rt, &scriptedPrompter{})
			if err == nil {
				t.Fatal("этап без результата по контракту должен провалиться")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ошибка %q не содержит %q", err, tc.want)
			}
			if got := strings.Join(rt.executed, ","); got != "coder" {
				t.Fatalf("после провала coder downstream не должен запускаться, выполнено: %s", got)
			}
			if len(n.calls) != 1 {
				t.Fatalf("ожидалась одна попытка coder, получено %d", len(n.calls))
			}
			if n.calls[0].ValidationFailed != tc.wantValidation {
				t.Fatalf("ValidationFailed=%v, want %v", n.calls[0].ValidationFailed, tc.wantValidation)
			}
		})
	}
}

func TestCoderResultContract_LoopbackWithReviewInput(t *testing.T) {
	dir := coderContractEnv(t)
	rt := coderContractRuntime()
	rt.contentFn["reviewer"] = func(call int) map[string]string {
		if call == 1 {
			return map[string]string{"review": "исправь\n\n**Verdict:** CHANGES_REQUESTED\n"}
		}
		return map[string]string{"review": "теперь ок\n\n**Verdict:** APPROVED\n"}
	}
	var coderInputs [][]string
	baseOnExec := rt.onExec
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		baseOnExec(name, inputs)
		if name != "coder" {
			return
		}
		var names []string
		for _, input := range inputs {
			names = append(names, input.Name)
		}
		coderInputs = append(coderInputs, names)
	}
	pr := &scriptedPrompter{interactive: true, answers: []string{"y", "review: исправить поведение"}}

	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.MaxVisits["coder"] = 3
		wf.MaxVisits["reviewer"] = 3
		wf.Edges = append(wf.Edges, loopbackEdge("reviewer", "coder", "reviewer"))
	}, config.AgentConfig{Name: "coder"}, config.AgentConfig{Name: "reviewer"}, config.AgentConfig{Name: "deployer"})

	err, _ := runCoderContract(t, dir, cfg, rt, pr)
	if err != nil {
		t.Fatalf("loopback должен завершиться успехом: %v", err)
	}
	if rt.calls["coder"] != 2 || rt.calls["reviewer"] != 2 {
		t.Fatalf("ожидались 2 попытки coder и reviewer, получено %d/%d", rt.calls["coder"], rt.calls["reviewer"])
	}
	if len(coderInputs) != 2 || !strings.Contains(strings.Join(coderInputs[1], ","), "review") {
		t.Fatalf("на retry coder должен получить review.md во входе: %v", coderInputs)
	}
	// Loopback не снимает контракт результата: выходы произведены заново.
	for _, name := range []string{"coder.diff", "unit-test-report.md"} {
		data, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "artifacts", "feat", name))
		if readErr != nil || len(data) == 0 {
			t.Fatalf("выход %s после loopback пуст или отсутствует: %v", name, readErr)
		}
	}
}

func TestStagePRResultContractRequiresControllerLink(t *testing.T) {
	stage := config.TemplateStage{
		ID: "implementation", Executor: "agent", Agent: "coder",
		Result: "link", LinkKind: "pr", Delivery: &config.TemplateDelivery{},
		Check: &config.TemplateCheck{Kind: "hard", Rules: []string{"pr_exists"}},
	}
	rs := &runState{p: New(&config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Stages:        []config.TemplateStage{stage},
	}, coderContractRegistry())}

	const prURL = "https://example.test/o/r/pull/1"
	passed := notifier.StageResult{Status: notifier.StatusPassed, Delivery: &delivery.Result{PRURL: prURL}}
	if err := rs.validateStagePRResultContract(stage.ID, passed); err == nil {
		t.Fatal("без PR-ссылки в результате этап implementation должен провалиться")
	}
	linkPath := filepath.Join(t.TempDir(), "result.link")
	if err := os.WriteFile(linkPath, []byte(prURL+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	passed.Outputs = []runtime.Artifact{{Name: "link", Path: linkPath}}
	if err := rs.validateStagePRResultContract(stage.ID, passed); err != nil {
		t.Fatalf("контроллер опубликовал PR-ссылку: %v", err)
	}
	failed := notifier.StageResult{Status: notifier.StatusFailed}
	if err := rs.validateStagePRResultContract(stage.ID, failed); err != nil {
		t.Fatalf("проваленная попытка не должна проверяться на PR-ссылку: %v", err)
	}
}
