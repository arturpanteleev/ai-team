package tracker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

// mockJira is a small stateful Jira REST double (API v2 or v3) used for the
// contract tests; it records every request and never reaches a real Jira.
type mockJira struct {
	mu        sync.Mutex
	version   int
	status    string
	comments  []json.RawMessage
	requests  []string
	auth      [][2]string
	histories []map[string]any
	failPath  string
	failCode  int
	personAt  string
	omitTotal bool // comment pages carry no total (some Jira variants)
	legacy    bool // only the old /search endpoint exists (Server/Data Center)
	jql       string
	jqls      []string // every search query seen
	noMyself  bool     // /myself answers 500
	server    *httptest.Server
}

func newMockJira(t *testing.T, version int) *mockJira {
	t.Helper()
	m := &mockJira{version: version, status: "To Do", personAt: time.Now().UTC().Add(2 * time.Second).Format("2006-01-02T15:04:05.000-0700")}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockJira) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, pass, _ := r.BasicAuth()
	m.auth = append(m.auth, [2]string{user, pass})
	m.requests = append(m.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if m.failPath != "" && strings.HasSuffix(r.URL.Path, m.failPath) {
		w.WriteHeader(m.failCode)
		return
	}
	prefix := fmt.Sprintf("/rest/api/%d/", m.version)
	issue := prefix + "issue/APP-1"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == issue:
		_, _ = fmt.Fprintf(w, `{"fields":{"summary":"Demo","status":{"name":%q}}}`, m.status)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix+"issue/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, prefix+"issue/"), "/"):
		http.NotFound(w, r)
	case r.Method == http.MethodGet && r.URL.Path == issue+"/transitions":
		_, _ = w.Write([]byte(`{"transitions":[{"id":"31","name":"Start Progress","to":{"name":"In Progress"}},{"id":"41","name":"Finish","to":{"name":"Done"}}]}`))
	case r.Method == http.MethodPost && r.URL.Path == issue+"/transitions":
		var body struct {
			Transition struct{ ID string }
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		from := m.status
		m.status = map[string]string{"31": "In Progress", "41": "Done"}[body.Transition.ID]
		m.histories = append(m.histories, map[string]any{
			"id": fmt.Sprint(100 + len(m.histories)), "created": time.Now().UTC().Add(time.Second).Format("2006-01-02T15:04:05.000-0700"),
			"author": map[string]any{"accountId": "bot-1", "displayName": "Bot"},
			"items":  []map[string]any{{"field": "status", "fromString": from, "toString": m.status}},
		})
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == issue+"/comment":
		newestFirst := make([]json.RawMessage, 0, len(m.comments))
		for index := len(m.comments) - 1; index >= 0; index-- {
			newestFirst = append(newestFirst, m.comments[index])
		}
		wrapped := make([]map[string]json.RawMessage, 0)
		for _, body := range newestFirst {
			wrapped = append(wrapped, map[string]json.RawMessage{"body": body})
		}
		startAt, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
		size, err := strconv.Atoi(r.URL.Query().Get("maxResults"))
		if err != nil || size <= 0 || size > 100 {
			size = 50 // Jira's default page
		}
		end := min(startAt+size, len(wrapped))
		if startAt > end {
			startAt = end
		}
		page := map[string]any{"total": len(wrapped), "startAt": startAt, "comments": wrapped[startAt:end]}
		if m.omitTotal {
			delete(page, "total")
		}
		_ = json.NewEncoder(w).Encode(page)
	case r.Method == http.MethodPost && r.URL.Path == issue+"/comment":
		var body struct {
			Body json.RawMessage `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.comments = append(m.comments, body.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"1"}`))
	case r.Method == http.MethodGet && r.URL.Path == prefix+"myself" && m.noMyself:
		w.WriteHeader(http.StatusInternalServerError)
	case r.Method == http.MethodGet && r.URL.Path == prefix+"myself":
		_, _ = w.Write([]byte(`{"accountId":"bot-1","emailAddress":""}`))
	case r.Method == http.MethodGet && r.URL.Path == prefix+"search/jql" && m.legacy:
		http.NotFound(w, r)
	case r.Method == http.MethodGet && r.URL.Path == prefix+"search" && !m.legacy:
		w.WriteHeader(http.StatusGone)
	case r.Method == http.MethodGet && (r.URL.Path == prefix+"search" || r.URL.Path == prefix+"search/jql"):
		m.jql = r.URL.Query().Get("jql")
		m.jqls = append(m.jqls, m.jql)
		if !strings.Contains(m.jql, "APP-1") && !strings.Contains(m.jql, "APP-1,") {
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []map[string]any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": []map[string]any{{
			"key": "APP-1", "changelog": map[string]any{"histories": append(append([]map[string]any{}, m.histories...), map[string]any{
				"id": "900", "created": m.personAt,
				"author": map[string]any{"accountId": "acc-person", "emailAddress": "person@example.test", "displayName": "Person"},
				"items":  []map[string]any{{"field": "status", "fromString": "Done", "toString": "In Review"}, {"field": "assignee"}},
			})},
		}}})
	default:
		http.NotFound(w, r)
	}
}

