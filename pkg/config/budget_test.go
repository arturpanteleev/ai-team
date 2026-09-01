package config

import (
	"testing"
	"time"
)

func TestBudgetConfigDefaults(t *testing.T) {
	var nilBudget *BudgetConfig
	dur, str := nilBudget.EffectiveMaxWallTime()
	if dur != 0 || str != DefaultBudgetMaxWallTime {
		t.Fatalf("nil budget wall: (%v, %s)", dur, str)
	}
	if nilBudget.EffectiveMaxAttempts() != DefaultBudgetMaxAttempts {
		t.Fatalf("nil budget attempts = %d", nilBudget.EffectiveMaxAttempts())
	}

	nilFields := &BudgetConfig{}
	dur, _ = nilFields.EffectiveMaxWallTime()
	if str != DefaultBudgetMaxWallTime || dur != 0 {
		t.Fatalf("пустые поля должны давать дефолт wall-time: (%v, %s)", dur, str)
	}
	if nilFields.EffectiveMaxAttempts() != DefaultBudgetMaxAttempts {
		t.Fatalf("пустые поля должны давать дефолт attempts")
	}
}

func TestBudgetConfigExplicit(t *testing.T) {
	bc := &BudgetConfig{MaxWallTime: "2h", MaxAttempts: 50}
	if err := bc.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	dur, str := bc.EffectiveMaxWallTime()
	want := 2 * time.Hour
	if dur != want || str != "2h" {
		t.Fatalf("wall = (%v, %s), want (%v, 2h)", dur, str, want)
	}
	if bc.EffectiveMaxAttempts() != 50 {
		t.Fatalf("attempts = %d", bc.EffectiveMaxAttempts())
	}
}

func TestBudgetConfigValidateRejects(t *testing.T) {
	cases := map[string]*BudgetConfig{
		"bad-duration": {MaxWallTime: "-2h", MaxAttempts: 10},
		"parse-fail":   {MaxWallTime: "xyz", MaxAttempts: 10},
		"neg-attempts": {MaxWallTime: "1h", MaxAttempts: -5},
	}
	for name, bc := range cases {
		if err := bc.Validate(); err == nil {
			t.Errorf("%s: должен быть отвергнут", name)
		}
	}
}
