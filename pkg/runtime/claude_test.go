package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeAdapterRegistered(t *testing.T) {
	adapter, err := Adapter("claude")
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != "claude" {
		t.Errorf("expected claude, got %s", adapter.Name())
	}
}

func TestClaudeDescribeDeclaresContractCapabilities(t *testing.T) {
	adapter, _ := Adapter("claude")
	declared := make(map[Capability]bool)
	for _, capability := range adapter.Describe().Capabilities {
		declared[capability] = true
	}
	for _, capability := range []Capability{
		CapModelSelection, CapEffortMapping, CapPromptFile, CapSessionIsolation, CapUsageReported,
	} {
		if !declared[capability] {
			t.Errorf("claude должен декларировать capability %q", capability)
		}
	}
}

func TestClaudeCommandProducesHeadlessArgs(t *testing.T) {
	a := &ClaudeAdapter{}
	args, err := a.Command("claude", Launch{Model: "claude-opus-4-6", Effort: "high", RequireIsolation: true}, "/tmp/prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	if !strings.Contains(got, "-p") ||
		!strings.Contains(got, "--output-format json") ||
		!strings.Contains(got, "--permission-mode acceptEdits") ||
		!strings.Contains(got, "--disallowedTools") ||
		!strings.Contains(got, "--model claude-opus-4-6") ||
		!strings.Contains(got, "--effort high") {
		t.Errorf("argv = %q не содержит headless/permission/model/effort контракт", got)
	}
	if !strings.Contains(got, "Bash") {
		t.Errorf("deny-list инструментов должен включать Bash: %q", got)
	}
}

func TestClaudeCommandNoModelNoEffort(t *testing.T) {
	a := &ClaudeAdapter{}
	args, err := a.Command("claude", Launch{RequireIsolation: true}, "/tmp/prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "--model") || strings.Contains(strings.Join(args, " "), "--effort") {
		t.Errorf("без model/effort argv не должен содержать эти флаги: %v", args)
	}
}

func TestClaudeCommandRejectsGuessedCliAndInvalidEffort(t *testing.T) {
	a := &ClaudeAdapter{}
	if _, err := a.Command("codex", Launch{}, "/tmp/prompt.md"); err == nil {
		t.Fatal("cli, не являющийся claude, должен отклоняться fail-closed")
	}
	if _, err := a.Command("claude", Launch{Effort: "xhigh"}, "/tmp/prompt.md"); err == nil {
		t.Fatal("effort вне контракта low/medium/high должен отклоняться")
	}
}

func TestClaudeParseUsageTokensAndCost(t *testing.T) {
	a := &ClaudeAdapter{}
	sample := `{"type":"result","subtype":"success","result":"ok","is_error":false,"total_cost_usd":0.42,"usage":{"input_tokens":1000,"cache_creation_input_tokens":100,"cache_read_input_tokens":50,"output_tokens":2000},"session_id":"s1"}`
	usage, err := a.ParseUsage(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if usage.TokensInput != 1000 || usage.TokensOutput != 2000 {
		t.Errorf("usage = %+v, want input=1000 output=2000", usage)
	}
	if usage.CachedInputTokens != 150 {
		t.Errorf("CachedInputTokens = %d, want 150 (не суммируется с TokensInput)", usage.CachedInputTokens)
	}
	if usage.CostUSD != 0.42 || usage.Currency != "USD" {
		t.Errorf("cost = %v %s, want 0.42 USD", usage.CostUSD, usage.Currency)
	}
	if !usage.Attested {
		t.Error("claude usage должен быть attested (поля из JSON-результата харнесса)")
	}
}

func TestClaudeParseUsageMissingResultFails(t *testing.T) {
	a := &ClaudeAdapter{}
	if _, err := a.ParseUsage(strings.NewReader("просто текст, не JSON\n")); err == nil {
		t.Fatal("отсутствие result JSON должно быть ошибкой")
	}
}

func TestClaudeClassifyErrorTaxonomyFromSubtype(t *testing.T) {
	a := &ClaudeAdapter{}
	for subtype, want := range map[string]ClaudeErrorCategory{
		"error_max_budget_usd":                 ClaudeErrorBudget,
		"error_max_turns":                      ClaudeErrorTimeout,
		"error_during_execution_no_permission": ClaudeErrorPermission,
		"error_during_execution":               ClaudeErrorRuntime,
	} {
		t.Run(subtype, func(t *testing.T) {
			output := `{"type":"result","subtype":"` + subtype + `","is_error":true,"result":""}`
			err := a.ClassifyError(output)
			runErr, ok := err.(*ClaudeRunError)
			if !ok {
				t.Fatalf("ожидали *ClaudeRunError, got %T", err)
			}
			if runErr.Category != want {
				t.Errorf("Category = %q, want %q", runErr.Category, want)
			}
		})
	}
}

func TestClaudeClassifyErrorStringHeuristics(t *testing.T) {
	a := &ClaudeAdapter{}
	for output, want := range map[string]ClaudeErrorCategory{
		"Authentication: invalid api key": ClaudeErrorAuth,
		"error 403 from api":              ClaudeErrorAuth,
		"model not found: claude-opus-4":  ClaudeErrorModel,
		"failed to load settings.json":    ClaudeErrorConfig,
		"network error while streaming":   ClaudeErrorNetwork,
		"случилось что-то совсем иное":    ClaudeErrorUnknown,
	} {
		t.Run(string(want)+":"+output, func(t *testing.T) {
			err := a.ClassifyError(output)
			runErr, ok := err.(*ClaudeRunError)
			if !ok {
				t.Fatalf("ожидали *ClaudeRunError, got %T", err)
			}
			if runErr.Category != want {
				t.Errorf("Category = %q, want %q", runErr.Category, want)
			}
		})
	}
}

func TestClaudeEnvironmentIsolatesConfigAndCreds(t *testing.T) {
	a := &ClaudeAdapter{}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("SECRET_PARENT_TOKEN", "top-secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-visible")
	t.Setenv(HarnessEnvAllowVar, "ANTHROPIC_API_KEY")

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
	if !strings.Contains(merged, "ANTHROPIC_API_KEY=sk-ant-visible") {
		t.Fatal("ANTHROPIC_API_KEY, разрешённая через AI_TEAM_HARNESS_ENV_ALLOW, должна присутствовать")
	}
	configDir := ""
	for _, line := range env {
		if strings.HasPrefix(line, "CLAUDE_CONFIG_DIR=") {
			configDir = strings.TrimPrefix(line, "CLAUDE_CONFIG_DIR=")
		}
	}
	if configDir == "" {
		t.Fatal("CLAUDE_CONFIG_DIR должен быть перенаправлен во временный каталог")
	}
	if info, err := os.Stat(configDir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("CLAUDE_CONFIG_DIR должен существовать с 0700: err=%v perm=%v", err, info)
	}
	settings, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(settings)
	for _, needled := range []string{"permissionMode", "acceptEdits", "disallowedTools", "Read(~/.ssh/**)", "**/.env"} {
		if !strings.Contains(content, needled) {
			t.Errorf("settings.json должен содержать %q: %s", needled, content)
		}
	}
}

func TestClaudeEnvironmentRejectsProjectSettingsSurface(t *testing.T) {
	a := &ClaudeAdapter{}
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".claude", "settings.json"), []byte(`{"model":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Environment(&Agent{Name: "coder"}, &Task{TargetDir: target, ArtifactRoot: filepath.Join(target, "artifacts"), Feature: "f"}); err == nil {
		t.Fatal("project .claude/settings.json должен блокировать запуск (execution surface)")
	}
}

func TestClaudeStdinPromptWiringThroughOrchestrator(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "stdin-capture.md")
	script := "#!/bin/sh\ncat > \"$AI_TEAM_CLAUDE_STDIN_CAPTURE\"\n"
	mock := filepath.Join(bin, "claude")
	if err := os.WriteFile(mock, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_TEAM_CLAUDE_STDIN_CAPTURE", outPath)
	t.Setenv(HarnessEnvAllowVar, "AI_TEAM_CLAUDE_STDIN_CAPTURE")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	runtime := &AgentCLIRuntime{}
	agent := &Agent{Name: "t", RuntimeType: "agentcli", CLI: mock, Prompt: "тестовый промпт claude", Mutation: "tests", AllowedPaths: []string{"**/*.go"}}
	task := &Task{Feature: "f", TaskDesc: "задача", TargetDir: target, ArtifactRoot: filepath.Join(target, "artifacts")}
	if err := runtime.Execute(t.Context(), agent, task, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "тестовый промпт claude") || !strings.Contains(content, "## Служебные требования") {
		t.Errorf("prompt должен уходить в stdin claude -p, got: %q", content)
	}
}