func (m *mockJira) adapter(version int) *JiraAdapter {
	return NewJiraAdapter(config.JiraConfig{
		BaseURL: m.server.URL, Email: "bot@example.test", TokenEnv: "T", WebBaseURL: "https://ai-team.example", APIVersion: version,
	}, "test-token", m.server.Client())
}

func (m *mockJira) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.requests...)
}

// trackerContract is the behavior every Tracker adapter must have. It runs
// against the fake and against Jira API v2 and v3 mocks.
func trackerContract(t *testing.T, tr Tracker, key string) {
	t.Helper()
	ctx := context.Background()
	if err := tr.ValidateKey(key); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	for _, bad := range []string{"", "app-1", "APP", "APP-0", "APP-1 x", "../APP-1"} {
		if err := tr.ValidateKey(bad); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("ValidateKey(%q)=%v, want ErrInvalidKey", bad, err)
		}
	}
	issue, err := tr.GetIssue(ctx, key)
	if err != nil || issue.Key != key || issue.Status != "To Do" {
		t.Fatalf("GetIssue=%+v, %v", issue, err)
	}
	if _, err := tr.GetIssue(ctx, "NOPE-9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing issue error=%v, want ErrNotFound", err)
	}
	moves, err := tr.Transitions(ctx, key)
	if err != nil || len(moves) == 0 {
		t.Fatalf("Transitions=%v, %v", moves, err)
	}
	cursor, err := tr.Changes(ctx, ChangeQuery{})
	if err != nil || cursor.Cursor == "" || len(cursor.Changes) != 0 {
		t.Fatalf("initial Changes=%+v, %v", cursor, err)
	}
	if changed, err := tr.Transition(ctx, key, "In Progress"); err != nil || !changed {
		t.Fatalf("Transition changed=%v err=%v", changed, err)
	}
	if changed, err := tr.Transition(ctx, key, "in progress"); err != nil || changed {
		t.Fatalf("repeated Transition must be a no-op, changed=%v err=%v", changed, err)
	}
	if _, err := tr.Transition(ctx, key, "Nonexistent Status"); !errors.Is(err, ErrNoTransition) {
		t.Fatalf("impossible transition error=%v, want ErrNoTransition", err)
	}
	comment := Comment{Key: "run-1#7", Lines: []string{"ai-team task: https://ai-team.example/pipelines/1", "Stage result: qa; outcome=failed"}}
	if posted, err := tr.AddComment(ctx, key, comment); err != nil || !posted {
		t.Fatalf("AddComment posted=%v err=%v", posted, err)
	}
	if posted, err := tr.AddComment(ctx, key, comment); err != nil || posted {
		t.Fatalf("same idempotency key must not post twice, posted=%v err=%v", posted, err)
	}
	other := Comment{Key: "run-1#70", Lines: []string{"another"}}
	if posted, err := tr.AddComment(ctx, key, other); err != nil || !posted {
		t.Fatalf("a key sharing a prefix is a different comment, posted=%v err=%v", posted, err)
	}
	if _, err := tr.AddComment(ctx, "NOPE-9", comment); !errors.Is(err, ErrNotFound) {
		t.Fatalf("comment on a missing issue error=%v", err)
	}
	page, err := tr.Changes(ctx, ChangeQuery{Cursor: cursor.Cursor, Keys: []string{key}})
	if err != nil || len(page.Changes) == 0 {
		t.Fatalf("Changes after cursor=%+v, %v", page, err)
	}
	if first := page.Changes[0]; first.IssueKey != key || first.To != "In Progress" || first.Origin != OriginAITeam {
		t.Fatalf("own transition must be reported with ai-team origin: %+v", first)
	}
	again, err := tr.Changes(ctx, ChangeQuery{Cursor: page.Cursor, Keys: []string{key}})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range again.Changes {
		for _, old := range page.Changes {
			if change.ID == old.ID {
				t.Fatalf("change %s returned twice across cursors", change.ID)
			}
		}
	}
	if _, err := tr.Changes(ctx, ChangeQuery{Cursor: "garbage"}); err == nil {
		t.Fatal("invalid cursor must be an error")
	}
}

