package metrics

import (
	"math"
	"testing"
	"time"
)

func price(value float64) *float64 { return &value }

func testTariffs() []Tariff {
	return []Tariff{
		{Model: "model-a", InputPerMTok: 3, OutputPerMTok: 15, CachedInputPerMTok: price(0.3), CacheWritePerMTok: price(3.75),
			Currency: "USD", EffectiveDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Source: "price-list-a-v1"},
		{Model: "model-a", InputPerMTok: 2, OutputPerMTok: 10, CachedInputPerMTok: price(0.2), CacheWritePerMTok: price(2.5),
			Currency: "USD", EffectiveDate: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Source: "price-list-a-v2"},
		{Model: "model-b", InputPerMTok: 1, OutputPerMTok: 4, Currency: "EUR",
			EffectiveDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Source: "price-list-b"},
	}
}

func near(left, right float64) bool { return math.Abs(left-right) < 1e-9 }

func TestPriceAttemptMultipliesTokensByModelTariff(t *testing.T) {
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	cost, tariff, reason := PriceAttempt(testTariffs(), TariffUsage{Model: "model-a", InputTokens: 1_000_000, OutputTokens: 200_000, At: at})
	if reason != "" || !near(cost, 3+3) || tariff.Source != "price-list-a-v1" {
		t.Fatalf("cost=%v tariff=%+v reason=%q; want 6 USD by v1", cost, tariff, reason)
	}
	// Anthropic convention: cache reads and writes are outside input tokens.
	cost, _, reason = PriceAttempt(testTariffs(), TariffUsage{Model: "model-a", InputTokens: 100_000, OutputTokens: 0,
		CacheReadTokens: 1_000_000, CacheWriteTokens: 200_000, At: at})
	if reason != "" || !near(cost, 0.3+0.3+0.75) {
		t.Fatalf("separate cache cost=%v reason=%q; want 1.35", cost, reason)
	}
	// OpenAI convention: cache reads are part of input tokens.
	cost, _, reason = PriceAttempt(testTariffs(), TariffUsage{Model: "model-a", InputTokens: 1_000_000, CacheReadTokens: 400_000,
		CacheReadIncludedInInput: true, At: at})
	if reason != "" || !near(cost, 0.6*3+0.4*0.3) {
		t.Fatalf("included cache cost=%v reason=%q; want 1.92", cost, reason)
	}
}

func TestPriceAttemptUnknownIsNeverZero(t *testing.T) {
	at := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	for name, test := range map[string]struct {
		tariffs []Tariff
		usage   TariffUsage
		reason  string
	}{
		"no tariffs":         {nil, TariffUsage{Model: "model-a", InputTokens: 10, At: at}, TariffReasonNotConfigured},
		"no model recorded":  {testTariffs(), TariffUsage{InputTokens: 10, At: at}, TariffReasonModelUnknown},
		"unknown model":      {testTariffs(), TariffUsage{Model: "model-z", InputTokens: 10, At: at}, TariffReasonNoModelTariff},
		"before any tariff":  {testTariffs(), TariffUsage{Model: "model-a", InputTokens: 10, At: at.AddDate(0, -1, 0)}, TariffReasonNotEffective},
		"cache price absent": {testTariffs(), TariffUsage{Model: "model-b", InputTokens: 10, CacheReadTokens: 5, At: at}, TariffReasonCachePriceMissing},
		"cache over input":   {testTariffs(), TariffUsage{Model: "model-a", InputTokens: 1, CacheReadTokens: 5, CacheReadIncludedInInput: true, At: at}, TariffReasonInvalidUsage},
	} {
		t.Run(name, func(t *testing.T) {
			cost, _, reason := PriceAttempt(test.tariffs, test.usage)
			if reason != test.reason || cost != 0 {
				t.Fatalf("reason=%q cost=%v; want %q", reason, cost, test.reason)
			}
		})
	}
}

func TestSelectTariffEffectiveDateBoundary(t *testing.T) {
	boundary := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if tariff, reason := SelectTariff(testTariffs(), "model-a", boundary.Add(-time.Nanosecond)); reason != "" || tariff.Source != "price-list-a-v1" {
		t.Fatalf("just before the new tariff: %+v %q; want v1", tariff, reason)
	}
	if tariff, reason := SelectTariff(testTariffs(), "model-a", boundary); reason != "" || tariff.Source != "price-list-a-v2" {
		t.Fatalf("at the new tariff date: %+v %q; want v2", tariff, reason)
	}
}

func TestTariffCostSummaryReportsTotalOnlyWhenFullyPriced(t *testing.T) {
	tariffs := testTariffs()
	var empty TariffCost
	if summary := empty.Summary(); summary.Amount != nil || summary.Reason != "no_agent_attempts" {
		t.Fatalf("empty summary=%+v", summary)
	}
	var complete TariffCost
	complete.AddPriced(1.5, tariffs[0])
	complete.AddPriced(2, tariffs[1])
	summary := complete.Summary()
	if summary.Amount == nil || !near(*summary.Amount, 3.5) || summary.Currency != "USD" || summary.Priced != 2 ||
		len(summary.TariffDates) != 2 || summary.TariffDates[0] != "2026-09-01" || summary.TariffDates[1] != "2026-10-01" {
		t.Fatalf("complete summary=%+v", summary)
	}
	partial := complete
	partial.AddUnpriced("model-z", TariffReasonNoModelTariff)
	if summary := partial.Summary(); summary.Amount != nil || summary.Reason != TariffReasonNoModelTariff ||
		summary.Unpriced != 1 || len(summary.UnpricedModels) != 1 || summary.UnpricedModels[0] != "model-z" {
		t.Fatalf("partial summary=%+v; want no amount", summary)
	}
	var mixed TariffCost
	mixed.AddPriced(1, tariffs[0])
	mixed.AddPriced(1, tariffs[2])
	if summary := mixed.Summary(); summary.Amount != nil || summary.Reason != "mixed_currency" {
		t.Fatalf("mixed summary=%+v", summary)
	}
}
