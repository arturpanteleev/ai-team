package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ClaudeAdapter — реализация RuntimeAdapter для Claude Code (print mode).
// Печать JSON-результата, --effort, deny-list инструментов и изоляция
// CLAUDE_CONFIG_DIR локализованы здесь.
//
// Промпт подаётся через stdin: argv `claude -p` без аргументов-промпта при
// piped stdin берёт промпт целиком из stdin (оркестратор направляет файл
// промпта в cmd.Stdin). Один -p прогон — одна headless session без
// интерактивных permission-промптов.
//
// Политика изоляции: permission-mode acceptEdits (автоприём file edits),
// deny-list эффектных инструментов (Bash/WebFetch/WebSearch/Task/Skill/...),
// path-deny для секретов (~/.ssh, ~/.aws, *.env), CLAUDE_CONFIG_DIR —
// временный каталог 0700 (user settings/history не загружаются), env —
// allow-лист. Результат парсится как JSON result c total_cost_usd (client-side
// оценка харнесса, attest только этого источника) и usage-tokens.
type ClaudeAdapter struct{}

func (a *ClaudeAdapter) Name() string { return "claude" }

func (a *ClaudeAdapter) Describe() Descriptor {
	return Descriptor{
		Name:   a.Name(),
		Binary: "claude",
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

// Validate — fail-closed: запрос capability, отсутствующей у claude,
// блокирует запуск (см. ValidateLaunch).
func (a *ClaudeAdapter) Validate(launch Launch) error {
	return ValidateLaunch(a, launch)
}

// Command — argv headless-запуска: print-режим, JSON-результат, acceptEdits,
// deny-list эффектных инструментов, явные model/effort.
func (a *ClaudeAdapter) Command(cli string, launch Launch, promptFile string) ([]string, error) {
	if filepath.Base(cli) != a.Name() {
		return nil, fmt.Errorf("CLI %q не поддерживается адаптером claude: требуется явный adapter вместо guessed arguments", cli)
	}
	args := []string{
		"-p",
		"--output-format", "json",
		"--permission-mode", "acceptEdits",
		"--disallowedTools", "Bash,PowerShell,WebFetch,WebSearch,Task,Agent,Skill,LSP,NotebookEdit",
	}
	if launch.Model != "" && launch.Model != "auto" {
		args = append(args, "--model", launch.Model)
	}
	if launch.Effort != "" {
		if !validClaudeEffort(launch.Effort) {
			return nil, fmt.Errorf("claude: недопустимый effort %q (low|medium|high)", launch.Effort)
		}
		args = append(args, "--effort", launch.Effort)
	}
	return args, nil
}

func validClaudeEffort(effort string) bool {
	switch effort {
	case "low", "medium", "high":
		return true
	}
	return false
}

// claudeDenyTools — deny-list инструментов, зеркалящий opencode permission
// (запрет эффектных/неинспектируемых инструментов: shell, web, subagents,
// skills, LSP). Записи в settings.permissions.deny дублируются в argv
// --disallowedTools (приоритет — merge deny).
const claudeDisallowedTools = "Bash,PowerShell,WebFetch,WebSearch,Task,Agent,Skill,LSP,NotebookEdit"

// claudeSettingsJSON — session policy, записываемая в изолированный
// CLAUDE_CONFIG_DIR: path-deny секретов и служебных каталогов плюс
// permission-mode.
const claudeSettingsJSON = `{
  "permissionMode": "acceptEdits",
  "disallowedTools": ["Bash", "PowerShell", "WebFetch", "WebSearch", "Task", "Agent", "Skill", "LSP", "NotebookEdit"],
  "permissions": {
    "allow": ["Read", "Edit", "Write", "Glob", "Grep"],
    "deny": [
      "Bash(**)", "PowerShell(**)", "WebFetch(**)", "WebSearch", "Task(**)", "Agent(**)", "Skill(**)", "LSP(*)",
      "Read(~/.ssh/**)", "Read(~/.aws/**)", "Read(**/.env)", "Read(**/.env.*)",
      "Read(.env)", "Read(.env.*)", "Read(**/.git/**)", "Read(.git/**)",
      "Edit(**/.git/**)", "Edit(.git/**)", "Edit(**/.ai-team/**)", "Edit(.ai-team/**)",
      "Edit(//**)", "Write(//**)",
      "Edit(~/**)", "Write(~/**)"
    ]
  }
}
`

// Environment — изоляция claude-сессии: project execution surface guard,
// CLAUDE_CONFIG_DIR во временном каталоге (0700) с session-policy, allow-list
// env субпроцесса.
func (a *ClaudeAdapter) Environment(agent *Agent, task *Task, inputs ...Artifact) ([]string, func(), error) {
	for _, relative := range []string{filepath.Join(".claude", "settings.json")} {
		if info, err := os.Lstat(filepath.Join(task.TargetDir, relative)); err == nil && (info.IsDir() || info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return nil, func() {}, fmt.Errorf("project execution surface %s запрещена; project settings входят в trusted controller, а не в agent runtime", relative)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, func() {}, fmt.Errorf("проверка %s: %w", relative, err)
		}
	}

	configDir, err := os.MkdirTemp("", "ai-team-claude-config-*")
	if err != nil {
		return nil, func() {}, err
	}
	if err := os.Chmod(configDir, 0700); err != nil {
		_ = os.RemoveAll(configDir)
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(configDir) }

	settingsPath := filepath.Join(configDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(claudeSettingsJSON), 0600); err != nil {
		cleanup()
		return nil, func() {}, err
	}

	env := withAllowedEnvironmentKeys(os.Environ(), allowedEnvironmentKeys())
	env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
	sort.Strings(env)
	return env, cleanup, nil
}

// claudeResultLine — JSON result, который печатает `claude -p
// --output-format json` (последняя строка stdout).
type claudeResultLine struct {
	Type         string   `json:"type"`
	Subtype      string   `json:"subtype"`
	IsError      bool     `json:"is_error"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
	Usage        *struct {
		InputTokens              int64 `json:"input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
	} `json:"usage"`
}

func lastClaudeResult(reader io.Reader) (*claudeResultLine, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	var result *claudeResultLine
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var parsed claudeResultLine
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			continue
		}
		if parsed.Type == "result" {
			result = &parsed
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("claude: result JSON не найден в stdout (проверь --output-format json)")
	}
	return result, nil
}