func TestFakeTrackerContract(t *testing.T) {
	fake := NewFake(nil, FakeIssueSeed{Key: "APP-1", Summary: "Demo"})
	trackerContract(t, fake, "APP-1")
}

func TestJiraTrackerContractV3(t *testing.T) {
	mock := newMockJira(t, 3)
	trackerContract(t, mock.adapter(3), "APP-1")
	for index, pair := range mock.auth {
		if pair != [2]string{"bot@example.test", "test-token"} {
			t.Fatalf("request %d did not use basic auth: %v", index, pair)
		}
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if !strings.Contains(string(mock.comments[0]), `"type":"doc"`) || !strings.Contains(string(mock.comments[0]), "ai-team event: run-1#7") {
		t.Fatalf("v3 comment must be ADF with the idempotency marker: %s", mock.comments[0])
	}
}

func TestJiraTrackerContractV2UsesPlainTextComments(t *testing.T) {
	mock := newMockJira(t, 2)
	trackerContract(t, mock.adapter(2), "APP-1")
	mock.mu.Lock()
	defer mock.mu.Unlock()
	var text string
	if err := json.Unmarshal(mock.comments[0], &text); err != nil || !strings.HasSuffix(text, "ai-team event: run-1#7") {
		t.Fatalf("v2 comment must be plain text ending with the marker: %s (%v)", mock.comments[0], err)
	}
	for _, request := range mock.requests {
		if !strings.Contains(request, "/rest/api/2/") {
			t.Fatalf("v2 adapter called %s", request)
		}
	}
}

func TestJiraTransitionMatchesTransitionNameWhenNoStatusMatches(t *testing.T) {
	mock := newMockJira(t, 3)
	changed, err := mock.adapter(3).Transition(context.Background(), "APP-1", "Start Progress")
	if err != nil || !changed || mock.status != "In Progress" {
		t.Fatalf("changed=%v err=%v status=%s", changed, err, mock.status)
	}
}

func TestJiraErrorsMapToSentinelsWithoutLeakingSecrets(t *testing.T) {
	mock := newMockJira(t, 3)
	adapter := mock.adapter(3)
	for code, want := range map[int]error{http.StatusForbidden: ErrForbidden, http.StatusUnauthorized: ErrForbidden, http.StatusNotFound: ErrNotFound} {
		mock.failPath, mock.failCode = "/issue/APP-1", code
		_, err := adapter.GetIssue(context.Background(), "APP-1")
		if !errors.Is(err, want) {
			t.Errorf("HTTP %d error=%v, want %v", code, err, want)
		}
		if err != nil && (strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "bot@example.test")) {
			t.Errorf("error leaks credentials: %v", err)
		}
	}
	mock.failCode = http.StatusInternalServerError
	if _, err := adapter.GetIssue(context.Background(), "APP-1"); err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
		t.Fatalf("HTTP 500 must be a plain failure, got %v", err)
	}
}

