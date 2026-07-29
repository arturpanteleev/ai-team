package workflow

import (
	"strings"
	"testing"
)

func TestGraphRejectsAmbiguousEdge(t *testing.T) {
	graph := Graph{
		Entry: "a", Nodes: []Node{{Name: "a"}, {Name: "b"}},
		Edges: []Edge{
			{From: "a", Outcome: OutcomePassed, To: "b", Approval: testApproval("b")},
			{From: "a", Outcome: OutcomePassed, To: TerminalComplete},
			{From: "b", Outcome: OutcomePassed, To: TerminalComplete},
		},
	}
	if err := graph.Validate(true, true); err == nil || !strings.Contains(err.Error(), "неоднозначное") {
		t.Fatalf("ambiguous graph принят: %v", err)
	}
}

func TestGraphCycleRequiresLimits(t *testing.T) {
	graph := Graph{
		Entry: "a", Nodes: []Node{{Name: "a"}, {Name: "b"}},
		Edges: []Edge{
			{From: "a", Outcome: OutcomePassed, To: "b", Approval: testApproval("b")},
			{From: "b", Outcome: OutcomeRejected, To: "a", Approval: testApproval("a")},
			{From: "b", Outcome: OutcomePassed, To: TerminalComplete},
		},
	}
	if err := graph.Validate(true, true); err == nil || !strings.Contains(err.Error(), "max_visits") {
		t.Fatalf("unbounded cycle принят: %v", err)
	}
	graph.Nodes[0].MaxVisits, graph.Nodes[1].MaxVisits = 2, 2
	if err := graph.Validate(true, true); err != nil {
		t.Fatalf("bounded graph отклонён: %v", err)
	}
}

func testApproval(target string) *ApprovalPolicy {
	return &ApprovalPolicy{
		Roles: []string{"operator"}, Quorum: "any",
		Actions: map[string]string{"approve": target, "reject": TerminalStop},
	}
}
