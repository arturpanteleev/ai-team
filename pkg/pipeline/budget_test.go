package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
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
	if !strings.Contains(err.Error(), "run budget: превышен max_wall_time 200ms") {
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
	if envelope.TokensInput != 121 || envelope.TokensOutput != 27 || envelope.CostUSD != 1.35 {
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
