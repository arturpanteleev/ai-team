package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
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

// publishWorkflowYAML is the process-only publish path the workflow card uses
// to save its editable fields: the whole document is submitted, and the server
// re-checks the trusted-config allow-list before writing a new version.
func publishWorkflowYAML(t *testing.T, f functionPermissionFixture, yaml, version string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"yaml": yaml, "expected_version": version})
	if err != nil {
		t.Fatal(err)
	}
	request := authorizedRequest(t, f.srv, http.MethodPost, "/api/template/publish", string(body))
	recorder := httptest.NewRecorder()
	f.srv.router.ServeHTTP(recorder, request)
	return recorder
}

func workflowCardAfterPublish(t *testing.T, f functionPermissionFixture) workflowDetailView {
	t.Helper()
	request := authorizedRequest(t, f.srv, http.MethodGet, "/api/workflows/idea-to-prod", "")
	recorder := httptest.NewRecorder()
	f.srv.router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("workflow card: %d %s", recorder.Code, recorder.Body.String())
	}
	var card workflowDetailView
	if err := json.Unmarshal(recorder.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	return card
}

func TestWorkflowProcessEditPublishesAndAuditsVersion(t *testing.T) {
	f := graphAPIFixture(t)
	store, err := f.srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	raw, version, err := store.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ListVersions()
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(raw), "description: Standard flow", `description: "Обновлённое описание"`, 1)
	if updated == string(raw) {
		t.Fatal("fixture description line not found")
	}
	response := publishWorkflowYAML(t, f, updated, version)
	if response.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", response.Code, response.Body.String())
	}
	var published struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &published); err != nil {
		t.Fatal(err)
	}
	if published.Version == "" || published.Version == version {
		t.Fatalf("a new template version must be published: %+v", published)
	}
	after, err := store.ListVersions()
	if err != nil || len(after) <= len(before) {
		t.Fatalf("version history must grow: %d -> %d, %v", len(before), len(after), err)
	}
	card := workflowCardAfterPublish(t, f)
	if card.Description != "Обновлённое описание" {
		t.Fatalf("edited description: %+v", card)
	}
	if got := gateKeys(card.EntryGateGroups); strings.Join(got, ",") != "verdict_marker@1" {
		t.Fatalf("entry gates changed by a process edit: %v", got)
	}
	if got := gateKeys(card.ExitGateGroups); strings.Join(got, ",") != "human_approval@1" {
		t.Fatalf("exit gates changed by a process edit: %v", got)
	}
}

func TestWorkflowProcessEditRefusesCommandsGatesAndInfrastructure(t *testing.T) {
	cases := map[string]struct {
		change   func(string) string
		expected string
	}{
		"command gate": {
			change: func(yaml string) string {
				return strings.Replace(yaml, "      - [verdict_marker@1]",
					"      - [verdict_marker@1]\n      - - {gate: external_program, version: 1, params: {command: [sh]}}", 1)
			},
			expected: "external_program",
		},
		"gate removed": {
			change: func(yaml string) string {
				return strings.Replace(yaml, "    exit_gate_groups:\n      - [human_approval@1]\n", "", 1)
			},
			expected: "human_approval",
		},
		"weaker active workflow": {
			change: func(yaml string) string {
				yaml = strings.Replace(yaml, "workflow: idea-to-prod", "workflow: content-factory", 1)
				return strings.Replace(yaml, "    title: Content\n    version: 2\n",
					"    title: Content\n    version: 2\n    entry_gate_overrides:\n      verdict_marker: {gate: verdict_marker, version: 1, mode: advisory}\n", 1)
			},
			expected: "verdict_marker",
		},
		"infrastructure": {
			change: func(yaml string) string {
				return yaml + "budget:\n  max_attempts: 1\n"
			},
			expected: "budget",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := graphAPIFixture(t)
			store, err := f.srv.templateStore()
			if err != nil {
				t.Fatal(err)
			}
			raw, version, err := store.ReadCurrent()
			if err != nil {
				t.Fatal(err)
			}
			submitted := tc.change(string(raw))
			if submitted == string(raw) {
				t.Fatal("test change did not apply")
			}
			response := publishWorkflowYAML(t, f, submitted, version)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s: %d %s", name, response.Code, response.Body.String())
			}
			var refusal apiResponseError
			if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
				t.Fatal(err)
			}
			if refusal.Code != config.ErrCodeTrustedConfigOnly || !strings.Contains(refusal.Error, tc.expected) {
				t.Fatalf("%s: refusal = %+v", name, refusal)
			}
			card := workflowCardAfterPublish(t, f)
			if card.Description != "Standard flow" {
				t.Fatalf("%s: refused edit changed the workflow: %+v", name, card)
			}
		})
	}
}
