package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// WF-07: the second workflow is a flexibility check. It must be a catalog of
// workflows (a set of graphs), not a model switch: every workflow exposes the
// same run engine, only the graphs and gate chains differ.

func TestDefaultCatalogCarriesTwoWorkflowsWithoutModelField(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("default config with two workflows must validate: %v", err)
	}
	catalog := cfg.AllWorkflows()
	if len(catalog) != 2 {
		t.Fatalf("default catalog: want 2 workflows, got %d: %+v", len(catalog), catalog)
	}
	byID := map[string]WorkflowMeta{}
	for _, meta := range catalog {
		byID[meta.ID] = meta
	}
	sdlc, ok := byID[DefaultWorkflowID]
	if !ok || len(sdlc.Graphs) != 1 || sdlc.Graphs[0].ID != DefaultGraphID {
		t.Fatalf("SDLC workflow lost or changed: %+v", sdlc)
	}
	bugfix, ok := byID[BugfixWorkflowID]
	if !ok {
		t.Fatalf("bugfix workflow missing from default catalog: %+v", catalog)
	}
	if len(bugfix.Graphs) != 2 {
		t.Fatalf("bugfix must declare its own graph set (2 graphs), got %+v", bugfix.Graphs)
	}
	graphIDs := map[string]bool{}
	for _, graph := range bugfix.Graphs {
		graphIDs[graph.ID] = true
	}
	if !graphIDs[BugfixTriageGraphID] || !graphIDs[BugfixFixGraphID] {
		t.Fatalf("bugfix graphs differ from SDLC graph: %+v", bugfix.Graphs)
	}
	if len(bugfix.Graphs[1].Transitions) != 1 || bugfix.Graphs[1].Transitions[0].To != BugfixFixGraphID {
		t.Fatalf("bugfix graphs must be connected: %+v", bugfix.Graphs[1].Transitions)
	}
	if len(sdlc.EntryGateGroups.Refs()) != 0 || len(sdlc.ExitGateGroups.Refs()) != 0 {
		t.Fatalf("SDLC workflow must keep its empty gate chains: %+v", sdlc)
	}
	if got := strings.Join(refsOf(bugfix.EntryGateGroups), ","); got != "verdict_marker@1:bug_report_verdict" {
		t.Fatalf("bugfix entry gates: %s", got)
	}
	if got := strings.Join(refsOf(bugfix.ExitGateGroups), ","); got != "human_approval@2:hotfix_approval" {
		t.Fatalf("bugfix exit gates: %s", got)
	}

	// A workflow is a set of graphs, not a model: no field may declare a model.
	typ := reflect.TypeOf(WorkflowMeta{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.ToLower(field.Tag.Get("yaml"))
		if strings.Contains(tag, "model") || strings.EqualFold(field.Name, "Model") {
			t.Fatalf("workflow metadata must not carry a model field: %s", field.Name)
		}
	}
}

func TestDefaultProfileCatalogDoesNotRewriteV6Files(t *testing.T) {
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllWorkflows()) != 2 {
		t.Fatalf("default profile must carry the second workflow in memory: %+v", cfg.AllWorkflows())
	}
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "workflows:") || strings.Contains(string(data), "workflow:") {
		t.Fatalf("v6 file must stay workflow-free:\n%s", data)
	}
	reparsed := mustParse(t, string(data))
	if got := len(reparsed.AllWorkflows()); got != 1 {
		t.Fatalf("reopened v6 file must synthesize only idea-to-prod, got %d", got)
	}
}

func TestDemoTemplatePublishesBothWorkflowsWithDifferentChains(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "demo", "demo-template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := mustParse(t, string(data))
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("demo template must validate: %v", err)
	}
	if cfg.SchemaVersion != WorkflowSchemaVersion || cfg.ActiveWorkflowID() != DefaultWorkflowID {
		t.Fatalf("demo must be v7 with the SDLC workflow active: schema=%d active=%q", cfg.SchemaVersion, cfg.ActiveWorkflowID())
	}
	catalog := cfg.AllWorkflows()
	if len(catalog) != 2 {
		t.Fatalf("demo catalog: want 2 workflows, got %d", len(catalog))
	}
	sdlc, err := cfg.WorkflowGateGroups(DefaultWorkflowID, false)
	if err != nil {
		t.Fatal(err)
	}
	bugfixEntry, err := cfg.WorkflowGateGroups(BugfixWorkflowID, true)
	if err != nil {
		t.Fatal(err)
	}
	bugfixExit, err := cfg.WorkflowGateGroups(BugfixWorkflowID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sdlc.Refs()) != 0 {
		t.Fatalf("SDLC workflow has no workflow-level gates: %v", refsOf(sdlc))
	}
	if got := strings.Join(refsOf(bugfixEntry), ","); got != "verdict_marker@1:bug_report_verdict" {
		t.Fatalf("demo bugfix entry gates: %s", got)
	}
	if got := strings.Join(refsOf(bugfixExit), ","); got != "human_approval@2:hotfix_approval" {
		t.Fatalf("demo bugfix exit gates: %s", got)
	}
	for _, meta := range catalog {
		if meta.ID == BugfixWorkflowID && (len(meta.Graphs) != 2 || meta.Graphs[0].Entry == meta.Graphs[1].Entry) {
			t.Fatalf("bugfix graph entries must differ: %+v", meta.Graphs)
		}
	}
}

