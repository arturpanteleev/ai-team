package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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

// claudeSettingsJSON — статическая часть session policy, записываемой в
// изолированный CLAUDE_CONFIG_DIR: path-deny секретов и служебных каталогов
// плюс permission-mode. Запись (Edit/Write) сюда не входит: её правила
// строит claudeWriteRules по конкретным путям этапа.
const claudeSettingsJSON = `{
  "permissionMode": "acceptEdits",
  "disallowedTools": ["Bash", "PowerShell", "WebFetch", "WebSearch", "Task", "Agent", "Skill", "LSP", "NotebookEdit"],
  "permissions": {
    "allow": ["Read", "Glob", "Grep"],
    "deny": [
      "Bash(**)", "PowerShell(**)", "WebFetch(**)", "WebSearch", "Task(**)", "Agent(**)", "Skill(**)", "LSP(*)",
      "Read(~/.ssh/**)", "Read(~/.aws/**)", "Read(~/.gnupg/**)",
      "Read(**/.env)", "Read(**/.env.*)",
      "Read(.env)", "Read(.env.*)", "Read(**/.git/**)", "Read(.git/**)",
      "Read(**/.ssh/**)", "Read(**/.aws/**)", "Read(**/.gnupg/**)",
      "Read(**/credentials)", "Read(**/credentials/**)",
      "Edit(**/.git/**)", "Edit(.git/**)"
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

	settings, err := claudeSessionSettings(task)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	settingsPath := filepath.Join(configDir, "settings.json")
	if err := os.WriteFile(settingsPath, settings, 0600); err != nil {
		cleanup()
		return nil, func() {}, err
	}

	env := withAllowedEnvironmentKeys(os.Environ(), allowedClaudeEnvironmentKeys())
	env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
	sort.Strings(env)
	return env, cleanup, nil
}

func claudeSessionSettings(task *Task) ([]byte, error) {
	var settings struct {
		PermissionMode  string   `json:"permissionMode"`
		DisallowedTools []string `json:"disallowedTools"`
		Permissions     struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(claudeSettingsJSON), &settings); err != nil {
		return nil, fmt.Errorf("claude: decode trusted session policy: %w", err)
	}
	allow, deny, err := claudeWriteRules(task)
	if err != nil {
		return nil, err
	}
	settings.Permissions.Allow = append(settings.Permissions.Allow, allow...)
	settings.Permissions.Deny = append(settings.Permissions.Deny, deny...)
	paths, err := exactDeniedReadPaths(task)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		rule, ruleErr := claudeReadDenyRule(path)
		if ruleErr != nil {
			return nil, ruleErr
		}
		settings.Permissions.Deny = append(settings.Permissions.Deny, rule)
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("claude: encode stage session policy: %w", err)
	}
	return encoded, nil
}

// claudeWriteRules строит правила Edit/Write для этапа. Claude применяет
// deny раньше allow, поэтому разрешить каталог артефактов внутри .ai-team
// общим deny **/.ai-team/** нельзя: запрещённые соседние элементы
// .ai-team перечисляются явно (на момент запуска этапа), а allow даётся
// только рабочему каталогу цели и каталогу <ArtifactRoot>/<Feature>.
func claudeWriteRules(task *Task) (allow, deny []string, err error) {
	if task == nil {
		return nil, nil, nil
	}
	if task.TargetDir != "" {
		target, absErr := filepath.Abs(task.TargetDir)
		if absErr != nil {
			return nil, nil, fmt.Errorf("claude: путь цели: %w", absErr)
		}
		allow = append(allow, claudeWriteAllowRules(target)...)
	}
	artifactDir, aiTeam, layoutOK, err := stageArtifactLayout(task)
	if err != nil {
		return nil, nil, err
	}
	if artifactDir != "" {
		allow = append(allow, claudeWriteAllowRules(artifactDir)...)
		// Протокольные файлы контроллера (tasks/<feature>/questions.md — вопросы
		// к Product Owner при BLOCKED) лежат в каталоге tasks рядом с артефактами.
		allow = append(allow, claudeWriteAllowRules(filepath.Join(filepath.Dir(artifactDir), "tasks", filepath.Base(artifactDir)))...)
	}
	if !layoutOK {
		// Нестандартная раскладка артефактов: запрещаем .ai-team целиком,
		// как и раньше; карве-аут каталога этапа в этом случае не строится.
		return allow, []string{"Edit(**/.ai-team/**)", "Edit(.ai-team/**)", "Write(**/.ai-team/**)", "Write(.ai-team/**)"}, nil
	}
	deny, err = aiTeamWriteDenyRules(aiTeam, filepath.Dir(artifactDir), filepath.Base(artifactDir))
	if err != nil {
		return nil, nil, err
	}
	return allow, deny, nil
}

// stageArtifactLayout возвращает каталог артефактов этапа
// (<ArtifactRoot>/<Feature>), а при раскладке <target>/.ai-team/artifacts
// также сам каталог .ai-team (layoutOK=true).
func stageArtifactLayout(task *Task) (artifactDir, aiTeam string, layoutOK bool, err error) {
	if task.ArtifactRoot == "" || !validStageFeatureSegment(task.Feature) {
		return "", "", false, nil
	}
	root, err := filepath.Abs(task.ArtifactRoot)
	if err != nil {
		return "", "", false, fmt.Errorf("claude: путь артефактов: %w", err)
	}
	artifactDir = filepath.Join(root, task.Feature)
	if filepath.Base(root) == "artifacts" && filepath.Base(filepath.Dir(root)) == ".ai-team" {
		return artifactDir, filepath.Dir(root), true, nil
	}
	return artifactDir, "", false, nil
}

func validStageFeatureSegment(feature string) bool {
	if feature == "" || feature == "." || feature == ".." {
		return false
	}
	return !strings.ContainsAny(feature, `/\*?()[]{}`)
}

func claudeWriteAllowRules(absDir string) []string {
	path := "//" + filepath.ToSlash(absDir)[1:] + "/**"
	return []string{"Edit(" + path + ")", "Write(" + path + ")"}
}

// aiTeamWriteDenyRules запрещает запись в .ai-team, кроме каталога этапа:
// каждый элемент .ai-team (кроме artifacts) и каждый элемент
// artifacts (кроме каталога этапа) закрываются целиком, плюс прямые файлы
// обоих каталогов.
func aiTeamWriteDenyRules(aiTeam, artifactsDir, feature string) ([]string, error) {
	var rules []string
	for _, item := range []struct {
		dir  string
		keep []string
	}{{aiTeam, []string{"artifacts"}}, {artifactsDir, []string{feature, "tasks"}}} {
		if err := claudeCheckRulePath(item.dir); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(item.dir)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("claude: список %s: %w", item.dir, err)
		}
		for _, entry := range entries {
			if slices.Contains(item.keep, entry.Name()) {
				continue
			}
			path := filepath.Join(item.dir, entry.Name())
			if err := claudeCheckRulePath(path); err != nil {
				return nil, err
			}
			rules = append(rules, claudeWriteDenyRules(path)...)
		}
		// Прямые файлы каталога: маска с расширением. Маска dir/* здесь
		// недопустима: правила Claude следуют семантике gitignore, и запрет
		// каталога закрывает всё его содержимое, включая каталог этапа.
		// Каталог feature с точкой в имени под маску *.* попал бы целиком,
		// поэтому для него прямое правило не строим.
		if strings.Contains(feature, ".") && item.dir == artifactsDir {
			continue
		}
		direct := "//" + filepath.ToSlash(item.dir)[1:] + "/*.*"
		rules = append(rules, "Edit("+direct+")", "Write("+direct+")")
	}
	return rules, nil
}

func claudeWriteDenyRules(absPath string) []string {
	path := "//" + filepath.ToSlash(absPath)[1:]
	return []string{"Edit(" + path + ")", "Edit(" + path + "/**)", "Write(" + path + ")", "Write(" + path + "/**)"}
}

// claudeCheckRulePath отклоняет пути с символами грамматики правил Claude:
// такой путь нельзя выразить точно, а расширение правила опасно.
func claudeCheckRulePath(absPath string) error {
	if !filepath.IsAbs(absPath) {
		return fmt.Errorf("claude: путь правила должен быть абсолютным: %q", absPath)
	}
	if strings.ContainsAny(absPath, "*?()[]{}") {
		return fmt.Errorf("claude: путь %q содержит символы, которые нельзя выразить правилом permissions", absPath)
	}
	return nil
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
		usage.CacheReadInputTokens = result.Usage.CacheReadInputTokens
		usage.CacheWriteInputTokens = result.Usage.CacheCreationInputTokens
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
