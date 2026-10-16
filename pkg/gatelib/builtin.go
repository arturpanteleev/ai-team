package gatelib

import (
	"sync"

	"github.com/arturpanteleev/ai-team/pkg/gatephrases"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// IDs of the gate definitions published in the built-in library (version 1).
// The first group wraps the behavior that schema v5 expressed through
// stage.required_sections, stage.check, stage.confirm; the semantics of every
// version-1 gate equals its legacy counterpart.
const (
	// Code gates (kind builtin).
	GateRequiredSections = "required_sections"
	GateVerdictMarker    = "verdict_marker"
	// GateRequiredPhrases checks that explicitly configured words or fragments
	// are present in the Markdown result (pkg/gatephrases).
	GateRequiredPhrases = "required_phrases"
	GatePRExists        = "pr_exists"
	GatePROpen          = "pr_open"
	GatePRBaseBranch    = "pr_base_branch"
	GateProjectChecks   = "project_checks"
	GatePRChecksGreen   = "pr_checks_green"
	// GateAgentReview is the LLM gate: a reviewer agent with a fixed input
	// and verdict schema. mode=grill is one way of running it.
	GateAgentReview = "agent_review"
	// GateHumanApproval is the human decision gate bound to the result hash.
	GateHumanApproval = "human_approval"
	// GateExternalProgram is the external-program gate (kind external): a
	// trusted command from the repository configuration that speaks the JSON
	// gate protocol (docs/reference/gate-protocol.md, pkg/gaterun).
	GateExternalProgram = "external_program"
	// GateDockerProgram is the same JSON gate protocol, run in a pinned
	// container image with strict isolation defaults (kind docker).
	GateDockerProgram = "docker_program"
)

// PRRuleGates are the code gates that check a pull-request reference. They
// correspond one-to-one to the legacy hard-check rule names.
var PRRuleGates = []string{GatePRExists, GatePROpen, GatePRBaseBranch, GateProjectChecks, GatePRChecksGreen}

var (
	defaultOnce     sync.Once
	defaultRegistry *Registry
)

// Default returns the process-wide built-in library. It is read-only by
// convention: callers that need to model a newer library use Clone.
func Default() *Registry {
	defaultOnce.Do(func() { defaultRegistry = newBuiltin() })
	return defaultRegistry
}

func newBuiltin() *Registry {
	zero := 0
	r := NewRegistry()
	r.MustRegister(workflow.GateDefinition{
		ID: GateRequiredSections, Version: 1, Kind: workflow.GateKindBuiltin,
		Title:       "Обязательные разделы",
		Description: "Markdown-результат содержит заголовки всех перечисленных разделов.",
		ResultTypes: []string{"md"},
		Params: []workflow.GateParam{{
			Name: "sections", Type: workflow.GateParamStringList, Required: true,
			Description: "Названия заголовков, которые обязаны присутствовать.",
		}},
	})
	r.MustRegister(workflow.GateDefinition{
		ID: GateVerdictMarker, Version: 1, Kind: workflow.GateKindBuiltin,
		Title:       "Маркер вердикта",
		Description: "Markdown-результат содержит маркер вердикта проверяющего.",
		ResultTypes: []string{"md"},
	})
	one := 1
	for _, phrasesVersion := range []int{1, 2} {
		description := "Markdown-результат содержит заданные слова или фрагменты. Регистр, область поиска (весь результат или раздел по заголовку) и правило совпадения задаются явно; пустой результат и отсутствующий раздел — fail. Пользовательских скриптов нет; регулярные выражения только RE2 и только с regex: true."
		if phrasesVersion >= 2 {
			description += " Версия 2 дополнительно отклоняет управляющие символы во фразах и пересекающиеся фразы при match: at_least."
		}
		r.MustRegister(workflow.GateDefinition{
			ID: GateRequiredPhrases, Version: phrasesVersion, Kind: workflow.GateKindBuiltin,
			Title:       "Обязательные слова и фрагменты",
			Description: description,
			ResultTypes: []string{"md"},
			Params: []workflow.GateParam{
				{Name: gatephrases.ParamPhrases, Type: workflow.GateParamStringList, Required: true,
					Description: "Слова или фрагменты (не больше 50, каждая до 200 байт); при regex: true — выражения RE2."},
				{Name: gatephrases.ParamCase, Type: workflow.GateParamEnum, Required: true, Enum: gatephrases.CaseValues,
					Description: "Учитывать ли регистр: sensitive или insensitive."},
				{Name: gatephrases.ParamScope, Type: workflow.GateParamEnum, Required: true, Enum: gatephrases.ScopeValues,
					Description: "Где искать: result — весь результат, section — только в разделе."},
				{Name: gatephrases.ParamSection, Type: workflow.GateParamString,
					Description: "Заголовок раздела (обязателен при scope: section); тело раздела — до заголовка того же или более высокого уровня."},
				{Name: gatephrases.ParamMatch, Type: workflow.GateParamEnum, Required: true, Enum: gatephrases.MatchValues,
					Description: "Правило: all — все фразы, any — любая, at_least — не меньше min_count."},
				{Name: gatephrases.ParamMinCount, Type: workflow.GateParamInt, Min: &one,
					Description: "Минимум найденных фраз (обязателен при match: at_least, не больше числа фраз)."},
				{Name: gatephrases.ParamUnit, Type: workflow.GateParamEnum, Enum: gatephrases.UnitValues,
					Description: "Единица совпадения: substring (по умолчанию) или word — целое слово."},
				{Name: gatephrases.ParamRegex, Type: workflow.GateParamBool,
					Description: "true — фразы это выражения RE2 (линейное время, без скриптов); по умолчанию false."},
			},
		})
	}
	for _, spec := range []struct{ id, title, description string }{
		{GatePRExists, "PR существует", "Ссылка результата указывает на существующий pull request."},
		{GatePROpen, "PR открыт", "Pull request из ссылки результата открыт."},
		{GatePRBaseBranch, "Базовая ветка PR", "Pull request нацелен на базовую ветку репозитория."},
	} {
		r.MustRegister(workflow.GateDefinition{
			ID: spec.id, Version: 1, Kind: workflow.GateKindBuiltin,
			Title: spec.title, Description: spec.description,
			ResultTypes: []string{"link"},
		})
	}
	for _, spec := range []struct{ id, title, description string }{
		{GateProjectChecks, "Проектные проверки PR", "Обязательные проверки проекта пройдены для pull request."},
		{GatePRChecksGreen, "CI PR зелёный", "Все проверки pull request зелёные."},
	} {
		r.MustRegister(workflow.GateDefinition{
			ID: spec.id, Version: 1, Kind: workflow.GateKindBuiltin,
			Title: spec.title, Description: spec.description,
			ResultTypes: []string{"link"}, LinkKinds: []string{"pr"},
		})
	}
	r.MustRegister(workflow.GateDefinition{
		ID: GateAgentReview, Version: 1, Kind: workflow.GateKindLLM,
		Title:       "Проверка агентом",
		Description: "Агент-проверяющий оценивает результат; ответ вне схемы вердикта — техническая ошибка, а не pass.",
		Params: []workflow.GateParam{
			{Name: "agent", Type: workflow.GateParamString, Required: true, Description: "Имя агента из реестра."},
			{Name: "mode", Type: workflow.GateParamEnum, Enum: []string{"grill"}, Description: "Режим проверки; grill — вопросы и уточнения."},
			{Name: "max_rounds", Type: workflow.GateParamInt, Min: &zero, Description: "Лимит раундов режима grill."},
		},
	})
	r.MustRegister(workflow.GateDefinition{
		ID: GateHumanApproval, Version: 1, Kind: workflow.GateKindHuman,
		Title:       "Решение человека",
		Description: "Пауза до решения ответственной функции этапа по точному хешу результата.",
	})
	r.MustRegister(workflow.GateDefinition{
		ID: GateExternalProgram, Version: 1, Kind: workflow.GateKindExternal,
		Title:       "Внешняя программа",
		Description: "Доверенная команда из конфигурации репозитория читает JSON-контекст и возвращает один JSON-вердикт. Падение, таймаут, превышение лимита и неверный JSON — техническая ошибка, а не вердикт.",
		Params: []workflow.GateParam{
			{Name: "command", Type: workflow.GateParamStringList, Required: true, Description: "argv без shell: программа и аргументы; секреты в аргументы не вписываются."},
			{Name: "timeout_seconds", Type: workflow.GateParamInt, Min: &one, Description: "Таймаут запуска, секунд (по умолчанию 60, максимум 3600)."},
			{Name: "cwd", Type: workflow.GateParamString, Description: "Рабочий каталог относительно корня репозитория (по умолчанию .)."},
			{Name: "env_allow", Type: workflow.GateParamStringList, Description: "Имена переменных окружения контроллера, передаваемых программе; остальное отбрасывается."},
			{Name: "max_output_bytes", Type: workflow.GateParamInt, Min: &one, Description: "Лимит stdout и stderr по отдельности, байт (по умолчанию 1 МиБ, максимум 16 МиБ)."},
		},
	})
	r.MustRegister(workflow.GateDefinition{
		ID: GateDockerProgram, Version: 1, Kind: workflow.GateKindDocker,
		Title:       "Программа в Docker",
		Description: "Тот же JSON-протокол, что у внешней программы, но команда выполняется в контейнере из образа, закреплённого digest: без сети, с read-only корнем и репозиторием, без capabilities, Docker-сокета, домашнего каталога и окружения контроллера. Любое расширение доступа задаётся параметрами и записывается в evidence. Ошибка Docker, образа или рантайма — техническая ошибка, а не вердикт.",
		Params: []workflow.GateParam{
			{Name: "image", Type: workflow.GateParamString, Required: true, Description: "Образ name@sha256:<digest>; тег без digest отклоняется."},
			{Name: "command", Type: workflow.GateParamStringList, Required: true, Description: "argv внутри образа без shell; заменяет entrypoint образа. Секреты в аргументы не вписываются."},
			{Name: "allow_unpinned_image", Type: workflow.GateParamBool, Description: "Явное разрешение образа без digest (исключение, записывается в evidence)."},
			{Name: "pull", Type: workflow.GateParamEnum, Enum: []string{"missing", "never"}, Description: "missing (по умолчанию) — тянуть образ, если его нет локально; never — только локальный."},
			{Name: "network", Type: workflow.GateParamEnum, Enum: []string{"none", "bridge"}, Description: "none (по умолчанию) — без сети; bridge — исключение."},
			{Name: "cpus", Type: workflow.GateParamString, Description: "Лимит CPU, десятичное число (по умолчанию 1)."},
			{Name: "memory_mb", Type: workflow.GateParamInt, Min: &one, Description: "Лимит памяти без swap, МиБ (по умолчанию 512)."},
			{Name: "pids_limit", Type: workflow.GateParamInt, Min: &one, Description: "Лимит процессов (по умолчанию 128)."},
			{Name: "tmpfs_mb", Type: workflow.GateParamInt, Min: &zero, Description: "Размер scratch /tmp в памяти (noexec), МиБ; 0 — без /tmp (по умолчанию 64)."},
			{Name: "user", Type: workflow.GateParamString, Description: "uid[:gid] или имя пользователя в контейнере; по умолчанию uid:gid контроллера (не root)."},
			{Name: "repo_access", Type: workflow.GateParamEnum, Enum: []string{"ro", "rw"}, Description: "Доступ к репозиторию: ro (по умолчанию) или rw (исключение)."},
			{Name: "mounts", Type: workflow.GateParamStringList, Description: "Дополнительные mounts подкаталогов репозитория: <путь в репозитории>:<абсолютный путь в контейнере>[:ro|rw]; по умолчанию ro."},
			{Name: "timeout_seconds", Type: workflow.GateParamInt, Min: &one, Description: "Таймаут запуска вместе с pull, секунд (по умолчанию 60, максимум 3600)."},
			{Name: "cwd", Type: workflow.GateParamString, Description: "Рабочий каталог относительно корня репозитория (по умолчанию .)."},
			{Name: "env_allow", Type: workflow.GateParamStringList, Description: "Имена переменных окружения контроллера, передаваемых программе в контейнере; остальное отбрасывается."},
			{Name: "max_output_bytes", Type: workflow.GateParamInt, Min: &one, Description: "Лимит stdout и stderr по отдельности, байт (по умолчанию 1 МиБ, максимум 16 МиБ)."},
		},
	})
	registerDecisionGates(r)
	return r
}

// paramValidators hold the cross-parameter rules a flat parameter schema cannot
// express (a parameter required only for one value of another, bounds relative
// to a list, pattern compilation). They run in Resolve, so a bad combination is
// rejected by configuration validation before any run starts. The key is the
// exact "id@version".
var paramValidators = map[string]func(map[string]any) error{
	"required_phrases@1": gatephrases.ValidateVersion(1),
	"required_phrases@2": gatephrases.ValidateVersion(2),
}
