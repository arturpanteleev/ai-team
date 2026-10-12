package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/cicd"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

const deployTestSHA = "0123456789abcdef0123456789abcdef01234567"

type stubDeployProvider struct {
	final    cicd.Build
	triggers []cicd.TriggerRequest
}

func (p *stubDeployProvider) Name() string { return "teamcity" }

func (p *stubDeployProvider) Trigger(_ context.Context, request cicd.TriggerRequest) (cicd.Build, error) {
	p.triggers = append(p.triggers, request)
	return cicd.Build{ID: "7", State: cicd.StateQueued}, nil
}

func (p *stubDeployProvider) Build(context.Context, string) (cicd.Build, error) { return p.final, nil }

func (p *stubDeployProvider) Cancel(context.Context, cicd.Build, string) error { return nil }

type mergedGit struct{ calls int }

func (g *mergedGit) Run(_ context.Context, _ string, name string, args ...string) delivery.StepResult {
	g.calls++
	if name == "gh" && args[0] == "api" {
		return delivery.StepResult{Status: delivery.StepPassed, Stdout: `{"name":"main","commit":{"sha":"` + deployTestSHA + `"}}`}
	}
	if name == "gh" {
		return delivery.StepResult{Status: delivery.StepPassed,
			Stdout: `{"state":"MERGED","baseRefName":"main","headRefOid":"` + deployTestSHA + `","mergeCommit":{"oid":"` + deployTestSHA + `"}}`}
	}
	if args[0] == "rev-parse" {
		return delivery.StepResult{Status: delivery.StepPassed, Stdout: deployTestSHA + "\n"}
	}
	return delivery.StepResult{Status: delivery.StepPassed}
}

type immediateClock struct{ now time.Time }

func (c *immediateClock) Now() time.Time { return c.now }
func (c *immediateClock) After(d time.Duration) <-chan time.Time {
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func deployTestState(t *testing.T, provider cicd.Provider, receipt *delivery.StageDeliveryReceipt) (*runState, *warningReplayRecorder, *agent.Agent) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Stages: []config.TemplateStage{{ID: "deploy", Title: "Выкладка", Function: "deployer", Result: "link",
			LinkKind: "build", Executor: "agent", Agent: "cicd-deploy"}},
		CICD: &config.CICDConfig{TeamCity: &config.TeamCityConfig{
			BaseURL: "https://teamcity.example.test", BuildType: "Project_Deploy", TokenEnv: "TEAMCITY_TOKEN",
			Branch: "main", Timeout: "1m", PollInterval: "15s",
		}},
	}
	recorder := &warningReplayRecorder{}
	options := []Option{WithRecorder(recorder), WithDeployClock(&immediateClock{}), WithDeployCommandRunner(&mergedGit{})}
	if provider != nil {
		options = append(options, WithDeployProvider(provider))
	}
	state := &runState{
		p:     New(cfg, nil, options...),
		runID: "run-deploy", runCfg: RunConfig{Feature: "feat", TargetDir: dir}, taskRepository: "github.example/org/repo",
		task:                 &runtime.Task{ArtifactRoot: filepath.Join(dir, ".ai-team", "artifacts")},
		stageDeliveryReceipt: receipt,
	}
	if err := os.MkdirAll(state.task.ArtifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	deployAgent := &agent.Agent{Name: "cicd-deploy", Kind: "deploy", RuntimeType: "deploy", Mutation: "external",
		Outputs: map[string]string{"link": "{feature}/deploy/build.link"}}
	return state, recorder, deployAgent
}

