package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// CodexAdapter — реализация RuntimeAdapter для OpenAI Codex CLI.
// Вся codex-специфика (argv `codex exec --json`, sandbox, CODEX_HOME,
// JSONL-события, usage-статистика) локализована здесь.
//
// Промпт передаётся через stdin (`codex exec -`): оркестратор направляет
// файл промпта в cmd.Stdin — большие промпты не упираются в ARG_MAX.
//
// Политика изоляции: sandbox=workspace-write (записи только внутри workspace),
// headless without approvals, CODEX_HOME перенаправлен во временный каталог
// (проектный/user config не загружаются; из MCP-серверов загружаются только
// явно выбранные на этапе), env заменён allow-листом. Для stage-specific
// read-deny используется Codex filesystem permission profile (CLI >= 0.138.0),
// поскольку legacy workspace-write sandbox разрешает чтение всего workspace.
type CodexAdapter struct{}

func (a *CodexAdapter) Name() string { return "codex" }

func (a *CodexAdapter) Describe() Descriptor {
	return Descriptor{
		Name:   a.Name(),
		Binary: "codex",
		Capabilities: []Capability{
			CapModelSelection,
			CapEffortMapping,
			CapPromptFile,
			CapSessionIsolation,
			CapUsageReported,
		},
		PromptViaStdin: true,
	}
}

// Validate — fail-closed: запрос capability, отсутствующей у codex, блокирует
// запуск (см. ValidateLaunch).
func (a *CodexAdapter) Validate(launch Launch) error {
	return ValidateLaunch(a, launch)
}

// Command — argv запуска `codex exec` в headless CI-режиме: JSONL-события на
// stdout, sandbox-confined workspace writes, чтение промпта из stdin ("-").
func (a *CodexAdapter) Command(cli string, launch Launch, promptFile string) ([]string, error) {
	if filepath.Base(cli) != a.Name() {
		return nil, fmt.Errorf("CLI %q не поддерживается адаптером codex: требуется явный adapter вместо guessed arguments", cli)
	}
	args := []string{"exec", "--json"}
	if len(launch.DeniedReadPaths) == 0 {
		args = append(args, "--sandbox", "workspace-write")
	}
	// The temp CODEX_HOME selects the exact-path profile for protected stages.
	// Do not pass the legacy --sandbox flag in that case: it takes precedence.
	args = append(args, "--skip-git-repo-check", "--ephemeral")
	if launch.Model != "" && launch.Model != "auto" {
		args = append(args, "-m", launch.Model)
	}
	if launch.Effort != "" {
		if !validCodexEffort(launch.Effort) {
			return nil, fmt.Errorf("codex: недопустимый effort %q (low|medium|high)", launch.Effort)
		}
		args = append(args, "-c", "model_reasoning_effort="+launch.Effort)
	}
	// "-" — явный sentinel: полный промпт читается из stdin.
	args = append(args, "-")
	return args, nil
}

func validCodexEffort(effort string) bool {
	switch effort {
	case "low", "medium", "high":
		return true
	}
	return false
}

