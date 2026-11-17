package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/stagestatus"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const graphAPITemplate = `schema_version: 7
template: graph-api
title: Graph API
forbid_self_approval: %FORBID%
workflow: idea-to-prod
workflows:
  - id: idea-to-prod
    title: SDLC
    description: Standard flow
    version: 3
    graphs:
      - id: sdlc
        title: SDLC graph
        template: graph-api
    entry_gate_groups:
      - [verdict_marker@1]
    exit_gate_groups:
      - [human_approval@1]
  - id: content-factory
    title: Content
    version: 2
    extends: idea-to-prod
    graphs:
      - id: write
        title: Write graph
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

func graphAPIFixture(t *testing.T) functionPermissionFixture {
	t.Helper()
	f := newTemplateFixture(t, graphAPITemplate, false, nil,
		map[string]lifecycle.State{"run-graph": {Phase: lifecycle.PhaseRunning, NextStage: "build"}})
	now := time.Now().UTC()
	appendS11Event(t, f, "run-graph", "run_started", "", map[string]any{})
	appendS11Event(t, f, "run-graph", "attempt_started", "a-spec", map[string]any{"stage": "spec", "agent": "spec", "stage_index": 1})
	appendS11Event(t, f, "run-graph", "attempt_finished", "a-spec", map[string]any{"stage": "spec", "outcome": "passed", "status": "passed", "executor": "human", "actor_id": "po-1"})
	appendS11Event(t, f, "run-graph", "transition_selected", "a-spec", map[string]any{"from": "spec", "target": "build", "outcome": "passed", "edge_target": "build"})
	appendS11Event(t, f, "run-graph", "attempt_started", "a-build", map[string]any{"stage": "build", "agent": "build", "stage_index": 2})
	run, err := f.srv.store.GetPipelineRunByRunID("run-graph")
	if err != nil || run == nil {
		t.Fatalf("run: %v %v", run, err)
	}
	run.Status = "running"
	run.StartedAt = now
	if err := f.srv.store.UpdatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	return f
}

// graphAPIRouter mounts only the package's read routes; wiring them into the
// main router is an integration step outside this package.
func graphAPIRouter(srv *Server) *chi.Mux {
	router := chi.NewRouter()
	srv.registerGraphAPI(router)
	return router
}

func graphGET(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	graphAPIRouter(srv).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
	return recorder
}

func TestGraphRoutesServeClassAndRunModes(t *testing.T) {
	f := graphAPIFixture(t)

	classResponse := graphGET(t, f.srv, "/api/graphs/sdlc")
	if classResponse.Code != http.StatusOK {
		t.Fatalf("class graph: %d %s", classResponse.Code, classResponse.Body.String())
	}
	var class graphView
	if err := json.Unmarshal(classResponse.Body.Bytes(), &class); err != nil {
		t.Fatal(err)
	}
	if class.Mode != graphModeClass || class.GraphID != "sdlc" || class.WorkflowID != "idea-to-prod" || class.Title != "SDLC graph" {
		t.Fatalf("class graph metadata: %+v", class)
	}
	if class.Entry != "spec" || len(class.Nodes) != 2 || len(class.Edges) == 0 {
		t.Fatalf("compiled graph missing: %+v", class)
	}
	if class.Nodes[0].MaxVisits != 3 {
		t.Fatalf("node max_visits must come from the template returns: %+v", class.Nodes[0])
	}
	var passedWithApproval bool
	for _, edge := range class.Edges {
		if edge.Outcome == "passed" && edge.Approval != nil && len(edge.Approval.Roles) > 0 {
			passedWithApproval = true
		}
	}
	if !passedWithApproval {
		t.Fatalf("edges must carry outcome and approval: %+v", class.Edges)
	}
	if len(class.Returns) != 1 || class.Returns[0].From != "build" || class.Returns[0].To != "spec" || class.Returns[0].MaxVisits != 3 {
		t.Fatalf("returns missing: %+v", class.Returns)
	}
	if class.StageStatuses != nil || class.RunID != "" {
		t.Fatalf("a class graph must not carry run facts: %+v", class)
	}

	runResponse := graphGET(t, f.srv, "/api/runs/run-graph/graph")
	if runResponse.Code != http.StatusOK {
		t.Fatalf("run graph: %d %s", runResponse.Code, runResponse.Body.String())
	}
	var run graphView
	if err := json.Unmarshal(runResponse.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Mode != graphModeRun || run.RunID != "run-graph" || run.RunStatus != "running" || run.WorkflowID != "idea-to-prod" {
		t.Fatalf("run graph metadata: %+v", run)
	}
	if !reflect.DeepEqual(class.Graph, run.Graph) || !reflect.DeepEqual(class.Returns, run.Returns) {
		t.Fatalf("the two modes must serve the same graph: class=%+v run=%+v", class, run)
	}
	if run.StageStatuses == nil || run.CurrentStage != "build" {
		t.Fatalf("run graph must carry the computed projection: %+v", run)
	}
	statuses := f.requestAs(t, "owner@example.test", s11Roles, http.MethodGet, "/api/runs/run-graph/stage-statuses", "")
	if statuses.Code != http.StatusOK {
		t.Fatalf("stage statuses: %d %s", statuses.Code, statuses.Body.String())
	}
	var projection stagestatus.Projection
	if err := json.Unmarshal(statuses.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projection, *run.StageStatuses) {
		t.Fatalf("the graph projection must equal the task page projection")
	}

	// The task page reads the same graph out of the pinned template. Compare
	// wire shapes, not Go structs, so an omitted field counts too.
	detailsResponse := f.requestAs(t, "owner@example.test", s11Roles, http.MethodGet, "/api/runs/run-graph", "")
	if detailsResponse.Code != http.StatusOK {
		t.Fatalf("task details: %d %s", detailsResponse.Code, detailsResponse.Body.String())
	}
	var details taskRunDetails
	if err := json.Unmarshal(detailsResponse.Body.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	if details.Template == nil || details.Template.Graph == nil {
		t.Fatalf("task page has no graph: %s", detailsResponse.Body.String())
	}
	taskGraph, err := json.Marshal(details.Template.Graph)
	if err != nil {
		t.Fatal(err)
	}
	runGraph, err := json.Marshal(run.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if string(taskGraph) != string(runGraph) {
		t.Fatalf("graph differs from the task page: %s vs %s", taskGraph, runGraph)
	}
}

func TestGraphRoutesRefuseBadIDsAndWrites(t *testing.T) {
	f := graphAPIFixture(t)
	for _, path := range []string{"/api/graphs/bad%5Cid", "/api/runs/bad%5Cid/graph"} {
		if got := graphGET(t, f.srv, path).Code; got != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, got)
		}
	}
	for _, path := range []string{"/api/graphs/no-such-graph", "/api/runs/no-such-run/graph"} {
		if got := graphGET(t, f.srv, path).Code; got != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, got)
		}
	}
	if err := f.srv.store.CreatePipelineRun(&store.PipelineRun{RunID: "run-unpinned", Feature: "feature", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if got := graphGET(t, f.srv, "/api/runs/run-unpinned/graph").Code; got != http.StatusConflict {
		t.Errorf("an unpinned run = %d, want 409", got)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		graphAPIRouter(f.srv).ServeHTTP(recorder, httptest.NewRequest(method, "http://127.0.0.1/api/graphs/sdlc", nil))
		if recorder.Code < 400 {
			t.Errorf("%s must not be accepted on a read graph route: %d", method, recorder.Code)
		}
	}
}