func TestControllerDeployWritesBuildLinkForMergedCommit(t *testing.T) {
	provider := &stubDeployProvider{final: cicd.Build{ID: "7", State: cicd.StateFinished, Status: cicd.StatusSuccess,
		Revision: deployTestSHA, WebURL: "https://teamcity.example.test/build/7"}}
	state, recorder, deployAgent := deployTestState(t, provider, &delivery.StageDeliveryReceipt{CommitSHA: deployTestSHA, PRURL: "https://github.example/org/repo/pull/1"})
	if err := state.runControllerDeploy(context.Background(), "deploy", "attempt-1", deployAgent); err != nil {
		t.Fatal(err)
	}
	if len(provider.triggers) != 1 || provider.triggers[0].CommitSHA != deployTestSHA || provider.triggers[0].Branch != "main" {
		t.Fatalf("unexpected triggers: %+v", provider.triggers)
	}
	link, err := os.ReadFile(filepath.Join(state.task.ArtifactRoot, "feat", "deploy", "build.link"))
	if err != nil || strings.TrimSpace(string(link)) != "https://teamcity.example.test/build/7" {
		t.Fatalf("build link = %q, %v", link, err)
	}
	if _, err := os.Stat(filepath.Join(state.runCfg.TargetDir, ".ai-team", "state", "deploy", "run-deploy-deploy.json")); err != nil {
		t.Fatalf("deploy receipt was not recorded: %v", err)
	}
	if len(recorder.projected) != 0 {
		t.Fatalf("successful deploy recorded events: %+v", recorder.projected)
	}
}

func TestControllerDeployFailureRecordsIntegrationError(t *testing.T) {
	provider := &stubDeployProvider{final: cicd.Build{ID: "7", State: cicd.StateFinished, Status: cicd.StatusFailure,
		Revision: deployTestSHA, WebURL: "https://teamcity.example.test/build/7"}}
	state, recorder, deployAgent := deployTestState(t, provider, &delivery.StageDeliveryReceipt{CommitSHA: deployTestSHA, PRURL: "https://github.example/org/repo/pull/1"})
	err := state.runControllerDeploy(context.Background(), "deploy", "attempt-1", deployAgent)
	if !errors.Is(err, cicd.ErrDeployRejected) {
		t.Fatalf("failed build must reject the stage, got %v", err)
	}
	if len(recorder.projected) != 1 {
		t.Fatalf("expected one integration_error, got %+v", recorder.projected)
	}
	event := recorder.projected[0]
	if event.Type != "integration_error" || event.Stage != "deploy" || event.AttemptID != "attempt-1" ||
		event.Data["integration"] != "cicd" || event.Data["provider"] != "teamcity" {
		t.Fatalf("unexpected integration_error: %+v", event)
	}
	if _, statErr := os.Stat(filepath.Join(state.task.ArtifactRoot, "feat", "deploy", "build.link")); !os.IsNotExist(statErr) {
		t.Fatalf("failed deploy must not publish a build link: %v", statErr)
	}
}

func TestControllerDeployFailsClosedWithoutCommitOrProvider(t *testing.T) {
	provider := &stubDeployProvider{}
	state, _, deployAgent := deployTestState(t, provider, nil)
	err := state.runControllerDeploy(context.Background(), "deploy", "attempt-1", deployAgent)
	if !errors.Is(err, cicd.ErrDeployRejected) || !strings.Contains(err.Error(), "PR delivery receipt") {
		t.Fatalf("deploy without a controller PR commit must be rejected, got %v", err)
	}
	if len(provider.triggers) != 0 {
		t.Fatal("deploy without a commit triggered a build")
	}

	state, _, deployAgent = deployTestState(t, nil, &delivery.StageDeliveryReceipt{CommitSHA: deployTestSHA, PRURL: "https://github.example/org/repo/pull/1"})
	if err := state.runControllerDeploy(context.Background(), "deploy", "attempt-1", deployAgent); err == nil ||
		!strings.Contains(err.Error(), "local controller") {
		t.Fatalf("process without a provider (worker) must fail closed, got %v", err)
	}

	state, _, deployAgent = deployTestState(t, provider, &delivery.StageDeliveryReceipt{CommitSHA: deployTestSHA, PRURL: "https://github.example/org/repo/pull/1"})
	state.p.cfg.Stages[0].LinkKind = "pr"
	if err := state.runControllerDeploy(context.Background(), "deploy", "attempt-1", deployAgent); !errors.Is(err, cicd.ErrDeployRejected) {
		t.Fatalf("deploy stage must be a build link stage, got %v", err)
	}
	if len(provider.triggers) != 0 {
		t.Fatal("misconfigured deploy stage triggered a build")
	}
}

