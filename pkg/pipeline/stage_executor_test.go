package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	aitruntime "github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/stageexec"
)

// stage_executor_test.go — S09: этап исполняется внешним адаптером по JSON
// контракту; контроллер проверяет выходы и mutation scope так же, как для
// внутреннего агента, и фиксирует фактического исполнителя в evidence.

func externalExecutorRegistry() *agent.Registry {
	return agent.NewLayered(agent.Layer{Name: "test", FS: fstest.MapFS{
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
	}})
}

func externalExecutorConfig(t *testing.T, registry *agent.Registry, adapter string) *config.Config {
	t.Helper()
	cfg := &config.Config{
		SchemaVersion:  config.CurrentSchemaVersion,
		Template:       "external-executor-test",
		Title:          "External executor test",
		PipelineAgents: []config.AgentConfig{{Name: "tech"}},
		Stages: []config.TemplateStage{
			{ID: "tech", Title: "Техническое решение", Function: "architect", Result: "md",
				Executor: "agent", Agent: "tech", Adapter: adapter, Confirm: "auto"},
		},
		ExecutorAdapters: map[string]stageexec.AdapterSpec{
			"demo": {Argv: []string{"./scripts/executor.sh"}, Timeout: "30s", EnvAllow: []string{"S09_TEST_ALLOWED"}},
		},
		CLI: "opencode",
	}
	if err := cfg.Validate(registry); err != nil {
		t.Fatalf("конфиг с внешним исполнителем должен валидироваться: %v", err)
	}
	return cfg
}

func writeExecutorScript(t *testing.T, dir, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell executor fixture requires a POSIX shell")
	}
	scripts := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "executor.sh"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runExternalExecutor(t *testing.T, dir string) (*captureNotifier, error) {
	t.Helper()
	registry := externalExecutorRegistry()
	capture := &captureNotifier{}
	p := New(externalExecutorConfig(t, registry, "demo"), registry,
		WithRuntimeFactory(func(string) (aitruntime.Runtime, error) {
			return nil, errors.New("internal runtime must not be created for an external executor")
		}),
		WithNotifier(capture),
		WithPrompter(&scriptedPrompter{}),
	)
	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "демо", TargetDir: dir, ApproveGates: true})
	return capture, err
}

func TestRun_ExternalExecutorProducesResultAndRecordsProvenance(t *testing.T) {
	dir := env(t)
	t.Setenv("S09_TEST_ALLOWED", "visible-value-123")
	t.Setenv("S09_TEST_SECRET", "must-not-leak-456")
	writeExecutorScript(t, dir, `cat >/dev/null
echo "allowed=$S09_TEST_ALLOWED secret=$S09_TEST_SECRET" >&2
printf '{"schema_version":1,"status":"ok","summary":"done","outputs":{"result":{"content":"# Tech\\n\\nрешение\\n"}},"metadata":{"tool_version":"demo-1"}}'
`)
	capture, err := runExternalExecutor(t, dir)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(capture.calls) != 1 || capture.calls[0].Status != "passed" {
		t.Fatalf("stage results = %+v", capture.calls)
	}
	result := capture.calls[0]
	if result.Executor != "agent" || result.ExecutorInfo == nil ||
		result.ExecutorInfo.Kind != stageexec.KindExternal || result.ExecutorInfo.Name != "demo" ||
		result.ExecutorInfo.Metadata["tool_version"] != "demo-1" || result.ExecutorInfo.LogFile == "" {
		t.Fatalf("executor info = %+v (executor %q)", result.ExecutorInfo, result.Executor)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".ai-team", "artifacts", "feat", "tech.md"))
	if err != nil || !strings.Contains(string(data), "решение") {
		t.Fatalf("output = %q, %v", data, err)
	}
	var logData []byte
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() && entry.Name() == result.ExecutorInfo.LogFile {
			logData, _ = os.ReadFile(path)
		}
		return nil
	})
	if len(logData) == 0 {
		t.Fatalf("executor log %s was not written", result.ExecutorInfo.LogFile)
	}
	if !strings.Contains(string(logData), "allowed=[redacted]") || strings.Contains(string(logData), "must-not-leak-456") {
		t.Fatalf("log must redact allowed env values and never see other env: %s", logData)
	}
}

func TestRun_ExternalExecutorFailureIsNotACompletedStage(t *testing.T) {
	cases := map[string]string{
		"nonzero exit":       "cat >/dev/null\necho boom >&2\nexit 3\n",
		"error status":       "cat >/dev/null\nprintf '{\"schema_version\":1,\"status\":\"error\",\"error\":\"no good\"}'\n",
		"invalid json":       "cat >/dev/null\necho not-json\n",
		"ok without outputs": "cat >/dev/null\nprintf '{\"schema_version\":1,\"status\":\"ok\"}'\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := env(t)
			writeExecutorScript(t, dir, body)
			capture, err := runExternalExecutor(t, dir)
			if err == nil {
				t.Fatal("a failed external executor must fail the run")
			}
			if len(capture.calls) != 1 || capture.calls[0].Status == "passed" || capture.calls[0].Err == nil {
				t.Fatalf("stage results = %+v", capture.calls)
			}
			if info := capture.calls[0].ExecutorInfo; info == nil || info.Name != "demo" {
				t.Fatalf("failed attempt must still name its executor: %+v", info)
			}
			if _, statErr := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", "tech.md")); statErr == nil {
				t.Fatal("no output may exist after a failed executor")
			}
		})
	}
}

func TestRun_ExternalExecutorMutationScopeIsEnforced(t *testing.T) {
	dir := env(t)
	writeExecutorScript(t, dir, `cat >/dev/null
echo stray > stray.txt
printf '{"schema_version":1,"status":"ok","outputs":{"result":{"content":"# Tech\\n"}}}'
`)
	capture, err := runExternalExecutor(t, dir)
	if err == nil {
		t.Fatal("a mutation outside the stage scope must fail the stage")
	}
	if len(capture.calls) != 1 || capture.calls[0].Status == "passed" {
		t.Fatalf("stage results = %+v", capture.calls)
	}
}

func TestRun_InternalAgentAttemptRecordsExecutorInfo(t *testing.T) {
	dir := env(t)
	registry := externalExecutorRegistry()
	capture := &captureNotifier{}
	rt := newScripted()
	p := New(externalExecutorConfig(t, registry, ""), registry,
		WithRuntimeFactory(rt.factory), WithNotifier(capture), WithPrompter(&scriptedPrompter{}))
	if err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "демо", TargetDir: dir, ApproveGates: true}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(capture.calls) != 1 || capture.calls[0].ExecutorInfo == nil ||
		capture.calls[0].ExecutorInfo.Kind != stageexec.KindInternalAgent {
		t.Fatalf("internal attempt must record its executor kind: %+v", capture.calls)
	}
}
