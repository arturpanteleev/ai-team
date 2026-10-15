package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/autopickup"
)

func TestGateCatalogIsReadOnlyAndListsPinnedDefinitions(t *testing.T) {
	srv, _ := newPickupServer(t, ":memory:", t.TempDir(), autopickup.Policy{}, &fixedLedger{}, time.Now().UTC())
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/gates/catalog", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("catalog: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Gates []struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
			Kind    string `json:"kind"`
			Digest  string `json:"digest"`
			Latest  bool   `json:"latest"`
		} `json:"gates"`
		Kinds []string `json:"kinds"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, gate := range body.Gates {
		// The decision gates have a second published version (S05); every
		// other gate has only version 1. Only the highest version is latest.
		latestVersion := 1
		if gate.ID == "agent_review" || gate.ID == "human_approval" {
			latestVersion = 2
		}
		if gate.Digest == "" || gate.Version < 1 || gate.Version > latestVersion || gate.Latest != (gate.Version == latestVersion) {
			t.Fatalf("incomplete catalog entry: %+v", gate)
		}
		found[gate.ID+"/"+gate.Kind] = true
	}
	for _, want := range []string{"required_sections/builtin", "verdict_marker/builtin", "agent_review/llm", "human_approval/human", "pr_checks_green/builtin", "required_phrases/builtin", "external_program/external", "docker_program/docker"} {
		if !found[want] {
			t.Fatalf("catalog lacks %s: %v", want, found)
		}
	}
	if len(body.Kinds) != 5 {
		t.Fatalf("kinds = %v", body.Kinds)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, "http://127.0.0.1/api/gates/catalog", strings.NewReader("{}"))
		srv.router.ServeHTTP(recorder, request)
		if recorder.Code < 400 {
			t.Fatalf("%s must not be accepted on the catalog, got %d", method, recorder.Code)
		}
	}
}
