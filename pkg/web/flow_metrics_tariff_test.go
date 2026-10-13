package web

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

type tariffAttemptFixture struct {
	id, stage, model  string
	input, output     int64
	cacheRead         int64
	cacheReadIncluded bool
	withUsage         bool
}

// newTariffFlowServer writes a project config with the given usage section and
// returns a server reading it, plus a helper that records one run whose agent
// attempts carry attempt manifests.
func newTariffFlowServer(t *testing.T, usage *config.UsageConfig) (*Server, func(runID string, start time.Time, attempts []tariffAttemptFixture)) {
	t.Helper()
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Usage = usage
	configData, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), configData, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"), WithTargetDir(target), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	createRun := func(runID string, start time.Time, attempts []tariffAttemptFixture) {
		t.Helper()
		finished := start.Add(time.Hour)
		run := &store.PipelineRun{RunID: runID, Feature: "tariff", Status: "completed", StartedAt: start, CompletedAt: &finished}
		if err := srv.store.CreatePipelineRun(run); err != nil {
			t.Fatal(err)
		}
		for index, attempt := range attempts {
			completed := start.Add(time.Duration(index+1) * time.Minute)
			stage := store.Stage{PipelineRunID: run.ID, AttemptID: attempt.id, StageIndex: index + 1, AgentName: attempt.stage,
				Status: "passed", Execution: "agent", Decision: "approved", Outcome: "passed",
				StartedAt: start, CompletedAt: &completed, DurationMs: completed.Sub(start).Milliseconds()}
			if err := srv.store.CreateStage(&stage); err != nil {
				t.Fatal(err)
			}
			if err := srv.store.UpdateStage(&stage); err != nil {
				t.Fatal(err)
			}
			if !attempt.withUsage {
				continue
			}
			attemptDir := filepath.Join(srv.runRoot, runID, "attempts", attempt.id)
			if err := os.MkdirAll(attemptDir, 0o755); err != nil {
				t.Fatal(err)
			}
			manifest, marshalErr := json.Marshal(evidence.AttemptManifest{RunID: runID, AttemptID: attempt.id, FinishedAt: completed,
				Usage: &workflow.AttemptUsage{Attested: true, TokensInput: attempt.input, TokensOutput: attempt.output,
					Model: attempt.model, CacheReadTokens: attempt.cacheRead, CacheReadIncludedInInput: attempt.cacheReadIncluded, CostUSD: 99}})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := os.WriteFile(filepath.Join(attemptDir, "manifest.json"), manifest, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return srv, createRun
}

func tariffPrice(value float64) *float64 { return &value }

func flowStageByName(result flowMetricsResponse, name string) *flowStageStats {
	for index := range result.ByStage {
		if result.ByStage[index].Stage == name {
			return &result.ByStage[index]
		}
	}
	return nil
}

func approxEqual(value *float64, want float64) bool {
	return value != nil && math.Abs(*value-want) < 1e-9
}

func TestFlowMetricsCostByTariffPricesTokensByModel(t *testing.T) {
	srv, createRun := newTariffFlowServer(t, &config.UsageConfig{
		MonthlySubscriptionAmount: 100, MonthlySubscriptionCurrency: "USD",
		Tariffs: []config.TariffConfig{
			{Model: "model-a", InputPerMTok: tariffPrice(3), OutputPerMTok: tariffPrice(15), CachedInputPerMTok: tariffPrice(0.3),
				Currency: "USD", EffectiveDate: "2026-09-01", Source: "provider price list (test)"},
			{Model: "model-b", InputPerMTok: tariffPrice(1), OutputPerMTok: tariffPrice(4),
				Currency: "USD", EffectiveDate: "2026-09-01", Source: "provider price list (test)"},
		},
	})
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	createRun("tariff-at-from", from, []tariffAttemptFixture{
		{id: "impl-a", stage: "implementation", model: "model-a", input: 1_000_000, output: 100_000, withUsage: true},
		{id: "review-b", stage: "review", model: "model-b", input: 1_000_000, cacheRead: 500_000, cacheReadIncluded: false, withUsage: true},
	})
	createRun("tariff-in-period", from.Add(12*time.Hour), []tariffAttemptFixture{
		{id: "impl-a-cached", stage: "implementation", model: "model-a", input: 1_000_000, output: 0, cacheRead: 1_000_000, cacheReadIncluded: true, withUsage: true},
	})
	// Starts exactly at the exclusive end of the period: must not be priced.
	createRun("tariff-at-to", to, []tariffAttemptFixture{
		{id: "impl-late", stage: "implementation", model: "model-a", input: 1_000_000, output: 1_000_000, withUsage: true},
	})

	result := getFlowMetricsJSON(t, srv, "/api/metrics/flow?from=2026-10-01&to=2026-10-02")
	implementation := flowStageByName(result, "implementation")
	if implementation == nil || !approxEqual(implementation.CostByTariff.Amount, 3+1.5+0.3) ||
		implementation.CostByTariff.PricedAttempts != 2 || implementation.CostByTariff.Currency != "USD" {
		t.Fatalf("implementation cost=%+v; want 4.8 USD from two in-period attempts", implementation)
	}
	review := flowStageByName(result, "review")
	if review == nil || review.CostByTariff.Amount != nil || review.CostByTariff.Reason != "cache_price_missing" {
		t.Fatalf("review cost=%+v; cache reads without a cached price must be unpriced", review)
	}
	if result.CostByTariff.Amount != nil || result.CostByTariff.PricedAttempts != 2 || result.CostByTariff.UnpricedAttempts != 1 {
		t.Fatalf("total cost=%+v; a partially priced period must have no amount", result.CostByTariff)
	}
	if got := result.CostByTariff.TariffDates; len(got) != 1 || got[0] != "2026-09-01" ||
		result.CostByTariff.Source != flowTariffCostSource || result.CostByTariff.Basis != flowTariffCostBasis {
		t.Fatalf("total provenance=%+v", result.CostByTariff)
	}
	// The subscription share stays a separate, unchanged field.
	if result.Usage.EstimateBasis != "" && result.Usage.EstimateBasis != "monthly_subscription_weighted_by_attested_tokens" {
		t.Fatalf("subscription estimate basis changed: %+v", result.Usage)
	}

	onlyImplementation := getFlowMetricsJSON(t, srv, "/api/metrics/flow?from=2026-10-01T12:00:00Z&to=2026-10-02T00:00:00Z")
	if !approxEqual(onlyImplementation.CostByTariff.Amount, 0.3) || onlyImplementation.CostByTariff.Reason != "" {
		t.Fatalf("fully priced period total=%+v; want 0.3 USD", onlyImplementation.CostByTariff)
	}
}

func TestFlowMetricsCostByTariffUnknownModelIsNoData(t *testing.T) {
	srv, createRun := newTariffFlowServer(t, &config.UsageConfig{Tariffs: []config.TariffConfig{
		{Model: "model-a", InputPerMTok: tariffPrice(3), OutputPerMTok: tariffPrice(15), Currency: "USD", EffectiveDate: "2026-09-01", Source: "test"},
	}})
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	createRun("tariff-unknown", start, []tariffAttemptFixture{
		{id: "impl-z", stage: "implementation", model: "model-z", input: 10, output: 10, withUsage: true},
		{id: "review-legacy", stage: "review", input: 10, output: 10, withUsage: true},
		{id: "accept-no-usage", stage: "acceptance"},
	})
	result := getFlowMetricsJSON(t, srv, "/api/metrics/flow?from=2026-10-01&to=2026-10-02")
	for stage, reason := range map[string]string{"implementation": "tariff_missing_for_model", "review": "model_unknown", "acceptance": "usage_unknown"} {
		stat := flowStageByName(result, stage)
		if stat == nil || stat.CostByTariff.Amount != nil || stat.CostByTariff.Reason != reason || stat.CostByTariff.UnpricedAttempts != 1 {
			t.Fatalf("%s cost=%+v; want no data with %s", stage, stat, reason)
		}
	}
	if impl := flowStageByName(result, "implementation"); len(impl.CostByTariff.UnpricedModels) != 1 || impl.CostByTariff.UnpricedModels[0] != "model-z" {
		t.Fatalf("unpriced models=%v", impl.CostByTariff.UnpricedModels)
	}
	if result.CostByTariff.Amount != nil || result.CostByTariff.UnpricedAttempts != 3 {
		t.Fatalf("total=%+v; unknown must not become zero", result.CostByTariff)
	}
}

func TestFlowMetricsCostByTariffWithoutTariffsKeepsContract(t *testing.T) {
	srv, createRun := newTariffFlowServer(t, nil)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	createRun("tariff-none", start, []tariffAttemptFixture{
		{id: "impl-a", stage: "implementation", model: "model-a", input: 10, output: 10, withUsage: true},
	})
	request := newLoopbackRequest(http.MethodGet, "/api/metrics/flow?from=2026-10-01&to=2026-10-02", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"from", "to", "lead_time", "cycle_time", "by_stage", "overrides_by_stage", "skips_by_stage",
		"returns_by_stage", "usage", "agent_results", "agent_accepted_without_edit", "cost_by_tariff"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("flow JSON lost key %q: %s", key, response.Body.String())
		}
	}
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(raw["usage"], &usage); err != nil {
		t.Fatal(err)
	}
	if _, ok := usage["subscription_share_estimate"]; !ok {
		t.Fatalf("usage lost subscription_share_estimate: %s", raw["usage"])
	}
	if strings.Contains(response.Body.String(), `"cost_usd"`) {
		t.Fatalf("flow API must not expose runtime cost_usd: %s", response.Body.String())
	}
	var cost flowTariffCost
	if err := json.Unmarshal(raw["cost_by_tariff"], &cost); err != nil {
		t.Fatal(err)
	}
	if cost.Amount != nil || cost.Reason != "tariff_not_configured" || !strings.Contains(string(raw["cost_by_tariff"]), `"amount":null`) {
		t.Fatalf("cost without tariffs=%s; want amount null and tariff_not_configured", raw["cost_by_tariff"])
	}

	csvRequest := newLoopbackRequest(http.MethodGet, "/api/metrics/flow.csv?from=2026-10-01&to=2026-10-02", nil)
	csvResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(csvResponse, csvRequest)
	for _, want := range []string{`cost_by_tariff,"",,"","unavailable:tariff_not_configured"`, `stage_cost_by_tariff,"implementation",,`} {
		if csvResponse.Code != http.StatusOK || !strings.Contains(csvResponse.Body.String(), want) {
			t.Fatalf("flow CSV missing %q: %s", want, csvResponse.Body.String())
		}
	}
}
