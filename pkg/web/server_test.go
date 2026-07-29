package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

type fakeRunController struct {
	startFeature string
	startTask    string
	startCalls   int
	resumeRunID  string
	cancelRunID  string
	decision     approval.Decision
	approvalID   string
	runID        string
	approvals    []approval.PendingApproval
}

func (f *fakeRunController) Start(feature, task string) (string, error) {
	f.startCalls++
	f.startFeature, f.startTask = feature, task
	return "run-created", nil
}
func (f *fakeRunController) Resume(runID string) error { f.resumeRunID = runID; return nil }
func (f *fakeRunController) Cancel(runID string) error { f.cancelRunID = runID; return nil }
func (f *fakeRunController) Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error) {
	f.runID, f.approvalID, f.decision = runID, approvalID, decision
	return approval.PendingApproval{ID: approvalID, RunID: runID, Status: approval.StatusResolved, ResolvedAction: decision.Action}, nil
}
func (f *fakeRunController) Approvals(string) ([]approval.PendingApproval, error) {
	return f.approvals, nil
}
func (f *fakeRunController) Preflight(context.Context) preflight.Report {
	return preflight.Report{Ready: true, CheckedAt: time.Now().UTC(), Checks: []preflight.Check{{
		ID: "opencode", Status: preflight.StatusPassed, Required: true, Message: "opencode test",
	}}}
}

func TestPreflightEndpoint(t *testing.T) {
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, newLoopbackRequest(http.MethodGet, "/api/preflight", nil))
	if writer.Code != http.StatusOK {
		t.Fatalf("preflight: %d %s", writer.Code, writer.Body.String())
	}
	var report preflight.Report
	if err := json.NewDecoder(writer.Body).Decode(&report); err != nil || !report.Ready || len(report.Checks) != 1 {
		t.Fatalf("preflight report: %+v, %v", report, err)
	}
}

