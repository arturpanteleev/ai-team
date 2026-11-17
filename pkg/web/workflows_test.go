package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// workflowProcessPatchBody builds the structured request the card sends: only
// the workflow id, the allow-listed fields, the optimistic version, and
// (optionally) an extra field an attacker tries to smuggle through.
func workflowProcessPatchBody(t *testing.T, workflowID, version string, extra map[string]any) string {
	t.Helper()
	payload := map[string]any{"workflow_id": workflowID, "expected_version": version}
	for name, value := range extra {
		payload[name] = value
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func patchWorkflowProcess(t *testing.T, f functionPermissionFixture, workflowID, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := authorizedRequest(t, f.srv, http.MethodPost, "/api/workflows/"+workflowID+"/process", body)
	recorder := httptest.NewRecorder()
	f.srv.router.ServeHTTP(recorder, request)
	return recorder
}

func workflowVersion(t *testing.T, f functionPermissionFixture) string {
	t.Helper()
	store, err := f.srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	_, version, err := store.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	return version
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
	version := workflowVersion(t, f)
	before, err := store.ListVersions()
	if err != nil {
		t.Fatal(err)
	}
	response := patchWorkflowProcess(t, f, "idea-to-prod", workflowProcessPatchBody(t, "idea-to-prod", version, map[string]any{
		"title": "SDLC v2", "description": "Обновлённое описание",
	}))
	if response.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", response.Code, response.Body.String())
	}
	var published struct {
		Version    string `json:"version"`
		WorkflowID string `json:"workflow_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &published); err != nil {
		t.Fatal(err)
	}
	if published.Version == "" || published.Version == version || published.WorkflowID != "idea-to-prod" {
		t.Fatalf("a new template version must be published: %+v", published)
	}
	after, err := store.ListVersions()
	if err != nil || len(after) <= len(before) {
		t.Fatalf("version history must grow: %d -> %d, %v", len(before), len(after), err)
	}
	// Provenance is kept by TemplateStore.Publish: the new bytes are registered
	// as web-written and the file config it replaced stays as the baseline.
	if _, err := os.Stat(filepath.Join(f.target, ".ai-team", "templates", "web-written", published.Version)); err != nil {
		t.Fatalf("web-written marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.target, ".ai-team", "templates", "trusted-baseline.yaml")); err != nil {
		t.Fatalf("trusted baseline: %v", err)
	}
	card := workflowCardAfterPublish(t, f)
	if card.Title != "SDLC v2" || card.Description != "Обновлённое описание" {
		t.Fatalf("edited process fields: %+v", card)
	}
	if got := gateKeys(card.EntryGateGroups); strings.Join(got, ",") != "verdict_marker@1" {
		t.Fatalf("entry gates changed by a process edit: %v", got)
	}
	if got := gateKeys(card.ExitGateGroups); strings.Join(got, ",") != "human_approval@1" {
		t.Fatalf("exit gates changed by a process edit: %v", got)
	}
}

// workflowCommandGateTemplate is a reviewed file config whose effective entry
// chain carries a command gate (external_program). The whole-document publish
// path refuses such a document even when it is byte-identical to the file;
// the process-only patch must still let the card edit title/description.
const workflowCommandGateTemplate = `schema_version: 7
template: command-gate
title: Command gate config
forbid_self_approval: false
workflow: idea-to-prod
workflows:
  - id: idea-to-prod
    title: SDLC
    description: Standard flow
    version: 3
    graphs:
      - id: sdlc
        title: SDLC graph
        template: command-gate
    entry_gate_groups:
      - [verdict_marker@1]
      - - gate: external_program
          version: 1
          params: {command: ["sh", "scripts/check.sh"]}
    exit_gate_groups:
      - [human_approval@1]
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

func TestWorkflowProcessEditWorksWithCommandGate(t *testing.T) {
	f := newTemplateFixture(t, workflowCommandGateTemplate, false, nil, nil)
	version := workflowVersion(t, f)
	response := patchWorkflowProcess(t, f, "idea-to-prod", workflowProcessPatchBody(t, "idea-to-prod", version, map[string]any{
		"description": "Описание с command-гейтом",
	}))
	if response.Code != http.StatusOK {
		t.Fatalf("patch with a command gate: %d %s", response.Code, response.Body.String())
	}
	card := workflowCardAfterPublish(t, f)
	if card.Description != "Описание с command-гейтом" {
		t.Fatalf("edited description: %+v", card)
	}
	refs := strings.Join(gateKeys(card.EntryGateGroups), ",")
	if !strings.Contains(refs, "verdict_marker@1") || !strings.Contains(refs, "external_program@1") {
		t.Fatalf("the command gate must survive the process edit: %v", refs)
	}
	if got := strings.Join(gateKeys(card.ExitGateGroups), ","); got != "human_approval@1" {
		t.Fatalf("exit gates changed by a process edit: %v", got)
	}

	// F2 regression: publishing the very same document through the full route
	// is still refused — that is why the card uses the process-only patch.
	store, err := f.srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	raw, currentVersion, err := store.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"yaml": string(raw), "expected_version": currentVersion})
	if err != nil {
		t.Fatal(err)
	}
	full := authorizedRequest(t, f.srv, http.MethodPost, "/api/template/publish", string(body))
	fullRecorder := httptest.NewRecorder()
	f.srv.router.ServeHTTP(fullRecorder, full)
	if fullRecorder.Code != http.StatusBadRequest {
		t.Fatalf("full publish of a command-gate document: %d %s", fullRecorder.Code, fullRecorder.Body.String())
	}
	var refusal apiResponseError
	if err := json.Unmarshal(fullRecorder.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Code != config.ErrCodeTrustedConfigOnly || !strings.Contains(refusal.Error, "external_program") {
		t.Fatalf("full publish refusal: %+v", refusal)
	}
}

// workflowAnchoredTemplate has a folded description (>-) and a YAML anchor
// (&t) reused as an alias (*t). A textual patch broke both; the structured
// patch re-renders from the parsed config and stays valid.
const workflowAnchoredTemplate = `schema_version: 7
template: anchored
title: Anchored config
forbid_self_approval: false
workflow: idea-to-prod
workflows:
  - id: idea-to-prod
    title: &t SDLC
    description: >-
      Многострочное
      описание workflow
    version: 3
    graphs:
      - id: sdlc
        title: SDLC graph
        template: anchored
    entry_gate_groups:
      - [verdict_marker@1]
    exit_gate_groups:
      - [human_approval@1]
  - id: content-factory
    title: Content
    description: *t
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

func TestWorkflowProcessEditWithBlockScalarAndAnchor(t *testing.T) {
	f := newTemplateFixture(t, workflowAnchoredTemplate, false, nil, nil)
	version := workflowVersion(t, f)
	response := patchWorkflowProcess(t, f, "idea-to-prod", workflowProcessPatchBody(t, "idea-to-prod", version, map[string]any{
		"title": "SDLC v2", "description": "Правка после блочного скаляра и якоря",
	}))
	if response.Code != http.StatusOK {
		t.Fatalf("patch with >- and &t/*t: %d %s", response.Code, response.Body.String())
	}
	card := workflowCardAfterPublish(t, f)
	if card.Title != "SDLC v2" || card.Description != "Правка после блочного скаляра и якоря" {
		t.Fatalf("edited process fields: %+v", card)
	}
	if got := strings.Join(gateKeys(card.EntryGateGroups), ","); got != "verdict_marker@1" {
		t.Fatalf("entry gates changed: %v", got)
	}
	// The whole published document must still parse after re-rendering.
	cfg, err := f.srv.currentConfig()
	if err != nil {
		t.Fatalf("published config is not parseable: %v", err)
	}
	other := ""
	for _, declared := range cfg.AllWorkflows() {
		if declared.ID == "content-factory" {
			other = declared.Description
		}
	}
	if other != "SDLC" {
		t.Fatalf("the aliased description of the other workflow changed: %q", other)
	}
}

func TestWorkflowProcessPatchRefusesCommandsGatesAndOtherWorkflow(t *testing.T) {
	cases := map[string]map[string]any{
		"commands":          {"commands": []any{map[string]any{"command": []string{"sh"}}}},
		"entry gates":       {"entry_gate_groups": []any{}},
		"exit gates":        {"exit_gate_groups": []any{}},
		"graphs":            {"graphs": []any{}},
		"infrastructure":    {"budget": map[string]any{"max_attempts": 1}},
		"executor adapters": {"executor_adapters": map[string]any{"evil": map[string]any{}}},
		"another workflow":  {"title": "Чужой workflow"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			f := graphAPIFixture(t)
			version := workflowVersion(t, f)
			workflowID := "idea-to-prod"
			if name == "another workflow" {
				// The body tries to redirect the patch to a different workflow.
				workflowID = "content-factory"
			}
			response := patchWorkflowProcess(t, f, "idea-to-prod", workflowProcessPatchBody(t, workflowID, version, extra))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s: %d %s", name, response.Code, response.Body.String())
			}
			var refusal apiResponseError
			if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
				t.Fatal(err)
			}
			if refusal.Code != config.ErrCodeTrustedConfigOnly {
				t.Fatalf("%s: refusal = %+v", name, refusal)
			}
			if card := workflowCardAfterPublish(t, f); card.Title != "SDLC" || card.Description != "Standard flow" {
				t.Fatalf("%s: refused patch changed the workflow: %+v", name, card)
			}
		})
	}
}

func TestWorkflowProcessPatchStaleVersionIsConflict(t *testing.T) {
	f := graphAPIFixture(t)
	response := patchWorkflowProcess(t, f, "idea-to-prod", workflowProcessPatchBody(t, "idea-to-prod", strings.Repeat("e", 64), map[string]any{
		"description": "Устаревшая правка",
	}))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale version: %d %s", response.Code, response.Body.String())
	}
	var refusal apiResponseError
	if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Code != "version_conflict" {
		t.Fatalf("stale version refusal: %+v", refusal)
	}
	if card := workflowCardAfterPublish(t, f); card.Description != "Standard flow" {
		t.Fatalf("stale patch changed the workflow: %+v", card)
	}
}

func TestWorkflowProcessPatchRefusesInvalidFieldsAndUnknownWorkflow(t *testing.T) {
	invalid := map[string]map[string]any{
		"empty title":           {"title": "  "},
		"multiline description": {"description": "первая\nвторая"},
	}
	for name, extra := range invalid {
		t.Run(name, func(t *testing.T) {
			f := graphAPIFixture(t)
			version := workflowVersion(t, f)
			response := patchWorkflowProcess(t, f, "idea-to-prod", workflowProcessPatchBody(t, "idea-to-prod", version, extra))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s: %d %s", name, response.Code, response.Body.String())
			}
			var refusal apiResponseError
			if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
				t.Fatal(err)
			}
			if refusal.Code != "invalid_process_patch" {
				t.Fatalf("%s: refusal = %+v", name, refusal)
			}
			if card := workflowCardAfterPublish(t, f); card.Description != "Standard flow" {
				t.Fatalf("%s: refused patch changed the workflow: %+v", name, card)
			}
		})
	}

	f := graphAPIFixture(t)
	version := workflowVersion(t, f)
	response := patchWorkflowProcess(t, f, "no-such-workflow", workflowProcessPatchBody(t, "no-such-workflow", version, map[string]any{
		"title": "X",
	}))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown workflow: %d %s", response.Code, response.Body.String())
	}
}
