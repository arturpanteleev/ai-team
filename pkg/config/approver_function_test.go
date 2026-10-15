package config

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func standardAgents() fakeLookup {
	return fakeLookup{"analyst": true, "verifier": true, "spec-checker": true, "architect": true, "design-reviewer": true, "reviewer": true, "coder": true, "tester": true, "observer": true}
}

func TestTemplateApproverFunctionValidation(t *testing.T) {
	base, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		stage    int
		approver string
		want     string
	}{
		{"omitted approver is valid", 3, "", ""},
		{"distinct approver is valid", 3, "reviewer", ""},
		{"alias approver is valid", 3, "po", ""},
		{"same function", 3, "developer", "совпадает с функцией исполнителя"},
		{"alias of executor function", 1, "product_owner", "совпадает с функцией исполнителя"},
		{"deployer alias of release manager", 6, "release_manager", "совпадает с функцией исполнителя"},
		{"unknown approver", 3, "operator", "не сопоставлен ни одной роли"},
		{"padded approver", 3, " reviewer", "пробелы"},
		{"admin is a system right, not an approver function", 3, "admin", "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := cloneConfig(t, base)
			copy.Stages[tc.stage].ApproverFunction = tc.approver
			err := copy.Validate(standardAgents())
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected valid template, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestTemplateApproverFunctionYAMLAndGraph(t *testing.T) {
	raw := []byte(`schema_version: 5
template: reviewed
title: Reviewed
forbid_self_approval: true
stages:
  - id: spec
    title: Spec
    function: po
    result: md
  - id: build
    title: Build
    function: developer
    approver_function: reviewer
    result: md
  - id: accept
    title: Accept
    function: bo
    result: approve
returns:
  - from: build
    to: spec
`)
	cfg, err := ParseYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ForbidSelfApproval {
		t.Fatal("forbid_self_approval must be parsed")
	}
	if cfg.Stages[1].ApproverFunction != "reviewer" || cfg.Stages[1].DecisionFunction() != "reviewer" {
		t.Fatalf("approver_function not parsed: %+v", cfg.Stages[1])
	}
	if cfg.Stages[0].DecisionFunction() != "po" {
		t.Fatalf("stage without approver must be decided by its executor function, got %q", cfg.Stages[0].DecisionFunction())
	}
	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		from    string
		outcome workflow.Outcome
		role    string
	}{
		{"spec", workflow.OutcomePassed, "product_owner"},
		{"build", workflow.OutcomePassed, "reviewer"},
		{"build", workflow.OutcomeRejected, "reviewer"},
	}
	for _, tc := range cases {
		edge, ok := graph.Edge(tc.from, tc.outcome)
		if !ok || edge.Approval == nil || len(edge.Approval.Roles) != 1 || edge.Approval.Roles[0] != tc.role {
			t.Fatalf("edge %s/%s approval roles = %+v, want [%s]", tc.from, tc.outcome, edge.Approval, tc.role)
		}
	}

	same := strings.Replace(string(raw), "approver_function: reviewer", "approver_function: developer", 1)
	if cfg, err := ParseYAML([]byte(same)); err == nil {
		if err := cfg.Validate(nil); err == nil || !strings.Contains(err.Error(), "совпадает") {
			t.Fatalf("matching executor and approver functions must be rejected, got %v", err)
		}
	} else if !strings.Contains(err.Error(), "совпадает") {
		t.Fatalf("matching executor and approver functions must be rejected, got %v", err)
	}
}

// admin is a system right: an approval edge must not name it, with a clear
// configuration error, whatever produced the graph.
func TestApprovalRoleErrors_AdminIsNotAProcessRole(t *testing.T) {
	graph := workflow.Graph{Edges: []workflow.Edge{
		{From: "build", To: "done", Approval: &workflow.ApprovalPolicy{Roles: []string{"reviewer"}}},
		{From: "done", To: "stop", Approval: &workflow.ApprovalPolicy{Roles: []string{" Admin "}}},
	}}
	for _, cloud := range []bool{true, false} {
		errs := approvalRoleErrors(graph, cloud)
		if len(errs) != 1 || !strings.Contains(errs[0], "admin") || !strings.Contains(errs[0], "системное право") {
			t.Fatalf("cloud=%v: %v", cloud, errs)
		}
	}
}
