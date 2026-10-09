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
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func TestTakeStageRequiresCurrentHumanInputAndProjectsTakeover(t *testing.T) {
	target := t.TempDir()
	controller := &fakeRunController{approvals: []approval.PendingApproval{{
		Kind: approval.KindInput, ID: "input-1", RunID: "run-owner", AttemptID: "attempt-1",
		FromStage: "product_spec", ToStage: "product_spec", Trigger: humanInputTrigger,
		Status: approval.StatusPending, RequiredRoles: []string{"product_owner"},
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
	if !firstResult.Changed || firstResult.Owner.ActorID != "alice@example.test" || firstResult.Owner.ActorRole != "product_owner" || firstResult.Owner.TakenAt.IsZero() {
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
	if events[1].Type != "stage_taken" || eventData["actor_name"] != "bob@example.test" || eventData["previous_actor_name"] != "alice@example.test" {
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
