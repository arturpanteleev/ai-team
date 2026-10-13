package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const functionPermissionsTemplate = `schema_version: 5
template: permissions
title: Permissions
forbid_self_approval: %FORBID%
stages:
  - id: spec
    title: Spec
    function: po
    result: md
  - id: build
    title: Build
    function: developer
    approver_function: reviewer
    result: md
    skippable: true
  - id: accept
    title: Accept
    function: bo
    result: approve
returns:
  - from: build
    to: spec
`

// stageActionRecorder accepts every connected stage action so authorization is
// the only reason a request can be refused.
type stageActionRecorder struct {
	*fakeRunController
	calls map[string]int
}

func (c *stageActionRecorder) record(action string) error {
	c.calls[action]++
	return nil
}
func (c *stageActionRecorder) RunStageAgent(context.Context, string, string, string, bool) error {
	return c.record("run-agent")
}
func (c *stageActionRecorder) ChangeStageExecutor(string, string, string, string) error {
	return c.record("executor")
}
func (c *stageActionRecorder) OverrideStageCheck(context.Context, string, string, string, string, string) error {
	return c.record("override")
}
func (c *stageActionRecorder) SkipStage(context.Context, string, string, string, string, string) error {
	return c.record("skip")
}
func (c *stageActionRecorder) ReturnStage(context.Context, string, string, string, string, string, string) error {
	return c.record("return")
}
func (c *stageActionRecorder) AnswerStageGrill(context.Context, string, string, string, string, string) error {
	return c.record("grill")
}
func (c *stageActionRecorder) SubmitStageGrillVersion(context.Context, string, string, string, string, string) error {
	return c.record("grill")
}
func (c *stageActionRecorder) CompleteStageGrill(context.Context, string, string, string, string) error {
	return c.record("grill")
}

type functionPermissionFixture struct {
	srv        *Server
	controller *stageActionRecorder
	target     string
	dbPath     string
}

func newFunctionPermissionFixture(t *testing.T, forbidSelfApproval bool, approvals []approval.PendingApproval, runs map[string]lifecycle.State) functionPermissionFixture {
	t.Helper()
	return newTemplateFixture(t, functionPermissionsTemplate, forbidSelfApproval, approvals, runs)
}

func newTemplateFixture(t *testing.T, template string, forbidSelfApproval bool, approvals []approval.PendingApproval, runs map[string]lifecycle.State) functionPermissionFixture {
	t.Helper()
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	forbid := "false"
	if forbidSelfApproval {
		forbid = "true"
	}
	yamlData := strings.Replace(template, "%FORBID%", forbid, 1)
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), []byte(yamlData), 0o644); err != nil {
		t.Fatal(err)
	}
	controller := &stageActionRecorder{fakeRunController: &fakeRunController{approvals: approvals}, calls: map[string]int{}}
	dbPath := filepath.Join(controlDir, "web.db")
	srv, err := NewServer(dbPath, "", filepath.Join(controlDir, "artifacts"),
		WithTargetDir(target), WithRunController(controller))
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
		if _, err := templateStore.PinCurrentForRun(runID, runID); err != nil {
			t.Fatal(err)
		}
		if err := srv.store.CreatePipelineRun(&store.PipelineRun{RunID: runID, Feature: "feature",
			Status: "waiting_for_approval", StartedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		state.RunID, state.Feature, state.TargetDir, state.Task = runID, "feature", target, "task"
		state.ConfigSHA256, state.WorkflowSHA256 = strings.Repeat("0", 64), strings.Repeat("1", 64)
		state.CreatedAt = time.Now().UTC()
		if err := stateStore.Create(state); err != nil {
			t.Fatal(err)
		}
	}
	return functionPermissionFixture{srv: srv, controller: controller, target: target, dbPath: dbPath}
}

// requestAs issues an authenticated request and replaces the session principal
// so each case exercises one exact set of assigned functions. A nil roles list
// keeps the local single-user principal.
func (f functionPermissionFixture) requestAs(t *testing.T, actorID string, roles []cloudidentity.Role, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := authorizedRequest(t, f.srv, method, target, body)
	if roles != nil {
		principal, err := cloudidentity.NewPrincipal(actorID, roles)
		if err != nil {
			t.Fatal(err)
		}
		setSessionPrincipalForTest(t, f.srv, request, principal)
	}
	response := httptest.NewRecorder()
	f.srv.router.ServeHTTP(response, request)
	return response
}

func buildInputApproval(id string, status approval.Status, created time.Time, decisions ...approval.Decision) approval.PendingApproval {
	return approval.PendingApproval{
		Kind: approval.KindInput, ID: id, RunID: "run", AttemptID: "attempt-1",
		FromStage: "build", ToStage: "build", Trigger: humanInputTrigger, Status: status,
		RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny,
		Actions: []string{"reject", "submit", "skip"}, Targets: map[string]string{"submit": "build", "skip": "build"},
		SubjectHash: testSubjectHash, CreatedAt: created, Decisions: decisions,
		Payload: []byte(`{"kind":"input","stage_id":"build","result":"md","output_name":"build","output_path":"build.md"}`),
	}
}

func buildConfirmApproval(created time.Time) approval.PendingApproval {
	return approval.PendingApproval{
		ID: "gate-1", RunID: "run", AttemptID: "attempt-2", FromStage: "build", ToStage: "accept",
		Trigger: "stage_completed", Status: approval.StatusPending, SubjectHash: testSubjectHash,
		RequiredRoles: []string{"reviewer"}, Quorum: approval.QuorumAny,
		Actions: []string{"approve", "reject", "return_to_spec"},
		Targets: map[string]string{"approve": "accept", "return_to_spec": "spec"}, CreatedAt: created,
	}
}

