// Package autopickup содержит политику автоматического подбора задач из
// бэклога: что и когда агенту можно взять. Хранение очереди и запуск задачи
// остаются за адаптерами (web store и контроллер); здесь — чистое решение,
// которое проверяется таблицами без базы и часов.
package autopickup

import (
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/tokenbudget"
)

// Статусы элемента бэклога. Агент берёт только StatusReady — его ставит
// человек. Любое другое значение, включая неизвестное, автоподбор не трогает.
const (
	// StatusDraft — черновик идеи. Хранится в бэклоге, не запускается.
	StatusDraft = "draft"
	// StatusReady — человек явно разрешил автоподбор.
	StatusReady = "ready"
	// StatusStarting — подборщик захватил элемент и запускает задачу.
	StatusStarting = "starting"
	// StatusStarted — задача создана, run_id связан с элементом.
	StatusStarted = "started"
	// StatusFailed — запуск не удался; вернуть в очередь может только человек.
	StatusFailed = "failed"
	// StatusDiscarded — идея отклонена.
	StatusDiscarded = "discarded"
)

// HumanTransitionAllowed — переходы, которые человек может сделать через API.
// Статусы starting/started принадлежат подборщику и вручную не ставятся.
func HumanTransitionAllowed(from, to string) bool {
	switch to {
	case StatusReady:
		return from == StatusDraft || from == StatusFailed
	case StatusDraft:
		return from == StatusReady || from == StatusFailed
	case StatusDiscarded:
		return from == StatusDraft || from == StatusReady || from == StatusFailed
	default:
		return false
	}
}

// Причины, по которым подбор не состоялся. Попадают в событие
// auto_pickup_deferred.
const (
	ReasonDisabled            = "disabled"
	ReasonBudgetNotConfigured = "budget_not_configured"
	ReasonNoReadyItems        = "no_ready_items"
	ReasonParallelLimit       = "parallel_limit"
	ReasonDailyBudget         = "daily_budget"
)

// Типы событий подборщика.
const (
	EventDeferred = "auto_pickup_deferred"
	EventStarted  = "auto_pickup_started"
	EventFailed   = "auto_pickup_failed"
)

// Policy — действующие лимиты автоподбора на инстансе.
type Policy struct {
	Enabled          bool
	MaxParallelTasks int
	MaxTaskTokens    int64
	MaxDailyTokens   int64
}

// PolicyFromConfig собирает политику из конфигурации проекта. Отсутствие
// секции auto_pickup — подбор выключен.
func PolicyFromConfig(cfg *config.Config) Policy {
	if cfg == nil || cfg.AutoPickup == nil {
		return Policy{}
	}
	policy := Policy{
		Enabled:          cfg.AutoPickup.Enabled,
		MaxParallelTasks: cfg.AutoPickup.EffectiveMaxParallelTasks(),
	}
	if cfg.Budget != nil {
		policy.MaxTaskTokens = cfg.Budget.MaxTaskTokens
		policy.MaxDailyTokens = cfg.Budget.MaxDailyTokens
	}
	return policy
}

// Snapshot — состояние инстанса на момент решения, пересчитанное из
// долговечных источников.
type Snapshot struct {
	ActiveTasks int
	DailyUsage  tokenbudget.Usage
	HasReady    bool
}

// Decision — итог одного шага подборщика. Pick=false всегда несёт причину.
type Decision struct {
	Pick   bool
	Reason string
}

// Decide применяет лимиты в фиксированном порядке. Все проверки fail-closed:
// неполная политика или неизвестный расход запрещают подбор.
func Decide(policy Policy, snapshot Snapshot) Decision {
	if !policy.Enabled {
		return Decision{Reason: ReasonDisabled}
	}
	if policy.MaxTaskTokens <= 0 || policy.MaxDailyTokens <= 0 || policy.MaxParallelTasks <= 0 {
		return Decision{Reason: ReasonBudgetNotConfigured}
	}
	if !snapshot.HasReady {
		return Decision{Reason: ReasonNoReadyItems}
	}
	if snapshot.ActiveTasks < 0 || snapshot.ActiveTasks >= policy.MaxParallelTasks {
		return Decision{Reason: ReasonParallelLimit}
	}
	if tokenbudget.Check(tokenbudget.ScopeDaily, snapshot.DailyUsage, policy.MaxDailyTokens) != nil {
		return Decision{Reason: ReasonDailyBudget}
	}
	return Decision{Pick: true}
}
