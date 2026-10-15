package gatelib

import "github.com/arturpanteleev/ai-team/pkg/workflow"

// Parameter values of the version-2 decision gates.
const (
	// OnRejectStop ends the run when the human rejects (version 1 behavior).
	OnRejectStop = "stop"
	// OnRejectStay keeps the task at the stage: a rejection is recorded and
	// the result waits for a new version, which runs the whole chain again.
	OnRejectStay = "stay"
)

// registerDecisionGates publishes the version-2 LLM and human gates. Version 1
// of both stays published unchanged: pinned templates keep their definitions.
func registerDecisionGates(r *Registry) {
	zero := 0
	one := 1
	r.MustRegister(workflow.GateDefinition{
		ID: GateAgentReview, Version: 2, Kind: workflow.GateKindLLM,
		Title: "Проверка агентом",
		Description: "Агент-проверяющий получает результат этапа и обязан ответить по объявленной схеме вердикта. " +
			"Ответ вне схемы и отказ рантайма — техническая ошибка (error), а не pass и не fail; " +
			"число попыток вызова ограничено конфигом, каждая попытка остаётся в трассе. " +
			"mode grill — отдельный режим вопросов человеку и не часть контракта вердикта.",
		Params: []workflow.GateParam{
			{Name: "agent", Type: workflow.GateParamString, Required: true, Description: "Имя агента из реестра."},
			{Name: "mode", Type: workflow.GateParamEnum, Enum: []string{"grill"}, Description: "Режим проверки; grill — вопросы и уточнения (схема и попытки к нему не применяются)."},
			{Name: "max_rounds", Type: workflow.GateParamInt, Min: &zero, Description: "Лимит раундов режима grill."},
			{Name: "verdict_schema", Type: workflow.GateParamEnum, Enum: []string{"verdict_marker", "json"},
				Description: "Схема ответа проверяющего: verdict_marker (строка Verdict: PASSED|FAILED, по умолчанию) или json (объект {verdict, reason, findings})."},
			{Name: "max_attempts", Type: workflow.GateParamInt, Min: &one,
				Description: "Сколько раз вызвать проверяющего, пока нет валидного вердикта (по умолчанию 1, максимум 5). Валидный fail не повторяется."},
		},
	})
	r.MustRegister(workflow.GateDefinition{
		ID: GateHumanApproval, Version: 2, Kind: workflow.GateKindHuman,
		Title: "Решение человека",
		Description: "Пауза до решения ответственной функции этапа по точному хешу результата. Запрос человеку создаётся только после завершения " +
			"предшествующих автоматических гейтов. Допустимые действия: approve, reject и возвраты по маршрутам шаблона; " +
			"роль — функция подтверждающего этапа, кворум — любой один. Каждое решение (одобрение, отказ, возврат) — отдельная " +
			"запись попытки гейта и событие с автором, ролью и хешем.",
		Params: []workflow.GateParam{
			{Name: "on_reject", Type: workflow.GateParamEnum, Enum: []string{OnRejectStop, OnRejectStay},
				Description: "Что делает отказ (reject): stop — завершает задачу (по умолчанию); stay — оставляет задачу на этапе для новой версии."},
		},
	})
}
