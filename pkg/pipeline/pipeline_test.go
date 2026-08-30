package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// --- Тестовая инфраструктура -------------------------------------------------

func def(yaml string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(yaml)}
}

// testRegistry — реестр из четырёх агентов, повторяющий реальные контракты.
func testRegistry() *agent.Registry {
	return agent.NewFS(fstest.MapFS{
		"analyst/def.yaml": def(`name: analyst
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  proposal: '{feature}/proposal.md'
`),
		"coder/def.yaml": def(`name: coder
runtime: agentcli
prompt_file: prompt.md
mutation: source
allowed_paths: ['**']
require_diff: true
inputs:
  proposal: '{feature}/proposal.md'
outputs: {}
`),
		"tester/def.yaml": def(`name: tester
runtime: agentcli
prompt_file: prompt.md
mutation: tests
allowed_paths: ['**/*_test.go']
verdict:
  required: true
  marker: Result
  values: [PASS, FAIL]
inputs:
  proposal: '{feature}/proposal.md'
outputs:
  report: '{feature}/test-report.md'
`),
		"reviewer/def.yaml": def(`name: reviewer
runtime: agentcli
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED, REJECTED]
inputs:
  proposal: '{feature}/proposal.md'
outputs:
  review: '{feature}/review.md'
`),
		"deployer/def.yaml": def(`name: deployer
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  review: '{feature}/review.md'
outputs: {}
`),
		"analyst/prompt.md":  def("test"),
		"coder/prompt.md":    def("test"),
		"tester/prompt.md":   def("test"),
		"reviewer/prompt.md": def("test"),
		"deployer/prompt.md": def("test"),
	})
}

func deliveryRegistry() *agent.Registry {
	return agent.NewFS(fstest.MapFS{
		"approver/def.yaml": def(`name: approver
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
`),
		"deployer/def.yaml": def(`name: deployer
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
`),
		"approver/prompt.md": def("test"),
	})
}

// scriptedRuntime — фейковый runtime: пишет заданное содержимое выходов,
// умеет падать, блокироваться и вызывать hook для ассертов на входы.
type scriptedRuntime struct {
	executed []string
	// content[agent][output] — содержимое выхода; по умолчанию "ok"
	content map[string]map[string]string
	// contentFn — динамическое содержимое (по номеру запуска агента)
	contentFn map[string]func(callN int) map[string]string
	execErr   map[string]error
	blocked   map[string]string // agent -> blocker reason
	skipWrite map[string]bool   // agent -> не создавать выходы
	waitCtx   map[string]bool   // agent -> блокироваться до отмены ctx
	onExec    func(agentName string, inputs []runtime.Artifact)
	calls     map[string]int
	targetDir string
}

func newScripted() *scriptedRuntime {
	return &scriptedRuntime{
		content:   map[string]map[string]string{},
		contentFn: map[string]func(int) map[string]string{},
		execErr:   map[string]error{},
		blocked:   map[string]string{},
		skipWrite: map[string]bool{},
		waitCtx:   map[string]bool{},
		calls:     map[string]int{},
	}
}

func (r *scriptedRuntime) factory(string) (runtime.Runtime, error) { return r, nil }

