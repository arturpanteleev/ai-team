package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// OpenCodeAdapter — первая реализация RuntimeAdapter для harness opencode.
// Вся opencode-специфика (permission JSON, argv `run --file`, env allow-list)
// локализована здесь; общий контроллер знает только контракт.
type OpenCodeAdapter struct{}

func (a *OpenCodeAdapter) Name() string { return "opencode" }

func (a *OpenCodeAdapter) Describe() Descriptor {
	return Descriptor{
		Name:   a.Name(),
		Binary: "opencode",
		Capabilities: []Capability{
			CapModelSelection,
			CapEffortMapping,
			CapPromptFile,
			CapSessionIsolation,
		},
	}
}

// Validate — fail-closed: любой запрос этапа, требующий capability, которую
// opencode не декларирует, блокирует запуск.
func (a *OpenCodeAdapter) Validate(launch Launch) error {
	return ValidateLaunch(a, launch)
}

// Command — документированный argv OpenCode CLI. Большой промпт прикрепляется
// файлом (0600), избегая ARG_MAX и случайного продолжения прежней сессии.
func (a *OpenCodeAdapter) Command(cli, model, promptFile string) ([]string, error) {
	if filepath.Base(cli) != a.Name() {
		return nil, fmt.Errorf("CLI %q не поддерживается адаптером opencode: требуется явный adapter вместо guessed arguments", cli)
	}
	args := []string{"run"}
	if model != "" && model != "auto" {
		args = append(args, "-m", model)
	}
	args = append(args, "--file", promptFile, "Выполни все инструкции из прикреплённого workflow-файла.")
	return args, nil
}

// Environment — изоляция сессии opencode: deny эффектных инструментов,
// суженные edit/read scopes из policy этапа и allow-list окружения процесса.
func (a *OpenCodeAdapter) Environment(agent *Agent, task *Task, inputs ...Artifact) ([]string, func(), error) {
	return openCodeIsolationEnvironment(agent, task, inputs...)
}

func init() {
	RegisterAdapter(&OpenCodeAdapter{})
}

// questionPermission разрешает инструмент question только агентам с
// ask_questions в интерактивном режиме; иначе deny (вопрос в non-TTY повис
// бы до таймаута).
func questionPermission(agent *Agent, task *Task) string {
	if agent != nil && agent.AskQuestions && task != nil && task.Interactive {
		return "allow"
	}
	return "deny"
}

func openCodeIsolationEnvironment(agent *Agent, task *Task, inputs ...Artifact) ([]string, func(), error) {
	for _, relative := range []string{"opencode.json", "opencode.jsonc", filepath.Join(".opencode", "plugins"), filepath.Join(".opencode", "tools")} {
		if info, err := os.Lstat(filepath.Join(task.TargetDir, relative)); err == nil && (info.IsDir() || info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return nil, func() {}, fmt.Errorf("project execution surface %s запрещена; плагины и custom tools входят в trusted controller, а не в agent runtime", relative)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, func() {}, fmt.Errorf("проверка %s: %w", relative, err)
		}
	}

	editRules := map[string]string{"*": "deny", ".ai-team/**": "deny", ".git/**": "deny"}
	target, err := filepath.Abs(task.TargetDir)
	if err != nil {
		return nil, func() {}, err
	}
	editRules[filepath.ToSlash(filepath.Join(target, ".ai-team"))+"/**"] = "deny"
	editRules[filepath.ToSlash(filepath.Join(target, ".git"))+"/**"] = "deny"
	readRules := map[string]string{
		"*": "allow", ".git/**": "deny", ".ai-team/**": "deny",
		".env": "deny", ".env.*": "deny", "**/.env": "deny", "**/.env.*": "deny",
	}
	readRules[filepath.ToSlash(filepath.Join(target, ".ai-team"))+"/**"] = "deny"
	readRules[filepath.ToSlash(filepath.Join(target, ".git"))+"/**"] = "deny"
	for _, input := range inputs {
		inputPath, pathErr := filepath.Abs(input.Path)
		if pathErr != nil {
			return nil, func() {}, pathErr
		}
		readRules[filepath.ToSlash(inputPath)] = "allow"
		if info, statErr := os.Lstat(inputPath); statErr == nil && info.IsDir() {
			readRules[filepath.ToSlash(inputPath)+"/**"] = "allow"
		}
	}
	if agent.Mutation == "source" || agent.Mutation == "tests" {
		for _, pattern := range agent.AllowedPaths {
			editRules[filepath.ToSlash(pattern)] = "allow"
			editRules[filepath.ToSlash(filepath.Join(target, filepath.FromSlash(pattern)))] = "allow"
		}
	}
	var artifactPaths []string
	for _, output := range agent.Outputs {
		artifactPaths = append(artifactPaths, ReplaceVars(output, task.Feature))
	}
	artifactPaths = append(artifactPaths,
		filepath.ToSlash(filepath.Join(task.Feature, "status", agent.Name+".md")),
		filepath.ToSlash(filepath.Join(task.Feature, ".stage-summary", agent.Name+".md")),
	)
	artifactRoot, err := filepath.Abs(task.ArtifactRoot)
	if err != nil {
		return nil, func() {}, err
	}
	for _, artifactPath := range artifactPaths {
		fullPath := filepath.Join(artifactRoot, filepath.FromSlash(artifactPath))
		relative, relErr := filepath.Rel(target, fullPath)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, func() {}, fmt.Errorf("artifact output %s находится вне target", fullPath)
		}
		relative = filepath.ToSlash(relative)
		editRules[relative] = "allow"
		editRules[relative+"/**"] = "allow"
		editRules[filepath.ToSlash(fullPath)] = "allow"
		editRules[filepath.ToSlash(fullPath)+"/**"] = "allow"
	}

	permission := map[string]any{
		"*":                  "deny",
		"bash":               "deny",
		"edit":               editRules,
		"external_directory": "deny",
		"glob":               "allow",
		"grep":               "allow",
		"list":               "allow",
		"lsp":                "deny",
		"question":           questionPermission(agent, task),
		"read":               readRules,
		"skill":              "deny",
		"task":               "deny",
		"webfetch":           "deny",
		"websearch":          "deny",
	}
	permissionJSON, err := json.Marshal(permission)
	if err != nil {
		return nil, func() {}, err
	}
	configJSON, err := json.Marshal(map[string]any{
		"permission": permission,
		"plugin":     []string{},
		"share":      "disabled",
	})
	if err != nil {
		return nil, func() {}, err
	}
	configHome, err := os.MkdirTemp("", "ai-team-opencode-config-*")
	if err != nil {
		return nil, func() {}, err
	}
	if err := os.Chmod(configHome, 0700); err != nil {
		_ = os.RemoveAll(configHome)
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(configHome) }
	env := withAllowedEnvironmentKeys(os.Environ(), allowedEnvironmentKeys())
	env = append(env,
		"OPENCODE_PERMISSION="+string(permissionJSON),
		"OPENCODE_CONFIG_CONTENT="+string(configJSON),
		"OPENCODE_DISABLE_DEFAULT_PLUGINS=true",
		"OPENCODE_DISABLE_LSP_DOWNLOAD=true",
		"OPENCODE_DISABLE_CLAUDE_CODE=true",
		"XDG_CONFIG_HOME="+configHome,
	)
	sort.Strings(env)
	return env, cleanup, nil
}

