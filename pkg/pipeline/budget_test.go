package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

func TestRun_BudgetAttemptsCap(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}

	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}, config.AgentConfig{Name: "deployer"})
	cfg.Budget = &config.BudgetConfig{MaxAttempts: 2}

	err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{})
	if err == nil {
		t.Fatal("ожидалась бюджет-ошибка при переборе total attempts")
	}
	if !strings.Contains(err.Error(), "run budget: превышен лимит попыток max_attempts=2") {
		t.Fatalf("ошибка бюджета не распознана: %v", err)
	}
	// analyst запущен, затем reviewer — попытка 3 должна не выполнить deployer.
	if len(rt.executed) != 2 || rt.executed[1] != "reviewer" {
		t.Fatalf("executed после кэпа: %v", rt.executed)
	}
}

func TestRun_BudgetWallTime(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.waitCtx["analyst"] = true // блокируется до отмены ctx

	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	cfg.Budget = &config.BudgetConfig{MaxWallTime: "200ms"}

	start := time.Now()
	err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{})
	if err == nil {
		t.Fatal("ожидалась бюджет-ошибка по wall-time")
	}
	if !strings.Contains(err.Error(), "run budget: превышен max_execution_time 200ms") {
		t.Fatalf("ошибка бюджета не распознана: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("budget не остановил run вовремя: %v", elapsed)
	}
}

// Конфиг БЕЗ секции budget: таймер wall-time всё равно вооружается. Проверка
// через deadline, который видит стадия: per-agent timeout заведомо длиннее
// дефолтного бюджета, поэтому наблюдаемый дедлайн может прийти только от
// run-бюджета. Без вооружённого таймера дедлайн был бы 48h (QS-09, #143).
func TestRun_DefaultWallTimeArmedWithoutBudgetSection(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}

	cfg := cfgFor(config.AgentConfig{Name: "analyst", Timeout: "48h"}, config.AgentConfig{Name: "reviewer", Timeout: "48h"})
	if cfg.Budget != nil {
		t.Fatal("тест требует конфиг без секции budget")
	}

	start := time.Now()
	if err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{}); err != nil {
		t.Fatalf("ожидался успех, got: %v", err)
	}
	if !rt.firstHasDeadline {
		t.Fatal("стадия исполнена без deadline: run не ограничен по времени вообще")
	}
	assertDeadlineNear(t, rt.firstDeadline.Sub(start), config.DefaultBudgetMaxWallTimeDuration)
}

// Стадия без stage_timeout и без per-agent timeout тоже ограничена: дефолт
// применяется на уровне кода, а не только в сгенерированном `init` конфиге.
func TestRun_DefaultStageTimeoutArmedWithoutConfiguredValue(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}

	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	if cfg.StageTimeout != "" {
		t.Fatal("тест требует конфиг без stage_timeout")
	}

	start := time.Now()
	if err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{}); err != nil {
		t.Fatalf("ожидался успех, got: %v", err)
	}
	if !rt.firstHasDeadline {
		t.Fatal("стадия исполнена без deadline")
	}
	assertDeadlineNear(t, rt.firstDeadline.Sub(start), config.DefaultStageTimeout)
}

// assertDeadlineNear сверяет наблюдаемый дедлайн с ожидаемым бюджетом.
// Допуск нужен с обеих сторон: точка отсчёта теста и момент, когда пайплайн
// вооружает таймер, расходятся на время подготовки run'а.
func assertDeadlineNear(t *testing.T, observed, want time.Duration) {
	t.Helper()
	if observed < want-time.Minute || observed > want+time.Minute {
		t.Fatalf("deadline стадии %v, ожидался бюджет ~%v", observed, want)
	}
}

// TestRun_AttestedUsagePersisted проверяет, что usage принимается ТОЛЬКО от
// attested источника и попадает в usage.json envelope (P1-7).
func TestRun_AttestedUsagePersisted(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	rt.usagePer = map[string]*runtime.Usage{
		"analyst":  {Attested: true, TokensInput: 111, TokensOutput: 22, CostUSD: 1.25},
		"reviewer": {Attested: true, TokensInput: 10, TokensOutput: 5, CostUSD: 0.1},
	}

	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("ожидался успех, got: %v", err)
	}

	runDir := onlyRunDir(t, dir)
	raw, err := os.ReadFile(filepath.Join(runDir, "usage.json"))
	if err != nil {
		t.Fatalf("usage.json: %v", err)
	}
	var envelope metrics.UsageEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("usage.json не парсится: %v", err)
	}
	if !envelope.UsageReported || envelope.TokensUnknown {
		t.Fatalf("ожидали attested usage: %+v", envelope)
	}
	if envelope.TokensInput != 121 || envelope.TokensOutput != 27 || envelope.CostUSD != 0 {
		t.Fatalf("usage-значения: %+v", envelope)
	}
}

// TestRun_UnattestedUsageStaysUnknown: адаптер БЕЗ attested usage не должен
// влиять на envelope (остаётся tokens_unknown=true).
func TestRun_UnattestedUsageStaysUnknown(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.usage = &runtime.Usage{Attested: false, TokensInput: 9}

	cfg := cfgFor(config.AgentConfig{Name: "analyst"})
	err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("ожидался успех, got: %v", err)
	}

	runDir := onlyRunDir(t, dir)
	raw, err := os.ReadFile(filepath.Join(runDir, "usage.json"))
	if err != nil {
		t.Fatalf("usage.json: %v", err)
	}
	var envelope metrics.UsageEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("usage.json не парсится: %v", err)
	}
	if !envelope.TokensUnknown || envelope.UsageReported {
		t.Fatalf("unattested usage не должен маркироваться reported: %+v", envelope)
	}
}

