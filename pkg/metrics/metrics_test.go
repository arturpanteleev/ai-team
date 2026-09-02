package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func fixedResult(name, attemptID string, duration time.Duration, superseded bool) workflow.StageResult {
	return workflow.StageResult{
		RunID: "run-1", AttemptID: attemptID, Name: name,
		Status: workflow.StatusPassed, Duration: duration, Superseded: superseded,
	}
}

func TestBuildExcludesSupersededAndAggregates(t *testing.T) {
	started := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)
	results := []workflow.StageResult{
		fixedResult("analyst", "run-1-001-analyst", 10*time.Second, false),
		fixedResult("coder", "run-1-002-coder", 20*time.Second, false),
		fixedResult("reviewer", "run-1-003-reviewer", 15*time.Second, true),
		fixedResult("coder", "run-1-004-coder", 30*time.Second, false),
		fixedResult("reviewer", "run-1-005-reviewer", 5*time.Second, false),
	}
	envelope := Build("run-1", "feature-x", started, finished, results, 2, "completed", Usage{Attested: true, TokensInput: 123, TokensOutput: 45, CostUSD: 0.42})

	if envelope.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, хочу %d", envelope.SchemaVersion, SchemaVersion)
	}
	if envelope.RunID != "run-1" || envelope.Feature != "feature-x" {
		t.Fatalf("identity: %+v", envelope)
	}
	if envelope.TokensUnknown {
		t.Fatal("tokens_unknown должен быть false при attested usage")
	}
	if !envelope.UsageReported {
		t.Fatal("usage_reported должен быть true при attested usage")
	}
	if envelope.TokensInput != 123 || envelope.TokensOutput != 45 || envelope.CostUSD != 0.42 {
		t.Fatalf("attested usage: %+v", envelope)
	}
	if envelope.Outcome != "completed" || envelope.LoopbackCycles != 2 {
		t.Fatalf("outcome/loops: %+v", envelope)
	}
	if envelope.TotalDurationMS != 90000 {
		t.Fatalf("total_duration_ms = %d, хочу 90000", envelope.TotalDurationMS)
	}
	wantStages := []StageMetrics{
		{Stage: "analyst", Attempts: 1, DurationMS: 10000},
		{Stage: "coder", Attempts: 2, DurationMS: 50000},
		{Stage: "reviewer", Attempts: 1, DurationMS: 5000},
	}
	if len(envelope.Stages) != len(wantStages) {
		t.Fatalf("stages = %+v, хочу %+v", envelope.Stages, wantStages)
	}
	for i, want := range wantStages {
		if envelope.Stages[i] != want {
			t.Fatalf("stages[%d] = %+v, хочу %+v", i, envelope.Stages[i], want)
		}
	}
}

func TestBuildZeroTimesOmitsTotalDuration(t *testing.T) {
	envelope := Build("run-2", "f", time.Time{}, time.Time{}, nil, 0, "failed", Usage{})
	if envelope.TotalDurationMS != 0 {
		t.Fatalf("total_duration_ms = %d при нулевых временах", envelope.TotalDurationMS)
	}
	if len(envelope.Stages) != 0 {
		t.Fatalf("stages = %+v, хочу пусто", envelope.Stages)
	}
	if !envelope.TokensUnknown || envelope.UsageReported {
		t.Fatalf("без attested usage envelope должен быть unknown: %+v", envelope)
	}
}

func TestFormatTable(t *testing.T) {
	started := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	envelope := Build("run-1", "feature-x", started, started.Add(35*time.Second), []workflow.StageResult{
		fixedResult("analyst", "a", 10*time.Second, false),
		fixedResult("coder", "b", 20*time.Second, false),
	}, 1, "completed", Usage{})
	var out strings.Builder
	if err := envelope.Format(&out); err != nil {
		t.Fatalf("format: %v", err)
	}
	rendered := out.String()
	for _, want := range []string{"Этап", "analyst", "10000ms", "coder", "20000ms", "ИТОГО", "35000ms"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("в таблице нет %q:\n%s", want, rendered)
		}
	}
}
