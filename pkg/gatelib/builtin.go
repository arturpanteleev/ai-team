package gatelib

import (
	"sync"

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
	GatePRExists         = "pr_exists"
	GatePROpen           = "pr_open"
	GatePRBaseBranch     = "pr_base_branch"
	GateProjectChecks    = "project_checks"
	GatePRChecksGreen    = "pr_checks_green"
	// GateAgentReview is the LLM gate: a reviewer agent with a fixed input
	// and verdict schema. mode=grill is one way of running it.
	GateAgentReview = "agent_review"
	// GateHumanApproval is the human decision gate bound to the result hash.
	GateHumanApproval = "human_approval"
	// GateExternalProgram is the external-program gate (kind external): a
	// trusted command from the repository configuration that speaks the JSON
	// gate protocol (docs/reference/gate-protocol.md, pkg/gaterun).
	GateExternalProgram = "external_program"
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
	one := 1
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
	return r
}
