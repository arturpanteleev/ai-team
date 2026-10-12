package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// The observability page selects inclusive calendar days and sends the next
// day as the exclusive `to` bound in YYYY-MM-DD form.
func TestFlowMetricsDateOnlyPeriodIsHalfOpen(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	runs := []struct {
		id      string
		started time.Time
	}{
		{"before-period", time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC)},
		{"first-day", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{"last-day", time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)},
		{"after-period", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, fixture := range runs {
		completed := fixture.started.Add(time.Hour)
		run := &store.PipelineRun{RunID: fixture.id, Feature: fixture.id, Status: "completed", StartedAt: fixture.started, CompletedAt: &completed}
		if err := srv.store.CreatePipelineRun(run); err != nil {
			t.Fatal(err)
		}
		stage := store.Stage{PipelineRunID: run.ID, AttemptID: fixture.id + "-attempt", StageIndex: 1, AgentName: "implementation",
			Status: "passed", Execution: "agent", Decision: "approved", Outcome: "passed", StartedAt: fixture.started,
			CompletedAt: &completed, DurationMs: 60_000}
		if err := srv.store.CreateStage(&stage); err != nil {
			t.Fatal(err)
		}
		if err := srv.store.UpdateStage(&stage); err != nil {
			t.Fatal(err)
		}
	}

	result := getFlowMetricsJSON(t, srv, "/api/metrics/flow?from=2026-10-01&to=2026-11-01")
	if !result.From.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || !result.To.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("period echo = %s..%s", result.From, result.To)
	}
	if len(result.ByStage) != 1 || result.ByStage[0].Attempts != 2 {
		t.Fatalf("want only the two in-period attempts, got %+v", result.ByStage)
	}
}

func TestFlowMetricsEmptyPeriodReportsNoDataInsteadOfZeroes(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	result := getFlowMetricsJSON(t, srv, "/api/metrics/flow?from=2026-12-01&to=2026-12-02")
	if result.LeadTime.Count != 0 || result.LeadTime.MedianMS != nil || result.LeadTime.P80MS != nil ||
		result.CycleTime.Count != 0 || result.CycleTime.MedianMS != nil {
		t.Fatalf("empty period must not report durations: lead=%+v cycle=%+v", result.LeadTime, result.CycleTime)
	}
	if result.ByStage == nil || len(result.ByStage) != 0 || result.Usage.TokensInput != nil || result.Usage.TokensOutput != nil ||
		result.AgentResults != 0 || result.AgentAcceptedWithoutEditRate != nil {
		t.Fatalf("empty period must report empty stages and unknown usage: %+v", result)
	}

	request := newLoopbackRequest(http.MethodGet, "/api/metrics/flow.csv?from=2026-12-01&to=2026-12-02", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.HasPrefix(body, "metric,stage,value,unit,attestation,source,basis\n") ||
		!strings.Contains(body, "\nlead_time_median,\"\",,") || strings.Contains(body, "stage_attempts,") {
		t.Fatalf("empty CSV: status=%d body=%s", response.Code, body)
	}
}

func TestFlowMetricsRejectsInvalidPeriods(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	for _, query := range []string{
		"from=2026-10-31&to=2026-10-01",
		"from=2026-10-01&to=2026-10-01",
		"from=01.10.2026&to=2026-10-31",
		"from=2026-10-01&to=tomorrow",
	} {
		for _, path := range []string{"/api/metrics/flow", "/api/metrics/flow.csv"} {
			request := newLoopbackRequest(http.MethodGet, path+"?"+query, nil)
			response := httptest.NewRecorder()
			srv.router.ServeHTTP(response, request)
			var payload apiResponseError
			if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Code != "bad_period" {
				t.Errorf("%s?%s: status=%d body=%s", path, query, response.Code, response.Body.String())
			}
		}
	}
}

func getFlowMetricsJSON(t *testing.T, srv *Server, path string) flowMetricsResponse {
	t.Helper()
	request := newLoopbackRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	var result flowMetricsResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("%s: status=%d body=%s", path, response.Code, response.Body.String())
	}
	return result
}
