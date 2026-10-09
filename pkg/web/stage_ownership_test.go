package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func TestTakeStageRequiresCurrentHumanInputAndProjectsTakeover(t *testing.T) {
	target := t.TempDir()
	controller := &fakeRunController{approvals: []approval.PendingApproval{{
		Kind: approval.KindInput, ID: "input-1", RunID: "run-owner", AttemptID: "attempt-1",
		FromStage: "product_spec", ToStage: "product_spec", Trigger: humanInputTrigger,
		Status: approval.StatusPending, RequiredRoles: []string{"po"},
		Payload: []byte(`{"kind":"input","stage_id":"product_spec","result":"md","output_name":"proposal","output_path":"proposal.md"}`),
	}}}
	srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"),
		WithRunController(controller), WithTargetDir(target))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	run := &store.PipelineRun{RunID: "run-owner", Feature: "feature", Status: "waiting_for_approval", StartedAt: time.Now().UTC()}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if err := stateStore.Create(lifecycle.State{
		RunID: run.RunID, Feature: run.Feature, TargetDir: target, Task: "write a proposal",
		Phase: lifecycle.PhaseWaiting, NextStage: "product_spec", PendingApprovalID: "input-1",
		ConfigSHA256: strings.Repeat("a", 64), WorkflowSHA256: strings.Repeat("b", 64), CreatedAt: started,
	}); err != nil {
		t.Fatal(err)
	}

	first := takeStageAs(t, srv, run.RunID, "product_spec", "alice@example.test")
	if first.Code != http.StatusOK {
		t.Fatalf("first take: %d %s", first.Code, first.Body.String())
	}
	var firstResult struct {
		Owner   store.StageOwner `json:"owner"`
		Changed bool             `json:"changed"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil {
		t.Fatal(err)
	}
	if !firstResult.Changed || firstResult.Owner.ApprovalID != "input-1" || firstResult.Owner.ActorID != "alice@example.test" || firstResult.Owner.ActorRole != "po" || firstResult.Owner.TakenAt.IsZero() {
		t.Fatalf("first owner response=%+v", firstResult)
	}

	// The same user can safely retry after losing the response; the claim's
	// original timestamp is retained.
	retried := takeStageAs(t, srv, run.RunID, "product_spec", "alice@example.test")
	if retried.Code != http.StatusOK {
		t.Fatalf("idempotent retry: %d %s", retried.Code, retried.Body.String())
	}
	var retryResult struct {
		Owner   store.StageOwner `json:"owner"`
		Changed bool             `json:"changed"`
	}
	if err := json.Unmarshal(retried.Body.Bytes(), &retryResult); err != nil {
		t.Fatal(err)
	}
	if retryResult.Changed || !retryResult.Owner.TakenAt.Equal(firstResult.Owner.TakenAt) {
		t.Fatalf("same-owner retry changed claim: first=%+v retry=%+v", firstResult, retryResult)
	}

	second := takeStageAs(t, srv, run.RunID, "product_spec", "bob@example.test")
	if second.Code != http.StatusOK {
		t.Fatalf("takeover: %d %s", second.Code, second.Body.String())
	}
	var takeover struct {
		Owner   store.StageOwner `json:"owner"`
		Changed bool             `json:"changed"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &takeover); err != nil {
		t.Fatal(err)
	}
	if !takeover.Changed || takeover.Owner.ActorID != "bob@example.test" {
		t.Fatalf("takeover response=%+v", takeover)
	}

	var events []store.Event
	events, err = srv.store.GetEventsAfter(0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("takeover events=%+v err=%v", events, err)
	}
	var eventData map[string]string
	if err := json.Unmarshal([]byte(events[1].DataJSON), &eventData); err != nil {
		t.Fatal(err)
	}
	if events[1].Type != "stage_taken" || eventData["approval_id"] != "input-1" || eventData["actor_name"] != "bob@example.test" || eventData["previous_actor_name"] != "alice@example.test" {
		t.Fatalf("takeover must identify both participants: type=%q data=%+v", events[1].Type, eventData)
	}

	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, authorizedRequest(t, srv, http.MethodGet, fmt.Sprintf("/api/pipelines/%d", run.ID), ""))
	if response.Code != http.StatusOK {
		t.Fatalf("pipeline projection: %d %s", response.Code, response.Body.String())
	}
	var projection struct {
		StageOwners map[string]store.StageOwner `json:"stage_owners"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if owner := projection.StageOwners["product_spec"]; owner.ActorID != "bob@example.test" || !owner.TakenAt.Equal(takeover.Owner.TakenAt) {
		t.Fatalf("GET pipeline did not project current owner: %+v", projection.StageOwners)
	}
}

func TestTakeStageStartsFreshOwnershipForNewApprovalVisit(t *testing.T) {
	target := t.TempDir()
	controller := &fakeRunController{approvals: []approval.PendingApproval{
		stageInputApproval("input-a", approval.StatusPending),
		stageInputApproval("input-b", approval.StatusPending),
	}}
	srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"), WithRunController(controller), WithTargetDir(target))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	run := &store.PipelineRun{RunID: "run-owner-visits", Feature: "feature", Status: "waiting_for_approval", StartedAt: time.Now().UTC()}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	state := lifecycle.State{
		RunID: run.RunID, Feature: run.Feature, TargetDir: target, Task: "write a proposal",
		Phase: lifecycle.PhaseWaiting, NextStage: "product_spec", PendingApprovalID: "input-a",
		ConfigSHA256: strings.Repeat("a", 64), WorkflowSHA256: strings.Repeat("b", 64), CreatedAt: started,
	}
	if err := stateStore.Create(state); err != nil {
		t.Fatal(err)
	}
	firstAt := started.Add(-time.Minute)
	firstOwner, changed, err := srv.store.TakeStage(run.RunID, "product_spec", "input-a", "alice@example.test", "po", firstAt)
	if err != nil || !changed {
		t.Fatalf("initial take = %+v changed=%v err=%v", firstOwner, changed, err)
	}
	firstProjection := getPipelineStageOwners(t, srv, run.ID)
	if owner, ok := firstProjection["product_spec"]; !ok || owner.ApprovalID != "input-a" || owner.ActorID != "alice@example.test" {
		t.Fatalf("approval A owner is missing: %+v", firstProjection)
	}

	// Returning to the same stage creates a new pending input approval. The old
	// owner must disappear as soon as the lifecycle checkpoint points at B.
	controller.approvals = []approval.PendingApproval{
		stageInputApproval("input-a", approval.StatusResolved),
		stageInputApproval("input-b", approval.StatusPending),
	}
	nextState := state
	nextState.PendingApprovalID = "input-b"
	if err := stateStore.Save(state, nextState); err != nil {
		t.Fatal(err)
	}
	if owners := getPipelineStageOwners(t, srv, run.ID); len(owners) != 0 {
		t.Fatalf("approval A owner leaked into approval B: %+v", owners)
	}

	second := takeStageAs(t, srv, run.RunID, "product_spec", "alice@example.test")
	if second.Code != http.StatusOK {
		t.Fatalf("take on approval B: %d %s", second.Code, second.Body.String())
	}
	var secondResult struct {
		Owner   store.StageOwner `json:"owner"`
		Changed bool             `json:"changed"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondResult); err != nil {
		t.Fatal(err)
	}
	if !secondResult.Changed || secondResult.Owner.ApprovalID != "input-b" || secondResult.Owner.ActorID != "alice@example.test" || !secondResult.Owner.TakenAt.After(firstAt) {
		t.Fatalf("approval B did not start a fresh claim: first=%+v second=%+v", firstOwner, secondResult)
	}
	var events []store.Event
	events, err = srv.store.GetEventsAfter(0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("approval visits should produce two take events: events=%+v err=%v", events, err)
	}
	var secondEvent map[string]string
	if err := json.Unmarshal([]byte(events[1].DataJSON), &secondEvent); err != nil {
		t.Fatal(err)
	}
	if events[1].Type != "stage_taken" || secondEvent["approval_id"] != "input-b" || secondEvent["actor_id"] != "alice@example.test" || secondEvent["previous_actor_id"] != "" {
		t.Fatalf("approval B event should be a fresh claim, not a takeover: type=%q data=%+v", events[1].Type, secondEvent)
	}

	// The idempotency key includes the approval ID. A retry on B remains a no-op.
	retry := takeStageAs(t, srv, run.RunID, "product_spec", "alice@example.test")
	if retry.Code != http.StatusOK {
		t.Fatalf("retry on approval B: %d %s", retry.Code, retry.Body.String())
	}
	var retryResult struct {
		Owner   store.StageOwner `json:"owner"`
		Changed bool             `json:"changed"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &retryResult); err != nil {
		t.Fatal(err)
	}
	if retryResult.Changed || !retryResult.Owner.TakenAt.Equal(secondResult.Owner.TakenAt) {
		t.Fatalf("approval B retry reset claim: original=%+v retry=%+v", secondResult, retryResult)
	}

	// Submission resolves B but leaves the waiting checkpoint on that approval;
	// retain its owner until the lifecycle moves on.
	controller.approvals = []approval.PendingApproval{
		stageInputApproval("input-a", approval.StatusResolved),
		stageInputApproval("input-b", approval.StatusResolved),
	}
	resolvedProjection := getPipelineStageOwners(t, srv, run.ID)
	if owner, ok := resolvedProjection["product_spec"]; !ok || owner.ApprovalID != "input-b" || owner.ActorID != "alice@example.test" {
		t.Fatalf("resolved current approval owner should remain visible: %+v", resolvedProjection)
	}
}

func TestMatchingApprovalRoleAuthorizesCanonicalAliasAndPreservesRawRole(t *testing.T) {
	for _, test := range []struct {
		name          string
		principalRole cloudidentity.Role
		rawRole       string
		canonical     cloudidentity.Role
	}{
		{name: "po", principalRole: cloudidentity.RoleProductOwner, rawRole: "po", canonical: cloudidentity.RoleProductOwner},
		{name: "bo", principalRole: cloudidentity.RoleProductOwner, rawRole: "bo", canonical: cloudidentity.RoleProductOwner},
		{name: "business_owner", principalRole: cloudidentity.RoleProductOwner, rawRole: "business_owner", canonical: cloudidentity.RoleProductOwner},
		{name: "deployer", principalRole: cloudidentity.RoleReleaseManager, rawRole: "deployer", canonical: cloudidentity.RoleReleaseManager},
	} {
		t.Run(test.name, func(t *testing.T) {
			principal, err := cloudidentity.NewPrincipal("stage-owner@example.test", []cloudidentity.Role{test.principalRole})
			if err != nil {
				t.Fatal(err)
			}
			rawRole, authorizationRole, ok := matchingApprovalRole(principal, []string{test.rawRole})
			if !ok || rawRole != test.rawRole || authorizationRole != test.canonical {
				t.Fatalf("matching role = raw %q canonical %q ok=%v", rawRole, authorizationRole, ok)
			}
			if err := cloudidentity.Authorize(principal, cloudidentity.PermissionDecision, authorizationRole); err != nil {
				t.Fatalf("canonical role authorization failed: %v", err)
			}
		})
	}
}

func TestDecisionUsesCanonicalAuthorizationAndKeepsRawStageFunction(t *testing.T) {
	for _, rawRole := range []string{"po", "bo", "business_owner", "deployer"} {
		t.Run(rawRole, func(t *testing.T) {
			controller := &fakeRunController{approvals: []approval.PendingApproval{{
				Kind: approval.KindInput, ID: "input-1", RunID: "run-1", AttemptID: "attempt-1",
				FromStage: "product_spec", ToStage: "product_spec", Trigger: humanInputTrigger,
				Status: approval.StatusPending, RequiredRoles: []string{rawRole},
				Payload: []byte(`{"kind":"input","stage_id":"product_spec"}`),
			}}}
			srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = srv.Close() }()
			request := authorizedRequest(t, srv, http.MethodPost, "/api/runs/run-1/approvals/input-1/decisions", `{"actor_role":"spoofed","action":"submit","subject_hash":"`+strings.Repeat("a", 64)+`"}`)
			response := httptest.NewRecorder()
			srv.router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("decision with %q: %d %s", rawRole, response.Code, response.Body.String())
			}
			if controller.decision.ActorRole != rawRole {
				t.Fatalf("approval decision role was canonicalized: got %q want raw stage function %q", controller.decision.ActorRole, rawRole)
			}
			if len(controller.approvals[0].RequiredRoles) != 1 || controller.approvals[0].RequiredRoles[0] != rawRole {
				t.Fatalf("approval required role changed: %+v", controller.approvals[0].RequiredRoles)
			}
		})
	}
}

func stageInputApproval(id string, status approval.Status) approval.PendingApproval {
	return approval.PendingApproval{
		Kind: approval.KindInput, ID: id, RunID: "run-owner-visits", AttemptID: "attempt-" + id,
		FromStage: "product_spec", ToStage: "product_spec", Trigger: humanInputTrigger,
		Status: status, RequiredRoles: []string{"po"},
		Payload: []byte(`{"kind":"input","stage_id":"product_spec","result":"md","output_name":"proposal","output_path":"proposal.md"}`),
	}
}

func getPipelineStageOwners(t *testing.T, srv *Server, runID int64) map[string]store.StageOwner {
	t.Helper()
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, authorizedRequest(t, srv, http.MethodGet, fmt.Sprintf("/api/pipelines/%d", runID), ""))
	if response.Code != http.StatusOK {
		t.Fatalf("pipeline projection: %d %s", response.Code, response.Body.String())
	}
	var projection struct {
		StageOwners map[string]store.StageOwner `json:"stage_owners"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	return projection.StageOwners
}

