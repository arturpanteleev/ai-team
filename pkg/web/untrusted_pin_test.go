package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/autopickup"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const untrustedActiveYAML = "schema_version: 6\ntemplate: t\ntitle: T\nstages:\n  - {id: spec, title: Spec, function: po, result: md, executor: human}\n"

const untrustedOldYAML = `schema_version: 6
template: t
title: Old
stages:
  - id: spec
    title: Spec
    function: po
    result: md
    executor: human
    exit_gate_groups:
      - - gate: external_program
          version: 1
          name: evil
          params: {command: ["sh", "-c", "id"], timeout_seconds: 30}
`

// A run pinned (before the trusted-config rule) to a template with a host
// command gate is refused on resume, with the machine-readable code, before the
// controller or the authorship record is touched. The MCP resume_task tool is
// the same REST route.
func TestResumeRefusesRunPinnedToCommandGateTemplate(t *testing.T) {
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), []byte(untrustedActiveYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	templates, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templates.PinDataForRun("run-old", "run-old", []byte(untrustedOldYAML)); err != nil {
		t.Fatal(err)
	}
	if _, err := templates.PinDataForRun("run-fine", "run-fine", []byte(untrustedActiveYAML)); err != nil {
		t.Fatal(err)
	}
	srv, controller := newPickupServer(t, ":memory:", target, autopickup.Policy{}, &fixedLedger{}, pickupTestNow)
	for _, id := range []string{"run-old", "run-fine"} {
		if err := srv.store.CreatePipelineRun(&store.PipelineRun{RunID: id, Feature: "f", Status: "stopped", StartedAt: pickupTestNow}); err != nil {
			t.Fatal(err)
		}
	}

	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, authorizedRequest(t, srv, http.MethodPost, "/api/runs/run-old/resume", `{}`))
	if response.Code != http.StatusConflict {
		t.Fatalf("resume of an untrusted pin: %d %s", response.Code, response.Body.String())
	}
	var body struct{ Code, Error string }
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Code != config.ErrCodeTrustedConfigOnly {
		t.Fatalf("want code %s, got %s", config.ErrCodeTrustedConfigOnly, response.Body.String())
	}
	if controller.resumeCalls != 0 {
		t.Fatal("the controller must not be asked to resume")
	}
	if event, _ := srv.store.LatestRunEvent("run-old", "run_resume_requested"); event != nil {
		t.Fatal("a refused resume must leave no audit record of a launch")
	}

	response = httptest.NewRecorder()
	srv.router.ServeHTTP(response, authorizedRequest(t, srv, http.MethodPost, "/api/runs/run-fine/resume", `{}`))
	if response.Code != http.StatusAccepted || controller.resumeCalls != 1 {
		t.Fatalf("a pin equal to the active template must resume: %d %s", response.Code, response.Body.String())
	}
}
