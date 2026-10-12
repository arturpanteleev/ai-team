package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
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
		{"flow metrics", "/api/metrics/flow?from=" + started.Add(-time.Minute).Format(time.RFC3339) + "&to=" + started.Add(24*time.Hour).Format(time.RFC3339), `"tokens_input":null`},
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
		!strings.Contains(response.Body.String(), `tokens_input,"",,"tokens","unknown","metrics.UsageEnvelope"`) {
		t.Fatalf("flow CSV: status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestGlobalEventFeedPaginatesWarningsByPersistedCursor(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	run := &store.PipelineRun{RunID: "run-global-feed", Feature: "feed demo", Status: "running", StartedAt: startedAt}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	storedEvents := []*store.Event{
		{RunID: run.RunID, Type: "run_started", Timestamp: startedAt, DataJSON: `{"feature":"feed demo"}`},
		{RunID: run.RunID, Type: "check_error", Stage: "qa", Severity: "warning", Timestamp: startedAt.Add(time.Second), DataJSON: `{"reason":"check failed"}`},
		{RunID: run.RunID, Type: "attempt_started", Stage: "review", Timestamp: startedAt.Add(2 * time.Second), DataJSON: `{}`},
		{RunID: run.RunID, Type: "stage_skipped", Stage: "review", Timestamp: startedAt.Add(3 * time.Second), DataJSON: `{"reason":"not required"}`},
	}
	for _, event := range storedEvents {
		if err := srv.store.AppendEventNext(event); err != nil {
			t.Fatal(err)
		}
	}

	request := newLoopbackRequest(http.MethodGet, "/api/events?after=0&limit=10", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	var allPage struct {
		Events     []Event `json:"events"`
		NextCursor int64   `json:"next_cursor"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &allPage) != nil || len(allPage.Events) != len(storedEvents) {
		t.Fatalf("all event page: status=%d body=%s events=%+v", response.Code, response.Body.String(), allPage.Events)
	}
	if allPage.Events[0].Type != "run_started" || allPage.Events[3].Type != "stage_skipped" || allPage.NextCursor != storedEvents[3].ID {
		t.Fatalf("all event cursor order=%+v next=%d", allPage.Events, allPage.NextCursor)
	}

	firstWarningRequest := newLoopbackRequest(http.MethodGet, "/api/events?after=0&limit=1&warnings=true", nil)
	firstWarningResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(firstWarningResponse, firstWarningRequest)
	var firstWarningPage struct {
		Events     []Event `json:"events"`
		NextCursor int64   `json:"next_cursor"`
	}
	if firstWarningResponse.Code != http.StatusOK || json.Unmarshal(firstWarningResponse.Body.Bytes(), &firstWarningPage) != nil ||
		len(firstWarningPage.Events) != 1 || firstWarningPage.Events[0].Type != "check_error" ||
		firstWarningPage.NextCursor != storedEvents[1].ID {
		t.Fatalf("first warning page: status=%d body=%s page=%+v", firstWarningResponse.Code, firstWarningResponse.Body.String(), firstWarningPage)
	}

	secondWarningRequest := newLoopbackRequest(http.MethodGet, fmt.Sprintf("/api/events?after=%d&limit=1&warnings=true", firstWarningPage.NextCursor), nil)
	secondWarningResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(secondWarningResponse, secondWarningRequest)
	var secondWarningPage struct {
		Events     []Event `json:"events"`
		NextCursor int64   `json:"next_cursor"`
	}
	if secondWarningResponse.Code != http.StatusOK || json.Unmarshal(secondWarningResponse.Body.Bytes(), &secondWarningPage) != nil ||
		len(secondWarningPage.Events) != 1 || secondWarningPage.Events[0].Type != "stage_skipped" ||
		secondWarningPage.NextCursor != storedEvents[3].ID {
		t.Fatalf("second warning page: status=%d body=%s page=%+v", secondWarningResponse.Code, secondWarningResponse.Body.String(), secondWarningPage)
	}
}

func TestGlobalEventFeedPagesNewestFirstWithBeforeCursor(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	run := &store.PipelineRun{RunID: "run-desc-feed", Feature: "feed demo", Status: "running", StartedAt: startedAt}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	storedEvents := []*store.Event{
		{RunID: run.RunID, Type: "run_started", Timestamp: startedAt, DataJSON: `{}`},
		{RunID: run.RunID, Type: "check_error", Stage: "qa", Severity: "warning", Timestamp: startedAt.Add(time.Second), DataJSON: `{}`},
		{RunID: run.RunID, Type: "attempt_started", Stage: "review", Timestamp: startedAt.Add(2 * time.Second), DataJSON: `{}`},
		{RunID: run.RunID, Type: "stage_skipped", Stage: "review", Timestamp: startedAt.Add(3 * time.Second), DataJSON: `{}`},
	}
	for _, event := range storedEvents {
		if err := srv.store.AppendEventNext(event); err != nil {
			t.Fatal(err)
		}
	}
	type page struct {
		Events     []Event `json:"events"`
		NextCursor int64   `json:"next_cursor"`
	}
	get := func(path string) (int, page) {
		t.Helper()
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, newLoopbackRequest(http.MethodGet, path, nil))
		var value page
		if response.Code == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
				t.Fatalf("decode %s: %v body=%s", path, err, response.Body.String())
			}
		}
		return response.Code, value
	}
	cursors := func(events []Event) []int64 {
		values := make([]int64, 0, len(events))
		for _, event := range events {
			values = append(values, event.Cursor)
		}
		return values
	}

	status, newest := get("/api/events?order=desc&limit=2")
	if status != http.StatusOK || fmt.Sprint(cursors(newest.Events)) != fmt.Sprint([]int64{storedEvents[3].ID, storedEvents[2].ID}) ||
		newest.NextCursor != storedEvents[2].ID {
		t.Fatalf("newest page status=%d page=%+v", status, newest)
	}
	status, older := get(fmt.Sprintf("/api/events?order=desc&limit=2&before=%d", newest.NextCursor))
	if status != http.StatusOK || fmt.Sprint(cursors(older.Events)) != fmt.Sprint([]int64{storedEvents[1].ID, storedEvents[0].ID}) ||
		older.NextCursor != storedEvents[0].ID {
		t.Fatalf("older page status=%d page=%+v", status, older)
	}
	status, warnings := get("/api/events?order=desc&limit=1&warnings=true")
	if status != http.StatusOK || len(warnings.Events) != 1 || warnings.Events[0].Type != "stage_skipped" {
		t.Fatalf("newest warning status=%d page=%+v", status, warnings)
	}
	status, olderWarnings := get(fmt.Sprintf("/api/events?order=desc&limit=1&warnings=true&before=%d", warnings.NextCursor))
	if status != http.StatusOK || len(olderWarnings.Events) != 1 || olderWarnings.Events[0].Type != "check_error" ||
		olderWarnings.NextCursor != storedEvents[1].ID {
		t.Fatalf("older warning status=%d page=%+v", status, olderWarnings)
	}
	status, runPage := get(fmt.Sprintf("/api/runs/%s/events?order=desc&limit=1", run.RunID))
	if status != http.StatusOK || len(runPage.Events) != 1 || runPage.Events[0].Cursor != storedEvents[3].ID {
		t.Fatalf("run newest page status=%d page=%+v", status, runPage)
	}
	for _, path := range []string{
		"/api/events?before=3",
		"/api/events?order=desc&after=1",
		"/api/events?order=desc&after=1&before=3",
		"/api/events?order=sideways",
		"/api/events?order=desc&before=-1",
	} {
		if status, _ := get(path); status != http.StatusBadRequest {
			t.Fatalf("GET %s status=%d, want 400", path, status)
		}
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

type taskCreateCapturingController struct {
	*fakeRunController
	feature, task, title, idea, mode string
	externalLinks                    []string
}

func (f *taskCreateCapturingController) StartTask(_ context.Context, feature, task, title, idea, mode string,
	externalLinks []string, admit func(string) error,
) (string, error) {
	f.feature, f.task, f.title, f.idea, f.mode, f.externalLinks = feature, task, title, idea, mode, externalLinks
	if err := admit("run-created"); err != nil {
		return "", err
	}
	return f.Start(feature, task)
}

func TestTaskCreateOptionsReachController(t *testing.T) {
	const token = "test-local-web-token-0123456789abcdef0123456789abcdef"
	verifier, err := NewLocalAuthenticator(token)
	if err != nil {
		t.Fatal(err)
	}
	controller := &taskCreateCapturingController{fakeRunController: &fakeRunController{}}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller), WithLocalAuthenticator(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	body := `{"feature":"board-task","task":"legacy text","title":"Новая задача","idea":"Текст идеи","mode":"auto","external_links":["JIRA-1 https://tracker.example/JIRA-1"]}`
	request := authorizedRequest(t, srv, http.MethodPost, "/api/runs", body)
	request.Header.Set("Idempotency-Key", "create-full-task")
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || controller.startCalls != 1 {
		t.Fatalf("task create with metadata: status=%d calls=%d body=%s", response.Code, controller.startCalls, response.Body.String())
	}
	if controller.title != "Новая задача" || controller.idea != "Текст идеи" || controller.mode != "auto" ||
		len(controller.externalLinks) != 1 || controller.externalLinks[0] != "JIRA-1 https://tracker.example/JIRA-1" {
		t.Fatalf("controller metadata: title=%q idea=%q mode=%q links=%v", controller.title, controller.idea, controller.mode, controller.externalLinks)
	}
	run := httptest.NewRecorder()
	srv.router.ServeHTTP(run, authorizedRequest(t, srv, http.MethodGet, "/api/runs/run-created", ""))
	if run.Code != http.StatusOK || !strings.Contains(run.Body.String(), `"config_snapshot"`) {
		t.Fatalf("created task details: status=%d body=%s", run.Code, run.Body.String())
	}
}

func TestTaskListCarriesStageWarningsAndLinks(t *testing.T) {
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
	if _, err := templateStore.PinCurrentForRun("run-board", "run-board"); err != nil {
		t.Fatal(err)
	}
	recorder := NewStoreRecorder(srv.store)
	recorder.RunStarted("run-board", "feature-board",
		`{"task":"board idea","mode":"manual","external_links":["JIRA-7 https://tracker.example/JIRA-7"]}`, started)
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	stageEntered := started.Add(2 * time.Minute).Truncate(time.Second)
	if err := stateStore.Create(lifecycle.State{
		RunID: "run-board", Feature: "feature-board", TargetDir: target, Task: "board idea",
		Phase: lifecycle.PhaseResumable, NextStage: "acceptance",
		ConfigSHA256:   strings.Repeat("a", 64),
		WorkflowSHA256: strings.Repeat("b", 64),
		CreatedAt:      stageEntered,
	}); err != nil {
		t.Fatal(err)
	}
	warningAt := stageEntered.Add(time.Minute)
	if err := srv.store.AppendEventNext(&store.Event{RunID: "run-board", Type: "check_overridden",
		Timestamp: warningAt, DataJSON: `{"severity":"warning"}`}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, newLoopbackRequest(http.MethodGet, "/api/runs?limit=10", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("task list: status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Runs []taskRunListItem `json:"runs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	var found *taskRunListItem
	for index := range payload.Runs {
		if payload.Runs[index].RunID == "run-board" {
			found = &payload.Runs[index]
			break
		}
	}
	if found == nil {
		t.Fatalf("run-board отсутствует в списке: %s", response.Body.String())
	}
	if found.ID == 0 || found.Warnings != 1 {
		t.Fatalf("list item identity and warnings: id=%d warnings=%d", found.ID, found.Warnings)
	}
	if found.StageEnteredAt == nil || !found.StageEnteredAt.Equal(stageEntered) {
		t.Fatalf("stage entry time: %v (want %v)", found.StageEnteredAt, stageEntered)
	}
	if len(found.ExternalLinks) != 1 || found.ExternalLinks[0] != "JIRA-7 https://tracker.example/JIRA-7" {
		t.Fatalf("external links: %v", found.ExternalLinks)
	}
	if found.CurrentStage != "acceptance" {
		t.Fatalf("current stage: %q", found.CurrentStage)
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

func TestTaskConfirmPinsAndReusesFirstVotedArtifactRevision(t *testing.T) {
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
	const runID, stageID = "run-confirm", "tech_design"
	relative := "attempts/attempt-1/artifacts/design.md"
	controller := &fakeRunController{approvals: []approval.PendingApproval{{
		ID: "approval-confirm", RunID: runID, AttemptID: "attempt-1", Status: approval.StatusPending,
		FromStage: stageID, ToStage: stageID, Trigger: "stage_completed", SubjectHash: testSubjectHash,
		RequiredRoles: []string{"architect"}, Actions: []string{"approve"},
		Targets: map[string]string{"approve": stageID},
	}}}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"),
		WithTargetDir(target), WithRunController(controller), WithLocalAuthenticator(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	templateStore, err := srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templateStore.PinCurrentForRun(runID, runID); err != nil {
		t.Fatal(err)
	}
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Create(lifecycle.State{RunID: runID, Feature: "feature", TargetDir: target, Task: "task",
		Phase: lifecycle.PhaseWaiting, NextStage: stageID, PendingApprovalID: "approval-confirm",
		ConfigSHA256: strings.Repeat("0", 64), WorkflowSHA256: strings.Repeat("1", 64), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(target, ".ai-team", "runs", runID)
	source := []byte("source design\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(runRoot, relative)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, relative), source, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"run_id":%q,"attempt_id":"attempt-1","outputs":[{"name":"design","evidence_path":%q}]}`, runID, relative)
	if err := os.MkdirAll(filepath.Join(runRoot, "attempts", "attempt-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "attempts", "attempt-1", "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	humanStore, err := humanartifact.New(target)
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("%x", sha256.Sum256(source))
	pinned, err := humanStore.Append(runID, relative, "", base, "design A\n", "first voter selection", "architect-1")
	if err != nil {
		t.Fatal(err)
	}

	first := authorizedRequest(t, srv, http.MethodPost, "/api/runs/"+runID+"/stages/"+stageID+"/confirm", "")
	firstResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusAccepted || controller.decision.ArtifactRevisions[relative] != pinned.ID {
		t.Fatalf("first confirm should pin the reviewed revision: code=%d selection=%v body=%s",
			firstResponse.Code, controller.decision.ArtifactRevisions, firstResponse.Body.String())
	}
	// Model the durable post-vote state, then append a newer human edit while
	// the approval stays pending.
	controller.approvals[0].Decisions = []approval.Decision{{
		ActorID: "architect-1", ActorRole: "architect", Action: "approve",
		ArtifactRevisions: map[string]string{relative: pinned.ID},
	}}
	newer, err := humanStore.Append(runID, relative, pinned.ID, pinned.SHA256, "design B\n", "later edit", "architect-2")
	if err != nil {
		t.Fatal(err)
	}
	if newer.ID == pinned.ID {
		t.Fatal("test setup expected a distinct newer revision")
	}

	second := authorizedRequest(t, srv, http.MethodPost, "/api/runs/"+runID+"/stages/"+stageID+"/confirm", "")
	secondResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusAccepted || controller.decision.ArtifactRevisions[relative] != pinned.ID {
		t.Fatalf("later confirm must reuse the first pinned revision instead of the newer edit: code=%d selection=%v body=%s",
			secondResponse.Code, controller.decision.ArtifactRevisions, secondResponse.Body.String())
	}
}

func TestTaskStageSkipReachesConnectedController(t *testing.T) {
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
	controller := &actionRecordingController{fakeRunController: &fakeRunController{}}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"),
		WithTargetDir(target), WithRunController(controller), WithLocalAuthenticator(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	templateStore, err := srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templateStore.PinCurrentForRun("run-skip", "run-skip"); err != nil {
		t.Fatal(err)
	}
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Create(lifecycle.State{RunID: "run-skip", Feature: "feature", TargetDir: target, Task: "task",
		Phase: lifecycle.PhaseResumable, NextStage: "tech_design",
		ConfigSHA256: strings.Repeat("0", 64), WorkflowSHA256: strings.Repeat("1", 64), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	request := authorizedRequest(t, srv, http.MethodPost, "/api/runs/run-skip/stages/tech_design/skip", `{"reason":"not needed"}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || controller.skipCalls != 1 ||
		controller.skipRunID != "run-skip" || controller.skipStageID != "tech_design" ||
		controller.skipReason != "not needed" || controller.skipActorRole != "architect" {
		t.Fatalf("skip action did not reach the connected controller: code=%d calls=%d run=%q stage=%q reason=%q role=%q body=%s",
			response.Code, controller.skipCalls, controller.skipRunID, controller.skipStageID,
			controller.skipReason, controller.skipActorRole, response.Body.String())
	}
}

type actionRecordingController struct {
	*fakeRunController
	skipCalls                                                      int
	skipRunID, skipStageID, skipReason, skipActorID, skipActorRole string
}

func (c *actionRecordingController) SkipStage(_ context.Context, runID, stageID, reason, actorID, actorRole string) error {
	c.skipCalls++
	c.skipRunID, c.skipStageID, c.skipReason = runID, stageID, reason
	c.skipActorID, c.skipActorRole = actorID, actorRole
	return nil
}

func TestTaskPlanApprovalUsesExistingControllerSemanticsAndIdempotency(t *testing.T) {
	const token = "test-local-web-token-0123456789abcdef0123456789abcdef"
	verifier, err := NewLocalAuthenticator(token)
	if err != nil {
		t.Fatal(err)
	}
	planHash := strings.Repeat("a", 64)
	controller := &fakeRunController{approvals: []approval.PendingApproval{{
		ID: "plan-approval", RunID: "run-plan", Status: approval.StatusPending,
		Trigger: "delivery_plan", SubjectHash: planHash, RequiredRoles: []string{"release_manager"},
		Actions: []string{"approve"},
	}}}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller), WithLocalAuthenticator(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	malicious := authorizedRequest(t, srv, http.MethodPost, "/api/runs/run-plan/plan/approve",
		`{"plan_hash":"`+planHash+`","actor_id":"spoofed"}`)
	maliciousResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(maliciousResponse, malicious)
	if maliciousResponse.Code != http.StatusBadRequest || controller.decisionCalls != 0 || controller.resumeCalls != 0 {
		t.Fatalf("request body must not supply actor identity: status=%d decisions=%d resumes=%d body=%s",
			maliciousResponse.Code, controller.decisionCalls, controller.resumeCalls, maliciousResponse.Body.String())
	}

	request := authorizedRequest(t, srv, http.MethodPost, "/api/runs/run-plan/plan/approve", `{"plan_hash":"`+planHash+`"}`)
	request.Header.Set("Idempotency-Key", "approve-plan-once")
	first := httptest.NewRecorder()
	srv.router.ServeHTTP(first, request)
	if first.Code != http.StatusAccepted || controller.decisionCalls != 1 || controller.resumeCalls != 1 ||
		controller.decision.ActorID != "local-user" || controller.decision.ActorRole != "release_manager" {
		t.Fatalf("plan approval did not use server identity and controller: status=%d decision=%+v decisions=%d resumes=%d body=%s",
			first.Code, controller.decision, controller.decisionCalls, controller.resumeCalls, first.Body.String())
	}

	replayRequest := authorizedRequest(t, srv, http.MethodPost, "/api/runs/run-plan/plan/approve", `{"plan_hash":"`+planHash+`"}`)
	replayRequest.Header.Set("Idempotency-Key", "approve-plan-once")
	replay := httptest.NewRecorder()
	srv.router.ServeHTTP(replay, replayRequest)
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || controller.decisionCalls != 1 || controller.resumeCalls != 1 {
		t.Fatalf("plan approval retry repeated controller action: first=%d retry=%d decisions=%d resumes=%d",
			first.Code, replay.Code, controller.decisionCalls, controller.resumeCalls)
	}
}

func TestFlowMetricsReportsPerStageUsageAndAgentEdits(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Usage = &config.UsageConfig{MonthlySubscriptionAmount: 50, MonthlySubscriptionCurrency: "USD"}
	configData, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), configData, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"),
		WithTargetDir(target), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	run := &store.PipelineRun{RunID: "run-flow-metrics", Feature: "feature", Status: "completed", StartedAt: started}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	agentFinished, humanStarted := started.Add(10*time.Minute), started.Add(11*time.Minute)
	completed := agentFinished.Add(time.Minute)
	stages := []store.Stage{
		{PipelineRunID: run.ID, AttemptID: "attempt-agent", StageIndex: 1, AgentName: "implementation", Status: "passed",
			Execution: "agent", Outcome: "passed", StartedAt: started.Add(9 * time.Minute), CompletedAt: &agentFinished, DurationMs: 60_000},
		{PipelineRunID: run.ID, AttemptID: "attempt-human", StageIndex: 2, AgentName: "implementation", Status: "passed",
			Execution: "human", Outcome: "passed", StartedAt: humanStarted, CompletedAt: &completed, DurationMs: 60_000},
		{PipelineRunID: run.ID, AttemptID: "attempt-review", StageIndex: 3, AgentName: "review", Status: "passed",
			Execution: "agent", Outcome: "passed", StartedAt: completed.Add(time.Second), CompletedAt: timePointer(completed.Add(2 * time.Minute)), DurationMs: 120_000},
	}
	for index := range stages {
		if err := srv.store.CreateStage(&stages[index]); err != nil {
			t.Fatal(err)
		}
		if err := srv.store.UpdateStage(&stages[index]); err != nil {
			t.Fatal(err)
		}
	}
	manifestDir := filepath.Join(srv.runRoot, run.RunID, "attempts", "attempt-agent")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(evidence.AttemptManifest{RunID: run.RunID, AttemptID: "attempt-agent",
		Usage: &workflow.AttemptUsage{Attested: true, TokensInput: 12, TokensOutput: 8, CostUSD: 0.01}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := srv.buildFlowMetrics(started.Add(-time.Minute), started.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentResults != 2 || result.AgentAcceptedWithoutEdit != 1 || result.AgentAcceptedWithoutEditRate == nil || *result.AgentAcceptedWithoutEditRate != 0.5 {
		t.Fatalf("agent acceptance metric=%+v", result)
	}
	var implementation, review *flowStageStats
	for index := range result.ByStage {
		switch result.ByStage[index].Stage {
		case "implementation":
			implementation = &result.ByStage[index]
		case "review":
			review = &result.ByStage[index]
		}
	}
	if implementation == nil || implementation.TokensInput == nil || *implementation.TokensInput != 12 ||
		implementation.TokensOutput == nil || *implementation.TokensOutput != 8 ||
		implementation.UsageKnownAttempts != 1 || implementation.UsageUnknownAttempts != 0 ||
		implementation.UsageAttestation != "complete" || implementation.UsageSource != "attempt_manifest" {
		t.Fatalf("implementation usage=%+v", implementation)
	}
	if review == nil || review.UsageUnknownAttempts != 1 || review.UsageAttestation != "unknown" ||
		result.Usage.UnknownRuns != 1 || result.Usage.TokensInput != nil || result.Usage.SubscriptionShareEstimate != nil {
		t.Fatalf("unknown usage was not explicit: review=%+v totals=%+v", review, result.Usage)
	}
}

func TestFlowMetricsUsesSubscriptionShareAndPreservesUsageProvenance(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Usage = &config.UsageConfig{MonthlySubscriptionAmount: 100, MonthlySubscriptionCurrency: "USD"}
	configData, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), configData, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"), WithTargetDir(target), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	month := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	first := store.PipelineRun{RunID: "run-usage-first", Feature: "usage", Status: "completed", StartedAt: month,
		CompletedAt: timePointer(month.Add(time.Hour))}
	peer := store.PipelineRun{RunID: "run-usage-peer", Feature: "usage", Status: "completed", StartedAt: month.Add(24 * time.Hour),
		CompletedAt: timePointer(month.Add(25 * time.Hour))}
	envelopeStore := metrics.FileUsageEnvelopeStore{}
	for _, run := range []store.PipelineRun{first, peer} {
		if err := srv.store.CreatePipelineRun(&run); err != nil {
			t.Fatal(err)
		}
	}
	writeEnvelope := func(runID string, startedAt, finishedAt time.Time, input, output int64) {
		t.Helper()
		envelope := metrics.UsageEnvelope{SchemaVersion: metrics.SchemaVersion, RunID: runID, Feature: "usage",
			StartedAt: startedAt, FinishedAt: finishedAt, UsageReported: true, TokensInput: input, TokensOutput: output, Outcome: "completed"}
		if runID == first.RunID {
			runDir := filepath.Join(controlDir, "runs", runID)
			if err := os.MkdirAll(runDir, 0o755); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(runDir, "usage.json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := envelopeStore.Reserve(target, runID); err != nil {
			t.Fatal(err)
		}
		if err := envelopeStore.Write(target, runID, envelope); err != nil {
			t.Fatal(err)
		}
	}
	writeEnvelope(first.RunID, month, month.Add(time.Hour), 40, 60)
	writeEnvelope(peer.RunID, month.Add(24*time.Hour), month.Add(25*time.Hour), 200, 300)

	result, err := srv.buildFlowMetrics(month.Add(-time.Minute), month.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.KnownRuns != 1 || result.Usage.UnknownRuns != 0 || result.Usage.Attestation != "complete" ||
		result.Usage.Source != "metrics.UsageEnvelope" || result.Usage.TokensInput == nil || *result.Usage.TokensInput != 40 ||
		result.Usage.TokensOutput == nil || *result.Usage.TokensOutput != 60 {
		t.Fatalf("run usage lost token attestation/provenance: %+v", result.Usage)
	}
	if result.Usage.SubscriptionShareEstimate == nil || *result.Usage.SubscriptionShareEstimate < 16.6666 || *result.Usage.SubscriptionShareEstimate > 16.6667 ||
		result.Usage.SubscriptionCurrency != "USD" || result.Usage.EstimateSource != "project_config+metrics.UsageEnvelope" ||
		result.Usage.EstimateBasis != "monthly_subscription_weighted_by_attested_tokens" {
		t.Fatalf("subscription share estimate=%+v, want ≈16.67 USD from 100/600 tokens", result.Usage)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"cost_usd"`) {
		t.Fatalf("flow API must not report provider token prices as subscription cost: %s", encoded)
	}
	request := newLoopbackRequest(http.MethodGet, "/api/metrics/flow.csv?from="+month.Add(-time.Minute).Format(time.RFC3339)+"&to="+month.Add(2*time.Hour).Format(time.RFC3339), nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `subscription_share_estimate,"",16.666667,"USD","estimated","project_config+metrics.UsageEnvelope","monthly_subscription_weighted_by_attested_tokens"`) ||
		!strings.Contains(response.Body.String(), `"complete","metrics.UsageEnvelope"`) {
		t.Fatalf("flow CSV lost estimate provenance: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFlowMetricsBreaksDownStagesStatesAndSubscriptionShare(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Usage = &config.UsageConfig{MonthlySubscriptionAmount: 90, MonthlySubscriptionCurrency: "USD"}
	configData, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), configData, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"), WithTargetDir(target), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	type attemptFixture struct {
		id, stage, execution, decision, outcome string
		index                                   int
		started, completed                      time.Time
		tokensInput, tokensOutput               int64
		withUsage                               bool
	}
	createRun := func(runID string, start time.Time, runInput, runOutput int64, attempts []attemptFixture) {
		t.Helper()
		finished := start.Add(time.Hour)
		run := &store.PipelineRun{RunID: runID, Feature: "flow", Status: "completed", StartedAt: start, CompletedAt: &finished}
		if err := srv.store.CreatePipelineRun(run); err != nil {
			t.Fatal(err)
		}
		for _, attempt := range attempts {
			stage := store.Stage{PipelineRunID: run.ID, AttemptID: attempt.id, StageIndex: attempt.index, AgentName: attempt.stage,
				Status: attempt.outcome, Execution: attempt.execution, Decision: attempt.decision, Outcome: attempt.outcome,
				StartedAt: attempt.started, CompletedAt: &attempt.completed, DurationMs: attempt.completed.Sub(attempt.started).Milliseconds()}
			if err := srv.store.CreateStage(&stage); err != nil {
				t.Fatal(err)
			}
			if err := srv.store.UpdateStage(&stage); err != nil {
				t.Fatal(err)
			}
			if attempt.withUsage {
				attemptDir := filepath.Join(srv.runRoot, runID, "attempts", attempt.id)
				if err := os.MkdirAll(attemptDir, 0o755); err != nil {
					t.Fatal(err)
				}
				manifest, marshalErr := json.Marshal(evidence.AttemptManifest{RunID: runID, AttemptID: attempt.id,
					Usage: &workflow.AttemptUsage{Attested: true, TokensInput: attempt.tokensInput, TokensOutput: attempt.tokensOutput, CostUSD: 12.34}})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if err := os.WriteFile(filepath.Join(attemptDir, "manifest.json"), manifest, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		envelope := metrics.UsageEnvelope{SchemaVersion: metrics.SchemaVersion, RunID: runID, Feature: "flow", StartedAt: start,
			FinishedAt: finished, UsageReported: true, TokensInput: runInput, TokensOutput: runOutput, Outcome: "completed"}
		runDir := filepath.Join(controlDir, "runs", runID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatal(err)
		}
		encoded, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := os.WriteFile(filepath.Join(runDir, "usage.json"), encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	createRun("flow-first", month, 30, 20, []attemptFixture{
		{id: "impl-agent-first", stage: "implementation", execution: "agent", decision: "approved", outcome: "passed", index: 1,
			started: month.Add(6 * time.Minute), completed: month.Add(10 * time.Minute), tokensInput: 20, tokensOutput: 10, withUsage: true},
		{id: "impl-human-first", stage: "implementation", execution: "human", decision: "approved", outcome: "passed", index: 2,
			started: month.Add(10 * time.Minute), completed: month.Add(14 * time.Minute)},
		{id: "accept-first", stage: "acceptance", execution: "agent", decision: "approved", outcome: "passed", index: 3,
			started: month.Add(20 * time.Minute), completed: month.Add(30 * time.Minute), tokensInput: 10, tokensOutput: 10, withUsage: true},
	})
	createRun("flow-second", month.Add(2*time.Hour), 60, 40, []attemptFixture{
		{id: "impl-agent-second", stage: "implementation", execution: "agent", decision: "rejected", outcome: "failed", index: 1,
			started: month.Add(2*time.Hour + 2*time.Minute), completed: month.Add(2*time.Hour + 8*time.Minute), tokensInput: 40, tokensOutput: 20, withUsage: true},
		{id: "accept-second", stage: "acceptance", execution: "agent", decision: "approved", outcome: "passed", index: 2,
			started: month.Add(2*time.Hour + 10*time.Minute), completed: month.Add(2*time.Hour + 20*time.Minute), tokensInput: 20, tokensOutput: 20, withUsage: true},
	})
	createRun("flow-month-peer", month.Add(4*time.Hour), 200, 100, nil)
	for _, event := range []store.Event{
		{RunID: "flow-first", Type: "stage_taken", Stage: "implementation", Timestamp: month.Add(5 * time.Minute)},
		{RunID: "flow-second", Type: "agent_started", Stage: "implementation", Timestamp: month.Add(2*time.Hour + 2*time.Minute)},
		{RunID: "flow-first", Type: "check_overridden", Stage: "implementation", Timestamp: month.Add(11 * time.Minute)},
		{RunID: "flow-first", Type: "stage_skipped", Stage: "review", Timestamp: month.Add(12 * time.Minute)},
		{RunID: "flow-first", Type: "transition_selected", Stage: "review", AttemptID: "return-attempt",
			Timestamp: month.Add(13 * time.Minute), DataJSON: `{"action":"return_to_implementation","from":"review","target":"implementation"}`},
		{RunID: "flow-first", Type: "stage_returned", Stage: "review", AttemptID: "return-attempt", Timestamp: month.Add(13 * time.Minute)},
		{RunID: "flow-first", Type: "attempt_finished", Stage: "implementation", AttemptID: "impl-human-first",
			Timestamp: month.Add(14 * time.Minute), DataJSON: `{"human_edit_of_agent_attempt_id":"impl-agent-first"}`},
	} {
		if err := srv.store.AppendEventNext(&event); err != nil {
			t.Fatal(err)
		}
	}

	result, err := srv.buildFlowMetrics(month, month.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.LeadTime.Count != 2 || result.LeadTime.MedianMS == nil || *result.LeadTime.MedianMS != 25*60*1000 || result.LeadTime.P80MS == nil || *result.LeadTime.P80MS != 30*60*1000 {
		t.Fatalf("lead time stats=%+v, want median 25m and p80 30m", result.LeadTime)
	}
	if result.CycleTime.Count != 2 || result.CycleTime.MedianMS == nil || *result.CycleTime.MedianMS != 21*60*1000+30*1000 || result.CycleTime.P80MS == nil || *result.CycleTime.P80MS != 25*60*1000 {
		t.Fatalf("cycle time stats=%+v, want median 21.5m and p80 25m", result.CycleTime)
	}
	var implementation, acceptance *flowStageStats
	for index := range result.ByStage {
		switch result.ByStage[index].Stage {
		case "implementation":
			implementation = &result.ByStage[index]
		case "acceptance":
			acceptance = &result.ByStage[index]
		}
	}
	if implementation == nil || implementation.Attempts != 3 || implementation.Agent != 2 || implementation.Human != 1 ||
		implementation.AgentShare == nil || *implementation.AgentShare < 0.666666 || *implementation.AgentShare > 0.666667 ||
		len(implementation.ByState) != 3 || implementation.SubscriptionShareEstimate == nil || *implementation.SubscriptionShareEstimate < 17.999999 || *implementation.SubscriptionShareEstimate > 18.000001 ||
		implementation.SubscriptionCurrency != "USD" || implementation.EstimateBasis != "monthly_subscription_weighted_by_attested_stage_tokens" {
		t.Fatalf("implementation breakdown=%+v", implementation)
	}
	if acceptance == nil || acceptance.Attempts != 2 || acceptance.SubscriptionShareEstimate == nil || *acceptance.SubscriptionShareEstimate < 11.999999 || *acceptance.SubscriptionShareEstimate > 12.000001 {
		t.Fatalf("acceptance breakdown=%+v", acceptance)
	}
	if result.Overrides["implementation"] != 1 || result.Skips["review"] != 1 || result.Returns["review"] != 1 {
		t.Fatalf("action counts overrides=%v skips=%v returns=%v", result.Overrides, result.Skips, result.Returns)
	}
	if result.AgentResults != 3 || result.AgentAcceptedWithoutEdit != 2 || result.AgentAcceptedWithoutEditRate == nil ||
		*result.AgentAcceptedWithoutEditRate < 0.666666 || *result.AgentAcceptedWithoutEditRate > 0.666667 {
		t.Fatalf("agent edit rate/counts=%d/%d/%v", result.AgentAcceptedWithoutEdit, result.AgentResults, result.AgentAcceptedWithoutEditRate)
	}
	if result.Usage.SubscriptionShareEstimate == nil || *result.Usage.SubscriptionShareEstimate < 29.999999 || *result.Usage.SubscriptionShareEstimate > 30.000001 {
		t.Fatalf("selected flow subscription estimate=%+v", result.Usage)
	}
	request := newLoopbackRequest(http.MethodGet, "/api/metrics/flow.csv?from="+month.Format(time.RFC3339)+"&to="+month.Add(4*time.Hour).Format(time.RFC3339), nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	for _, want := range []string{"stage_state_duration", "stage_subscription_share_estimate", "stage_agent_share", "override_count", "skip_count", "return_count"} {
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) {
			t.Fatalf("flow CSV missing %q (status=%d): %s", want, response.Code, response.Body.String())
		}
	}
}

func TestFlowMetricsJSONSubscriptionEstimatesStayFinite(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Usage = &config.UsageConfig{MonthlySubscriptionAmount: 1e308, MonthlySubscriptionCurrency: "USD"}
	configData, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), configData, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"),
		WithTargetDir(target), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	startedAt := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Hour)
	const runID = "flow-huge-subscription"
	run := &store.PipelineRun{RunID: runID, Feature: "large estimate", Status: "completed", StartedAt: startedAt, CompletedAt: &finishedAt}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	for index, attemptID := range []string{"attempt-one", "attempt-two"} {
		attemptFinished := startedAt.Add(time.Duration(index+1) * time.Minute)
		stage := store.Stage{PipelineRunID: run.ID, AttemptID: attemptID, StageIndex: index + 1, AgentName: "implementation",
			Status: "passed", Execution: "agent", Decision: "approved", Outcome: "passed", StartedAt: startedAt.Add(time.Duration(index) * time.Minute),
			CompletedAt: &attemptFinished, DurationMs: 60_000}
		if err := srv.store.CreateStage(&stage); err != nil {
			t.Fatal(err)
		}
		if err := srv.store.UpdateStage(&stage); err != nil {
			t.Fatal(err)
		}
		attemptDir := filepath.Join(srv.runRoot, runID, "attempts", attemptID)
		if err := os.MkdirAll(attemptDir, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest, marshalErr := json.Marshal(evidence.AttemptManifest{RunID: runID, AttemptID: attemptID,
			Usage: &workflow.AttemptUsage{Attested: true, TokensInput: 12, TokensOutput: 8, CostUSD: 0.01}})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := os.WriteFile(filepath.Join(attemptDir, "manifest.json"), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	envelope := metrics.UsageEnvelope{SchemaVersion: metrics.SchemaVersion, RunID: runID, Feature: "large estimate", StartedAt: startedAt,
		FinishedAt: finishedAt, UsageReported: true, TokensInput: 12, TokensOutput: 8, Outcome: "completed"}
	runDir := filepath.Join(controlDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	envelopeData, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "usage.json"), envelopeData, 0o644); err != nil {
		t.Fatal(err)
	}

	request := newLoopbackRequest(http.MethodGet, "/api/metrics/flow?from=2026-10-01T00:00:00Z&to=2026-10-02T00:00:00Z", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	var result flowMetricsResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("flow metrics JSON: status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Usage.SubscriptionShareEstimate == nil || *result.Usage.SubscriptionShareEstimate != 1e308 {
		t.Fatalf("flow subscription estimate=%v; want finite 1e308", result.Usage.SubscriptionShareEstimate)
	}
	if len(result.ByStage) != 1 || result.ByStage[0].SubscriptionShareEstimate != nil {
		t.Fatalf("overflowing stage estimate should be unavailable, got %+v", result.ByStage)
	}
}

func TestFlowMetrics1000RunsUnder300ms(t *testing.T) {
	if flowMetricsRaceBuild || testing.CoverMode() != "" {
		t.Skip("race and coverage instrumentation change the wall-clock performance acceptance measurement")
	}
	if os.Getenv("CI") == "true" && os.Getenv("AI_TEAM_FLOW_METRICS_PERF") != "1" {
		t.Skip("CI runs the wall-clock acceptance check separately after the unit and coverage suites")
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
	cfg.Usage = &config.UsageConfig{MonthlySubscriptionAmount: 20, MonthlySubscriptionCurrency: "USD"}
	configData, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), configData, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"), WithTargetDir(target), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for index := range 1000 {
		runID := fmt.Sprintf("flow-perf-%04d", index)
		startedAt := start.Add(time.Duration(index) * time.Second)
		finishedAt := startedAt.Add(time.Minute)
		run := &store.PipelineRun{RunID: runID, Feature: "flow-perf", Status: "completed", StartedAt: startedAt, CompletedAt: &finishedAt}
		if err := srv.store.CreatePipelineRun(run); err != nil {
			t.Fatal(err)
		}
		stage := store.Stage{PipelineRunID: run.ID, AttemptID: runID + "-human-attempt", StageIndex: 1, AgentName: "implementation",
			Status: "passed", Execution: "human", Decision: "approved", Outcome: "passed", StartedAt: startedAt, CompletedAt: &finishedAt, DurationMs: 60_000}
		if err := srv.store.CreateStage(&stage); err != nil {
			t.Fatal(err)
		}
		if err := srv.store.UpdateStage(&stage); err != nil {
			t.Fatal(err)
		}
		envelope := metrics.UsageEnvelope{SchemaVersion: metrics.SchemaVersion, RunID: runID, Feature: "flow-perf", StartedAt: startedAt,
			FinishedAt: finishedAt, UsageReported: true, TokensInput: 100, TokensOutput: 50, Outcome: "completed"}
		runDir := filepath.Join(controlDir, "runs", runID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatal(err)
		}
		encoded, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := os.WriteFile(filepath.Join(runDir, "usage.json"), encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	startedAt := time.Now()
	if _, err := srv.buildFlowMetrics(start, start.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(startedAt)
	if elapsed > 300*time.Millisecond {
		t.Fatalf("flow metrics for 1000 runs took %s; want <=300ms", elapsed)
	}
	t.Logf("flow metrics for 1000 runs completed in %s", elapsed)
}

func timePointer(value time.Time) *time.Time { return &value }