func TestTakeStageRejectsNonCurrentOrResolvedInput(t *testing.T) {
	for _, test := range []struct {
		name     string
		state    lifecycle.State
		approval approval.PendingApproval
	}{
		{
			name:     "not current stage",
			state:    lifecycle.State{Phase: lifecycle.PhaseWaiting, NextStage: "another", PendingApprovalID: "input-1"},
			approval: approval.PendingApproval{Kind: approval.KindInput, ID: "input-1", FromStage: "product_spec", ToStage: "product_spec", Trigger: humanInputTrigger, Status: approval.StatusPending, RequiredRoles: []string{"product_owner"}, Payload: []byte(`{"kind":"input","stage_id":"product_spec"}`)},
		},
		{
			name:     "resolved approval",
			state:    lifecycle.State{Phase: lifecycle.PhaseWaiting, NextStage: "product_spec", PendingApprovalID: "input-1"},
			approval: approval.PendingApproval{Kind: approval.KindInput, ID: "input-1", FromStage: "product_spec", ToStage: "product_spec", Trigger: humanInputTrigger, Status: approval.StatusResolved, RequiredRoles: []string{"product_owner"}, Payload: []byte(`{"kind":"input","stage_id":"product_spec"}`)},
		},
		{
			name:     "stale approval from an earlier visit",
			state:    lifecycle.State{Phase: lifecycle.PhaseWaiting, NextStage: "product_spec", PendingApprovalID: "input-2"},
			approval: approval.PendingApproval{Kind: approval.KindInput, ID: "input-1", FromStage: "product_spec", ToStage: "product_spec", Trigger: humanInputTrigger, Status: approval.StatusPending, RequiredRoles: []string{"product_owner"}, Payload: []byte(`{"kind":"input","stage_id":"product_spec"}`)},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := t.TempDir()
			controller := &fakeRunController{approvals: []approval.PendingApproval{test.approval}}
			srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"), WithRunController(controller), WithTargetDir(target))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = srv.Close() }()
			run := &store.PipelineRun{RunID: "run-owner", Feature: "feature", Status: "waiting_for_approval", StartedAt: time.Now().UTC()}
			if err := srv.store.CreatePipelineRun(run); err != nil {
				t.Fatal(err)
			}
			stateStore, err := lifecycle.NewStore(target)
			if err != nil {
				t.Fatal(err)
			}
			test.state.RunID, test.state.Feature, test.state.TargetDir, test.state.Task = run.RunID, run.Feature, target, "task"
			test.state.ConfigSHA256, test.state.WorkflowSHA256, test.state.CreatedAt = strings.Repeat("a", 64), strings.Repeat("b", 64), time.Now().UTC()
			if err := stateStore.Create(test.state); err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			srv.router.ServeHTTP(response, authorizedRequest(t, srv, http.MethodPost, "/api/runs/run-owner/stages/product_spec/take", ""))
			if response.Code != http.StatusConflict {
				t.Fatalf("take stage returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func takeStageAs(t *testing.T, srv *Server, runID, stageID, actorID string) *httptest.ResponseRecorder {
	t.Helper()
	request := authorizedRequest(t, srv, http.MethodPost, "/api/runs/"+runID+"/stages/"+stageID+"/take", "")
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		t.Fatal(err)
	}
	srv.sessionMu.Lock()
	session := srv.sessions[cookie.Value]
	session.Principal.ActorID = actorID
	srv.sessions[cookie.Value] = session
	srv.sessionMu.Unlock()
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	return response
}
