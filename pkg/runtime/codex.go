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

// CodexAdapter — реализация RuntimeAdapter для OpenAI Codex CLI.
// Вся codex-специфика (argv `codex exec --json`, sandbox, CODEX_HOME,
// JSONL-события, usage-статистика) локализована здесь.
//
// Промпт передаётся через stdin (`codex exec -`): оркестратор направляет
// файл промпта в cmd.Stdin — большие промпты не упираются в ARG_MAX.
//
// Политика изоляции: sandbox=workspace-write (записи только внутри workspace),
// headless without approvals, CODEX_HOME перенаправлен во временный каталог
// (проектный/user config и MCP-серверы не загружаются), env заменён
// allow-листом. Ограничение: tool-level deny (webfetch/websearch), который у
// OpenCode выражается permission-JSON, у Codex требует стабильного execpolicy
// grammar; пока он в preview — полагаемся на sandbox и zero-approval режим.
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
	args := []string{
		"exec",
		"--json",
		"--sandbox", "workspace-write",
		// Субпроцесс всегда ограничен sandbox workspace-write: безопаснее
		// гарантировать границу записей и для eval-каталогов вне git, чем
		// полагаться на требование git-репозитория (preflight проверил его
		// для agent-стадий отдельно).
		"--skip-git-repo-check",
		"--ephemeral",
	}
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

	// Defense-in-depth: те же policy, что и в argv-флагах; инициализирует
	// изолированный config, если harness станет читать его (а не только флаги).
	configContent := "approval_policy = \"never\"\nsandbox_mode = \"workspace-write\"\n"
	configPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		cleanup()
		return nil, func() {}, err
	}

	env := withAllowedEnvironmentKeys(os.Environ(), allowedEnvironmentKeys())
	env = append(env, "CODEX_HOME="+codexHome)
	sort.Strings(env)
	return env, cleanup, nil
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
				TokensInput:  event.Usage.InputTokens + event.Usage.CachedInputTokens,
				TokensOutput: event.Usage.OutputTokens,
				Attested:     true,
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
