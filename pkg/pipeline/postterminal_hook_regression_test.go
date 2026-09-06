package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// Regression A6 (AUD-06): ручной/retry-путь post-terminal доставки
// (DeliverDeferred) применяет ОДИНАКОВУЮ policy к обоим терминальным
// статусам run: completed и completed_with_warnings (см. delivery_deferred.go
// switch terminalStatus). Успешная delivery-стадия и одинаковый approved plan
// дают одинаковый deferred-маркер и доставку независимо от статуса run.
// Примечание: автоматический post-terminal хук (pipeline.go, outcome ==
// RunCompleted) пока ограничен чистым completed; parity здесь фиксируется на
// уровне DeliverDeferred — контроллерном пути enforcement.
func TestDeliverDeferredPolicyParityCompletedAndWithWarnings(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		dir := env(t)
		approvedPlanHash := prepareDelivery(t, dir)
		rt := newScripted()
		rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
		p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
			WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(&gracefulDeliveryService{}))
		if err := p.Run(context.Background(), RunConfig{
			Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
		}); err == nil {
			t.Fatal("post-terminal hook сбоит — Run обязан вернуть ошибку")
		}
		runDir := onlyRunDir(t, dir)
		if got := runFinishedStatus(t, runDir); got != string(workflow.RunCompleted) {
			t.Fatalf("run_finished=%q, ожидали %q", got, workflow.RunCompleted)
		}
		record, err := New(nil, nil, WithDeliveryService(&fakeDeliveryService{})).DeliverDeferred(runDir, "", dir)
		if err != nil {
			t.Fatalf("DeliverDeferred(completed): %v", err)
		}
		if record.PlanHash != approvedPlanHash {
			t.Fatalf("record plan hash %q != approved %q", record.PlanHash, approvedPlanHash)
		}
	})

	t.Run("completed_with_warnings", func(t *testing.T) {
		dir := env(t)
		approvedPlanHash := prepareDelivery(t, dir)
		rt := newScripted()
		rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
		// Optional check на delivery-стадии падает (команда отсутствует) →
		// attempt получает OutcomeWarning, run завершается как
		// completed_with_warnings, но delivery-стадия успела записать маркер.
		cfg := cfgFor(
			config.AgentConfig{Name: "approver"},
			config.AgentConfig{Name: "deployer", Checks: []checks.Definition{{
				Name: "optional-security", Class: "unit", Adapter: checks.AdapterCommand,
				Command: []string{"ai-team-tool-that-does-not-exist"}, Policy: checks.PolicyOptional,
			}}},
		)
		p := New(cfg, deliveryRegistry(),
			WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(&gracefulDeliveryService{}))
		if err := p.Run(context.Background(), RunConfig{
			Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
		}); err != nil {
			t.Fatalf("run с warning должен завершиться успешно: %v", err)
		}
		runDir := onlyRunDir(t, dir)
		if got := runFinishedStatus(t, runDir); got != string(workflow.RunCompletedWithWarnings) {
			t.Fatalf("run_finished=%q, ожидали %q", got, workflow.RunCompletedWithWarnings)
		}
		record, err := New(nil, nil, WithDeliveryService(&fakeDeliveryService{})).DeliverDeferred(runDir, "", dir)
		if err != nil {
			t.Fatalf("DeliverDeferred(completed_with_warnings): %v", err)
		}
		if record.PlanHash != approvedPlanHash {
			t.Fatalf("record plan hash %q != approved %q", record.PlanHash, approvedPlanHash)
		}
	})
}

func runFinishedStatus(t *testing.T, runDir string) string {
	t.Helper()
	events, err := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), filepath.Base(runDir))
	if err != nil {
		t.Fatalf("event chain: %v", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != "run_finished" {
			continue
		}
		status, _ := events[i].Data["status"].(string)
		return status
	}
	t.Fatal("run_finished event не найден")
	return ""
}
