package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestWorkflowCatalogListsCardsWithVersionsAndGraphCounts(t *testing.T) {
	f := graphAPIFixture(t)
	recorder := graphGET(t, f.srv, "/api/workflows")
	if recorder.Code != http.StatusOK {
		t.Fatalf("catalog: %d %s", recorder.Code, recorder.Body.String())
	}
	var body workflowsListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ActiveWorkflowID != "idea-to-prod" || len(body.Workflows) != 2 {
		t.Fatalf("catalog: %+v", body)
	}
	byID := map[string]workflowListItem{}
	for _, item := range body.Workflows {
		byID[item.ID] = item
	}
	if got := byID["idea-to-prod"]; got.Title != "SDLC" || got.Description != "Standard flow" || got.Version != 3 || got.GraphCount != 1 || !got.Active {
		t.Fatalf("active workflow card: %+v", got)
	}
	if got := byID["content-factory"]; got.Title != "Content" || got.Version != 2 || got.GraphCount != 1 || got.Active {
		t.Fatalf("inherited workflow card: %+v", got)
	}
}

func TestWorkflowCardCarriesGraphsEffectiveGatesAndTasks(t *testing.T) {
	f := graphAPIFixture(t)
	recorder := graphGET(t, f.srv, "/api/workflows/idea-to-prod")
	if recorder.Code != http.StatusOK {
		t.Fatalf("workflow card: %d %s", recorder.Code, recorder.Body.String())
	}
	var card workflowDetailView
	if err := json.Unmarshal(recorder.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.ID != "idea-to-prod" || card.Version != 3 || !card.Active || card.Description != "Standard flow" {
		t.Fatalf("card metadata: %+v", card)
	}
	if len(card.Graphs) != 1 || card.Graphs[0].GraphID != "sdlc" || card.Graphs[0].WorkflowID != "idea-to-prod" ||
		card.Graphs[0].Mode != graphModeClass || card.Graphs[0].Entry != "spec" {
		t.Fatalf("card graphs: %+v", card.Graphs)
	}
	if refs := gateKeys(card.EntryGateGroups); strings.Join(refs, ",") != "verdict_marker@1" {
		t.Fatalf("effective entry gates: %v", refs)
	}
	if refs := gateKeys(card.ExitGateGroups); strings.Join(refs, ",") != "human_approval@1" {
		t.Fatalf("effective exit gates: %v", refs)
	}
	if len(card.Tasks) != 1 || card.Tasks[0].RunID != "run-graph" || card.Tasks[0].CurrentStage != "build" {
		t.Fatalf("tasks pinned to the workflow: %+v", card.Tasks)
	}

	// The child workflow has no gates of its own: the card must show the
	// inherited effective chains, not an empty list.
	inherited := graphGET(t, f.srv, "/api/workflows/content-factory")
	if inherited.Code != http.StatusOK {
		t.Fatalf("child workflow card: %d %s", inherited.Code, inherited.Body.String())
	}
	var child workflowDetailView
	if err := json.Unmarshal(inherited.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	if child.Active || len(child.Tasks) != 0 || len(child.Graphs) != 1 || child.Graphs[0].GraphID != "write" {
		t.Fatalf("child card: %+v", child)
	}
	if refs := gateKeys(child.EntryGateGroups); strings.Join(refs, ",") != "verdict_marker@1" {
		t.Fatalf("child must inherit the effective entry chain: %v", refs)
	}
}

func gateKeys(groups workflow.GateGroups) []string {
	refs := make([]string, 0)
	for _, ref := range groups.Refs() {
		refs = append(refs, ref.Key())
	}
	return refs
}

func TestWorkflowRoutesRefuseBadIDsAndWrites(t *testing.T) {
	f := graphAPIFixture(t)
	if got := graphGET(t, f.srv, "/api/workflows/bad%5Cid").Code; got != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400", got)
	}
	if got := graphGET(t, f.srv, "/api/workflows/no-such-workflow").Code; got != http.StatusNotFound {
		t.Errorf("unknown workflow = %d, want 404", got)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		graphAPIRouter(f.srv).ServeHTTP(recorder, httptest.NewRequest(method, "http://127.0.0.1/api/workflows", strings.NewReader("{}")))
		if recorder.Code < 400 {
			t.Errorf("%s must not be accepted on the catalog: %d", method, recorder.Code)
		}
	}
}
