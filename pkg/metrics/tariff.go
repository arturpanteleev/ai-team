package metrics

import (
	"math"
	"sort"
	"time"
)

// Tariff is one provider price list entry for a model in currency units per
// one million tokens (B-66). A nil cache price means the price is not known.
type Tariff struct {
	Model              string
	InputPerMTok       float64
	OutputPerMTok      float64
	CachedInputPerMTok *float64
	CacheWritePerMTok  *float64
	Currency           string
	EffectiveDate      time.Time
	Source             string
}

// TariffUsage is the attested usage of one model attempt as needed for
// pricing. CacheReadTokens are part of InputTokens only when
// CacheReadIncludedInInput is set; CacheWriteTokens never are.
type TariffUsage struct {
	Model                    string
	InputTokens              int64
	OutputTokens             int64
	CacheReadTokens          int64
	CacheWriteTokens         int64
	CacheReadIncludedInInput bool
	At                       time.Time
}

// Reasons an attempt cannot be priced. The caller reports such an attempt as
// unpriced ("нет данных"), never as zero cost.
const (
	TariffReasonNotConfigured     = "tariff_not_configured"
	TariffReasonModelUnknown      = "model_unknown"
	TariffReasonNoModelTariff     = "tariff_missing_for_model"
	TariffReasonNotEffective      = "tariff_not_effective_at_attempt"
	TariffReasonCachePriceMissing = "cache_price_missing"
	TariffReasonInvalidUsage      = "usage_invalid"
)

const tokensPerMillion = 1_000_000

// SelectTariff returns the entry for usage.Model with the latest effective
// date not after usage.At (dates are UTC days, so the whole effective day is
// covered). An empty reason means a tariff was found.
func SelectTariff(tariffs []Tariff, model string, at time.Time) (Tariff, string) {
	if len(tariffs) == 0 {
		return Tariff{}, TariffReasonNotConfigured
	}
	if model == "" {
		return Tariff{}, TariffReasonModelUnknown
	}
	var selected Tariff
	found, known := false, false
	for _, tariff := range tariffs {
		if tariff.Model != model {
			continue
		}
		known = true
		if tariff.EffectiveDate.After(at.UTC()) {
			continue
		}
		if !found || tariff.EffectiveDate.After(selected.EffectiveDate) {
			selected, found = tariff, true
		}
	}
	switch {
	case found:
		return selected, ""
	case known:
		return Tariff{}, TariffReasonNotEffective
	default:
		return Tariff{}, TariffReasonNoModelTariff
	}
}

// PriceAttempt computes tokens × tariff for one attempt. It returns the cost,
// the tariff used and an empty reason, or a reason why the attempt has no
// price. Unknown never becomes zero.
func PriceAttempt(tariffs []Tariff, usage TariffUsage) (float64, Tariff, string) {
	tariff, reason := SelectTariff(tariffs, usage.Model, usage.At)
	if reason != "" {
		return 0, Tariff{}, reason
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheReadTokens < 0 || usage.CacheWriteTokens < 0 ||
		(usage.CacheReadIncludedInInput && usage.CacheReadTokens > usage.InputTokens) {
		return 0, Tariff{}, TariffReasonInvalidUsage
	}
	uncachedInput := usage.InputTokens
	if usage.CacheReadIncludedInInput {
		uncachedInput -= usage.CacheReadTokens
	}
	cost := float64(uncachedInput)*tariff.InputPerMTok + float64(usage.OutputTokens)*tariff.OutputPerMTok
	if usage.CacheReadTokens > 0 {
		if tariff.CachedInputPerMTok == nil {
			return 0, Tariff{}, TariffReasonCachePriceMissing
		}
		cost += float64(usage.CacheReadTokens) * *tariff.CachedInputPerMTok
	}
	if usage.CacheWriteTokens > 0 {
		if tariff.CacheWritePerMTok == nil {
			return 0, Tariff{}, TariffReasonCachePriceMissing
		}
		cost += float64(usage.CacheWriteTokens) * *tariff.CacheWritePerMTok
	}
	cost /= tokensPerMillion
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return 0, Tariff{}, TariffReasonInvalidUsage
	}
	return cost, tariff, ""
}

// TariffCost accumulates priced and unpriced attempts. Amount is reported
// only when every attempt is priced in one currency.
type TariffCost struct {
	amount   float64
	currency string
	mixed    bool
	priced   int
	unpriced int
	reasons  map[string]int
	models   map[string]bool
	dates    map[string]bool
	sources  map[string]bool
}

// AddPriced records a priced attempt.
func (c *TariffCost) AddPriced(cost float64, tariff Tariff) {
	c.priced++
	if c.currency == "" {
		c.currency = tariff.Currency
	} else if c.currency != tariff.Currency {
		c.mixed = true
	}
	total := c.amount + cost
	if math.IsInf(total, 0) || math.IsNaN(total) {
		c.mixed = true
	}
	c.amount = total
	if c.dates == nil {
		c.dates, c.sources = map[string]bool{}, map[string]bool{}
	}
	c.dates[tariff.EffectiveDate.UTC().Format("2006-01-02")] = true
	c.sources[tariff.Source] = true
}

// AddUnpriced records an attempt without a price and why.
func (c *TariffCost) AddUnpriced(model, reason string) {
	c.unpriced++
	if c.reasons == nil {
		c.reasons, c.models = map[string]int{}, map[string]bool{}
	}
	c.reasons[reason]++
	if model != "" && reason != TariffReasonNotConfigured {
		c.models[model] = true
	}
}

// TariffCostSummary is the reported view of an accumulator.
type TariffCostSummary struct {
	Amount         *float64
	Currency       string
	Priced         int
	Unpriced       int
	Reason         string
	UnpricedModels []string
	TariffDates    []string
	TariffSources  []string
}

// Summary reports the total only when all attempts are priced in a single
// currency; otherwise Amount is nil and Reason names the dominant cause.
func (c TariffCost) Summary() TariffCostSummary {
	summary := TariffCostSummary{Priced: c.priced, Unpriced: c.unpriced, Currency: c.currency,
		UnpricedModels: sortedKeys(c.models), TariffDates: sortedKeys(c.dates), TariffSources: sortedKeys(c.sources)}
	switch {
	case c.priced == 0 && c.unpriced == 0:
		summary.Reason = "no_agent_attempts"
	case c.unpriced > 0:
		summary.Reason = dominantReason(c.reasons)
	case c.mixed:
		summary.Reason = "mixed_currency"
	default:
		amount := c.amount
		summary.Amount = &amount
	}
	return summary
}

func dominantReason(reasons map[string]int) string {
	best, bestCount := "", -1
	for reason, count := range reasons {
		if count > bestCount || (count == bestCount && reason < best) {
			best, bestCount = reason, count
		}
	}
	return best
}

func sortedKeys(values map[string]bool) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