// baselineOpenCodeEnvironmentKeys are variable names passed through to the
// opencode subprocess unconditionally: they're standard OS/locale/session
// plumbing, not credentials, and opencode cannot run at all without at
// least PATH and HOME resolving correctly.
var baselineOpenCodeEnvironmentKeys = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "PWD",
	"LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TERM", "COLORTERM", "NO_COLOR",
}

// allowedEnvironmentKeys returns the set of environment variable names
// permitted to reach the opencode subprocess: the fixed baseline above, plus
// any names the project or user explicitly opts in via
// AI_TEAM_OPENCODE_ENV_ALLOW (comma-separated). This replaces passing the
// full parent environment minus a short deny-list: by default nothing beyond
// standard OS/locale plumbing crosses into the subprocess, and any provider
// credential (e.g. an LLM API key opencode itself needs to authenticate)
// must be named explicitly rather than leaking implicitly.
func allowedEnvironmentKeys() map[string]bool {
	allowed := make(map[string]bool, len(baselineOpenCodeEnvironmentKeys))
	for _, key := range baselineOpenCodeEnvironmentKeys {
		allowed[key] = true
	}
	for _, extra := range strings.Split(os.Getenv("AI_TEAM_OPENCODE_ENV_ALLOW"), ",") {
		if key := strings.TrimSpace(extra); key != "" {
			allowed[key] = true
		}
	}
	return allowed
}

// withAllowedEnvironmentKeys builds a subprocess environment containing only
// the explicitly allowed variable names from the parent environment.
func withAllowedEnvironmentKeys(environment []string, allowed map[string]bool) []string {
	filtered := make([]string, 0, len(allowed))
	for _, item := range environment {
		key := strings.SplitN(item, "=", 2)[0]
		if allowed[key] {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// EnvAllowVar — инструмент явного opt-in для проброса переменных окружения
// в opencode-субпроцесс (список имён через запятую).
const EnvAllowVar = "AI_TEAM_OPENCODE_ENV_ALLOW"

// LookPath проверяет доступность бинарника харнесса в PATH.
func (a *OpenCodeAdapter) LookPath(cli string) error {
	if _, err := exec.LookPath(cli); err != nil {
		return fmt.Errorf("%s: команда не найдена в PATH (%s)", cli, binaryDocsURL(a.Name()))
	}
	return nil
}

func binaryDocsURL(binary string) string {
	switch binary {
	case "opencode":
		return "https://opencode.ai"
	case "codex":
		return "https://github.com/openai/codex"
	case "claude":
		return "https://docs.anthropic.com/en/docs/claude-code"
	default:
		return "https://github.com/arturpanteleev/ai-team"
	}
}
