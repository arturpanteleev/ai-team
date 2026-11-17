package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// graphAPITwoWorkflowTemplate declares two workflows whose exit gate chains
// differ (human_approval@1 stops a rejected run vs human_approval@2 stays at
// the stage) and whose graphs declare different entry stages. A graph compiled
// from the active workflow's gates instead of its own would serve the wrong
// reject target and the wrong entry for the content-factory graph.
const graphAPITwoWorkflowTemplate = `schema_version: 7
template: graph-two
title: Graph Two
workflow: idea-to-prod
workflows:
  - id: idea-to-prod
    title: SDLC
    version: 3
    graphs:
      - id: sdlc
        title: SDLC graph
        template: graph-two
        entry: spec
    exit_gate_groups:
      - [human_approval@1]
  - id: content-factory
    title: Content
    version: 2
    graphs:
      - id: write
        title: Write graph
        template: graph-two
        entry: build
    exit_gate_groups:
      - - gate: human_approval
          version: 2
          params: {on_reject: stay}
stages:
  - id: spec
    title: Spec
    function: po
    result: md
  - id: build
    title: Build
    function: developer
    result: md
returns:
  - from: build
    to: spec
    max_visits: 3
`

func graphTwoWorkflowFixture(t *testing.T) functionPermissionFixture {
	t.Helper()
	return newTemplateFixture(t, graphAPITwoWorkflowTemplate, false, nil, nil)
}

// graphPassedEdge returns the passed edge leaving a stage.
func graphPassedEdge(graph workflow.Graph, from string) (workflow.Edge, bool) {
	for _, edge := range graph.Edges {
		if edge.From == from && edge.Outcome == workflow.OutcomePassed {
			return edge, true
		}
	}
	return workflow.Edge{}, false
}

func TestWorkflowGraphsCompilePerWorkflowWithItsGatesAndEntry(t *testing.T) {
	f := graphTwoWorkflowFixture(t)

	activeResponse := graphGET(t, f.srv, "/api/graphs/sdlc")
	if activeResponse.Code != http.StatusOK {
		t.Fatalf("sdlc graph: %d %s", activeResponse.Code, activeResponse.Body.String())
	}
	var active graphView
	if err := json.Unmarshal(activeResponse.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if active.WorkflowID != "idea-to-prod" || active.Entry != "spec" {
		t.Fatalf("active graph must keep its own workflow and entry: %+v", active)
	}
	activeEdge, ok := graphPassedEdge(active.Graph, "spec")
	if !ok || activeEdge.Approval == nil {
		t.Fatalf("active workflow human_approval@1 must compile an approval on the spec transition: %+v", active.Edges)
	}
	if got := activeEdge.Approval.Actions["reject"]; got != workflow.TerminalStop {
		t.Fatalf("human_approval@1 must reject to $stop, got %q", got)
	}

	contentResponse := graphGET(t, f.srv, "/api/graphs/write")
	if contentResponse.Code != http.StatusOK {
		t.Fatalf("write graph: %d %s", contentResponse.Code, contentResponse.Body.String())
	}
	var content graphView
	if err := json.Unmarshal(contentResponse.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if content.WorkflowID != "content-factory" || content.Entry != "build" {
		t.Fatalf("content graph must keep its own workflow and entry: %+v", content)
	}
	contentEdge, ok := graphPassedEdge(content.Graph, "spec")
	if !ok || contentEdge.Approval == nil {
		t.Fatalf("content workflow human_approval@2 must compile an approval on the spec transition: %+v", content.Edges)
	}
	if got := contentEdge.Approval.Actions["reject"]; got != "spec" {
		t.Fatalf("human_approval@2 on_reject:stay must keep the task at spec, got %q", got)
	}

	// The workflow card must carry the same per-graph data, one graph at a time.
	cardResponse := graphGET(t, f.srv, "/api/workflows/content-factory")
	if cardResponse.Code != http.StatusOK {
		t.Fatalf("content workflow card: %d %s", cardResponse.Code, cardResponse.Body.String())
	}
	var card workflowDetailView
	if err := json.Unmarshal(cardResponse.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if len(card.Graphs) != 1 || card.Graphs[0].Entry != "build" {
		t.Fatalf("content workflow card must carry its own graph entry: %+v", card.Graphs)
	}
}

func TestGraphRouteRefusesAnAmbiguousGraphID(t *testing.T) {
	f := graphTwoWorkflowAmbiguousFixture(t)
	response := graphGET(t, f.srv, "/api/graphs/shared")
	if response.Code != http.StatusConflict {
		t.Fatalf("an id declared by two workflows must be refused, got %d %s", response.Code, response.Body.String())
	}
}

// graphAPITwoWorkflowAmbiguousTemplate declares the same graph id in two
// different workflows: uniqueness is only enforced within one workflow.
const graphAPITwoWorkflowAmbiguousTemplate = `schema_version: 7
template: graph-dup
title: Graph Dup
workflow: idea-to-prod
workflows:
  - id: idea-to-prod
    title: SDLC
    version: 1
    graphs:
      - id: shared
        title: First
        template: graph-dup
  - id: content-factory
    title: Content
    version: 1
    graphs:
      - id: shared
        title: Second
        template: graph-dup
stages:
  - id: spec
    title: Spec
    function: po
    result: md
`

func graphTwoWorkflowAmbiguousFixture(t *testing.T) functionPermissionFixture {
	t.Helper()
	return newTemplateFixture(t, graphAPITwoWorkflowAmbiguousTemplate, false, nil, nil)
}

func TestWorkflowCardListsTasksBeyondTheFirstPage(t *testing.T) {
	f := graphAPIFixture(t)
	templateStore, err := f.srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	// graphAPIFixture already pins one run ("run-graph") to idea-to-prod; add
	// more than one page (100) of further runs pinned to the same workflow.
	const extra = 150
	for i := 0; i < extra; i++ {
		runID := fmt.Sprintf("bulk-run-%03d", i)
		if err := f.srv.store.CreatePipelineRun(&store.PipelineRun{RunID: runID, Feature: "feature",
			Status: "running", StartedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if _, err := templateStore.PinCurrentForRun(runID, "bulk-task"); err != nil {
			t.Fatal(err)
		}
	}
	card := graphGET(t, f.srv, "/api/workflows/idea-to-prod")
	if card.Code != http.StatusOK {
		t.Fatalf("workflow card: %d %s", card.Code, card.Body.String())
	}
	var detail workflowDetailView
	if err := json.Unmarshal(card.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Tasks) != extra+1 {
		t.Fatalf("workflow card must list every pinned task across all pages, got %d want %d", len(detail.Tasks), extra+1)
	}
}
