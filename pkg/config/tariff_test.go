package config

import (
	"strings"
	"testing"
)

func TestUsageTariffsRoundTripThroughStrictYAML(t *testing.T) {
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	price := func(value float64) *float64 { return &value }
	cfg.Usage = &UsageConfig{Tariffs: []TariffConfig{{Model: "model-a", InputPerMTok: price(3), OutputPerMTok: price(15),
		CachedInputPerMTok: price(0.3), Currency: "USD", EffectiveDate: "2026-09-01", Source: "provider price list"}}}
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tariffs:", "input_per_mtok: 3", "cached_input_per_mtok: 0.3", "effective_date: \"2026-09-01\"", "source: provider price list"} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("marshalled config lacks %q:\n%s", key, data)
		}
	}
	parsed, err := ParseYAML(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Usage == nil || len(parsed.Usage.Tariffs) != 1 {
		t.Fatalf("tariffs not parsed: %+v", parsed.Usage)
	}
	tariff := parsed.Usage.Tariffs[0]
	if tariff.Model != "model-a" || tariff.InputPerMTok == nil || *tariff.InputPerMTok != 3 || tariff.CachedInputPerMTok == nil ||
		tariff.CacheWritePerMTok != nil || tariff.EffectiveDate != "2026-09-01" {
		t.Fatalf("tariff=%+v", tariff)
	}
}

func TestUsageTariffsRejectIncompleteEntries(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	valid := TariffConfig{Model: "model-a", InputPerMTok: price(3), OutputPerMTok: price(15), Currency: "USD", EffectiveDate: "2026-09-01", Source: "list"}
	for name, test := range map[string]struct {
		mutate func(*TariffConfig)
		want   string
	}{
		"missing model":   {func(tariff *TariffConfig) { tariff.Model = "" }, "model"},
		"missing input":   {func(tariff *TariffConfig) { tariff.InputPerMTok = nil }, "input_per_mtok"},
		"negative output": {func(tariff *TariffConfig) { tariff.OutputPerMTok = price(-1) }, "output_per_mtok"},
		"bad currency":    {func(tariff *TariffConfig) { tariff.Currency = "usd" }, "currency"},
		"bad date":        {func(tariff *TariffConfig) { tariff.EffectiveDate = "01.09.2026" }, "effective_date"},
		"missing source":  {func(tariff *TariffConfig) { tariff.Source = " " }, "source"},
		"negative cached": {func(tariff *TariffConfig) { tariff.CachedInputPerMTok = price(-0.1) }, "cached_input_per_mtok"},
	} {
		t.Run(name, func(t *testing.T) {
			tariff := valid
			test.mutate(&tariff)
			err := (&UsageConfig{Tariffs: []TariffConfig{tariff}}).Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v; want mention of %q", err, test.want)
			}
		})
	}
	if err := (&UsageConfig{Tariffs: []TariffConfig{valid, valid}}).Validate(); err == nil || !strings.Contains(err.Error(), "дважды") {
		t.Fatalf("duplicate tariff err=%v", err)
	}
	if err := (&UsageConfig{Tariffs: []TariffConfig{valid}}).Validate(); err != nil {
		t.Fatalf("valid tariff rejected: %v", err)
	}
}
