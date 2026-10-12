package autopickup

import (
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/tokenbudget"
)

func TestDecideLimitsAndBudgets(t *testing.T) {
	policy := Policy{Enabled: true, MaxParallelTasks: 2, MaxTaskTokens: 1000, MaxDailyTokens: 100}
	ready := func(active int, daily int64) Snapshot {
		return Snapshot{HasReady: true, ActiveTasks: active, DailyUsage: tokenbudget.Usage{Tokens: daily}}
	}
	cases := []struct {
		name     string
		policy   Policy
		snapshot Snapshot
		want     Decision
	}{
		{"disabled", Policy{MaxParallelTasks: 2, MaxTaskTokens: 1, MaxDailyTokens: 1}, ready(0, 0), Decision{Reason: ReasonDisabled}},
		{"no task budget", Policy{Enabled: true, MaxParallelTasks: 2, MaxDailyTokens: 100}, ready(0, 0), Decision{Reason: ReasonBudgetNotConfigured}},
		{"no daily budget", Policy{Enabled: true, MaxParallelTasks: 2, MaxTaskTokens: 100}, ready(0, 0), Decision{Reason: ReasonBudgetNotConfigured}},
		{"no parallel slots", Policy{Enabled: true, MaxTaskTokens: 1, MaxDailyTokens: 1}, ready(0, 0), Decision{Reason: ReasonBudgetNotConfigured}},
		{"empty queue", policy, Snapshot{}, Decision{Reason: ReasonNoReadyItems}},
		{"one slot below parallel limit", policy, ready(1, 0), Decision{Pick: true}},
		{"exactly at parallel limit", policy, ready(2, 0), Decision{Reason: ReasonParallelLimit}},
		{"one above parallel limit", policy, ready(3, 0), Decision{Reason: ReasonParallelLimit}},
		{"invalid active count", policy, ready(-1, 0), Decision{Reason: ReasonParallelLimit}},
		{"one token below daily budget", policy, ready(0, 99), Decision{Pick: true}},
		{"exactly at daily budget", policy, ready(0, 100), Decision{Reason: ReasonDailyBudget}},
		{"one token above daily budget", policy, ready(0, 101), Decision{Reason: ReasonDailyBudget}},
		{"unknown daily usage", policy, Snapshot{HasReady: true, DailyUsage: tokenbudget.Usage{Unknown: true}}, Decision{Reason: ReasonDailyBudget}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.policy, tc.snapshot); got != tc.want {
				t.Fatalf("Decide = %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestHumanTransitions(t *testing.T) {
	cases := []struct {
		from, to string
		allowed  bool
	}{
		{StatusDraft, StatusReady, true},
		{StatusFailed, StatusReady, true},
		{StatusReady, StatusDraft, true},
		{StatusFailed, StatusDraft, true},
		{StatusDraft, StatusDiscarded, true},
		{StatusReady, StatusDiscarded, true},
		{StatusFailed, StatusDiscarded, true},
		{StatusStarting, StatusReady, false},
		{StatusStarted, StatusReady, false},
		{StatusStarted, StatusDraft, false},
		{StatusDiscarded, StatusReady, false},
		{StatusDraft, StatusStarted, false},
		{StatusDraft, StatusStarting, false},
		{"READY", StatusReady, false},
		{StatusDraft, "READY", false},
		{StatusDraft, "auto", false},
	}
	for _, tc := range cases {
		if got := HumanTransitionAllowed(tc.from, tc.to); got != tc.allowed {
			t.Fatalf("%s -> %s allowed=%t want %t", tc.from, tc.to, got, tc.allowed)
		}
	}
}

func TestPolicyFromConfig(t *testing.T) {
	if got := PolicyFromConfig(nil); got != (Policy{}) {
		t.Fatalf("nil config policy = %+v", got)
	}
	if got := PolicyFromConfig(&config.Config{}); got.Enabled {
		t.Fatalf("missing auto_pickup must disable pickup: %+v", got)
	}
	cfg := &config.Config{
		AutoPickup: &config.AutoPickupConfig{Enabled: true},
		Budget:     &config.BudgetConfig{MaxTaskTokens: 10, MaxDailyTokens: 20},
	}
	want := Policy{Enabled: true, MaxParallelTasks: config.DefaultAutoPickupMaxParallelTasks, MaxTaskTokens: 10, MaxDailyTokens: 20}
	if got := PolicyFromConfig(cfg); got != want {
		t.Fatalf("policy = %+v want %+v", got, want)
	}
	cfg.Budget = nil
	cfg.AutoPickup.MaxParallelTasks = 3
	if got := PolicyFromConfig(cfg); got.MaxParallelTasks != 3 || got.MaxTaskTokens != 0 || Decide(got, Snapshot{HasReady: true}).Reason != ReasonBudgetNotConfigured {
		t.Fatalf("policy without budget must not pick: %+v", got)
	}
}
