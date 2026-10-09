package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

type cliDeliveryRuntime struct{}

func (cliDeliveryRuntime) Execute(_ context.Context, a *runtime.Agent, task *runtime.Task, _ []runtime.Artifact) error {
	for name, output := range a.Outputs {
		path := filepath.Join(task.ArtifactRoot, runtime.ReplaceVars(output, task.Feature))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		content := "ok\n"
		if name == "review" {
			content = "**Verdict:** APPROVED\n"
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

type cliDeliveryPrompter struct{}

func (cliDeliveryPrompter) Interactive() bool { return false }
func (cliDeliveryPrompter) Ask(string) string { return "n" }

type cliFailDelivery struct{}

func (cliFailDelivery) Execute(context.Context, delivery.Request) (delivery.Result, error) {
	return delivery.Result{}, fmt.Errorf("simulated post-terminal delivery failure")
}

type cliDeadlineDelivery struct{ remaining time.Duration }

func (d *cliDeadlineDelivery) Execute(ctx context.Context, request delivery.Request) (delivery.Result, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return delivery.Result{}, fmt.Errorf("delivery context has no deadline")
	}
	d.remaining = time.Until(deadline)
	hash, err := request.Plan.Hash()
	if err != nil {
		return delivery.Result{}, err
	}
	return delivery.Result{PlanHash: hash, CommitSHA: "cccccccccccccccccccccccccccccccccccccccc", PRURL: "https://example.test/pr/9"}, nil
}

func cliDeliveryTemplate(timeout string) *config.Config {
	return &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		Template:        "cli-pinned-delivery-timeout",
		Title:           "CLI pinned delivery timeout",
		DeliveryTimeout: timeout,
		Checks: []checks.Definition{{
			Name: "project-vet", Class: "lint", Command: []string{"go", "version"}, Policy: checks.PolicyRequired,
		}},
		Stages: []config.TemplateStage{
			{ID: "approver", Title: "Approve", Function: "operator", Result: "approve", Executor: "agent", Agent: "approver", Confirm: "auto"},
			{ID: "implementation", Title: "Implement", Function: "developer", Result: "link", LinkKind: "pr", Executor: "agent", Agent: "implementation", Confirm: "auto",
				Delivery: &config.TemplateDelivery{RequireChecks: []string{"project-vet"}, RequireVerdicts: []string{"approver"}}},
			{ID: "deployer", Title: "Deliver", Function: "deployer", Result: "link", LinkKind: "pr", Executor: "agent", Agent: "deployer", Confirm: "auto"},
		},
	}
}

func writeProjectApproverAgent(t *testing.T, target string) {
	t.Helper()
	agentDir := filepath.Join(target, ".ai-team", "agents", "approver")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	definition := []byte(`name: approver
description: Test project approver
runtime: agentcli
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED]
inputs:
  task: tasks/{feature}/task.md
outputs:
  review: '{feature}/review.md'
`)
	if err := os.WriteFile(filepath.Join(agentDir, "def.yaml"), definition, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "prompt.md"), []byte("test prompt"), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeProjectImplementationAgent(t *testing.T, target string) {
	t.Helper()
	agentDir := filepath.Join(target, ".ai-team", "agents", "implementation")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	definition := []byte(`name: implementation
description: Test project implementation stage
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs: {}
`)
	if err := os.WriteFile(filepath.Join(agentDir, "def.yaml"), definition, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "prompt.md"), []byte("test prompt"), 0644); err != nil {
		t.Fatal(err)
	}
}

func cliDeliveryRegistry() *agent.Registry {
	return agent.NewFS(fstest.MapFS{
		"approver/def.yaml": &fstest.MapFile{Data: []byte(`name: approver
runtime: agentcli
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED]
inputs:
  task: tasks/{feature}/task.md
outputs:
  review: '{feature}/review.md'
`)},
		"approver/prompt.md": &fstest.MapFile{Data: []byte("test prompt")},
		"implementation/def.yaml": &fstest.MapFile{Data: []byte(`name: implementation
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs: {}
`)},
		"implementation/prompt.md": &fstest.MapFile{Data: []byte("test prompt")},
		"deployer/def.yaml": &fstest.MapFile{Data: []byte(`name: deployer
runtime: delivery
kind: delivery
mutation: external
inputs:
  review: '{feature}/review.md'
preconditions:
  review:
    required: true
    marker: Verdict
    values: [APPROVED]
outputs:
  plan: '{feature}/delivery-plan.json'
`)},
	})
}

func prepareCLIDelivery(t *testing.T, target string) string {
	t.Helper()
	change := []byte("package change\n")
	for path, data := range map[string][]byte{
		"change.go":      change,
		"change_test.go": []byte("package change\nimport \"testing\"\nfunc TestPrepared(t *testing.T) {}\n"),
		"go.mod":         []byte("module example.test/prepared\n\ngo 1.26\n"),
	} {
		if err := os.WriteFile(filepath.Join(target, path), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	check, err := (checks.Runner{TargetDir: target}).Run(context.Background(), checks.Definition{
		Name: "prepared-test", Class: "unit", Adapter: checks.AdapterGoTest,
		Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: checks.PolicyRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := evidence.Start(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: "prepared-run", ConfigSnapshot: json.RawMessage(`{"schema_version":1}`),
		WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.PublishAttempt(evidence.AttemptManifest{
		AttemptID: "prepared-run-001-check", Stage: "check", Status: notifier.StatusPassed,
		Checks: []checks.Result{check},
	}, filepath.Join(target, ".ai-team", "artifacts"), nil, nil); err != nil {
		t.Fatal(err)
	}
	fileDigest := sha256.Sum256(change)
	reviewDigest := sha256.Sum256([]byte("**Verdict:** APPROVED\n"))
	plan := delivery.Plan{
		SchemaVersion: delivery.SchemaVersion, Branch: "ai-team/feat", BaseBranch: "main", Remote: "origin",
		RemoteURL: "https://example.test/repo.git", Files: []string{"change.go"},
		FileDigests: map[string]string{"change.go": fmt.Sprintf("%x", fileDigest)},
		FileModes:   map[string]string{"change.go": "100644"}, BaselineHead: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SourceRunID: "prepared-run", VerifiedWorkspaceDigest: check.WorkspaceDigestAfter,
		CheckEvidenceDigest: check.EvidenceDigest,
		Preconditions: map[string]delivery.PreconditionEvidence{
			"review":                  {Type: "file", Size: 22, SHA256: fmt.Sprintf("%x", reviewDigest), Verdict: "APPROVED"},
			"verdict:approver:review": {Type: "file", Size: 22, SHA256: fmt.Sprintf("%x", reviewDigest), Verdict: "APPROVED"},
		},
		CommitMessage: "feat change", PRTitle: "feat change", PRBody: "test delivery plan",
	}
	if _, err := delivery.Prepare(target, "feat", plan); err != nil {
		t.Fatal(err)
	}
	hash, err := plan.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestDeliverCommandUsesPinnedDeliveryTimeoutAfterPublish(t *testing.T) {
	target := t.TempDir()
	writeProjectApproverAgent(t, target)
	writeProjectImplementationAgent(t, target)
	artifacts := filepath.Join(target, ".ai-team", "artifacts")
	if err := os.MkdirAll(filepath.Join(artifacts, "tasks", "feat"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "tasks", "feat", "task.md"), []byte("test task"), 0644); err != nil {
		t.Fatal(err)
	}
	planHash := prepareCLIDelivery(t, target)
	initial := cliDeliveryTemplate("4m")
	initialYAML, err := initial.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), initialYAML, 0644); err != nil {
		t.Fatal(err)
	}
	runtimeInstance := cliDeliveryRuntime{}
	startEngine := pipeline.NewRunEngine(pipeline.New(initial, cliDeliveryRegistry(),
		pipeline.WithRuntimeFactory(func(string) (runtime.Runtime, error) { return runtimeInstance, nil }),
		pipeline.WithPrompter(cliDeliveryPrompter{}), pipeline.WithDeliveryService(cliFailDelivery{})))
	const runID = "cli-pinned-timeout"
	if _, err := startEngine.Start(context.Background(), pipeline.RunConfig{
		RunID: runID, Feature: "feat", TaskDesc: "CLI pinned timeout", TargetDir: target,
		ApproveGates: true, ApprovePlanHash: planHash,
	}); err == nil {
		t.Fatal("simulated post-terminal failure should leave a retryable delivery")
	}

	templateStore, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	updated := *initial
	updated.DeliveryTimeout = "30m"
	updatedYAML, err := updated.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templateStore.Publish(updatedYAML, config.TemplateVersionID(initialYAML)); err != nil {
		t.Fatal(err)
	}

	probe := &cliDeadlineDelivery{}
	if _, err := deliverDeferredCLI(context.Background(), runID, "", target,
		pipeline.WithDeliveryService(probe), pipeline.WithDeliveryApprovalHash(planHash)); err != nil {
		t.Fatalf("CLI deferred retry: %v", err)
	}
	if probe.remaining <= 3*time.Minute || probe.remaining > 4*time.Minute {
		t.Fatalf("CLI retry used %v delivery budget, want the pinned 4m template rather than the active 30m template", probe.remaining)
	}
}