func (r *scriptedRuntime) Execute(ctx context.Context, a *runtime.Agent, task *runtime.Task, inputs []runtime.Artifact) error {
	r.executed = append(r.executed, a.Name)
	r.calls[a.Name]++
	r.targetDir = task.TargetDir
	if r.onExec != nil {
		r.onExec(a.Name, inputs)
	}
	if r.waitCtx[a.Name] {
		<-ctx.Done()
		return ctx.Err()
	}
	if reason, ok := r.blocked[a.Name]; ok {
		path := verdict.StatusFilePath(task.ArtifactRoot, task.Feature, a.Name)
		os.MkdirAll(filepath.Dir(path), 0755)
		os.WriteFile(path, []byte("**Status:** BLOCKED\n**Blocker:** "+reason+"\n"), 0644)
		return nil
	}
	if err := r.execErr[a.Name]; err != nil {
		return err
	}
	if r.skipWrite[a.Name] {
		return nil
	}
	contents := r.content[a.Name]
	if fn := r.contentFn[a.Name]; fn != nil {
		contents = fn(r.calls[a.Name])
	}
	for outName, outPath := range a.Outputs {
		full := filepath.Join(task.ArtifactRoot, runtime.ReplaceVars(outPath, task.Feature))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return err
		}
		content := "ok"
		if c, ok := contents[outName]; ok {
			content = c
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

type scriptedPrompter struct {
	interactive bool
	answers     []string
	asked       []string
}

func (p *scriptedPrompter) Interactive() bool { return p.interactive }

func (p *scriptedPrompter) Ask(q string) string {
	p.asked = append(p.asked, q)
	if len(p.answers) == 0 {
		return "n"
	}
	ans := p.answers[0]
	p.answers = p.answers[1:]
	return ans
}

type captureNotifier struct {
	calls []notifier.StageResult
}

type fakeDeliveryService struct {
	calls int
}

func (f *fakeDeliveryService) Execute(_ context.Context, request delivery.Request) (delivery.Result, error) {
	f.calls++
	hash, _ := request.Plan.Hash()
	return delivery.Result{PlanHash: hash, CommitSHA: "deadbeef", PRURL: "https://example.test/pr/1"}, nil
}

func prepareDelivery(t *testing.T, dir string) string {
	t.Helper()
	change := []byte("package change\n")
	if err := os.WriteFile(filepath.Join(dir, "change.go"), change, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "change_test.go"), []byte("package change\nimport \"testing\"\nfunc TestPrepared(t *testing.T) {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/prepared\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	check, err := (checks.Runner{TargetDir: dir}).Run(context.Background(), checks.Definition{
		Name: "prepared-test", Class: "unit", Adapter: checks.AdapterGoTest,
		Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: checks.PolicyRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := evidence.Start(filepath.Join(dir, ".ai-team", "runs"), evidence.RunManifest{
		RunID: "prepared-run", ConfigSnapshot: json.RawMessage(`{"schema_version":1}`),
		WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(evidence.AttemptManifest{
		AttemptID: "prepared-run-001-check", Stage: "check", Status: notifier.StatusPassed, Checks: []checks.Result{check},
	}, filepath.Join(dir, ".ai-team", "artifacts"), nil, nil); err != nil {
		t.Fatal(err)
	}
	fileDigest := sha256.Sum256(change)
	reviewDigest := sha256.Sum256([]byte("**Verdict:** APPROVED\n"))
	plan := delivery.Plan{
		SchemaVersion: delivery.SchemaVersion, Branch: "ai-team/feat", BaseBranch: "main", Remote: "origin",
		Files: []string{"change.go"}, FileDigests: map[string]string{"change.go": fmt.Sprintf("%x", fileDigest)}, FileModes: map[string]string{"change.go": "100644"},
		BaselineHead: strings.Repeat("b", 40), SourceRunID: "prepared-run",
		VerifiedWorkspaceDigest: check.WorkspaceDigestAfter, CheckEvidenceDigest: check.EvidenceDigest,
		Preconditions: map[string]delivery.PreconditionEvidence{
			"review": {Type: "file", Size: 22, SHA256: fmt.Sprintf("%x", reviewDigest), Verdict: "APPROVED"},
		},
		CommitMessage: "feat change", PRTitle: "feat change", PRBody: "test delivery plan",
	}
	_, err = delivery.Prepare(dir, "feat", plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := plan.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func (m *captureNotifier) Notify(ctx context.Context, stage notifier.StageResult) error {
	m.calls = append(m.calls, stage)
	return nil
}

// env готовит target-директорию с task.md.
func env(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	taskDir := filepath.Join(dir, ".ai-team", "artifacts", "tasks", "feat")
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "task.md"), []byte("тестовая задача"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func onlyRunDir(t *testing.T, target string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(target, ".ai-team", "runs"))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".tmp-") {
			dirs = append(dirs, filepath.Join(target, ".ai-team", "runs", entry.Name()))
		}
	}
	if len(dirs) != 1 {
		t.Fatalf("ожидался один immutable run, got %v", dirs)
	}
	return dirs[0]
}

// cfgFor строит v4 конфиг с линейными passed-рёбрами без approvals:
// негативный вердикт без rejected-ребра останавливает run.
func cfgFor(agents ...config.AgentConfig) *config.Config {
	return cfgForGraph(nil, agents...)
}

// cfgForGraph строит линейный v4 граф и даёт тесту дополнить его
// (approvals, rejected-рёбра, max_visits).
func cfgForGraph(setup func(wf *config.WorkflowConfig), agents ...config.AgentConfig) *config.Config {
	cfg := &config.Config{SchemaVersion: config.CurrentSchemaVersion, PipelineAgents: agents, CLI: "opencode"}
	wf := &config.WorkflowConfig{Entry: agents[0].Name, MaxVisits: map[string]int{}}
	operatorApproval := func(target string) *config.WorkflowApprovalConfig {
		return &config.WorkflowApprovalConfig{
			Roles: []string{"operator"}, Quorum: "any",
			Actions: map[string]string{"approve": target, "reject": "$stop"},
		}
	}
	for i, a := range agents {
		target := workflow.TerminalComplete
		if i+1 < len(agents) {
			target = agents[i+1].Name
		}
		passed := config.WorkflowEdgeConfig{From: a.Name, Outcome: "passed", To: target}
		if !workflow.IsTerminal(target) {
			passed.Approval = operatorApproval(target)
			wf.Edges = append(wf.Edges, passed)
			wf.Edges = append(wf.Edges, config.WorkflowEdgeConfig{
				From: a.Name, Outcome: "warning", To: target, Approval: operatorApproval(target),
			})
		} else {
			wf.Edges = append(wf.Edges, passed)
			wf.Edges = append(wf.Edges, config.WorkflowEdgeConfig{From: a.Name, Outcome: "warning", To: target})
		}
	}
	if setup != nil {
		setup(wf)
	}
	cfg.Workflow = wf
	return cfg
}

// loopbackEdge — rejected-ребро с approval на возврат к source.
func loopbackEdge(from, to, role string) config.WorkflowEdgeConfig {
	return config.WorkflowEdgeConfig{
		From: from, Outcome: "rejected", To: to,
		Approval: &config.WorkflowApprovalConfig{
			Roles: []string{role}, Quorum: "any",
			Actions: map[string]string{"return_to_coder": to, "override_approve": "$complete", "reject": "$stop"},
		},
	}
}

func runPipeline(t *testing.T, dir string, cfg *config.Config, rt *scriptedRuntime, pr Prompter) (error, *captureNotifier) {
	t.Helper()
	n := &captureNotifier{}
	p := New(cfg, testRegistry(),
		WithNotifier(n),
		WithRuntimeFactory(rt.factory),
		WithPrompter(pr),
	)
	err := p.Run(context.Background(), RunConfig{
		Feature:      "feat",
		TaskDesc:     "тестовая задача",
		TargetDir:    dir,
		ApproveGates: true,
	})
	return err, n
}

// --- Happy path и вердикты ---------------------------------------------------

func TestRun_HappyPath(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "# Ревью\n\nвсё ок\n\n**Verdict:** APPROVED\n"}

	err, n := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}, config.AgentConfig{Name: "deployer"}),
		rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("ожидался успех, got: %v", err)
	}
	if got := strings.Join(rt.executed, ","); got != "analyst,reviewer,deployer" {
		t.Errorf("порядок выполнения: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", "review.md")); err != nil {
		t.Error("review.md не создан")
	}
	if len(n.calls) != 3 {
		t.Fatalf("нотификаций: %d", len(n.calls))
	}
	if n.calls[1].Verdict != verdict.Approved {
		t.Errorf("вердикт reviewer в StageResult: %q", n.calls[1].Verdict)
	}
	// Отчёты генерируются
	if _, err := os.Stat(filepath.Join(dir, ".ai-team", "reports", "feat", "index.html")); err != nil {
		t.Error("итоговый отчёт не создан")
	}
	if _, err := os.Stat(filepath.Join(dir, ".ai-team", "reports", "feat", "attempts", n.calls[1].AttemptID, "index.html")); err != nil {
		t.Error("stage-отчёт reviewer не создан")
	}
	runDir := onlyRunDir(t, dir)
	attempts, readErr := os.ReadDir(filepath.Join(runDir, "attempts"))
	if readErr != nil || len(attempts) != 3 {
		t.Fatalf("immutable attempts: count=%d err=%v", len(attempts), readErr)
	}
	if _, statErr := os.Stat(filepath.Join(runDir, "reports", "feat", "index.html")); statErr != nil {
		t.Fatalf("immutable final report не опубликован: %v", statErr)
	}
	events, readErr := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if readErr != nil || !strings.Contains(string(events), `"type":"run_finished"`) {
		t.Fatalf("run_finished event отсутствует: err=%v", readErr)
	}
	if n.calls[0].RunID == "" || n.calls[0].AttemptID == "" || n.calls[0].RunID != n.calls[1].RunID {
		t.Fatalf("run/attempt identity не передана в StageResult: %+v", n.calls)
	}
}

func TestRun_NonInteractiveApprovalDecisionResumeSkipsCompletedStage(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.Edges[0].Approval = &config.WorkflowApprovalConfig{
			Roles: []string{"product_owner"}, Quorum: "any",
			Actions: map[string]string{"approve": "reviewer", "reject": "$stop"},
		}
	}, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))

	first, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("ожидался pending approval, got result=%+v err=%v", first, err)
	}
	if rt.calls["analyst"] != 1 || rt.calls["reviewer"] != 0 {
		t.Fatalf("до решения выполнены неверные этапы: %+v", rt.calls)
	}
	stateStore, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := stateStore.Load(first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID != required.ApprovalID {
		t.Fatalf("не сохранено ожидание approval: %+v", state)
	}
	approvalStore, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dbg, dbgErr := approvalStore.Load(first.RunID, required.ApprovalID); dbgErr == nil {
		t.Logf("DEBUG pending: %s -> %s trigger=%s roles=%v actions=%v", dbg.FromStage, dbg.ToStage, dbg.Trigger, dbg.RequiredRoles, dbg.Actions)
	}
	if _, err := approvalStore.Decide(first.RunID, required.ApprovalID, approval.Decision{
		ActorID: "product-1", ActorRole: "product_owner", Action: "approve",
		SubjectHash: required.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}

	second, err := p.RunWithResult(context.Background(), RunConfig{
		ResumeRunID: first.RunID, TargetDir: dir,
	})
	if err != nil {
		t.Fatalf("resume должен завершиться успешно: result=%+v err=%v", second, err)
	}
	if second.RunID != first.RunID || rt.calls["analyst"] != 1 || rt.calls["reviewer"] != 1 {
		t.Fatalf("resume изменил identity или повторил этап: first=%+v second=%+v calls=%+v", first, second, rt.calls)
	}
	state, err = stateStore.Load(first.RunID)
	if err != nil || state.Phase != lifecycle.PhaseTerminal {
		t.Fatalf("run не стал terminal: %+v err=%v", state, err)
	}
}

func TestRun_ResumeRejectsApprovalForMutatedCandidate(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", args, commandErr, output)
		}
	}

	rt := newScripted()
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.Edges[0].Approval = &config.WorkflowApprovalConfig{
			Roles: []string{"product_owner"}, Quorum: "any",
			Actions: map[string]string{"approve": "reviewer", "reject": "$stop"},
		}
	}, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("ожидался pending approval, got result=%+v err=%v", first, err)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.CandidateSHA256 == "" {
		t.Fatal("approval subject не содержит candidate identity")
	}
	manager, err := candidate.Load(context.Background(), dir, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager.Root(), "stale.go"), []byte("package stale\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(first.RunID, required.ApprovalID, approval.Decision{
		ActorID: "product-1", ActorRole: "product_owner", Action: "approve",
		SubjectHash: required.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = p.RunWithResult(context.Background(), RunConfig{
		ResumeRunID: first.RunID, TargetDir: dir,
	})
	if err == nil || !strings.Contains(err.Error(), "candidate identity changed") {
		t.Fatalf("stale candidate approval должен быть отклонён: %v", err)
	}
	if rt.calls["reviewer"] != 0 {
		t.Fatalf("после stale approval нельзя запускать следующий AI stage: %+v", rt.calls)
	}
}

