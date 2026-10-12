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

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/prchecks"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/stagecheck"
)

func TestAutoModeRetriesFailedCheckOnceThenStopsForHuman(t *testing.T) {
	dir := env(t)
	cfg := autoCheckTemplate(t)
	rt := newScripted()
	rt.contentFn["analyst"] = func(int) map[string]string { return map[string]string{"proposal": "# Incomplete\n"} }
	var retries []string
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name != "analyst" || len(retries) > 0 {
			return
		}
		for _, input := range inputs {
			if input.Name == "check-feedback" {
				data, err := os.ReadFile(input.Path)
				if err != nil {
					t.Errorf("read retry feedback: %v", err)
					return
				}
				retries = append(retries, string(data))
			}
		}
	}
	engine := NewRunEngine(New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{})))
	result, err := engine.Start(context.Background(), RunConfig{Mode: "auto", Feature: "feat", TaskDesc: "auto retry", TargetDir: dir})
	var approvalErr *ApprovalRequiredError
	if !errors.As(err, &approvalErr) || !strings.Contains(approvalErr.Checkpoint, "проверка этапа implementation") {
		t.Fatalf("second failed check must stop for a human decision: result=%+v err=%v", result, err)
	}
	if got := rt.calls["analyst"]; got != 2 {
		t.Fatalf("auto mode must run exactly one retry; calls=%d", got)
	}
	if len(retries) != 1 || !strings.Contains(retries[0], "required_sections:Acceptance") {
		t.Fatalf("retry must receive precise failed-check feedback: %q", retries)
	}
	events, err := evidence.VerifyEventLog(filepath.Join(dir, ".ai-team", "runs", result.RunID, "events.jsonl"), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	var autoRetries, failedChecks int
	var checkDecision bool
	for _, event := range events {
		switch event.Type {
		case "run_started":
			mode, _ = event.Data["mode"].(string)
		case "auto_check_retry":
			autoRetries++
		case "check_failed":
			failedChecks++
		case "approval_requested":
			kind, _ := event.Data["kind"].(string)
			checkDecision = checkDecision || kind == string(approval.KindCheckDecision)
		}
	}
	if mode != "auto" || autoRetries != 1 || failedChecks != 2 || !checkDecision {
		t.Fatalf("auto retry evidence mismatch: mode=%q retries=%d failed=%d checkDecision=%t", mode, autoRetries, failedChecks, checkDecision)
	}
}

func TestAutoModeSuccessfulCheckRetryStopsAtHumanOnlyStage(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.contentFn["analyst"] = func(call int) map[string]string {
		if call == 1 {
			return map[string]string{"proposal": "# Incomplete\n"}
		}
		return map[string]string{"proposal": "# Acceptance\nAll criteria met.\n"}
	}
	var gotFeedback bool
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name == "analyst" && rt.calls[name] == 2 {
			for _, input := range inputs {
				gotFeedback = gotFeedback || input.Name == "check-feedback"
			}
		}
	}
	engine := NewRunEngine(New(autoCheckTemplate(t), testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{})))
	result, err := engine.Start(context.Background(), RunConfig{Mode: "auto", Feature: "feat", TaskDesc: "auto retry and human stop", TargetDir: dir})
	var approvalErr *ApprovalRequiredError
	if !errors.As(err, &approvalErr) || !strings.Contains(approvalErr.Checkpoint, "human input acceptance") {
		t.Fatalf("auto mode must stop at the agentless human stage: result=%+v err=%v", result, err)
	}
	if rt.calls["analyst"] != 2 || !gotFeedback {
		t.Fatalf("retry behavior: calls=%d feedback=%t", rt.calls["analyst"], gotFeedback)
	}
	events, err := evidence.VerifyEventLog(filepath.Join(dir, ".ai-team", "runs", result.RunID, "events.jsonl"), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var passed, retries int
	for _, event := range events {
		if event.Type == "check_passed" {
			passed++
		}
		if event.Type == "auto_check_retry" {
			retries++
		}
	}
	if passed != 1 || retries != 1 {
		t.Fatalf("successful auto retry evidence: check_passed=%d retries=%d", passed, retries)
	}
}

