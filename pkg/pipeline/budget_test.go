package pipeline

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(err.Error(), "run budget: превышен max_wall_time 200ms") {
		t.Fatalf("ошибка бюджета не распознана: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("budget не остановил run вовремя: %v", elapsed)
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

// TestRun_UnparsedUsageIsVisible (QS-20): если адаптер аттестует usage, но
// запись не разобрана, пропуск обязан быть виден — счётчиком в usage.json и
// событием в append-only логе, а не нулём в сумме расхода.
func TestRun_UnparsedUsageIsVisible(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	rt.usagePer = map[string]*runtime.Usage{
		"reviewer": {Attested: true, TokensInput: 10, TokensOutput: 5, CostUSD: 0.1},
	}
	rt.usageErrPer = map[string]error{
		"analyst": errors.New("codex: событие turn.completed с usage не найдено в выводе"),
	}

	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("пропуск в учёте расхода не должен валить run: %v", err)
	}

	runDir := onlyRunDir(t, dir)
	raw, readErr := os.ReadFile(filepath.Join(runDir, "usage.json"))
	if readErr != nil {
		t.Fatalf("usage.json: %v", readErr)
	}
	var envelope metrics.UsageEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("usage.json не парсится: %v", err)
	}
	if envelope.UsageGaps != 1 {
		t.Fatalf("неучтённый этап обязан попасть в usage.json: %+v", envelope)
	}
	if envelope.TokensInput != 10 {
		t.Fatalf("учтённый этап должен остаться в сумме: %+v", envelope)
	}

	events, evErr := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), filepath.Base(runDir))
	if evErr != nil {
		t.Fatalf("event log: %v", evErr)
	}
	var found *evidence.Event
	for i := range events {
		if events[i].Type == "usage_unreported" {
			found = &events[i]
		}
	}
	if found == nil {
		t.Fatal("ожидалось событие usage_unreported в append-only логе")
	}
	if found.Stage != "analyst" {
		t.Fatalf("событие должно называть этап: %+v", found)
	}
	if reason, _ := found.Data["reason"].(string); !strings.Contains(reason, "turn.completed") {
		t.Fatalf("событие должно нести причину: %+v", found.Data)
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