// TestRunDeployStageIsControllerOwned runs a kind=deploy stage through the
// ordinary graph: no model runtime starts, and without a PR commit from an
// earlier controller delivery the attempt fails before any build is queued.
func TestRunDeployStageIsControllerOwned(t *testing.T) {
	dir := env(t)
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion, Template: "deploy-only", Title: "Deploy only", CLI: "opencode",
		PipelineAgents: []config.AgentConfig{{Name: "deploy"}},
		Stages: []config.TemplateStage{{ID: "deploy", Title: "Выкладка", Function: "deployer", Result: "link",
			LinkKind: "build", Executor: "agent", Agent: "cicd-deploy", Confirm: "auto"}},
		CICD: &config.CICDConfig{TeamCity: &config.TeamCityConfig{BaseURL: "https://teamcity.example.test",
			BuildType: "Project_Deploy", TokenEnv: "TEAMCITY_TOKEN", Branch: "main"}},
	}
	registry := agent.NewFS(fstest.MapFS{"cicd-deploy/def.yaml": def("name: cicd-deploy\nruntime: deploy\nkind: deploy\nmutation: external\noutputs:\n  link: '{feature}/deploy/build.link'\n")})
	provider := &stubDeployProvider{}
	rt := newScripted()
	p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithNotifier(&captureNotifier{}),
		WithPrompter(&scriptedPrompter{}), WithDeployProvider(provider), WithDeployCommandRunner(&mergedGit{}))
	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "deploy", TargetDir: dir, ApproveGates: true})
	if err == nil {
		t.Fatal("deploy without a merged PR commit must fail")
	}
	if len(rt.executed) != 0 {
		t.Fatalf("deploy stage started a model runtime: %v", rt.executed)
	}
	if len(provider.triggers) != 0 {
		t.Fatalf("deploy without commit queued builds: %+v", provider.triggers)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, `"type":"attempt_finished"`) || !strings.Contains(events, `"outcome":"failed"`) {
		t.Fatalf("deploy attempt must finish failed:\n%s", events)
	}
}

func TestControllerDeployRequiresCapturedRepository(t *testing.T) {
	provider := &stubDeployProvider{}
	state, _, deployAgent := deployTestState(t, provider, &delivery.StageDeliveryReceipt{CommitSHA: deployTestSHA, PRURL: "https://github.example/org/repo/pull/1"})
	state.taskRepository, state.taskRepositoryErr = "", "origin rewritten"
	err := state.runControllerDeploy(context.Background(), "deploy", "attempt-1", deployAgent)
	if !errors.Is(err, cicd.ErrDeployRejected) || !strings.Contains(err.Error(), "captured at run start") {
		t.Fatalf("deploy without the captured repository must be rejected, got %v", err)
	}
	if len(provider.triggers) != 0 {
		t.Fatal("deploy without repository identity triggered a build")
	}
}

func TestDeployStageHasOwnBudgetInsteadOfStageTimeout(t *testing.T) {
	state, _, deployAgent := deployTestState(t, &stubDeployProvider{}, nil)
	state.p.cfg.StageTimeout = "1m"
	state.p.cfg.CICD.TeamCity.MergeTimeout = "40m"
	state.p.cfg.CICD.TeamCity.Timeout = "50m"
	agentCfg := &config.AgentConfig{Name: "deploy", Timeout: "1m"}
	budget, err := state.stageTimeoutFor(deployAgent, agentCfg)
	if err != nil || budget != 40*time.Minute+50*time.Minute+config.DeployStageMargin {
		t.Fatalf("deploy budget = %s, %v", budget, err)
	}
	ordinary, err := state.stageTimeoutFor(&agent.Agent{Name: "coder"}, agentCfg)
	if err != nil || ordinary != time.Minute {
		t.Fatalf("ordinary stage timeout = %s, %v", ordinary, err)
	}
}