func TestJiraCommentLookupFailureDoesNotPost(t *testing.T) {
	mock := newMockJira(t, 3)
	mock.failPath, mock.failCode = "/comment", http.StatusForbidden
	posted, err := mock.adapter(3).AddComment(context.Background(), "APP-1", Comment{Key: "run#1", Lines: []string{"x"}})
	if err == nil || posted {
		t.Fatalf("posted=%v err=%v", posted, err)
	}
	for _, request := range mock.seen() {
		if request == "POST /rest/api/3/issue/APP-1/comment" {
			t.Fatal("comment posted although the idempotency lookup failed")
		}
	}
}

func TestJiraChangesIgnoresNonStatusItemsAndMarksExternalAuthors(t *testing.T) {
	mock := newMockJira(t, 3)
	adapter := mock.adapter(3)
	start, err := adapter.Changes(context.Background(), ChangeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: start.Cursor, Keys: []string{"APP-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 1 || page.Changes[0].To != "In Review" || page.Changes[0].Origin != OriginExternal || page.Changes[0].Author != "Person" {
		t.Fatalf("changes=%+v", page.Changes)
	}
	empty, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: page.Cursor, Keys: []string{"APP-1"}})
	if err != nil || len(empty.Changes) != 0 || empty.Cursor != page.Cursor {
		t.Fatalf("replay after cursor=%+v, %v", empty, err)
	}
	limited, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: start.Cursor, Keys: []string{"APP-1"}, Limit: 1})
	if err != nil || len(limited.Changes) != 1 {
		t.Fatalf("limit=%+v, %v", limited, err)
	}
}

func keysFromLinks(adapter *JiraAdapter, links ...string) []string {
	var keys []string
	for _, link := range links {
		if key, ok := adapter.KeyFromLink(link); ok {
			keys = append(keys, key)
		}
	}
	return keys
}

func TestJiraKeyFromLinkIgnoresKeyShapedTextAndForeignHosts(t *testing.T) {
	adapter := NewJiraAdapter(config.JiraConfig{BaseURL: "https://acme.atlassian.net"}, "", nil)
	for _, link := range []string{
		"encoding UTF-8", "artifact digest SHA-256: abc", "see CVE-2024-1234 and ADR-12", "timestamp ISO-8601",
		"https://other.example/browse/APP-1", "https://acme.atlassian.net.evil.example/browse/APP-1",
		"https://acme.atlassian.net/wiki/UTF-8", "https://acme.atlassian.net/browse/APP-1/extra",
		"ftp://acme.atlassian.net/browse/APP-1", "APP-1 https://acme.atlassian.net/browse/APP-1", "app-1",
		"https://acme.atlassian.net:8443/browse/APP-1", "https://acme.atlassian.net/browse/app",
	} {
		if keys := keysFromLinks(adapter, link); len(keys) != 0 {
			t.Errorf("KeyFromLink(%q)=%v; want no key", link, keys)
		}
	}
	for link, want := range map[string]string{
		"APP-7": "APP-7", " APP-7 ": "APP-7",
		"https://acme.atlassian.net/browse/APP-7":                    "APP-7",
		"https://ACME.atlassian.net/browse/APP-7/":                   "APP-7",
		"https://acme.atlassian.net/browse/APP-7?focusedCommentId=1": "APP-7",
		"https://acme.atlassian.net/browse/APP-7#comment-1":          "APP-7",
		"https://acme.atlassian.net:443/browse/APP-7":                "APP-7",
		"https://acme.atlassian.net/browse/app-7":                    "APP-7",
		"http://acme.atlassian.net/browse/App-7":                     "APP-7",
	} {
		if keys := keysFromLinks(adapter, link); len(keys) != 1 || keys[0] != want {
			t.Errorf("KeyFromLink(%q)=%v; want %q", link, keys, want)
		}
	}
	context := NewJiraAdapter(config.JiraConfig{BaseURL: "https://jira.example/jira/"}, "", nil)
	if keys := keysFromLinks(context, "https://jira.example/jira/browse/OPS-3"); len(keys) != 1 || keys[0] != "OPS-3" {
		t.Errorf("context-path browse URL keys=%v", keys)
	}
	port := NewJiraAdapter(config.JiraConfig{BaseURL: "https://jira.example:443"}, "", nil)
	if keys := keysFromLinks(port, "https://jira.example/browse/OPS-4"); len(keys) != 1 {
		t.Errorf("explicit default port in base_url: keys=%v", keys)
	}
	other := NewJiraAdapter(config.JiraConfig{BaseURL: "https://jira.example:8443"}, "", nil)
	if keys := keysFromLinks(other, "https://jira.example/browse/OPS-4"); len(keys) != 0 {
		t.Errorf("default port must not match base_url on :8443: keys=%v", keys)
	}
}

