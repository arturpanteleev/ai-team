package web

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/stagestatus"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func appendS11Event(t *testing.T, f functionPermissionFixture, runID, eventType, attemptID string, data map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	event := &store.Event{RunID: runID, Type: eventType, AttemptID: attemptID, Timestamp: time.Now().UTC(), DataJSON: string(encoded)}
	if err := f.srv.store.AppendEventNext(event); err != nil {
		t.Fatal(err)
	}
}

func TestStageStatusRouteRefusesBadRunsAndWritesNothing(t *testing.T) {
	now := time.Now().UTC()
	f := newFunctionPermissionFixture(t, false, []approval.PendingApproval{buildInputApproval("input-take", approval.StatusPending, now)},
		map[string]lifecycle.State{"run-input": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"}})
	get := func(runID string) *httpResult {
		response := f.requestAs(t, "owner@example.test", s11Roles, http.MethodGet, "/api/runs/"+runID+"/stage-statuses", "")
		return &httpResult{Code: response.Code, Body: response.Body.Bytes()}
	}
	if result := get("bad%5Cid"); result.Code != http.StatusBadRequest {
		t.Fatalf("a malformed run id is a 400: %d %s", result.Code, result.Body)
	}
	if result := get("no-such-run"); result.Code != http.StatusNotFound {
		t.Fatalf("an unknown run is a 404: %d %s", result.Code, result.Body)
	}
	// A run record without a pinned template cannot be projected.
	if err := f.srv.store.CreatePipelineRun(&store.PipelineRun{RunID: "run-unpinned", Feature: "feature", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if result := get("run-unpinned"); result.Code != http.StatusConflict {
		t.Fatalf("an unpinned run is a 409: %d %s", result.Code, result.Body)
	}
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete} {
		response := f.requestAs(t, "owner@example.test", s11Roles, method, "/api/runs/run-input/stage-statuses", `{"status":"passed"}`)
		if response.Code != http.StatusMethodNotAllowed && response.Code != http.StatusNotFound {
			t.Fatalf("%s must not exist on the projection: %d", method, response.Code)
		}
	}
}

func TestStageStatusProjectionReachesTheTaskViewAndTheCard(t *testing.T) {
	now := time.Now().UTC()
	f := newFunctionPermissionFixture(t, false, []approval.PendingApproval{buildInputApproval("input-take", approval.StatusPending, now)},
		map[string]lifecycle.State{"run-input": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"}})
	// The journal of the task: it started at spec, spec finished, build opened
	// and asked a person for the result.
	appendS11Event(t, f, "run-input", "run_started", "", map[string]any{})
	appendS11Event(t, f, "run-input", "attempt_started", "attempt-spec", map[string]any{"agent": "spec", "stage_index": 1})
	appendS11Event(t, f, "run-input", "attempt_finished", "attempt-spec", map[string]any{"stage": "spec", "outcome": "passed", "status": "passed", "executor": "human", "actor_id": "po-1"})
	appendS11Event(t, f, "run-input", "transition_selected", "attempt-spec", map[string]any{"from": "spec", "target": "build", "outcome": "passed", "edge_target": "build"})
	appendS11Event(t, f, "run-input", "approval_requested", "attempt-spec", map[string]any{"approval_id": "input-take", "kind": "input", "trigger": "human_input",
		"from_stage": "build", "to_stage": "build", "required_roles": []string{"developer"}, "actions": []string{"reject", "submit", "skip"}})

	response := f.requestAs(t, "owner@example.test", s11Roles, http.MethodGet, "/api/runs/run-input", "")
	if response.Code != http.StatusOK {
		t.Fatalf("task view: %d %s", response.Code, response.Body.String())
	}
	var details taskRunDetails
	if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	if details.StageStatuses == nil || details.StageStatuses.CurrentStage != "build" {
		t.Fatalf("the task view carries the projection: %s", response.Body.String())
	}
	build := details.StageStatuses.Stages[1]
	if build.StageID != "build" || build.Status != stagestatus.StatusReady || build.Executor.Kind != stagestatus.ExecutorHuman ||
		build.BlockedBy == nil || build.BlockedBy.Kind != stagestatus.BlockHumanInput || !actionIn(build.AllowedActions, "take") {
		t.Fatalf("a human stage that waits for a person to take it: %+v", build)
	}
	if spec := details.StageStatuses.Stages[0]; spec.Status != stagestatus.StatusPassed || spec.Executor.Actor != "po-1" || spec.Visits[0].Active {
		t.Fatalf("spec is done by po-1: %+v", spec)
	}

	list := f.requestAs(t, "owner@example.test", s11Roles, http.MethodGet, "/api/runs", "")
	var page struct {
		Runs []taskRunListItem `json:"runs"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 1 || page.Runs[0].StageStatus == nil || page.Runs[0].StageStatus.Status != stagestatus.StatusReady ||
		page.Runs[0].StageStatus.CurrentStage != "build" || len(page.Runs[0].StageStatus.Route) != 3 {
		t.Fatalf("the card carries the compact projection: %s", list.Body.String())
	}
}

type httpResult struct {
	Code int
	Body []byte
}