func TestRunLogReturnsBoundedTail(t *testing.T) {
	srv, artifactRoot := newTestServer(t)
	run := &store.PipelineRun{RunID: "run-log", Feature: "feat", Status: "running", StartedAt: time.Now()}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(filepath.Dir(artifactRoot), "runs", run.RunID, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("x", maxLogTailSize) + "TAIL"
	if err := os.WriteFile(filepath.Join(logDir, "001-agent.log"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, newLoopbackRequest(http.MethodGet, "/api/runs/run-log/logs/001-agent", nil))
	if writer.Code != http.StatusOK {
		t.Fatalf("log: %d %s", writer.Code, writer.Body.String())
	}
	var tail logTail
	if err := json.NewDecoder(writer.Body).Decode(&tail); err != nil {
		t.Fatal(err)
	}
	if !tail.Truncated || tail.Offset != 4 || len(tail.Content) != maxLogTailSize || !strings.HasSuffix(tail.Content, "TAIL") {
		t.Fatalf("unexpected log tail: offset=%d truncated=%t size=%d", tail.Offset, tail.Truncated, len(tail.Content))
	}
}

func TestRunLogRejectsUnsafeIdentity(t *testing.T) {
	for _, value := range []string{"", ".", "..", "../attempt", "dir/attempt", `dir\attempt`} {
		if safeIdentity(value) {
			t.Errorf("identity %q должна быть отклонена", value)
		}
	}
}

func TestRunWorkflowReturnsImmutableSnapshot(t *testing.T) {
	srv, artifactRoot := newTestServer(t)
	run := &store.PipelineRun{RunID: "run-graph", Feature: "feat", Status: "running", StartedAt: time.Now()}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(filepath.Dir(artifactRoot), "runs", run.RunID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	snapshot := `{"schema_version":2,"graph":{"entry":"analyst","nodes":[],"edges":[]}}`
	if err := os.WriteFile(filepath.Join(runDir, "workflow.json"), []byte(snapshot), 0644); err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, newLoopbackRequest(http.MethodGet, "/api/runs/run-graph/workflow", nil))
	if writer.Code != http.StatusOK || strings.TrimSpace(writer.Body.String()) != snapshot {
		t.Fatalf("workflow: %d %s", writer.Code, writer.Body.String())
	}
}

// newLoopbackRequest wraps httptest.NewRequest and sets Host to a loopback
// value: httptest.NewRequest defaults Host to "example.com" for relative
// targets, which sameOriginMiddleware now correctly rejects (the CLI only
// ever binds this server to a loopback address in practice).
func newLoopbackRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = "127.0.0.1"
	return req
}

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	artifactRoot := t.TempDir()
	srv, err := NewServer(":memory:", "", artifactRoot)
	if err != nil {
		t.Fatalf("failed to create test server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, artifactRoot
}

func authorizedRequest(t *testing.T, srv *Server, method, target, body string) *http.Request {
	t.Helper()
	sessionRequest := newLoopbackRequest("GET", "/api/session", nil)
	sessionWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(sessionWriter, sessionRequest)
	if sessionWriter.Code != http.StatusOK {
		t.Fatalf("session bootstrap: %d %s", sessionWriter.Code, sessionWriter.Body.String())
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(sessionWriter.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	response := sessionWriter.Result()
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookie отсутствует: %v", cookies)
	}
	request := newLoopbackRequest(method, target, strings.NewReader(body))
	request.AddCookie(cookies[0])
	request.Header.Set("X-CSRF-Token", session.CSRFToken)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestWriteAPIRequiresSessionAndCSRF(t *testing.T) {
	controller := &fakeRunController{}
	artifactRoot := t.TempDir()
	srv, err := NewServer(":memory:", "", artifactRoot, WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	noSession := newLoopbackRequest("POST", "/api/runs", strings.NewReader(`{"feature":"f","task":"t"}`))
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, noSession)
	if writer.Code != http.StatusUnauthorized {
		t.Fatalf("без session: %d", writer.Code)
	}

	sessionRequest := newLoopbackRequest("GET", "/api/session", nil)
	sessionWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(sessionWriter, sessionRequest)
	noCSRF := newLoopbackRequest("POST", "/api/runs", strings.NewReader(`{"feature":"f","task":"t"}`))
	noCSRF.AddCookie(sessionWriter.Result().Cookies()[0])
	writer = httptest.NewRecorder()
	srv.router.ServeHTTP(writer, noCSRF)
	if writer.Code != http.StatusForbidden {
		t.Fatalf("без CSRF: %d", writer.Code)
	}
}

func TestWriteRunAndDecisionCommands(t *testing.T) {
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	start := authorizedRequest(t, srv, "POST", "/api/runs", `{"feature":"feat","task":"задача"}`)
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, start)
	if writer.Code != http.StatusAccepted || controller.startFeature != "feat" || controller.startTask != "задача" {
		t.Fatalf("start: code=%d controller=%+v body=%s", writer.Code, controller, writer.Body.String())
	}

	bad := authorizedRequest(t, srv, "POST", "/api/runs", `{"feature":"feat","task":"задача","unknown":true}`)
	writer = httptest.NewRecorder()
	srv.router.ServeHTTP(writer, bad)
	if writer.Code != http.StatusBadRequest || controller.startCalls != 1 {
		t.Fatalf("unknown JSON field запустил worker: code=%d calls=%d", writer.Code, controller.startCalls)
	}

	decision := authorizedRequest(t, srv, "POST",
		"/api/runs/run-1/approvals/approval-1/decisions",
		`{"actor_id":"user-1","actor_role":"qa","action":"approve","comment":"проверено","subject_hash":"`+testSubjectHash+`"}`)
	writer = httptest.NewRecorder()
	srv.router.ServeHTTP(writer, decision)
	if writer.Code != http.StatusOK || controller.runID != "run-1" ||
		controller.approvalID != "approval-1" || controller.decision.ActorID != "user-1" {
		t.Fatalf("decision: code=%d controller=%+v body=%s", writer.Code, controller, writer.Body.String())
	}
}

func TestCloudAuthenticationAndRBACUseTrustedPrincipal(t *testing.T) {
	controller := &fakeRunController{}
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cloudidentity.NewPrincipal("reviewer-1", []cloudidentity.Role{cloudidentity.RoleReviewer})
	if err != nil {
		t.Fatal(err)
	}
	token, err := manager.Issue(principal, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(),
		WithRunController(controller), WithAuthenticator(manager))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, newLoopbackRequest("GET", "/api/pipelines", nil))
	if writer.Code != http.StatusUnauthorized {
		t.Fatalf("cloud read без session: %d", writer.Code)
	}

	sessionRequest := newLoopbackRequest("GET", "/api/session", nil)
	sessionRequest.Header.Set("Authorization", "Bearer "+token)
	sessionWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(sessionWriter, sessionRequest)
	if sessionWriter.Code != http.StatusOK {
		t.Fatalf("cloud session: %d %s", sessionWriter.Code, sessionWriter.Body.String())
	}
	var session sessionResponse
	if err := json.NewDecoder(sessionWriter.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	cookie := sessionWriter.Result().Cookies()[0]

	command := func(target, body string) *httptest.ResponseRecorder {
		request := newLoopbackRequest("POST", target, strings.NewReader(body))
		request.AddCookie(cookie)
		request.Header.Set("X-CSRF-Token", session.CSRFToken)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)
		return response
	}
	if response := command("/api/runs", `{"feature":"f","task":"t"}`); response.Code != http.StatusForbidden {
		t.Fatalf("reviewer не должен создавать run: %d %s", response.Code, response.Body.String())
	}
	response := command("/api/runs/run-1/approvals/approval-1/decisions",
		`{"actor_id":"spoofed","actor_role":"reviewer","action":"approve","subject_hash":"`+testSubjectHash+`"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("reviewer decision: %d %s", response.Code, response.Body.String())
	}
	if controller.decision.ActorID != "reviewer-1" || controller.decision.ActorRole != "reviewer" {
		t.Fatalf("decision audit использовал недоверенную identity: %+v", controller.decision)
	}
}

const testSubjectHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGetPipelines_Empty(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/pipelines", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var runs []interface{}
	if err := json.NewDecoder(w.Body).Decode(&runs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected empty array, got %d items", len(runs))
	}
}

func TestGetPipelines_WithData(t *testing.T) {
	srv, _ := newTestServer(t)

	srv.Store().CreatePipelineRun(&store.PipelineRun{
		Feature:   "test-feat",
		Status:    "running",
		StartedAt: time.Now(),
	})

	req := newLoopbackRequest("GET", "/api/pipelines", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	var runs []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&runs)

	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0]["feature"] != "test-feat" {
		t.Errorf("expected feature 'test-feat', got %v", runs[0]["feature"])
	}
}

func TestGetPipelinesPagination(t *testing.T) {
	srv, _ := newTestServer(t)
	for index := 0; index < 3; index++ {
		if err := srv.Store().CreatePipelineRun(&store.PipelineRun{RunID: fmt.Sprintf("run-%d", index), Feature: "f", Status: "completed", StartedAt: time.Now().Add(time.Duration(index) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	req := newLoopbackRequest("GET", "/api/pipelines?limit=1&offset=1", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	var runs []store.PipelineRun
	_ = json.NewDecoder(w.Body).Decode(&runs)
	if w.Code != http.StatusOK || len(runs) != 1 || w.Header().Get("X-Total-Count") != "3" {
		t.Fatalf("pagination response: code=%d total=%s runs=%+v", w.Code, w.Header().Get("X-Total-Count"), runs)
	}
	bad := newLoopbackRequest("GET", "/api/pipelines?limit=1000", nil)
	badWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(badWriter, bad)
	if badWriter.Code != http.StatusBadRequest {
		t.Fatalf("invalid pagination must be 400, got %d", badWriter.Code)
	}
}

func TestGetPipelineByID(t *testing.T) {
	srv, _ := newTestServer(t)

	run := &store.PipelineRun{Feature: "detail-test", Status: "completed", StartedAt: time.Now()}
	srv.Store().CreatePipelineRun(run)

	req := newLoopbackRequest("GET", "/api/pipelines/1", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["run"] == nil {
		t.Error("expected 'run' in response")
	}
	if resp["stages"] == nil {
		t.Error("expected 'stages' in response")
	}
}

func TestGetPipelineByID_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/pipelines/999", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetPipelineByID_InvalidID(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/pipelines/abc", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestGetArtifacts_ListsFeatureFiles(t *testing.T) {
	srv, root := newTestServer(t)

	run := &store.PipelineRun{Feature: "feat-x", Status: "completed", StartedAt: time.Now()}
	srv.Store().CreatePipelineRun(run)

	featureDir := filepath.Join(root, "feat-x")
	os.MkdirAll(featureDir, 0755)
	os.WriteFile(filepath.Join(featureDir, "proposal.md"), []byte("# P"), 0644)

	req := newLoopbackRequest("GET", "/api/pipelines/1/artifacts", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var artifacts []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&artifacts)
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	if artifacts[0]["path"] != "feat-x/proposal.md" {
		t.Errorf("path = %v", artifacts[0]["path"])
	}
}

func TestGetArtifactsUnknownRunReturnsNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	request := newLoopbackRequest(http.MethodGet, "/api/pipelines/999/artifacts", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", response.Code, response.Body.String())
	}
}

func TestGetArtifactsUsesImmutableRunEvidence(t *testing.T) {
	srv, root := newTestServer(t)
	runID := "20260720T000000.000000000Z-0123456789abcdef"
	run := &store.PipelineRun{RunID: runID, Feature: "feat", Status: "completed", StartedAt: time.Now()}
	if err := srv.Store().CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(filepath.Dir(root), "runs", runID)
	evidenceFile := filepath.Join(runDir, "attempts", "001-analyst", "artifacts", "proposal", "proposal.md")
	if err := os.MkdirAll(filepath.Dir(evidenceFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidenceFile, []byte("immutable"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "feat"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "feat", "proposal.md"), []byte("live-mutated"), 0644)

	req := newLoopbackRequest("GET", fmt.Sprintf("/api/pipelines/%d/artifacts", run.ID), nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	var artifacts []artifactInfo
	_ = json.NewDecoder(w.Body).Decode(&artifacts)
	if len(artifacts) != 1 || artifacts[0].RunID != runID {
		t.Fatalf("immutable listing: %+v", artifacts)
	}
	raw := newLoopbackRequest("GET", "/api/runs/"+runID+"/artifacts/"+artifacts[0].Path, nil)
	rawWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(rawWriter, raw)
	if rawWriter.Code != http.StatusOK || rawWriter.Body.String() != "immutable" {
		t.Fatalf("immutable artifact: code=%d body=%q", rawWriter.Code, rawWriter.Body.String())
	}
}

// Артефакты отдаются ТОЛЬКО внутри artifactRoot: абсолютные пути и traversal
// за пределы корня недоступны (регрессия против arbitrary file read).
func TestGetArtifact_ConfinedToRoot(t *testing.T) {
	srv, root := newTestServer(t)

	os.MkdirAll(filepath.Join(root, "feat"), 0755)
	os.WriteFile(filepath.Join(root, "feat", "review.md"), []byte("# Review"), 0644)

	// Файл вне корня
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	os.WriteFile(outside, []byte("secret"), 0644)
	t.Cleanup(func() { os.Remove(outside) })

	t.Run("valid relative path", func(t *testing.T) {
		req := newLoopbackRequest("GET", "/api/artifacts/feat/review.md", nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/markdown" {
			t.Errorf("expected text/markdown, got %s", ct)
		}
		if w.Body.String() != "# Review" {
			t.Errorf("body = %q", w.Body.String())
		}
	})

	for _, path := range []string{
		"/api/artifacts/../secret.txt",
		"/api/artifacts/feat/../../secret.txt",
		"/api/artifacts/" + outside, // абсолютный путь
		"/api/artifacts/etc/passwd",
	} {
		t.Run(path, func(t *testing.T) {
			req := newLoopbackRequest("GET", path, nil)
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)
			if w.Code == http.StatusOK {
				t.Errorf("путь %q не должен отдаваться (код %d)", path, w.Code)
			}
		})
	}
}

func TestGetArtifact_RejectsSymlinkOutsideRoot(t *testing.T) {
	srv, root := newTestServer(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	os.WriteFile(outside, []byte("secret"), 0644)
	os.MkdirAll(filepath.Join(root, "feat"), 0755)
	link := filepath.Join(root, "feat", "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	req := newLoopbackRequest("GET", "/api/artifacts/feat/link.md", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("symlink outside artifact root не должен читаться: %s", w.Body.String())
	}
}

func TestGetArtifact_RejectsOversizedFile(t *testing.T) {
	srv, root := newTestServer(t)
	path := filepath.Join(root, "large.md")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxArtifactSize + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	req := newLoopbackRequest("GET", "/api/artifacts/large.md", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", w.Code)
	}
}

func TestGetArtifact_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/artifacts/nonexistent.md", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestNoCORSWildcard(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/pipelines", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS wildcard не должен выставляться")
	}
}

func TestSameOriginMiddlewareRejectsHostileHost(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/pipelines", nil)
	req.Host = "evil.example"
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("non-loopback Host must be rejected, got %d", w.Code)
	}
}

func TestSameOriginMiddlewareRejectsHostileOrigin(t *testing.T) {
	srv, _ := newTestServer(t)

	// Simulates DNS rebinding: Host is loopback (the connection really did
	// land here), but Origin reflects the attacker's domain from the
	// browser's address bar.
	req := newLoopbackRequest("GET", "/api/pipelines", nil)
	req.Header.Set("Origin", "http://evil.example")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("hostile Origin must be rejected even with a loopback Host, got %d", w.Code)
	}
}

func TestSameOriginMiddlewareAllowsLoopbackRequests(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, host := range []string{"127.0.0.1", "127.0.0.1:8080", "localhost", "localhost:8080", "[::1]:8080"} {
		req := newLoopbackRequest("GET", "/api/pipelines", nil)
		req.Host = host
		req.Header.Set("Origin", "http://"+host)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code == http.StatusForbidden {
			t.Errorf("loopback host %q must not be rejected, got 403", host)
		}
	}
}

func TestSPAHandler(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>SPA</html>"), 0644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('hi')"), 0644)

	handler := spaHandler(dir)

	t.Run("serves existing file", func(t *testing.T) {
		req := newLoopbackRequest("GET", "/app.js", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("falls back to index.html for unknown routes", func(t *testing.T) {
		req := newLoopbackRequest("GET", "/unknown/route", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if w.Body.String() != "<html>SPA</html>" {
			t.Errorf("expected SPA fallback, got %q", w.Body.String())
		}
	})
}

func TestNewServerFallsBackToEmbeddedFrontend(t *testing.T) {
	artifactRoot := filepath.Join(t.TempDir(), "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", filepath.Join(t.TempDir(), "missing-dist"), artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	request := newLoopbackRequest(http.MethodGet, "/pipelines/123", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected embedded SPA response, got %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `<div id="root"></div>`) {
		t.Fatalf("response does not contain embedded frontend marker: %q", response.Body.String())
	}
}
