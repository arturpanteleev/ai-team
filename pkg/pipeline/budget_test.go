package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/report"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestRun_BudgetAttemptsCap(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}

	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}, config.AgentConfig{Name: "deployer"})
	cfg.Budget = &config.BudgetConfig{MaxAttempts: 2}

	err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{})
	if err == nil {
		t.Fatal("ожидалась бюджет-ошибка при переборе total attempts")
	}
	if !strings.Contains(err.Error(), "run budget: превышен лимит попыток max_attempts=2") {
		t.Fatalf("ошибка бюджета не распознана: %v", err)
	}
	// analyst запущен, затем reviewer — попытка 3 должна не выполнить deployer.
	if len(rt.executed) != 2 || rt.executed[1] != "reviewer" {
		t.Fatalf("executed после кэпа: %v", rt.executed)
	}
}

func TestRun_BudgetWallTime(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.waitCtx["analyst"] = true // блокируется до отмены ctx

	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	cfg.Budget = &config.BudgetConfig{MaxWallTime: "200ms"}

	start := time.Now()
	err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{})
	if err == nil {
		t.Fatal("ожидалась бюджет-ошибка по wall-time")
	}
	if !strings.Contains(err.Error(), "run budget: превышен max_execution_time 200ms") {
		t.Fatalf("ошибка бюджета не распознана: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("budget не остановил run вовремя: %v", elapsed)
	}
}

// Конфиг БЕЗ секции budget: таймер wall-time всё равно вооружается. Проверка
// через deadline, который видит стадия: per-agent timeout заведомо длиннее
// дефолтного бюджета, поэтому наблюдаемый дедлайн может прийти только от
// run-бюджета. Без вооружённого таймера дедлайн был бы 48h (QS-09, #143).
func TestRun_DefaultWallTimeArmedWithoutBudgetSection(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}

	cfg := cfgFor(config.AgentConfig{Name: "analyst", Timeout: "48h"}, config.AgentConfig{Name: "reviewer", Timeout: "48h"})
	if cfg.Budget != nil {
		t.Fatal("тест требует конфиг без секции budget")
	}

	start := time.Now()
	if err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{}); err != nil {
		t.Fatalf("ожидался успех, got: %v", err)
	}
	if !rt.firstHasDeadline {
		t.Fatal("стадия исполнена без deadline: run не ограничен по времени вообще")
	}
	assertDeadlineNear(t, rt.firstDeadline.Sub(start), config.DefaultBudgetMaxWallTimeDuration)
}

// Стадия без stage_timeout и без per-agent timeout тоже ограничена: дефолт
// применяется на уровне кода, а не только в сгенерированном `init` конфиге.
func TestRun_DefaultStageTimeoutArmedWithoutConfiguredValue(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}

	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	if cfg.StageTimeout != "" {
		t.Fatal("тест требует конфиг без stage_timeout")
	}

	start := time.Now()
	if err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{}); err != nil {
		t.Fatalf("ожидался успех, got: %v", err)
	}
	if !rt.firstHasDeadline {
		t.Fatal("стадия исполнена без deadline")
	}
	assertDeadlineNear(t, rt.firstDeadline.Sub(start), config.DefaultStageTimeout)
}

// assertDeadlineNear сверяет наблюдаемый дедлайн с ожидаемым бюджетом.
// Допуск нужен с обеих сторон: точка отсчёта теста и момент, когда пайплайн
// вооружает таймер, расходятся на время подготовки run'а.
func assertDeadlineNear(t *testing.T, observed, want time.Duration) {
	t.Helper()
	if observed < want-time.Minute || observed > want+time.Minute {
		t.Fatalf("deadline стадии %v, ожидался бюджет ~%v", observed, want)
	}
}

// TestRun_AttestedUsagePersisted проверяет, что usage принимается ТОЛЬКО от
// attested источника и попадает в usage.json envelope (P1-7).
func TestRun_AttestedUsagePersisted(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	rt.usagePer = map[string]*runtime.Usage{
		"analyst":  {Attested: true, TokensInput: 111, TokensOutput: 22, CostUSD: 1.25},
		"reviewer": {Attested: true, TokensInput: 10, TokensOutput: 5, CostUSD: 0.1},
	}

	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("ожидался успех, got: %v", err)
	}

	runDir := onlyRunDir(t, dir)
	raw, err := os.ReadFile(filepath.Join(runDir, "usage.json"))
	if err != nil {
		t.Fatalf("usage.json: %v", err)
	}
	var envelope metrics.UsageEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("usage.json не парсится: %v", err)
	}
	if !envelope.UsageReported || envelope.TokensUnknown {
		t.Fatalf("ожидали attested usage: %+v", envelope)
	}
	if envelope.TokensInput != 121 || envelope.TokensOutput != 27 || envelope.CostUSD != 1.35 {
		t.Fatalf("usage-значения: %+v", envelope)
	}
}

type fixedAttemptManifestSource []byte

func (s fixedAttemptManifestSource) ReadAttemptManifest(_, _, _ string) ([]byte, error) {
	return append([]byte(nil), s...), nil
}

