package config

import "testing"

// Без гейта human_approval у этапа нет подтверждения перехода. Единственное
// исключение — этап с возвратами: возврат является решением человека на
// переходе, поэтому переход вперёд оформляется approve или return_to_*.
func TestV6TransitionApprovalFollowsHumanGateAndReturns(t *testing.T) {
	cfg := mustParse(t, `schema_version: 6
template: gates
title: Gates
stages:
  - id: plain
    title: Plain
    function: po
    result: md
  - id: checked
    title: Checked
    function: po
    result: md
    exit_gate_groups:
      - - gate: required_sections
          version: 1
          params: {sections: ["Раздел"]}
  - id: approved
    title: Approved
    function: po
    result: md
    exit_gate_groups:
      - [human_approval@1]
  - id: reviewed
    title: Reviewed
    function: po
    result: md
  - id: done
    title: Done
    function: bo
    result: approve
returns:
  - {from: reviewed, to: plain}
`)
	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"plain": false, "checked": false, "approved": true, "reviewed": true} {
		edge, ok := graph.Edge(id, "passed")
		if !ok || (edge.Approval != nil) != want {
			t.Fatalf("stage %q: approval=%v, want %v", id, edge.Approval != nil, want)
		}
	}
	edge, _ := graph.Edge("reviewed", "passed")
	if edge.Approval.Actions["return_to_plain"] != "plain" || edge.Approval.Actions["approve"] != "done" {
		t.Fatalf("этап с возвратом должен предлагать approve и return_to_*: %+v", edge.Approval.Actions)
	}
}