func TestBugfixTaskCompilesAndTravelsItsGraph(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "demo", "demo-template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	base := mustParse(t, string(data))
	bugfix, err := base.WithActiveWorkflow(BugfixWorkflowID)
	if err != nil {
		t.Fatalf("select bugfix for a new task: %v", err)
	}
	if bugfix.ActiveWorkflowID() != BugfixWorkflowID || base.ActiveWorkflowID() != DefaultWorkflowID {
		t.Fatalf("workflow selection must not mutate the pinned base config")
	}
	bugfixGraph, err := bugfix.CompiledGraph()
	if err != nil {
		t.Fatalf("bugfix task must compile: %v", err)
	}
	if err := bugfixGraph.Validate(false, false); err != nil {
		t.Fatalf("compiled bugfix graph invalid: %v", err)
	}

	// Walk the recorded verdict path like a run does: each stage approves, and
	// the passed edge must lead to the next declared stage or to completion.
	visited := []string{}
	current := bugfixGraph.Entry
	for step := 0; !workflow.IsTerminal(current); step++ {
		if step > len(bugfixGraph.Nodes)+2 {
			t.Fatalf("run did not terminate: %v", visited)
		}
		node, ok := bugfixGraph.Node(current)
		if !ok {
			t.Fatalf("unknown graph node %q", current)
		}
		visited = append(visited, node.Key())
		edge, ok := bugfixGraph.Edge(current, workflow.OutcomePassed)
		if !ok {
			t.Fatalf("node %q has no passed edge", current)
		}
		current = edge.To
	}
	if len(visited) == 0 || visited[0] != "intent" {
		t.Fatalf("bugfix run must have a declared entry: %v", visited)
	}

	// The workflow chain is part of the compiled graph: with bugfix active the
	// forward edges carry the human_approval gate of the bugfix exit chain.
	stage, ok := bugfixGraph.Node("product_spec")
	if !ok {
		t.Fatal("demo stage product_spec missing from graph")
	}
	passed, ok := bugfixGraph.Edge(stage.Key(), workflow.OutcomePassed)
	if !ok || passed.Approval == nil {
		t.Fatalf("bugfix exit gate must compile into an approval: %+v", passed)
	}
	groups, err := bugfix.ExitGateGroupsFor(base.Stages[1])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range groups.Refs() {
		if ref.Gate == gatelib.GateHumanApproval && ref.InstanceName() == "hotfix_approval" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bugfix human decision lost at stage: %v", refsOf(groups))
	}
}

func TestIdeaToProdStillCompilesWithoutWorkflowsBlock(t *testing.T) {
	// An old v6 task keeps its pinned template and the synthesized default
	// workflow: no second workflow, no entry gates, unchanged SDLC path.
	cfg := mustParse(t, v6Doc(""))
	if err := cfg.Validate(gateAgents()); err != nil {
		t.Fatalf("old v6 config must validate: %v", err)
	}
	catalog := cfg.AllWorkflows()
	if len(catalog) != 1 || catalog[0].ID != DefaultWorkflowID {
		t.Fatalf("old config must keep one synthesized workflow: %+v", catalog)
	}
	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatalf("old config graph must still compile: %v", err)
	}
	if graph.Entry != "product_spec" {
		t.Fatalf("old config entry changed: %q", graph.Entry)
	}
	if edge, ok := graph.Edge("product_spec", workflow.OutcomePassed); !ok || edge.To != "done" {
		t.Fatalf("old config path changed: %+v", edge)
	}
	if _, err := cfg.WithActiveWorkflow(BugfixWorkflowID); err == nil {
		t.Fatal("old config must not resolve a workflow it never declared")
	}
}
