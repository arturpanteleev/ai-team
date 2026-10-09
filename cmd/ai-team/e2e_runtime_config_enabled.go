//go:build e2etest

package main

import (
	"os"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

// e2eInMemoryLegacyConfig lets CLI-level tests continue exercising the current
// runtime without writing or loading a public schema-v4 file. It is compiled
// only into binaries built with the e2etest tag and is opt-in per subprocess.
func e2eInMemoryLegacyConfig(target string) (*config.Config, bool) {
	if os.Getenv("AI_TEAM_E2E_IN_MEMORY_LEGACY_RUNTIME") != "1" {
		return nil, false
	}
	cfg := config.Default()
	_, _ = cfg.ApplyDetectedChecks(target)
	if os.Getenv("AI_TEAM_E2E_GENERIC_APPROVAL_FIXTURE") == "1" {
		for i := range cfg.Workflow.Edges {
			edge := &cfg.Workflow.Edges[i]
			if edge.From == "analyst" && edge.Outcome == "passed" && edge.Approval != nil {
				delete(edge.Approval.Actions, "approve_spec")
				edge.Approval.Actions["approve"] = edge.To
				break
			}
		}
	}
	return cfg, true
}
