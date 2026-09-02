package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexAdapterRegistered(t *testing.T) {
	adapter, err := Adapter("codex")
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != "codex" {
		t.Errorf("expected codex, got %s", adapter.Name())
	}
}

func TestCodexDescribeDeclaresContractCapabilities(t *testing.T) {
	adapter, _ := Adapter("codex")
	declared := make(map[Capability]bool)
	for _, capability := range adapter.Describe().Capabilities {
		declared[capability] = true
	}
	for _, capability := range []Capability{
		CapModelSelection, CapEffortMapping, CapPromptFile, CapSessionIsolation, CapUsageReported,
	} {
		if !declared[capability] {
			t.Errorf("codex должен декларировать capability %q", capability)
		}
	}
}

func TestCodexCommandProducesHeadlessArgs(t *testing.T) {
	a := &CodexAdapter{}
	args, err := a.Command("codex", Launch{Model: "gpt-5", Effort: "high", RequireIsolation: true}, "/tmp/prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "exec --json --sandbox workspace-write --skip-git-repo-check --ephemeral -m gpt-5 -c model_reasoning_effort=high -"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestCodexCommandNoModelNoEffort(t *testing.T) {
	a := &CodexAdapter{}
	args, err := a.Command("codex", Launch{RequireIsolation: true}, "/tmp/prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "-m") || strings.Contains(strings.Join(args, " "), "effort") {
		t.Errorf("без model/effort argv не должен содержать эти флаги: %v", args)
	}
	if args[len(args)-1] != "-" {
		t.Errorf("промпт должен передаваться через stdin sentinel '-', got %v", args)
	}
}

func TestCodexCommandRejectsGuessedCli(t *testing.T) {
	a := &CodexAdapter{}
	if _, err := a.Command("/usr/bin/opencode", Launch{}, "/tmp/prompt.md"); err == nil {
		t.Fatal("cli, не являющийся codex, должен отклоняться fail-closed")
	}
}

func TestCodexCommandRejectsInvalidEffort(t *testing.T) {
	a := &CodexAdapter{}
	if _, err := a.Command("codex", Launch{Effort: "extreme"}, "/tmp/prompt.md"); err == nil {
		t.Fatal("недопустимый effort должен отклоняться")
	}
}

func TestCodexParseUsageLastTurnWins(t *testing.T) {
	a := &CodexAdapter{}
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"hi"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":50,"output_tokens":30,"reasoning_output_tokens":5}}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.completed","usage":{"input_tokens":200,"cached_input_tokens":60,"output_tokens":40,"reasoning_output_tokens":7}}`,
	}, "\n")
	usage, err := a.ParseUsage(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if usage.TokensInput != 200 || usage.TokensOutput != 40 {
		t.Errorf("usage = %+v, want input=200 output=40 (последний turn)", usage)
	}
	if usage.CachedInputTokens != 60 {
		t.Errorf("CachedInputTokens = %d, want 60 (не суммируется с TokensInput)", usage.CachedInputTokens)
	}
	if !usage.Attested {
		t.Error("codex usage должен быть attested (токены из JSONL харнесса)")
	}
	if usage.CostUSD != 0 {
		t.Errorf("codex не сообщает cost — он не должен заполняться, got %v", usage.CostUSD)
	}
}

func TestCodexParseUsageMissingTurnFails(t *testing.T) {
	a := &CodexAdapter{}
	if _, err := a.ParseUsage(strings.NewReader(`{"type":"thread.started","thread_id":"t1"}\n`)); err == nil {
		t.Fatal("отсутствие turn.completed с usage должно быть ошибкой")
	}
}

func TestCodexClassifyErrorTaxonomy(t *testing.T) {
	a := &CodexAdapter{}
	for output, want := range map[string]CodexErrorCategory{
		"error: invalid_api_key":                     CodexErrorAuth,
		"Authentication failed: 403":                 CodexErrorAuth,
		"model not found: gpt-5":                     CodexErrorModel,
		"validation error: model does not exist":     CodexErrorModel,
		"failed to parse config.toml: unknown field": CodexErrorConfig,
		"connection timed out to api.openai.com":     CodexErrorNetwork,
		"503 service unavailable":                    CodexErrorNetwork,
		"blocked by sandbox policy":                  CodexErrorSandbox,
		"permission denied: /etc/hosts":              CodexErrorSandbox,
		"something totally unexpected":               CodexErrorUnknown,
	} {
		t.Run(string(want)+":"+output, func(t *testing.T) {
			err := a.ClassifyError(output)
			runErr, ok := err.(*CodexRunError)
			if !ok {
				t.Fatalf("ожидали *CodexRunError, got %T", err)
			}
			if runErr.Category != want {
				t.Errorf("Category = %q, want %q", runErr.Category, want)
			}
		})
	}
}

func TestCodexValidateFailClosedOnUnrequestedGapIsSafe(t *testing.T) {
	a := &CodexAdapter{}
	if err := a.Validate(Launch{Model: "gpt-5", Effort: "high", RequireIsolation: true}); err != nil {
		t.Fatalf("codex декларирует все capabilities этого запроса: %v", err)
	}
	if err := ValidateLaunchWith(a, Launch{RequireIsolation: true}, []Capability{CapModelSelection}); err != nil {
		t.Fatalf("CapModelSelection декларирован: %v", err)
	}
}

func TestCodexEnvironmentIsolatesConfigAndCreds(t *testing.T) {
	a := &CodexAdapter{}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("SECRET_PARENT_TOKEN", "top-secret")
	t.Setenv("CODEX_API_KEY", "sk-visible")
	t.Setenv(HarnessEnvAllowVar, "CODEX_API_KEY")

	target := t.TempDir()
	env, cleanup, err := a.Environment(&Agent{Name: "coder"}, &Task{TargetDir: target, ArtifactRoot: filepath.Join(target, "artifacts"), Feature: "f"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	merged := strings.Join(env, "\n")
	if strings.Contains(merged, "SECRET_PARENT_TOKEN") {
		t.Fatal("секрет родительского env не должен попадать в субпроцесс без opt-in")
	}
	if !strings.Contains(merged, "CODEX_API_KEY=sk-visible") {
		t.Fatal("CODEX_API_KEY, разрешённая через AI_TEAM_HARNESS_ENV_ALLOW, должна присутствовать")
	}
	codexHome := ""
	for _, line := range env {
		if strings.HasPrefix(line, "CODEX_HOME=") {
			codexHome = strings.TrimPrefix(line, "CODEX_HOME=")
		}
	}
	if codexHome == "" {
		t.Fatal("CODEX_HOME должен быть перенаправлен во временный каталог")
	}
	if info, err := os.Stat(codexHome); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("CODEX_HOME должен существовать с 0700: err=%v perm=%v", err, info)
	}
	config, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), `approval_policy = "never"`) || !strings.Contains(string(config), `sandbox_mode = "workspace-write"`) {
		t.Errorf("config.toml должен фиксировать zero-approval + workspace-write: %s", config)
	}
}

func TestCodexEnvironmentRejectsProjectConfigSurface(t *testing.T) {
	a := &CodexAdapter{}
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".codex", "config.toml"), []byte("model = \"x\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Environment(&Agent{Name: "coder"}, &Task{TargetDir: target, ArtifactRoot: filepath.Join(target, "artifacts"), Feature: "f"}); err == nil {
		t.Fatal("project .codex/config.toml должен блокировать запуск (execution surface)")
	}
}

func TestAgentCLIRuntimeFeedsPromptViaStdin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "stdin-capture.md")
	script := "#!/bin/sh\ncat > \"$AI_TEAM_STDIN_CAPTURE\"\n"
	mock := filepath.Join(bin, "codex")
	if err := os.WriteFile(mock, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_TEAM_STDIN_CAPTURE", outPath)
	t.Setenv(HarnessEnvAllowVar, "AI_TEAM_STDIN_CAPTURE")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	runtime := &AgentCLIRuntime{}
	agent := &Agent{Name: "t", RuntimeType: "agentcli", CLI: mock, Prompt: "тестовый промпт", Mutation: "tests", AllowedPaths: []string{"**/*.go"}}
	task := &Task{Feature: "f", TaskDesc: "задача", TargetDir: target, ArtifactRoot: filepath.Join(target, "artifacts")}
	if err := runtime.Execute(t.Context(), agent, task, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "тестовый промпт") || !strings.Contains(content, "## Фича") {
		t.Errorf("prompt должен уходить в stdin codex (exec -), got: %q", content)
	}
}