func TestRun_NonInteractiveReviewLoopbackDecisionResume(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "coder.go"), []byte("package retry\nconst Revision = 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", args, commandErr, output)
		}
	}

	rt := newScripted()
	rt.contentFn["reviewer"] = func(call int) map[string]string {
		if call == 1 {
			return map[string]string{"review": "**Verdict:** CHANGES_REQUESTED\n"}
		}
		return map[string]string{"review": "**Verdict:** APPROVED\n"}
	}
	var secondCoderInputs []string
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name != "coder" {
			return
		}
		if rt.calls["coder"] == 2 {
			for _, input := range inputs {
				secondCoderInputs = append(secondCoderInputs, input.Name)
			}
		}
		_ = os.WriteFile(filepath.Join(rt.targetDir, "coder.go"),
			[]byte(fmt.Sprintf("package retry\nconst Revision = %d\n", rt.calls["coder"])), 0644)
	}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.MaxVisits["coder"] = 3
		wf.MaxVisits["reviewer"] = 3
		wf.Edges = append(wf.Edges, loopbackEdge("reviewer", "coder", "reviewer"))
	}, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "coder"}, config.AgentConfig{Name: "reviewer"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	runCfg := RunConfig{Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// V4-семантика: каждое переходное ребро требует решения. Последовательно
	// проводим два обычных перехода, затем loopback-решение.
	var required *ApprovalRequiredError
	for i, step := range []struct{ role, action string }{
		{"operator", "approve"}, {"operator", "approve"}, {"reviewer", "return_to_coder"},
		// после возврата к coder повторный переход coder → reviewer требует решения
		{"operator", "approve"},
	} {
		result, runErr := p.RunWithResult(context.Background(), runCfg)
		if !errors.As(runErr, &required) {
			t.Fatalf("шаг %d: ожидался pending approval, got %v", i, runErr)
		}
		if _, err := store.Decide(result.RunID, required.ApprovalID, approval.Decision{
			ActorID: "human-1", ActorRole: step.role, Action: step.action,
			SubjectHash: required.SubjectHash,
		}); err != nil {
			t.Fatal(err)
		}
		runCfg = RunConfig{ResumeRunID: result.RunID, TargetDir: dir}
	}
	second, err := p.RunWithResult(context.Background(), runCfg)
	if err != nil {
		t.Fatalf("approved loopback resume: result=%+v err=%v", second, err)
	}
	if rt.calls["analyst"] != 1 || rt.calls["coder"] != 2 || rt.calls["reviewer"] != 2 {
		t.Fatalf("неверное число запусков после loopback: %+v", rt.calls)
	}
	if !containsString(secondCoderInputs, "review") {
		t.Fatalf("исправляющий coder не получил review: %v", secondCoderInputs)
	}
	events, err := os.ReadFile(filepath.Join(onlyRunDir(t, dir), "events.jsonl"))
	if err != nil || !strings.Contains(string(events), `"reason":"approved_loopback"`) {
		t.Fatalf("нет evidence approved loopback invalidation: err=%v", err)
	}
}

func TestRun_RequiredCheckOverridesPositiveAgentVerdict(t *testing.T) {
	dir := env(t)
	runtime := newScripted()
	runtime.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}

	err, notifications := runPipeline(t, dir, cfgFor(
		config.AgentConfig{Name: "analyst"},
		config.AgentConfig{Name: "reviewer", Checks: []checks.Definition{{
			Name: "forced-failure", Class: "unit",
			Command: []string{"go", "tool", "definitely-missing-ai-team-tool"}, Policy: checks.PolicyRequired,
		}}},
		config.AgentConfig{Name: "deployer"},
	), runtime, &scriptedPrompter{})
	var requiredFailure *checks.RequiredFailureError
	if !errors.As(err, &requiredFailure) {
		t.Fatalf("required check должен остановить pipeline, got: %v", err)
	}
	if got := strings.Join(runtime.executed, ","); got != "analyst,reviewer" {
		t.Fatalf("downstream не должен выполняться: %s", got)
	}
	if len(notifications.calls) != 2 || !notifications.calls[1].ValidationFailed ||
		notifications.calls[1].Status != notifier.StatusFailed || len(notifications.calls[1].Checks) != 1 {
		t.Fatalf("неверный result обязательной проверки: %+v", notifications.calls)
	}
	runDir := onlyRunDir(t, dir)
	attempts, readErr := os.ReadDir(filepath.Join(runDir, "attempts"))
	if readErr != nil || len(attempts) != 2 {
		t.Fatalf("attempt evidence: entries=%v err=%v", attempts, readErr)
	}
	manifest, readErr := os.ReadFile(filepath.Join(runDir, "attempts", attempts[1].Name(), "manifest.json"))
	if readErr != nil || !strings.Contains(string(manifest), `"checks"`) || !strings.Contains(string(manifest), `"forced-failure"`) {
		t.Fatalf("check evidence отсутствует: %s err=%v", manifest, readErr)
	}
}

func TestRun_OptionalUnavailableCheckProducesWarningAndContinues(t *testing.T) {
	dir := env(t)
	runtime := newScripted()
	runtime.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	err, notifications := runPipeline(t, dir, cfgFor(
		config.AgentConfig{Name: "analyst", Checks: []checks.Definition{{
			Name: "optional-security", Class: "security",
			Command: []string{"ai-team-tool-that-does-not-exist"}, Policy: checks.PolicyOptional,
		}}},
		config.AgentConfig{Name: "reviewer"},
	), runtime, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("optional check не должен останавливать pipeline: %v", err)
	}
	if got := strings.Join(runtime.executed, ","); got != "analyst,reviewer" {
		t.Fatalf("pipeline не продолжился: %s", got)
	}
	if len(notifications.calls) != 2 || notifications.calls[0].Status != notifier.StatusWarning ||
		len(notifications.calls[0].Checks) != 1 || notifications.calls[0].Checks[0].Status != checks.StatusSkipped {
		t.Fatalf("optional check должен быть explicit warning/skipped: %+v", notifications.calls)
	}
}

func TestRun_DeliveryRequiresExplicitApprovalNonInteractive(t *testing.T) {
	dir := env(t)
	_ = prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true})
	var approvalErr *ApprovalRequiredError
	if !errors.As(err, &approvalErr) {
		t.Fatalf("delivery без explicit approval должен быть остановлен, got: %v", err)
	}
	if got := strings.Join(rt.executed, ","); got != "approver" {
		t.Fatalf("deployer не должен запускаться без approval: %s", got)
	}
	reportData, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "reports", "feat", "index.html"))
	if readErr != nil || !strings.Contains(string(reportData), "Stopped") {
		t.Fatalf("финальный отчёт должен фиксировать stopped: err=%v", readErr)
	}
}