// Environment — изоляция codex-сессии: запрет проектного execution surface,
// CODEX_HOME во временном каталоге (0700) без пользовательских config/MCP,
// allow-list env субпроцесса.
func (a *CodexAdapter) Environment(agent *Agent, task *Task, inputs ...Artifact) ([]string, func(), error) {
	for _, relative := range []string{filepath.Join(".codex", "config.toml")} {
		if info, err := os.Lstat(filepath.Join(task.TargetDir, relative)); err == nil && (info.IsDir() || info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return nil, func() {}, fmt.Errorf("project execution surface %s запрещена; project config входит в trusted controller, а не в agent runtime", relative)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, func() {}, fmt.Errorf("проверка %s: %w", relative, err)
		}
	}

	codexHome, err := os.MkdirTemp("", "ai-team-codex-config-*")
	if err != nil {
		return nil, func() {}, err
	}
	if err := os.Chmod(codexHome, 0700); err != nil {
		_ = os.RemoveAll(codexHome)
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(codexHome) }
	var mcpHome string
	if agent != nil && len(agent.MCPServers) > 0 {
		if err := validateRuntimeMCPServers(agent.MCPServers); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		mcpHome, err = os.MkdirTemp("", "ai-team-codex-mcp-home-*")
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		if err := os.Chmod(mcpHome, 0700); err != nil {
			_ = os.RemoveAll(mcpHome)
			cleanup()
			return nil, func() {}, err
		}
		cleanup = func() {
			_ = os.RemoveAll(mcpHome)
			_ = os.RemoveAll(codexHome)
		}
	}

	deniedReadPaths, err := exactDeniedReadPaths(task)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if len(deniedReadPaths) > 0 {
		cli := agent.CLI
		if cli == "" {
			cli = "codex"
		}
		if err := requireCodexPermissionProfiles(cli); err != nil {
			cleanup()
			return nil, func() {}, err
		}
	}
	var mcpServers []MCPServerConfig
	if agent != nil {
		mcpServers = agent.MCPServers
	}
	configContent, err := codexSessionConfig(deniedReadPaths, mcpServers, mcpHome)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	configPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(configPath, configContent, 0600); err != nil {
		cleanup()
		return nil, func() {}, err
	}

	// The user's login lives outside CODEX_HOME. When no explicitly allowed
	// API-key environment variable will reach Codex, copy only auth.json into
	// the private temporary home so subscription credentials remain usable.
	if !hasAllowedNonEmptyEnvironment("CODEX_API_KEY", "OPENAI_API_KEY") {
		auth, err := loadCodexAuthFile()
		if err != nil && !os.IsNotExist(err) {
			cleanup()
			return nil, func() {}, fmt.Errorf("codex: не удалось безопасно прочитать пользовательскую авторизацию")
		}
		if err == nil {
			authPath := filepath.Join(codexHome, "auth.json")
			if err := safeio.WriteRegularFileNoFollow(authPath, auth.contents, 0600); err != nil {
				cleanup()
				return nil, func() {}, fmt.Errorf("codex: не удалось скопировать авторизацию во временный CODEX_HOME")
			}
			if err := os.Chmod(authPath, 0600); err != nil {
				cleanup()
				return nil, func() {}, fmt.Errorf("codex: не удалось защитить временный auth.json")
			}
		}
	}

	env := withAllowedEnvironmentKeys(os.Environ(), allowedNonClaudeEnvironmentKeys())
	env = append(env, "CODEX_HOME="+codexHome)
	sort.Strings(env)
	return env, cleanup, nil
}

const codexReadDenyProfileMinimum = "0.138.0"

var codexVersionPattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+][A-Za-z0-9.-]+)?`)

func requireCodexPermissionProfiles(cli string) error {
	output, err := exec.Command(cli, "--version").Output()
	if err != nil {
		return fmt.Errorf("codex per-stage read isolation requires a verifiable CLI version with permission profiles (>= %s): %w", codexReadDenyProfileMinimum, err)
	}
	match := codexVersionPattern.FindStringSubmatch(strings.TrimSpace(string(output)))
	if len(match) != 4 {
		return fmt.Errorf("codex per-stage read isolation requires a verifiable CLI version with permission profiles (>= %s)", codexReadDenyProfileMinimum)
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	if major == 0 && minor < 138 {
		return fmt.Errorf("codex %s does not support enforced per-stage read-deny permission profiles (requires >= %s)", strings.Join(match[1:], "."), codexReadDenyProfileMinimum)
	}
	return nil
}

func validateRuntimeMCPServers(servers []MCPServerConfig) error {
	if len(servers) > MaxMCPServersPerStage {
		return fmt.Errorf("codex: допускается не более %d MCP серверов на этап", MaxMCPServersPerStage)
	}
	seen := make(map[string]bool, len(servers))
	allowedEnv := allowedNonClaudeEnvironmentKeys()
	for _, server := range servers {
		if err := server.Validate(server.Name); err != nil {
			return err
		}
		if seen[server.Name] {
			return fmt.Errorf("codex: повторный MCP сервер %q", server.Name)
		}
		seen[server.Name] = true
		if _, err := exec.LookPath(server.Command); err != nil {
			return fmt.Errorf("codex: MCP сервер %q command недоступна", server.Name)
		}
		for _, name := range server.EnvVars {
			if !allowedEnv[name] {
				return fmt.Errorf("codex: MCP сервер %q env_vars %q должен быть явно добавлен в %s", server.Name, name, HarnessEnvAllowVar)
			}
			if value, present := os.LookupEnv(name); !present || value == "" {
				return fmt.Errorf("codex: MCP сервер %q требует незаданную переменную окружения %q", server.Name, name)
			}
		}
	}
	return nil
}

func codexSessionConfig(deniedReadPaths []string, mcpServers []MCPServerConfig, mcpHome string) ([]byte, error) {
	paths, err := validateExactDeniedReadPaths(deniedReadPaths)
	if err != nil {
		return nil, err
	}
	var config strings.Builder
	config.WriteString("approval_policy = \"never\"\n")
	if len(paths) == 0 {
		config.WriteString("sandbox_mode = \"workspace-write\"\n")
	} else {
		config.WriteString("default_permissions = \"ai_team_workspace\"\n\n")
		config.WriteString("[permissions.ai_team_workspace]\nextends = \":workspace\"\n\n")
		config.WriteString("[permissions.ai_team_workspace.filesystem]\n")
		for _, path := range paths {
			config.WriteString(strconv.Quote(path))
			config.WriteString(" = \"deny\"\n")
		}
		config.WriteString("\n[permissions.ai_team_workspace.network]\nenabled = false\n")
	}
	if len(mcpServers) == 0 {
		return []byte(config.String()), nil
	}
	if mcpHome == "" {
		return nil, fmt.Errorf("codex: internal error: MCP home is required when MCP servers are enabled")
	}
	if len(mcpServers) > MaxMCPServersPerStage {
		return nil, fmt.Errorf("codex: допускается не более %d MCP серверов на этап", MaxMCPServersPerStage)
	}
	servers := append([]MCPServerConfig(nil), mcpServers...)
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	seen := map[string]bool{}
	for _, server := range servers {
		if err := server.Validate(server.Name); err != nil {
			return nil, err
		}
		if seen[server.Name] {
			return nil, fmt.Errorf("codex: повторный MCP сервер %q", server.Name)
		}
		seen[server.Name] = true
		config.WriteString("\n[mcp_servers.")
		config.WriteString(server.Name)
		config.WriteString("]\ncommand = ")
		config.WriteString(strconv.Quote(server.Command))
		config.WriteString("\nenabled = true\nstartup_timeout_sec = ")
		config.WriteString(strconv.Itoa(effectiveMCPStartupTimeout(server)))
		config.WriteString("\ntool_timeout_sec = ")
		config.WriteString(strconv.Itoa(effectiveMCPToolTimeout(server)))
		if len(server.Args) > 0 {
			config.WriteString("\nargs = [")
			for index, arg := range server.Args {
				if index > 0 {
					config.WriteString(", ")
				}
				config.WriteString(strconv.Quote(arg))
			}
			config.WriteString("]")
		}
		if len(server.EnvVars) > 0 {
			envVars := append([]string(nil), server.EnvVars...)
			sort.Strings(envVars)
			config.WriteString("\nenv_vars = [")
			for index, name := range envVars {
				if index > 0 {
					config.WriteString(", ")
				}
				config.WriteString(strconv.Quote(name))
			}
			config.WriteString("]")
		}
		config.WriteString("\n\n[mcp_servers.")
		config.WriteString(server.Name)
		config.WriteString(".env]\nHOME = ")
		config.WriteString(strconv.Quote(mcpHome))
		envNames := make([]string, 0, len(server.Env))
		for name := range server.Env {
			envNames = append(envNames, name)
		}
		sort.Strings(envNames)
		for _, name := range envNames {
			config.WriteString("\n")
			config.WriteString(name)
			config.WriteString(" = ")
			config.WriteString(strconv.Quote(server.Env[name]))
		}
		config.WriteString("\n")
	}
	return []byte(config.String()), nil
}

// ParseUsage — типизированный разбор usage из JSONL-событий `codex exec
// --json`. Берётся последний turn.completed (финальный); токены аттестуются
// как реальный расход, cost от харнесса не приходит — остаётся empty.
func (a *CodexAdapter) ParseUsage(reader io.Reader) (*Usage, error) {
	var usage *Usage
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	var parseErr error
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Usage *struct {
				InputTokens           int64 `json:"input_tokens"`
				CachedInputTokens     int64 `json:"cached_input_tokens"`
				OutputTokens          int64 `json:"output_tokens"`
				ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			parseErr = err
			continue
		}
		if event.Type == "turn.completed" && event.Usage != nil {
			usage = &Usage{
				TokensInput:       event.Usage.InputTokens,
				CachedInputTokens: event.Usage.CachedInputTokens,
				TokensOutput:      event.Usage.OutputTokens,
				Attested:          true,
			}
		}
	}
	if parseErr != nil {
		return nil, fmt.Errorf("codex: невалидная JSONL-строка: %w", parseErr)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if usage == nil {
		return nil, fmt.Errorf("codex: событие turn.completed с usage не найдено в выводе")
	}
	return usage, nil
}

