package pipeline

import (
	"context"
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
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

type continuationFixture struct {
	dir       string
	cfg       *config.Config
	registry  *agent.Registry
	rt        *scriptedRuntime
	approvals *approval.Store
	runID     string
	pending   approval.PendingApproval
	workspace string
}

// newAwaitingPlanFixture runs one agent PR stage non-interactively until it
// stops in awaiting_plan for its exact delivery plan.
func newAwaitingPlanFixture(t *testing.T) continuationFixture {
	t.Helper()
	dir := env(t)
	gitInit(t, dir)
	for name, content := range map[string]string{
		".gitignore":     ".ai-team/\n",
		"go.mod":         "module example.test/b35resume\n\ngo 1.26\n",
		"answer.go":      "package b35resume\nfunc answer() int { return 42 }\n",
		"answer_test.go": "package b35resume\nimport \"testing\"\nfunc TestAnswer(t *testing.T) { if answer() != 42 { t.Fatal(\"wrong answer\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	for _, step := range []struct {
		dir  string
		args []string
	}{
		{dir, []string{"add", "."}}, {dir, []string{"commit", "-qm", "init"}}, {dir, []string{"branch", "-M", "master"}},
		{"", []string{"init", "--bare", "--initial-branch=master", remote}},
		{dir, []string{"remote", "add", "origin", remote}}, {dir, []string{"push", "-u", "origin", "master"}},
	} {
		command := exec.Command("git", step.args...)
		command.Dir = step.dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", step.args, err, output)
		}
	}
	bin := t.TempDir()
	ghOutput := `{"url":"https://example.test/org/repo/pull/1","state":"OPEN","baseRefName":"master","headRefName":"ai-team/feat"}`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' '"+ghOutput+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	check := checks.Definition{Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest,
		Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: checks.PolicyRequired}
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion, Template: "b35-resume", Title: "Resume awaiting plan",
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
			if err := os.WriteFile(filepath.Join(task.TargetDir, "change.go"), []byte("package b35resume\nfunc changed() bool { return true }\n"), 0644); err != nil {
				t.Errorf("coder mutation: %v", err)
			}
		}
	}
	approvals, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithApprovalStore(approvals),
		WithDeliveryService(&pullRequestDeliveryService{}), WithPrompter(&scriptedPrompter{interactive: false}))
	first, runErr := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "resume awaiting plan", TargetDir: dir})
	if runErr == nil || first.RunID == "" {
		t.Fatalf("non-interactive run must stop for delivery approval: result=%+v err=%v", first, runErr)
	}
	values, err := approvals.List(first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var pending approval.PendingApproval
	for _, value := range values {
		if value.Trigger == "delivery_plan" && value.FromStage == "implementation" {
			pending = value
		}
	}
	if pending.ID == "" || pending.Status != approval.StatusPending {
		t.Fatalf("delivery plan approval is not pending: %+v", values)
	}
	workspace := filepath.Join(dir, ".ai-team", "worktrees", first.RunID)
	if _, err := os.Stat(filepath.Join(workspace, "change.go")); err != nil {
		t.Fatalf("candidate workspace lacks the coder change: %v", err)
	}
	return continuationFixture{dir: dir, cfg: cfg, registry: registry, rt: rt, approvals: approvals,
		runID: first.RunID, pending: pending, workspace: workspace}
}

func (f continuationFixture) state(store ApprovalStore, results ...notifier.StageResult) *runState {
	if results == nil {
		results = []notifier.StageResult{{Name: "implementation", AttemptID: f.pending.AttemptID, ControlStopped: true}}
	}
	return &runState{
		p:     New(f.cfg, f.registry),
		runID: f.runID, results: results, approvalStore: store,
		runCfg: RunConfig{Feature: "feat", TargetDir: f.dir}, sourceTarget: f.workspace,
	}
}