func TestRun_DeliveryRunsWithExplicitApproval(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	service := &fakeDeliveryService{}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service))
	err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
	})
	if err != nil {
		t.Fatalf("delivery с explicit approval должен пройти: %v", err)
	}
	if got := strings.Join(rt.executed, ","); got != "approver" || service.calls != 1 {
		t.Fatalf("LLM deployer не должен запускаться, controller calls=%d runtimes=%s", service.calls, got)
	}
}

func TestRun_DeliveryRejectsMismatchedApprovedPlanHash(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	wrongHash := strings.Repeat("f", 64)
	if wrongHash == approvedPlanHash {
		t.Fatal("test invariant broken: wrongHash accidentally matches the real plan hash")
	}
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	service := &fakeDeliveryService{}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service))
	err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: wrongHash,
	})
	if err == nil {
		t.Fatal("delivery с несовпадающим --approve-plan хешем должен быть отклонён, got nil")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("ожидалась ошибка hash mismatch, got: %v", err)
	}
	if service.calls != 0 {
		t.Fatalf("delivery не должен выполниться при несовпадении хеша, calls=%d", service.calls)
	}
	reportData, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "reports", "feat", "index.html"))
	if readErr != nil || strings.Contains(string(reportData), "Completed") {
		t.Fatalf("финальный отчёт не должен показывать успешное завершение: err=%v", readErr)
	}
}

func TestRun_DeliveryPreconditionsAreControllerEnforced(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** CHANGES_REQUESTED\n"}
	p := New(cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.Edges = append(wf.Edges, loopbackEdge("approver", "deployer", "operator"))
	}, config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}),
		deliveryRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true,
	})
	if err == nil || !strings.Contains(err.Error(), "precondition review не выполнен") {
		t.Fatalf("контроллер должен запретить delivery с негативным prerequisite, got: %v", err)
	}
	if got := strings.Join(rt.executed, ","); got != "approver" {
		t.Fatalf("deployer не должен исполняться при failed precondition: %s", got)
	}
}

func TestRun_DeliveryRequiresDeterministicTestEvidence(t *testing.T) {
	dir := env(t)
	runtime := newScripted()
	runtime.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(runtime.factory), WithPrompter(&scriptedPrompter{}))
	err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true,
	})
	if err == nil || !strings.Contains(err.Error(), "нет успешно выполненного required check") {
		t.Fatalf("LLM approval без controller-run tests не должен разрешать delivery: %v", err)
	}
	if got := strings.Join(runtime.executed, ","); got != "approver" {
		t.Fatalf("delivery planner/executor не должны стартовать: %s", got)
	}
}

func TestRun_DeliveryApprovalPersistedAndResumable(t *testing.T) {
	dir := env(t)
	prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	service := &fakeDeliveryService{}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service))

	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true})
	var approvalErr *ApprovalRequiredError
	if !errors.As(err, &approvalErr) || approvalErr.ApprovalID == "" {
		t.Fatalf("non-TTY delivery должен создать persisted approval, got: %v", err)
	}
	if service.calls != 0 {
		t.Fatalf("delivery не должен выполниться до решения, calls=%d", service.calls)
	}

	store, storeErr := approval.NewStore(dir)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	value, loadErr := store.Load(approvalErr.RunID, approvalErr.ApprovalID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if value.Status != approval.StatusPending || value.Trigger != "delivery_plan" ||
		value.SubjectHash != approvalErr.SubjectHash {
		t.Fatalf("неожиданный pending approval: status=%s trigger=%s subject=%s",
			value.Status, value.Trigger, value.SubjectHash)
	}
	if fmt.Sprint(value.RequiredRoles) != fmt.Sprint([]string{"release_manager"}) {
		t.Fatalf("роль delivery approval: %v", value.RequiredRoles)
	}
	var payload any
	if len(value.Payload) == 0 || json.Unmarshal(value.Payload, &payload) != nil {
		t.Fatalf("approval должен содержать canonical plan JSON payload: %q", value.Payload)
	}

	// Повторный resume без решения — снова waiting, без доставки.
	err = p.Run(context.Background(), RunConfig{ResumeRunID: approvalErr.RunID, TargetDir: dir, ApproveGates: true})
	if !errors.As(err, &approvalErr) || approvalErr.ApprovalID != value.ID {
		t.Fatalf("resume pending approval должен вернуть ApprovalRequiredError, got: %v", err)
	}
	if service.calls != 0 {
		t.Fatalf("delivery не должен выполниться без решения, calls=%d", service.calls)
	}

	// Resume с exact hash записывает решение и выполняет доставку.
	err = p.Run(context.Background(), RunConfig{
		ResumeRunID: approvalErr.RunID, TargetDir: dir, ApprovePlanHash: value.SubjectHash,
	})
	if err != nil {
		t.Fatalf("resume с --approve-plan должен завершить delivery: %v", err)
	}
	if service.calls != 1 {
		t.Fatalf("delivery должен выполниться ровно один раз, calls=%d", service.calls)
	}
	resolved, loadErr := store.Load(approvalErr.RunID, approvalErr.ApprovalID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if resolved.Status != approval.StatusResolved || resolved.ResolvedAction != "approve" ||
		len(resolved.Decisions) != 1 || resolved.Decisions[0].ActorRole != "release_manager" {
		t.Fatalf("решение не зафиксировано: %+v", resolved)
	}
}

func TestRun_DeferredGateConsolidatedIntoDeliveryDecision(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	service := &fakeDeliveryService{}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.Edges[0].Approval.Deferred = true
	}, config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"})
	p := New(cfg, deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service))

	// Первый прогон — non-interactive БЕЗ --approve-gates: deferred-гейт не
	// паузит, run доходит до единственного consolidated delivery-решения.
	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir})
	var approvalErr *ApprovalRequiredError
	if !errors.As(err, &approvalErr) {
		t.Fatalf("ожидался ApprovalRequiredError на delivery, got: %v", err)
	}
	if got := strings.Join(rt.executed, ","); got != "approver" {
		t.Fatalf("deferral не должен паузить у гейта: %s", got)
	}
	store, storeErr := approval.NewStore(dir)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	deliveryApproval, loadErr := store.Load(approvalErr.RunID, approvalErr.ApprovalID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if deliveryApproval.Trigger != "delivery_plan" {
		t.Fatalf("run должен остановиться именно на delivery, trigger=%s", deliveryApproval.Trigger)
	}
	values, listErr := store.List(approvalErr.RunID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	var gate *approval.PendingApproval
	for i := range values {
		if values[i].Deferred {
			gate = &values[i]
			break
		}
	}
	if gate == nil {
		t.Fatal("deferred-гейт не создан")
	}
	if gate.Status != approval.StatusPending || gate.FromStage != "approver" || gate.ToStage != "deployer" {
		t.Fatalf("неожиданный deferred-гейт: %+v", gate)
	}

	// Resume с точным canonical plan hash решает delivery и ratify-ит гейт.
	err = p.Run(context.Background(), RunConfig{
		ResumeRunID: approvalErr.RunID, TargetDir: dir, ApprovePlanHash: approvedPlanHash,
	})
	if err != nil {
		t.Fatalf("resume с --approve-plan должен завершиться: %v", err)
	}
	if service.calls != 1 {
		t.Fatalf("delivery должен выполниться после решения, calls=%d", service.calls)
	}
	resolvedGate, loadErr := store.Load(gate.RunID, gate.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if resolvedGate.Status != approval.StatusResolved || resolvedGate.ResolvedAction != "approve" ||
		len(resolvedGate.Decisions) != 1 || resolvedGate.Decisions[0].ActorRole != "release_manager" {
		t.Fatalf("deferred-гейт не ratify-ён consolidated delivery-решением: %+v", resolvedGate)
	}
}

func TestRun_DeliveryResumeRejectsMismatchedPlanHash(t *testing.T) {
	dir := env(t)
	prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	service := &fakeDeliveryService{}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service))

	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true})
	var approvalErr *ApprovalRequiredError
	if !errors.As(err, &approvalErr) {
		t.Fatalf("ожидался ApprovalRequiredError, got: %v", err)
	}

	wrongHash := strings.Repeat("f", 64)
	err = p.Run(context.Background(), RunConfig{
		ResumeRunID: approvalErr.RunID, TargetDir: dir, ApprovePlanHash: wrongHash,
	})
	if err == nil || !strings.Contains(err.Error(), "не совпадает с subject") {
		t.Fatalf("чужой --approve-plan должен быть отклонён на resume, got: %v", err)
	}
	if service.calls != 0 {
		t.Fatalf("delivery не должен выполниться, calls=%d", service.calls)
	}
	store, storeErr := approval.NewStore(dir)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	value, loadErr := store.Load(approvalErr.RunID, approvalErr.ApprovalID)
	if loadErr != nil || value.Status != approval.StatusPending {
		t.Fatalf("решение не должно быть записано чужим хешем: %+v err=%v", value, loadErr)
	}
}