func TestReplayedStageResultRestoresManifestReportFields(t *testing.T) {
	started := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	manifest := evidence.AttemptManifest{
		SchemaVersion: evidence.SchemaVersion, RunID: "replay-fields", AttemptID: "attempt-coder-2",
		Stage: "coder", StageIndex: 2, TotalStages: 5, StartedAt: started, FinishedAt: finished,
		Status: "passed", Mutations: []string{"src/changed.go"},
		MutationChanges: []workflow.MutationChange{{Path: "pkg/example_test.go", Kind: workflow.MutationAdded, Class: "tests"}},
		Delivery:        &delivery.Result{PlanHash: "plan-123", CommitSHA: "commit-456"},
		Usage:           &workflow.AttemptUsage{Attested: true, TokensInput: 17, TokensOutput: 4, CostUSD: 0.75},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	run := evidence.ReplayedRun{RunID: manifest.RunID, Attempts: []evidence.ReplayedAttempt{{
		AttemptID: manifest.AttemptID, Stage: manifest.Stage, StageIndex: manifest.StageIndex,
		StartedAt: started, FinishedAt: finished, Status: manifest.Status, ManifestSHA256: "manifest-digest",
	}}}
	results, usage, unknown, err := replayedStageResults(run, t.TempDir(), fixedAttemptManifestSource(data), testRegistry(), 5)
	if err != nil {
		t.Fatalf("replay attempt: %v", err)
	}
	if unknown || usage.CostUSD != 0.75 || usage.TokensInput != 17 || usage.TokensOutput != 4 {
		t.Fatalf("replayed usage was not restored: usage=%+v unknown=%t", usage, unknown)
	}
	result := results[0]
	if result.TotalStages != 5 || !reflect.DeepEqual(result.Mutations, manifest.Mutations) ||
		!reflect.DeepEqual(result.MutationChanges, manifest.MutationChanges) || !reflect.DeepEqual(result.Delivery, manifest.Delivery) {
		t.Fatalf("attempt report fields were not restored from manifest: %+v", result)
	}
	reportsDir := t.TempDir()
	if err := report.GenerateStageReport(reportsDir, "feature", result.AttemptID, result, t.TempDir()); err != nil {
		t.Fatalf("generate replayed report: %v", err)
	}
	page, err := os.ReadFile(filepath.Join(reportsDir, "feature", "attempts", result.AttemptID, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Stage:</strong> 2/5", "src/changed.go", "pkg/example_test.go", "plan-123", "commit-456"} {
		if !strings.Contains(string(page), expected) {
			t.Fatalf("restored stage report missing %q:\n%s", expected, page)
		}
	}
}

// TestRun_UnattestedUsageStaysUnknown: адаптер БЕЗ attested usage не должен
// влиять на envelope (остаётся tokens_unknown=true).
func TestRun_UnattestedUsageStaysUnknown(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.usage = &runtime.Usage{Attested: false, TokensInput: 9}

	cfg := cfgFor(config.AgentConfig{Name: "analyst"})
	err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("ожидался успех, got: %v", err)
	}

	runDir := onlyRunDir(t, dir)
	raw, err := os.ReadFile(filepath.Join(runDir, "usage.json"))
	if err != nil {
		t.Fatalf("usage.json: %v", err)
	}
	var envelope metrics.UsageEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("usage.json не парсится: %v", err)
	}
	if !envelope.TokensUnknown || envelope.UsageReported {
		t.Fatalf("unattested usage не должен маркироваться reported: %+v", envelope)
	}
}

