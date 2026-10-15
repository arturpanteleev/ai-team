package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/tracker"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// controllerCall is one stage action that reached the task controller.
type controllerCall struct {
	Action, RunID, StageID, Actor, Role, Reason, Extra string
}

// actionRecorder is a task controller that accepts every connected action and
// remembers who asked, so a test can prove that an MCP call reached the same
// controller method as the web UI, under the caller's identity and nobody
// else's.
type actionRecorder struct {
	*fakeRunController
	mu         sync.Mutex
	calls      []controllerCall
	startCount int
}

func (c *actionRecorder) record(call controllerCall) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
	return nil
}

func (c *actionRecorder) callsOf(action string) []controllerCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []controllerCall
	for _, call := range c.calls {
		if call.Action == action {
			out = append(out, call)
		}
	}
	return out
}

func (c *actionRecorder) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *actionRecorder) decisions() (int, approval.Decision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fakeRunController.decisionCalls, c.fakeRunController.decision
}

func (c *actionRecorder) StartTask(_ context.Context, feature, task, title, idea, mode string, links []string, admit func(string) error) (string, error) {
	c.mu.Lock()
	c.startCount++
	runID := "run-started-" + string(rune('a'+c.startCount))
	c.mu.Unlock()
	if err := admit(runID); err != nil {
		return "", err
	}
	_ = c.record(controllerCall{Action: "start", RunID: runID, Extra: feature + "|" + task + "|" + title + "|" + mode + "|" + strings.Join(links, ",")})
	return runID, nil
}

func (c *actionRecorder) RunStageAgent(_ context.Context, runID, stageID, actor string, refine bool) error {
	extra := "run"
	if refine {
		extra = "refine"
	}
	return c.record(controllerCall{Action: "run-agent", RunID: runID, StageID: stageID, Actor: actor, Extra: extra})
}

func (c *actionRecorder) ChangeStageExecutor(runID, stageID, executor, actor string) error {
	return c.record(controllerCall{Action: "executor", RunID: runID, StageID: stageID, Actor: actor, Extra: executor})
}

func (c *actionRecorder) OverrideStageCheck(_ context.Context, runID, stageID, reason, actor, role string) error {
	return c.record(controllerCall{Action: "override", RunID: runID, StageID: stageID, Actor: actor, Role: role, Reason: reason})
}

func (c *actionRecorder) SkipStage(_ context.Context, runID, stageID, reason, actor, role string) error {
	return c.record(controllerCall{Action: "skip", RunID: runID, StageID: stageID, Actor: actor, Role: role, Reason: reason})
}

func (c *actionRecorder) ReturnStage(_ context.Context, runID, stageID, to, reason, actor, role string) error {
	return c.record(controllerCall{Action: "return", RunID: runID, StageID: stageID, Actor: actor, Role: role, Reason: reason, Extra: to})
}

func (c *actionRecorder) AnswerStageGrill(_ context.Context, runID, stageID, answer, actor, role string) error {
	return c.record(controllerCall{Action: "grill-answer", RunID: runID, StageID: stageID, Actor: actor, Role: role, Extra: answer})
}

func (c *actionRecorder) SubmitStageGrillVersion(_ context.Context, runID, stageID, answer, actor, role string) error {
	return c.record(controllerCall{Action: "grill-version", RunID: runID, StageID: stageID, Actor: actor, Role: role, Extra: answer})
}

func (c *actionRecorder) CompleteStageGrill(_ context.Context, runID, stageID, actor, role string) error {
	return c.record(controllerCall{Action: "grill-done", RunID: runID, StageID: stageID, Actor: actor, Role: role})
}

func (c *actionRecorder) RetryStageGates(_ context.Context, runID, stageID string, gates []string, reason, actor, role string) error {
	return c.record(controllerCall{Action: "gate-retry", RunID: runID, StageID: stageID, Actor: actor, Role: role, Reason: reason, Extra: strings.Join(gates, ",")})
}

func (c *actionRecorder) Resume(runID string) error {
	_ = c.record(controllerCall{Action: "resume", RunID: runID})
	return c.fakeRunController.Resume(runID)
}

func (c *actionRecorder) Cancel(runID string) error {
	_ = c.record(controllerCall{Action: "cancel", RunID: runID})
	return c.fakeRunController.Cancel(runID)
}

func (c *actionRecorder) ResumeWithBudgetAllowance(runID, actor, reason string) error {
	return c.record(controllerCall{Action: "resume-budget", RunID: runID, Actor: actor, Reason: reason})
}

func (c *actionRecorder) deliveries() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fakeRunController.deliveryCalls
}

func (c *actionRecorder) DeliverDeferred(ctx context.Context, runID string) (delivery.TerminalRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fakeRunController.DeliverDeferred(ctx, runID)
}

// mcpActionEnv is a cloud-mode server with a team, a recording controller, a
// fake task tracker and seeded tasks, reachable over real HTTP so that MCP
// clients of the official SDK drive it.
type mcpActionEnv struct {
	*mcpEnv
	ctl     *actionRecorder
	tracker *tracker.Fake
	tokens  map[string]string
}

const (
	actorOwner    = "owner@example.com" // Product Owner and system administrator (team bootstrap)
	actorPO       = "po@example.com"    // Product Owner without the administrator right
	actorDev      = "dev@example.com"
	actorReviewer = "reviewer@example.com"
	actorQA       = "qa@example.com"
	actorAdmin    = "admin@example.com" // system administrator without a stage function
	actorBoth     = "both@example.com"  // developer and reviewer at once
)

