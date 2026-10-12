package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/tokenbudget"
)

type staticLedger struct {
	usage  tokenbudget.Usage
	starts []time.Time
}

func (l *staticLedger) TokensBetween(start, _ time.Time) (tokenbudget.Usage, error) {
	l.starts = append(l.starts, start)
	return l.usage, nil
}

func budgetRuntime() *scriptedRuntime {
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	rt.usagePer = map[string]*runtime.Usage{
		"analyst":  {Attested: true, TokensInput: 5, TokensOutput: 2},
		"reviewer": {Attested: true, TokensInput: 7, TokensOutput: 3},
		"deployer": {Attested: true, TokensInput: 1, TokensOutput: 1},
	}
	return rt
}

func budgetConfig(budget *config.BudgetConfig) *config.Config {
	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}, config.AgentConfig{Name: "deployer"})
	cfg.Budget = budget
	return cfg
}

func budgetEvents(t *testing.T, dir, runID string) []evidence.Event {
	t.Helper()
	events, err := evidence.VerifyEventLog(filepath.Join(dir, ".ai-team", "runs", runID, "events.jsonl"), runID)
	if err != nil {
		t.Fatal(err)
	}
	var matched []evidence.Event
	for _, event := range events {
		if event.Type == tokenbudget.EventBudgetExceeded {
			matched = append(matched, event)
		}
	}
	return matched
}

func assertBudgetPause(t *testing.T, dir string, result RunResult, err error) {
	t.Helper()
	if !errors.Is(err, ErrTokenBudgetExceeded) {
		t.Fatalf("expected token budget stop, got result=%+v err=%v", result, err)
	}
	store, storeErr := lifecycle.NewStore(dir)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	state, loadErr := store.Load(result.RunID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	// Задача не продолжается сама: только resumable-пауза до решения человека.
	if state.Phase != lifecycle.PhaseResumable {
		t.Fatalf("budget stop must leave a resumable checkpoint, got %s", state.Phase)
	}
	events, verifyErr := evidence.VerifyEventLog(filepath.Join(dir, ".ai-team", "runs", result.RunID, "events.jsonl"), result.RunID)
	if verifyErr != nil {
		t.Fatal(verifyErr)
	}
	last := events[len(events)-1]
	if last.Type != "run_paused" || last.Data["status"] != "stopped" || last.Data["reason"] != "budget_exceeded" {
		t.Fatalf("budget stop must end with a stopped run_paused event: %+v", last)
	}
}

func TestTaskTokenBudgetBoundaries(t *testing.T) {
	// После analyst задача израсходовала 7 токенов.
	cases := []struct {
		name         string
		limit        int64
		reviewerRuns bool
	}{
		{"one below limit", 8, true},
		{"exactly at limit", 7, false},
		{"one above limit", 6, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := env(t)
			rt := budgetRuntime()
			p := New(budgetConfig(&config.BudgetConfig{MaxTaskTokens: tc.limit}), testRegistry(),
				WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
			result, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "budget", TargetDir: dir, ApproveGates: true})
			if got := slices.Contains(rt.executed, "reviewer"); got != tc.reviewerRuns {
				t.Fatalf("reviewer executed=%t want %t (executed %v, err %v)", got, tc.reviewerRuns, rt.executed, err)
			}
			if tc.reviewerRuns {
				return
			}
			assertBudgetPause(t, dir, result, err)
			events := budgetEvents(t, dir, result.RunID)
			if len(events) != 1 {
				t.Fatalf("budget_exceeded events = %+v", events)
			}
			data := events[0].Data
			if events[0].Stage != "reviewer" || data["scope"] != tokenbudget.ScopeTask || data["tokens_used"] != float64(7) ||
				data["limit"] != float64(tc.limit) || data["usage_unknown"] != false || data["severity"] != "warning" {
				t.Fatalf("budget_exceeded payload = %+v stage=%s", data, events[0].Stage)
			}
		})
	}
}

// Явный resume человеком после остановки выдаёт задаче следующий бюджет того
// же размера, отсчитанный от расхода в момент остановки, — не бесконечный.
func TestTaskTokenBudgetResumeGrantsOneMoreAllowance(t *testing.T) {
	dir := env(t)
	rt := budgetRuntime()
	p := New(budgetConfig(&config.BudgetConfig{MaxTaskTokens: 7}), testRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "budget", TargetDir: dir, ApproveGates: true})
	assertBudgetPause(t, dir, first, err)

	second, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir, ApproveGates: true})
	if !slices.Contains(rt.executed, "reviewer") {
		t.Fatalf("human resume must allow the next attempt: executed=%v err=%v", rt.executed, err)
	}
	if slices.Contains(rt.executed, "deployer") {
		t.Fatalf("one resume must grant exactly one more allowance: executed=%v", rt.executed)
	}
	assertBudgetPause(t, dir, second, err)
	events := budgetEvents(t, dir, first.RunID)
	if len(events) != 2 || events[1].Data["tokens_used"] != float64(17) || events[1].Data["baseline_tokens"] != float64(7) {
		t.Fatalf("second stop must count from the first stop: %+v", events)
	}
}