func TestRun_AttemptUsageSurvivesTwoPausesWithoutDoubleCounting(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	rt.usagePer = map[string]*runtime.Usage{
		"analyst":  {Attested: true, TokensInput: 5, TokensOutput: 2, CostUSD: 0.11},
		"reviewer": {Attested: true, TokensInput: 7, TokensOutput: 3, CostUSD: 0.22},
		"deployer": {Attested: true, TokensInput: 11, TokensOutput: 4, CostUSD: 0.33},
	}
	cfg := cfgForGraph(nil,
		config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}, config.AgentConfig{Name: "deployer"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "durable usage", TargetDir: dir})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("first run should pause after analyst: result=%+v err=%v", first, err)
	}
	approve := func(approvalID string) time.Time {
		t.Helper()
		store, err := approval.NewStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		pending, err := store.Load(first.RunID, approvalID)
		if err != nil {
			t.Fatal(err)
		}
		decidedAt := time.Now().UTC()
		decided, err := store.Decide(first.RunID, approvalID, approval.Decision{
			ActorID: "operator-1", ActorRole: "operator", Action: "approve",
			SubjectHash: pending.SubjectHash, DecidedAt: decidedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		if decided.ResolvedAt.IsZero() || !decided.ResolvedAt.Equal(decidedAt) {
			t.Fatalf("approval did not preserve Decision.DecidedAt: %+v", decided)
		}
		time.Sleep(20 * time.Millisecond)
		return decidedAt
	}
	firstDecisionAt := approve(required.ApprovalID)
	reportsDir := filepath.Join(dir, ".ai-team", "reports", "feat")
	if err := os.RemoveAll(reportsDir); err != nil {
		t.Fatalf("remove derived report projection before first resume: %v", err)
	}
	second, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if !errors.As(err, &required) {
		t.Fatalf("second execution should pause after reviewer: result=%+v err=%v", second, err)
	}
	secondDecisionAt := approve(required.ApprovalID)
	if err := os.RemoveAll(reportsDir); err != nil {
		t.Fatalf("remove derived report projection before second resume: %v", err)
	}
	third, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil || third.Outcome != "completed" {
		t.Fatalf("third execution should finish: result=%+v err=%v", third, err)
	}

	runDir := filepath.Join(dir, ".ai-team", "runs", first.RunID)
	raw, err := os.ReadFile(filepath.Join(runDir, "usage.json"))
	if err != nil {
		t.Fatalf("usage.json: %v", err)
	}
	var envelope metrics.UsageEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.TokensUnknown || !envelope.UsageReported || envelope.TokensInput != 23 || envelope.TokensOutput != 9 || envelope.CostUSD < 0.659999 || envelope.CostUSD > 0.660001 {
		t.Fatalf("two-pause totals were lost or double-counted: %+v", envelope)
	}
	var manifestStartedAt time.Time
	manifestRaw, err := os.ReadFile(filepath.Join(runDir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest evidence.RunManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifestStartedAt = manifest.StartedAt
	if !envelope.StartedAt.Equal(manifestStartedAt) || envelope.TotalDurationMS < 30 {
		t.Fatalf("task duration must start at original creation and include pauses: envelope=%+v manifest=%s", envelope, manifestStartedAt)
	}
	attemptEntries, err := os.ReadDir(filepath.Join(runDir, "attempts"))
	if err != nil || len(attemptEntries) != 3 {
		t.Fatalf("expected three durable attempts: entries=%d err=%v", len(attemptEntries), err)
	}
	var inputTotal, outputTotal int64
	var costTotal float64
	for _, entry := range attemptEntries {
		data, err := os.ReadFile(filepath.Join(runDir, "attempts", entry.Name(), "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var attempt evidence.AttemptManifest
		if err := json.Unmarshal(data, &attempt); err != nil {
			t.Fatal(err)
		}
		if attempt.Usage == nil || !attempt.Usage.Attested {
			t.Fatalf("attempt %s did not persist attested usage: %+v", entry.Name(), attempt.Usage)
		}
		if attempt.TotalStages != 3 {
			t.Fatalf("attempt %s should persist total stage count 3, got %d", entry.Name(), attempt.TotalStages)
		}
		stageHTML, err := os.ReadFile(filepath.Join(reportsDir, "attempts", entry.Name(), "index.html"))
		if err != nil {
			t.Fatalf("restored stage report for %s: %v", entry.Name(), err)
		}
		for _, artifact := range map[string][]string{
			"analyst":  {"task.md", "proposal.md"},
			"reviewer": {"proposal.md", "review.md"},
			"deployer": {"review.md"},
		}[attempt.Stage] {
			if !strings.Contains(string(stageHTML), artifact) {
				t.Fatalf("restored report for %s is missing manifest artifact %s", attempt.Stage, artifact)
			}
		}
		if !strings.Contains(string(stageHTML), fmt.Sprintf("Stage:</strong> %d/3", attempt.StageIndex)) {
			t.Fatalf("restored report for %s should preserve stage count n/3:\n%s", entry.Name(), stageHTML)
		}
		finalHTML, err := os.ReadFile(filepath.Join(reportsDir, "index.html"))
		if err != nil {
			t.Fatalf("final report: %v", err)
		}
		if !strings.Contains(string(finalHTML), entry.Name()) {
			t.Fatalf("final report does not link restored attempt %s", entry.Name())
		}
		inputTotal += attempt.Usage.TokensInput
		outputTotal += attempt.Usage.TokensOutput
		costTotal += attempt.Usage.CostUSD
	}
	if inputTotal != envelope.TokensInput || outputTotal != envelope.TokensOutput || costTotal < envelope.CostUSD-0.000001 || costTotal > envelope.CostUSD+0.000001 {
		t.Fatalf("manifest sums do not equal run envelope: attempts=%d/%d/%.4f envelope=%d/%d/%.4f", inputTotal, outputTotal, costTotal, envelope.TokensInput, envelope.TokensOutput, envelope.CostUSD)
	}
	events, err := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var decisionEvents []time.Time
	for _, event := range events {
		if event.Type == "approval_decided" {
			decisionEvents = append(decisionEvents, event.Timestamp)
		}
	}
	if len(decisionEvents) != 2 || !decisionEvents[0].Equal(firstDecisionAt) || !decisionEvents[1].Equal(secondDecisionAt) {
		t.Fatalf("approval event times must match human decisions, got %v want %v, %v", decisionEvents, firstDecisionAt, secondDecisionAt)
	}
}