func TestRun_AttemptUsageSurvivesTwoPausesWithoutDoubleCounting(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	rt.usagePer = map[string]*runtime.Usage{
		"analyst":  {Attested: true, TokensInput: 5, TokensOutput: 2},
		"reviewer": {Attested: true, TokensInput: 7, TokensOutput: 3},
		"deployer": {Attested: true, TokensInput: 11, TokensOutput: 4},
	}
	cfg := cfgForGraph(nil,
		config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}, config.AgentConfig{Name: "deployer"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "durable usage", TargetDir: dir})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("first run should pause after analyst: result=%+v err=%v", first, err)
	}
	approve := func(approvalID string) time.Time {
		t.Helper()
		store, err := approval.NewStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		pending, err := store.Load(first.RunID, approvalID)
		if err != nil {
			t.Fatal(err)
		}
		decidedAt := time.Now().UTC()
		decided, err := store.Decide(first.RunID, approvalID, approval.Decision{
			ActorID: "operator-1", ActorRole: "operator", Action: "approve",
			SubjectHash: pending.SubjectHash, DecidedAt: decidedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		if decided.ResolvedAt.IsZero() || !decided.ResolvedAt.Equal(decidedAt) {
			t.Fatalf("approval did not preserve Decision.DecidedAt: %+v", decided)
		}
		time.Sleep(20 * time.Millisecond)
		return decidedAt
	}
	firstDecisionAt := approve(required.ApprovalID)
	reportsDir := filepath.Join(dir, ".ai-team", "reports", "feat")
	if err := os.RemoveAll(reportsDir); err != nil {
		t.Fatalf("remove derived report projection before first resume: %v", err)
	}
	second, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if !errors.As(err, &required) {
		t.Fatalf("second execution should pause after reviewer: result=%+v err=%v", second, err)
	}
	secondDecisionAt := approve(required.ApprovalID)
	if err := os.RemoveAll(reportsDir); err != nil {
		t.Fatalf("remove derived report projection before second resume: %v", err)
	}
	third, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil || third.Outcome != "completed" {
		t.Fatalf("third execution should finish: result=%+v err=%v", third, err)
	}

	runDir := filepath.Join(dir, ".ai-team", "runs", first.RunID)
	raw, err := os.ReadFile(filepath.Join(runDir, "usage.json"))
	if err != nil {
		t.Fatalf("usage.json: %v", err)
	}
	var envelope metrics.UsageEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.TokensUnknown || !envelope.UsageReported || envelope.TokensInput != 23 || envelope.TokensOutput != 9 {
		t.Fatalf("two-pause totals were lost or double-counted: %+v", envelope)
	}
	var manifestStartedAt time.Time
	manifestRaw, err := os.ReadFile(filepath.Join(runDir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest evidence.RunManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifestStartedAt = manifest.StartedAt
	if !envelope.StartedAt.Equal(manifestStartedAt) || envelope.TotalDurationMS < 30 {
		t.Fatalf("task duration must start at original creation and include pauses: envelope=%+v manifest=%s", envelope, manifestStartedAt)
	}
	attemptEntries, err := os.ReadDir(filepath.Join(runDir, "attempts"))
	if err != nil || len(attemptEntries) != 3 {
		t.Fatalf("expected three durable attempts: entries=%d err=%v", len(attemptEntries), err)
	}
	var inputTotal, outputTotal int64
	for _, entry := range attemptEntries {
		data, err := os.ReadFile(filepath.Join(runDir, "attempts", entry.Name(), "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var attempt evidence.AttemptManifest
		if err := json.Unmarshal(data, &attempt); err != nil {
			t.Fatal(err)
		}
		if attempt.Usage == nil || !attempt.Usage.Attested {
			t.Fatalf("attempt %s did not persist attested usage: %+v", entry.Name(), attempt.Usage)
		}
		stageHTML, err := os.ReadFile(filepath.Join(reportsDir, "attempts", entry.Name(), "index.html"))
		if err != nil {
			t.Fatalf("restored stage report for %s: %v", entry.Name(), err)
		}
		for _, artifact := range map[string][]string{
			"analyst":  {"task.md", "proposal.md"},
			"reviewer": {"proposal.md", "review.md"},
			"deployer": {"review.md"},
		}[attempt.Stage] {
			if !strings.Contains(string(stageHTML), artifact) {
				t.Fatalf("restored report for %s is missing manifest artifact %s", attempt.Stage, artifact)
			}
		}
		finalHTML, err := os.ReadFile(filepath.Join(reportsDir, "index.html"))
		if err != nil {
			t.Fatalf("final report: %v", err)
		}
		if !strings.Contains(string(finalHTML), entry.Name()) {
			t.Fatalf("final report does not link restored attempt %s", entry.Name())
		}
		inputTotal += attempt.Usage.TokensInput
		outputTotal += attempt.Usage.TokensOutput
	}
	if inputTotal != envelope.TokensInput || outputTotal != envelope.TokensOutput {
		t.Fatalf("manifest sum does not equal run envelope: attempts=%d/%d envelope=%d/%d", inputTotal, outputTotal, envelope.TokensInput, envelope.TokensOutput)
	}
	events, err := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var decisionEvents []time.Time
	for _, event := range events {
		if event.Type == "approval_decided" {
			decisionEvents = append(decisionEvents, event.Timestamp)
		}
	}
	if len(decisionEvents) != 2 || !decisionEvents[0].Equal(firstDecisionAt) || !decisionEvents[1].Equal(secondDecisionAt) {
		t.Fatalf("approval event times must match human decisions, got %v want %v, %v", decisionEvents, firstDecisionAt, secondDecisionAt)
	}
}