func TestTaskTokenBudgetUnknownUsageFailsClosed(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.usage = &runtime.Usage{Attested: false, TokensInput: 1}
	p := New(budgetConfig(&config.BudgetConfig{MaxTaskTokens: 1_000_000}), testRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	result, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "budget", TargetDir: dir, ApproveGates: true})
	assertBudgetPause(t, dir, result, err)
	if events := budgetEvents(t, dir, result.RunID); len(events) != 1 || events[0].Data["usage_unknown"] != true {
		t.Fatalf("unknown usage stop = %+v", events)
	}
}

func TestDailyTokenBudgetBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 12, 23, 59, 59, 0, time.UTC)
	cases := []struct {
		name    string
		usage   tokenbudget.Usage
		stopped bool
	}{
		{"one below limit", tokenbudget.Usage{Tokens: 99}, false},
		{"exactly at limit", tokenbudget.Usage{Tokens: 100}, true},
		{"one above limit", tokenbudget.Usage{Tokens: 101}, true},
		{"unknown usage", tokenbudget.Usage{Unknown: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := env(t)
			rt := budgetRuntime()
			ledger := &staticLedger{usage: tc.usage}
			p := New(budgetConfig(&config.BudgetConfig{MaxDailyTokens: 100}), testRegistry(),
				WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}),
				WithTokenLedger(ledger), WithClock(func() time.Time { return now }))
			result, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "budget", TargetDir: dir, ApproveGates: true})
			if len(ledger.starts) == 0 || !ledger.starts[0].Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)) {
				t.Fatalf("daily window must be the UTC day of the clock: %v", ledger.starts)
			}
			if !tc.stopped {
				if err != nil || !slices.Contains(rt.executed, "analyst") {
					t.Fatalf("run below daily budget: executed=%v err=%v", rt.executed, err)
				}
				return
			}
			if len(rt.executed) != 0 {
				t.Fatalf("no agent may start over the daily budget: %v", rt.executed)
			}
			assertBudgetPause(t, dir, result, err)
			events := budgetEvents(t, dir, result.RunID)
			if len(events) != 1 || events[0].Data["scope"] != tokenbudget.ScopeDaily || events[0].Data["day"] != "2026-10-12" {
				t.Fatalf("daily budget_exceeded = %+v", events)
			}
		})
	}
}

// Дневной расход пересчитывается из evidence на диске: новый процесс
// (рестарт) видит расход уже завершённой задачи, а следующие сутки UTC — нет.
func TestDailyTokenBudgetSurvivesRestart(t *testing.T) {
	dir := env(t)
	rt := budgetRuntime()
	cfg := budgetConfig(&config.BudgetConfig{MaxDailyTokens: 7})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "budget", TargetDir: dir, ApproveGates: true})
	assertBudgetPause(t, dir, first, err)
	manifestPath := filepath.Join(dir, ".ai-team", "runs", first.RunID, "attempts")
	entries, err := os.ReadDir(manifestPath)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one analyst attempt: %v %v", entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(manifestPath, entries[0].Name(), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest evidence.AttemptManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	spentAt := manifest.FinishedAt

	restarted := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}),
		WithClock(func() time.Time { return spentAt }))
	executedBefore := len(rt.executed)
	resumed, err := restarted.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir, ApproveGates: true})
	assertBudgetPause(t, dir, resumed, err)
	if len(rt.executed) != executedBefore {
		t.Fatalf("restart must not reset the daily counter: executed %v", rt.executed[executedBefore:])
	}

	_, dayEnd := tokenbudget.DayWindow(spentAt)
	nextDay := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}),
		WithClock(func() time.Time { return dayEnd }))
	if _, err := nextDay.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir, ApproveGates: true}); !slices.Contains(rt.executed[executedBefore:], "reviewer") {
		t.Fatalf("next UTC day must allow the resumed task: executed=%v err=%v", rt.executed, err)
	}
}

