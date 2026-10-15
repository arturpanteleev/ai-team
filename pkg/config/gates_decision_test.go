package config

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gates_decision_test.go — the version-2 LLM and human gates (S05).

func TestV6ProductSpecChainWithVersion2DecisionGates(t *testing.T) {
	cfg := mustParse(t, `schema_version: 6
template: gates
title: Gates
stages:
  - id: product_spec
    title: Spec
    function: po
    result: md
    exit_gate_groups:
      - - gate: required_sections
          version: 1
          params: {sections: ["Критерии приёмки"]}
      - - gate: agent_review
          version: 2
          name: spec_llm_review
          params: {agent: spec-checker, verdict_schema: json, max_attempts: 3}
        - gate: agent_review
          version: 2
          name: tone_review
          params: {agent: verifier}
      - - gate: human_approval
          version: 2
          name: po_approval
          params: {on_reject: stay}
  - id: done
    title: Done
    function: bo
    result: approve
`)
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatalf("valid chain rejected: %v", err)
	}
	stage := cfg.Stages[0]
	checks := stage.AgentChecks()
	if len(checks) != 2 || checks[0].VerdictSchema != "json" || checks[0].MaxAttempts != 3 || checks[1].VerdictSchema != "" {
		t.Fatalf("LLM gate contract not read from params: %+v", checks)
	}
	if !stage.RejectStays() || !stage.NeedsConfirmation() {
		t.Fatal("on_reject: stay must be visible on the stage")
	}
	graph, err := cfg.TemplateGraph()
	if err != nil {
		t.Fatal(err)
	}
	edge, ok := graph.Edge("product_spec", workflow.OutcomePassed)
	if !ok || edge.Approval == nil || edge.Approval.Actions["reject"] != "product_spec" || edge.Approval.Actions["approve"] != "done" {
		t.Fatalf("a rejection must keep the task at the stage: %+v", edge.Approval)
	}
}

func TestV6DefaultRejectStillEndsTheRun(t *testing.T) {
	cfg := mustParse(t, v6Doc("    exit_gate_groups:\n      - [human_approval@2]\n"))
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatal(err)
	}
	graph, err := cfg.TemplateGraph()
	if err != nil {
		t.Fatal(err)
	}
	edge, _ := graph.Edge("product_spec", workflow.OutcomePassed)
	if cfg.Stages[0].RejectStays() || edge.Approval == nil || edge.Approval.Actions["reject"] != workflow.TerminalStop {
		t.Fatalf("version-1 semantics of reject must stay the default: %+v", edge.Approval)
	}
}

func TestV6RejectsInvalidDecisionGateParams(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"bad schema", "    exit_gate_groups:\n      - - gate: agent_review\n          version: 2\n          params: {agent: verifier, verdict_schema: xml}\n", `параметр "verdict_schema"`},
		{"zero attempts", "    exit_gate_groups:\n      - - gate: agent_review\n          version: 2\n          params: {agent: verifier, max_attempts: 0}\n", "max_attempts"},
		{"too many attempts", "    exit_gate_groups:\n      - - gate: agent_review\n          version: 2\n          params: {agent: verifier, max_attempts: 6}\n", "max_attempts 6 больше допустимого 5"},
		{"schema on grill", "    exit_gate_groups:\n      - - gate: agent_review\n          version: 2\n          params: {agent: spec-checker, mode: grill, verdict_schema: json}\n", "не применим к mode grill"},
		{"attempts on grill", "    exit_gate_groups:\n      - - gate: agent_review\n          version: 2\n          params: {agent: spec-checker, mode: grill, max_attempts: 2}\n", "не применим к mode grill"},
		{"bad on_reject", "    exit_gate_groups:\n      - - gate: human_approval\n          version: 2\n          params: {on_reject: ignore}\n", `параметр "on_reject"`},
		{"version 1 has no on_reject", "    exit_gate_groups:\n      - - gate: human_approval\n          version: 1\n          params: {on_reject: stay}\n", "не принимает параметров"},
		{"version 1 has no schema", "    exit_gate_groups:\n      - - gate: agent_review\n          version: 1\n          params: {agent: verifier, verdict_schema: json}\n", `"verdict_schema"`},
		{"human before an automatic group", "    exit_gate_groups:\n      - [human_approval@2]\n      - [verdict_marker@1]\n", "только в последней группе"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseYAML([]byte(v6Doc(tc.body)))
			if err == nil {
				err = cfg.Validate(gateAgents())
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestV6SeveralPlainLLMGatesShareAChain(t *testing.T) {
	cfg := mustParse(t, v6Doc(`    exit_gate_groups:
      - - gate: agent_review
          version: 2
          name: one
          params: {agent: verifier}
        - gate: agent_review
          version: 2
          name: two
          params: {agent: reviewer, verdict_schema: json}
      - - gate: agent_review
          version: 2
          name: three
          params: {agent: spec-checker, max_attempts: 2}
      - [human_approval@2]
`))
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatalf("any number of LLM gates in a chain is allowed: %v", err)
	}
	if got := len(cfg.Stages[0].AgentChecks()); got != 3 {
		t.Fatalf("agent checks = %d", got)
	}
}
