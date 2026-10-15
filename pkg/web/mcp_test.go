package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const mcpTestRun = "run-mcp-1"

type mcpEnv struct {
	srv     *Server
	http    *httptest.Server
	manager *cloudidentity.TokenManager
	target  string
	cookies map[string]*http.Cookie
	csrf    map[string]string
}

// newMCPEnv starts a cloud-mode server with a seeded task, a real HTTP
// listener, an owner (Product Owner, bootstraps the team) and a QA member.
func newMCPEnv(t *testing.T, options ...ServerOption) *mcpEnv {
	t.Helper()
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
	if err = os.WriteFile(filepath.Join(controlDir, "config.yaml"), templateYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	options = append([]ServerOption{WithTargetDir(target), WithAuthenticator(manager), WithRunController(&fakeRunController{})}, options...)
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"), options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	templateStore, err := srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = templateStore.PinCurrentForRun(mcpTestRun, mcpTestRun); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	recorder := NewStoreRecorder(srv.store)
	recorder.RunStarted(mcpTestRun, "feature-mcp", `{"task":"read over mcp","mode":"manual","external_links":["JIRA-1 https://tracker.example/JIRA-1"]}`, started)
	recorder.StageStarted(mcpTestRun, "attempt-1", "acceptance", 1, started.Add(time.Minute))
	recorder.StageFinished(notifier.StageResult{RunID: mcpTestRun, AttemptID: "attempt-1", Name: "acceptance",
		Status: notifier.StatusPassed, StartedAt: started.Add(time.Minute), FinishedAt: started.Add(5 * time.Minute)})
	env := &mcpEnv{srv: srv, manager: manager, target: target}
	env.http = httptest.NewServer(srv.router)
	t.Cleanup(env.http.Close)
	// The first sign-in of a Product Owner bootstraps the team registry.
	cookie, csrf := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)
	env.cookies = map[string]*http.Cookie{"owner@example.com": cookie}
	env.csrf = map[string]string{"owner@example.com": csrf}
	env.addMember(t, "qa@example.com", "qa")
	return env
}

func (e *mcpEnv) addMember(t *testing.T, actor string, roles ...string) {
	t.Helper()
	digest := sha256.Sum256([]byte("invite-" + actor))
	hash := hex.EncodeToString(digest[:])
	now := time.Now().UTC()
	if err := e.srv.store.InviteTeamMember(actor, actor, roles, hash, now.Add(time.Hour), now, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.store.ActivateTeamInvitation(hash, now, func(*store.TeamMember) error { return nil }); err != nil {
		t.Fatal(err)
	}
	principal, err := cloudidentity.NewPrincipal(actor, mustRoles(t, roles))
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := cloudSessionForTest(t, e.srv, e.manager, actor, principal.Roles...)
	e.cookies[actor], e.csrf[actor] = cookie, csrf
}

func mustRoles(t *testing.T, values []string) []cloudidentity.Role {
	t.Helper()
	roles, err := cloudidentity.ParseRoles(values)
	if err != nil {
		t.Fatal(err)
	}
	return roles
}

// issueToken mints a personal MCP token through the real REST route.
func (e *mcpEnv) issueToken(t *testing.T, actor, body string) (string, string) {
	t.Helper()
	response := teamRequest(e.srv, e.cookies[actor], e.csrf[actor], http.MethodPost, "/api/mcp/tokens", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("issue token for %s: %d %s", actor, response.Code, response.Body.String())
	}
	var issued struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil || !strings.HasPrefix(issued.Token, "aitm_") {
		t.Fatalf("issued token: %v %s", err, response.Body.String())
	}
	return issued.Token, issued.ID
}

func (e *mcpEnv) rest(t *testing.T, actor, path string) (int, []byte) {
	t.Helper()
	request := newLoopbackRequest(http.MethodGet, path, nil)
	request.AddCookie(e.cookies[actor])
	response := httptest.NewRecorder()
	e.srv.router.ServeHTTP(response, request)
	return response.Code, bytes.TrimRight(response.Body.Bytes(), "\n")
}

type bearerTransport struct {
	token string
	extra http.Header
}

func (b bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	if b.token != "" {
		request.Header.Set("Authorization", "Bearer "+b.token)
	}
	for name, values := range b.extra {
		request.Header[name] = values
	}
	return http.DefaultTransport.RoundTrip(request)
}

func (e *mcpEnv) connect(t *testing.T, token string, options *mcp.ClientSessionOptions) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "s12-test", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: e.http.URL + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: bearerTransport{token: token}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	session, err := client.Connect(ctx, transport, options)
	if err == nil {
		t.Cleanup(func() { _ = session.Close() })
	}
	return session, err
}