func TestPRReviewReceivesDiffIdentitySpecsAndTechnicalDecision(t *testing.T) {
	dir := env(t)
	artifactRoot := filepath.Join(dir, ".ai-team", "artifacts", "feat")
	if err := os.MkdirAll(filepath.Join(artifactRoot, "specs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "specs", "acceptance.md"), []byte("# Acceptance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "design.md"), []byte("# Technical decision\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const prURL = "https://github.com/example/project/pull/9"
	rt := newScripted()
	rt.content["prauthor"] = map[string]string{"pr-url": prURL}
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	var reviewInputs map[string]string
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name != "reviewer" {
			return
		}
		reviewInputs = map[string]string{}
		for _, input := range inputs {
			data, err := os.ReadFile(input.Path)
			if err == nil {
				reviewInputs[input.Name] = string(data)
			} else if info, statErr := os.Stat(input.Path); statErr == nil && info.IsDir() {
				reviewInputs[input.Name] = "directory"
			}
		}
	}
	cfg := prReviewTemplate(t)
	checks := &fakePRStageChecks{}
	p := New(cfg, prReviewRegistry(), WithRuntimeFactory(rt.factory), WithPRStageChecks(checks), WithPrompter(&scriptedPrompter{}))
	if _, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "review PR", TargetDir: dir, ApproveGates: true}); err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if checks.called["project_checks"] != 1 || checks.called["pr_checks_green"] != 1 {
		t.Fatalf("project checks must run on the PR stage for agent results: %+v", checks.called)
	}
	if checks.repositoryCaptures != 1 {
		t.Fatalf("task repository must be captured exactly once at run start, got %d", checks.repositoryCaptures)
	}
	if checks.diffExpected == nil || checks.diffExpected.URL != prURL || checks.diffExpected.HeadRefOID != strings.Repeat("a", 40) {
		t.Fatalf("reviewer diff did not use identity accepted by project checks: %+v", checks.diffExpected)
	}
	if !strings.Contains(reviewInputs["pr-diff"], "diff --git a/main.go b/main.go") {
		t.Fatalf("reviewer did not receive PR diff: %+v", reviewInputs)
	}
	var identity prchecks.PullRequest
	if err := json.Unmarshal([]byte(reviewInputs["pr-identity"]), &identity); err != nil || identity.URL != prURL || identity.HeadRefOID != strings.Repeat("a", 40) {
		t.Fatalf("reviewer did not receive pinned PR identity: identity=%+v err=%v", identity, err)
	}
	if reviewInputs["specs"] != "directory" || reviewInputs["design"] != "# Technical decision\n" {
		t.Fatalf("reviewer is missing specification/design inputs: %+v", reviewInputs)
	}
}

func TestPRHardRulesRunForHumanAndAgentImplementations(t *testing.T) {
	for _, executor := range []string{"human", "agent"} {
		t.Run(executor, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "pr-url.txt")
			if err := os.WriteFile(path, []byte("https://github.com/example/project/pull/9\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			checks := &fakePRStageChecks{}
			p := New(&config.Config{}, nil, WithPRStageChecks(checks))
			rs := &runState{p: p, runCfg: RunConfig{TargetDir: dir}, taskRepository: fakeTaskRepository}
			stage := config.TemplateStage{
				ID: "implementation", Result: "link", LinkKind: "pr", Executor: executor,
				Check: &config.TemplateCheck{Kind: "hard", Rules: []string{"project_checks", "pr_checks_green"}},
			}
			result := notifier.StageResult{Outputs: []runtime.Artifact{{Name: "pr-url", Path: path}}}
			report := rs.runHardChecks(context.Background(), stage, result)
			if report.Status != stagecheck.StatusPassed || report.PRIdentity == nil || report.PRIdentity.HeadRefOID != strings.Repeat("a", 40) || checks.called["project_checks"] != 1 || checks.called["pr_checks_green"] != 1 {
				t.Fatalf("PR checks for executor=%s: report=%+v calls=%+v", executor, report, checks.called)
			}
		})
	}
}

type fakePRStageChecks struct {
	called             map[string]int
	diffExpected       *prchecks.PullRequest
	repositoryCaptures int
	// url overrides the PR URL the fake reports (default example/project#9).
	url string
}

func (f *fakePRStageChecks) prURL() string {
	if f.url != "" {
		return f.url
	}
	return "https://github.com/example/project/pull/9"
}

const fakeTaskRepository = "github.com/example/project"

func (f *fakePRStageChecks) TaskRepository(context.Context, string) (string, error) {
	f.repositoryCaptures++
	return fakeTaskRepository, nil
}