// leakyTransport fails every request with an error that echoes the request
// URL and Authorization header, as a misbehaving proxy or custom transport
// might. None of it may reach the returned error.
type leakyTransport struct{}

func (leakyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("refused %s with %s", request.URL, request.Header.Get("Authorization"))}
}

func TestJiraTransportErrorDoesNotLeakCredentials(t *testing.T) {
	const token = "jira-secret-token-123"
	adapter := NewJiraAdapter(config.JiraConfig{
		BaseURL: "https://jira.example/private-context", Email: "bot@example.test",
	}, token, &http.Client{Transport: leakyTransport{}})
	_, err := adapter.GetIssue(context.Background(), "APP-1")
	if err == nil {
		t.Fatal("transport failure must be reported")
	}
	basic := base64.StdEncoding.EncodeToString([]byte("bot@example.test:" + token))
	for _, secret := range []string{token, basic, "private-context", "jira.example"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("transport error leaks %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "connection failed (dial)") {
		t.Fatalf("transport error lost its classification: %v", err)
	}
}

func TestJiraCommentIdempotencyReadsBeyondTheFirstPage(t *testing.T) {
	mock := newMockJira(t, 3)
	adapter := mock.adapter(3)
	ctx := context.Background()
	comment := Comment{Key: "run-1#7", Lines: []string{"first"}}
	if posted, err := adapter.AddComment(ctx, "APP-1", comment); err != nil || !posted {
		t.Fatalf("first post=%v err=%v", posted, err)
	}
	// 230 newer comments push the marker beyond the newest 50 and 100.
	for index := 0; index < 230; index++ {
		other := Comment{Key: fmt.Sprintf("run-2#%d", index), Lines: []string{"noise"}}
		if posted, err := adapter.AddComment(ctx, "APP-1", other); err != nil || !posted {
			t.Fatalf("noise %d post=%v err=%v", index, posted, err)
		}
	}
	before := len(mock.comments)
	if posted, err := adapter.AddComment(ctx, "APP-1", comment); err != nil || posted {
		t.Fatalf("a marker on page 3 must still be found, posted=%v err=%v", posted, err)
	}
	if len(mock.comments) != before {
		t.Fatalf("duplicate comment was posted")
	}
}

func TestJiraCommentPaginationContinuesWhenTotalIsMissing(t *testing.T) {
	mock := newMockJira(t, 3)
	mock.omitTotal = true
	adapter := mock.adapter(3)
	ctx := context.Background()
	comment := Comment{Key: "run-1#7", Lines: []string{"first"}}
	if posted, err := adapter.AddComment(ctx, "APP-1", comment); err != nil || !posted {
		t.Fatalf("first post=%v err=%v", posted, err)
	}
	for index := 0; index < 230; index++ {
		if _, err := adapter.AddComment(ctx, "APP-1", Comment{Key: fmt.Sprintf("run-2#%d", index), Lines: []string{"noise"}}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(mock.comments)
	if posted, err := adapter.AddComment(ctx, "APP-1", comment); err != nil || posted {
		t.Fatalf("without total the marker on page 3 must still be found, posted=%v err=%v", posted, err)
	}
	if len(mock.comments) != before {
		t.Fatal("duplicate comment was posted")
	}
}

func TestJiraChangesUseSearchJQLWithFallbackAndRelativeWindow(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		mock := newMockJira(t, 3)
		mock.legacy = legacy
		adapter := mock.adapter(3)
		start, err := adapter.Changes(context.Background(), ChangeQuery{})
		if err != nil {
			t.Fatal(err)
		}
		page, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: start.Cursor, Keys: []string{"APP-1"}})
		if err != nil || len(page.Changes) != 1 {
			t.Fatalf("legacy=%v changes=%+v err=%v", legacy, page.Changes, err)
		}
		if !strings.Contains(mock.jql, `updated >= "-`) || !strings.Contains(mock.jql, `m"`) {
			t.Fatalf("JQL must use a zone-independent relative window: %q", mock.jql)
		}
		seen := mock.seen()
		first := "GET /rest/api/3/search/jql"
		found := false
		for _, request := range seen {
			found = found || request == first
		}
		if !found {
			t.Fatalf("legacy=%v: /search/jql must be tried first: %v", legacy, seen)
		}
	}
}

