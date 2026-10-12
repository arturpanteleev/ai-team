package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Отсутствие секции `budget` даёт НЕнулевой бюджет: нулевая длительность в
// pipeline означала бы run без wall-time границы вообще (QS-09, #143).
func TestBudgetConfigDefaults(t *testing.T) {
	var nilBudget *BudgetConfig
	dur, str := nilBudget.EffectiveMaxWallTime()
	if dur != DefaultBudgetMaxWallTimeDuration || str != DefaultBudgetMaxWallTime {
		t.Fatalf("nil budget wall: (%v, %s), want (%v, %s)",
			dur, str, DefaultBudgetMaxWallTimeDuration, DefaultBudgetMaxWallTime)
	}
	if dur <= 0 {
		t.Fatalf("нулевой бюджет оставляет run без верхней границы: %v", dur)
	}
	if nilBudget.EffectiveMaxAttempts() != DefaultBudgetMaxAttempts {
		t.Fatalf("nil budget attempts = %d", nilBudget.EffectiveMaxAttempts())
	}

	nilFields := &BudgetConfig{}
	dur, str = nilFields.EffectiveMaxWallTime()
	if dur != DefaultBudgetMaxWallTimeDuration || str != DefaultBudgetMaxWallTime {
		t.Fatalf("пустые поля должны давать дефолт wall-time: (%v, %s)", dur, str)
	}
	if nilFields.EffectiveMaxAttempts() != DefaultBudgetMaxAttempts {
		t.Fatalf("пустые поля должны давать дефолт attempts")
	}
}

// Непарсящееся и неположительное значение дают дефолт, а не ноль: Validate
// отвергает такие конфиги до запуска, а «без лимита» — худший из исходов.
func TestBudgetConfigMalformedFallsBackToDefault(t *testing.T) {
	for _, raw := range []string{"не длительность", "30", "-2h", "0s"} {
		bc := &BudgetConfig{MaxWallTime: raw}
		dur, str := bc.EffectiveMaxWallTime()
		if dur != DefaultBudgetMaxWallTimeDuration {
			t.Errorf("max_wall_time=%q -> %v, ожидался дефолт %v", raw, dur, DefaultBudgetMaxWallTimeDuration)
		}
		// Метка должна соответствовать реально вооружённому таймеру.
		if str != DefaultBudgetMaxWallTime {
			t.Errorf("max_wall_time=%q -> метка %q, ожидалась %q", raw, str, DefaultBudgetMaxWallTime)
		}
	}
}

// Строковая константа (диагностика) и длительность (таймер) обязаны совпадать:
// разойдясь, они снова сделают комментарий про «default 24h» ложью.
func TestDefaultBudgetWallTimeConstantsAgree(t *testing.T) {
	parsed, err := time.ParseDuration(DefaultBudgetMaxWallTime)
	if err != nil {
		t.Fatalf("DefaultBudgetMaxWallTime %q не парсится: %v", DefaultBudgetMaxWallTime, err)
	}
	if parsed != DefaultBudgetMaxWallTimeDuration {
		t.Fatalf("константы разошлись: %q = %v, а DefaultBudgetMaxWallTimeDuration = %v",
			DefaultBudgetMaxWallTime, parsed, DefaultBudgetMaxWallTimeDuration)
	}
}

// Each execution gets a bounded default; paused time belongs only to the task
// duration and is outside this execution budget.
func TestDefaultExecutionBudgetIsBounded(t *testing.T) {
	if DefaultBudgetMaxWallTimeDuration < 12*time.Hour {
		t.Fatalf("дефолт %v убьёт run, ждущий оператора до утра", DefaultBudgetMaxWallTimeDuration)
	}
}

func TestBudgetConfigExplicit(t *testing.T) {
	bc := &BudgetConfig{MaxExecutionTime: "2h", MaxAttempts: 50}
	if err := bc.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	dur, str := bc.EffectiveMaxWallTime()
	want := 2 * time.Hour
	if dur != want || str != "2h" {
		t.Fatalf("execution = (%v, %s), want (%v, 2h)", dur, str, want)
	}
	if bc.EffectiveMaxAttempts() != 50 {
		t.Fatalf("attempts = %d", bc.EffectiveMaxAttempts())
	}
}

func TestBudgetConfigLegacyMaxWallTimeAlias(t *testing.T) {
	legacy := &BudgetConfig{MaxWallTime: "45m"}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy max_wall_time should remain valid: %v", err)
	}
	dur, _ := legacy.EffectiveMaxExecutionTime()
	if dur != 45*time.Minute {
		t.Fatalf("legacy alias produced %v, want 45m", dur)
	}
	if err := (&BudgetConfig{MaxExecutionTime: "2h", MaxWallTime: "3h"}).Validate(); err == nil {
		t.Fatal("setting new and legacy keys together must be rejected")
	}
}

