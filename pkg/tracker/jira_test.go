package tracker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

type jiraRequest struct {
	method, path, user, password string
	body                         map[string]any
}

func TestJiraAdapterTransitionsAndCommentsFromTaskEvent(t *testing.T) {
	var requests []jiraRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := jiraRequest{method: r.Method, path: r.URL.Path}
		request.user, request.password, _ = r.BasicAuth()
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&request.body)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/APP-12":
			_, _ = w.Write([]byte(`{"fields":{"status":{"name":"To Do"}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/APP-12/transitions":
			_, _ = w.Write([]byte(`{"transitions":[{"id":"31","name":"Start Progress","to":{"name":"In Progress"}}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue/APP-12/transitions":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue/APP-12/comment":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"comment-1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := NewJiraAdapter(config.JiraConfig{
		BaseURL: server.URL, Email: "bot@example.test", TokenEnv: "AI_TEAM_JIRA_TOKEN",
		WebBaseURL: "https://ai-team.example", StageStatuses: map[string]string{"implementation": "In Progress"},
	}, "test-token", server.Client())
	err := adapter.HandleTaskEvent(context.Background(), TaskEvent{
		Cursor: 8, RunID: "run-8", TaskID: 42, Type: "attempt_finished", Stage: "implementation",
		AttemptID: "attempt-3", ExternalLinks: []string{"APP-12 https://jira.example/browse/APP-12"},
		Data: map[string]any{"status": "passed", "outcome": "success", "decision": "approve", "execution": "completed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		"GET /rest/api/3/issue/APP-12",
		"GET /rest/api/3/issue/APP-12/transitions",
		"POST /rest/api/3/issue/APP-12/transitions",
		"POST /rest/api/3/issue/APP-12/comment",
	}
	gotPaths := make([]string, len(requests))
	for index, request := range requests {
		gotPaths[index] = request.method + " " + request.path
		if request.user != "bot@example.test" || request.password != "test-token" {
			t.Errorf("request %d did not use configured Jira basic auth", index)
		}
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("requests=%v, want %v", gotPaths, wantPaths)
	}
	if requests[2].body["transition"].(map[string]any)["id"] != "31" {
		t.Fatalf("transition payload=%v", requests[2].body)
	}
	body, err := json.Marshal(requests[3].body)
	if err != nil {
		t.Fatal(err)
	}
	comment := string(body)
	for _, want := range []string{"https://ai-team.example/pipelines/42", "Stage result: implementation", "status=passed", "outcome=success"} {
		if !strings.Contains(comment, want) {
			t.Errorf("comment missing %q: %s", want, comment)
		}
	}
}

func TestJiraAdapterSkipsUnmappedStatusAndIgnoresUnlinkedTasks(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	adapter := NewJiraAdapter(config.JiraConfig{BaseURL: server.URL, Email: "bot", WebBaseURL: "https://ai-team.example"}, "token", server.Client())
	base := TaskEvent{Type: "attempt_finished", Stage: "qa", TaskID: 1, ExternalLinks: []string{"APP-12"}}
	if err := adapter.HandleTaskEvent(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("unmapped stage should still post its result comment")
	}
	called = false
	base.ExternalLinks = []string{"https://example.test/no-tracker-key"}
	if err := adapter.HandleTaskEvent(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("task without a Jira issue key must not make HTTP requests")
	}
}

func TestJiraAdapterReportsAmbiguousIssueKeysAndHTTPFailures(t *testing.T) {
	adapter := NewJiraAdapter(config.JiraConfig{BaseURL: "http://jira.example", Email: "bot", TokenEnv: "JIRA_TOKEN", WebBaseURL: "https://ai-team.example"}, "token", nil)
	event := TaskEvent{Type: "attempt_finished", Stage: "qa", TaskID: 1,
		ExternalLinks: []string{"APP-1", "APP-2"}}
	if err := adapter.HandleTaskEvent(context.Background(), event); err == nil || !strings.Contains(err.Error(), "multiple Jira issue keys") {
		t.Fatalf("ambiguous issue keys error=%v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	adapter = NewJiraAdapter(config.JiraConfig{BaseURL: server.URL, Email: "bot", TokenEnv: "JIRA_TOKEN", WebBaseURL: "https://ai-team.example", StageStatuses: map[string]string{"qa": "Done"}}, "token", server.Client())
	event.ExternalLinks = []string{"APP-1"}
	if err := adapter.HandleTaskEvent(context.Background(), event); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("HTTP failure=%v", err)
	}
}

func TestJiraAdapterMatchesConfiguredStatusToTransitionDestination(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/APP-17":
			_, _ = w.Write([]byte(`{"fields":{"status":{"name":"To Do"}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/APP-17/transitions":
			// Jira transition names may differ from their destination status. A
			// mapping to "Done" must not select a transition merely named Done.
			_, _ = w.Write([]byte(`{"transitions":[{"id":"51","name":"Done","to":{"name":"In Progress"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := NewJiraAdapter(config.JiraConfig{
		BaseURL: server.URL, Email: "bot@example.test", WebBaseURL: "https://ai-team.example",
		StageStatuses: map[string]string{"qa": "Done"},
	}, "test-token", server.Client())
	err := adapter.HandleTaskEvent(context.Background(), TaskEvent{
		TaskID: 17, Type: "attempt_finished", Stage: "qa", ExternalLinks: []string{"APP-17"},
	})
	if err == nil || !strings.Contains(err.Error(), `no available transition to status "Done"`) {
		t.Fatalf("transition-name match must not satisfy target status mapping: %v", err)
	}
	want := []string{
		"GET /rest/api/3/issue/APP-17",
		"GET /rest/api/3/issue/APP-17/transitions",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%v, want %v", requests, want)
	}
}

func TestJiraAdapterRequiresTokenOnlyWhenLinkedEventArrives(t *testing.T) {
	adapter := NewJiraAdapter(config.JiraConfig{TokenEnv: "JIRA_TOKEN"}, "", nil)
	event := TaskEvent{Type: "attempt_finished", Stage: "qa", TaskID: 1, ExternalLinks: []string{"APP-1"}}
	if err := adapter.HandleTaskEvent(context.Background(), event); err == nil || !strings.Contains(err.Error(), "JIRA_TOKEN") {
		t.Fatalf("missing token error=%v", err)
	}
}
