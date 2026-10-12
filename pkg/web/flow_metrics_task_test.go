package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// The task timeline reads per-stage aggregates for one task through the same
// flow metrics calculation as the observability page.
func TestFlowMetricsRunSelectionReturnsOnlyThatTask(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	started := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	for index, runID := range []string{"timeline-run", "other-run"} {
		runStarted := started.Add(time.Duration(index) * time.Minute)
		completed := runStarted.Add(time.Hour)
		run := &store.PipelineRun{RunID: runID, Feature: runID, Status: "completed", StartedAt: runStarted, CompletedAt: &completed}
		if err := srv.store.CreatePipelineRun(run); err != nil {
			t.Fatal(err)
		}
		// The recorder stores the workflow execution state, not the
		// executor kind, in stages.execution.
		stage := store.Stage{PipelineRunID: run.ID, AttemptID: runID + "-attempt", StageIndex: 1, AgentName: "implementation",
			Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed", StartedAt: runStarted,
			CompletedAt: &completed, DurationMs: 60_000}
		if err := srv.store.CreateStage(&stage); err != nil {
			t.Fatal(err)
		}
		if err := srv.store.UpdateStage(&stage); err != nil {
			t.Fatal(err)
		}
		if err := srv.store.AppendEventNext(&store.Event{RunID: runID, Type: "attempt_finished", AttemptID: stage.AttemptID,
			Timestamp: completed, DataJSON: `{"stage":"implementation","executor":"agent","outcome":"passed"}`}); err != nil {
			t.Fatal(err)
		}
		if err := srv.store.AppendEventNext(&store.Event{RunID: runID, Type: "stage_skipped", Stage: "review",
			Timestamp: completed, DataJSON: `{"reason":"not needed"}`}); err != nil {
			t.Fatal(err)
		}
	}

	result := getFlowMetricsJSON(t, srv, "/api/metrics/flow?run_id=timeline-run")
	if len(result.ByStage) != 1 || result.ByStage[0].Attempts != 1 {
		t.Fatalf("run selection must include only the selected task's attempt, got %+v", result.ByStage)
	}
	if result.ByStage[0].Agent != 1 || result.ByStage[0].Human != 0 {
		t.Fatalf("executor kind must come from attempt_finished, got human=%d agent=%d", result.ByStage[0].Human, result.ByStage[0].Agent)
	}
	if result.Skips["review"] != 1 {
		t.Fatalf("skips must be counted for the selected task only, got %+v", result.Skips)
	}
	if !result.From.Equal(started) || result.To.Before(result.From) {
		t.Fatalf("run selection must echo the task interval, got %s..%s", result.From, result.To)
	}

	combined := getFlowMetricsJSON(t, srv, "/api/metrics/flow?run_id=timeline-run&from=2026-10-13&to=2026-10-14")
	if len(combined.ByStage) != 0 {
		t.Fatalf("an explicit period must still apply to the selected task, got %+v", combined.ByStage)
	}

	request := newLoopbackRequest(http.MethodGet, "/api/metrics/flow.csv?run_id=timeline-run", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "stage_attempts,\"implementation\",1,") ||
		!strings.Contains(response.Body.String(), "skip_count,\"review\",1,") {
		t.Fatalf("CSV run selection: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFlowMetricsRunSelectionRejectsUnknownOrInvalidRun(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	for path, want := range map[string]int{
		"/api/metrics/flow?run_id=missing-run":     http.StatusNotFound,
		"/api/metrics/flow?run_id=..%2Fescape":     http.StatusBadRequest,
		"/api/metrics/flow.csv?run_id=missing-run": http.StatusNotFound,
	} {
		request := newLoopbackRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)
		if response.Code != want || !strings.Contains(response.Body.String(), `"code"`) {
			t.Fatalf("%s: status=%d body=%s, want %d JSON error", path, response.Code, response.Body.String(), want)
		}
	}
}