func coderAgent(t *testing.T, registry *agent.Registry) *agent.Agent {
	t.Helper()
	a, err := registry.Load("coder")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type overrideApprovalStore struct {
	ApprovalStore
	mutate func(*approval.PendingApproval)
}

func (s overrideApprovalStore) List(runID string) ([]approval.PendingApproval, error) {
	values, err := s.ApprovalStore.List(runID)
	for index := range values {
		s.mutate(&values[index])
	}
	return values, err
}

func TestDeliveryApprovalContinuationRequiresExactPendingPlan(t *testing.T) {
	f := newAwaitingPlanFixture(t)
	coder := coderAgent(t, f.registry)
	if ok, err := f.state(f.approvals).deliveryApprovalContinuation("implementation", coder); err != nil || !ok {
		t.Fatalf("pending exact plan must continue without a model run: ok=%t err=%v", ok, err)
	}

	t.Run("rejected decision", func(t *testing.T) {
		store := overrideApprovalStore{ApprovalStore: f.approvals, mutate: func(value *approval.PendingApproval) {
			value.Status, value.ResolvedAction = approval.StatusResolved, "reject"
		}}
		if ok, err := f.state(store).deliveryApprovalContinuation("implementation", coder); ok || err != nil {
			t.Fatalf("rejected plan must not continue: ok=%t err=%v", ok, err)
		}
	})
	t.Run("approval from another attempt", func(t *testing.T) {
		other := []notifier.StageResult{{Name: "implementation", AttemptID: f.pending.AttemptID + "-other", ControlStopped: true}}
		if ok, err := f.state(f.approvals, other...).deliveryApprovalContinuation("implementation", coder); ok || err != nil {
			t.Fatalf("approval of another attempt must not continue: ok=%t err=%v", ok, err)
		}
	})
	t.Run("approval for another stage", func(t *testing.T) {
		store := overrideApprovalStore{ApprovalStore: f.approvals, mutate: func(value *approval.PendingApproval) {
			value.FromStage, value.ToStage = "code_review", "code_review"
		}}
		if ok, err := f.state(store).deliveryApprovalContinuation("implementation", coder); ok || err != nil {
			t.Fatalf("approval of another stage must not continue: ok=%t err=%v", ok, err)
		}
	})
	t.Run("approval for another plan", func(t *testing.T) {
		store := overrideApprovalStore{ApprovalStore: f.approvals, mutate: func(value *approval.PendingApproval) {
			value.SubjectHash = strings.Repeat("0", 64)
		}}
		if ok, err := f.state(store).deliveryApprovalContinuation("implementation", coder); ok || err != nil {
			t.Fatalf("approval of another plan must not continue: ok=%t err=%v", ok, err)
		}
	})
	t.Run("stage passed after the stop", func(t *testing.T) {
		passed := []notifier.StageResult{
			{Name: "implementation", AttemptID: f.pending.AttemptID, ControlStopped: true},
			{Name: "implementation", AttemptID: f.pending.AttemptID + "-next", Status: notifier.StatusPassed},
		}
		if ok, err := f.state(f.approvals, passed...).deliveryApprovalContinuation("implementation", coder); ok || err != nil {
			t.Fatalf("only the latest stopped attempt may continue: ok=%t err=%v", ok, err)
		}
	})
	t.Run("workspace changed", func(t *testing.T) {
		path := filepath.Join(f.workspace, "change.go")
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(path, original, 0644) })
		if err := os.WriteFile(path, append(original, []byte("// edited after approval request\n")...), 0644); err != nil {
			t.Fatal(err)
		}
		if ok, err := f.state(f.approvals).deliveryApprovalContinuation("implementation", coder); ok || err != nil {
			t.Fatalf("changed candidate bytes must not continue: ok=%t err=%v", ok, err)
		}
	})
	t.Run("prepared plan replaced on disk", func(t *testing.T) {
		statePath := filepath.Join(f.workspace, ".ai-team", "delivery", "feat.json")
		original, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(statePath, original, 0600) })
		tampered := strings.Replace(string(original), "resume awaiting plan", "resume tampered plan", 1)
		if tampered == string(original) {
			t.Fatalf("prepared state does not contain the plan text:\n%s", original)
		}
		if err := os.WriteFile(statePath, []byte(tampered), 0600); err != nil {
			t.Fatal(err)
		}
		if ok, _ := f.state(f.approvals).deliveryApprovalContinuation("implementation", coder); ok {
			t.Fatal("a replaced prepared plan must not continue")
		}
	})
	if ok, err := f.state(f.approvals).deliveryApprovalContinuation("implementation", coder); err != nil || !ok {
		t.Fatalf("restored fixture must continue again: ok=%t err=%v", ok, err)
	}
}

func TestResumeAwaitingPlanDeliversWithoutRerunningCoder(t *testing.T) {
	f := newAwaitingPlanFixture(t)
	if f.rt.calls["coder"] != 1 {
		t.Fatalf("coder calls before resume = %d", f.rt.calls["coder"])
	}
	service := &pullRequestDeliveryService{}
	p := New(f.cfg, f.registry, WithRuntimeFactory(f.rt.factory), WithApprovalStore(f.approvals),
		WithDeliveryService(service), WithPrompter(&scriptedPrompter{interactive: false}))
	result, err := p.RunWithResult(context.Background(), RunConfig{
		ResumeRunID: f.runID, TargetDir: f.dir, ApprovePlanHash: f.pending.SubjectHash,
	})
	if err != nil || result.Outcome != workflow.RunCompleted {
		t.Fatalf("approved resume result=%+v err=%v", result, err)
	}
	if f.rt.calls["coder"] != 1 {
		t.Fatalf("approved resume re-ran the coder: calls=%d", f.rt.calls["coder"])
	}
	if service.calls != 1 {
		t.Fatalf("approved resume must create exactly one PR: calls=%d", service.calls)
	}
	link, err := os.ReadFile(filepath.Join(f.dir, ".ai-team", "artifacts", "feat", "stages", "implementation", "result.link"))
	if err != nil || strings.TrimSpace(string(link)) != "https://example.test/org/repo/pull/1" {
		t.Fatalf("stage result must be the controller PR link: %q err=%v", link, err)
	}
	if _, found, err := delivery.ReadTerminalRecord(onlyRunDir(t, f.dir)); err != nil || !found {
		t.Fatalf("terminal record missing: found=%t err=%v", found, err)
	}
}
