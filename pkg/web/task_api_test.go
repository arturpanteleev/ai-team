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

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
)

func TestTaskReadAPIListsTemplatesRunsEventsAndMetrics(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	templateYAML, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), templateYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"),
		WithTargetDir(target), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	templateStore, err := srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templateStore.PinCurrentForRun("run-read-api", "run-read-api"); err != nil {
		t.Fatal(err)
	}
	recorder := NewStoreRecorder(srv.store)
	recorder.RunStarted("run-read-api", "feature-read", `{"task":"review feature","mode":"auto"}`, started)
	recorder.StageStarted("run-read-api", "attempt-1", "acceptance", 1, started.Add(time.Minute))
	completedAt := started.Add(5 * time.Minute)
	recorder.StageFinished(notifier.StageResult{RunID: "run-read-api", AttemptID: "attempt-1", Name: "acceptance",
		Status: notifier.StatusPassed, StartedAt: started.Add(time.Minute), FinishedAt: completedAt})

	cases := []struct {
		name, path, want string
	}{
		{"templates", "/api/templates", `"templates":[`},
		{"run list", "/api/runs?status=running&limit=5", `"run_id":"run-read-api"`},
		{"run details", "/api/runs/run-read-api", `"attempt_id":"attempt-1"`},
		{"run events", "/api/runs/run-read-api/events?after=0", `"type":"run_started"`},
		{"global events", "/api/events?after=0", `"run_id":"run-read-api"`},
		{"flow metrics", "/api/metrics/flow?from=" + started.Add(-time.Minute).Format(time.RFC3339) + "&to=" + started.Add(24*time.Hour).Format(time.RFC3339), `"unknown_runs":1`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := newLoopbackRequest(http.MethodGet, testCase.path, nil)
			response := httptest.NewRecorder()
			srv.router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s: status=%d body=%s", testCase.path, response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), testCase.want) {
				t.Fatalf("GET %s did not contain %q: %s", testCase.path, testCase.want, response.Body.String())
			}
		})
	}
	request := newLoopbackRequest(http.MethodGet, "/api/metrics/flow.csv", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(response.Body.String(), "tokens_input,,") {
		t.Fatalf("flow CSV: status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestTaskWriteAPIRequiresAuthAndReplaysIdempotentRequests(t *testing.T) {
	const token = "test-local-web-token-0123456789abcdef0123456789abcdef"
	verifier, err := NewLocalAuthenticator(token)
	if err != nil {
		t.Fatal(err)
	}
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller), WithLocalAuthenticator(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	withoutAuth := newLoopbackRequest(http.MethodPost, "/api/runs", strings.NewReader(`{"feature":"auth","task":"task"}`))
	withoutAuthResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(withoutAuthResponse, withoutAuth)
	var authError apiResponseError
	if withoutAuthResponse.Code != http.StatusUnauthorized || json.Unmarshal(withoutAuthResponse.Body.Bytes(), &authError) != nil || authError.Code != "unauthorized" {
		t.Fatalf("write without auth should be JSON 401: %d %s", withoutAuthResponse.Code, withoutAuthResponse.Body.String())
	}

	request := authorizedRequest(t, srv, http.MethodPost, "/api/runs", `{"feature":"auth","task":"task"}`)
	request.Header.Set("Idempotency-Key", "create-task-once")
	first := httptest.NewRecorder()
	srv.router.ServeHTTP(first, request)
	if first.Code != http.StatusAccepted || controller.startCalls != 1 {
		t.Fatalf("first create: status=%d calls=%d body=%s", first.Code, controller.startCalls, first.Body.String())
	}

	replayRequest := authorizedRequest(t, srv, http.MethodPost, "/api/runs", `{"feature":"auth","task":"task"}`)
	replayRequest.Header.Set("Idempotency-Key", "create-task-once")
	replay := httptest.NewRecorder()
	srv.router.ServeHTTP(replay, replayRequest)
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || controller.startCalls != 1 {
		t.Fatalf("retry was not an exact replay: first=%d %s retry=%d %s calls=%d", first.Code, first.Body.String(), replay.Code, replay.Body.String(), controller.startCalls)
	}

	conflictRequest := authorizedRequest(t, srv, http.MethodPost, "/api/runs", `{"feature":"different","task":"task"}`)
	conflictRequest.Header.Set("Idempotency-Key", "create-task-once")
	conflict := httptest.NewRecorder()
	srv.router.ServeHTTP(conflict, conflictRequest)
	var conflictBody apiResponseError
	if conflict.Code != http.StatusConflict || json.Unmarshal(conflict.Body.Bytes(), &conflictBody) != nil || conflictBody.Code != "idempotency_conflict" || controller.startCalls != 1 {
		t.Fatalf("key reuse with different request should conflict: status=%d calls=%d body=%s", conflict.Code, controller.startCalls, conflict.Body.String())
	}

	advanced := authorizedRequest(t, srv, http.MethodPost, "/api/runs", `{"feature":"auth","task":"task","mode":"auto"}`)
	advancedResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(advancedResponse, advanced)
	var advancedError apiResponseError
	if advancedResponse.Code != http.StatusServiceUnavailable || json.Unmarshal(advancedResponse.Body.Bytes(), &advancedError) != nil ||
		advancedError.Code != "controller_action_unavailable" || controller.startCalls != 1 {
		t.Fatalf("unsupported task mode must fail closed instead of silently starting legacy mode: %d calls=%d body=%s",
			advancedResponse.Code, controller.startCalls, advancedResponse.Body.String())
	}
}

func TestTaskActionInvalidStateReturnsAllowedActions(t *testing.T) {
	const token = "test-local-web-token-0123456789abcdef0123456789abcdef"
	verifier, err := NewLocalAuthenticator(token)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	yamlData, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), yamlData, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"),
		WithTargetDir(target), WithRunController(&fakeRunController{}), WithLocalAuthenticator(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	templateStore, err := srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	store, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state := lifecycle.State{RunID: "run-invalid-state", Feature: "feature", TargetDir: target, Task: "task",
		Phase: lifecycle.PhaseRunning, NextStage: cfg.Stages[0].ID, ConfigSHA256: strings.Repeat("0", 64),
		WorkflowSHA256: strings.Repeat("1", 64), CreatedAt: time.Now().UTC()}
	if _, err := templateStore.PinCurrentForRun(state.RunID, state.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(state); err != nil {
		t.Fatal(err)
	}

	request := authorizedRequest(t, srv, http.MethodPost,
		"/api/runs/"+state.RunID+"/stages/"+state.NextStage+"/skip", `{"reason":"skip for test"}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	var body struct {
		Error          string   `json:"error"`
		Code           string   `json:"code"`
		AllowedActions []string `json:"allowed_actions"`
	}
	if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &body) != nil ||
		body.Code != "invalid_state" || body.AllowedActions == nil {
		t.Fatalf("invalid-state response must carry a JSON allowed_actions array: %d %s", response.Code, response.Body.String())
	}
}
