package pipeline

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/tokenbudget"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// ErrTokenBudgetExceeded останавливает задачу перед модельной попыткой, когда
// исчерпан бюджет токенов задачи или суток. Run уходит в resumable-паузу:
// продолжить его может только явный resume человеком, и при resume бюджет
// проверяется заново.
var ErrTokenBudgetExceeded = errors.New("бюджет токенов исчерпан")

// TokenBudgetError несёт подробности исчерпанного бюджета.
type TokenBudgetError struct {
	Exceeded tokenbudget.Exceeded
}

func (e *TokenBudgetError) Error() string { return e.Exceeded.Error() }

func (e *TokenBudgetError) Unwrap() error { return ErrTokenBudgetExceeded }

// pauseReasonBudgetExceeded — причина паузы в событии run_paused.
const pauseReasonBudgetExceeded = "budget_exceeded"

// WithTokenLedger подменяет источник дневного расхода токенов. По умолчанию
// локальный pipeline пересчитывает его по attempt-манифестам target; worker
// с удалённым evidence обязан передать ledger явно, иначе дневной бюджет
// считается неизвестным и задача останавливается (fail-closed).
func WithTokenLedger(ledger tokenbudget.Ledger) Option {
	return func(p *Pipeline) { p.tokenLedger = ledger }
}

// WithClock подменяет часы проверок бюджета (тесты, граница суток UTC).
func WithClock(now func() time.Time) Option {
	return func(p *Pipeline) {
		if now != nil {
			p.now = now
		}
	}
}

func (p *Pipeline) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// dailyLedger возвращает источник дневного расхода или nil, если локальное
// evidence не является полным источником (worker с удалённым evidence).
func (rs *runState) dailyLedger() tokenbudget.Ledger {
	if rs.p.tokenLedger != nil {
		return rs.p.tokenLedger
	}
	if rs.p.attemptManifestSource != nil || rs.p.eventLogSource != nil {
		return nil
	}
	return tokenbudget.FilesystemLedger{RunsRoot: filepath.Dir(rs.evidence.RunDir())}
}

// taskBudgetBaseline — расход задачи на момент последней остановки по
// бюджету задачи. Run продолжается после такой остановки только явным resume
// человеком; это решение выдаёт задаче следующий бюджет того же размера, а не
// бесконечный. Значение берётся из долговечного evidence, а не из памяти.
func (rs *runState) taskBudgetBaseline() (int64, error) {
	events, found, err := rs.readEvidenceEvents()
	if err != nil {
		return 0, fmt.Errorf("read budget evidence: %w", err)
	}
	if !found {
		return 0, nil
	}
	var baseline int64
	for _, event := range events {
		if event.Type != tokenbudget.EventBudgetExceeded || event.Data["scope"] != tokenbudget.ScopeTask {
			continue
		}
		switch used := event.Data["tokens_used"].(type) {
		case float64:
			baseline = int64(used)
		case int64:
			baseline = used
		case int:
			baseline = int64(used)
		}
	}
	return baseline, nil
}

// enforceTokenBudget проверяет бюджеты перед модельной попыткой stage. При
// исчерпании пишет budget_exceeded в evidence и возвращает TokenBudgetError.
func (rs *runState) enforceTokenBudget(stage string) error {
	return rs.enforceTokenBudgetFor(stage, "")
}

// enforceCheckTokenBudget — проверка бюджета перед проверяющим уже
// завершённой попытки этапа. Событие привязано к попытке (phase=check):
// resume по нему запускает только проверку, не повторяя оплаченную попытку.
func (rs *runState) enforceCheckTokenBudget(stage, attemptID string) error {
	return rs.enforceTokenBudgetFor(stage, attemptID)
}

func (rs *runState) enforceTokenBudgetFor(stage, checkAttemptID string) error {
	budget := rs.budgetConfig
	if budget == nil || (budget.MaxTaskTokens <= 0 && budget.MaxDailyTokens <= 0) {
		return nil
	}
	now := rs.p.clock().UTC()
	data := map[string]any{"severity": "warning"}
	var exceeded *tokenbudget.Exceeded
	if budget.MaxTaskTokens > 0 {
		baseline, err := rs.taskBudgetBaseline()
		if err != nil {
			return err
		}
		used := tokenbudget.Usage{Tokens: rs.usageTotal.TokensInput, Unknown: rs.usageUnknown}.
			Add(tokenbudget.Usage{Tokens: rs.usageTotal.TokensOutput}).
			Add(rs.checkerUsage)
		allowance := used
		if baseline > 0 && baseline <= used.Tokens {
			allowance.Tokens = used.Tokens - baseline
		}
		if over := tokenbudget.Check(tokenbudget.ScopeTask, allowance, budget.MaxTaskTokens); over != nil {
			exceeded = over
			// Событие хранит полный расход задачи: следующий resume
			// отсчитывает новый бюджет от этого значения.
			exceeded.Used = used.Tokens
			data["baseline_tokens"] = baseline
		}
	}
	if exceeded == nil && budget.MaxDailyTokens > 0 {
		usage := tokenbudget.Usage{Unknown: true}
		if ledger := rs.dailyLedger(); ledger != nil {
			start, end := tokenbudget.DayWindow(now)
			counted, err := ledger.TokensBetween(start, end)
			if err != nil {
				return fmt.Errorf("daily token ledger: %w", err)
			}
			usage = counted
		}
		if over := tokenbudget.Check(tokenbudget.ScopeDaily, usage, budget.MaxDailyTokens); over != nil {
			exceeded = over
			data["day"] = tokenbudget.Day(now)
		}
	}
	if exceeded == nil {
		return nil
	}
	data["scope"] = exceeded.Scope
	data["tokens_used"] = exceeded.Used
	data["limit"] = exceeded.Limit
	data["usage_unknown"] = exceeded.Unknown
	data["next_stage"] = stage
	if checkAttemptID != "" {
		data["phase"] = budgetPhaseCheck
		data["check_attempt_id"] = checkAttemptID
	}
	event := evidence.Event{Type: tokenbudget.EventBudgetExceeded, Stage: stage, Timestamp: now, Data: data}
	if err := rs.evidence.Append(event); err != nil {
		return errors.Join(&TokenBudgetError{Exceeded: *exceeded}, fmt.Errorf("record budget_exceeded: %w", err))
	}
	rs.recordTypedEvent(event)
	return &TokenBudgetError{Exceeded: *exceeded}
}