func TestJiraChangeOriginUsesAuthenticatedAccountNotConfiguredEmail(t *testing.T) {
	mock := newMockJira(t, 3)
	adapter := mock.adapter(3)
	adapter.settings.Email = "someone-else@example.test" // PAT or hidden Cloud e-mail
	start, _ := adapter.Changes(context.Background(), ChangeQuery{})
	if _, err := adapter.Transition(context.Background(), "APP-1", "In Progress"); err != nil {
		t.Fatal(err)
	}
	page, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: start.Cursor, Keys: []string{"APP-1"}})
	if err != nil || len(page.Changes) != 2 {
		t.Fatalf("changes=%+v err=%v", page.Changes, err)
	}
	origins := map[string]string{}
	for _, change := range page.Changes {
		origins[change.Author] = change.Origin
	}
	if origins["Bot"] != OriginAITeam || origins["Person"] != OriginExternal {
		t.Fatalf("origins=%v", origins)
	}
}

func TestJiraChangesFailWhenOwnAccountIsUnknown(t *testing.T) {
	mock := newMockJira(t, 3)
	adapter := mock.adapter(3)
	start, err := adapter.Changes(context.Background(), ChangeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	mock.noMyself = true
	page, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: start.Cursor, Keys: []string{"APP-1"}})
	if err == nil || len(page.Changes) != 0 || page.Cursor != "" {
		t.Fatalf("an unknown own account must be an error without changes or a moved cursor: %+v, %v", page, err)
	}
	// The failure is not cached: once /myself works the same cursor yields
	// the change with the right origin.
	mock.noMyself = false
	page, err = adapter.Changes(context.Background(), ChangeQuery{Cursor: start.Cursor, Keys: []string{"APP-1"}})
	if err != nil || len(page.Changes) != 1 || page.Changes[0].Origin != OriginExternal {
		t.Fatalf("recovery: %+v, %v", page, err)
	}
}

func TestJiraChangesBatchManyKeysUnderOneCursor(t *testing.T) {
	mock := newMockJira(t, 3)
	adapter := mock.adapter(3)
	start, err := adapter.Changes(context.Background(), ChangeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, 120)
	for i := 0; i < 119; i++ {
		keys = append(keys, fmt.Sprintf("ZZ-%d", i+1))
	}
	keys = append(keys, "APP-1") // lands in the third batch
	page, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: start.Cursor, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.jqls) != 3 {
		t.Fatalf("120 keys must go in 3 queries of at most 50, got %d", len(mock.jqls))
	}
	for _, jql := range mock.jqls {
		if got := strings.Count(jql, ",") + 1; got > 50 {
			t.Fatalf("batch of %d keys: %s", got, jql)
		}
	}
	if len(page.Changes) != 1 || page.Changes[0].IssueKey != "APP-1" || page.Cursor == start.Cursor {
		t.Fatalf("merged result: %+v", page)
	}
}