func (e *mcpEnv) mustConnect(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	session, err := e.connect(t, token, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) (string, bool) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: protocol error %v", name, err)
	}
	var text strings.Builder
	for _, content := range result.Content {
		if part, ok := content.(*mcp.TextContent); ok {
			text.WriteString(part.Text)
		}
	}
	return text.String(), result.IsError
}

func sameJSON(t *testing.T, label string, left, right []byte) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(left, &a); err != nil {
		t.Fatalf("%s: left is not JSON: %v: %s", label, err, left)
	}
	if err := json.Unmarshal(right, &b); err != nil {
		t.Fatalf("%s: right is not JSON: %v: %s", label, err, right)
	}
	encodedA, _ := json.Marshal(a)
	encodedB, _ := json.Marshal(b)
	if !bytes.Equal(encodedA, encodedB) {
		t.Fatalf("%s differs:\nMCP:  %s\nREST: %s", label, encodedA, encodedB)
	}
}

func TestMCPReadToolsMatchRESTUnderEachUsersIdentity(t *testing.T) {
	env := newMCPEnv(t)
	runDir := filepath.Join(env.target, ".ai-team", "runs", mcpTestRun)
	if err := os.MkdirAll(filepath.Join(runDir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runDir, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "logs", "attempt-1.log"), []byte("hello log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "reports", "checks.md"), []byte("# checks\nall green\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refusals := 0
	// An explicit period keeps both answers deterministic (the default "to" is now).
	period := [2]string{time.Now().Add(-6 * time.Hour).UTC().Format(time.RFC3339), time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)}
	for _, actor := range []string{"owner@example.com", "qa@example.com"} {
		token, _ := env.issueToken(t, actor, `{"label":"laptop"}`)
		session := env.mustConnect(t, token)
		for _, c := range []struct {
			tool string
			args map[string]any
			rest string
		}{
			{"whoami", nil, "/api/auth/me"},
			{"list_projects", nil, "/api/projects"},
			{"get_project", map[string]any{"project_id": 1}, "/api/projects/1"},
			{"list_tasks", map[string]any{"limit": 10}, "/api/runs?limit=10"},
			{"list_tasks", map[string]any{"status": "running", "mode": "manual"}, "/api/runs?status=running&mode=manual"},
			{"get_task", map[string]any{"run_id": mcpTestRun}, "/api/runs/" + mcpTestRun},
			{"get_task_workflow", map[string]any{"run_id": mcpTestRun}, "/api/runs/" + mcpTestRun + "/workflow"},
			{"get_task_template_version", map[string]any{"run_id": mcpTestRun}, "/api/runs/" + mcpTestRun + "/template-version"},
			{"get_task_gates", map[string]any{"run_id": mcpTestRun}, "/api/runs/" + mcpTestRun + "/gates"},
			{"list_task_events", map[string]any{"run_id": mcpTestRun, "after": 0}, "/api/runs/" + mcpTestRun + "/events?after=0"},
			{"list_events", map[string]any{"limit": 5, "order": "desc"}, "/api/events?limit=5&order=desc"},
			{"get_task_log", map[string]any{"run_id": mcpTestRun, "attempt_id": "attempt-1"}, "/api/runs/" + mcpTestRun + "/logs/attempt-1"},
			{"read_task_artifact", map[string]any{"run_id": mcpTestRun, "path": "reports/checks.md"}, "/api/runs/" + mcpTestRun + "/artifacts/reports/checks.md"},
			{"list_templates", nil, "/api/templates"},
			{"get_template", nil, "/api/template"},
			{"get_gate_catalog", nil, "/api/gates/catalog"},
			{"get_flow_metrics", map[string]any{"run_id": mcpTestRun, "from": period[0], "to": period[1]}, "/api/metrics/flow?run_id=" + mcpTestRun + "&from=" + period[0] + "&to=" + period[1]},
			{"get_preflight", nil, "/api/preflight"},
		} {
			text, isError := callTool(t, session, c.tool, c.args)
			status, body := env.rest(t, actor, c.rest)
			if status != http.StatusOK {
				// The same refusal, with the same status, as REST.
				if !isError || !strings.Contains(text, `"status":`+strconv.Itoa(status)) {
					t.Fatalf("%s %s: REST status %d but tool error=%v %s", actor, c.tool, status, isError, text)
				}
				refusals++
				continue
			}
			if isError {
				t.Fatalf("%s %s: tool error %s, REST status=%d", actor, c.tool, text, status)
			}
			if c.tool == "get_preflight" { // carries a fresh timestamp on every call
				if !strings.Contains(text, `"readiness":"ready"`) || !strings.Contains(string(body), `"readiness":"ready"`) {
					t.Fatalf("preflight: %s vs %s", text, body)
				}
				continue
			}
			if c.tool == "read_task_artifact" {
				if text != string(body)+"\n" { // rest() trims the final newline
					t.Fatalf("artifact text differs: %q vs %q", text, body)
				}
				continue
			}
			sameJSON(t, actor+" "+c.tool, stripPagination(t, []byte(text), c.tool), body)
		}
	}
	if refusals > 4 { // seeded run has no lifecycle workflow: only those reads may be refused (twice per actor)
		t.Fatalf("%d reads were refused; most must succeed", refusals)
	}
}

func TestMCPTaskSnapshotCarriesTheRESTFields(t *testing.T) {
	env := newMCPEnv(t)
	token, _ := env.issueToken(t, "qa@example.com", `{"label":"agent"}`)
	session := env.mustConnect(t, token)
	text, isError := callTool(t, session, "get_task", map[string]any{"run_id": mcpTestRun})
	if isError {
		t.Fatal(text)
	}
	var snapshot taskRunDetails
	if err := json.Unmarshal([]byte(text), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Task != "read over mcp" || len(snapshot.Stages) != 1 || snapshot.Stages[0].AttemptID != "attempt-1" ||
		snapshot.Template == nil || len(snapshot.ExternalLinks) != 1 || snapshot.AllowedActions == nil {
		t.Fatalf("snapshot lost REST fields: %+v", snapshot)
	}
}

func TestMCPEnforcesTheSameRBACAsREST(t *testing.T) {
	env := newMCPEnv(t)
	ownerToken, _ := env.issueToken(t, "owner@example.com", `{"label":"o"}`)
	qaToken, _ := env.issueToken(t, "qa@example.com", `{"label":"q"}`)
	owner, qa := env.mustConnect(t, ownerToken), env.mustConnect(t, qaToken)

	text, isError := callTool(t, owner, "list_team_members", nil)
	if isError || !strings.Contains(text, "qa@example.com") {
		t.Fatalf("owner must read the team: %v %s", isError, text)
	}
	text, isError = callTool(t, qa, "list_team_members", nil)
	if !isError || !strings.Contains(text, `"status":403`) || strings.Contains(text, "owner@example.com") {
		t.Fatalf("QA must be refused without leaking members: %v %s", isError, text)
	}
	if text, isError = callTool(t, qa, "get_team_audit", nil); !isError || strings.Contains(text, "mcp_token_issued") {
		t.Fatalf("QA must not read the team audit: %v %s", isError, text)
	}
	// Roles are read live: demoting the owner's function takes effect on the
	// next call with the same token.
	if text, _ = callTool(t, owner, "whoami", nil); !strings.Contains(text, "product_owner") {
		t.Fatalf("whoami: %s", text)
	}
	if err := env.srv.store.SetTeamMember("qa@example.com", []string{"qa", "reviewer"}, true, "owner@example.com", "roles", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if text, _ = callTool(t, qa, "whoami", nil); !strings.Contains(text, "reviewer") {
		t.Fatalf("role change must apply to the next call: %s", text)
	}
	// Revoking membership kills the token at once.
	if err := env.srv.store.SetTeamMember("qa@example.com", nil, false, "owner@example.com", "revoke", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if status := env.postRaw(t, qaToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil); status != http.StatusUnauthorized {
		t.Fatalf("revoked member's token: %d", status)
	}
}

func (e *mcpEnv) postRaw(t *testing.T, token, body string, headers http.Header) int {
	t.Helper()
	status, _ := e.postRawBody(t, token, body, headers)
	return status
}

func (e *mcpEnv) postRawBody(t *testing.T, token, body string, headers http.Header) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, e.http.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for name, values := range headers {
		request.Header[name] = values
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(data)
}

func TestMCPRejectsBadCredentialsUniformly(t *testing.T) {
	env := newMCPEnv(t)
	live, liveID := env.issueToken(t, "qa@example.com", `{"label":"x"}`)
	revoked, revokedID := env.issueToken(t, "qa@example.com", `{"label":"y"}`)
	if response := teamRequest(env.srv, env.cookies["qa@example.com"], env.csrf["qa@example.com"], http.MethodDelete, "/api/mcp/tokens/"+revokedID, ""); response.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", response.Code, response.Body.String())
	}
	expiring, _ := env.issueToken(t, "qa@example.com", `{"label":"z","ttl_days":1}`)
	if _, err := env.srv.store.MCPTokenByRaw(expiring, time.Now().Add(48*time.Hour)); err == nil {
		t.Fatal("expired token must not resolve")
	}
	manager := env.manager
	short, _ := cloudidentity.NewPrincipal("qa@example.com", []cloudidentity.Role{cloudidentity.RoleQA})
	expiredHMAC, _ := manager.Issue(short, time.Nanosecond)
	time.Sleep(1100 * time.Millisecond)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	var reference string
	for name, token := range map[string]string{
		"none": "", "garbage": "nope", "unknown-aitm": "aitm_" + strings.Repeat("A", 43), "revoked": revoked,
		"expired-hmac": expiredHMAC, "cookie-session-value": env.cookies["qa@example.com"].Value,
	} {
		status, response := env.postRawBody(t, token, body, nil)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, want 401", name, status)
		}
		if reference == "" {
			reference = response
		} else if response != reference {
			t.Fatalf("%s: response differs from the others (credential oracle): %q vs %q", name, response, reference)
		}
	}
	// A valid token plus a valid browser cookie still authenticates by token only.
	if status := env.postRaw(t, live, body, nil); status != http.StatusOK {
		t.Fatalf("live token: %d", status)
	}
	// The token authenticates MCP only; it is not a REST credential.
	request := newLoopbackRequest(http.MethodGet, "/api/runs", nil)
	request.Header.Set("Authorization", "Bearer "+live)
	response := httptest.NewRecorder()
	env.srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("MCP token must not open REST: %d", response.Code)
	}
	request = newLoopbackRequest(http.MethodGet, "/api/session", nil)
	request.Header.Set("Authorization", "Bearer "+live)
	response = httptest.NewRecorder()
	env.srv.router.ServeHTTP(response, request)
	if response.Code == http.StatusOK {
		t.Fatal("MCP token must not mint browser sessions")
	}
	_ = liveID
}

func TestMCPCookieSessionAloneIsNotEnough(t *testing.T) {
	env := newMCPEnv(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	request, _ := http.NewRequest(http.MethodPost, env.http.URL+"/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.AddCookie(env.cookies["owner@example.com"])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie only: %d", response.StatusCode)
	}
}

func TestMCPNegativeToolCallsDoNotLeak(t *testing.T) {
	env := newMCPEnv(t)
	token, _ := env.issueToken(t, "qa@example.com", `{"label":"x"}`)
	session := env.mustConnect(t, token)
	for name, c := range map[string]struct {
		tool, code string
		args       map[string]any
	}{
		"unknown run":         {"get_task", "not_found", map[string]any{"run_id": "no-such-run"}},
		"unknown run events":  {"list_task_events", "not_found", map[string]any{"run_id": "no-such-run"}},
		"unknown run log":     {"get_task_log", "", map[string]any{"run_id": "no-such-run", "attempt_id": "a"}},
		"unknown project":     {"get_project", "", map[string]any{"project_id": 9999}},
		"traversal artifact":  {"read_task_artifact", "bad_arguments", map[string]any{"run_id": mcpTestRun, "path": "../../../etc/passwd"}},
		"absolute artifact":   {"read_task_artifact", "bad_arguments", map[string]any{"run_id": mcpTestRun, "path": "/etc/passwd"}},
		"hidden evidence":     {"read_task_artifact", "", map[string]any{"run_id": mcpTestRun, "path": "approvals/secret.json"}},
		"slash in run id":     {"get_task", "bad_arguments", map[string]any{"run_id": "a/events"}},
		"route injection":     {"get_task_log", "bad_arguments", map[string]any{"run_id": mcpTestRun, "attempt_id": "../x"}},
		"missing argument":    {"get_task", "bad_arguments", nil},
		"unknown argument":    {"get_task", "bad_arguments", map[string]any{"run_id": mcpTestRun, "cwd": "/"}},
		"wrong type":          {"list_tasks", "bad_arguments", map[string]any{"limit": "ten"}},
		"limit out of range":  {"list_tasks", "bad_arguments", map[string]any{"limit": 1000}},
		"enum violation":      {"list_tasks", "bad_arguments", map[string]any{"mode": "chaos"}},
		"byte offset too far": {"get_task", "bad_arguments", map[string]any{"run_id": mcpTestRun, "byte_offset": 99999999}},
	} {
		text, isError := callTool(t, session, c.tool, c.args)
		if !isError {
			t.Errorf("%s: expected a tool error, got %s", name, text)
			continue
		}
		var failure struct {
			Error  string `json:"error"`
			Code   string `json:"code"`
			Status int    `json:"status"`
		}
		if err := json.Unmarshal([]byte(text), &failure); err != nil || failure.Error == "" || failure.Status < 400 {
			t.Errorf("%s: malformed error %q", name, text)
		}
		if c.code != "" && failure.Code != c.code {
			t.Errorf("%s: code %q, want %q (%s)", name, failure.Code, c.code, text)
		}
		for _, secret := range []string{env.target, "/etc/", "root:", "owner@example.com"} {
			if strings.Contains(text, secret) {
				t.Errorf("%s: error leaks %q: %s", name, secret, text)
			}
		}
	}
	// Unknown tool is a protocol error, not data.
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "drop_database"}); err == nil {
		t.Error("unknown tool must be rejected")
	}
}

