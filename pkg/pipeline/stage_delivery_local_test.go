package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// localStageDeliveryFixture prepares a Go repository with a local bare origin,
// a gh stub reporting prURL for the hard PR checks, and a single agent
// implementation stage that the in-process controller delivers before review.
func localStageDeliveryFixture(t *testing.T, prURL string) (string, *config.Config, *agent.Registry, *scriptedRuntime) {
	t.Helper()
	dir := env(t)
	gitInit(t, dir)
	for name, content := range map[string]string{
		".gitignore":     ".ai-team/\n",
		"go.mod":         "module example.test/bl20\n\ngo 1.26\n",
		"answer.go":      "package bl20\nfunc answer() int { return 42 }\n",
		"answer_test.go": "package bl20\nimport \"testing\"\nfunc TestAnswer(t *testing.T) { if answer() != 42 { t.Fatal(\"wrong answer\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	for _, args := range [][]string{
		{"add", "."}, {"commit", "-qm", "init"}, {"branch", "-M", "main"},
		{"init", "--bare", "--initial-branch=main", remote}, {"remote", "add", "origin", remote}, {"push", "-u", "origin", "main"},
	} {
		command := exec.Command("git", args...)
		if args[0] != "init" {
			command.Dir = dir
		}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	bin := t.TempDir()
	ghOutput := `{"url":"` + prURL + `","state":"OPEN","baseRefName":"main","headRefName":"ai-team/feat"}`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' '"+ghOutput+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	check := checks.Definition{Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest,
		Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: checks.PolicyRequired}
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion, Template: "bl20-local-stage-delivery", Title: "Local stage delivery",
		CLI: "opencode", Checks: []checks.Definition{check}, PipelineAgents: []config.AgentConfig{{Name: "implementation"}},
		Stages: []config.TemplateStage{{ID: "implementation", Title: "Implementation", Function: "developer",
			Result: "link", LinkKind: "pr", Executor: "agent", Agent: "coder", Confirm: "auto",
			Delivery: &config.TemplateDelivery{RequireChecks: []string{"go-test"}},
			Check:    &config.TemplateCheck{Kind: "hard", Rules: []string{"pr_exists", "pr_open", "pr_base_branch"}}}},
	}
	registry := agent.NewFS(fstest.MapFS{
		"coder/def.yaml":  def("name: coder\nruntime: agentcli\nprompt_file: prompt.md\nmutation: source\nallowed_paths: ['**']\nrequire_diff: true\ntest_modify_policy: off\noutputs: {}\n"),
		"coder/prompt.md": def("implement"),
	})
	rt := newScripted()
	rt.onExecute = func(agentName string, task *runtime.Task, _ []runtime.Artifact) {
		if agentName == "coder" {
			if err := os.WriteFile(filepath.Join(task.TargetDir, "change.go"), []byte("package bl20\nfunc changed() bool { return true }\n"), 0644); err != nil {
				t.Errorf("coder mutation: %v", err)
			}
		}
	}
	return dir, cfg, registry, rt
}

// hidingDeliveryApprovals hides resolved delivery-plan approvals from List
// while leaving Load intact, simulating an approval record that no longer
// validates for the run (the in-run authorization path only uses Load).
type hidingDeliveryApprovals struct{ ApprovalStore }

func (s hidingDeliveryApprovals) List(runID string) ([]approval.PendingApproval, error) {
	values, err := s.ApprovalStore.List(runID)
	if err != nil {
		return nil, err
	}
	kept := values[:0]
	for _, value := range values {
		if value.Trigger == "delivery_plan" && value.Status == approval.StatusResolved {
			continue
		}
		kept = append(kept, value)
	}
	return kept, nil
}

// BL-20: the in-process controller stage delivery validates the resolved
// approval with ValidateResolvedDeliveryOperationApproval, like the
// worker-facing controller path, and performs no delivery without it.
func TestLocalStageDeliveryRequiresResolvedApprovalBeforeSideEffects(t *testing.T) {
	dir, cfg, registry, rt := localStageDeliveryFixture(t, "https://example.test/org/repo/pull/1")
	local, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	service := &pullRequestDeliveryService{}
	p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithDeliveryService(service),
		WithApprovalStore(hidingDeliveryApprovals{local}),
		WithPrompter(&scriptedPrompter{interactive: true, answers: []string{"y"}}))
	_, runErr := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "local approval", TargetDir: dir, ApproveGates: true})
	if runErr == nil || !strings.Contains(runErr.Error(), "stage delivery approval") {
		t.Fatalf("local stage delivery must refuse an approval that does not validate: %v", runErr)
	}
	if service.calls != 0 {
		t.Fatalf("delivery ran without a validated approval: calls=%d", service.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ai-team", "state", "stage-delivery-receipts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a stage receipt was written without a validated approval: %v", err)
	}
}