// ParseUsage — типизированный разбор usage из JSON-результата claude.
// Tokens и total_cost_usd берутся из харнесса (CostUSD — client-side оценка,
// поле Attested=true фиксирует именно этот источник аттестации).
func (a *ClaudeAdapter) ParseUsage(reader io.Reader) (*Usage, error) {
	result, err := lastClaudeResult(reader)
	if err != nil {
		return nil, err
	}
	usage := &Usage{Currency: "USD", Attested: true}
	if result.Usage != nil {
		usage.TokensInput = result.Usage.InputTokens
		usage.CachedInputTokens = result.Usage.CacheCreationInputTokens + result.Usage.CacheReadInputTokens
		usage.TokensOutput = result.Usage.OutputTokens
	}
	if result.TotalCostUSD != nil {
		usage.CostUSD = *result.TotalCostUSD
	}
	return usage, nil
}

// ClassifyError — таксономия ошибок claude-запуска на основе захваченного
// вывода (включая subtype JSON-результата). Вызывается оркестратором
// опционально (см. Execute).
func (a *ClaudeAdapter) ClassifyError(output string) error {
	if result, err := lastClaudeResult(strings.NewReader(output)); err == nil {
		if result.IsError {
			switch {
			case strings.Contains(result.Subtype, "budget"):
				return &ClaudeRunError{Category: ClaudeErrorBudget, Detail: fmt.Sprintf("claude остановлен по бюджету (%s)", result.Subtype)}
			case strings.Contains(result.Subtype, "turns"):
				return &ClaudeRunError{Category: ClaudeErrorTimeout, Detail: fmt.Sprintf("claude исчерпал лимит шагов (%s)", result.Subtype)}
			case strings.Contains(result.Subtype, "permission"):
				return &ClaudeRunError{Category: ClaudeErrorPermission, Detail: fmt.Sprintf("действие отклонено политикой permissions (%s)", result.Subtype)}
			case strings.Contains(result.Subtype, "execution"):
				return &ClaudeRunError{Category: ClaudeErrorRuntime, Detail: fmt.Sprintf("claude прерван во время исполнения (%s)", result.Subtype)}
			}
		}
	}
	lower := strings.ToLower(output)
	switch {
	case containsAny(lower,
		"authentication", "authorization", "api key", "invalid key", "401", "403"):
		return &ClaudeRunError{Category: ClaudeErrorAuth, Detail: "аутентификация claude недоступна (проверь ANTHROPIC_API_KEY и allow-list окружения)"}
	case containsAny(lower,
		"model not found", "unknown model", "model does not exist", "invalid model", "model.does.not.exist"):
		return &ClaudeRunError{Category: ClaudeErrorModel, Detail: "запрошенная модель недоступна"}
	case containsAny(lower,
		"invalid config", "config error", "failed to load settings", "invalid settings", "settings file"):
		return &ClaudeRunError{Category: ClaudeErrorConfig, Detail: "некорректная конфигурация claude"}
	case containsAny(lower,
		"network", "connection", "timed out", "network error", "503", "failed to connect"):
		return &ClaudeRunError{Category: ClaudeErrorNetwork, Detail: "сетевая ошибка при обращении к Anthropic API"}
	default:
		return &ClaudeRunError{Category: ClaudeErrorUnknown, Detail: "claude завершился с ошибкой"}
	}
}

// ClaudeErrorCategory — категория сбоя claude-запуска (error taxonomy).
type ClaudeErrorCategory string

const (
	ClaudeErrorAuth       ClaudeErrorCategory = "authentication"
	ClaudeErrorModel      ClaudeErrorCategory = "model"
	ClaudeErrorConfig     ClaudeErrorCategory = "configuration"
	ClaudeErrorNetwork    ClaudeErrorCategory = "network"
	ClaudeErrorBudget     ClaudeErrorCategory = "budget"
	ClaudeErrorTimeout    ClaudeErrorCategory = "timeout"
	ClaudeErrorPermission ClaudeErrorCategory = "permission"
	ClaudeErrorRuntime    ClaudeErrorCategory = "runtime"
	ClaudeErrorUnknown    ClaudeErrorCategory = "unknown"
)

// ClaudeRunError — типизированная ошибка запуска claude.
type ClaudeRunError struct {
	Category ClaudeErrorCategory
	Detail   string
}

func (e *ClaudeRunError) Error() string {
	return fmt.Sprintf("claude-%s: %s", e.Category, e.Detail)
}

func init() {
	RegisterAdapter(&ClaudeAdapter{})
}