func TestBudgetConfigExecutionTimeKeysLoadFromYAML(t *testing.T) {
	for _, test := range []struct {
		name  string
		field string
	}{
		{name: "canonical", field: "max_execution_time"},
		{name: "legacy", field: "max_wall_time"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := "schema_version: 5\ntemplate: budget-test\ntitle: Budget test\nstages:\n  - id: analyst\n    title: Analyst\n    function: po\n    result: md\n    executor: human\nbudget:\n  " + test.field + ": 90m\n"
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("load %s: %v", test.field, err)
			}
			duration, _ := cfg.Budget.EffectiveMaxExecutionTime()
			if duration != 90*time.Minute {
				t.Fatalf("%s loaded as %v, want 90m", test.field, duration)
			}
		})
	}
}

func TestBudgetConfigValidateRejects(t *testing.T) {
	cases := map[string]*BudgetConfig{
		"bad-duration": {MaxExecutionTime: "-2h", MaxAttempts: 10},
		"parse-fail":   {MaxWallTime: "xyz", MaxAttempts: 10},
		"neg-attempts": {MaxExecutionTime: "1h", MaxAttempts: -5},
		"neg-task":     {MaxTaskTokens: -1},
		"neg-daily":    {MaxDailyTokens: -1},
	}
	for name, bc := range cases {
		if err := bc.Validate(); err == nil {
			t.Errorf("%s: должен быть отвергнут", name)
		}
	}
}

func TestAutoPickupConfigLoadsAndRequiresTokenBudgets(t *testing.T) {
	base := "schema_version: 5\ntemplate: pickup-test\ntitle: Pickup test\nstages:\n  - id: analyst\n    title: Analyst\n    function: po\n    result: md\n    executor: human\n"
	cases := []struct {
		name    string
		extra   string
		wantErr bool
	}{
		{"enabled with both budgets", "budget:\n  max_task_tokens: 500\n  max_daily_tokens: 5000\nauto_pickup:\n  enabled: true\n  max_parallel_tasks: 2\n  poll_interval: 30s\n", false},
		{"disabled without budgets", "auto_pickup:\n  enabled: false\n", false},
		{"enabled without budget section", "auto_pickup:\n  enabled: true\n", true},
		{"enabled without daily budget", "budget:\n  max_task_tokens: 500\nauto_pickup:\n  enabled: true\n", true},
		{"enabled without task budget", "budget:\n  max_daily_tokens: 500\nauto_pickup:\n  enabled: true\n", true},
		{"negative parallel limit", "auto_pickup:\n  max_parallel_tasks: -1\n", true},
		{"bad poll interval", "auto_pickup:\n  poll_interval: soon\n", true},
		{"negative daily budget", "budget:\n  max_daily_tokens: -5\n", true},
		{"budget key typo", "budget:\n  max_daily_token: 1\n", true},
		{"unknown auto_pickup key", "auto_pickup:\n  bogus: 1\n", true},
		{"auto_pickup must be a mapping", "auto_pickup: true\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(base+tc.extra), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err == nil {
				err = cfg.Validate(nil)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("load/validate err=%v, wantErr=%t", err, tc.wantErr)
			}
			if tc.name == "enabled with both budgets" {
				if !cfg.AutoPickup.Enabled || cfg.AutoPickup.EffectiveMaxParallelTasks() != 2 ||
					cfg.AutoPickup.EffectivePollInterval() != 30*time.Second || cfg.Budget.MaxTaskTokens != 500 || cfg.Budget.MaxDailyTokens != 5000 {
					t.Fatalf("auto_pickup loaded incorrectly: %+v budget=%+v", cfg.AutoPickup, cfg.Budget)
				}
			}
		})
	}
}

func TestAutoPickupConfigDefaults(t *testing.T) {
	var missing *AutoPickupConfig
	if missing.EffectiveMaxParallelTasks() != DefaultAutoPickupMaxParallelTasks || missing.EffectivePollInterval() != DefaultAutoPickupPollInterval {
		t.Fatal("nil auto_pickup must use canonical defaults")
	}
	if err := missing.Validate(nil); err != nil {
		t.Fatalf("absent auto_pickup is valid: %v", err)
	}
	malformed := &AutoPickupConfig{PollInterval: "-1m"}
	if malformed.EffectivePollInterval() != DefaultAutoPickupPollInterval {
		t.Fatal("non-positive poll interval must fall back to the default")
	}
}
