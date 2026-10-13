package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/tracker"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

type trackerHTTPRequest struct {
	Method, Path, User, Password string
	Body                         string
}

type recordingTrackerAdapter struct{ events chan tracker.TaskEvent }

func (a recordingTrackerAdapter) HandleTaskEvent(_ context.Context, event tracker.TaskEvent) error {
	a.events <- event
	return nil
}

func TestTrackerConsumerReplaysDurableEventsAfterRestart(t *testing.T) {
	target := t.TempDir()
	database := filepath.Join(t.TempDir(), "web.db")
	firstAdapter := recordingTrackerAdapter{events: make(chan tracker.TaskEvent, 1)}
	first, err := NewServer(database, "", target, WithTargetDir(target), WithTrackerAdapter(firstAdapter))
	if err != nil {
		t.Fatal(err)
	}
	run := createJiraTask(t, first, "jira-replay-run", []string{"APP-21"})
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	offlineStore, err := store.New(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := offlineStore.AppendEventNext(&store.Event{
		RunID: run.RunID, Type: "attempt_finished", Stage: "qa", AttemptID: "attempt-offline",
		Timestamp: time.Now().UTC(), DataJSON: `{"status":"passed","outcome":"passed"}`,
	}); err != nil {
		_ = offlineStore.Close()
		t.Fatal(err)
	}
	if err := offlineStore.Close(); err != nil {
		t.Fatal(err)
	}

	restartedAdapter := recordingTrackerAdapter{events: make(chan tracker.TaskEvent, 1)}
	restarted, err := NewServer(database, "", target, WithTargetDir(target), WithTrackerAdapter(restartedAdapter))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close() }()
	select {
	case event := <-restartedAdapter.events:
		if event.RunID != run.RunID || event.Type != "attempt_finished" || event.Stage != "qa" ||
			event.TaskID != run.ID || len(event.ExternalLinks) != 1 || event.ExternalLinks[0] != "APP-21" {
			t.Fatalf("replayed tracker event=%+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("durable pending task event was not replayed after restart")
	}
}

func TestTaskEventDrivesJiraStatusAndComment(t *testing.T) {
	requests := make(chan trackerHTTPRequest, 5)
	jira := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		username, password, _ := r.BasicAuth()
		requests <- trackerHTTPRequest{Method: r.Method, Path: r.URL.Path, User: username, Password: password, Body: string(body)}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/APP-19":
			_, _ = w.Write([]byte(`{"fields":{"status":{"name":"To Do"}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/APP-19/transitions":
			_, _ = w.Write([]byte(`{"transitions":[{"id":"44","name":"Complete","to":{"name":"Done"}}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue/APP-19/transitions":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/APP-19/comment":
			_, _ = w.Write([]byte(`{"comments":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue/APP-19/comment":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"comment-19"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer jira.Close()

	target := t.TempDir()
	adapter := tracker.NewJiraAdapter(config.JiraConfig{
		BaseURL: jira.URL, Email: "bot@example.test", TokenEnv: "JIRA_TOKEN", WebBaseURL: "https://ai-team.example",
		StageStatuses: map[string]string{"qa": "Done"},
	}, "mock-token", jira.Client())
	srv, err := NewServer(":memory:", "", target, WithTargetDir(target), WithTrackerAdapter(adapter))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	run := createJiraTask(t, srv, "jira-event-run", []string{"APP-19"})
	if err := srv.store.AppendEventNext(&store.Event{
		RunID: run.RunID, Type: "attempt_finished", Stage: "qa", AttemptID: "attempt-qa",
		Timestamp: time.Now().UTC(), DataJSON: `{"status":"passed","outcome":"passed","decision":"approved","execution":"succeeded"}`,
	}); err != nil {
		t.Fatal(err)
	}

	got := make([]trackerHTTPRequest, 0, 5)
	for len(got) < 5 {
		select {
		case request := <-requests:
			got = append(got, request)
		case <-time.After(5 * time.Second):
			t.Fatalf("Jira requests from attempt_finished event=%+v, want 5 requests", got)
		}
	}
	wantPaths := []string{
		"GET /rest/api/3/issue/APP-19",
		"GET /rest/api/3/issue/APP-19/transitions",
		"POST /rest/api/3/issue/APP-19/transitions",
		"GET /rest/api/3/issue/APP-19/comment",
		"POST /rest/api/3/issue/APP-19/comment",
	}
	for index, request := range got {
		if gotPath := request.Method + " " + request.Path; gotPath != wantPaths[index] {
			t.Fatalf("request[%d]=%q, want %q", index, gotPath, wantPaths[index])
		}
		if request.User != "bot@example.test" || request.Password != "mock-token" {
			t.Errorf("request[%d] auth was not configured", index)
		}
	}
	if !strings.Contains(got[2].Body, `"id":"44"`) {
		t.Fatalf("transition body=%s", got[2].Body)
	}
	for _, want := range []string{"https://ai-team.example/pipelines/", "Stage result: qa", "status=passed", "outcome=passed", "ai-team attempt: attempt-qa"} {
		if !strings.Contains(got[4].Body, want) {
			t.Errorf("Jira comment missing %q: %s", want, got[4].Body)
		}
	}
}

func TestJiraFailureIsIsolatedAndRecordsDurableIntegrationError(t *testing.T) {
	called := make(chan struct{}, 1)
	jira := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called <- struct{}{}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer jira.Close()
	target := t.TempDir()
	adapter := tracker.NewJiraAdapter(config.JiraConfig{
		BaseURL: jira.URL, Email: "bot@example.test", TokenEnv: "JIRA_TOKEN", WebBaseURL: "https://ai-team.example",
		StageStatuses: map[string]string{"qa": "Done"},
	}, "mock-token", jira.Client())
	srv, err := NewServer(":memory:", "", target, WithTargetDir(target), WithTrackerAdapter(adapter))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	run := createJiraTask(t, srv, "jira-failure-run", []string{"APP-20"})
	completedAt := time.Now().UTC()
	run.Status = "completed"
	run.CompletedAt = &completedAt
	if err := srv.store.UpdatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	events := srv.store.SubscribeEvents(context.Background(), 8)
	if err := srv.store.AppendEventNext(&store.Event{
		RunID: run.RunID, Type: "attempt_finished", Stage: "qa", AttemptID: "attempt-failed",
		Timestamp: time.Now().UTC(), DataJSON: `{"status":"failed","outcome":"blocked"}`,
	}); err != nil {
		t.Fatalf("task event append failed: %v", err)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("mock Jira server was not called")
	}
	observedError := false
	deadline := time.After(5 * time.Second)
	for !observedError {
		select {
		case event := <-events:
			observedError = event.Type == "integration_error"
		case <-deadline:
			t.Fatal("timed out waiting for integration_error")
		}
	}
	stored, err := srv.store.GetRunEventsAfter(run.RunID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	var integrationError *store.Event
	for index := range stored {
		if stored[index].Type == "integration_error" {
			integrationError = &stored[index]
			break
		}
	}
	if integrationError == nil || integrationError.Severity != "warning" {
		t.Fatalf("durable integration_error missing: %+v", stored)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(integrationError.DataJSON), &data); err != nil {
		t.Fatal(err)
	}
	if data["integration"] != "tracker" || data["source_event"] != "attempt_finished" || !strings.Contains(data["error"].(string), "HTTP 503") {
		t.Fatalf("integration error details=%v", data)
	}
	unchanged, err := srv.store.GetPipelineRunByRunID(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != "completed" {
		t.Fatalf("Jira failure changed task status to %q", unchanged.Status)
	}
}

func createJiraTask(t *testing.T, srv *Server, runID string, links []string) *store.PipelineRun {
	t.Helper()
	snapshot, err := json.Marshal(startRunCommand{Task: "stage event integration test", ExternalLinks: links})
	if err != nil {
		t.Fatal(err)
	}
	run := &store.PipelineRun{
		RunID: runID, Feature: "tracker-test", Status: "running", StartedAt: time.Now().UTC(), ConfigSnapshot: string(snapshot),
	}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	return run
}

// echoingTransport fails like a misbehaving proxy whose error echoes the
// request URL and Authorization header.
type echoingTransport struct{}

func (echoingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("proxy refused %s (%s)", request.URL, request.Header.Get("Authorization"))}
}

func TestJiraTransportErrorDoesNotLeakTokenIntoIntegrationError(t *testing.T) {
	const token = "jira-secret-token-456"
	target := t.TempDir()
	adapter := tracker.NewJiraAdapter(config.JiraConfig{
		BaseURL: "https://jira.example/secret-context", Email: "bot@example.test", TokenEnv: "JIRA_TOKEN",
		WebBaseURL: "https://ai-team.example",
	}, token, &http.Client{Transport: echoingTransport{}})
	srv, err := NewServer(":memory:", "", target, WithTargetDir(target), WithTrackerAdapter(adapter))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	run := createJiraTask(t, srv, "jira-transport-run", []string{"APP-30"})
	events := srv.store.SubscribeEvents(context.Background(), 8)
	if err := srv.store.AppendEventNext(&store.Event{
		RunID: run.RunID, Type: "attempt_finished", Stage: "qa", AttemptID: "attempt-transport",
		Timestamp: time.Now().UTC(), DataJSON: `{"status":"passed","outcome":"passed"}`,
	}); err != nil {
		t.Fatal(err)
	}
	var integrationError string
	deadline := time.After(5 * time.Second)
	for integrationError == "" {
		select {
		case event := <-events:
			if event.Type == "integration_error" {
				encoded, _ := json.Marshal(event.Data)
				integrationError = string(encoded)
			}
		case <-deadline:
			t.Fatal("timed out waiting for integration_error")
		}
	}
	basic := base64.StdEncoding.EncodeToString([]byte("bot@example.test:" + token))
	for _, secret := range []string{token, basic, "secret-context", "jira.example"} {
		if strings.Contains(integrationError, secret) {
			t.Fatalf("integration_error leaks %q: %s", secret, integrationError)
		}
	}
	if !strings.Contains(integrationError, "connection failed") {
		t.Fatalf("integration_error lost the failure class: %s", integrationError)
	}
}