func TestStageActionsAreAuthorizedByStageFunction(t *testing.T) {
	now := time.Now().UTC()
	approvals := []approval.PendingApproval{
		buildInputApproval("input-take", approval.StatusPending, now),
		{
			Kind: approval.KindCheckDecision, ID: "check-1", RunID: "run", FromStage: "build", ToStage: "build",
			Trigger: approval.StageCheckTrigger, Status: approval.StatusPending, SubjectHash: testSubjectHash,
			RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny, Actions: []string{"override", "reject"},
			Targets: map[string]string{"override": "build"}, CreatedAt: now,
		},
		{
			Kind: approval.KindGrillQuestions, ID: "grill-1", RunID: "run", FromStage: "build", ToStage: "build",
			Trigger: approval.GrillTrigger, Status: approval.StatusPending, SubjectHash: testSubjectHash,
			RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny,
			Actions: []string{approval.GrillActionAnswer, approval.GrillActionNewVersion, "done"}, CreatedAt: now,
		},
		buildConfirmApproval(now.Add(time.Minute)),
	}
	runs := map[string]lifecycle.State{
		"run-input":    {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"},
		"run-check":    {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "check-1"},
		"run-grill":    {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "grill-1"},
		"run-skip":     {Phase: lifecycle.PhaseResumable, NextStage: "build"},
		"run-confirm":  {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"},
		"run-return":   {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"},
		"run-executor": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"},
	}

	type action struct {
		name, stageAction, path, body string
	}
	actions := []action{
		{"take", "take", "/api/runs/run-input/stages/build/take", ""},
		{"executor", "take", "/api/runs/run-executor/stages/build/executor", `{"executor":"human"}`},
		{"grill done", "submit", "/api/runs/run-grill/stages/build/grill/done", ""},
		{"override", "bypass", "/api/runs/run-check/stages/build/override", `{"reason":"flaky check"}`},
		{"skip", "bypass", "/api/runs/run-skip/stages/build/skip", `{"reason":"not needed"}`},
		{"confirm", "confirm", "/api/runs/run-confirm/stages/build/confirm", ""},
		{"return", "confirm", "/api/runs/run-return/stages/build/return", `{"to_stage":"spec","reason":"spec gap"}`},
	}
	members := []struct {
		name    string
		roles   []cloudidentity.Role
		allowed map[string]bool
	}{
		{"developer executes but cannot confirm", []cloudidentity.Role{cloudidentity.RoleDeveloper},
			map[string]bool{"take": true, "submit": true, "bypass": true}},
		{"reviewer confirms but cannot execute", []cloudidentity.Role{cloudidentity.RoleReviewer},
			map[string]bool{"confirm": true}},
		{"member with both functions", []cloudidentity.Role{cloudidentity.RoleDeveloper, cloudidentity.RoleReviewer},
			map[string]bool{"take": true, "submit": true, "bypass": true, "confirm": true}},
		{"foreign function is denied", []cloudidentity.Role{cloudidentity.RoleQA}, map[string]bool{}},
		{"product owner without stage function is denied", []cloudidentity.Role{cloudidentity.RoleProductOwner}, map[string]bool{}},
	}
	for _, member := range members {
		for _, current := range actions {
			t.Run(member.name+"/"+current.name, func(t *testing.T) {
				fixture := newFunctionPermissionFixture(t, true, append([]approval.PendingApproval(nil), approvals...), runs)
				response := fixture.requestAs(t, "member@example.test", member.roles, http.MethodPost, current.path, current.body)
				if member.allowed[current.stageAction] {
					if response.Code == http.StatusForbidden || response.Code == http.StatusUnauthorized || response.Code >= 400 {
						t.Fatalf("expected %s to be allowed: %d %s", current.name, response.Code, response.Body.String())
					}
					return
				}
				if response.Code != http.StatusForbidden {
					t.Fatalf("expected %s to be forbidden: %d %s", current.name, response.Code, response.Body.String())
				}
				if len(fixture.controller.calls) != 0 || fixture.controller.decisionCalls != 0 {
					t.Fatalf("forbidden %s reached the controller: calls=%v decisions=%d", current.name, fixture.controller.calls, fixture.controller.decisionCalls)
				}
			})
		}
	}
}

func TestStageResultSubmissionRequiresExecutorFunction(t *testing.T) {
	now := time.Now().UTC()
	runs := map[string]lifecycle.State{
		"run-input": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"},
	}
	for _, tc := range []struct {
		name    string
		roles   []cloudidentity.Role
		allowed bool
	}{
		{"executor function submits", []cloudidentity.Role{cloudidentity.RoleDeveloper}, true},
		{"approver function cannot submit", []cloudidentity.Role{cloudidentity.RoleReviewer}, false},
		{"foreign function cannot submit", []cloudidentity.Role{cloudidentity.RoleQA}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFunctionPermissionFixture(t, false, []approval.PendingApproval{buildInputApproval("input-take", approval.StatusPending, now)}, runs)
			response := fixture.requestAs(t, "member@example.test", tc.roles, http.MethodPost,
				"/api/runs/run-input/stages/build/results", `{"result":"md","content":"# Build\n"}`)
			if tc.allowed && (response.Code == http.StatusForbidden || response.Code == http.StatusUnauthorized) {
				t.Fatalf("executor submission must pass authorization: %d %s", response.Code, response.Body.String())
			}
			if !tc.allowed && response.Code != http.StatusForbidden {
				t.Fatalf("submission must be forbidden: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestForbidSelfApprovalRejectsStageAuthors(t *testing.T) {
	submitted := time.Now().UTC().Add(-time.Hour)
	confirmAt := submitted.Add(30 * time.Minute)
	both := []cloudidentity.Role{cloudidentity.RoleDeveloper, cloudidentity.RoleReviewer}
	runs := map[string]lifecycle.State{
		"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"},
	}
	cases := []struct {
		name      string
		forbid    bool
		submitter string
		owner     string
		actor     string
		roles     []cloudidentity.Role
		wantCode  int
	}{
		{"submitter cannot confirm own result", true, "author@example.test", "", "author@example.test", both, http.StatusForbidden},
		{"stage owner cannot confirm own result", true, "other@example.test", "author@example.test", "author@example.test", both, http.StatusForbidden},
		{"another reviewer confirms", true, "author@example.test", "author@example.test", "reviewer@example.test", []cloudidentity.Role{cloudidentity.RoleReviewer}, http.StatusAccepted},
		{"policy disabled allows self confirmation", false, "author@example.test", "author@example.test", "author@example.test", both, http.StatusAccepted},
		{"local single user is bound by the policy", true, "local-user", "", "local-user", nil, http.StatusForbidden},
		{"local single user confirms with the policy disabled", false, "local-user", "", "local-user", nil, http.StatusAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			approvals := []approval.PendingApproval{
				buildInputApproval("input-old", approval.StatusResolved, submitted.Add(-time.Hour),
					approval.Decision{ActorID: "old-author@example.test", ActorRole: "developer", Action: "submit"}),
				buildInputApproval("input-1", approval.StatusResolved, submitted,
					approval.Decision{ActorID: tc.submitter, ActorRole: "developer", Action: "submit"}),
				buildConfirmApproval(confirmAt),
			}
			fixture := newFunctionPermissionFixture(t, tc.forbid, approvals, runs)
			if tc.owner != "" {
				if _, _, err := fixture.srv.store.TakeStage("run-confirm", "build", "input-1", tc.owner, "developer", submitted); err != nil {
					t.Fatal(err)
				}
			}
			response := fixture.requestAs(t, tc.actor, tc.roles, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
			if response.Code != tc.wantCode {
				t.Fatalf("confirm code=%d want %d body=%s", response.Code, tc.wantCode, response.Body.String())
			}
			if tc.wantCode == http.StatusForbidden {
				if !strings.Contains(response.Body.String(), "self_approval_forbidden") || fixture.controller.decisionCalls != 0 {
					t.Fatalf("self approval must be refused before a decision: decisions=%d body=%s", fixture.controller.decisionCalls, response.Body.String())
				}
			} else if fixture.controller.decision.ActorID != tc.actor || fixture.controller.decision.ActorRole != "reviewer" {
				t.Fatalf("confirmation must be recorded under the approver role: %+v", fixture.controller.decision)
			}
		})
	}
}

func TestForbidSelfApprovalCoversGenericDecisionRoute(t *testing.T) {
	submitted := time.Now().UTC().Add(-time.Hour)
	approvals := []approval.PendingApproval{
		buildInputApproval("input-1", approval.StatusResolved, submitted,
			approval.Decision{ActorID: "local-user", ActorRole: "developer", Action: "submit"}),
		buildConfirmApproval(submitted.Add(time.Minute)),
	}
	runs := map[string]lifecycle.State{
		"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"},
	}
	fixture := newFunctionPermissionFixture(t, true, approvals, runs)
	response := fixture.requestAs(t, "local-user", nil, http.MethodPost, "/api/runs/run-confirm/approvals/gate-1/decisions",
		`{"action":"approve","subject_hash":"`+testSubjectHash+`"}`)
	if response.Code != http.StatusForbidden || fixture.controller.decisionCalls != 0 {
		t.Fatalf("generic decision route must enforce forbid_self_approval: code=%d decisions=%d body=%s",
			response.Code, fixture.controller.decisionCalls, response.Body.String())
	}
}

func TestTeamFunctionAssignmentAcceptsTemplateAliases(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(manager), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	adminCookie, adminCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)
	invite := teamRequest(srv, adminCookie, adminCSRF, http.MethodPost, "/api/team/invitations",
		`{"email":"dev@example.com","roles":["developer","deployer"]}`)
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite with function aliases: %d %s", invite.Code, invite.Body.String())
	}
	var invitation struct {
		Token string `json:"activation_token"`
	}
	if err := json.NewDecoder(invite.Body).Decode(&invitation); err != nil {
		t.Fatal(err)
	}
	activation := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+invitation.Token+`"}`))
	activation.Header.Set("Content-Type", "application/json")
	activated := httptest.NewRecorder()
	srv.router.ServeHTTP(activated, activation)
	if activated.Code != http.StatusOK {
		t.Fatalf("activate member: %d %s", activated.Code, activated.Body.String())
	}
	update := teamRequest(srv, adminCookie, adminCSRF, http.MethodPatch, "/api/team/members/dev@example.com/roles",
		`{"roles":["po","Developer","product_owner"]}`)
	if update.Code != http.StatusOK {
		t.Fatalf("assign function aliases: %d %s", update.Code, update.Body.String())
	}
	member, err := srv.store.TeamMember("dev@example.com")
	if err != nil || member == nil || strings.Join(member.Roles, ",") != "developer,product_owner" {
		t.Fatalf("functions must be stored as canonical roles: member=%+v err=%v", member, err)
	}
	rejected := teamRequest(srv, adminCookie, adminCSRF, http.MethodPatch, "/api/team/members/dev@example.com/roles", `{"roles":["operator"]}`)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("unknown function must be rejected: %d %s", rejected.Code, rejected.Body.String())
	}
}