// ClassifyError — таксономия ошибок codex-запуска на основе захваченного
// вывода. Оркестратор вызывает этот метод опционально (см. Execute).
func (a *CodexAdapter) ClassifyError(output string) error {
	lower := strings.ToLower(output)
	switch {
	case containsAny(lower,
		"authentication", "authorization", "api key", "credentials",
		"not logged in", "401", "403", "invalid_api_key"):
		return &CodexRunError{Category: CodexErrorAuth, Detail: "аутентификация codex недоступна (проверь CODEX_API_KEY или allow-list окружения)"}
	case containsAny(lower,
		"model not found", "no such model", "model does not exist", "model not available", "unknown model"):
		return &CodexRunError{Category: CodexErrorModel, Detail: "запрошенная модель недоступна у codex-провайдера"}
	case containsAny(lower,
		"invalid config", "config error", "failed to parse config", "unknown setting", "toml"):
		return &CodexRunError{Category: CodexErrorConfig, Detail: "некорректная конфигурация codex"}
	case containsAny(lower,
		"network", "connection", "timed out", "timeout", "server error", "503", "failed to connect"):
		return &CodexRunError{Category: CodexErrorNetwork, Detail: "сетевая ошибка при обращении к codex-провайдеру"}
	case containsAny(lower,
		"not permitted", "permission denied", "sandbox", "blocked by policy", "denied"):
		return &CodexRunError{Category: CodexErrorSandbox, Detail: "действие заблокировано политикой sandbox codex"}
	default:
		return &CodexRunError{Category: CodexErrorUnknown, Detail: wrapDefaultDetail(output)}
	}
}

func containsAny(lower string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// wrapDefaultDetail не детализирует вывод харнесса (может содержать данные
// репозитория), а лишь сообщает о сбое запуска без утечки содержимого.
func wrapDefaultDetail(output string) string {
	return "codex завершился с ошибкой"
}

// CodexErrorCategory — категория сбоя codex-запуска (error taxonomy).
type CodexErrorCategory string

const (
	CodexErrorAuth    CodexErrorCategory = "authentication"
	CodexErrorModel   CodexErrorCategory = "model"
	CodexErrorConfig  CodexErrorCategory = "configuration"
	CodexErrorNetwork CodexErrorCategory = "network"
	CodexErrorSandbox CodexErrorCategory = "sandbox"
	CodexErrorUnknown CodexErrorCategory = "unknown"
)

// CodexRunError — типизированная ошибка запуска codex.
type CodexRunError struct {
	Category CodexErrorCategory
	Detail   string
}

func (e *CodexRunError) Error() string {
	return fmt.Sprintf("codex-%s: %s", e.Category, e.Detail)
}

func init() {
	RegisterAdapter(&CodexAdapter{})
}
