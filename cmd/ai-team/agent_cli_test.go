package main

import (
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

func TestEffectiveAgentCLIUsesGlobalCLIForUnusedBuiltinChecker(t *testing.T) {
	cfg := &config.Config{CLI: "claude"}
	if got := effectiveAgentCLI(cfg, "design-checker", "opencode"); got != "claude" {
		t.Fatalf("unused checker CLI = %q, want claude", got)
	}
	cfg.PipelineAgents = []config.AgentConfig{{Name: "design-checker", CLI: "opencode"}}
	if got := effectiveAgentCLI(cfg, "design-checker", "claude"); got != "opencode" {
		t.Fatalf("explicit checker CLI = %q, want opencode", got)
	}
}