func TestMCPToolsAreReadOnlyAndRegistered(t *testing.T) {
	env := newMCPEnv(t)
	token, _ := env.issueToken(t, "owner@example.com", `{"label":"x"}`)
	session := env.mustConnect(t, token)
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	registry := newMCPRegistry()
	registerMCPReadTools(registry)
	if len(listed.Tools) != len(registry.operations) || len(listed.Tools) < 20 {
		t.Fatalf("listed %d tools, registry has %d", len(listed.Tools), len(registry.operations))
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Description == "" {
			t.Errorf("tool %s must be annotated read-only and described", tool.Name)
		}
	}
	for _, operation := range registry.operations {
		if operation.Method != http.MethodGet {
			t.Errorf("%s: S12 registers reads only", operation.Name)
		}
	}
}

func TestMCPLegacyInitializeHandshakeStillWorks(t *testing.T) {
	env := newMCPEnv(t)
	token, _ := env.issueToken(t, "qa@example.com", `{"label":"x"}`)
	session, err := env.connect(t, token, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatalf("2025-11-25 client: %v", err)
	}
	if text, isError := callTool(t, session, "whoami", nil); isError || !strings.Contains(text, "qa@example.com") {
		t.Fatalf("whoami over legacy handshake: %v %s", isError, text)
	}
	// And the raw wire shape of a modern, sessionless request.
	status, body := env.postRawBody(t, token, `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		http.Header{"Mcp-Protocol-Version": {"2026-07-28"}, "Mcp-Method": {"tools/list"}})
	if status != http.StatusOK || !strings.Contains(body, `"whoami"`) {
		t.Fatalf("modern raw tools/list: %d %s", status, body)
	}
}

func TestMCPTransportShape(t *testing.T) {
	env := newMCPEnv(t)
	token, _ := env.issueToken(t, "qa@example.com", `{"label":"x"}`)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		request, _ := http.NewRequest(method, env.http.URL+"/mcp", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "text/event-stream")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s /mcp: %d, want 405", method, response.StatusCode)
		}
	}
	// A notification is acknowledged with 202 and no body.
	status, body := env.postRawBody(t, token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if status != http.StatusAccepted || strings.TrimSpace(body) != "" {
		t.Errorf("notification: %d %q", status, body)
	}
	// Tool results are plain JSON, not an SSE stream.
	request, _ := http.NewRequest(http.MethodPost, env.http.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") || response.Header.Get("Mcp-Session-Id") != "" {
		t.Errorf("response must be sessionless JSON: %q session=%q", response.Header.Get("Content-Type"), response.Header.Get("Mcp-Session-Id"))
	}
	// An unsupported protocol version is refused.
	if status = env.postRaw(t, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, http.Header{"Mcp-Protocol-Version": {"2024-01-01"}}); status != http.StatusBadRequest {
		t.Errorf("old protocol version: %d", status)
	}
	// Oversized bodies are cut off.
	huge := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami","arguments":{"x":"` + strings.Repeat("a", mcpMaxRequestBody+10) + `"}}}`
	if status = env.postRaw(t, token, huge, nil); status != http.StatusRequestEntityTooLarge && status != http.StatusBadRequest {
		t.Errorf("oversized body: %d", status)
	}
}

func TestMCPOriginPolicy(t *testing.T) {
	env := newMCPEnv(t, WithMCPAllowedOrigins("https://Claude.example:443", "http://allowed.example:8080"))
	token, _ := env.issueToken(t, "qa@example.com", `{"label":"x"}`)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	for origin, want := range map[string]int{
		"":                            http.StatusOK,
		env.http.URL:                  http.StatusOK, // the server's own origin
		"https://claude.example":      http.StatusOK, // allow-list, default port, case-insensitive
		"http://allowed.example:8080": http.StatusOK,
		"http://allowed.example":      http.StatusForbidden,
		"https://evil.example":        http.StatusForbidden,
		"null":                        http.StatusForbidden,
		"http://127.0.0.1:1":          http.StatusForbidden, // another local service
	} {
		headers := http.Header{}
		if origin != "" {
			headers.Set("Origin", origin)
		}
		if got := env.postRaw(t, token, body, headers); got != want {
			t.Errorf("Origin %q: %d, want %d", origin, got, want)
		}
	}
	// The Origin check precedes authentication: a foreign origin never reaches the verifier.
	if got := env.postRaw(t, "", body, http.Header{"Origin": {"https://evil.example"}}); got != http.StatusForbidden {
		t.Errorf("foreign origin without token: %d", got)
	}
	if _, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(env.manager), WithMCPAllowedOrigins("not a url")); err == nil {
		t.Error("an invalid allowed origin must fail startup")
	}
	if _, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(env.manager), WithMCPAllowedOrigins("https://host/path")); err == nil {
		t.Error("an origin with a path must fail startup")
	}
}