// Worker с удалённым evidence не видит полного дневного расхода: без явно
// переданного ledger дневной бюджет неизвестен и задача останавливается.
func TestDailyTokenBudgetWithoutLocalLedgerFailsClosed(t *testing.T) {
	p := New(budgetConfig(&config.BudgetConfig{MaxDailyTokens: 100}), testRegistry(),
		WithAttemptManifestStore(evidence.FilesystemAttemptManifestSource(), nil))
	rs := &runState{p: p}
	if ledger := rs.dailyLedger(); ledger != nil {
		t.Fatalf("remote evidence must not fall back to a partial local ledger: %#v", ledger)
	}
}

// Агент-проверяющий вызывает модель: перед ним бюджет проверяется так же,
// как перед агентом этапа, а после resume проверка повторяется.
func TestCheckerAgentStopsOnExhaustedTaskBudget(t *testing.T) {
	cases := []struct {
		name       string
		limit      int64
		checkerRun bool
	}{
		{"one below limit", 8, true},
		{"exactly at limit", 7, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := env(t)
			rt := newScripted()
			rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
			rt.content["verifier"] = map[string]string{"verification": "# Отчёт проверки\n\n**Verdict:** PASSED\n"}
			rt.usagePer = map[string]*runtime.Usage{
				"tech":     {Attested: true, TokensInput: 5, TokensOutput: 2},
				"verifier": {Attested: true, TokensInput: 3, TokensOutput: 1},
			}
			cfg := stageCheckConfig(&config.TemplateCheck{Kind: "agent", Agent: "verifier"})
			cfg.Budget = &config.BudgetConfig{MaxTaskTokens: tc.limit}
			p := New(cfg, stageCheckRegistry(true), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
			result, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "budget", TargetDir: dir, ApproveGates: true})
			if got := slices.Contains(rt.executed, "verifier"); got != tc.checkerRun {
				t.Fatalf("checker executed=%t want %t (executed %v err %v)", got, tc.checkerRun, rt.executed, err)
			}
			runDir := filepath.Join(dir, ".ai-team", "runs", result.RunID)
			if tc.checkerRun {
				if err != nil {
					t.Fatalf("run below budget: %v", err)
				}
				usage, usageErr := tokenbudget.RunCheckerUsage(runDir)
				if usageErr != nil || usage != (tokenbudget.Usage{Tokens: 4}) {
					t.Fatalf("checker usage must be durable: %+v err=%v", usage, usageErr)
				}
				return
			}
			assertBudgetPause(t, dir, result, err)
			if strings.Contains(readEvents(t, dir), `"type":"check_started"`) {
				t.Fatal("budget stop must precede check_started so the check reruns after resume")
			}
			resumed, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: result.RunID, TargetDir: dir, ApproveGates: true})
			// Этап съел весь бюджет; resume не повторяет оплаченную попытку, а
			// запускает только проверку на выданный человеком бюджет.
			if err != nil || rt.calls["verifier"] != 1 || rt.calls["tech"] != 1 {
				t.Fatalf("resume must run only the pending check: result=%+v err=%v calls=%v", resumed, err, rt.calls)
			}
			usage, usageErr := tokenbudget.RunCheckerUsage(runDir)
			if usageErr != nil || usage != (tokenbudget.Usage{Tokens: 4}) {
				t.Fatalf("checker usage after resume = %+v err=%v", usage, usageErr)
			}
		})
	}
}