// newMCPActionEnv seeds the given runs from the function-permissions template
// (spec, build with an approver, accept) with self-approval forbidden.
func newMCPActionEnv(t *testing.T, approvals []approval.PendingApproval, runs map[string]lifecycle.State) *mcpActionEnv {
	t.Helper()
	return newMCPActionEnvFor(t, functionPermissionsTemplate, approvals, runs)
}

// newMCPActionEnvFor is newMCPActionEnv over another template (%FORBID% is set to true).
func newMCPActionEnvFor(t *testing.T, template string, approvals []approval.PendingApproval, runs map[string]lifecycle.State) *mcpActionEnv {
	t.Helper()
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yamlData := strings.Replace(template, "%FORBID%", "true", 1)
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), []byte(yamlData), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	controller := &actionRecorder{fakeRunController: &fakeRunController{approvals: approvals}}
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"}, tracker.FakeIssueSeed{Key: "APP-2"})
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"), WithTargetDir(target),
		WithAuthenticator(manager), WithRunController(controller), WithTracker(fake, trackerTestMaps, "https://ai-team.example"),
		WithTrackerInbound([]tracker.InboundRule{{Status: "Done", Action: tracker.ActionAdvance}}, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	templateStore, err := srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	for runID, state := range runs {
		if _, err = templateStore.PinCurrentForRun(runID, runID); err != nil {
			t.Fatal(err)
		}
		if err = srv.store.CreatePipelineRun(&store.PipelineRun{RunID: runID, Feature: "feature",
			Status: "waiting_for_approval", StartedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		state.RunID, state.Feature, state.TargetDir, state.Task = runID, "feature", target, "task"
		state.ConfigSHA256, state.WorkflowSHA256 = strings.Repeat("0", 64), strings.Repeat("1", 64)
		state.CreatedAt = time.Now().UTC()
		if err = stateStore.Create(state); err != nil {
			t.Fatal(err)
		}
	}
	base := &mcpEnv{srv: srv, manager: manager, target: target}
	base.http = httptest.NewServer(srv.router)
	t.Cleanup(base.http.Close)
	cookie, csrf := cloudSessionForTest(t, srv, manager, actorOwner, cloudidentity.RoleProductOwner)
	base.cookies = map[string]*http.Cookie{actorOwner: cookie}
	base.csrf = map[string]string{actorOwner: csrf}
	env := &mcpActionEnv{mcpEnv: base, ctl: controller, tracker: fake, tokens: map[string]string{}}
	env.addMember(t, actorPO, "product_owner")
	env.addMember(t, actorDev, "developer")
	env.addMember(t, actorReviewer, "reviewer")
	env.addMember(t, actorQA, "qa")
	env.addMember(t, actorAdmin, "admin")
	env.addMember(t, actorBoth, "developer", "reviewer")
	return env
}

// as returns the MCP client of one team member (a personal token is minted
// through the real REST route, as a person would do in the web UI).
func (e *mcpActionEnv) as(t *testing.T, actor string) *mcpActor {
	t.Helper()
	token, ok := e.tokens[actor]
	if !ok {
		token, _ = e.issueToken(t, actor, `{"label":"s13 `+actor+`"}`)
		e.tokens[actor] = token
	}
	return &mcpActor{t: t, name: actor, session: e.mustConnect(t, token)}
}

type mcpActor struct {
	t       *testing.T
	name    string
	session *mcp.ClientSession
}

// call runs a tool and returns its text and whether it failed.
func (a *mcpActor) call(tool string, args map[string]any) (string, bool) {
	a.t.Helper()
	return callTool(a.t, a.session, tool, args)
}

// do runs an action that must succeed and returns the decoded JSON answer.
func (a *mcpActor) do(tool string, args map[string]any) map[string]any {
	a.t.Helper()
	if _, has := args[mcpIdempotencyKey]; !has {
		args = withKey(args, freshKey())
	}
	text, failed := a.call(tool, args)
	if failed {
		a.t.Fatalf("%s as %s failed: %s", tool, a.name, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		a.t.Fatalf("%s as %s: not an object: %s", tool, a.name, text)
	}
	return out
}

// refused runs an action that must fail and returns the error document.
func (a *mcpActor) refused(tool string, args map[string]any) mcpFailure {
	a.t.Helper()
	if _, has := args[mcpIdempotencyKey]; !has {
		args = withKey(args, freshKey())
	}
	text, failed := a.call(tool, args)
	if !failed {
		a.t.Fatalf("%s as %s must be refused, got: %s", tool, a.name, text)
	}
	var failure mcpFailure
	if err := json.Unmarshal([]byte(text), &failure); err != nil {
		a.t.Fatalf("%s as %s: error is not JSON: %s", tool, a.name, text)
	}
	return failure
}

type mcpFailure struct {
	Error  string `json:"error"`
	Code   string `json:"code"`
	Status int    `json:"status"`
}

func withKey(args map[string]any, key string) map[string]any {
	out := map[string]any{mcpIdempotencyKey: key}
	for name, value := range args {
		out[name] = value
	}
	return out
}

var keyCounter atomic.Int64

// freshKey is a request key never used before in this process.
func freshKey() string { return "s13-key-" + strconv.FormatInt(keyCounter.Add(1), 10) }

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// mcpTestRequest is a /mcp request authenticated as a team member with the
// cloud token the web server already accepts (roles come from the registry).
func mcpTestRequest(t *testing.T, env *mcpEnv, actor string) *http.Request {
	t.Helper()
	principal, err := cloudidentity.NewPrincipal(actor, []cloudidentity.Role{cloudidentity.RoleDeveloper})
	if err != nil {
		t.Fatal(err)
	}
	token, err := env.manager.Issue(principal, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := newLoopbackRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}
