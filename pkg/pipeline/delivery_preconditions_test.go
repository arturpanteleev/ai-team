package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
)

func deliveryPreconditionsRegistry() *agent.Registry {
	return agent.NewFS(fstest.MapFS{
		"coder/def.yaml": def(`name: coder
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs: {}
`),
		"deployer/def.yaml": def(`name: deployer
runtime: delivery
kind: delivery
mutation: external
outputs:
  plan: '{feature}/delivery-plan.json'
`),
		"reviewer/def.yaml": def(`name: reviewer
runtime: agentcli
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED]
outputs:
  review: '{feature}/review.md'
`),
		"coder/prompt.md":    def("test"),
		"reviewer/prompt.md": def("test"),
	})
}

func deliveryPreconditionsConfig(checksSuite []checks.Definition, requireVerdicts []string) *config.Config {
	return &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "delivery-preconditions-test",
		Title:         "Delivery preconditions test",
		PipelineAgents: []config.AgentConfig{
			{Name: "coder"}, {Name: "deployer"}, {Name: "reviewer"},
		},
		Stages: []config.TemplateStage{
			{ID: "coder", Title: "Implementation", Function: "developer", Result: "link", LinkKind: "pr", Executor: "agent", Agent: "coder", Confirm: "auto", Delivery: &config.TemplateDelivery{RequireChecks: []string{"project-vet"}, RequireVerdicts: append([]string(nil), requireVerdicts...)}},
			{ID: "deployer", Title: "Delivery", Function: "deployer", Result: "link", LinkKind: "pr", Executor: "agent", Agent: "deployer", Confirm: "auto"},
		},
		Checks: checksSuite,
		CLI:    "opencode",
	}
}

func TestRun_DeliveryPlanUsesConfiguredChecksWithoutReviewOrQAAgents(t *testing.T) {
	dir := env(t)
	planHash := prepareCheckOnlyDelivery(t, dir, true)
	checkDefinition := checks.Definition{
		Name: "project-vet", Class: "lint", Command: []string{"go", "vet", "./..."}, Policy: checks.PolicyRequired,
	}
	config := deliveryPreconditionsConfig([]checks.Definition{checkDefinition}, nil)
	rt := newScripted()
	service := &fakeDeliveryService{}
	notifications := &captureNotifier{}
	p := New(config, deliveryPreconditionsRegistry(), WithRuntimeFactory(rt.factory),
		WithDeliveryService(service), WithNotifier(notifications), WithDeliveryApprovalHash(planHash))
	if err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: planHash,
	}); err != nil {
		t.Fatalf("delivery should reach the plan without review or QA stages: %v", err)
	}
	if strings.Join(rt.executed, ",") != "coder" {
		t.Fatalf("expected only implementation agent runtime, got %v", rt.executed)
	}
	if service.calls != 1 || len(notifications.calls) != 2 || notifications.calls[1].Status != notifier.StatusPassed {
		t.Fatalf("delivery did not pass through the controller plan: service=%d stages=%+v", service.calls, notifications.calls)
	}
	runDir := onlyRunDir(t, dir)
	_, manifest, err := evidence.ReadAttemptManifest(nil, runDir, filepath.Base(runDir), notifications.calls[0].AttemptID)
	if err != nil || len(manifest.Checks) != 1 || manifest.Checks[0].Name != "project-vet" || manifest.Checks[0].Status != checks.StatusPassed {
		t.Fatalf("implementation must persist controller check evidence: manifest=%+v err=%v", manifest, err)
	}
}

func TestRun_MissingRequiredDeliveryCheckBlocksBeforePlanSideEffects(t *testing.T) {
	dir := env(t)
	_ = prepareCheckOnlyDelivery(t, dir, false)
	config := deliveryPreconditionsConfig(nil, nil)
	rt := newScripted()
	service := &fakeDeliveryService{}
	notifications := &captureNotifier{}
	p := New(config, deliveryPreconditionsRegistry(), WithRuntimeFactory(rt.factory),
		WithDeliveryService(service), WithNotifier(notifications))
	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true})
	if err == nil || !strings.Contains(err.Error(), "project-vet") {
		t.Fatalf("missing controller check should block delivery, got %v", err)
	}
	if strings.Join(rt.executed, ",") != "coder" || service.calls != 0 {
		t.Fatalf("missing check reached delivery side effects: runtime=%v service=%d", rt.executed, service.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", "delivery-plan.json")); !os.IsNotExist(err) {
		t.Fatalf("delivery plan was written despite missing check: %v", err)
	}
	if _, found, err := delivery.LoadPreparedPlan(dir, "feat"); err != nil || found {
		t.Fatalf("delivery state was created despite missing check: found=%v err=%v", found, err)
	}
}