func TestRun_DeliveryInteractiveRejectRecordsDecision(t *testing.T) {
	dir := env(t)
	prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	service := &fakeDeliveryService{}
	prompter := &scriptedPrompter{interactive: true}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(prompter), WithDeliveryService(service))

	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true})
	if !errors.Is(err, ErrUserStopped) {
		t.Fatalf("отказ в интерактиве должен остановить run, got: %v", err)
	}
	if service.calls != 0 {
		t.Fatalf("delivery не должен выполниться после отказа, calls=%d", service.calls)
	}
	store, storeErr := approval.NewStore(dir)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	// prepareDelivery создаёт отдельный immutable run "prepared-run",
	// поэтому ищем run по единственному каталогу approvals.
	approvalRoot := filepath.Join(dir, ".ai-team", "state", "approvals")
	entries, readErr := os.ReadDir(approvalRoot)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("ожидался один каталог approvals: %v err=%v", entries, readErr)
	}
	approvals, listErr := store.List(entries[0].Name())
	if listErr != nil || len(approvals) < 2 {
		t.Fatalf("ожидались approvals перехода и delivery: %v err=%v", approvals, listErr)
	}
	var deliveryApproval *approval.PendingApproval
	for i := range approvals {
		if approvals[i].Trigger == "delivery_plan" {
			deliveryApproval = &approvals[i]
		}
	}
	if deliveryApproval == nil {
		t.Fatalf("delivery_plan approval не найден: %+v", approvals)
	}
	approvals = []approval.PendingApproval{*deliveryApproval}
	if approvals[0].Status != approval.StatusResolved || approvals[0].ResolvedAction != "reject" {
		t.Fatalf("отказ должен быть зафиксирован как решение: %+v", approvals[0])
	}
}

func TestRun_NegativeVerdictStops_NonInteractive(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "плохо\n\n**Verdict:** REJECTED\n"}

	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}, config.AgentConfig{Name: "deployer"}),
		rt, &scriptedPrompter{interactive: false})

	var nve *NegativeVerdictError
	if !errors.As(err, &nve) {
		t.Fatalf("ожидался NegativeVerdictError, got: %v", err)
	}
	if nve.Verdict != verdict.Rejected {
		t.Errorf("вердикт: %q", nve.Verdict)
	}
	for _, name := range rt.executed {
		if name == "deployer" {
			t.Error("deployer не должен был выполниться после REJECTED")
		}
	}
}

func TestRun_FailResultStops(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tester"] = map[string]string{"report": "# Отчёт\n\n**Result:** FAIL\n"}

	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "tester"}, config.AgentConfig{Name: "deployer"}),
		rt, &scriptedPrompter{})

	var nve *NegativeVerdictError
	if !errors.As(err, &nve) {
		t.Fatalf("ожидался NegativeVerdictError по FAIL, got: %v", err)
	}
}

func TestRun_NegativeVerdict_ContinuePolicy(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** CHANGES_REQUESTED\n"}

	err, _ := runPipeline(t, dir,
		cfgForGraph(func(wf *config.WorkflowConfig) {
			wf.Edges = append(wf.Edges, loopbackEdge("reviewer", "deployer", "operator"))
		},
			config.AgentConfig{Name: "analyst"},
			config.AgentConfig{Name: "reviewer"},
			config.AgentConfig{Name: "deployer"},
		),
		rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("continue-политика должна пропустить: %v", err)
	}
	if got := strings.Join(rt.executed, ","); !strings.HasSuffix(got, "deployer") {
		t.Errorf("deployer должен был выполниться: %s", got)
	}
	reportData, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "reports", "feat", "index.html"))
	if readErr != nil || !strings.Contains(string(reportData), "Completed with warnings") || !strings.Contains(string(reportData), "Rejected") {
		t.Fatalf("negative-continue не должен выглядеть зелёным: err=%v", readErr)
	}
}

// --- BLOCKED -------------------------------------------------------------------

func TestRun_Blocked(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.blocked["analyst"] = "требования противоречивы"

	err, n := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}),
		rt, &scriptedPrompter{})

	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("ожидался BlockedError, got: %v", err)
	}
	if be.Reason != "требования противоречивы" {
		t.Errorf("причина: %q", be.Reason)
	}
	if len(rt.executed) != 1 {
		t.Errorf("после BLOCKED не должно быть этапов: %v", rt.executed)
	}
	if n.calls[0].Status != notifier.StatusBlocked {
		t.Errorf("статус этапа: %q", n.calls[0].Status)
	}
}

func TestRun_StaleBlockedMarkerIgnored(t *testing.T) {
	dir := env(t)
	root := filepath.Join(dir, ".ai-team", "artifacts")
	path := verdict.StatusFilePath(root, "feat", "analyst")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte("**Status:** BLOCKED\n**Blocker:** старый блокер\n"), 0644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)

	rt := newScripted()
	err, _ := runPipeline(t, dir, cfgFor(config.AgentConfig{Name: "analyst"}), rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("старый BLOCKED marker не должен блокировать новую попытку: %v", err)
	}
}

func TestRun_StaleStageSummaryRemoved(t *testing.T) {
	dir := env(t)
	summary := filepath.Join(dir, ".ai-team", "artifacts", "feat", ".stage-summary", "analyst.md")
	if err := os.MkdirAll(filepath.Dir(summary), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(summary, []byte("STALE-SUMMARY-MUST-NOT-APPEAR"), 0644); err != nil {
		t.Fatal(err)
	}

	rt := newScripted()
	err, notifications := runPipeline(t, dir, cfgFor(config.AgentConfig{Name: "analyst"}), rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("run должен пройти: %v", err)
	}
	reportData, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "reports", "feat", "attempts", notifications.calls[0].AttemptID, "index.html"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(reportData), "STALE-SUMMARY-MUST-NOT-APPEAR") {
		t.Fatal("stale stage summary попал в отчёт новой попытки")
	}
}