const agentStageTemplate = `schema_version: 5
template: agent-permissions
title: Agent permissions
forbid_self_approval: %FORBID%
stages:
  - id: spec
    title: Spec
    function: po
    result: md
  - id: build
    title: Build
    function: developer
    executor: agent
    agent: coder
    result: md
    check:
      kind: agent
      agent: reviewer
      mode: grill
  - id: accept
    title: Accept
    function: bo
    result: approve
`

func grillApproval(id string, created time.Time, decisions ...approval.Decision) approval.PendingApproval {
	return approval.PendingApproval{
		Kind: approval.KindGrillQuestions, ID: id, RunID: "run", AttemptID: "attempt-grill",
		FromStage: "build", ToStage: "build", Trigger: approval.GrillTrigger, Status: approval.StatusResolved,
		SubjectHash: testSubjectHash, RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny,
		Actions:   []string{approval.GrillActionAnswer, approval.GrillActionNewVersion, "done"},
		CreatedAt: created, Decisions: decisions,
	}
}

func agentConfirmApproval(created time.Time) approval.PendingApproval {
	value := buildConfirmApproval(created)
	value.RequiredRoles = []string{"developer"}
	return value
}

func writeAttemptManifest(t *testing.T, fixture functionPermissionFixture, runID, attemptID, body string) {
	t.Helper()
	dir := filepath.Join(fixture.target, ".ai-team", "runs", runID, "attempts", attemptID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestForbidSelfApprovalCountsEveryStageContribution covers authorship beyond
// the human input submitter: grill decisions, result edits, agent output
// edits and agent launches in the confirmed visit.
func TestForbidSelfApprovalCountsEveryStageContribution(t *testing.T) {
	developer := []cloudidentity.Role{cloudidentity.RoleDeveloper}
	both := []cloudidentity.Role{cloudidentity.RoleDeveloper, cloudidentity.RoleReviewer}
	base := time.Now().UTC().Add(-2 * time.Hour)
	confirmRun := map[string]lifecycle.State{
		"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"},
	}
	const outputPath = "attempts/attempt-2/artifacts/build.md"
	manifest := `{"run_id":"run-confirm","attempt_id":"attempt-2","outputs":[{"name":"build","evidence_path":"` + outputPath + `"}]}`

	t.Run("agent stage grill new_version author cannot confirm", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{
			grillApproval("grill-1", base, approval.Decision{ActorID: "b@example.test", ActorRole: "developer", Action: approval.GrillActionNewVersion}),
			agentConfirmApproval(base.Add(time.Hour)),
		}, confirmRun)
		response := fixture.requestAs(t, "b@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
		if response.Code != http.StatusForbidden || fixture.controller.decisionCalls != 0 {
			t.Fatalf("grill author confirmed own version: %d %s", response.Code, response.Body.String())
		}
		other := fixture.requestAs(t, "c@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
		if other.Code != http.StatusAccepted {
			t.Fatalf("non-author must confirm: %d %s", other.Code, other.Body.String())
		}
	})

	t.Run("grill answer and done authors cannot confirm", func(t *testing.T) {
		for _, action := range []string{approval.GrillActionAnswer, "done"} {
			fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{
				grillApproval("grill-1", base, approval.Decision{ActorID: "b@example.test", ActorRole: "developer", Action: action}),
				agentConfirmApproval(base.Add(time.Hour)),
			}, confirmRun)
			response := fixture.requestAs(t, "b@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
			if response.Code != http.StatusForbidden {
				t.Fatalf("grill %s author confirmed: %d %s", action, response.Code, response.Body.String())
			}
		}
	})

	t.Run("editor of a human result cannot confirm", func(t *testing.T) {
		fixture := newFunctionPermissionFixture(t, true, []approval.PendingApproval{
			buildInputApproval("input-1", approval.StatusResolved, base,
				approval.Decision{ActorID: "a@example.test", ActorRole: "developer", Action: "submit"}),
			buildConfirmApproval(base.Add(time.Hour)),
		}, confirmRun)
		if _, err := fixture.srv.humanArtifacts.Append("run-confirm", "stages/build/result.md", "", "", "# edited by B\n", "grill edit", "b@example.test"); err != nil {
			t.Fatal(err)
		}
		response := fixture.requestAs(t, "b@example.test", both, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
		if response.Code != http.StatusForbidden {
			t.Fatalf("result editor confirmed own edit: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("editor of agent output cannot confirm", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{agentConfirmApproval(base.Add(time.Hour))}, confirmRun)
		writeAttemptManifest(t, fixture, "run-confirm", "attempt-2", manifest)
		if _, err := fixture.srv.humanArtifacts.Append("run-confirm", outputPath, "", "", "agent output edited by B\n", "edit", "b@example.test"); err != nil {
			t.Fatal(err)
		}
		response := fixture.requestAs(t, "b@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
		if response.Code != http.StatusForbidden {
			t.Fatalf("agent output editor confirmed: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("agent launcher cannot confirm", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{agentConfirmApproval(base.Add(time.Hour))},
			map[string]lifecycle.State{"run-confirm": {Phase: lifecycle.PhaseResumable, NextStage: "build"}})
		launch := fixture.requestAs(t, "b@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/run-agent", `{"action":"run"}`)
		if launch.Code != http.StatusAccepted || fixture.controller.calls["run-agent"] != 1 {
			t.Fatalf("run-agent: %d %s", launch.Code, launch.Body.String())
		}
		stateStore, err := lifecycle.NewStore(fixture.target)
		if err != nil {
			t.Fatal(err)
		}
		previous, err := stateStore.Load("run-confirm")
		if err != nil {
			t.Fatal(err)
		}
		running := previous
		running.Phase = lifecycle.PhaseRunning
		if err := stateStore.Save(previous, running); err != nil {
			t.Fatal(err)
		}
		next := running
		next.Phase, next.PendingApprovalID = lifecycle.PhaseWaiting, "gate-1"
		if err := stateStore.Save(running, next); err != nil {
			t.Fatal(err)
		}
		fixture.controller.approvals[0].CreatedAt = time.Now().UTC()
		response := fixture.requestAs(t, "b@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
		if response.Code != http.StatusForbidden {
			t.Fatalf("agent launcher confirmed: %d %s", response.Code, response.Body.String())
		}
		other := fixture.requestAs(t, "c@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
		if other.Code != http.StatusAccepted {
			t.Fatalf("non-launcher must confirm: %d %s", other.Code, other.Body.String())
		}
	})

	t.Run("author of a previous visit may confirm the new visit", func(t *testing.T) {
		previousGate := buildConfirmApproval(base.Add(10 * time.Minute))
		previousGate.ID, previousGate.Status, previousGate.ResolvedAt = "gate-0", approval.StatusResolved, base.Add(20*time.Minute)
		fixture := newFunctionPermissionFixture(t, true, []approval.PendingApproval{
			buildInputApproval("input-0", approval.StatusResolved, base,
				approval.Decision{ActorID: "b@example.test", ActorRole: "developer", Action: "submit"}),
			previousGate,
			buildInputApproval("input-1", approval.StatusResolved, base.Add(30*time.Minute),
				approval.Decision{ActorID: "a@example.test", ActorRole: "developer", Action: "submit"}),
			buildConfirmApproval(base.Add(time.Hour)),
		}, confirmRun)
		response := fixture.requestAs(t, "b@example.test", both, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
		if response.Code != http.StatusAccepted {
			t.Fatalf("previous-visit author must confirm the new visit: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("unreadable attempt evidence fails closed", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{agentConfirmApproval(base.Add(time.Hour))}, confirmRun)
		writeAttemptManifest(t, fixture, "run-confirm", "attempt-2", "{not json")
		response := fixture.requestAs(t, "c@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
		if response.Code != http.StatusForbidden || fixture.controller.decisionCalls != 0 {
			t.Fatalf("unverifiable authorship must be refused: %d %s", response.Code, response.Body.String())
		}
	})
}

// moveToConfirmation advances a run checkpoint to the pending confirmation
// gate-1 through the lifecycle transitions the engine would take.
func moveToConfirmation(t *testing.T, fixture functionPermissionFixture, runID string, mutate func(*lifecycle.State)) {
	t.Helper()
	stateStore, err := lifecycle.NewStore(fixture.target)
	if err != nil {
		t.Fatal(err)
	}
	current, err := stateStore.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Phase != lifecycle.PhaseRunning {
		running := current
		running.Phase, running.PendingApprovalID = lifecycle.PhaseRunning, ""
		if err := stateStore.Save(current, running); err != nil {
			t.Fatal(err)
		}
		current = running
	}
	next := current
	next.Phase, next.PendingApprovalID = lifecycle.PhaseWaiting, "gate-1"
	if mutate != nil {
		mutate(&next)
	}
	if err := stateStore.Save(current, next); err != nil {
		t.Fatal(err)
	}
}

func TestForbidSelfApprovalCountsExecutorSwitchAndResume(t *testing.T) {
	developer := []cloudidentity.Role{cloudidentity.RoleDeveloper}
	confirmPath := "/api/runs/run-confirm/stages/build/confirm"

	t.Run("executor switch through the task API", func(t *testing.T) {
		now := time.Now().UTC()
		fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{
			buildInputApproval("input-1", approval.StatusPending, now),
			agentConfirmApproval(now.Add(time.Hour)),
		}, map[string]lifecycle.State{"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-1"}})
		switched := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/executor", `{"executor":"agent"}`)
		if switched.Code != http.StatusOK || fixture.controller.calls["executor"] != 1 {
			t.Fatalf("executor switch: %d %s", switched.Code, switched.Body.String())
		}
		moveToConfirmation(t, fixture, "run-confirm", nil)
		if response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, confirmPath, ""); response.Code != http.StatusForbidden {
			t.Fatalf("executor switcher confirmed: %d %s", response.Code, response.Body.String())
		}
		if response := fixture.requestAs(t, "c@example.test", developer, http.MethodPost, confirmPath, ""); response.Code != http.StatusAccepted {
			t.Fatalf("other member must confirm: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("executor override recorded only in the lifecycle checkpoint", func(t *testing.T) {
		now := time.Now().UTC()
		fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{agentConfirmApproval(now)},
			map[string]lifecycle.State{"run-confirm": {Phase: lifecycle.PhaseResumable, NextStage: "build"}})
		moveToConfirmation(t, fixture, "run-confirm", func(state *lifecycle.State) {
			state.ExecutorOverrides = map[string]lifecycle.ExecutorOverride{"build": {
				Executor: "agent", PreviousExecutor: "human", ActorID: "a@example.test",
				ApprovalID: "input-1", VisitID: "input-1", ChangedAt: now.Add(-time.Minute),
			}}
		})
		if response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, confirmPath, ""); response.Code != http.StatusForbidden {
			t.Fatalf("checkpoint executor switcher confirmed: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("resume of an agent stage", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{agentConfirmApproval(time.Now().UTC())},
			map[string]lifecycle.State{"run-confirm": {Phase: lifecycle.PhaseResumable, NextStage: "build"}})
		resumed := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, "/api/runs/run-confirm/resume", "")
		if resumed.Code != http.StatusAccepted || fixture.controller.resumeCalls != 1 {
			t.Fatalf("resume: %d %s", resumed.Code, resumed.Body.String())
		}
		moveToConfirmation(t, fixture, "run-confirm", nil)
		fixture.controller.approvals[0].CreatedAt = time.Now().UTC()
		if response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, confirmPath, ""); response.Code != http.StatusForbidden {
			t.Fatalf("resuming member confirmed the agent result: %d %s", response.Code, response.Body.String())
		}
		if response := fixture.requestAs(t, "c@example.test", developer, http.MethodPost, confirmPath, ""); response.Code != http.StatusAccepted {
			t.Fatalf("other member must confirm: %d %s", response.Code, response.Body.String())
		}
	})
}

// sessionRequestAs prepares an authenticated request without serving it, so a
// test can break the store between session setup and the action.
func (f functionPermissionFixture) sessionRequestAs(t *testing.T, actorID string, roles []cloudidentity.Role, method, target, body string) *http.Request {
	t.Helper()
	request := authorizedRequest(t, f.srv, method, target, body)
	principal, err := cloudidentity.NewPrincipal(actorID, roles)
	if err != nil {
		t.Fatal(err)
	}
	setSessionPrincipalForTest(t, f.srv, request, principal)
	return request
}

func TestAuthorshipRecordingFailuresFailClosed(t *testing.T) {
	developer := []cloudidentity.Role{cloudidentity.RoleDeveloper}

	t.Run("unrecorded run-agent launch does not dispatch", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, nil,
			map[string]lifecycle.State{"run-confirm": {Phase: lifecycle.PhaseResumable, NextStage: "build"}})
		request := fixture.sessionRequestAs(t, "a@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/run-agent", `{"action":"run"}`)
		breakTablesForTest(t, fixture.dbPath, "events")
		response := httptest.NewRecorder()
		fixture.srv.router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || fixture.controller.calls["run-agent"] != 0 {
			t.Fatalf("run-agent without a recorded actor must not dispatch: %d calls=%v %s", response.Code, fixture.controller.calls, response.Body.String())
		}
	})

	t.Run("unrecorded resume launch does not resume", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, nil,
			map[string]lifecycle.State{"run-confirm": {Phase: lifecycle.PhaseResumable, NextStage: "build"}})
		request := fixture.sessionRequestAs(t, "a@example.test", developer, http.MethodPost, "/api/runs/run-confirm/resume", "")
		breakTablesForTest(t, fixture.dbPath, "events")
		response := httptest.NewRecorder()
		fixture.srv.router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || fixture.controller.resumeCalls != 0 {
			t.Fatalf("resume without a recorded actor must not run: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("unreadable run events refuse confirmation", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{agentConfirmApproval(time.Now().UTC())},
			map[string]lifecycle.State{"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"}})
		request := fixture.sessionRequestAs(t, "c@example.test", developer, http.MethodPost, "/api/runs/run-confirm/stages/build/confirm", "")
		breakTablesForTest(t, fixture.dbPath, "events")
		response := httptest.NewRecorder()
		fixture.srv.router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || fixture.controller.decisionCalls != 0 {
			t.Fatalf("unverifiable authorship must be refused: %d %s", response.Code, response.Body.String())
		}
	})
}

// stageAgentRequests counts recorded agent launches of stageID by actorID.
func stageAgentRequests(t *testing.T, fixture functionPermissionFixture, runID, stageID, actorID string) int {
	t.Helper()
	events, err := fixture.srv.allRunEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == stageAgentRequestedEvent && event.Stage == stageID && event.Actor == actorID {
			count++
		}
	}
	return count
}

func resolvedGate(id, from string, targets map[string]string, action string, created time.Time) approval.PendingApproval {
	actions := make([]string, 0, len(targets))
	for name := range targets {
		actions = append(actions, name)
	}
	return approval.PendingApproval{
		ID: id, RunID: "run", AttemptID: "attempt-0", FromStage: from, ToStage: targets[action],
		Trigger: "stage_completed", Status: approval.StatusResolved, ResolvedAction: action, SubjectHash: testSubjectHash,
		RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny, Actions: actions, Targets: targets,
		CreatedAt: created, ResolvedAt: created.Add(time.Minute),
	}
}

func TestResumeRecordsAgentLaunchInEveryResumablePhase(t *testing.T) {
	developer := []cloudidentity.Role{cloudidentity.RoleDeveloper}
	const resumePath = "/api/runs/run-r/resume"
	base := time.Now().UTC().Add(-time.Hour)

	t.Run("interrupted running checkpoint of an agent stage", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, nil,
			map[string]lifecycle.State{"run-r": {Phase: lifecycle.PhaseRunning, NextStage: "build"}})
		if response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, resumePath, ""); response.Code != http.StatusAccepted {
			t.Fatalf("resume: %d %s", response.Code, response.Body.String())
		}
		if got := stageAgentRequests(t, fixture, "run-r", "build", "a@example.test"); got != 1 {
			t.Fatalf("running resume must record the launcher, got %d", got)
		}
	})

	t.Run("waiting checkpoint resolved into the agent stage", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true,
			[]approval.PendingApproval{resolvedGate("gate-0", "spec", map[string]string{"approve": "build"}, "approve", base)},
			map[string]lifecycle.State{"run-r": {Phase: lifecycle.PhaseWaiting, NextStage: "spec", PendingApprovalID: "gate-0"}})
		if response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, resumePath, ""); response.Code != http.StatusAccepted {
			t.Fatalf("resume: %d %s", response.Code, response.Body.String())
		}
		if got := stageAgentRequests(t, fixture, "run-r", "build", "a@example.test"); got != 1 {
			t.Fatalf("resolved target agent stage must record the launcher, got %d", got)
		}
	})

	t.Run("waiting checkpoint at the agent stage", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true,
			[]approval.PendingApproval{resolvedGate("gate-0", "build", map[string]string{"approve": "accept"}, "approve", base)},
			map[string]lifecycle.State{"run-r": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-0"}})
		if response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, resumePath, ""); response.Code != http.StatusAccepted {
			t.Fatalf("resume: %d %s", response.Code, response.Body.String())
		}
		if got := stageAgentRequests(t, fixture, "run-r", "build", "a@example.test"); got != 1 {
			t.Fatalf("waiting NextStage agent stage must record the launcher, got %d", got)
		}
	})

	t.Run("unresolved waiting approval records nothing", func(t *testing.T) {
		gate := resolvedGate("gate-0", "spec", map[string]string{"approve": "build"}, "", base)
		gate.Status, gate.ResolvedAt = approval.StatusPending, time.Time{}
		fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{gate},
			map[string]lifecycle.State{"run-r": {Phase: lifecycle.PhaseWaiting, NextStage: "spec", PendingApprovalID: "gate-0"}})
		response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, resumePath, "")
		if response.Code != http.StatusConflict || fixture.controller.resumeCalls != 0 {
			t.Fatalf("unresolved approval must be a conflict without Resume: %d calls=%d %s",
				response.Code, fixture.controller.resumeCalls, response.Body.String())
		}
		if got := stageAgentRequests(t, fixture, "run-r", "build", "a@example.test"); got != 0 {
			t.Fatalf("pending approval does not launch the agent, got %d records", got)
		}
	})
}

