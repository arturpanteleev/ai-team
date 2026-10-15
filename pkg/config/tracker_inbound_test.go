package config

import (
	"strings"
	"testing"
	"time"
)

func TestTrackerInboundConfigRoundTripAndValidation(t *testing.T) {
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tracker = &TrackerConfig{
		Fake: &FakeTrackerConfig{},
		Inbound: &TrackerInboundConfig{PollInterval: "30s", Rules: []TrackerInboundRule{
			{Status: "In Progress", Action: "advance"},
			{Status: "Rework", Action: "return", Stage: "code_review", ToStage: "implementation"},
		}},
	}
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("valid inbound map rejected: %v", err)
	}
	if got := cfg.Tracker.Inbound.EffectivePollInterval(); got != 30*time.Second {
		t.Fatalf("poll interval %v", got)
	}
	encoded, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ParseYAML(encoded)
	if err != nil || loaded.Tracker.Inbound == nil || len(loaded.Tracker.Inbound.Rules) != 2 ||
		loaded.Tracker.Inbound.Rules[1].ToStage != "implementation" {
		t.Fatalf("round-trip lost the inbound map: %+v %v", loaded.Tracker, err)
	}
	if (*TrackerInboundConfig)(nil).EffectivePollInterval() != DefaultTrackerInboundPollInterval {
		t.Fatal("default poll interval")
	}

	for name, tc := range map[string]struct {
		inbound TrackerInboundConfig
		want    string
	}{
		"empty status":      {TrackerInboundConfig{Rules: []TrackerInboundRule{{Status: " ", Action: "advance"}}}, "status"},
		"unknown action":    {TrackerInboundConfig{Rules: []TrackerInboundRule{{Status: "Done", Action: "complete"}}}, "advance или return"},
		"return no target":  {TrackerInboundConfig{Rules: []TrackerInboundRule{{Status: "Rework", Action: "return"}}}, "to_stage обязателен"},
		"advance with to":   {TrackerInboundConfig{Rules: []TrackerInboundRule{{Status: "Done", Action: "advance", ToStage: "qa"}}}, "только для action: return"},
		"unknown stage":     {TrackerInboundConfig{Rules: []TrackerInboundRule{{Status: "Done", Action: "advance", Stage: "nope"}}}, "неизвестный этап"},
		"unknown target":    {TrackerInboundConfig{Rules: []TrackerInboundRule{{Status: "R", Action: "return", ToStage: "nope"}}}, "неизвестный этап"},
		"duplicate":         {TrackerInboundConfig{Rules: []TrackerInboundRule{{Status: "Done", Action: "advance"}, {Status: "done", Action: "advance"}}}, "неоднозначно"},
		"too frequent poll": {TrackerInboundConfig{PollInterval: "1s"}, "poll_interval"},
		"unparsable poll":   {TrackerInboundConfig{PollInterval: "often"}, "poll_interval"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg.Tracker.Inbound = &tc.inbound
			if err := cfg.Validate(nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error with %q, got %v", tc.want, err)
			}
		})
	}
	// A wildcard rule overlapping a stage-specific one is legal here; the
	// ambiguity is reported when such a status actually arrives.
	cfg.Tracker.Inbound = &TrackerInboundConfig{Rules: []TrackerInboundRule{
		{Status: "Done", Action: "advance"}, {Status: "Done", Action: "return", Stage: "code_review", ToStage: "implementation"}}}
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("overlap must be accepted: %v", err)
	}
	if _, err := ParseYAML([]byte("schema_version: 5\ntemplate: x\ntitle: X\nstages: []\ntracker:\n  fake: {}\n  inbound:\n    rules:\n      - status: A\n        action: advance\n        command: rm\n")); err == nil {
		t.Fatal("unknown rule field must be rejected")
	}
}