func TestRun_OutputCleanupRefusesSymlinkTraversal(t *testing.T) {
	dir := env(t)
	outside := t.TempDir()
	marker := filepath.Join(outside, "proposal.md")
	if err := os.WriteFile(marker, []byte("do-not-delete"), 0644); err != nil {
		t.Fatal(err)
	}
	featurePath := filepath.Join(dir, ".ai-team", "artifacts", "feat")
	if err := os.Symlink(outside, featurePath); err != nil {
		t.Fatal(err)
	}

	rt := newScripted()
	err, _ := runPipeline(t, dir, cfgFor(config.AgentConfig{Name: "analyst"}), rt, &scriptedPrompter{})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("cleanup через symlink должен быть запрещён: %v", err)
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil || string(data) != "do-not-delete" {
		t.Fatalf("outside marker повреждён: data=%q err=%v", data, readErr)
	}
}

func TestRun_StaleOutputRejected(t *testing.T) {
	dir := env(t)
	proposal := filepath.Join(dir, ".ai-team", "artifacts", "feat", "proposal.md")
	os.MkdirAll(filepath.Dir(proposal), 0755)
	os.WriteFile(proposal, []byte("старый output"), 0644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(proposal, old, old)

	rt := newScripted()
	rt.skipWrite["analyst"] = true
	err, _ := runPipeline(t, dir, cfgFor(config.AgentConfig{Name: "analyst"}), rt, &scriptedPrompter{})
	if err == nil || !strings.Contains(err.Error(), "не создан") {
		t.Fatalf("stale output должен быть отклонён, got: %v", err)
	}
}

// --- Loopback -------------------------------------------------------------------

func TestRun_Loopback_RetryWithReviewInput(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "coder.go"), []byte("package retry\nconst Revision = 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", args, commandErr, output)
		}
	}
	rt := newScripted()
	// Первый прогон reviewer — REJECTED, второй — APPROVED
	rt.contentFn["reviewer"] = func(call int) map[string]string {
		if call == 1 {
			return map[string]string{"review": "исправь\n\n**Verdict:** REJECTED\n"}
		}
		return map[string]string{"review": "теперь ок\n\n**Verdict:** APPROVED\n"}
	}

	var coderInputs [][]string
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name == "coder" {
			_ = os.WriteFile(filepath.Join(rt.targetDir, "coder.go"), []byte(fmt.Sprintf("package retry\nconst Revision = %d\n", rt.calls["coder"])), 0644)
			var names []string
			for _, in := range inputs {
				names = append(names, in.Name)
			}
			coderInputs = append(coderInputs, names)
		}
	}

	pr := &scriptedPrompter{interactive: true, answers: []string{"y"}}
	err, _ := runPipeline(t, dir,
		cfgForGraph(func(wf *config.WorkflowConfig) {
			wf.MaxVisits["coder"] = 3
			wf.MaxVisits["reviewer"] = 3
			wf.Edges = append(wf.Edges, loopbackEdge("reviewer", "coder", "reviewer"))
		},
			config.AgentConfig{Name: "analyst"},
			config.AgentConfig{Name: "coder"},
			config.AgentConfig{Name: "reviewer"},
			config.AgentConfig{Name: "deployer"},
		),
		rt, pr)
	if err != nil {
		t.Fatalf("loopback должен завершиться успехом: %v", err)
	}
	if rt.calls["coder"] != 2 {
		t.Errorf("coder должен выполниться дважды, calls=%d", rt.calls["coder"])
	}
	if rt.calls["reviewer"] != 2 {
		t.Errorf("reviewer должен выполниться дважды, calls=%d", rt.calls["reviewer"])
	}
	if rt.calls["deployer"] != 1 {
		t.Errorf("deployer должен выполниться один раз, calls=%d", rt.calls["deployer"])
	}
	if len(coderInputs) != 2 {
		t.Fatalf("coder запусков: %d", len(coderInputs))
	}
	// На втором запуске coder получает review.md дополнительным входом
	second := strings.Join(coderInputs[1], ",")
	if !strings.Contains(second, "review") {
		t.Errorf("на retry coder должен получить review во входах: %s", second)
	}
	runDir := onlyRunDir(t, dir)
	events, readErr := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if readErr != nil || !strings.Contains(string(events), `"type":"attempts_invalidated"`) {
		t.Fatalf("loopback должен записать invalidation event: err=%v", readErr)
	}
	reportData, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "reports", "feat", "index.html"))
	if readErr != nil || !strings.Contains(string(reportData), "Invalidated") || !strings.Contains(string(reportData), "Passed") {
		t.Fatalf("final report должен различать superseded и актуальные attempts: err=%v", readErr)
	}
}

// TestRun_Loopback_DefaultTargetIsMetadataDrivenNotNamedCoder проверяет, что
// дефолтный loopback (без явного loopback_to) находит стадию по mutation:
// source, даже если она называется не "coder" — ранее дефолт был строковым
func TestRun_Loopback_ExhaustedStops(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** REJECTED\n"}
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name == "coder" {
			_ = os.WriteFile(filepath.Join(rt.targetDir, fmt.Sprintf("coder-%d.go", rt.calls["coder"])), []byte("package retry\n"), 0644)
		}
	}

	pr := &scriptedPrompter{interactive: true, answers: []string{"y", "y"}}
	err, _ := runPipeline(t, dir,
		cfgForGraph(func(wf *config.WorkflowConfig) {
			wf.MaxVisits["coder"] = 2
			wf.MaxVisits["reviewer"] = 3
			wf.Edges = append(wf.Edges, loopbackEdge("reviewer", "coder", "reviewer"))
		},
			config.AgentConfig{Name: "analyst"},
			config.AgentConfig{Name: "coder"},
			config.AgentConfig{Name: "reviewer"},
		),
		rt, pr)
	if err == nil || !strings.Contains(err.Error(), "max_visits=2") {
		t.Fatalf("ожидался gate max_visits, got: %v", err)
	}
	if rt.calls["coder"] != 2 {
		t.Errorf("coder: 1 исходный + 1 retry = 2 запуска, got %d", rt.calls["coder"])
	}
}

func TestRun_GraphV4RoutesRejectedEdgeAndEnforcesMaxVisits(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** REJECTED\n"}
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name == "coder" {
			_ = os.WriteFile(filepath.Join(rt.targetDir, fmt.Sprintf("graph-%d.go", rt.calls["coder"])), []byte("package retry\n"), 0644)
		}
	}
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion, CLI: "opencode",
		PipelineAgents: []config.AgentConfig{{Name: "analyst"}, {Name: "coder"}, {Name: "reviewer"}},
		Workflow: &config.WorkflowConfig{
			Entry: "analyst", MaxVisits: map[string]int{"coder": 2, "reviewer": 2},
			Edges: []config.WorkflowEdgeConfig{
				graphEdge("analyst", "passed", "coder", "product_owner", map[string]string{"approve": "coder", "reject": "$stop"}),
				graphEdge("coder", "passed", "reviewer", "developer", map[string]string{"approve": "reviewer", "reject": "$stop"}),
				graphEdge("reviewer", "rejected", "coder", "reviewer", map[string]string{"return_to_coder": "coder", "reject": "$stop"}),
				{From: "reviewer", Outcome: "passed", To: "$complete"},
			},
		},
	}
	err, _ := runPipeline(t, dir, cfg, rt, &scriptedPrompter{
		interactive: true, answers: []string{"y", "y", "y", "y", "y"},
	})
	if err == nil || !strings.Contains(err.Error(), "max_visits=2") {
		t.Fatalf("ожидался max_visits gate, got: %v", err)
	}
	if rt.calls["coder"] != 2 || rt.calls["reviewer"] != 2 {
		t.Fatalf("graph visits: coder=%d reviewer=%d", rt.calls["coder"], rt.calls["reviewer"])
	}
	events, readErr := os.ReadFile(filepath.Join(onlyRunDir(t, dir), "events.jsonl"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(events), `"type":"transition_selected"`) ||
		!strings.Contains(string(events), `"target":"coder"`) {
		t.Fatalf("graph transition не записан:\n%s", events)
	}
}