// Пример из ревью: spec-checker в grill тратит токены каждым раундом. Его
// расход входит в бюджет задачи, поэтому следующий раунд после исчерпания
// не вызывает модель — и после рестарта процесса тоже.
func TestGrillCheckerUsageCountsTowardTaskBudget(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Спецификация\n\nКритерии не проверяемы.\n"}
	rt.content["spec-checker"] = map[string]string{"spec-check": "**Question:** Как измерить критерий успеха?\n"}
	rt.usagePer = map[string]*runtime.Usage{
		"tech":         {Attested: true, TokensInput: 2},
		"spec-checker": {Attested: true, TokensInput: 10},
	}
	cfg := grillStageCheckConfig(3)
	cfg.Budget = &config.BudgetConfig{MaxTaskTokens: 12}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := runGrillPipeline(t, newGrillPipeline(cfg, rt, store), dir, ""); !errors.Is(err, ErrUserStopped) {
		t.Fatalf("first grill round should wait for an answer: %v", err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	pending := grillApproval(t, store, runID)
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: approval.GrillActionAnswer,
		Comment: "Порог — рост конверсии на 20%.", SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	// Новый Pipeline — модель рестарта: расход проверяющего читается с диска.
	err = runGrillPipeline(t, newGrillPipeline(cfg, rt, store), dir, runID)
	if !errors.Is(err, ErrTokenBudgetExceeded) || rt.calls["spec-checker"] != 1 {
		t.Fatalf("second grill round must stop on the budget: err=%v checker calls=%d", err, rt.calls["spec-checker"])
	}
	events := budgetEvents(t, dir, runID)
	if len(events) != 1 || events[0].Data["tokens_used"] != float64(12) {
		t.Fatalf("budget_exceeded must include checker usage: %+v", events)
	}
}

// Доставка и продолжение после delivery-плана модель не вызывают и бюджетом
// не останавливаются.
func TestDeliveryStageIgnoresExhaustedBudget(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		def   string
		want  bool
	}{
		{"delivery kind", "shipper", "name: shipper\nruntime: delivery\nkind: delivery\nmutation: external\noutputs:\n  plan: '{feature}/delivery-plan.json'\n", false},
		{"model agent", "shipper", "name: shipper\nruntime: agentcli\nprompt_file: prompt.md\nmutation: none\noutputs: {}\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := env(t)
			cfg := &config.Config{Budget: &config.BudgetConfig{MaxTaskTokens: 1}}
			p := New(cfg, agent.NewFS(fstest.MapFS{
				tc.agent + "/def.yaml":  def(tc.def),
				tc.agent + "/prompt.md": def("test"),
			}))
			if definition, loadErr := p.loadStageDefinition(tc.agent); loadErr != nil || definition == nil {
				t.Fatalf("fixture definition must load, otherwise the test is vacuous: %v", loadErr)
			}
			store := &appendOnlyEvidence{runDir: filepath.Join(dir, ".ai-team", "runs", "run-budget")}
			rs := &runState{p: p, runID: "run-budget", evidence: store, budgetConfig: cfg.Budget,
				usageTotal: runtime.Usage{Attested: true, TokensInput: 5}}
			got := rs.enforceStageTokenBudget(tc.agent)
			if (got != nil) != tc.want || (len(store.events) == 1) != tc.want {
				t.Fatalf("enforceStageTokenBudget = %v events=%v, want stop=%t", got, store.events, tc.want)
			}
		})
	}
}

// appendOnlyEvidence — минимальный EvidenceStore для проверки решения бюджета
// без полного run: остальные методы не вызываются.
type appendOnlyEvidence struct {
	EvidenceStore
	runDir string
	events []evidence.Event
}

func (e *appendOnlyEvidence) RunDir() string { return e.runDir }

func (e *appendOnlyEvidence) Append(event evidence.Event) error {
	e.events = append(e.events, event)
	return nil
}

// Процесс умер посреди вызова проверяющего: маркер начала без итоговой записи.
// Resume не считает такой расход нулевым — бюджет задачи становится
// неизвестным, задача стоит, а дневной итог видит неизвестную запись.
func TestInterruptedCheckerUsageIsUnknownOnResume(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
	rt.content["verifier"] = map[string]string{"verification": "# Отчёт проверки\n\n**Verdict:** PASSED\n"}
	rt.usagePer = map[string]*runtime.Usage{
		"tech":     {Attested: true, TokensInput: 7},
		"verifier": {Attested: true, TokensInput: 1},
	}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "agent", Agent: "verifier"})
	cfg.Budget = &config.BudgetConfig{MaxTaskTokens: 7}
	p := New(cfg, stageCheckRegistry(true), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "budget", TargetDir: dir, ApproveGates: true})
	assertBudgetPause(t, dir, first, err)
	runDir := filepath.Join(dir, ".ai-team", "runs", first.RunID)
	crashedAt := time.Now().UTC()
	if _, err := tokenbudget.BeginCheckerUsage(runDir, "crashed-verifier-r00", tokenbudget.CheckerUsageRecord{
		RunID: first.RunID, Stage: "tech", Checker: "verifier", StartedAt: crashedAt,
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir, ApproveGates: true})
	assertBudgetPause(t, dir, resumed, err)
	if rt.calls["verifier"] != 0 {
		t.Fatalf("checker must not run with unknown usage: %v", rt.calls)
	}
	events := budgetEvents(t, dir, first.RunID)
	if last := events[len(events)-1]; last.Data["usage_unknown"] != true {
		t.Fatalf("interrupted checker must make usage unknown: %+v", last)
	}
	start, end := tokenbudget.DayWindow(crashedAt)
	daily, err := tokenbudget.FilesystemLedger{RunsRoot: filepath.Dir(runDir)}.TokensBetween(start, end)
	if err != nil || !daily.Unknown {
		t.Fatalf("daily ledger must see the reconciled interrupted checker as unknown: %+v err=%v", daily, err)
	}
}
