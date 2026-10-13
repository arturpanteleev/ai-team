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

// BL-26: the executor kind comes from attempt_finished even when the stage row
// only holds the workflow execution state; a later human attempt without edit
// metadata counts as a human edit of the agent result; a skipped attempt is
// counted in neither executor nor the agent share.
func TestFlowMetricsHumanExecutorEditAndSkip(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	started := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	type attempt struct {
		id, stage, executor, outcome, data string
	}
	createRun := func(runID string, attempts []attempt) {
		completed := started.Add(time.Hour)
		run := &store.PipelineRun{RunID: runID, Feature: runID, Status: "completed", StartedAt: started, CompletedAt: &completed}
		if err := srv.store.CreatePipelineRun(run); err != nil {
			t.Fatal(err)
		}
		for index, value := range attempts {
			at := started.Add(time.Duration(index+1) * time.Minute)
			done := at.Add(30 * time.Second)
			stage := store.Stage{PipelineRunID: run.ID, AttemptID: value.id, StageIndex: index + 1, AgentName: value.stage,
				Status: value.outcome, Execution: "succeeded", Decision: "approved", Outcome: value.outcome, StartedAt: at,
				CompletedAt: &done, DurationMs: 30_000}
			if err := srv.store.CreateStage(&stage); err != nil {
				t.Fatal(err)
			}
			if err := srv.store.UpdateStage(&stage); err != nil {
				t.Fatal(err)
			}
			data := `{"stage":"` + value.stage + `","outcome":"` + value.outcome + `"` + value.data + `}`
			if err := srv.store.AppendEventNext(&store.Event{RunID: runID, Type: "attempt_finished", AttemptID: value.id,
				Executor: value.executor, Timestamp: done, DataJSON: data}); err != nil {
				t.Fatal(err)
			}
		}
	}
	createRun("edited-run", []attempt{
		{id: "impl-agent", stage: "implementation", executor: "agent", outcome: "passed"},
		{id: "impl-human", stage: "implementation", executor: "human", outcome: "passed"},
		{id: "docs-skip", stage: "docs", executor: "agent", outcome: "skipped"},
	})
	createRun("reviewed-run", []attempt{
		{id: "rev-agent", stage: "implementation", executor: "agent", outcome: "passed"},
		{id: "rev-human", stage: "implementation", executor: "human", outcome: "passed", data: `,"human_edit_of_agent_attempt_id":""`},
	})

	stageOf := func(result flowMetricsResponse, name string) flowStageStats {
		t.Helper()
		for _, stage := range result.ByStage {
			if stage.Stage == name {
				return stage
			}
		}
		t.Fatalf("stage %q missing in %+v", name, result.ByStage)
		return flowStageStats{}
	}

	edited := getFlowMetricsJSON(t, srv, "/api/metrics/flow?run_id=edited-run")
	implementation := stageOf(edited, "implementation")
	if implementation.Human != 1 || implementation.Agent != 1 || implementation.AgentShare == nil || *implementation.AgentShare != 0.5 {
		t.Fatalf("executor kinds must come from attempt_finished, got %+v", implementation)
	}
	if edited.AgentResults != 1 || edited.AgentAcceptedWithoutEdit != 0 {
		t.Fatalf("a later human attempt without edit metadata is a human edit, got %d/%d", edited.AgentAcceptedWithoutEdit, edited.AgentResults)
	}
	docs := stageOf(edited, "docs")
	if docs.Attempts != 1 || docs.Skipped != 1 || docs.Agent != 0 || docs.Human != 0 || docs.AgentShare != nil ||
		docs.UsageUnknownAttempts != 0 || docs.UsageAttestation != "not_applicable" {
		t.Fatalf("a skipped attempt must not count toward an executor or usage, got %+v", docs)
	}

	reviewed := getFlowMetricsJSON(t, srv, "/api/metrics/flow?run_id=reviewed-run")
	if reviewed.AgentResults != 1 || reviewed.AgentAcceptedWithoutEdit != 1 {
		t.Fatalf("a human attempt with empty edit metadata is not an edit, got %d/%d", reviewed.AgentAcceptedWithoutEdit, reviewed.AgentResults)
	}
}

// BL-26: a store failure while resolving run_id is a 500, not "run not found".
func TestFlowMetricsRunSelectionStoreFailureIs500(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.Close(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	for _, path := range []string{"/api/metrics/flow?run_id=any-run", "/api/metrics/flow.csv?run_id=any-run"} {
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, newLoopbackRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("%s: status=%d body=%s, want 500", path, response.Code, response.Body.String())
		}
	}
}

// BL-27: text cells that a spreadsheet would evaluate as a formula are
// prefixed with an apostrophe.
func TestCSVFieldNeutralizesFormulas(t *testing.T) {
	for input, want := range map[string]string{
		"implementation":         `"implementation"`,
		"=HYPERLINK(\"x\")":      `"'=HYPERLINK(""x"")"`,
		"+1":                     `"'+1"`,
		"-2":                     `"'-2"`,
		"@SUM(A1)":               `"'@SUM(A1)"`,
		"\tcmd":                  "\"'\tcmd\"",
		"\rcmd":                  "\"'\rcmd\"",
		"":                       `""`,
		"execution=a;decision=b": `"execution=a;decision=b"`,
	} {
		if got := csvField(input); got != want {
			t.Fatalf("csvField(%q) = %s, want %s", input, got, want)
		}
	}
}
