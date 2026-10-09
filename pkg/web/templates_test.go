package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const architectAnalystTemplateYAML = `schema_version: 5
template: project-flow
title: Project flow
stages:
  - id: architect
    title: Architecture
    function: architect
    result: md
    executor: human
  - id: analyst
    title: Analysis
    function: po
    result: md
    executor: human
returns:
  - from: analyst
    to: architect
    max_visits: 2
`

func TestTemplateEditorAPIValidatesPublishesAndPinsNewTasks(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.Mkdir(controlDir, 0755); err != nil {
		t.Fatal(err)
	}
	initial, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	initialYAML, err := initial.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), initialYAML, 0644); err != nil {
		t.Fatal(err)
	}
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"),
		WithTargetDir(target), WithRunController(controller), WithTemplateAgentLookup(agent.NewFS(os.DirFS("../../agents"))))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	get := authorizedRequest(t, srv, http.MethodGet, "/api/template", "")
	getResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get template: %d %s", getResponse.Code, getResponse.Body.String())
	}
	var current struct {
		Template struct {
			Version string `json:"version"`
			YAML    string `json:"yaml"`
		} `json:"template"`
	}
	if err := json.Unmarshal(getResponse.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.Template.Version != config.TemplateVersionID(initialYAML) {
		t.Fatalf("active template version=%q", current.Template.Version)
	}
	versionsRequest := authorizedRequest(t, srv, http.MethodGet, "/api/template/versions", "")
	versionsResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(versionsResponse, versionsRequest)
	if versionsResponse.Code != http.StatusOK || !strings.Contains(versionsResponse.Body.String(), current.Template.Version) {
		t.Fatalf("active template missing from version list: %d %s", versionsResponse.Code, versionsResponse.Body.String())
	}

	invalid := strings.Replace(architectAnalystTemplateYAML, "function: architect", "function: unknown-team-role", 1)
	validate := authorizedRequest(t, srv, http.MethodPost, "/api/template/validate",
		`{"yaml":`+mustJSONTemplate(t, invalid)+`}`)
	validateResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(validateResponse, validate)
	if validateResponse.Code != http.StatusOK || !strings.Contains(validateResponse.Body.String(), "недоступна в cloud") {
		t.Fatalf("cloud role validation: %d %s", validateResponse.Code, validateResponse.Body.String())
	}

	missingAgent := strings.Replace(architectAnalystTemplateYAML,
		"    executor: human", "    executor: agent\n    agent: missing-agent", 1)
	validate = authorizedRequest(t, srv, http.MethodPost, "/api/template/validate",
		`{"yaml":`+mustJSONTemplate(t, missingAgent)+`}`)
	validateResponse = httptest.NewRecorder()
	srv.router.ServeHTTP(validateResponse, validate)
	if validateResponse.Code != http.StatusOK || !strings.Contains(validateResponse.Body.String(), "не найден в registry") {
		t.Fatalf("agent registry validation: %d %s", validateResponse.Code, validateResponse.Body.String())
	}

	validate = authorizedRequest(t, srv, http.MethodPost, "/api/template/validate",
		`{"yaml":`+mustJSONTemplate(t, architectAnalystTemplateYAML)+`}`)
	validateResponse = httptest.NewRecorder()
	srv.router.ServeHTTP(validateResponse, validate)
	if validateResponse.Code != http.StatusOK || !strings.Contains(validateResponse.Body.String(), `"valid":true`) {
		t.Fatalf("architect → analyst template validation: %d %s", validateResponse.Code, validateResponse.Body.String())
	}
	if err := srv.store.CreatePipelineRun(&store.PipelineRun{
		RunID: "existing-active", Feature: "existing", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	publish := authorizedRequest(t, srv, http.MethodPost, "/api/template/publish",
		`{"yaml":`+mustJSONTemplate(t, architectAnalystTemplateYAML)+`,"expected_version":"`+current.Template.Version+`"}`)
	publishResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(publishResponse, publish)
	if publishResponse.Code != http.StatusOK {
		t.Fatalf("publish template: %d %s", publishResponse.Code, publishResponse.Body.String())
	}
	var published struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(publishResponse.Body.Bytes(), &published); err != nil {
		t.Fatal(err)
	}
	if published.Version != config.TemplateVersionID([]byte(architectAnalystTemplateYAML)) {
		t.Fatalf("published version=%q", published.Version)
	}
	_, existingVersion, found, err := mustTemplateStore(t, target).ReadPinnedRun("existing-active")
	if err != nil || !found || existingVersion != current.Template.Version {
		t.Fatalf("pre-publication task pin: found=%t version=%q err=%v", found, existingVersion, err)
	}

	start := authorizedRequest(t, srv, http.MethodPost, "/api/runs", `{"feature":"template-test","task":"Use the active template"}`)
	startResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(startResponse, start)
	if startResponse.Code != http.StatusAccepted {
		t.Fatalf("start run: %d %s", startResponse.Code, startResponse.Body.String())
	}
	const runID = "run-created"
	_, pinnedVersion, found, err := mustTemplateStore(t, target).ReadPinnedRun(runID)
	if err != nil || !found || pinnedVersion != published.Version {
		t.Fatalf("new task pin: found=%t version=%q err=%v", found, pinnedVersion, err)
	}

	versionRequest := authorizedRequest(t, srv, http.MethodGet, "/api/runs/"+runID+"/template-version", "")
	versionResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(versionResponse, versionRequest)
	if versionResponse.Code != http.StatusOK || !strings.Contains(versionResponse.Body.String(), published.Version) {
		t.Fatalf("run template version: %d %s", versionResponse.Code, versionResponse.Body.String())
	}
}

func mustJSONTemplate(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustTemplateStore(t *testing.T, target string) *config.TemplateStore {
	t.Helper()
	store, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