// withAcceptanceStage appends a human approval stage after implementation so a
// run stops after the stage delivery and can be resumed at a later stage.
func withAcceptanceStage(cfg *config.Config) *config.Config {
	cfg.Stages = append(cfg.Stages, config.TemplateStage{ID: "acceptance", Title: "Acceptance", Function: "bo", Result: "approve", Executor: "human"})
	return cfg
}

// stopAfterLocalStageDelivery runs implementation (delivered in-process) and
// approves the pending acceptance gate, returning the run to resume.
func stopAfterLocalStageDelivery(t *testing.T, dir string, cfg *config.Config, registry *agent.Registry, rt *scriptedRuntime, service delivery.Service) string {
	t.Helper()
	p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithDeliveryService(service),
		WithPrompter(&scriptedPrompter{interactive: true, answers: []string{"y"}}))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "resume after stage delivery", TargetDir: dir, ApproveGates: true})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("first run must stop at acceptance after stage delivery: result=%+v err=%v", first, err)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || len(pending.RequiredRoles) == 0 {
		t.Fatalf("pending approval=%+v err=%v", pending, err)
	}
	if _, err := store.Decide(first.RunID, required.ApprovalID, approval.Decision{
		ActorID: "bo-1", ActorRole: pending.RequiredRoles[0], Action: "approve", SubjectHash: required.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	return first.RunID
}

// BL-20 review: after resume no --approve-plan is given and the in-process
// authorization is gone. The stage receipt is revalidated against the resolved
// file-store approval named by the delivery_plan_approved event; nothing is
// delivered again.
func TestLocalStageDeliveryResumeRevalidatesWithEventApproval(t *testing.T) {
	dir, cfg, registry, rt := localStageDeliveryFixture(t, "https://example.test/org/repo/pull/1")
	cfg = withAcceptanceStage(cfg)
	service := &pullRequestDeliveryService{}
	runID := stopAfterLocalStageDelivery(t, dir, cfg, registry, rt, service)
	resumed := New(cfg, registry, WithRuntimeFactory(rt.factory), WithDeliveryService(service), WithPrompter(&scriptedPrompter{}))
	result, err := resumed.RunWithResult(context.Background(), RunConfig{ResumeRunID: runID, TargetDir: dir})
	if err != nil || result.Outcome != workflow.RunCompleted {
		t.Fatalf("resume must revalidate the stage receipt from evidence: result=%+v err=%v", result, err)
	}
	if service.calls != 1 || rt.calls["coder"] != 1 {
		t.Fatalf("resume must not deliver or implement again: deliveries=%d coder=%d", service.calls, rt.calls["coder"])
	}
}

// BL-20 review: an operator-supplied plan hash that differs from the approved
// plan is not overridden by the event; revalidation fails without delivery.
func TestLocalStageDeliveryResumeRejectsForeignApprovePlanHash(t *testing.T) {
	dir, cfg, registry, rt := localStageDeliveryFixture(t, "https://example.test/org/repo/pull/1")
	cfg = withAcceptanceStage(cfg)
	service := &pullRequestDeliveryService{}
	runID := stopAfterLocalStageDelivery(t, dir, cfg, registry, rt, service)
	foreign := strings.Repeat("f", 64)
	resumed := New(cfg, registry, WithRuntimeFactory(rt.factory), WithDeliveryService(service),
		WithDeliveryApprovalHash(foreign), WithPrompter(&scriptedPrompter{}))
	_, err := resumed.RunWithResult(context.Background(), RunConfig{ResumeRunID: runID, TargetDir: dir})
	if err == nil || !strings.Contains(err.Error(), "stage delivery approval") {
		t.Fatalf("a foreign --approve-plan hash must fail stage delivery revalidation: %v", err)
	}
	if service.calls != 1 {
		t.Fatalf("rejected revalidation must not deliver: deliveries=%d", service.calls)
	}
}