// enforceStageTokenBudget применяет бюджет только к попыткам, которые вызовут
// модель: доставка и продолжение после ожидания delivery-плана токенов не
// расходуют и бюджетом не останавливаются. Ошибку загрузки определения
// возвращает сам runStage, поэтому здесь она не маскируется отказом бюджета.
func (rs *runState) enforceStageTokenBudget(stage string) error {
	if rs.budgetConfig == nil || (rs.budgetConfig.MaxTaskTokens <= 0 && rs.budgetConfig.MaxDailyTokens <= 0) {
		return nil
	}
	definition, err := rs.p.loadStageDefinition(stage)
	if err != nil || definition == nil {
		return nil
	}
	if definition.Kind == "delivery" {
		return nil
	}
	if continuation, err := rs.deliveryApprovalContinuation(stage, definition); err == nil && continuation {
		return nil
	}
	return rs.enforceTokenBudget(stage)
}

// budgetPhaseCheck помечает остановку перед проверяющим завершённой попытки.
const budgetPhaseCheck = "check"

// beginCheckerUsage пишет маркер начала запуска проверяющего до вызова
// модели: если процесс умрёт посреди вызова, resume увидит маркер без итога
// и сочтёт расход неизвестным, а не нулевым.
func (rs *runState) beginCheckerUsage(stage, checker, attemptID string, round int, startedAt time.Time) (string, error) {
	prefix := fmt.Sprintf("%s-%s-r%02d", attemptID, checker, round)
	return tokenbudget.BeginCheckerUsage(rs.evidence.RunDir(), prefix, tokenbudget.CheckerUsageRecord{
		RunID: rs.runID, Stage: stage, Checker: checker, StartedAt: startedAt.UTC(),
	})
}

// finishCheckerUsage сохраняет расход агента-проверяющего: в счётчик задачи и
// в итоговую запись run, которую видят resume и дневной ledger. Рантайм,
// не сообщивший подтверждённый расход, делает расход неизвестным.
func (rs *runState) finishCheckerUsage(key, stage, checker string, reporter any, startedAt, finishedAt time.Time) error {
	usage := &workflow.AttemptUsage{}
	if source, ok := reporter.(runtime.UsageReporter); ok {
		if reported := source.Usage(); reported != nil && reported.Attested {
			usage = &workflow.AttemptUsage{Attested: true, TokensInput: reported.TokensInput,
				TokensOutput: reported.TokensOutput, CostUSD: reported.CostUSD}
		}
	}
	rs.checkerUsage = rs.checkerUsage.Add(tokenbudget.AttemptUsage(usage))
	return tokenbudget.FinishCheckerUsage(rs.evidence.RunDir(), key, tokenbudget.CheckerUsageRecord{
		RunID: rs.runID, Stage: stage, Checker: checker,
		StartedAt: startedAt.UTC(), FinishedAt: finishedAt.UTC(), Usage: usage,
	})
}

// pendingCheckAfterBudgetStop находит попытку этапа, которая завершилась,
// но чья проверка была остановлена бюджетом. Resume продолжает с проверки
// этой попытки: повтор этапа заново оплачивал бы уже сделанную работу и при
// бюджете не больше одной попытки зацикливался бы.
func (rs *runState) pendingCheckAfterBudgetStop(stage string) (notifier.StageResult, bool, error) {
	events, found, err := rs.readEvidenceEvents()
	if err != nil || !found {
		return notifier.StageResult{}, false, err
	}
	attemptID := ""
	for _, event := range events {
		switch {
		case event.Type == tokenbudget.EventBudgetExceeded && event.Stage == stage && event.Data["phase"] == budgetPhaseCheck:
			attemptID, _ = event.Data["check_attempt_id"].(string)
		case event.Type == "attempt_started" && event.Stage == stage, event.Type == "attempts_invalidated":
			// Новая попытка или откат делают старую остановку неактуальной.
			attemptID = ""
		}
	}
	if attemptID == "" {
		return notifier.StageResult{}, false, nil
	}
	if _, checked, err := rs.replayedStageCheck(stage, attemptID); err != nil {
		return notifier.StageResult{}, false, err
	} else if checked {
		return notifier.StageResult{}, false, nil
	}
	for index := len(rs.results) - 1; index >= 0; index-- {
		result := rs.results[index]
		if result.AttemptID != attemptID {
			continue
		}
		if result.Superseded || result.Status != notifier.StatusPassed || result.State.Outcome != workflow.OutcomePassed {
			return notifier.StageResult{}, false, nil
		}
		return result, true, nil
	}
	return notifier.StageResult{}, false, nil
}