func (f *fakePRStageChecks) CheckIdentity(_ context.Context, _ string, repository string, _ string, rule string, expected *prchecks.PullRequest) (string, string, prchecks.PullRequest, error) {
	if repository != fakeTaskRepository {
		return "", "", prchecks.PullRequest{}, errors.New("PR check did not receive the captured task repository")
	}
	if f.called == nil {
		f.called = map[string]int{}
	}
	f.called[rule]++
	identity := prchecks.PullRequest{URL: f.prURL(), State: "OPEN", BaseRefName: "main", BaseRefOID: strings.Repeat("b", 40), HeadRefName: "feature", HeadRefOID: strings.Repeat("a", 40)}
	if expected != nil && (expected.URL != identity.URL || expected.BaseRefOID != identity.BaseRefOID || expected.HeadRefOID != identity.HeadRefOID) {
		return "", "", prchecks.PullRequest{}, errors.New("identity mismatch")
	}
	return stagecheck.RulePassed, "test pass", identity, nil
}

func (f *fakePRStageChecks) Diff(_ context.Context, _ string, repository string, _ string) ([]byte, prchecks.PullRequest, error) {
	if repository != fakeTaskRepository {
		return nil, prchecks.PullRequest{}, errors.New("PR diff did not receive the captured task repository")
	}
	return []byte("diff --git a/main.go b/main.go\n+added\n"), prchecks.PullRequest{
		URL: f.prURL(), State: "OPEN",
		BaseRefName: "main", BaseRefOID: strings.Repeat("b", 40),
		HeadRefName: "feature", HeadRefOID: strings.Repeat("a", 40),
	}, nil
}

func (f *fakePRStageChecks) DiffIdentity(ctx context.Context, targetDir, repository, reference string, expected *prchecks.PullRequest) ([]byte, prchecks.PullRequest, error) {
	if expected != nil {
		copy := *expected
		f.diffExpected = &copy
	}
	diff, identity, err := f.Diff(ctx, targetDir, repository, reference)
	if err == nil && expected != nil && (expected.URL != identity.URL || expected.BaseRefOID != identity.BaseRefOID || expected.HeadRefOID != identity.HeadRefOID) {
		return nil, prchecks.PullRequest{}, errors.New("identity mismatch")
	}
	return diff, identity, err
}

func autoCheckTemplate(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.ParseYAML([]byte(`schema_version: 5
template: auto-check-test
title: Auto check test
stages:
  - id: implementation
    title: Implementation
    function: developer
    result: md
    executor: human
    agent: analyst
    confirm: required
    required_sections: [Acceptance]
    check:
      kind: hard
      rules: [required_sections]
  - id: acceptance
    title: Acceptance
    function: bo
    result: approve
    executor: human
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func prReviewTemplate(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.ParseYAML([]byte(`schema_version: 5
template: pr-review-test
title: PR review test
stages:
  - id: implementation
    title: Implementation
    function: developer
    result: link
    link_kind: pr
    executor: agent
    agent: prauthor
    confirm: auto
    check:
      kind: hard
      rules: [project_checks, pr_checks_green]
  - id: code_review
    title: Code review
    function: reviewer
    result: md
    executor: agent
    agent: reviewer
    confirm: auto
    check:
      kind: hard
      rules: [verdict_marker]
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func prReviewRegistry() *agent.Registry {
	return agent.NewFS(fstest.MapFS{
		"prauthor/def.yaml": def(`name: prauthor
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs: {}
outputs:
  pr-url: '{feature}/pr-url.txt'
`),
		"prauthor/prompt.md": def("create PR link"),
		"reviewer/def.yaml": def(`name: reviewer
runtime: agentcli
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED, REJECTED]
inputs:
  specs: '{feature}/specs'
  design: '{feature}/design.md'
outputs:
  review: '{feature}/review.md'
`),
		"reviewer/prompt.md": def("review"),
	})
}

var _ PRStageChecks = (*fakePRStageChecks)(nil)

func TestPRRulesFailClosedWithoutCapturedTaskRepository(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pr-url.txt")
	if err := os.WriteFile(path, []byte("https://github.com/example/project/pull/9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := &fakePRStageChecks{}
	p := New(&config.Config{}, nil, WithPRStageChecks(checks))
	rs := &runState{p: p, runCfg: RunConfig{TargetDir: dir}, taskRepositoryErr: "origin is not a GitHub repository URL"}
	stage := config.TemplateStage{
		ID: "implementation", Result: "link", LinkKind: "pr", Executor: "agent",
		Check: &config.TemplateCheck{Kind: "hard", Rules: []string{"pr_checks_green"}},
	}
	result := notifier.StageResult{Outputs: []runtime.Artifact{{Name: "pr-url", Path: path}}}
	report := rs.runHardChecks(context.Background(), stage, result)
	if report.Status == stagecheck.StatusPassed || report.PRIdentity != nil || len(checks.called) != 0 {
		t.Fatalf("PR rules must not run without a captured task repository: report=%+v calls=%+v", report, checks.called)
	}
}