func TestMCPLargeResultsAreSlicedAndReassemble(t *testing.T) {
	env := newMCPEnv(t)
	reports := filepath.Join(env.target, ".ai-team", "runs", mcpTestRun, "reports")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		t.Fatal(err)
	}
	// Multi-byte text, so slice boundaries must respect UTF-8.
	original := strings.Repeat("строка отчёта — ✓\n", 40000)
	if err := os.WriteFile(filepath.Join(reports, "big.md"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	token, _ := env.issueToken(t, "qa@example.com", `{"label":"x"}`)
	session := env.mustConnect(t, token)
	var assembled strings.Builder
	offset, pages := 0, 0
	for {
		args := map[string]any{"run_id": mcpTestRun, "path": "reports/big.md"}
		if offset > 0 {
			args["byte_offset"] = offset
		}
		text, isError := callTool(t, session, "read_task_artifact", args)
		if isError {
			t.Fatal(text)
		}
		var page struct {
			Truncated bool   `json:"truncated"`
			Total     int    `json:"total_bytes"`
			Content   string `json:"content"`
			Next      int    `json:"next_byte_offset"`
		}
		if err := json.Unmarshal([]byte(text), &page); err != nil {
			t.Fatalf("page %d is not an envelope: %v", pages, err)
		}
		if len(text) > mcpMaxResultBytes*2 {
			t.Fatalf("page %d is %d bytes", pages, len(text))
		}
		assembled.WriteString(page.Content)
		pages++
		if !page.Truncated {
			break
		}
		offset = page.Next
	}
	if pages < 2 || assembled.String() != original {
		t.Fatalf("reassembled %d bytes in %d pages, want %d", assembled.Len(), pages, len(original))
	}
}

func TestMCPListsArePaginated(t *testing.T) {
	env := newMCPEnv(t)
	for i := 0; i < 5; i++ {
		if _, err := env.srv.store.CreateBacklogItem(1, "idea "+string(rune('A'+i)), "text", "feature-mcp", "owner@example.com", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	token, _ := env.issueToken(t, "qa@example.com", `{"label":"x"}`)
	session := env.mustConnect(t, token)
	seen := 0
	offset := 0
	for {
		args := map[string]any{"limit": 2}
		if offset > 0 {
			args["offset"] = offset
		}
		text, isError := callTool(t, session, "list_backlog", args)
		if isError {
			t.Fatal(text)
		}
		var page struct {
			Items   []json.RawMessage `json:"items"`
			Total   int               `json:"total"`
			HasMore bool              `json:"has_more"`
			Next    *int              `json:"next_offset"`
		}
		if err := json.Unmarshal([]byte(text), &page); err != nil || page.Total != 5 || len(page.Items) > 2 {
			t.Fatalf("page: %v %s", err, text)
		}
		seen += len(page.Items)
		if !page.HasMore {
			if page.Next != nil {
				t.Fatal("last page must not offer next_offset")
			}
			break
		}
		offset = *page.Next
	}
	if seen != 5 {
		t.Fatalf("saw %d of 5 backlog items", seen)
	}
	text, _ := callTool(t, session, "list_tasks", map[string]any{"limit": 1, "offset": 5})
	if !strings.Contains(text, `"runs":[]`) {
		t.Fatalf("an offset past the end must give an empty page: %s", text)
	}
}

func TestMCPResourcesServeTheSameData(t *testing.T) {
	env := newMCPEnv(t)
	token, _ := env.issueToken(t, "qa@example.com", `{"label":"x"}`)
	session := env.mustConnect(t, token)
	templates, err := session.ListResourceTemplates(context.Background(), nil)
	if err != nil || len(templates.ResourceTemplates) == 0 {
		t.Fatalf("resource templates: %v %+v", err, templates)
	}
	resources, err := session.ListResources(context.Background(), nil)
	if err != nil || len(resources.Resources) == 0 {
		t.Fatalf("resources: %v %+v", err, resources)
	}
	for uri, rest := range map[string]string{
		"aiteam://tasks/" + mcpTestRun: "/api/runs/" + mcpTestRun,
		"aiteam://gates/catalog":       "/api/gates/catalog",
		"aiteam://projects":            "/api/projects",
		"aiteam://projects/1":          "/api/projects/1",
		"aiteam://template/versions":   "/api/template/versions",
	} {
		result, readErr := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
		if readErr != nil || len(result.Contents) != 1 {
			t.Fatalf("read %s: %v", uri, readErr)
		}
		_, body := env.rest(t, "qa@example.com", rest)
		sameJSON(t, uri, []byte(result.Contents[0].Text), body)
	}
	for _, uri := range []string{"aiteam://tasks/no-such-run", "aiteam://tasks/a%2Fb", "aiteam://unknown", "aiteam://projects/abc"} {
		if _, err = session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("%s must not resolve", uri)
		}
	}
}

func TestMCPTokenEndpointsAreSelfServiceAndGuarded(t *testing.T) {
	env := newMCPEnv(t)
	owner, qa := "owner@example.com", "qa@example.com"
	_, qaTokenID := env.issueToken(t, qa, `{"label":"qa laptop","ttl_days":30}`)
	// The listing shows metadata and never the secret or its digest.
	response := teamRequest(env.srv, env.cookies[qa], env.csrf[qa], http.MethodGet, "/api/mcp/tokens", "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "aitm_") || strings.Contains(response.Body.String(), "token_hash") ||
		!strings.Contains(response.Body.String(), "qa laptop") {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	// The owner sees only their own (none) tokens, and cannot revoke the QA token.
	response = teamRequest(env.srv, env.cookies[owner], env.csrf[owner], http.MethodGet, "/api/mcp/tokens", "")
	if strings.Contains(response.Body.String(), "qa laptop") {
		t.Fatalf("a user listed someone else's tokens: %s", response.Body.String())
	}
	if response = teamRequest(env.srv, env.cookies[owner], env.csrf[owner], http.MethodDelete, "/api/mcp/tokens/"+qaTokenID, ""); response.Code != http.StatusNotFound {
		t.Fatalf("revoking another user's token: %d", response.Code)
	}
	// A request body cannot name another owner.
	response = teamRequest(env.srv, env.cookies[qa], env.csrf[qa], http.MethodPost, "/api/mcp/tokens", `{"label":"x","actor_id":"owner@example.com"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field must be rejected: %d %s", response.Code, response.Body.String())
	}
	// CSRF, session, label and TTL are enforced.
	if response = teamRequest(env.srv, env.cookies[qa], "wrong", http.MethodPost, "/api/mcp/tokens", `{"label":"x"}`); response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d", response.Code)
	}
	anonymous := httptest.NewRecorder()
	env.srv.router.ServeHTTP(anonymous, newLoopbackRequest(http.MethodPost, "/api/mcp/tokens", strings.NewReader(`{"label":"x"}`)))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous issue: %d", anonymous.Code)
	}
	for _, body := range []string{`{"label":""}`, `{"label":"` + strings.Repeat("я", 81) + `"}`, `{"label":"a\nb"}`, `{"label":"x","ttl_days":400}`, `{"label":"x","ttl_days":-1}`} {
		if response = teamRequest(env.srv, env.cookies[qa], env.csrf[qa], http.MethodPost, "/api/mcp/tokens", body); response.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, response.Code)
		}
	}
	// An MCP token cannot manage tokens: the route needs a browser session.
	token, _ := env.issueToken(t, qa, `{"label":"y"}`)
	request := newLoopbackRequest(http.MethodPost, "/api/mcp/tokens", strings.NewReader(`{"label":"escalate"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	denied := httptest.NewRecorder()
	env.srv.router.ServeHTTP(denied, request)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("MCP token minting tokens: %d", denied.Code)
	}
	// Per-user cap.
	for i := 0; i < store.MaxActiveMCPTokens; i++ {
		response = teamRequest(env.srv, env.cookies[owner], env.csrf[owner], http.MethodPost, "/api/mcp/tokens", `{"label":"bulk"}`)
		if i < store.MaxActiveMCPTokens && response.Code != http.StatusCreated {
			t.Fatalf("token %d: %d", i, response.Code)
		}
	}
	if response = teamRequest(env.srv, env.cookies[owner], env.csrf[owner], http.MethodPost, "/api/mcp/tokens", `{"label":"over"}`); response.Code != http.StatusConflict {
		t.Fatalf("cap: %d", response.Code)
	}
	// Issue and revoke are audited.
	audit, err := env.srv.store.ListTeamAudit(500)
	if err != nil {
		t.Fatal(err)
	}
	issued := 0
	for _, entry := range audit {
		if entry.Action == "mcp_token_issued" {
			issued++
		}
	}
	if issued < 2 {
		t.Fatalf("token issues must be audited, found %d", issued)
	}
}

func TestMCPLocalModeAcceptsOnlyTheLocalToken(t *testing.T) {
	const localToken = "local-mcp-mode-token-0123456789abcdef"
	verifier, err := NewLocalAuthenticator(localToken)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"), WithTargetDir(target), WithLocalAuthenticator(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	httpServer := httptest.NewServer(srv.router)
	defer httpServer.Close()
	env := &mcpEnv{srv: srv, http: httpServer, target: target}
	session := env.mustConnect(t, localToken)
	if text, isError := callTool(t, session, "list_projects", nil); isError || !strings.Contains(text, "Основной") {
		t.Fatalf("local token over MCP: %v %s", isError, text)
	}
	if status := env.postRaw(t, "aitm_"+strings.Repeat("A", 43), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil); status != http.StatusUnauthorized {
		t.Fatalf("personal token on a local server: %d", status)
	}
	// A non-loopback Host is refused even with the right token (DNS rebinding).
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Host = "attacker.example"
	request.Header.Set("Authorization", "Bearer "+localToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host: %d", response.StatusCode)
	}
	// Personal tokens are a cloud feature.
	_ = srv
}

func TestMCPServerWithoutAuthenticationRefusesService(t *testing.T) {
	target := t.TempDir()
	srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"), WithTargetDir(target))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	request := newLoopbackRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unauthenticated server must not serve MCP: %d", response.Code)
	}
}

func TestMCPRegistryGuards(t *testing.T) {
	operation := mcpOperation{Name: "t", Path: "/api/runs/{run_id}/artifacts/{path}", Params: []mcpParam{
		{Name: "run_id", Kind: mcpString, Required: true}, {Name: "path", Kind: mcpString, Required: true, Subpath: true}}}
	for _, values := range []map[string]string{
		{"run_id": "a/b", "path": "x"}, {"run_id": "..", "path": "x"}, {"run_id": "r", "path": "../x"},
		{"run_id": "r", "path": "a//b"}, {"run_id": "r", "path": `a\b`}, {"run_id": "", "path": "x"},
	} {
		if _, err := operation.restPath(values); err == nil {
			t.Errorf("%v must be rejected", values)
		}
	}
	if got, err := operation.restPath(map[string]string{"run_id": "r1", "path": "reports/a.md"}); err != nil || got != "/api/runs/r1/artifacts/reports/a.md" {
		t.Errorf("restPath = %q %v", got, err)
	}
	defer func() {
		if recover() == nil {
			t.Error("registering a non-read operation in S12 must panic")
		}
	}()
	newMCPRegistry().add(mcpOperation{Name: "write", Method: http.MethodPost, Path: "/api/runs"})
}

func TestMCPSliceKeepsUTF8AndReportsContinuation(t *testing.T) {
	body := []byte(strings.Repeat("я", mcpMaxResultBytes)) // 2 bytes per rune
	first, err := mcpSlice(body, 0)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Content string `json:"content"`
		Next    int    `json:"next_byte_offset"`
	}
	if err = json.Unmarshal([]byte(first), &envelope); err != nil || envelope.Next == 0 || envelope.Next%2 != 0 || len(envelope.Content) != envelope.Next {
		t.Fatalf("first slice: %v next=%d len=%d", err, envelope.Next, len(envelope.Content))
	}
	if _, err = mcpSlice([]byte{0xff, 0xfe}, 0); err == nil {
		t.Error("binary data must not be returned as text")
	}
	if text, err := mcpSlice([]byte(`{"a":1}`), 0); err != nil || text != `{"a":1}` {
		t.Errorf("small body must pass through verbatim: %q %v", text, err)
	}
}

func TestMatchMCPResourceURI(t *testing.T) {
	values, ok := matchMCPResourceURI("aiteam://tasks/{run_id}/stages/{stage_id}", "aiteam://tasks/r%2D1/stages/s")
	if !ok || values["run_id"] != "r-1" || values["stage_id"] != "s" {
		t.Fatalf("%v %v", values, ok)
	}
	for _, uri := range []string{"aiteam://tasks//stages/s", "aiteam://tasks/r/stages", "aiteam://tasks/r/other/s", "aiteam://tasks/a/b/stages/s"} {
		if _, ok = matchMCPResourceURI("aiteam://tasks/{run_id}/stages/{stage_id}", uri); ok {
			t.Errorf("%s must not match", uri)
		}
	}
}

// stripPagination removes the paging metadata MCP adds to locally paginated
// lists, leaving the REST-identical payload.
func stripPagination(t *testing.T, text []byte, tool string) []byte {
	t.Helper()
	registry := newMCPRegistry()
	registerMCPReadTools(registry)
	for _, operation := range registry.operations {
		if operation.Name != tool || operation.ArrayKey == "" {
			continue
		}
		var document map[string]json.RawMessage
		if err := json.Unmarshal(text, &document); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"total", "limit", "offset", "has_more", "next_offset"} {
			delete(document, key)
		}
		stripped, _ := json.Marshal(document)
		return stripped
	}
	return text
}

// An MCP request must never create the first team member, even with a valid
// server token that carries the Product Owner role.
func TestMCPNeverBootstrapsTheTeam(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srv, err := NewServer(":memory:", "", filepath.Join(dir, "artifacts"), WithTargetDir(dir), WithAuthenticator(manager), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	owner, _ := cloudidentity.NewPrincipal("owner@example.com", []cloudidentity.Role{cloudidentity.RoleProductOwner})
	token, err := manager.Issue(owner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.router)
	t.Cleanup(httpServer.Close)
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", response.StatusCode)
	}
	if count, countErr := srv.store.TeamMemberCount(); countErr != nil || count != 0 {
		t.Fatalf("MCP created a team member: count=%d err=%v", count, countErr)
	}
}