func graphEdge(from, outcome, to, role string, actions map[string]string) config.WorkflowEdgeConfig {
	return config.WorkflowEdgeConfig{
		From: from, Outcome: outcome, To: to,
		Approval: &config.WorkflowApprovalConfig{Roles: []string{role}, Quorum: "any", Actions: actions},
	}
}

func TestRun_Loopback_DeclineStops(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** REJECTED\n"}
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name == "coder" {
			_ = os.WriteFile(filepath.Join(rt.targetDir, fmt.Sprintf("coder-%d.go", rt.calls["coder"])), []byte("package retry\n"), 0644)
		}
	}

	// В интерактиве каждое approval-ребро задаётся вопрос; отказ ("n") на
	// запросе loopback маппится в action reject → $stop → ErrUserStopped.
	pr := &scriptedPrompter{interactive: true, answers: []string{"y", "y", "n"}}
	p := New(cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.MaxVisits["coder"] = 3
		wf.MaxVisits["reviewer"] = 3
		wf.Edges = append(wf.Edges, loopbackEdge("reviewer", "coder", "reviewer"))
	}, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "coder"}, config.AgentConfig{Name: "reviewer"}),
		testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(pr))
	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir})
	if !errors.Is(err, ErrUserStopped) {
		t.Fatalf("отказ от retry = ErrUserStopped, got: %v", err)
	}
	if rt.calls["coder"] != 1 || rt.calls["reviewer"] != 1 {
		t.Fatalf("после отказа retry не должен выполняться: %+v", rt.calls)
	}
}

func TestRun_MissingRequiredVerdictFails(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "отчёт без control marker\n"}

	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}),
		rt, &scriptedPrompter{})
	if err == nil || !strings.Contains(err.Error(), "обязательный маркер") {
		t.Fatalf("missing verdict должен быть contract error, got: %v", err)
	}
}

func TestRun_MultipleRequiredVerdictsFail(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{
		"review": "**Verdict:** REJECTED\n**Verdict:** APPROVED\n",
	}

	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}),
		rt, &scriptedPrompter{})
	if err == nil || !strings.Contains(err.Error(), "несколько control-маркеров") {
		t.Fatalf("ambiguous verdict должен быть contract error, got: %v", err)
	}
}

// --- Таймаут ---------------------------------------------------------------------

func TestRun_StageTimeout(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.waitCtx["analyst"] = true

	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst", Timeout: "50ms"}),
		rt, &scriptedPrompter{})
	if err == nil || !strings.Contains(err.Error(), "превысил таймаут") {
		t.Fatalf("ожидалась ошибка таймаута, got: %v", err)
	}
}

// --- Git diff guard ---------------------------------------------------------------

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestRun_GitGuard_NoChangesFails(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	// .ai-team не должен считаться изменением кодера
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644)
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-qm", "init")
	cmd.Dir = dir
	cmd.Run()

	rt := newScripted() // coder ничего не меняет

	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "coder"}),
		rt, &scriptedPrompter{})
	if err == nil || !strings.Contains(err.Error(), "не создал изменений") {
		t.Fatalf("ожидалась ошибка git guard, got: %v", err)
	}
}

func TestRun_GitGuard_WithChangesPasses(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644)
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-qm", "init")
	cmd.Dir = dir
	cmd.Run()

	rt := newScripted()
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name == "coder" {
			os.WriteFile(filepath.Join(rt.targetDir, "new.go"), []byte("package main\n"), 0644)
		}
	}

	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "coder"}),
		rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("guard должен пропустить при изменениях: %v", err)
	}
}

func TestRun_GitGuard_PreexistingDirtyStateDoesNotCount(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	tracked := filepath.Join(dir, "tracked.go")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("package original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(tracked, []byte("package dirty_before_run\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rt := newScripted() // coder не добавляет изменений к существующему dirty state
	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "coder"}),
		rt, &scriptedPrompter{})
	if err == nil || !strings.Contains(err.Error(), "требует clean git workspace") {
		t.Fatalf("новый run с пользовательским dirty state должен fail closed, got: %v", err)
	}
}

func TestRun_ReadOnlyAgentCannotMutateProject(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	rt := newScripted()
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name == "analyst" {
			_ = os.WriteFile(filepath.Join(rt.targetDir, "unauthorized.go"), []byte("package unauthorized\n"), 0644)
		}
	}
	err, _ := runPipeline(t, dir, cfgFor(config.AgentConfig{Name: "analyst"}), rt, &scriptedPrompter{})
	if err == nil || !strings.Contains(err.Error(), "нарушил mutation policy") {
		t.Fatalf("read-only агент должен быть остановлен после изменения проекта, got: %v", err)
	}
}

func TestRun_MutationPoliciesFailClosedWithoutGit(t *testing.T) {
	t.Run("read-only mutation", func(t *testing.T) {
		dir := env(t)
		rt := newScripted()
		rt.onExec = func(name string, _ []runtime.Artifact) {
			if name == "analyst" {
				_ = os.WriteFile(filepath.Join(rt.targetDir, "unauthorized.txt"), []byte("changed"), 0644)
			}
		}
		err, _ := runPipeline(t, dir, cfgFor(config.AgentConfig{Name: "analyst"}), rt, &scriptedPrompter{})
		if err == nil || !strings.Contains(err.Error(), "read-only этап изменил проект") {
			t.Fatalf("non-git mutation должна быть обнаружена: %v", err)
		}
	})
	t.Run("required diff", func(t *testing.T) {
		dir := env(t)
		rt := newScripted()
		err, _ := runPipeline(t, dir, cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "coder"}), rt, &scriptedPrompter{})
		if err == nil || !strings.Contains(err.Error(), "не создал изменений") {
			t.Fatalf("non-git require_diff не должен пропускаться: %v", err)
		}
	})
}

func TestRun_TestMutationScopeRejectsProductionFile(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	rt := newScripted()
	rt.content["tester"] = map[string]string{"report": "**Result:** PASS\n"}
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name == "tester" {
			_ = os.WriteFile(filepath.Join(rt.targetDir, "production.go"), []byte("package production\n"), 0644)
		}
	}
	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "tester"}), rt, &scriptedPrompter{})
	if err == nil || !strings.Contains(err.Error(), "пути вне allowed_paths: production.go") {
		t.Fatalf("tester не должен менять production file, got: %v", err)
	}
}

func TestRun_TestMutationScopeAllowsTestFile(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	rt := newScripted()
	rt.content["tester"] = map[string]string{"report": "**Result:** PASS\n"}
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name == "tester" {
			_ = os.WriteFile(filepath.Join(rt.targetDir, "production_test.go"), []byte("package production\n"), 0644)
		}
	}
	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "tester"}), rt, &scriptedPrompter{})
	if err != nil {
		t.Fatalf("tester должен иметь право создать *_test.go: %v", err)
	}
}

// --- Разное -----------------------------------------------------------------------

func TestRun_CancelledContext(t *testing.T) {
	dir := env(t)
	rt := newScripted()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := New(cfgFor(config.AgentConfig{Name: "analyst"}), testRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	err := p.Run(ctx, RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir})
	if err == nil {
		t.Error("ожидалась ошибка отменённого контекста")
	}
	// Итоговый отчёт генерируется даже при отмене
	if _, sErr := os.Stat(filepath.Join(dir, ".ai-team", "reports", "feat", "index.html")); sErr != nil {
		t.Error("итоговый отчёт должен генерироваться при отмене")
	}
	reportData, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "reports", "feat", "index.html"))
	if readErr != nil || !strings.Contains(string(reportData), "Canceled") {
		t.Fatalf("отчёт отменённого run должен иметь Canceled: err=%v", readErr)
	}
}