func TestResumeLaunchRecordingFailsClosed(t *testing.T) {
	developer := []cloudidentity.Role{cloudidentity.RoleDeveloper}
	const resumePath = "/api/runs/run-r/resume"

	t.Run("unreadable lifecycle checkpoint", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, nil,
			map[string]lifecycle.State{"run-r": {Phase: lifecycle.PhaseResumable, NextStage: "build"}})
		statePath := filepath.Join(fixture.target, ".ai-team", "state", "runs", "run-r.json")
		if err := os.WriteFile(statePath, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, resumePath, "")
		if response.Code != http.StatusServiceUnavailable || fixture.controller.resumeCalls != 0 {
			t.Fatalf("unreadable lifecycle must refuse resume: %d calls=%d %s", response.Code, fixture.controller.resumeCalls, response.Body.String())
		}
	})

	t.Run("unreadable approvals of a waiting run", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, nil,
			map[string]lifecycle.State{"run-r": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-0"}})
		fixture.controller.approvalsErr = os.ErrPermission
		response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, resumePath, "")
		if response.Code != http.StatusServiceUnavailable || fixture.controller.resumeCalls != 0 {
			t.Fatalf("unreadable approvals must refuse resume: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("waiting approval missing", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, nil,
			map[string]lifecycle.State{"run-r": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-0"}})
		response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, resumePath, "")
		if response.Code != http.StatusServiceUnavailable || fixture.controller.resumeCalls != 0 {
			t.Fatalf("missing approval must refuse resume: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("corrupt pinned template", func(t *testing.T) {
		fixture := newTemplateFixture(t, agentStageTemplate, true, nil,
			map[string]lifecycle.State{"run-r": {Phase: lifecycle.PhaseResumable, NextStage: "build"}})
		corruptPinnedTemplate(t, fixture, "run-r")
		response := fixture.requestAs(t, "a@example.test", developer, http.MethodPost, resumePath, "")
		if response.Code != http.StatusServiceUnavailable || fixture.controller.resumeCalls != 0 {
			t.Fatalf("corrupt template must refuse resume: %d %s", response.Code, response.Body.String())
		}
	})
}

func TestTaskExecutorSwitchFailsClosedWithoutEvent(t *testing.T) {
	developer := []cloudidentity.Role{cloudidentity.RoleDeveloper}
	fixture := newTemplateFixture(t, agentStageTemplate, true,
		[]approval.PendingApproval{buildInputApproval("input-1", approval.StatusPending, time.Now().UTC())},
		map[string]lifecycle.State{"run-x": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-1"}})
	request := fixture.sessionRequestAs(t, "a@example.test", developer, http.MethodPost, "/api/runs/run-x/stages/build/executor", `{"executor":"agent"}`)
	breakTablesForTest(t, fixture.dbPath, "events")
	response := httptest.NewRecorder()
	fixture.srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || fixture.controller.calls["executor"] != 0 {
		t.Fatalf("unrecorded executor switch must not reach the controller: %d calls=%v %s", response.Code, fixture.controller.calls, response.Body.String())
	}
}

// TestSelfApprovalVisitBoundary pins the visit start to the resolution time of
// the previous confirmation: an authorship event at that instant belongs to
// the previous visit, one a millisecond later to the confirmed visit.
func TestSelfApprovalVisitBoundary(t *testing.T) {
	developer := []cloudidentity.Role{cloudidentity.RoleDeveloper}
	confirmPath := "/api/runs/run-confirm/stages/build/confirm"
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
	for _, test := range []struct {
		name   string
		offset time.Duration
		want   int
	}{
		{"event at the previous resolution belongs to the previous visit", 0, http.StatusAccepted},
		{"event before the previous resolution belongs to the previous visit", -time.Millisecond, http.StatusAccepted},
		{"event after the previous resolution belongs to the confirmed visit", time.Millisecond, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := agentConfirmApproval(base)
			previous.ID, previous.Status, previous.ResolvedAction = "gate-0", approval.StatusResolved, "approve"
			previous.ResolvedAt = base.Add(10 * time.Minute)
			fixture := newTemplateFixture(t, agentStageTemplate, true,
				[]approval.PendingApproval{previous, agentConfirmApproval(base.Add(time.Hour))},
				map[string]lifecycle.State{"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"}})
			if err := fixture.srv.appendRunEventRequired("run-confirm", stageAgentRequestedEvent, previous.ResolvedAt.Add(test.offset),
				map[string]any{"stage_id": "build", "actor_id": "b@example.test"}); err != nil {
				t.Fatal(err)
			}
			response := fixture.requestAs(t, "b@example.test", developer, http.MethodPost, confirmPath, "")
			if response.Code != test.want {
				t.Fatalf("status %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestUnreadableRunEventsRefuseOtherwiseValidConfirmation(t *testing.T) {
	developer := []cloudidentity.Role{cloudidentity.RoleDeveloper}
	confirmPath := "/api/runs/run-confirm/stages/build/confirm"
	setup := func() functionPermissionFixture {
		return newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{agentConfirmApproval(time.Now().UTC())},
			map[string]lifecycle.State{"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"}})
	}
	// Control: the same non-author confirms when events are readable.
	if response := setup().requestAs(t, "c@example.test", developer, http.MethodPost, confirmPath, ""); response.Code != http.StatusAccepted {
		t.Fatalf("control confirmation: %d %s", response.Code, response.Body.String())
	}
	fixture := setup()
	request := fixture.sessionRequestAs(t, "c@example.test", developer, http.MethodPost, confirmPath, "")
	breakTablesForTest(t, fixture.dbPath, "events")
	response := httptest.NewRecorder()
	fixture.srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || fixture.controller.decisionCalls != 0 ||
		!strings.Contains(response.Body.String(), errSelfApprovalUnverifiable.Error()) {
		t.Fatalf("unreadable events must refuse as unverifiable: %d %s", response.Code, response.Body.String())
	}
}

func TestGrillEditRequiresStageExecutorFunction(t *testing.T) {
	reviewer := []cloudidentity.Role{cloudidentity.RoleReviewer}
	both := []cloudidentity.Role{cloudidentity.RoleDeveloper, cloudidentity.RoleReviewer}
	const resultPath = "stages/build/result.md"
	payload, err := json.Marshal(approval.GrillQuestionsPayload{
		Kind: string(approval.KindGrillQuestions), Markdown: "question", Round: 1,
		ArtifactRevisions: map[string]string{resultPath: "rev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	grill := grillApproval("grill-1", time.Now().UTC())
	grill.Status, grill.Payload = approval.StatusPending, payload
	fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{grill},
		map[string]lifecycle.State{"run-g": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "grill-1"}})
	latest, err := fixture.srv.humanArtifacts.Append("run-g", resultPath, "", "", "# v1\n", "submit", "a@example.test")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"artifact_path": resultPath, "base_revision": latest.ID,
		"base_sha256": latest.SHA256, "content": "# v2\n"})
	if err != nil {
		t.Fatal(err)
	}
	if response := fixture.requestAs(t, "r@example.test", reviewer, http.MethodPost, "/api/runs/run-g/artifact-revisions", string(body)); response.Code != http.StatusForbidden {
		t.Fatalf("artifact-edit without the stage function must be refused: %d %s", response.Code, response.Body.String())
	}
	if response := fixture.requestAs(t, "d@example.test", both, http.MethodPost, "/api/runs/run-g/artifact-revisions", string(body)); response.Code != http.StatusCreated {
		t.Fatalf("stage executor with artifact-edit must edit: %d %s", response.Code, response.Body.String())
	}
}

func TestGrillAttemptOutputEditRequiresStageExecutorFunction(t *testing.T) {
	reviewer := []cloudidentity.Role{cloudidentity.RoleReviewer}
	both := []cloudidentity.Role{cloudidentity.RoleDeveloper, cloudidentity.RoleReviewer}
	const outputPath = "attempts/attempt-grill/artifacts/build.md"
	payload, err := json.Marshal(approval.GrillQuestionsPayload{
		Kind: string(approval.KindGrillQuestions), Markdown: "question", Round: 1,
		ArtifactContentSHA256: map[string]string{outputPath: strings.Repeat("a", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	grill := grillApproval("grill-1", time.Now().UTC())
	grill.Status, grill.Payload = approval.StatusPending, payload
	fixture := newTemplateFixture(t, agentStageTemplate, true, []approval.PendingApproval{grill},
		map[string]lifecycle.State{"run-g": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "grill-1"}})
	body, err := json.Marshal(map[string]string{"artifact_path": outputPath, "base_sha256": strings.Repeat("a", 64), "content": "# v2\n"})
	if err != nil {
		t.Fatal(err)
	}
	if response := fixture.requestAs(t, "r@example.test", reviewer, http.MethodPost, "/api/runs/run-g/artifact-revisions", string(body)); response.Code != http.StatusForbidden {
		t.Fatalf("agent output edit without the stage function must be refused: %d %s", response.Code, response.Body.String())
	}
	// Control: the executor passes authorization (and then fails on the
	// missing source attempt, not on permissions).
	if response := fixture.requestAs(t, "d@example.test", both, http.MethodPost, "/api/runs/run-g/artifact-revisions", string(body)); response.Code == http.StatusForbidden {
		t.Fatalf("stage executor must pass authorization: %d %s", response.Code, response.Body.String())
	}
}

// corruptPinnedTemplate replaces the pinned template bytes so the pin no
// longer verifies.
func corruptPinnedTemplate(t *testing.T, fixture functionPermissionFixture, runID string) {
	t.Helper()
	path := filepath.Join(fixture.target, ".ai-team", "task-template-pins", runID+".yaml")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCloudDecisionRouteRefusesUnassignedRoleBeforeStore covers the generic
// decision route in cloud mode: the stage executor who claims its own role
// for a confirmation assigned to approver_function gets 403 before the
// approval store sees the decision.
func TestCloudDecisionRouteRefusesUnassignedRoleBeforeStore(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("d", 32)))
	if err != nil {
		t.Fatal(err)
	}
	controller := &fakeRunController{approvals: []approval.PendingApproval{buildConfirmApproval(time.Now().UTC())}}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(manager), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	decide := func(actor, role string, roles ...cloudidentity.Role) *httptest.ResponseRecorder {
		names := make([]string, len(roles))
		for i, value := range roles {
			names[i] = string(value)
		}
		hash := store.SessionDigest("activation-" + actor)
		now := time.Now().UTC()
		if err := srv.store.InviteTeamMember(actor, actor, names, hash, now.Add(time.Hour), now, "admin@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := srv.store.ActivateTeamInvitation(hash, now, nil); err != nil {
			t.Fatal(err)
		}
		cookie, csrf := cloudSessionForTest(t, srv, manager, actor, roles...)
		return teamRequest(srv, cookie, csrf, http.MethodPost, "/api/runs/run/approvals/gate-1/decisions",
			`{"action":"approve","actor_role":"`+role+`","subject_hash":"`+testSubjectHash+`"}`)
	}
	if response := decide("dev@example.test", "developer", cloudidentity.RoleDeveloper); response.Code != http.StatusForbidden || controller.decisionCalls != 0 {
		t.Fatalf("executor role on an approver confirmation: %d calls=%d %s", response.Code, controller.decisionCalls, response.Body.String())
	}
	if response := decide("dev2@example.test", "reviewer", cloudidentity.RoleDeveloper); response.Code != http.StatusForbidden || controller.decisionCalls != 0 {
		t.Fatalf("claimed role the member lacks: %d %s", response.Code, response.Body.String())
	}
	if response := decide("rev@example.test", "reviewer", cloudidentity.RoleReviewer); response.Code != http.StatusOK || controller.decisionCalls != 1 {
		t.Fatalf("assigned approver must decide: %d %s", response.Code, response.Body.String())
	}
}