func TestRun_FailedRequiredDeliveryCheckBlocksBeforePlanSideEffects(t *testing.T) {
	dir := env(t)
	_ = prepareCheckOnlyDelivery(t, dir, false)
	checkDefinition := checks.Definition{
		Name: "project-vet", Class: "lint", Command: []string{"missing-ai-team-check-command"}, Policy: checks.PolicyRequired,
	}
	config := deliveryPreconditionsConfig([]checks.Definition{checkDefinition}, nil)
	rt := newScripted()
	service := &fakeDeliveryService{}
	notifications := &captureNotifier{}
	p := New(config, deliveryPreconditionsRegistry(), WithRuntimeFactory(rt.factory),
		WithDeliveryService(service), WithNotifier(notifications))
	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true})
	var requiredFailure *checks.RequiredFailureError
	if !errors.As(err, &requiredFailure) {
		t.Fatalf("failed required controller check should stop the run, got %v", err)
	}
	if strings.Join(rt.executed, ",") != "coder" || service.calls != 0 {
		t.Fatalf("failed check reached delivery side effects: runtime=%v service=%d", rt.executed, service.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", "delivery-plan.json")); !os.IsNotExist(err) {
		t.Fatalf("delivery plan was written after failed check: %v", err)
	}
}

func TestDeliveryRequiredVerdictsUseImmutableContractSnapshots(t *testing.T) {
	for _, test := range []struct {
		name          string
		includeReview bool
		status        string
		marker        string
		wantErr       bool
	}{
		{name: "positive configured verdict", includeReview: true, status: notifier.StatusPassed, marker: "APPROVED"},
		{name: "missing configured verdict", wantErr: true},
		{name: "negative configured verdict", includeReview: true, status: notifier.StatusRejected, marker: "CHANGES_REQUESTED", wantErr: true},
		{name: "negative immutable marker despite passed status", includeReview: true, status: notifier.StatusPassed, marker: "CHANGES_REQUESTED", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rs := deliveryVerdictRunState(t, test.includeReview, test.status, test.marker)
			if test.name == "positive configured verdict" {
				mutableOutput := filepath.Join(rs.runCfg.TargetDir, ".ai-team", "artifacts", "feat", "review.md")
				if err := os.WriteFile(mutableOutput, []byte("**Verdict:** CHANGES_REQUESTED\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			err := rs.validateDeliveryChecks()
			if test.wantErr {
				if err == nil {
					t.Fatal("missing or negative immutable verdict must block delivery")
				}
				if _, statErr := os.Stat(filepath.Join(rs.runCfg.TargetDir, ".ai-team", "artifacts", "feat", "delivery-plan.json")); !os.IsNotExist(statErr) {
					t.Fatalf("plan output exists despite rejected verdict: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("positive immutable verdict should satisfy the configured gate: %v", err)
			}
			var deliveryStage config.TemplateStage
			for _, stage := range rs.p.cfg.Stages {
				if stage.Delivery != nil {
					deliveryStage = stage
					break
				}
			}
			evidenceSet, err := rs.requiredDeliveryVerdictEvidence(deliveryStage)
			if err != nil || len(evidenceSet) != 1 {
				t.Fatalf("positive verdict snapshot evidence: set=%v err=%v", evidenceSet, err)
			}
			if value := evidenceSet["verdict:review:review"]; value.Type != "file" || value.Verdict != string(verdict.Approved) || value.SHA256 == "" {
				t.Fatalf("plan precondition did not bind immutable verdict output: %+v", value)
			}
		})
	}
}

func prepareCheckOnlyDelivery(t *testing.T, dir string, includePlan bool) string {
	t.Helper()
	_ = prepareDelivery(t, dir)
	plan, found, err := delivery.LoadPreparedPlan(dir, "feat")
	if err != nil || !found {
		t.Fatalf("load prepared plan: found=%v err=%v", found, err)
	}
	plan.Preconditions = nil
	statePath := filepath.Join(dir, ".ai-team", "delivery", "feat.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if !includePlan {
		return ""
	}
	if _, err := delivery.Prepare(dir, "feat", plan); err != nil {
		t.Fatalf("prepare check-only delivery: %v", err)
	}
	hash, err := plan.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func deliveryVerdictRunState(t *testing.T, includeReview bool, reviewStatus, reviewMarker string) *runState {
	t.Helper()
	dir := env(t)
	check, err := (checks.Runner{TargetDir: dir}).Run(context.Background(), checks.Definition{
		Name: "project-vet", Class: "lint", Command: []string{"go", "version"}, Policy: checks.PolicyRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	const runID = "delivery-verdict-run"
	store, err := evidence.Start(filepath.Join(dir, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "feat", TargetDir: dir, StartedAt: time.Now().UTC(),
		ConfigSnapshot: json.RawMessage(`{"schema_version":5}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":5,"stages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	implementation := notifier.StageResult{
		RunID: runID, AttemptID: "implementation-attempt", Name: "implementation", Status: notifier.StatusPassed,
		StartedAt: time.Now().UTC().Add(-time.Minute), FinishedAt: time.Now().UTC(),
	}
	if err := store.PublishAttempt(evidence.AttemptManifest{
		AttemptID: implementation.AttemptID, Stage: implementation.Name, Status: implementation.Status,
		StartedAt: implementation.StartedAt, FinishedAt: implementation.FinishedAt, Checks: []checks.Result{check},
	}, filepath.Join(dir, ".ai-team", "artifacts"), nil, nil); err != nil {
		t.Fatal(err)
	}
	results := []notifier.StageResult{implementation}
	if includeReview {
		reviewPath := filepath.Join(dir, ".ai-team", "artifacts", "feat", "review.md")
		if err := os.MkdirAll(filepath.Dir(reviewPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reviewPath, []byte("**Verdict:** "+reviewMarker+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		review := notifier.StageResult{
			RunID: runID, AttemptID: "review-attempt", Name: "review", Status: reviewStatus,
			Verdict: verdict.Verdict(reviewMarker), StartedAt: time.Now().UTC().Add(-time.Minute), FinishedAt: time.Now().UTC(),
		}
		if err := store.PublishAttempt(evidence.AttemptManifest{
			AttemptID: review.AttemptID, Stage: review.Name, Status: review.Status, Verdict: string(review.Verdict),
			StartedAt: review.StartedAt, FinishedAt: review.FinishedAt,
		}, filepath.Join(dir, ".ai-team", "artifacts"), nil, []evidence.Artifact{{Name: "review", Path: reviewPath}}); err != nil {
			t.Fatal(err)
		}
		results = append(results, review)
	}
	config := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion, Template: "delivery-verdict-test", Title: "Delivery verdict test",
		PipelineAgents: []config.AgentConfig{{Name: "implementation"}, {Name: "reviewer"}, {Name: "deployer"}},
		Stages: []config.TemplateStage{
			{ID: "review", Title: "Review", Function: "reviewer", Result: "md", Executor: "agent", Agent: "reviewer"},
			{ID: "implementation", Title: "Implementation", Function: "developer", Result: "link", LinkKind: "pr", Executor: "agent", Agent: "coder", Delivery: &config.TemplateDelivery{RequireChecks: []string{"project-vet"}, RequireVerdicts: []string{"review"}}},
			{ID: "deployer", Title: "Delivery", Function: "deployer", Result: "link", LinkKind: "pr", Executor: "agent", Agent: "deployer"},
		},
	}
	p := &Pipeline{cfg: config, reg: deliveryPreconditionsRegistry()}
	return &runState{
		p: p, runCfg: RunConfig{Feature: "feat", TargetDir: dir}, runID: runID,
		evidence: store, results: results,
	}
}

var _ runtime.Runtime = (*scriptedRuntime)(nil)