func TestRun_ResumeKeepsRunIdentity(t *testing.T) {
	dir := env(t)
	cfg := cfgFor(config.AgentConfig{Name: "analyst"})
	first := New(cfg, testRegistry(),
		WithRuntimeFactory(newScripted().factory), WithPrompter(&scriptedPrompter{}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	firstResult, err := first.RunWithResult(ctx, RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir})
	if err == nil || firstResult.RunID == "" {
		t.Fatalf("ожидался resumable run с identity: result=%+v err=%v", firstResult, err)
	}

	secondRuntime := newScripted()
	second := New(cfg, testRegistry(),
		WithRuntimeFactory(secondRuntime.factory), WithPrompter(&scriptedPrompter{}))
	secondResult, err := second.RunWithResult(context.Background(), RunConfig{
		ResumeRunID: firstResult.RunID,
		TargetDir:   dir,
	})
	if err != nil {
		t.Fatalf("resume должен завершиться: %v", err)
	}
	if secondResult.RunID != firstResult.RunID {
		t.Fatalf("run identity изменилась: %s != %s", secondResult.RunID, firstResult.RunID)
	}
	events, err := evidence.VerifyEventLog(
		filepath.Join(dir, ".ai-team", "runs", firstResult.RunID, "events.jsonl"),
		firstResult.RunID,
	)
	if err != nil {
		t.Fatal(err)
	}
	var started, resumed int
	for _, event := range events {
		switch event.Type {
		case "run_started":
			started++
		case "run_resumed":
			resumed++
		}
	}
	if started != 1 || resumed != 1 {
		t.Fatalf("ожидались один start и один resume, получено start=%d resume=%d", started, resumed)
	}
}

func TestRunEngineCancelTerminatesResumableRun(t *testing.T) {
	dir := env(t)
	cfg := cfgFor(config.AgentConfig{Name: "analyst"})
	p := New(cfg, testRegistry(),
		WithRuntimeFactory(newScripted().factory), WithPrompter(&scriptedPrompter{}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started, err := p.RunWithResult(ctx, RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir})
	if err == nil || started.RunID == "" {
		t.Fatalf("не создан resumable run: %+v %v", started, err)
	}
	result, err := NewRunEngine(p).Cancel(CancelConfig{RunID: started.RunID, TargetDir: dir})
	if err != nil || result.Outcome != workflow.RunCanceled {
		t.Fatalf("cancel: result=%+v err=%v", result, err)
	}
	stateStore, _ := lifecycle.NewStore(dir)
	state, err := stateStore.Load(started.RunID)
	if err != nil || state.Phase != lifecycle.PhaseTerminal {
		t.Fatalf("cancel lifecycle: %+v err=%v", state, err)
	}
	replayed, err := evidence.ReplayEventLog(
		filepath.Join(dir, ".ai-team", "runs", started.RunID, "events.jsonl"), started.RunID,
	)
	if err != nil || replayed.Status != workflow.RunCanceled {
		t.Fatalf("cancel evidence: %+v err=%v", replayed, err)
	}
}

func TestRun_WorkspaceLockRejectsConcurrentRun(t *testing.T) {
	dir := env(t)
	firstRuntime := newScripted()
	firstRuntime.waitCtx["analyst"] = true
	started := make(chan struct{})
	firstRuntime.onExec = func(name string, _ []runtime.Artifact) {
		if name == "analyst" {
			close(started)
		}
	}
	first := New(cfgFor(config.AgentConfig{Name: "analyst"}), testRegistry(),
		WithRuntimeFactory(firstRuntime.factory), WithPrompter(&scriptedPrompter{}))
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.Run(ctx, RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir})
	}()
	<-started

	secondRuntime := newScripted()
	second := New(cfgFor(config.AgentConfig{Name: "analyst"}), testRegistry(),
		WithRuntimeFactory(secondRuntime.factory), WithPrompter(&scriptedPrompter{}))
	secondErr := second.Run(context.Background(), RunConfig{Feature: "other", TaskDesc: "t", TargetDir: dir})
	if secondErr == nil || !strings.Contains(secondErr.Error(), "workspace уже занят") {
		t.Fatalf("конкурентный run должен быть отклонён lock-ом, got: %v", secondErr)
	}
	if len(secondRuntime.executed) != 0 {
		t.Fatalf("второй runtime не должен запускаться: %v", secondRuntime.executed)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "tasks", "other", "task.md")); !os.IsNotExist(statErr) {
		t.Fatalf("отклонённый конкурентный run не должен записать task.md: %v", statErr)
	}

	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("первый run должен завершиться после cancel: %v", err)
	}
}

func TestRun_FailedStage_GeneratesStageReport(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.execErr["analyst"] = fmt.Errorf("агент analyst завершился с ошибкой: boom")

	err, notifications := runPipeline(t, dir, cfgFor(config.AgentConfig{Name: "analyst"}), rt, &scriptedPrompter{})
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if _, sErr := os.Stat(filepath.Join(dir, ".ai-team", "reports", "feat", "attempts", notifications.calls[0].AttemptID, "index.html")); sErr != nil {
		t.Error("stage-отчёт должен генерироваться и при ошибке этапа")
	}
}

func TestNewPipeline_Defaults(t *testing.T) {
	p := New(nil, nil)
	if p.cfg == nil || p.notifier == nil || p.prompter == nil || p.newRuntime == nil {
		t.Error("New должен установить дефолты")
	}
}

func TestRunStatus(t *testing.T) {
	if got := runStatusFor(nil, []notifier.StageResult{{Status: notifier.StatusRejected, Verdict: verdict.ChangesRequested}}); got != "completed_with_warnings" {
		t.Errorf("negative continue → completed_with_warnings, got %s", got)
	}
}

func TestReviewedCandidateIdentityFailsAfterWorkspaceMutation(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "source.go"), []byte("package source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(target, ".ai-team", "artifacts")
	controlDir := filepath.Join(artifactRoot, "feat", ".control")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := checks.WorkspaceDigest(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeControllerJSON(filepath.Join(controlDir, "review-candidate.json"), candidateEvidence{
		SchemaVersion: 1, RunID: "run-candidate", Purpose: "semantic_code_review", WorkspaceSHA256: digest,
		ChangedFiles: []candidateFile{}, Checks: []candidateCheck{}, Attempts: []candidateAttempt{},
	}); err != nil {
		t.Fatal(err)
	}
	state := &runState{
		runCfg: RunConfig{TargetDir: target, Feature: "feat"}, runID: "run-candidate",
		task: &runtime.Task{ArtifactRoot: artifactRoot},
	}
	if err := state.verifyCandidateEvidence("review-candidate.json", "semantic_code_review"); err != nil {
		t.Fatalf("unchanged reviewed candidate rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "source.go"), []byte("package source\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := state.verifyCandidateEvidence("review-candidate.json", "semantic_code_review"); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("mutated reviewed candidate must fail closed: %v", err)
	}
}

func TestDigestCaptureHashesCompleteBoundedStream(t *testing.T) {
	data := []byte("0123456789")
	capture := newDigestCapture(4)
	if written, err := capture.Write(data); err != nil || written != len(data) {
		t.Fatalf("Write() = %d, %v", written, err)
	}
	want := sha256.Sum256(data)
	if capture.String() != "0123" || capture.Total() != int64(len(data)) || !capture.Truncated() {
		t.Fatalf("unexpected bounded capture: value=%q total=%d truncated=%v", capture.String(), capture.Total(), capture.Truncated())
	}
	if capture.Digest() != fmt.Sprintf("%x", want[:]) {
		t.Fatalf("digest = %s, want %x", capture.Digest(), want)
	}
}
