package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
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
ask_questions: true
inputs:
  task: tasks/{feature}/task.md
outputs:
  proposal: '{feature}/proposal.md'
`),
		"questioner/def.yaml": def(`name: questioner
runtime: agentcli
prompt_file: prompt.md
mutation: none
ask_questions: true
inputs:
  task: tasks/{feature}/task.md
outputs: {}
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
		"verifier/def.yaml": def(`name: verifier
runtime: agentcli
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED]
inputs:
  proposal: '{feature}/proposal.md'
  review: '{feature}/review.md'
  test-report: '{feature}/test-report.md'
outputs:
  verification: '{feature}/verification.md'
`),
		"analyst/prompt.md":    def("test"),
		"questioner/prompt.md": def("test"),
		"coder/prompt.md":      def("test"),
		"tester/prompt.md":     def("test"),
		"reviewer/prompt.md":   def("test"),
		"deployer/prompt.md":   def("test"),
		"verifier/prompt.md":   def("test"),
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
		"coder/def.yaml": def(`name: coder
runtime: agentcli
prompt_file: prompt.md
mutation: source
allowed_paths: ['**']
require_diff: true
inputs:
  task: tasks/{feature}/task.md
outputs: {}
`),
		"approver/prompt.md": def("test"),
		"coder/prompt.md":    def("test"),
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
	onExecute func(agentName string, task *runtime.Task, inputs []runtime.Artifact)
	calls     map[string]int
	targetDir string
	usage     *runtime.Usage
	usagePer  map[string]*runtime.Usage
	// deadline первого Execute: стадия видит min(stage timeout, run budget),
	// поэтому по нему проверяется, что бюджет run'а реально вооружён.
	firstDeadline    time.Time
	firstHasDeadline bool
	deadlineSeen     bool
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

type trackingLifecycleStore struct {
	store                 *lifecycle.Store
	creates, loads, saves int
}

func (s *trackingLifecycleStore) Create(state lifecycle.State) error {
	s.creates++
	return s.store.Create(state)
}
func (s *trackingLifecycleStore) Load(runID string) (lifecycle.State, error) {
	s.loads++
	return s.store.Load(runID)
}
func (s *trackingLifecycleStore) Save(previous, next lifecycle.State) error {
	s.saves++
	return s.store.Save(previous, next)
}

func (r *scriptedRuntime) factory(string) (runtime.Runtime, error) { return r, nil }

// Usage — UsageReporter для тестов (P1-7): имитирует attested usage.
func (r *scriptedRuntime) Usage() *runtime.Usage { return r.usage }

func (r *scriptedRuntime) Execute(ctx context.Context, a *runtime.Agent, task *runtime.Task, inputs []runtime.Artifact) error {
	r.executed = append(r.executed, a.Name)
	r.calls[a.Name]++
	r.targetDir = task.TargetDir
	if !r.deadlineSeen {
		r.deadlineSeen = true
		r.firstDeadline, r.firstHasDeadline = ctx.Deadline()
	}
	if r.usagePer != nil {
		r.usage = r.usagePer[a.Name]
	}
	if r.onExec != nil {
		r.onExec(a.Name, inputs)
	}
	if r.onExecute != nil {
		r.onExecute(a.Name, task, inputs)
	}
	if r.waitCtx[a.Name] {
		<-ctx.Done()
		return ctx.Err()
	}
	if reason, ok := r.blocked[a.Name]; ok {
		path := verdict.StatusFilePath(task.ArtifactRoot, task.Feature, a.Name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("**Status:** BLOCKED\n**Blocker:** "+reason+"\n"), 0644); err != nil {
			return err
		}
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
	return delivery.Result{PlanHash: hash, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PRURL: "https://example.test/pr/1"}, nil
}

// capturingDeliveryService фиксирует TargetDir каждого delivery request
// (AUD-05: retry должен исполняться в candidate worktree, а не control target).
type capturingDeliveryService struct {
	calls   int
	targets []string
}

func (f *capturingDeliveryService) Execute(_ context.Context, request delivery.Request) (delivery.Result, error) {
	f.calls++
	f.targets = append(f.targets, request.TargetDir)
	hash, _ := request.Plan.Hash()
	return delivery.Result{PlanHash: hash, CommitSHA: strings.Repeat("a", 40), PRURL: "https://example.test/pr/1"}, nil
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
		SchemaVersion: delivery.SchemaVersion, Branch: "ai-team/feat", BaseBranch: "main", Remote: "origin", RemoteURL: "https://example.test/repo.git",
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
	// delivery-тесты рядом кладут prepared-run (delivery.Prepare) — это не цель.
	live := dirs[:0]
	for _, dir := range dirs {
		if filepath.Base(dir) == "prepared-run" {
			continue
		}
		live = append(live, dir)
	}
	dirs = live
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
	// Containment receipt (V0-P1-4): trusted-local → все оси PARTIAL.
	receiptData, readErr := os.ReadFile(filepath.Join(runDir, "containment.json"))
	if readErr != nil {
		t.Fatalf("containment.json не записан: %v", readErr)
	}
	var receipt containment.Receipt
	if err := json.Unmarshal(receiptData, &receipt); err != nil {
		t.Fatalf("повреждённый containment.json: %v", err)
	}
	if receipt.Profile != "trusted-local" || receipt.HasUnavailable() {
		t.Fatalf("trusted-local receipt: profile=%q unavailable=%v", receipt.Profile, receipt.HasUnavailable())
	}
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

// TestPipelineWorkerCrashRecovery kills an actual subprocess while its second
// pipeline stage is executing, after the first stage checkpoint is durable.
// A replacement process resumes the same run and must not replay the first
// stage. It exercises the process boundary and evidence/lifecycle recovery;
// external Git push/PR side effects are intentionally outside this fixture.
// The durable queue lease/claim boundary is tested independently in
// pkg/scheduler/queue_test.go: importing scheduler here would create a test
// import cycle because scheduler itself depends on pipeline. The CLI package
// separately tests OperationRecover dispatch through Poller and lifecycle
// reconciliation in TestRecoveryDispatchCompletesQueueFromFinishedEvidenceBeforeTerminalLifecycle.
func TestPipelineWorkerCrashRecovery(t *testing.T) {
	target := env(t)
	marker := filepath.Join(t.TempDir(), "reviewer-entered")
	ledger := filepath.Join(t.TempDir(), "stage-ledger")
	child := exec.Command(os.Args[0], "-test.run=^TestPipelineWorkerCrashHelper$")
	child.Env = append(os.Environ(), "AI_TEAM_PIPELINE_CRASH_TARGET="+target,
		"AI_TEAM_PIPELINE_CRASH_MARKER="+marker, "AI_TEAM_PIPELINE_CRASH_LEDGER="+ledger,
		"AI_TEAM_PIPELINE_CRASH_MODE=start")
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = child.Process.Kill()
			_ = child.Wait()
			t.Fatalf("pipeline child did not enter live reviewer stage: %s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()

	recovery := exec.Command(os.Args[0], "-test.run=^TestPipelineWorkerCrashHelper$")
	recovery.Env = append(os.Environ(), "AI_TEAM_PIPELINE_CRASH_TARGET="+target,
		"AI_TEAM_PIPELINE_CRASH_MARKER="+marker, "AI_TEAM_PIPELINE_CRASH_LEDGER="+ledger,
		"AI_TEAM_PIPELINE_CRASH_MODE=recover")
	if data, err := recovery.CombinedOutput(); err != nil {
		t.Fatalf("pipeline recovery process failed: %v\n%s", err, data)
	}
	data, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	var analystCalls, reviewerCalls int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		switch line {
		case "analyst":
			analystCalls++
		case "reviewer":
			reviewerCalls++
		}
	}
	if analystCalls != 1 || reviewerCalls != 2 {
		t.Fatalf("completed stage was replayed or interrupted stage not retried: analyst=%d reviewer=%d ledger=%q", analystCalls, reviewerCalls, data)
	}
}

// TestPipelineWorkerCrashHelper is the disposable process used above. It
// constructs the normal Pipeline and RunEngine with the same durable target.
func TestPipelineWorkerCrashHelper(t *testing.T) {
	target := os.Getenv("AI_TEAM_PIPELINE_CRASH_TARGET")
	if target == "" {
		return
	}
	marker := os.Getenv("AI_TEAM_PIPELINE_CRASH_MARKER")
	ledger := os.Getenv("AI_TEAM_PIPELINE_CRASH_LEDGER")
	mode := os.Getenv("AI_TEAM_PIPELINE_CRASH_MODE")
	rt := newScripted()
	rt.onExec = func(name string, _ []runtime.Artifact) {
		f, err := os.OpenFile(ledger, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatalf("open stage ledger: %v", err)
		}
		if _, err := fmt.Fprintln(f, name); err != nil {
			_ = f.Close()
			t.Fatalf("write stage ledger: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close stage ledger: %v", err)
		}
		if name == "reviewer" && mode == "start" {
			if err := os.WriteFile(marker, []byte("entered"), 0600); err != nil {
				t.Fatalf("write stage marker: %v", err)
			}
			select {}
		}
	}
	p := New(cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}),
		testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	engine := NewRunEngine(p)
	if mode == "start" {
		_, _ = engine.Start(context.Background(), RunConfig{RunID: "process-recovery", Feature: "feat", TaskDesc: "тестовая задача", TargetDir: target, ApproveGates: true})
		return
	}
	if mode != "recover" {
		t.Fatalf("unexpected helper mode %q", mode)
	}
	_, _ = engine.Resume(context.Background(), ResumeConfig{RunID: "process-recovery", TargetDir: target, ApproveGates: true})
}

func TestRun_StrictProfileBlockedBeforeExecution(t *testing.T) {
	// AUD-02 fail-closed: strict-контракт в V1 не реализован, поэтому запрос
	// strict-профиля блокируется ДО первого обращения к runtime и ДО записи
	// какого-либо evidence (никакого misleading UNAVAILABLE receipt).
	dir := env(t)
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "# Ревью\n\nвсё ок\n\n**Verdict:** APPROVED\n"}

	p := New(cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"}, config.AgentConfig{Name: "deployer"}),
		testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	result, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ContainmentProfile: "strict", ApproveGates: true,
	})
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Outcome != workflow.RunBlocked {
		t.Fatalf("strict: ожидался RunBlocked (fail-closed), result=%+v err=%v", result, err)
	}
	if len(rt.calls) != 0 {
		t.Fatalf("strict: runtime не должен вызываться, calls=%+v", rt.calls)
	}
	if _, listErr := os.Stat(filepath.Join(dir, ".ai-team", "runs")); listErr == nil {
		if dirs, _ := os.ReadDir(filepath.Join(dir, ".ai-team", "runs")); len(dirs) != 0 {
			t.Fatalf("strict: run evidence не должен писаться, найден: %v", dirs)
		}
	}
}

func TestRun_NonInteractiveApprovalDecisionResumeSkipsCompletedStage(t *testing.T) {
	dir := env(t)
	filesystemStore, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	checkpointPort := &trackingLifecycleStore{store: filesystemStore}
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.Edges[0].Approval = &config.WorkflowApprovalConfig{
			Roles: []string{"product_owner"}, Quorum: "any",
			Actions: map[string]string{"approve": "reviewer", "reject": "$stop"},
		}
	}, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithLifecycleStore(checkpointPort))

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
	state, err := checkpointPort.Load(first.RunID)
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
	state, err = checkpointPort.Load(first.RunID)
	if err != nil || state.Phase != lifecycle.PhaseTerminal {
		t.Fatalf("run не стал terminal: %+v err=%v", state, err)
	}
	if checkpointPort.creates != 1 || checkpointPort.loads < 2 || checkpointPort.saves < 2 {
		t.Fatalf("pipeline bypassed injected lifecycle port: creates=%d loads=%d saves=%d", checkpointPort.creates, checkpointPort.loads, checkpointPort.saves)
	}
}

func TestRun_AnalystQuestionsWaitAndResumeSameRunWithDurableAnswer(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	var targetDenied, laterDenied []string
	rt.blocked["analyst"] = "нужны сведения о целевой аудитории"
	rt.content["analyst"] = map[string]string{"proposal": "готовая спецификация"}
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name != "analyst" {
			return
		}
		if rt.calls[name] == 1 {
			path := filepath.Join(dir, ".ai-team", "artifacts", "tasks", "feat", "questions.md")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("## Уточнение\nКто целевой клиент и на какую метрику влияем?\n"), 0644); err != nil {
				t.Fatal(err)
			}
			return
		}
		delete(rt.blocked, name)
		gotQuestion, gotAnswer, gotBrief, gotBriefVersion := false, false, false, false
		for _, input := range inputs {
			if input.Name == "questions" {
				gotQuestion = true
				question, err := os.ReadFile(input.Path)
				if err != nil || !strings.Contains(string(question), "целевой клиент") {
					t.Errorf("архивный source artifact с вопросами не попал в повторную попытку: %q err=%v", question, err)
				}
			}
			if input.Name == "clarification-answer" {
				gotAnswer = true
				answer, err := os.ReadFile(input.Path)
				if err != nil || !strings.Contains(string(answer), "B2B-клиенты") {
					t.Errorf("аналитик не получил durable answer: %q err=%v", answer, err)
				}
			}
			if input.Name == "business-brief" {
				gotBrief = true
				brief, err := os.ReadFile(input.Path)
				if err != nil || !strings.Contains(string(brief), "B2B-клиенты среднего бизнеса") {
					t.Errorf("analyst не получил новую версию brief с ответом: %q err=%v", brief, err)
				}
			}
			if input.Name == "business-brief-version" {
				gotBriefVersion = true
				metadata, err := os.ReadFile(input.Path)
				if err != nil || !strings.Contains(string(metadata), `"sha256"`) || !strings.Contains(string(metadata), `"id"`) {
					t.Errorf("analyst должен получить ID/SHA version manifest: %q err=%v", metadata, err)
				}
			}
		}
		if !gotQuestion || !gotAnswer || !gotBrief || !gotBriefVersion {
			t.Errorf("resume inputs: questions=%v answer=%v brief=%v version=%v", gotQuestion, gotAnswer, gotBrief, gotBriefVersion)
		}
	}
	rt.onExecute = func(name string, task *runtime.Task, _ []runtime.Artifact) {
		switch name {
		case "analyst":
			if rt.calls[name] == 2 {
				targetDenied = append([]string(nil), task.DeniedReadPaths...)
			}
		case "questioner":
			laterDenied = append([]string(nil), task.DeniedReadPaths...)
		}
	}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.MaxVisits["analyst"] = 4
		wf.Edges = append(wf.Edges, config.WorkflowEdgeConfig{
			From: "analyst", Outcome: "blocked", To: "analyst",
			Approval: &config.WorkflowApprovalConfig{
				Roles: []string{"product_owner"}, Quorum: "any",
				Actions: map[string]string{"answer_questions": "analyst", "stop": "$stop"},
			},
		})
	}, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "questioner"})
	pr := &scriptedPrompter{}
	controllerDBPath := filepath.Join(dir, ".ai-team", "state", "custom-controller.sqlite")
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(pr),
		WithControllerReadDenyPaths(controllerDBPath))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "увеличить доход продаж", TargetDir: dir})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("run должен ждать Product Owner, result=%+v err=%v", first, err)
	}
	stateStore, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := stateStore.Load(first.RunID)
	if err != nil || state.Phase != lifecycle.PhaseWaiting || state.NextStage != "analyst" {
		t.Fatalf("ожидание должно сохранить тот же этап и run: %+v err=%v", state, err)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || pending.FromStage != "analyst" || pending.Targets["answer_questions"] != "analyst" || !strings.Contains(string(pending.Payload), "целевой клиент") {
		t.Fatalf("question approval потерял содержимое/маршрут: %+v err=%v", pending, err)
	}
	if _, err := store.Decide(first.RunID, required.ApprovalID, approval.Decision{
		ActorID: "product-1", ActorRole: "product_owner", Action: "answer_questions",
		Comment: "Целевые клиенты — B2B-клиенты среднего бизнеса.", SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	var followupRequired *ApprovalRequiredError
	if !errors.As(err, &followupRequired) {
		t.Fatalf("после успешного этапа с ответом должен сохраниться обычный graph approval: result=%+v err=%v", second, err)
	}
	if second.RunID != first.RunID || rt.calls["analyst"] != 2 || rt.calls["questioner"] != 0 {
		t.Fatalf("первый resume изменил identity или выполнил последующий этап до approval: first=%+v second=%+v calls=%+v", first, second, rt.calls)
	}
	followup, err := store.Load(first.RunID, followupRequired.ApprovalID)
	if err != nil || followup.FromStage != "analyst" || followup.ToStage != "questioner" {
		t.Fatalf("не найден обычный graph approval после ответа: %+v err=%v", followup, err)
	}
	if _, err := store.Decide(first.RunID, followup.ID, approval.Decision{
		ActorID: "operator-1", ActorRole: "operator", Action: "approve", SubjectHash: followup.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	third, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil {
		t.Fatalf("resume после обычного graph approval должен продолжить тот же run: result=%+v err=%v", third, err)
	}
	if third.RunID != first.RunID || rt.calls["analyst"] != 2 || rt.calls["questioner"] != 1 || third.Outcome != "completed" {
		t.Fatalf("неверный resume после graph approval: first=%+v second=%+v third=%+v calls=%+v", first, second, third, rt.calls)
	}
	if len(targetDenied) == 0 || len(laterDenied) == 0 {
		t.Fatalf("both target and following stage must receive a per-stage answer deny policy: target=%v later=%v", targetDenied, laterDenied)
	}
	resolvedTarget, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	canonicalPath, err := QuestionAnswerCanonicalPath(resolvedTarget, first.RunID, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	projectionPath, err := QuestionAnswerMaterializationPath(resolvedTarget, first.RunID, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	approvalJSONPath := filepath.Join(resolvedTarget, ".ai-team", "state", "approvals", first.RunID, pending.ID+".json")
	controllerEventPath, err := (evidence.ControllerEventStore{TargetDir: resolvedTarget}).Path(first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, protected := range []string{canonicalPath, projectionPath, approvalJSONPath} {
		for label, paths := range map[string][]string{"target": targetDenied, "later resume": laterDenied} {
			found := false
			for _, path := range paths {
				if !filepath.IsAbs(path) || filepath.Clean(path) != path {
					t.Fatalf("%s stage received invalid denied path %q", label, path)
				}
				if path == protected {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s stage lost durable protected path %q: target=%v later=%v", label, protected, targetDenied, laterDenied)
			}
		}
	}
	persistedAnswerSnapshotDenied := false
	for _, path := range laterDenied {
		if strings.Contains(path, filepath.Join(".ai-team", "runs", first.RunID, "attempts")) &&
			strings.Contains(path, "clarification-answer") {
			persistedAnswerSnapshotDenied = true
		}
	}
	if !persistedAnswerSnapshotDenied {
		t.Fatalf("later stage after graph-approval resume must deny the prior immutable answer snapshot: %v", laterDenied)
	}
	for label, paths := range map[string][]string{"target": targetDenied, "later resume": laterDenied} {
		found := false
		for _, path := range paths {
			if path == approvalJSONPath {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s stage must deny the durable approval JSON containing the answer comment: %v", label, paths)
		}
	}
	for _, protected := range []string{
		filepath.Join(resolvedTarget, ".ai-team", "runs", first.RunID, "events.jsonl"),
		controllerEventPath,
		filepath.Join(resolvedTarget, ".ai-team", "web.db"),
		filepath.Join(resolvedTarget, ".ai-team", "web.db-wal"),
		filepath.Join(resolvedTarget, ".ai-team", "web.db-shm"),
		filepath.Join(resolvedTarget, ".ai-team", "web.db-journal"),
		controllerDBPath, controllerDBPath + "-wal", controllerDBPath + "-shm", controllerDBPath + "-journal",
	} {
		for label, paths := range map[string][]string{"target": targetDenied, "later resume": laterDenied} {
			found := false
			for _, path := range paths {
				if path == protected {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s stage must deny controller event/SQLite projection %q: %v", label, protected, paths)
			}
		}
	}
	versions, err := listBriefVersions(filepath.Join(dir, ".ai-team", "runs", first.RunID, "brief"))
	if err != nil || len(versions) != 2 {
		t.Fatalf("ожидаются исходная версия и версия с уточнением: %#v err=%v", versions, err)
	}
	brief, err := os.ReadFile(versions[1].Path)
	if err != nil || !strings.Contains(string(brief), "увеличить доход продаж") || !strings.Contains(string(brief), "Целевые клиенты — B2B-клиенты") {
		t.Fatalf("версия brief должна хранить намерение и ответ: %q err=%v", brief, err)
	}
}

func TestRun_ProductOwnerApprovesVersionedSpecBeforeArchitect(t *testing.T) {
	dir := env(t)
	registry := agent.NewFS(fstest.MapFS{
		"analyst/def.yaml": def(`name: analyst
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  proposal: '{feature}/proposal.md'
  spec: '{feature}/specs/product/spec.md'
`),
		"architect/def.yaml": def(`name: architect
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs:
  design: '{feature}/design.md'
`),
		"analyst/prompt.md": def("test"), "architect/prompt.md": def("test"),
	})
	governance := func(wf *config.WorkflowConfig) {
		for i := range wf.Edges {
			if wf.Edges[i].From == "analyst" && wf.Edges[i].Outcome == "passed" {
				wf.Edges[i].Approval = &config.WorkflowApprovalConfig{
					Roles: []string{"product_owner"}, Quorum: "any", Deferred: false,
					Actions: map[string]string{"approve_spec": "architect", "reject": "$stop"},
				}
			}
		}
	}
	cfg := cfgForGraph(governance, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "architect"})
	rt := newScripted()
	architectBrief, architectBriefVersion, architectSpec := false, false, false
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name != "architect" {
			return
		}
		for _, input := range inputs {
			if input.Name == "business-brief" {
				architectBrief = true
			}
			if input.Name == "business-brief-version" {
				architectBriefVersion = true
			}
			if input.Name == "approved-spec" {
				architectSpec = true
			}
		}
	}
	p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "увеличить доход продаж", TargetDir: dir, ApproveGates: true,
	})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) || rt.calls["architect"] != 0 {
		t.Fatalf("architect должен ждать явного решения Product Owner: result=%+v calls=%v err=%v", first, rt.calls, err)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	var payload approvedSpecPayload
	if json.Unmarshal(pending.Payload, &payload) != nil || payload.Kind != "agreed_spec" ||
		payload.BriefVersion.SHA256 == "" || payload.Artifacts["proposal"] == "" || payload.Artifacts["spec"] == "" {
		t.Fatalf("approval должен связывать версии намерения и обоих артефактов: %+v", payload)
	}
	if _, err := store.Decide(first.RunID, pending.ID, approval.Decision{
		ActorID: "product-1", ActorRole: "product_owner", Action: "approve_spec", SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil || second.RunID != first.RunID || rt.calls["architect"] != 1 || !architectBrief || !architectBriefVersion || !architectSpec {
		t.Fatalf("архитектор должен получить согласованные immutable inputs: result=%+v calls=%v brief=%v version=%v spec=%v err=%v", second, rt.calls, architectBrief, architectBriefVersion, architectSpec, err)
	}
	briefInfo, err := os.Stat(filepath.Join(dir, ".ai-team", "runs", first.RunID, payload.BriefVersion.Path))
	if err != nil || briefInfo.Mode().Perm()&0o222 != 0 {
		t.Fatalf("версия brief должна быть immutable: info=%v err=%v", briefInfo, err)
	}
}

func TestRun_ApproveGatesDoesNotAutoApproveAnalystQuestions(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.blocked["analyst"] = "нужны сведения о целевой аудитории"
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name != "analyst" {
			return
		}
		path := filepath.Join(dir, ".ai-team", "artifacts", "tasks", "feat", "questions.md")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("Кто целевой клиент?\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.MaxVisits["analyst"] = 4
		wf.Edges = append(wf.Edges, config.WorkflowEdgeConfig{
			From: "analyst", Outcome: "blocked", To: "analyst",
			Approval: &config.WorkflowApprovalConfig{
				Roles: []string{"product_owner"}, Quorum: "any",
				Actions: map[string]string{"answer_questions": "analyst", "stop": "$stop"},
			},
		})
	}, config.AgentConfig{Name: "analyst"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	result, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "увеличить доход продаж", TargetDir: dir, ApproveGates: true,
	})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("answer_questions должен ждать человеческий ответ даже при ApproveGates: result=%+v err=%v", result, err)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Load(result.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != approval.StatusPending || len(pending.Decisions) != 0 {
		t.Fatalf("уточнение должно оставаться pending без автоматически созданных решений: %+v", pending)
	}
}

func TestRun_AnalystClarificationRecoversAfterRunningStatePersisted(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.blocked["analyst"] = "нужны сведения о целевой аудитории"
	rt.content["analyst"] = map[string]string{"proposal": "готовая спецификация"}
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name != "analyst" {
			return
		}
		if rt.calls[name] == 1 {
			path := filepath.Join(dir, ".ai-team", "artifacts", "tasks", "feat", "questions.md")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("Кто целевой клиент?\n"), 0644); err != nil {
				t.Fatal(err)
			}
			return
		}
		delete(rt.blocked, name)
		var gotQuestion, gotAnswer bool
		for _, input := range inputs {
			switch input.Name {
			case "questions":
				gotQuestion = true
			case "clarification-answer":
				gotAnswer = true
				if !strings.Contains(input.Path, filepath.Join(".ai-team", "runs")) || strings.Contains(input.Path, "inflight-inputs") {
					t.Errorf("recovery did not pass the read-only run-scoped materialization to the runtime: %q", input.Path)
				}
				answer, readErr := os.ReadFile(input.Path)
				if readErr != nil || !strings.Contains(string(answer), "B2B-клиенты") {
					t.Errorf("после recovery потерян durable answer: %q err=%v", answer, readErr)
				}
			}
		}
		if !gotQuestion || !gotAnswer {
			t.Errorf("после recovery analyst inputs: questions=%v answer=%v", gotQuestion, gotAnswer)
		}
	}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.MaxVisits["analyst"] = 4
		wf.Edges = append(wf.Edges, config.WorkflowEdgeConfig{
			From: "analyst", Outcome: "blocked", To: "analyst",
			Approval: &config.WorkflowApprovalConfig{
				Roles: []string{"product_owner"}, Quorum: "any",
				Actions: map[string]string{"answer_questions": "analyst", "stop": "$stop"},
			},
		})
	}, config.AgentConfig{Name: "analyst"})
	canonicalTarget, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	answerStore := ControllerQuestionAnswerStore{TargetDir: canonicalTarget}
	var orphanSnapshotPath string
	rt.onExecute = func(name string, _ *runtime.Task, _ []runtime.Artifact) {
		if name != "analyst" || rt.calls[name] != 2 {
			return
		}
		if _, statErr := os.Lstat(orphanSnapshotPath); !os.IsNotExist(statErr) {
			t.Errorf("resume must remove the unmanifested crash snapshot before starting a local stage: path=%q err=%v", orphanSnapshotPath, statErr)
		}
	}
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}),
		WithQuestionAnswerInputProvider(canonicalQuestionAnswerTestProvider{store: answerStore}))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "увеличить доход продаж", TargetDir: dir})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("ожидалось ожидание ответа: result=%+v err=%v", first, err)
	}
	approvals, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := approvals.Load(first.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	decided, err := approvals.Decide(first.RunID, pending.ID, approval.Decision{
		ActorID: "product-1", ActorRole: "product_owner", Action: "answer_questions",
		Comment: "B2B-клиенты среднего бизнеса", SubjectHash: pending.SubjectHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	answerPath, err := answerStore.Prepare(decided)
	if err != nil {
		t.Fatalf("prepare controller-owned canonical answer: %v", err)
	}
	// Match the durable events written by the first resume attempt before its
	// lifecycle state advances to running. The process then crashes at that
	// boundary, so the next process sees a running lifecycle and invalidated
	// question attempt but must still rebuild the answer input.
	evidenceStore, _, replayed, err := evidence.Resume(filepath.Join(dir, ".ai-team", "runs"), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "approval_decided", AttemptID: decided.AttemptID, Data: approvalEventData(decided)}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "transition_selected", AttemptID: decided.AttemptID, Stage: decided.FromStage, Data: map[string]any{
		"from": decided.FromStage, "outcome": "blocked", "edge_target": decided.ToStage,
		"action": decided.ResolvedAction, "target": decided.Targets[decided.ResolvedAction],
	}}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "attempts_invalidated", Data: map[string]any{
		"attempt_ids": []string{replayed.Attempts[0].AttemptID}, "reason": "approved_loopback",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "run_resumed"}); err != nil {
		t.Fatal(err)
	}
	// Reproduce a hard crash after SnapshotInputs has copied the answer but
	// before PublishAttempt could bind that copy to a durable manifest.
	orphanAttemptID := evidenceStore.NewAttemptID("analyst", 2)
	compiledGraph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "attempt_started", AttemptID: orphanAttemptID, Stage: "analyst", Timestamp: time.Now().UTC(), Data: map[string]any{
		"stage_index": compiledGraph.Index("analyst") + 1,
	}}); err != nil {
		t.Fatal(err)
	}
	orphanedInputs, _, err := evidenceStore.SnapshotInputs(orphanAttemptID, []evidence.Artifact{{Name: "clarification-answer", Path: answerPath}})
	if err != nil || len(orphanedInputs) != 1 {
		t.Fatalf("simulate pre-manifest input snapshot: inputs=%+v err=%v", orphanedInputs, err)
	}
	orphanSnapshotPath = orphanedInputs[0].Path
	if data, readErr := os.ReadFile(orphanSnapshotPath); readErr != nil || !strings.Contains(string(data), "B2B-клиенты среднего бизнеса") {
		t.Fatalf("crash fixture should contain the sensitive answer before resume: %q err=%v", data, readErr)
	}

	// Reproduce a crash after decision and the PhaseRunning/NextStage write, but
	// before the analyst consumes its reconstructed extra inputs.
	lifecycles, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := lifecycles.Load(first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	running := waiting
	running.Phase = lifecycle.PhaseRunning
	running.NextStage = "analyst"
	running.PendingApprovalID = ""
	if err := lifecycles.Save(waiting, running); err != nil {
		t.Fatal(err)
	}

	second, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil {
		t.Fatalf("resume после crash должен восстановить approval answer: result=%+v err=%v", second, err)
	}
	if second.RunID != first.RunID || second.Outcome != "completed" || rt.calls["analyst"] != 2 {
		t.Fatalf("неверный resumed result: first=%+v second=%+v calls=%+v", first, second, rt.calls)
	}
	if _, err := os.Lstat(orphanSnapshotPath); !os.IsNotExist(err) {
		t.Fatalf("resume must remove the orphaned input snapshot: path=%q err=%v", orphanSnapshotPath, err)
	}
}

func TestRun_AnalystClarificationRemovesPublishedOrphanBeforeLaterStage(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.blocked["analyst"] = "нужны сведения о целевой аудитории"
	rt.content["analyst"] = map[string]string{"proposal": "готовая спецификация"}
	var orphanAnswerPath string
	laterStageChecked := false
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		switch name {
		case "analyst":
			if rt.calls[name] == 1 {
				path := filepath.Join(dir, ".ai-team", "artifacts", "tasks", "feat", "questions.md")
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("Кто целевой клиент?\n"), 0644); err != nil {
					t.Fatal(err)
				}
				return
			}
			delete(rt.blocked, name)
			foundAnswer := false
			for _, input := range inputs {
				if input.Name != "clarification-answer" {
					continue
				}
				foundAnswer = true
				data, err := os.ReadFile(input.Path)
				if err != nil || !strings.Contains(string(data), "B2B-клиенты") {
					t.Errorf("resumed analyst must receive the approved answer: %q err=%v", data, err)
				}
			}
			if !foundAnswer {
				t.Error("resumed analyst did not receive the approved clarification answer")
			}
			if _, err := os.Lstat(orphanAnswerPath); !os.IsNotExist(err) {
				t.Errorf("published orphan answer must be removed before resumed stage: path=%q err=%v", orphanAnswerPath, err)
			}
		case "questioner":
			laterStageChecked = true
			if data, err := os.ReadFile(orphanAnswerPath); err == nil {
				t.Errorf("later stage could still read the answer from the crashed attempt: %q", data)
			} else if !os.IsNotExist(err) {
				t.Errorf("checking the crashed attempt answer: %v", err)
			}
		}
	}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.MaxVisits["analyst"] = 4
		wf.Edges = append(wf.Edges, config.WorkflowEdgeConfig{
			From: "analyst", Outcome: "blocked", To: "analyst",
			Approval: &config.WorkflowApprovalConfig{
				Roles: []string{"product_owner"}, Quorum: "any",
				Actions: map[string]string{"answer_questions": "analyst", "stop": "$stop"},
			},
		})
	}, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "questioner"})
	canonicalTarget, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	answerStore := ControllerQuestionAnswerStore{TargetDir: canonicalTarget}
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}),
		WithQuestionAnswerInputProvider(canonicalQuestionAnswerTestProvider{store: answerStore}))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "уточнить целевую аудиторию", TargetDir: dir})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("ожидалось ожидание ответа: result=%+v err=%v", first, err)
	}
	approvals, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := approvals.Load(first.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	decided, err := approvals.Decide(first.RunID, pending.ID, approval.Decision{
		ActorID: "product-1", ActorRole: "product_owner", Action: "answer_questions",
		Comment: "B2B-клиенты среднего бизнеса", SubjectHash: pending.SubjectHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := answerStore.Prepare(decided); err != nil {
		t.Fatal(err)
	}

	evidenceStore, _, replayed, err := evidence.Resume(filepath.Join(dir, ".ai-team", "runs"), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "approval_decided", AttemptID: decided.AttemptID, Data: approvalEventData(decided)}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "transition_selected", AttemptID: decided.AttemptID, Stage: decided.FromStage, Data: map[string]any{
		"from": decided.FromStage, "outcome": "blocked", "edge_target": decided.ToStage,
		"action": decided.ResolvedAction, "target": decided.Targets[decided.ResolvedAction],
	}}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "attempts_invalidated", Data: map[string]any{
		"attempt_ids": []string{replayed.Attempts[0].AttemptID}, "reason": "approved_loopback",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "run_resumed"}); err != nil {
		t.Fatal(err)
	}
	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	orphanAttemptID := evidenceStore.NewAttemptID("analyst", 2)
	attemptStarted := time.Now().UTC()
	if err := evidenceStore.Append(evidence.Event{Type: "attempt_started", AttemptID: orphanAttemptID, Stage: "analyst", Timestamp: attemptStarted, Data: map[string]any{
		"stage_index": graph.Index("analyst") + 1,
	}}); err != nil {
		t.Fatal(err)
	}
	answerInput, err := (canonicalQuestionAnswerTestProvider{store: answerStore}).MaterializeQuestionAnswer(first.RunID, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The temp target may be spelled through /var while the provider returns
	// its canonical /private/var path. Use the run store's own spelling for
	// evidence provenance, as a worker does when collecting its scoped input.
	answerInput.Path = filepath.Join(evidenceStore.RunDir(), "inputs", pending.ID+"-answer.md")
	orphanInputs, cleanup, err := evidenceStore.SnapshotInputs(orphanAttemptID, toEvidenceArtifacts([]runtime.Artifact{answerInput}))
	if err != nil || len(orphanInputs) != 1 {
		t.Fatalf("snapshot answer before publishing orphan attempt: inputs=%+v err=%v", orphanInputs, err)
	}
	finishedAt := attemptStarted.Add(time.Second)
	if err := evidenceStore.PublishAttempt(evidence.AttemptManifest{
		AttemptID: orphanAttemptID, Stage: "analyst", StageIndex: graph.Index("analyst") + 1,
		StartedAt: attemptStarted, FinishedAt: finishedAt,
		Status: "passed", Execution: string(workflow.ExecutionSucceeded),
		Decision: string(workflow.DecisionNotApplicable), Outcome: string(workflow.OutcomePassed),
	}, filepath.Join(dir, ".ai-team", "artifacts"), orphanInputs, nil); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	_, orphanManifest, err := evidence.ReadAttemptManifest(nil, evidenceStore.RunDir(), first.RunID, orphanAttemptID)
	if err != nil || len(orphanManifest.Inputs) != 1 {
		t.Fatalf("read the published crash-boundary manifest: manifest=%+v err=%v", orphanManifest, err)
	}
	orphanAnswerPath = filepath.Join(evidenceStore.RunDir(), filepath.FromSlash(orphanManifest.Inputs[0].EvidencePath))
	if data, err := os.ReadFile(orphanAnswerPath); err != nil || !strings.Contains(string(data), "B2B-клиенты") {
		t.Fatalf("fixture must contain the answer under attempts/<id>/inputs before crash: %q err=%v", data, err)
	}

	lifecycles, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := lifecycles.Load(first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	running := waiting
	running.Phase, running.NextStage, running.PendingApprovalID = lifecycle.PhaseRunning, "analyst", ""
	if err := lifecycles.Save(waiting, running); err != nil {
		t.Fatal(err)
	}

	second, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	var followup *ApprovalRequiredError
	if !errors.As(err, &followup) || second.RunID != first.RunID || rt.calls["analyst"] != 2 {
		t.Fatalf("resume must abandon the orphan and retry analyst: result=%+v calls=%v err=%v", second, rt.calls, err)
	}
	if _, err := os.Lstat(orphanAnswerPath); !os.IsNotExist(err) {
		t.Fatalf("resume left the published orphan answer in place: path=%q err=%v", orphanAnswerPath, err)
	}
	followupApproval, err := approvals.Load(first.RunID, followup.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approvals.Decide(first.RunID, followupApproval.ID, approval.Decision{
		ActorID: "operator-1", ActorRole: "operator", Action: "approve", SubjectHash: followupApproval.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	third, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil || third.RunID != first.RunID || third.Outcome != "completed" || rt.calls["questioner"] != 1 || !laterStageChecked {
		t.Fatalf("later stage must run without access to the crashed answer copy: result=%+v calls=%v checked=%v err=%v", third, rt.calls, laterStageChecked, err)
	}
}

func TestRun_AnalystClarificationFailsClosedAfterCompletedTargetBeforeTransition(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.blocked["analyst"] = "нужны сведения о целевой аудитории"
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name != "analyst" || rt.calls[name] != 1 {
			return
		}
		path := filepath.Join(dir, ".ai-team", "artifacts", "tasks", "feat", "questions.md")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("Кто целевой клиент?\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.MaxVisits["analyst"] = 4
		wf.Edges = append(wf.Edges, config.WorkflowEdgeConfig{
			From: "analyst", Outcome: "blocked", To: "analyst",
			Approval: &config.WorkflowApprovalConfig{
				Roles: []string{"product_owner"}, Quorum: "any",
				Actions: map[string]string{"answer_questions": "analyst", "stop": "$stop"},
			},
		})
	}, config.AgentConfig{Name: "analyst"})
	targetDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	answerStore := ControllerQuestionAnswerStore{TargetDir: targetDir}
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}),
		WithQuestionAnswerInputProvider(canonicalQuestionAnswerTestProvider{store: answerStore}))
	first, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "уточнить целевую аудиторию", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("ожидалось ожидание ответа: result=%+v err=%v", first, err)
	}
	approvals, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := approvals.Load(first.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	decided, err := approvals.Decide(first.RunID, pending.ID, approval.Decision{
		ActorID: "product-1", ActorRole: "product_owner", Action: "answer_questions",
		Comment: "B2B-клиенты среднего бизнеса", SubjectHash: pending.SubjectHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := answerStore.Prepare(decided); err != nil {
		t.Fatalf("prepare controller-owned canonical answer: %v", err)
	}

	runRoot := filepath.Join(dir, ".ai-team", "runs")
	evidenceStore, _, replayed, err := evidence.Resume(runRoot, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	sourceAttempt := replayed.Attempts[0]
	if err := evidenceStore.Append(evidence.Event{
		Type: "approval_decided", AttemptID: decided.AttemptID, Timestamp: time.Now().UTC(),
		Data: approvalEventData(decided),
	}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{
		Type: "transition_selected", AttemptID: decided.AttemptID, Stage: "analyst", Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"from": "analyst", "outcome": "blocked", "edge_target": "analyst",
			"action": "answer_questions", "target": "analyst",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "attempts_invalidated", Timestamp: time.Now().UTC(), Data: map[string]any{
		"attempt_ids": []string{sourceAttempt.AttemptID}, "reason": "approved_loopback",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "run_resumed", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	targetAttemptID := evidenceStore.NewAttemptID("analyst", 2)
	attemptStarted := time.Now().UTC()
	attemptFinished := attemptStarted.Add(time.Second)
	if err := evidenceStore.Append(evidence.Event{
		Type: "attempt_started", Stage: "analyst", AttemptID: targetAttemptID, Timestamp: attemptStarted,
		Data: map[string]any{"stage_index": graph.Index("analyst") + 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.PublishAttempt(evidence.AttemptManifest{
		AttemptID: targetAttemptID, Stage: "analyst", StageIndex: graph.Index("analyst") + 1,
		StartedAt: attemptStarted, FinishedAt: attemptFinished,
		Status: "passed", Execution: string(workflow.ExecutionSucceeded),
		Decision: string(workflow.DecisionNotApplicable), Outcome: string(workflow.OutcomePassed),
	}, filepath.Join(dir, ".ai-team", "artifacts"), nil, nil); err != nil {
		t.Fatal(err)
	}
	manifestDigest, _, err := evidence.AttemptManifestDigest(nil, evidenceStore.RunDir(), first.RunID, targetAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{
		Type: "attempt_finished", Stage: "analyst", AttemptID: targetAttemptID, Timestamp: attemptFinished,
		Data: map[string]any{
			"status": "passed", "execution": workflow.ExecutionSucceeded,
			"decision": workflow.DecisionNotApplicable, "outcome": workflow.OutcomePassed,
			"manifest_sha256": manifestDigest,
		},
	}); err != nil {
		t.Fatal(err)
	}

	lifecycles, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := lifecycles.Load(first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	running := waiting
	running.Phase, running.NextStage, running.PendingApprovalID = lifecycle.PhaseRunning, "analyst", ""
	if err := lifecycles.Save(waiting, running); err != nil {
		t.Fatal(err)
	}

	if _, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir}); !errors.Is(err, ErrStaleQuestionApproval) {
		t.Fatalf("resume should fail closed after successful attempt_finished without transition_selected: %v", err)
	}
	if rt.calls["analyst"] != 1 {
		t.Fatalf("resume must not retry analyst without the clarification answer; calls=%d", rt.calls["analyst"])
	}
}

type clarificationRecoveryFixture struct {
	dir       string
	pipeline  *Pipeline
	runtime   *scriptedRuntime
	first     RunResult
	approvals *approval.Store
	evidence  EvidenceStore
	graph     workflow.Graph
	observed  struct {
		returnFeedback string
		hasAnswer      bool
	}
}

func newClarificationRecoveryFixture(t *testing.T) *clarificationRecoveryFixture {
	t.Helper()
	dir := env(t)
	if err := os.WriteFile(filepath.Join(dir, "coder.go"), []byte("package recovery\nconst Revision = 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rt := newScripted()
	rt.blocked["analyst"] = "need buyer context"
	fixture := &clarificationRecoveryFixture{dir: dir, runtime: rt}
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name == "coder" {
			if err := os.WriteFile(filepath.Join(dir, "coder.go"), []byte("package recovery\nconst Revision = 1\n"), 0644); err != nil {
				t.Error(err)
			}
			return
		}
		if name != "analyst" {
			return
		}
		if rt.calls[name] == 1 {
			path := filepath.Join(dir, ".ai-team", "artifacts", "tasks", "feat", "questions.md")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("Who is the buyer?\n"), 0644); err != nil {
				t.Fatal(err)
			}
			return
		}
		delete(rt.blocked, "analyst")
		for _, input := range inputs {
			switch input.Name {
			case "clarification-answer":
				fixture.observed.hasAnswer = true
			case "human-return-feedback":
				data, err := os.ReadFile(input.Path)
				if err != nil {
					t.Errorf("read recovered graph return feedback: %v", err)
					return
				}
				fixture.observed.returnFeedback = string(data)
			}
		}
	}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.MaxVisits["analyst"] = 4
		wf.MaxVisits["coder"] = 4
		wf.Edges = append(wf.Edges,
			config.WorkflowEdgeConfig{
				From: "analyst", Outcome: "blocked", To: "analyst",
				Approval: &config.WorkflowApprovalConfig{
					Roles: []string{"product_owner"}, Quorum: "any",
					Actions: map[string]string{"answer_questions": "analyst", "stop": "$stop"},
				},
			},
			config.WorkflowEdgeConfig{
				From: "coder", Outcome: "rejected", To: "analyst",
				Approval: &config.WorkflowApprovalConfig{
					Roles: []string{"product_owner"}, Quorum: "any",
					Actions: map[string]string{"return_to_analyst": "analyst", "stop": "$stop"},
				},
			},
		)
	}, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "coder"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "clarify and continue", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("expected clarification approval, result=%+v err=%v", first, err)
	}
	approvals, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := approvals.Load(first.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	decided, err := approvals.Decide(first.RunID, pending.ID, approval.Decision{
		ActorID: "owner@example.com", ActorRole: "product_owner", Action: "answer_questions",
		Comment: "B2B buyers", SubjectHash: pending.SubjectHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	targetDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	answerStore := ControllerQuestionAnswerStore{TargetDir: targetDir}
	if _, err := answerStore.Prepare(decided); err != nil {
		t.Fatal(err)
	}
	evidenceStore, _, replayed, err := evidence.Resume(filepath.Join(dir, ".ai-team", "runs"), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "approval_decided", AttemptID: decided.AttemptID, Data: approvalEventData(decided)}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "transition_selected", AttemptID: decided.AttemptID, Stage: "analyst", Data: map[string]any{
		"from": "analyst", "outcome": "blocked", "edge_target": "analyst",
		"action": "answer_questions", "target": "analyst",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "attempts_invalidated", Data: map[string]any{
		"attempt_ids": []string{replayed.Attempts[0].AttemptID}, "reason": "approved_loopback",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "run_resumed"}); err != nil {
		t.Fatal(err)
	}
	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	proposalPath := filepath.Join(dir, ".ai-team", "artifacts", "feat", "proposal.md")
	if err := os.MkdirAll(filepath.Dir(proposalPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proposalPath, []byte("approved analyst output\n"), 0644); err != nil {
		t.Fatal(err)
	}
	analystAttemptID := evidenceStore.NewAttemptID("analyst", 2)
	proposal := evidence.Artifact{Name: "proposal", Path: proposalPath}
	appendSyntheticFinishedAttempt(t, evidenceStore, filepath.Join(dir, ".ai-team", "artifacts"), analystAttemptID,
		"analyst", graph.Index("analyst")+1,
		workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionNotApplicable, Outcome: workflow.OutcomePassed},
		[]evidence.Artifact{proposal})
	handoffSubject := sha256.Sum256([]byte("analyst passed to coder"))
	handoff, err := approvals.Create(approval.PendingApproval{
		RunID: first.RunID, AttemptID: analystAttemptID, FromStage: "analyst", ToStage: "coder",
		Trigger: "graph_outcome:passed", SubjectHash: fmt.Sprintf("%x", handoffSubject[:]),
		RequiredRoles: []string{"operator"}, Quorum: approval.QuorumAny,
		Actions: []string{"approve", "reject"}, Targets: map[string]string{"approve": "coder", "reject": "$stop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "approval_requested", AttemptID: analystAttemptID, Data: approvalEventData(handoff)}); err != nil {
		t.Fatal(err)
	}
	handoff, err = approvals.Decide(first.RunID, handoff.ID, approval.Decision{
		ActorID: "operator@example.com", ActorRole: "operator", Action: "approve", SubjectHash: handoff.SubjectHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "approval_decided", AttemptID: analystAttemptID, Data: approvalEventData(handoff)}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "transition_selected", AttemptID: analystAttemptID, Stage: "analyst", Data: map[string]any{
		"from": "analyst", "outcome": "passed", "edge_target": "coder", "action": "approve", "target": "coder",
	}}); err != nil {
		t.Fatal(err)
	}
	fixture.pipeline, fixture.first, fixture.approvals = p, first, approvals
	fixture.evidence, fixture.graph = evidenceStore, graph
	return fixture
}

func appendSyntheticFinishedAttempt(t *testing.T, store EvidenceStore, artifactRoot, attemptID, stage string, stageIndex int,
	state workflow.AttemptState, outputs []evidence.Artifact) {
	t.Helper()
	started := time.Now().UTC()
	finished := started
	if err := store.Append(evidence.Event{
		Type: "attempt_started", Stage: stage, AttemptID: attemptID, Timestamp: started,
		Data: map[string]any{"stage_index": stageIndex},
	}); err != nil {
		t.Fatal(err)
	}
	manifest := evidence.AttemptManifest{
		SchemaVersion: evidence.SchemaVersion, RunID: store.RunID(), AttemptID: attemptID,
		Stage: stage, StageIndex: stageIndex, StartedAt: started, FinishedAt: finished,
		Status: state.LegacyStatus(), Execution: string(state.Execution),
		Decision: string(state.Decision), Outcome: string(state.Outcome),
	}
	if err := store.PublishAttempt(manifest, artifactRoot, nil, outputs); err != nil {
		t.Fatal(err)
	}
	digest, _, err := evidence.AttemptManifestDigest(nil, store.RunDir(), store.RunID(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{
		Type: "attempt_finished", Stage: stage, AttemptID: attemptID, Timestamp: finished,
		Data: map[string]any{
			"status": state.LegacyStatus(), "execution": state.Execution,
			"decision": state.Decision, "outcome": state.Outcome, "manifest_sha256": digest,
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func setLifecycleRunningAtAnalyst(t *testing.T, dir, runID string) {
	t.Helper()
	store, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := store.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	running := waiting
	running.Phase, running.NextStage, running.PendingApprovalID = lifecycle.PhaseRunning, "analyst", ""
	if err := store.Save(waiting, running); err != nil {
		t.Fatal(err)
	}
}

func TestRun_ClarificationRecoveryReconcilesCompletedTargetTransition(t *testing.T) {
	fixture := newClarificationRecoveryFixture(t)
	setLifecycleRunningAtAnalyst(t, fixture.dir, fixture.first.RunID)
	result, err := fixture.pipeline.RunWithResult(context.Background(), RunConfig{
		ResumeRunID: fixture.first.RunID, TargetDir: fixture.dir, ApproveGates: true,
	})
	if err != nil || result.Outcome != workflow.RunCompleted {
		t.Fatalf("resume should reconcile the recorded analyst transition and run coder: result=%+v err=%v", result, err)
	}
	if fixture.runtime.calls["analyst"] != 1 || fixture.runtime.calls["coder"] != 1 {
		t.Fatalf("resume reran an already completed analyst target: calls=%+v", fixture.runtime.calls)
	}
}

func TestRun_LaterGraphReturnInputSupersedesOlderClarificationRecovery(t *testing.T) {
	fixture := newClarificationRecoveryFixture(t)
	coderAttemptID := fixture.evidence.NewAttemptID("coder", 3)
	appendSyntheticFinishedAttempt(t, fixture.evidence, filepath.Join(fixture.dir, ".ai-team", "artifacts"), coderAttemptID,
		"coder", fixture.graph.Index("coder")+1,
		workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionRejected, Outcome: workflow.OutcomeRejected}, nil)
	subject := sha256.Sum256([]byte("later graph return to analyst"))
	pending, err := fixture.approvals.Create(approval.PendingApproval{
		RunID: fixture.first.RunID, AttemptID: coderAttemptID, FromStage: "coder", ToStage: "analyst",
		Trigger: "graph_outcome:rejected", SubjectHash: fmt.Sprintf("%x", subject[:]),
		RequiredRoles: []string{"product_owner"}, Quorum: approval.QuorumAny,
		Actions: []string{"return_to_analyst", "stop"},
		Targets: map[string]string{"return_to_analyst": "analyst", "stop": "$stop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.evidence.Append(evidence.Event{Type: "approval_requested", AttemptID: coderAttemptID, Data: approvalEventData(pending)}); err != nil {
		t.Fatal(err)
	}
	decided, err := fixture.approvals.Decide(fixture.first.RunID, pending.ID, approval.Decision{
		ActorID: "owner@example.com", ActorRole: "product_owner", Action: "return_to_analyst",
		Comment: "Use the latest graph return input.", SubjectHash: pending.SubjectHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.evidence.Append(evidence.Event{Type: "approval_decided", AttemptID: coderAttemptID, Data: approvalEventData(decided)}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.evidence.Append(evidence.Event{Type: "transition_selected", AttemptID: coderAttemptID, Stage: "coder", Data: map[string]any{
		"from": "coder", "outcome": "rejected", "edge_target": "analyst",
		"action": "return_to_analyst", "target": "analyst",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.evidence.Append(evidence.Event{Type: "run_resumed"}); err != nil {
		t.Fatal(err)
	}
	setLifecycleRunningAtAnalyst(t, fixture.dir, fixture.first.RunID)

	result, err := fixture.pipeline.RunWithResult(context.Background(), RunConfig{
		ResumeRunID: fixture.first.RunID, TargetDir: fixture.dir, ApproveGates: true,
	})
	if err != nil || (result.Outcome != workflow.RunCompleted && result.Outcome != workflow.RunCompletedWithWarnings) {
		t.Fatalf("resume should use the later graph handoff: result=%+v err=%v", result, err)
	}
	if fixture.runtime.calls["analyst"] != 2 || !strings.Contains(fixture.observed.returnFeedback, "Use the latest graph return input.") {
		t.Fatalf("later graph return input was not delivered: calls=%+v feedback=%q", fixture.runtime.calls, fixture.observed.returnFeedback)
	}
	if fixture.observed.hasAnswer {
		t.Fatal("older clarification answer preempted the later graph return input")
	}
}

func TestRun_ResumeRecoversPinnedReturnInputAfterCrash(t *testing.T) {
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
	rt.contentFn["reviewer"] = func(int) map[string]string {
		return map[string]string{"review": "**Verdict:** CHANGES_REQUESTED\nReview v1\n"}
	}
	var selectedReview, returnFeedback string
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name == "coder" {
			for _, input := range inputs {
				if input.Name == "review" {
					data, readErr := os.ReadFile(input.Path)
					if readErr != nil {
						t.Errorf("read pinned review: %v", readErr)
					}
					selectedReview = string(data)
				}
				if input.Name == "human-return-feedback" {
					data, readErr := os.ReadFile(input.Path)
					if readErr != nil {
						t.Errorf("read return feedback: %v", readErr)
					}
					returnFeedback = string(data)
				}
			}
			_ = os.WriteFile(filepath.Join(rt.targetDir, "coder.go"), []byte("package retry\nconst Revision = 1\n"), 0644)
		}
	}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.Entry = "reviewer"
		wf.MaxVisits["reviewer"] = 2
		wf.MaxVisits["coder"] = 2
		for index := range wf.Edges {
			if wf.Edges[index].From == "coder" && wf.Edges[index].Outcome == "passed" {
				wf.Edges[index].To = workflow.TerminalComplete
				wf.Edges[index].Approval = nil
			}
		}
		wf.Edges = append(wf.Edges, loopbackEdge("reviewer", "coder", "reviewer"))
	}, config.AgentConfig{Name: "coder"}, config.AgentConfig{Name: "reviewer"})
	registry := agent.NewFS(fstest.MapFS{
		"reviewer/def.yaml": def(`name: reviewer
runtime: agentcli
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED, REJECTED]
inputs: {}
outputs:
  review: '{feature}/review.md'
`),
		"reviewer/prompt.md": def("review"),
		"coder/def.yaml": def(`name: coder
runtime: agentcli
prompt_file: prompt.md
mutation: source
allowed_paths: ['**']
require_diff: true
inputs:
  review: '{feature}/review.md'
outputs: {}
`),
		"coder/prompt.md": def("code"),
	})
	p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "revise review", TargetDir: dir})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("expected return approval, result=%+v err=%v", first, err)
	}
	approvals, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := approvals.Load(first.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, ".ai-team", "runs", first.RunID, "attempts", pending.AttemptID, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var attemptManifest evidence.AttemptManifest
	if err := json.Unmarshal(manifestData, &attemptManifest); err != nil {
		t.Fatal(err)
	}
	var reviewPath string
	for _, output := range attemptManifest.Outputs {
		if output.Name == "review" {
			reviewPath = output.EvidencePath
		}
	}
	if reviewPath == "" {
		t.Fatalf("review output missing: %+v", attemptManifest.Outputs)
	}
	source, err := os.ReadFile(filepath.Join(dir, ".ai-team", "runs", first.RunID, filepath.FromSlash(reviewPath)))
	if err != nil {
		t.Fatal(err)
	}
	humanStore, err := humanartifact.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := humanStore.Append(first.RunID, reviewPath, "", fmt.Sprintf("%x", sha256.Sum256(source)),
		"Human-pinned review vA\n", "freeze this review", "qa-1")
	if err != nil {
		t.Fatal(err)
	}
	decided, err := approvals.Decide(first.RunID, pending.ID, approval.Decision{
		ActorID: "qa-1", ActorRole: "reviewer", Action: "return_to_coder",
		Comment: "Use the exact corrected review.", SubjectHash: pending.SubjectHash,
		ArtifactRevisions: map[string]string{reviewPath: revision.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Persist the durable decision and lifecycle handoff, then simulate the
	// coder starting and writing partial work before the process crashes.
	evidenceStore, _, _, err := evidence.Resume(filepath.Join(dir, ".ai-team", "runs"), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "approval_decided", AttemptID: decided.AttemptID, Data: approvalEventData(decided)}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "transition_selected", AttemptID: decided.AttemptID, Stage: "reviewer", Data: map[string]any{
		"from": "reviewer", "outcome": "rejected", "edge_target": "coder", "action": "return_to_coder", "target": "coder",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "run_resumed"}); err != nil {
		t.Fatal(err)
	}
	compiledGraph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	crashedCoderAttempt := evidenceStore.NewAttemptID("coder", 100)
	if err := evidenceStore.Append(evidence.Event{Type: "attempt_started", AttemptID: crashedCoderAttempt, Stage: "coder", Timestamp: time.Now().UTC(), Data: map[string]any{
		"stage_index": compiledGraph.Index("coder") + 1,
	}}); err != nil {
		t.Fatal(err)
	}
	lifecycles, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := lifecycles.Load(first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	running := waiting
	running.Phase, running.NextStage, running.PendingApprovalID = lifecycle.PhaseRunning, "coder", ""
	if err := lifecycles.Save(waiting, running); err != nil {
		t.Fatal(err)
	}

	second, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil {
		t.Fatalf("resume after handoff crash: result=%+v err=%v", second, err)
	}
	if second.RunID != first.RunID || second.Outcome != workflow.RunCompletedWithWarnings {
		t.Fatalf("wrong resumed outcome: %+v", second)
	}
	if !strings.Contains(selectedReview, "Human-pinned review vA") || !strings.Contains(returnFeedback, "Use the exact corrected review.") {
		t.Fatalf("pinned handoff input lost after recovery: review=%q feedback=%q", selectedReview, returnFeedback)
	}
	stale, err := recoveredGraphInputApproval(approvals, first.RunID, "coder", compiledGraph, evidence.ReplayedRun{
		Attempts: []evidence.ReplayedAttempt{{
			AttemptID: pending.AttemptID, Stage: "reviewer", StageIndex: 2,
			StartedAt: time.Now().Add(-2 * time.Minute), FinishedAt: time.Now().Add(-time.Minute),
			Status: "rejected", State: workflow.AttemptState{Execution: workflow.ExecutionSucceeded, Decision: workflow.DecisionRejected, Outcome: workflow.OutcomeRejected},
		}, {AttemptID: "later-coder-attempt", Stage: "coder", FinishedAt: time.Now().UTC(), Status: "passed"}},
		ApprovalDecisions: []evidence.ReplayedApprovalDecision{{
			Sequence: 10,
			ID:       decided.ID, SubjectHash: decided.SubjectHash, AttemptID: decided.AttemptID,
			FromStage: decided.FromStage, ToStage: decided.ToStage, Trigger: decided.Trigger, Action: decided.ResolvedAction,
		}},
		Transitions: []evidence.ReplayedTransition{{
			Sequence:  11,
			AttemptID: decided.AttemptID, From: decided.FromStage, Outcome: "rejected",
			EdgeTarget: decided.ToStage, Action: decided.ResolvedAction, Target: decided.Targets[decided.ResolvedAction],
		}},
	})
	if err != nil || stale != nil {
		t.Fatalf("must not reuse a return approval after its target stage has started: approval=%+v err=%v", stale, err)
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
	var feedbackContent string
	var revisedArtifactContent string
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name != "coder" {
			return
		}
		if rt.calls["coder"] == 2 {
			for _, input := range inputs {
				secondCoderInputs = append(secondCoderInputs, input.Name)
				if input.Name == "human-return-feedback" {
					data, readErr := os.ReadFile(input.Path)
					if readErr != nil {
						t.Errorf("read returned feedback: %v", readErr)
					}
					feedbackContent = string(data)
				} else if input.Name == "review" {
					data, readErr := os.ReadFile(input.Path)
					if readErr != nil {
						t.Errorf("read selected revision: %v", readErr)
					}
					revisedArtifactContent = string(data)
				}
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
		decision := approval.Decision{
			ActorID: "human-1", ActorRole: step.role, Action: step.action,
			SubjectHash: required.SubjectHash,
		}
		if strings.HasPrefix(step.action, "return_to_") {
			decision.Comment = "Исправь замечание reviewer и пересмотри scope."
			pending, loadErr := store.Load(result.RunID, required.ApprovalID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			manifestData, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "runs", result.RunID, "attempts", pending.AttemptID, "manifest.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var manifest evidence.AttemptManifest
			if err := json.Unmarshal(manifestData, &manifest); err != nil {
				t.Fatal(err)
			}
			var reviewPath string
			for _, output := range manifest.Outputs {
				if output.Name == "review" {
					reviewPath = output.EvidencePath
				}
			}
			if reviewPath == "" {
				t.Fatalf("review output missing from manifest: %+v", manifest.Outputs)
			}
			humanStore, err := humanartifact.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			sourceData, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "runs", result.RunID, filepath.FromSlash(reviewPath)))
			if readErr != nil {
				t.Fatal(readErr)
			}
			baseSHA := fmt.Sprintf("%x", sha256.Sum256(sourceData))
			revision, appendErr := humanStore.Append(result.RunID, reviewPath, "", baseSHA,
				"Human-edited review: fix the missing edge case.\n", "QA correction", "product-1")
			if appendErr != nil {
				t.Fatal(appendErr)
			}
			decision.ArtifactRevisions = map[string]string{reviewPath: revision.ID}
		}
		if _, err := store.Decide(result.RunID, required.ApprovalID, decision); err != nil {
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
	if !containsString(secondCoderInputs, "human-return-feedback") || !strings.Contains(feedbackContent, "Исправь замечание reviewer") ||
		!strings.Contains(feedbackContent, "review.md") || !strings.Contains(feedbackContent, "SHA-256") {
		t.Fatalf("исправляющий coder не получил неизменяемый feedback: inputs=%v content=%q", secondCoderInputs, feedbackContent)
	}
	if revisedArtifactContent != "Human-edited review: fix the missing edge case.\n" {
		t.Fatalf("coder did not consume the exact human-selected immutable revision: %q", revisedArtifactContent)
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

func TestRun_AllAutoV5TemplateStillRequiresExplicitDeliveryApproval(t *testing.T) {
	dir := env(t)
	_ = prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	allAutoConfig := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "all-auto-delivery-test",
		Title:         "All auto delivery test",
		CLI:           "opencode",
		PipelineAgents: []config.AgentConfig{
			{Name: "approver"}, {Name: "deployer"},
		},
		Stages: []config.TemplateStage{
			{ID: "approver", Title: "Approve", Function: "operator", Result: "approve", Executor: "agent", Agent: "approver", Confirm: "auto"},
			{ID: "deployer", Title: "Deliver", Function: "deployer", Result: "link", LinkKind: "pr", Executor: "agent", Agent: "deployer", Confirm: "auto"},
		},
	}
	p := New(allAutoConfig, deliveryRegistry(),
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

	// V0-9: deferral → контроллер исполнил доставку строго один раз
	// (post-terminal). Terminal delivery.json фиксирует commit + trailers,
	// привязанные к evidence run.
	runDir := onlyRunDir(t, dir)
	record, ok, readErr := delivery.ReadTerminalRecord(runDir)
	if readErr != nil || !ok {
		t.Fatalf("terminal delivery record не записан: err=%v ok=%v", readErr, ok)
	}
	if record.CommitSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || record.PlanHash != approvedPlanHash {
		t.Fatalf("terminal record не согласован: %+v", record)
	}
	if len(record.Trailers) != 3 {
		t.Fatalf("expected 3 commit trailers, got %v", record.Trailers)
	}
	runID := filepath.Base(runDir)
	for _, trailer := range record.Trailers {
		switch {
		case strings.HasPrefix(trailer, delivery.TrailerRunID+": "):
			if trailer != delivery.TrailerRunID+": "+runID {
				t.Fatalf("trailer run id mismatch: %q != %q", trailer, runID)
			}
		case strings.HasPrefix(trailer, delivery.TrailerRuntime+": "):
			if record.RuntimeIdentity == "" {
				t.Fatalf("runtime identity не заполнена")
			}
		case strings.HasPrefix(trailer, delivery.TrailerAttestation+": "):
			if record.AttestationSHA256 == "" || !strings.Contains(trailer, record.AttestationSHA256) {
				t.Fatalf("attestation trailer не согласован: %q", trailer)
			}
		default:
			t.Fatalf("неожиданный trailer: %q", trailer)
		}
	}
}

func TestDeliverDaemonRejectsAlreadyDeliveredRun(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	service := &fakeDeliveryService{}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service))
	if err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
	}); err != nil {
		t.Fatalf("run должен завершиться с deferred delivery: %v", err)
	}
	runDir := onlyRunDir(t, dir)
	runID := filepath.Base(runDir)

	// Exact retry returns the recorded result and does not invoke delivery again.
	retry, err := New(nil, nil, WithDeliveryApprovalHash(approvedPlanHash)).DeliverDeferred(context.Background(), runDir, "", dir)
	if err != nil || retry.CommitSHA == "" || retry.PlanHash != approvedPlanHash || service.calls != 1 {
		t.Fatalf("exact retry should be idempotent: record=%+v calls=%d err=%v", retry, service.calls, err)
	}
	// Неизвестный run id также отклоняется.
	if _, err := New(nil, nil).DeliverDeferred(context.Background(), filepath.Join(dir, ".ai-team", "runs", runID+"-nope"), "", dir); err == nil {
		t.Fatal("доставка несуществующего run должна быть отклонена")
	}
}

func TestDeliverDeferredDoesNotTrustWorkerOrLegacyTerminalRecordForRetry(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	failed := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(&gracefulDeliveryService{}))
	if err := failed.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
	}); err == nil {
		t.Fatal("post-terminal delivery failure should leave a retryable run")
	}
	runDir := onlyRunDir(t, dir)
	runID := filepath.Base(runDir)
	attestationDigest, err := attestationDigestOfRun(runDir)
	if err != nil {
		t.Fatal(err)
	}
	runtimeIdentity, err := runtimeIdentityOfRun(runDir)
	if err != nil {
		t.Fatal(err)
	}
	forged := delivery.TerminalRecord{
		SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: runID, Feature: "feat",
		PlanHash: approvedPlanHash, CommitSHA: strings.Repeat("b", 40), PRURL: "https://example.test/pr/forged",
		AttestationSHA256: attestationDigest, RuntimeIdentity: runtimeIdentity,
		Trailers: []string{
			delivery.TrailerRunID + ": " + runID,
			delivery.TrailerRuntime + ": " + runtimeIdentity,
			delivery.TrailerAttestation + ": " + attestationDigest,
		},
		PerformedAt: time.Now().UTC(),
	}
	if err := delivery.WriteControllerTerminalRecord(dir, runID, forged); err != nil {
		t.Fatalf("seed worker-submitted controller mirror: %v", err)
	}
	if err := delivery.WriteTerminalRecord(runDir, forged); err != nil {
		t.Fatalf("seed legacy run-local record: %v", err)
	}
	service := &fakeDeliveryService{}
	record, err := New(nil, nil, WithDeliveryService(service), WithDeliveryApprovalHash(approvedPlanHash)).DeliverDeferred(context.Background(), runDir, "", dir)
	if err != nil {
		t.Fatalf("retry must execute despite self-consistent worker/legacy records: %v", err)
	}
	if service.calls != 1 || record.CommitSHA == forged.CommitSHA || record.PRURL == forged.PRURL {
		t.Fatalf("retry trusted forged record instead of executing: record=%+v forged=%+v calls=%d", record, forged, service.calls)
	}
	trusted, found, err := delivery.ReadControllerDeliveryReceipt(dir, runID)
	if err != nil || !found || trusted.CommitSHA != record.CommitSHA {
		t.Fatalf("trusted retry receipt missing: record=%+v found=%v err=%v", trusted, found, err)
	}
	retried, err := New(nil, nil, WithDeliveryService(service), WithDeliveryApprovalHash(approvedPlanHash)).DeliverDeferred(context.Background(), runDir, "", dir)
	if err != nil || service.calls != 1 || retried.CommitSHA != record.CommitSHA {
		t.Fatalf("receipt-backed exact retry must not execute twice: record=%+v calls=%d err=%v", retried, service.calls, err)
	}
}

func TestDeliverDeferredRequiresControllerResolvedExactApprovalAfterForgedMarker(t *testing.T) {
	dir := env(t)
	planHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	failed := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(&gracefulDeliveryService{}))
	if err := failed.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: planHash,
	}); err == nil {
		t.Fatal("post-terminal delivery failure should leave a retryable run")
	}
	runDir := onlyRunDir(t, dir)
	runID := filepath.Base(runDir)
	logPath := filepath.Join(runDir, "events.jsonl")
	rewriteEventLogWithWorkerMarkerEdit(t, logPath, runID)
	if _, err := firstDeferredMarker(runDir, nil); err != nil {
		t.Fatalf("worker-edited but re-hashed event log should still parse its marker: %v", err)
	}

	approvalStore, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	values, err := approvalStore.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	var original approval.PendingApproval
	for _, value := range values {
		if value.Trigger == "delivery_plan" {
			original = value
			break
		}
	}
	if original.ID == "" {
		t.Fatalf("expected exact plan approval row among %d approvals", len(values))
	}
	approvalPath := filepath.Join(dir, ".ai-team", "state", "approvals", runID, original.ID+".json")
	service := &fakeDeliveryService{}
	assertDenied := func(name string) {
		t.Helper()
		_, err := New(nil, nil, WithDeliveryService(service)).DeliverDeferred(context.Background(), runDir, "", dir)
		if err == nil || !strings.Contains(err.Error(), "approval") || service.calls != 0 {
			t.Fatalf("%s approval must deny trusted retry before Execute: calls=%d err=%v", name, service.calls, err)
		}
	}

	if err := os.Remove(approvalPath); err != nil {
		t.Fatal(err)
	}
	assertDenied("missing")

	pending := original
	pending.Status = approval.StatusPending
	pending.ResolvedAction = ""
	pending.ResolvedAt = time.Time{}
	pending.Decisions = nil
	pending.ArtifactRevisions = nil
	pending.ArtifactRevisionBindingSHA256 = ""
	writeApprovalFixture(t, approvalPath, pending)
	assertDenied("unresolved")

	wrongHash := original
	wrongHash.SubjectHash = strings.Repeat("d", 64)
	for index := range wrongHash.Decisions {
		wrongHash.Decisions[index].SubjectHash = wrongHash.SubjectHash
	}
	writeApprovalFixture(t, approvalPath, wrongHash)
	assertDenied("wrong hash")
}

func rewriteEventLogWithWorkerMarkerEdit(t *testing.T, path, runID string) {
	t.Helper()
	events, err := evidence.VerifyEventLog(path, runID)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for index := range events {
		if events[index].Type == "delivery_deferred" {
			events[index].Data["worker_marker_note"] = "rewritten by worker"
			changed = true
			break
		}
	}
	if !changed || len(events) == 0 {
		t.Fatal("delivery_deferred event is missing from test run")
	}
	previous := events[0].PreviousSHA256
	var encoded bytes.Buffer
	for index := range events {
		events[index].PreviousSHA256 = previous
		events[index].SHA256 = ""
		canonical, err := json.Marshal(events[index])
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(canonical)
		events[index].SHA256 = fmt.Sprintf("%x", sum[:])
		previous = events[index].SHA256
		line, err := json.Marshal(events[index])
		if err != nil {
			t.Fatal(err)
		}
		encoded.Write(line)
		encoded.WriteByte('\n')
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeApprovalFixture(t *testing.T, path string, value approval.PendingApproval) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// gracefulDeliveryService не выполняет post-terminal доставку и не пишет
// terminal-запись: имитирует сбой post-terminal хука после terminal finalize.
type gracefulDeliveryService struct {
	calls int
}

func (f *gracefulDeliveryService) Execute(ctx context.Context, request delivery.Request) (delivery.Result, error) {
	f.calls++
	for {
		select {
		case <-ctx.Done():
			return delivery.Result{}, ctx.Err()
		default:
			return delivery.Result{}, errors.New("post-terminal hook сбой: доставка не выполнена")
		}
	}
}

// TestDeliverDeferredRetriesFailedHook — позитивный путь DeliverDeferred через
// evidence.Resume (V0-9 blocker): run терминальный completed, но post-terminal
// хук упал (terminal record не записан). Повтор доставки через CLI-путь обязан
// разрешить evidence.Resume по корректному корню и выполнить controller.Execute.
func TestDeliverDeferredRetriesFailedHook(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	service := &gracefulDeliveryService{}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service))
	err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
	})
	if err == nil {
		t.Fatalf("post-terminal хук упал — Run обязан вернуть ошибку")
	}
	runDir := onlyRunDir(t, dir)
	runID := filepath.Base(runDir)
	if _, ok, readErr := delivery.ReadTerminalRecord(runDir); readErr != nil || ok {
		t.Fatalf("terminal record не должен существовать при сбойном хуке: ok=%v err=%v", ok, readErr)
	}

	// Retry-путь CLI: DeliverDeferred разрешает evidence через Resume с
	// корректным корнем (filepath.Dir(runDir), runID) и доставляет.
	okService := &fakeDeliveryService{}
	record, err := New(nil, nil, WithDeliveryService(okService), WithDeliveryApprovalHash(approvedPlanHash)).DeliverDeferred(context.Background(), runDir, "", dir)
	if err != nil {
		t.Fatalf("DeliverDeferred позитивный путь: %v", err)
	}
	if okService.calls != 1 || record.CommitSHA == "" || record.PlanHash != approvedPlanHash {
		t.Fatalf("controller вызовов=%d record=%+v", okService.calls, record)
	}
	if len(record.Trailers) != 3 {
		t.Fatalf("expected 3 trailers, got %v", record.Trailers)
	}
	for _, trailer := range record.Trailers {
		switch {
		case strings.HasPrefix(trailer, delivery.TrailerRunID+": "):
			if trailer != delivery.TrailerRunID+": "+runID {
				t.Fatalf("run id trailer mismatch: %q", trailer)
			}
		case strings.HasPrefix(trailer, delivery.TrailerRuntime+": "):
			if record.RuntimeIdentity == "" {
				t.Fatalf("runtime identity пустая")
			}
		case strings.HasPrefix(trailer, delivery.TrailerAttestation+": "):
			if record.AttestationSHA256 == "" || !strings.Contains(trailer, record.AttestationSHA256) {
				t.Fatalf("attestation trailer mismatch: %q", trailer)
			}
		}
	}
	// Exact retry returns the validated controller record without executing a
	// second push or pull request creation.
	retried, err := New(nil, nil, WithDeliveryService(okService), WithDeliveryApprovalHash(approvedPlanHash)).DeliverDeferred(context.Background(), runDir, "", dir)
	if err != nil || okService.calls != 1 || retried.CommitSHA != record.CommitSHA || retried.PRURL != record.PRURL {
		t.Fatalf("exact retry must return the original delivery: record=%+v calls=%d err=%v", retried, okService.calls, err)
	}

	// A changed prepared plan no longer matches the approved deferred marker;
	// the previous record must not authorize it.
	statePath := filepath.Join(dir, ".ai-team", "delivery", "feat.json")
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		t.Fatal(err)
	}
	var changedPlan delivery.Plan
	if err := json.Unmarshal(state["plan"], &changedPlan); err != nil {
		t.Fatal(err)
	}
	changedPlan.CommitMessage += " changed"
	changedHash, err := changedPlan.Hash()
	if err != nil {
		t.Fatal(err)
	}
	state["plan"], err = json.Marshal(changedPlan)
	if err != nil {
		t.Fatal(err)
	}
	state["plan_hash"], err = json.Marshal(changedHash)
	if err != nil {
		t.Fatal(err)
	}
	stateBytes, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, stateBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(nil, nil, WithDeliveryService(okService), WithDeliveryApprovalHash(approvedPlanHash)).DeliverDeferred(context.Background(), runDir, "", dir); err == nil || !strings.Contains(err.Error(), "plan hash mismatch") {
		t.Fatalf("changed plan must require new approval, got: %v", err)
	}
	if okService.calls != 1 {
		t.Fatalf("changed plan must not execute delivery, calls=%d", okService.calls)
	}
}

// TestRun_CompletedWithWarningsRunsPostTerminalDelivery — AUD-06: post-terminal
// deferred доставка должна выполняться не только для completed, но и для
// completed_with_warnings (необязательный check на stage дал warning). Итог
// фиксируется в terminal delivery record; сбой hook-а при warnings не
// маскируется успехом run (exit 0).
func TestRun_CompletedWithWarningsRunsPostTerminalDelivery(t *testing.T) {
	warningCheck := config.AgentConfig{
		Name: "approver",
		Checks: []checks.Definition{{
			Name: "optional-neta", Class: "security",
			Command: []string{"ai-team-tool-that-does-not-exist"}, Policy: checks.PolicyOptional,
		}},
	}

	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	service := &fakeDeliveryService{}
	p := New(cfgFor(warningCheck, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service))
	result, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
	})
	if err != nil {
		t.Fatalf("completed_with_warnings run обязан выполнить post-terminal delivery: %v", err)
	}
	if result.Outcome != workflow.RunCompletedWithWarnings {
		t.Fatalf("ожидался outcome %q, got %q", workflow.RunCompletedWithWarnings, result.Outcome)
	}
	if service.calls != 1 {
		t.Fatalf("post-terminal delivery должен выполняться и при warnings, calls=%d", service.calls)
	}
	runDir := onlyRunDir(t, dir)
	record, ok, readErr := delivery.ReadTerminalRecord(runDir)
	if readErr != nil || !ok {
		t.Fatalf("terminal delivery record не записан: err=%v ok=%v", readErr, ok)
	}
	if record.CommitSHA == "" || record.PlanHash != approvedPlanHash {
		t.Fatalf("terminal record не согласован: %+v", record)
	}

	// Сбой доставки при warnings не должен маскироваться успехом run.
	dir2 := env(t)
	approvedPlanHash2 := prepareDelivery(t, dir2)
	rt2 := newScripted()
	rt2.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	failing := &gracefulDeliveryService{}
	p2 := New(cfgFor(warningCheck, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt2.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(failing))
	if err := p2.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir2, ApproveGates: true, ApprovePlanHash: approvedPlanHash2,
	}); err == nil {
		t.Fatal("сбой post-terminal delivery при warnings обязан вернуть ошибку (exit 0 не маскирует delivery result)")
	}
}

// TestDeliverDeferredRetriesFailedHookFromCandidateWorktree — AUD-05: retry
// доставки Git-run'а обязан резолвить candidate worktree из state_path
// delivery_deferred event, а не читать prepared plan в control target (decoy).
// Сбой post-terminal хука → DeliverDeferred(runDir, "", controlRoot) продолжает
// exact candidate: тот же approved plan, тот же workspace, без чтения decoy
// плана и без нового LLM-вызова (delivery — controller-owned).
func TestDeliverDeferredRetriesFailedHookFromCandidateWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	dir := env(t)
	gitInit(t, dir)
	for name, content := range map[string]string{
		".gitignore":     ".ai-team/\n",
		"go.mod":         "module example.test/retry\n\ngo 1.26\n",
		"change_test.go": "package change\n\nimport \"testing\"\n\nfunc TestPrepared(t *testing.T) {}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-qm", "init"},
		{"branch", "-M", "main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	for _, args := range [][]string{{"init", "--bare", remote}, {"remote", "add", "origin", remote}} {
		cmd := exec.Command("git", args...)
		if args[0] == "remote" {
			cmd.Dir = dir
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name == "coder" {
			// Мутация пишется в candidate worktree (rt.targetDir), как вёл бы
			// себя реальный agent CLI в worktree Git-run'а.
			if err := os.WriteFile(filepath.Join(rt.targetDir, "change.go"), []byte("package change\n"), 0644); err != nil {
				t.Fatalf("coder mutation: %v", err)
			}
		}
	}
	service := &gracefulDeliveryService{}
	p := New(cfgFor(
		config.AgentConfig{Name: "coder", Checks: []checks.Definition{{
			Name: "candidate-tests", Class: "unit", Adapter: checks.AdapterGoTest,
			Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: checks.PolicyRequired,
		}}},
		config.AgentConfig{Name: "approver"},
		config.AgentConfig{Name: "deployer"},
	), deliveryRegistry(), WithRuntimeFactory(rt.factory),
		WithPrompter(&scriptedPrompter{interactive: true, answers: []string{"y"}}),
		WithDeliveryService(service))
	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true})
	if err == nil {
		t.Fatal("post-terminal хук упал — Run обязан вернуть ошибку")
	}
	// Decoy plan — рукам подготовка плана в control target (feature "feat").
	// После run: candidate уже создан, clean-workspace требования нет. Hash
	// decoy заведомо отличается от in-run плана candidate worktree: старая
	// реализация читала control root и падала бы на hash mismatch.
	prepareDelivery(t, dir)
	runDir := onlyRunDir(t, dir)
	runID := filepath.Base(runDir)
	if _, ok, readErr := delivery.ReadTerminalRecord(runDir); readErr != nil || ok {
		t.Fatalf("terminal record не должен существовать при сбойном хуке: ok=%v err=%v", ok, readErr)
	}

	// Workspace из delivery_deferred event — candidate worktree run'а.
	var marker struct {
		PlanHash  string `json:"plan_hash"`
		StatePath string `json:"state_path"`
	}
	events, readEventsErr := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if readEventsErr != nil {
		t.Fatal(readEventsErr)
	}
	foundMarker := false
	for _, line := range strings.Split(string(events), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Type string            `json:"type"`
			Data map[string]string `json:"data"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "delivery_deferred" {
			continue
		}
		marker.PlanHash, marker.StatePath = ev.Data["plan_hash"], ev.Data["state_path"]
		foundMarker = true
		break
	}
	if !foundMarker || marker.StatePath == "" {
		t.Fatalf("delivery_deferred маркер должен содержать state_path: found=%v marker=%+v", foundMarker, marker)
	}
	worktree := filepath.FromSlash(filepath.Dir(filepath.Dir(filepath.Dir(marker.StatePath))))
	expectedWorktree := filepath.Join(canonicalPath(dir), ".ai-team", "worktrees", runID)
	if worktree != expectedWorktree {
		t.Fatalf("ожидался candidate worktree %s, got %s (state_path=%s)", expectedWorktree, worktree, marker.StatePath)
	}

	okService := &capturingDeliveryService{}
	record, err := New(nil, nil, WithDeliveryService(okService), WithDeliveryApprovalHash(marker.PlanHash)).DeliverDeferred(context.Background(), runDir, "", dir)
	if err != nil {
		t.Fatalf("DeliverDeferred из control target: %v", err)
	}
	if okService.calls != 1 || len(okService.targets) != 1 || okService.targets[0] != worktree {
		t.Fatalf("доставка выполнена не в candidate worktree: calls=%d targets=%v", okService.calls, okService.targets)
	}
	if record.CommitSHA == "" || record.PlanHash != marker.PlanHash {
		t.Fatalf("terminal record не согласован с approved plan: %+v vs %s", record, marker.PlanHash)
	}
	if len(record.Trailers) != 3 {
		t.Fatalf("expected 3 trailers, got %v", record.Trailers)
	}
	for _, trailer := range record.Trailers {
		switch {
		case strings.HasPrefix(trailer, delivery.TrailerRunID+": "):
			if trailer != delivery.TrailerRunID+": "+runID {
				t.Fatalf("run id trailer mismatch: %q", trailer)
			}
		case strings.HasPrefix(trailer, delivery.TrailerRuntime+": "):
			if record.RuntimeIdentity == "" {
				t.Fatalf("runtime identity пустая")
			}
		case strings.HasPrefix(trailer, delivery.TrailerAttestation+": "):
			if record.AttestationSHA256 == "" || !strings.Contains(trailer, record.AttestationSHA256) {
				t.Fatalf("attestation trailer mismatch: %q", trailer)
			}
		}
	}
	// Нет нового LLM-вызова: deployer controller-owned, run исполнял только
	// coder и approver.
	if rt.calls["deployer"] != 0 || len(rt.executed) != 2 {
		t.Fatalf("не должно быть LLM-вызовов при repeat доставки: executed=%v calls=%v", rt.executed, rt.calls)
	}
	// Prepared plan существует в candidate worktree (decoy в control target,
	// куда его положил prepareDelivery, — не читался).
	if _, found, loadErr := delivery.LoadPreparedPlan(worktree, "feat"); loadErr != nil || !found {
		t.Fatalf("prepared plan в worktree обязан существовать: found=%v err=%v", found, loadErr)
	}
	// Exact retry returns the original result without another controller call.
	retried, err := New(nil, nil, WithDeliveryService(okService), WithDeliveryApprovalHash(marker.PlanHash)).DeliverDeferred(context.Background(), runDir, "", dir)
	if err != nil || okService.calls != 1 || retried.CommitSHA != record.CommitSHA || retried.PRURL != record.PRURL {
		t.Fatalf("exact retry must be idempotent: record=%+v calls=%d err=%v", retried, okService.calls, err)
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

func TestRun_DeliveryApprovalAfterRestartRequiresCurrentAuthority(t *testing.T) {
	for _, test := range []struct {
		name               string
		storeKind          string
		controllerDecision bool
		resumeHash         bool
		wrongHash          bool
		wantExecution      bool
	}{
		{name: "resolved filesystem record is not authority", storeKind: "filesystem"},
		{name: "exact current hash authorizes filesystem record", storeKind: "filesystem", resumeHash: true, wantExecution: true},
		{name: "mismatched current hash is rejected", storeKind: "filesystem", wrongHash: true},
		{name: "direct SQLite decision is not authenticated", storeKind: "sqlite"},
		{name: "legacy file cannot claim authenticated provenance when imported into SQLite", storeKind: "legacy-import", controllerDecision: true},
		{name: "authenticated controller SQLite decision", storeKind: "sqlite", controllerDecision: true, wantExecution: true},
		{name: "authenticated controller decision survives worker store wrapper", storeKind: "worker-wrapper", controllerDecision: true, wantExecution: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := env(t)
			prepareDelivery(t, dir)
			rt := newScripted()
			rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
			service := &fakeDeliveryService{}
			var store ApprovalStore
			var decisionStore interface {
				Decide(string, string, approval.Decision) (approval.PendingApproval, error)
			}
			var fileStore *approval.Store
			var workerReadStore ApprovalStore
			if test.storeKind == "sqlite" || test.storeKind == "worker-wrapper" {
				if err := os.MkdirAll(filepath.Join(dir, ".ai-team"), 0700); err != nil {
					t.Fatal(err)
				}
				db, err := approval.NewSQLiteStore(filepath.Join(dir, ".ai-team", "web.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				decisionStore = db
				if test.storeKind == "worker-wrapper" {
					store = db
					workerReadStore = approval.NewWorkerStore(db)
				} else {
					store = db
				}
			} else {
				var err error
				fileStore, err = approval.NewStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				store = fileStore
				decisionStore = fileStore
			}
			p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
				WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service), WithApprovalStore(store))

			err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true})
			var required *ApprovalRequiredError
			if !errors.As(err, &required) || required.ApprovalID == "" {
				t.Fatalf("first delivery attempt must wait for a decision, got: %v", err)
			}
			value, err := store.Load(required.RunID, required.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			decision := approval.Decision{
				ActorID: "release-manager-1", ActorRole: deliveryApprovalRole,
				Action: "approve", SubjectHash: value.SubjectHash,
			}
			if test.controllerDecision {
				decision.ControllerAuthenticated = true
			}
			if _, err := decisionStore.Decide(value.RunID, value.ID, decision); err != nil {
				t.Fatal(err)
			}
			if test.storeKind == "legacy-import" {
				if err := os.MkdirAll(filepath.Join(dir, ".ai-team"), 0700); err != nil {
					t.Fatal(err)
				}
				db, err := approval.NewSQLiteStore(filepath.Join(dir, ".ai-team", "web.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				if err := db.ImportLegacy(filepath.Join(dir, ".ai-team", "state", "approvals")); err != nil {
					t.Fatal(err)
				}
				store = db
				p = New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
					WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service), WithApprovalStore(store))
			}
			if workerReadStore != nil {
				p = New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
					WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service), WithApprovalStore(workerReadStore))
			}

			resumeHash := ""
			if test.resumeHash {
				resumeHash = value.SubjectHash
			} else if test.wrongHash {
				resumeHash = strings.Repeat("f", 64)
			}
			err = p.Run(context.Background(), RunConfig{ResumeRunID: required.RunID, TargetDir: dir, ApprovePlanHash: resumeHash})
			if test.wantExecution {
				if err != nil {
					t.Fatalf("authenticated controller decision should resume delivery: %v", err)
				}
				if service.calls != 1 {
					t.Fatalf("trusted database approval should execute once, got %d", service.calls)
				}
			} else if test.wrongHash {
				if err == nil || !strings.Contains(err.Error(), "не совпадает с subject") {
					t.Fatalf("mismatched current hash must be rejected, got: %v", err)
				}
				if service.calls != 0 {
					t.Fatalf("mismatched hash must not execute delivery, calls=%d", service.calls)
				}
			} else {
				if !errors.As(err, &required) {
					t.Fatalf("untrusted resolved approval must still require current --approve-plan, got: %v", err)
				}
				if service.calls != 0 {
					t.Fatalf("untrusted approval must not execute delivery, calls=%d", service.calls)
				}
			}
		})
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
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(path, []byte("**Status:** BLOCKED\n**Blocker:** старый блокер\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := os.MkdirAll(filepath.Dir(proposal), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(proposal, []byte("старый output"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(proposal, old, old); err != nil {
		t.Fatalf("setup: %v", err)
	}

	rt := newScripted()
	rt.skipWrite["analyst"] = true
	err, _ := runPipeline(t, dir, cfgFor(config.AgentConfig{Name: "analyst"}), rt, &scriptedPrompter{})
	if err == nil || !strings.Contains(err.Error(), "не создан") {
		t.Fatalf("stale output должен быть отклонён, got: %v", err)
	}
}

// --- Loopback -------------------------------------------------------------------

// TestForwardPass_CoderDoesNotSeeFutureReview — contract test P1-9: на прямом
// forward-проходе во входы coder не должен попадать ни один будущий artifact
// ревью/теста/верификации, даже если он физически существует на диске. Канал
// доставки вердикт-артефактов в coder — ТОЛЬКО явное loopback-ребро.
func TestForwardPass_CoderDoesNotSeeFutureReview(t *testing.T) {
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
	rt.content["tester"] = map[string]string{"report": "tests ok\n\n**Result:** PASS\n"}
	rt.content["reviewer"] = map[string]string{"review": "# Ревью\n\nвсё ок\n\n**Verdict:** APPROVED\n"}

	var coderInputs [][]string
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name == "coder" {
			_ = os.WriteFile(filepath.Join(rt.targetDir, "coder.go"), []byte("package retry\nconst Revision = 1\n"), 0644)
			var names []string
			for _, in := range inputs {
				names = append(names, in.Name)
			}
			coderInputs = append(coderInputs, names)
		}
	}

	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"},
			config.AgentConfig{Name: "coder"},
			config.AgentConfig{Name: "tester"},
			config.AgentConfig{Name: "reviewer"},
			config.AgentConfig{Name: "deployer"}),
		rt, &scriptedPrompter{interactive: true, answers: []string{"y"}})
	if err != nil {
		t.Fatalf("forward-проход должен завершиться успехом: %v", err)
	}

	if rt.calls["coder"] != 1 {
		t.Fatalf("coder на прямом проходе должен выполниться ровно один раз, calls=%d", rt.calls["coder"])
	}
	if len(coderInputs) != 1 {
		t.Fatalf("ожидалось одно собрание входов coder, got %d", len(coderInputs))
	}

	if _, statErr := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", "review.md")); statErr != nil {
		t.Fatalf("review.md должен существовать после reviewer: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", "test-report.md")); statErr != nil {
		t.Fatalf("test-report.md должен существовать после tester: %v", statErr)
	}

	forbidden := []string{"review", "report", "verification", "test-report"}
	for _, name := range coderInputs[0] {
		for _, bad := range forbidden {
			if name == bad {
				t.Fatalf("coder получил будущий artifact %q во входах на прямом проходе: %v", bad, coderInputs[0])
			}
		}
	}
	joined := strings.Join(coderInputs[0], ",")
	if joined != "proposal" {
		t.Fatalf("входы coder на прямом проходе должны быть ровно [proposal], got [%s]", joined)
	}
}

// TestForwardPass_VerifierStageIsolation — contract test P1-9 на пути стадии
// verifier (R2 should-fix: раньше покрывались только coder→reviewer и
// coder→tester→reviewer). Полный standard-профиль c verifier: coder не видит
// будущий verification, а сам verifier получает входами ровно свой
// декларированный набор и не видит будущих артефактов deployer.
func TestForwardPass_VerifierStageIsolation(t *testing.T) {
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
	rt.content["tester"] = map[string]string{"report": "tests ok\n\n**Result:** PASS\n"}
	rt.content["reviewer"] = map[string]string{"review": "# Ревью\n\nвсё ок\n\n**Verdict:** APPROVED\n"}
	rt.content["verifier"] = map[string]string{"verification": "verified\n\n**Verdict:** APPROVED\n"}

	var coderInputs [][]string
	var verifierInputs [][]string
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		var names []string
		for _, in := range inputs {
			names = append(names, in.Name)
		}
		switch name {
		case "coder":
			_ = os.WriteFile(filepath.Join(rt.targetDir, "coder.go"), []byte("package retry\nconst Revision = 1\n"), 0644)
			coderInputs = append(coderInputs, names)
		case "verifier":
			verifierInputs = append(verifierInputs, names)
		}
	}

	err, _ := runPipeline(t, dir,
		cfgFor(config.AgentConfig{Name: "analyst"},
			config.AgentConfig{Name: "coder"},
			config.AgentConfig{Name: "reviewer"},
			config.AgentConfig{Name: "tester"},
			config.AgentConfig{Name: "verifier"},
			config.AgentConfig{Name: "deployer"}),
		rt, &scriptedPrompter{interactive: true, answers: []string{"y"}})
	if err != nil {
		t.Fatalf("standard-профиль c verifier должен завершиться успехом: %v", err)
	}
	if rt.calls["verifier"] != 1 {
		t.Fatalf("verifier должен выполниться ровно один раз, calls=%d", rt.calls["verifier"])
	}
	if rt.calls["coder"] != 1 {
		t.Fatalf("coder должен выполниться ровно один раз, calls=%d", rt.calls["coder"])
	}

	if _, statErr := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", "verification.md")); statErr != nil {
		t.Fatalf("verification.md должен существовать после verifier: %v", statErr)
	}

	// coder не видит будущий verification (как review/report/test-report).
	joined := strings.Join(coderInputs[0], ",")
	if joined != "proposal" {
		t.Fatalf("входы coder на прямом проходе должны быть ровно [proposal], got [%s]", joined)
	}
	for _, name := range coderInputs[0] {
		switch name {
		case "review", "report", "verification", "test-report":
			t.Fatalf("coder получил будущий artifact %q на проходе с verifier: %v", name, coderInputs[0])
		}
	}

	// verifier получает ровно свой декларированный набор, без будущих
	// артефактов deployer (не prepend-артефактов чужих стадий).
	if len(verifierInputs) != 1 {
		t.Fatalf("verifier запусков: %d", len(verifierInputs))
	}
	set := make(map[string]bool, len(verifierInputs[0]))
	for _, name := range verifierInputs[0] {
		set[name] = true
		if !isVerifierDeclaredInput(name) {
			t.Fatalf("verifier получил недекларированный вход %q: %v", name, verifierInputs[0])
		}
	}
	for _, want := range []string{"proposal", "review", "test-report"} {
		if !set[want] {
			t.Fatalf("verifier должен получить вход %q, got %v", want, verifierInputs[0])
		}
	}
}

// isVerifierDeclaredInput — declared input names стадии verifier в testRegistry.
func isVerifierDeclaredInput(name string) bool {
	switch name {
	case "proposal", "review", "test-report":
		return true
	}
	return false
}

// TestCoderSeesReviewOnlyAfterLoopback — бинарность канала (P1-9): на прямом
// проходе coder НЕ видит review, а после approved loopback-ребра видит. Пара к
// положительным loopback-тестам закрепляет «loopback — единственный канал».
func TestCoderSeesReviewOnlyAfterLoopback(t *testing.T) {
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
			return map[string]string{"review": "исправь\n\n**Verdict:** REJECTED\n"}
		}
		return map[string]string{"review": "теперь ок\n\n**Verdict:** APPROVED\n"}
	}
	rt.content["tester"] = map[string]string{"report": "tests ok\n\n**Result:** PASS\n"}

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
			wf.MaxVisits["tester"] = 3
			wf.MaxVisits["reviewer"] = 3
			wf.Edges = append(wf.Edges, loopbackEdge("reviewer", "coder", "reviewer"))
		},
			config.AgentConfig{Name: "analyst"},
			config.AgentConfig{Name: "coder"},
			config.AgentConfig{Name: "tester"},
			config.AgentConfig{Name: "reviewer"},
			config.AgentConfig{Name: "deployer"},
		),
		rt, pr)
	if err != nil {
		t.Fatalf("loopback должен завершиться успехом: %v", err)
	}
	if rt.calls["coder"] != 2 {
		t.Fatalf("coder должен выполниться дважды, calls=%d", rt.calls["coder"])
	}
	if len(coderInputs) != 2 {
		t.Fatalf("coder запусков: %d", len(coderInputs))
	}
	first := strings.Join(coderInputs[0], ",")
	if strings.Contains(first, "review") {
		t.Fatalf("coder на прямом проходе не должен видеть review: %s", first)
	}
	if first != "proposal" {
		t.Fatalf("forward входы coder должны быть ровно [proposal], got [%s]", first)
	}
	second := strings.Join(coderInputs[1], ",")
	if !strings.Contains(second, "review") {
		t.Fatalf("на retry coder должен получить review во входах: %s", second)
	}
}

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

	pr := &scriptedPrompter{interactive: true, answers: []string{"y", "reviewer замечание: исправить неверное поведение"}}
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

	cfg := cfgFor(config.AgentConfig{Name: "analyst", Timeout: "50ms"})
	p := New(cfg, testRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	result, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true,
	})
	if err == nil {
		t.Fatal("ожидалась ошибка таймаута стадии")
	}
	// stage-timeout — resumable sentinel, а НЕ превышение run budget:
	if !errors.Is(err, ErrStageTimeout) {
		t.Fatalf("ожидался ErrStageTimeout, got: %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stage-timeout не должен матчить context.DeadlineExceeded: %v", err)
	}
	if strings.Contains(err.Error(), "run budget") {
		t.Fatalf("stage-timeout не должен превращаться в бюджет-ошибку: %v", err)
	}
	// resume сохраняет run identity (resumable, а не терминальный бюджет).
	if result.RunID == "" {
		t.Fatalf("stage-timeout должен быть resumable: %+v err=%v", result, err)
	}
	stateStore, _ := lifecycle.NewStore(dir)
	state, loadErr := stateStore.Load(result.RunID)
	if loadErr != nil || state.Phase != lifecycle.PhaseResumable {
		t.Fatalf("stage-timeout должен перевести run в resumable: %+v err=%v", state, loadErr)
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
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cmd = exec.Command("git", "commit", "-qm", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cmd = exec.Command("git", "commit", "-qm", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("setup: %v", err)
	}

	rt := newScripted()
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name == "coder" {
			if err := os.WriteFile(filepath.Join(rt.targetDir, "new.go"), []byte("package main\n"), 0644); err != nil {
				t.Fatalf("setup: %v", err)
			}
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

func TestRun_CancelRequestWaitsForStageBoundary(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	started, release := make(chan struct{}), make(chan struct{})
	rt.onExec = func(name string, _ []runtime.Artifact) {
		if name == "analyst" {
			close(started)
			<-release // emulate an in-flight agent that must finish its mutation.
		}
	}
	var requested atomic.Bool
	p := New(cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "coder"}), testRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	done := make(chan error, 1)
	go func() {
		_, err := p.RunWithResult(context.Background(), RunConfig{
			Feature: "feat", TaskDesc: "safe cancel", TargetDir: dir,
			CancelRequested: requested.Load,
		})
		done <- err
	}()
	<-started
	requested.Store(true)
	select {
	case err := <-done:
		t.Fatalf("run stopped inside the active agent stage: %v", err)
	default:
	}
	close(release)
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel at boundary: %v", err)
	}
	if rt.calls["analyst"] != 1 || rt.calls["coder"] != 0 {
		t.Fatalf("cancel should preserve the completed current attempt and prevent the next stage: %+v", rt.calls)
	}
}

func TestSelectedHumanRevisionChangesDownstreamApprovalSubject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coder-output.md")
	if err := os.WriteFile(path, []byte("unchanged agent output"), 0600); err != nil {
		t.Fatal(err)
	}
	rs := &runState{
		runID: "run-subject", results: []notifier.StageResult{{
			Name: "coder", AttemptID: "attempt-coder", Outputs: []runtime.Artifact{{Name: "code", Path: path}},
		}},
	}
	base, err := rs.checkpointSubjectHash("transition", "coder")
	if err != nil {
		t.Fatal(err)
	}
	rs.selectedArtifactRevisions = map[string]string{"attempts/review.md": "rev-000002-first"}
	first, err := rs.checkpointSubjectHash("transition", "coder")
	if err != nil {
		t.Fatal(err)
	}
	rs.selectedArtifactRevisions["attempts/review.md"] = "rev-000003-second"
	second, err := rs.checkpointSubjectHash("transition", "coder")
	if err != nil {
		t.Fatal(err)
	}
	if base == first || first == second {
		t.Fatalf("revision change reused a prior approval subject: base=%s first=%s second=%s", base, first, second)
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

func TestRecoverInitialLifecycleRebuildsOnlyRunStartedCheckpoint(t *testing.T) {
	target := env(t)
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	factory := &countingEvidenceFactory{delegate: filesystemEvidenceStoreFactory{}}
	p := New(cfgFor(config.AgentConfig{Name: "analyst"}), testRegistry(), WithEvidenceStoreFactory(factory))
	configSnapshot, workflowSnapshot, err := p.resolvedEvidenceSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute)
	const runID = "run-startup-crash"
	store, err := evidence.Start(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "feat", TargetDir: target, StartedAt: started,
		ConfigSnapshot: configSnapshot, WorkflowSnapshot: workflowSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	// This is the durable state at the crash boundary: run.json and run_started
	// exist, while lifecycle.Create has not yet committed.
	if err := p.recoverInitialLifecycle(runID, target, "feat", "тестовая задача"); err != nil {
		t.Fatalf("restore startup checkpoint: %v", err)
	}
	if factory.resumes != 1 {
		t.Fatalf("recovery must reopen evidence through injected factory: resumes=%d", factory.resumes)
	}
	lifecycleStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state, err := lifecycleStore.Load(runID)
	if err != nil || state.Phase != lifecycle.PhaseRunning || state.NextStage != "analyst" || state.Task != "тестовая задача" {
		t.Fatalf("restored lifecycle: state=%+v err=%v", state, err)
	}
	if err := p.recoverInitialLifecycle(runID, target, "feat", "тестовая задача"); err == nil {
		t.Fatal("must reject replay after lifecycle exists")
	}
}

func TestRecoverInitialLifecycleRebuildsRunManifestBeforeRunStarted(t *testing.T) {
	target := env(t)
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	p := New(cfgFor(config.AgentConfig{Name: "analyst"}), testRegistry())
	configSnapshot, workflowSnapshot, err := p.resolvedEvidenceSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute)
	const runID = "run-manifest-only-crash"
	if _, err := evidence.Start(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "feat", TargetDir: target, StartedAt: started,
		ConfigSnapshot: configSnapshot, WorkflowSnapshot: workflowSnapshot,
	}); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(target, ".ai-team", "artifacts", "tasks", "feat", "task.md")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte("тестовая задача"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := p.recoverInitialLifecycle(runID, target, "feat", "тестовая задача"); err != nil {
		t.Fatalf("restore manifest-only startup checkpoint: %v", err)
	}
	lifecycleStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state, err := lifecycleStore.Load(runID)
	if err != nil || state.Phase != lifecycle.PhaseRunning || state.Task != "тестовая задача" {
		t.Fatalf("restored lifecycle: state=%+v err=%v", state, err)
	}
}

func TestRunEngineCancelTerminatesResumableRun(t *testing.T) {
	dir := env(t)
	cfg := cfgFor(config.AgentConfig{Name: "analyst"})
	factory := &countingEvidenceFactory{delegate: filesystemEvidenceStoreFactory{}}
	p := New(cfg, testRegistry(),
		WithRuntimeFactory(newScripted().factory), WithPrompter(&scriptedPrompter{}),
		WithEvidenceStoreFactory(factory))
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
	if factory.resumes != 1 {
		t.Fatalf("cancel must reopen evidence through injected factory: resumes=%d", factory.resumes)
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
	events, err := evidence.VerifyEventLog(
		filepath.Join(dir, ".ai-team", "runs", started.RunID, "events.jsonl"), started.RunID,
	)
	if err != nil {
		t.Fatalf("cancel evidence chain: %v", err)
	}
	canceled, finished := false, false
	for _, event := range events {
		canceled = canceled || event.Type == "run_canceled"
		finished = finished || event.Type == "run_finished"
	}
	if !canceled || !finished {
		t.Fatalf("cancel evidence must include run_canceled and run_finished: canceled=%t finished=%t", canceled, finished)
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

type cleanupFailureEvidenceStore struct {
	EvidenceStore
	cleanupErr   error
	cleanupCalls int
	snapshots    []evidence.Artifact
}

func (s *cleanupFailureEvidenceStore) SnapshotInputs(attemptID string, inputs []evidence.Artifact) ([]evidence.Artifact, func() error, error) {
	snapshots, _, err := s.EvidenceStore.SnapshotInputs(attemptID, inputs)
	if err != nil {
		return nil, func() error { return nil }, err
	}
	s.snapshots = append([]evidence.Artifact(nil), snapshots...)
	return snapshots, func() error {
		s.cleanupCalls++
		// Simulate a filesystem cleanup failure while leaving the immutable
		// snapshot in place for terminal evidence sealing.
		return s.cleanupErr
	}, nil
}

type cleanupFailureEvidenceFactory struct {
	delegate EvidenceStoreFactory
	store    *cleanupFailureEvidenceStore
}

func (f *cleanupFailureEvidenceFactory) Start(root string, manifest evidence.RunManifest) (EvidenceStore, error) {
	store, err := f.delegate.Start(root, manifest)
	if err != nil {
		return nil, err
	}
	f.store = &cleanupFailureEvidenceStore{EvidenceStore: store, cleanupErr: errors.New("injected cleanup failure")}
	return f.store, nil
}

func (f *cleanupFailureEvidenceFactory) Resume(root, runID string) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error) {
	return f.delegate.Resume(root, runID)
}

func TestRun_CleanupFailureLeavesInputBoundInTerminalEvidence(t *testing.T) {
	dir := env(t)
	factory := &cleanupFailureEvidenceFactory{delegate: filesystemEvidenceStoreFactory{}}
	p := New(cfgFor(config.AgentConfig{Name: "analyst"}), testRegistry(),
		WithRuntimeFactory(newScripted().factory),
		WithPrompter(&scriptedPrompter{}),
		WithEvidenceStoreFactory(factory))

	result, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true,
	})
	if err != nil {
		t.Fatalf("run with a reported scratch cleanup failure: %v", err)
	}
	if factory.store == nil || factory.store.cleanupCalls != 1 || len(factory.store.snapshots) == 0 {
		t.Fatalf("snapshot cleanup was not attempted exactly once: store=%+v", factory.store)
	}
	inputPath := factory.store.snapshots[0].Path
	if _, err := os.Stat(inputPath); err != nil {
		t.Fatalf("failed cleanup should leave the input snapshot for evidence sealing: %v", err)
	}
	runDir := filepath.Join(dir, ".ai-team", "runs", result.RunID)
	targetDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.VerifyTerminalEvidence(runDir, result.RunID, targetDir); err != nil {
		t.Fatalf("terminal evidence must verify with the leftover input bound: %v", err)
	}
	if err := evidence.VerifyAnchor(runDir); err != nil {
		t.Fatalf("terminal anchor must bind the leftover input: %v", err)
	}

	if err := os.WriteFile(inputPath, []byte("tampered after terminal seal"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := evidence.VerifyAnchor(runDir); err == nil || !strings.Contains(err.Error(), "supplemental") {
		t.Fatalf("terminal evidence must reject tampering with leftover input, got %v", err)
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

type memoryCandidateEvidenceStore struct {
	documents map[string]CandidateEvidence
}

func (s *memoryCandidateEvidenceStore) WriteCandidateEvidence(name string, document CandidateEvidence) error {
	if s.documents == nil {
		s.documents = make(map[string]CandidateEvidence)
	}
	if existing, ok := s.documents[name]; ok {
		if !reflect.DeepEqual(existing, document) {
			return errors.New("candidate evidence already submitted with different content")
		}
		return nil
	}
	s.documents[name] = document
	return nil
}

func (s *memoryCandidateEvidenceStore) ReadCandidateEvidence(name string) (CandidateEvidence, error) {
	document, ok := s.documents[name]
	if !ok {
		return CandidateEvidence{}, os.ErrNotExist
	}
	return document, nil
}

func TestReviewedCandidateImportsLegacyProjectionIntoControllerStore(t *testing.T) {
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
	legacy := CandidateEvidence{
		SchemaVersion: 1, RunID: "run-legacy-candidate", Purpose: "semantic_code_review", WorkspaceSHA256: digest,
		ChangedFiles: []CandidateFile{}, Checks: []CandidateCheck{}, Attempts: []CandidateAttempt{},
	}
	if err := writeControllerJSON(filepath.Join(controlDir, "review-candidate.json"), legacy); err != nil {
		t.Fatal(err)
	}
	store := &memoryCandidateEvidenceStore{}
	state := &runState{
		runCfg: RunConfig{TargetDir: target, Feature: "feat"}, runID: legacy.RunID,
		task: &runtime.Task{ArtifactRoot: artifactRoot}, p: &Pipeline{candidateEvidence: store},
	}
	if err := state.verifyCandidateEvidence("review-candidate.json", "semantic_code_review"); err != nil {
		t.Fatalf("legacy projection should resume through controller store: %v", err)
	}
	if got, err := store.ReadCandidateEvidence("review-candidate.json"); err != nil || !reflect.DeepEqual(got, legacy) {
		t.Fatalf("legacy document was not imported exactly: got=%+v err=%v", got, err)
	}
	// A repeated resume reads the canonical record and does not need the legacy
	// projection after the one-time import.
	if err := os.Remove(filepath.Join(controlDir, "review-candidate.json")); err != nil {
		t.Fatal(err)
	}
	if err := state.verifyCandidateEvidence("review-candidate.json", "semantic_code_review"); err != nil {
		t.Fatalf("canonical controller record should resume independently: %v", err)
	}
}

func TestReviewedCandidateLegacyImportRejectsSymlinkedControlDirectory(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(target, ".ai-team", "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(artifactRoot, "feat")); err != nil {
		t.Fatal(err)
	}
	state := &runState{
		runCfg: RunConfig{TargetDir: target, Feature: "feat"}, runID: "run-legacy-candidate",
		task: &runtime.Task{ArtifactRoot: artifactRoot}, p: &Pipeline{candidateEvidence: &memoryCandidateEvidenceStore{}},
	}
	if err := state.verifyCandidateEvidence("review-candidate.json", "semantic_code_review"); err == nil {
		t.Fatal("legacy import must reject a symlinked feature directory")
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

func TestRun_QuestionsApprovalWorksForDifferentStage(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.blocked["questioner"] = "нужны уточнения"
	gotQuestions, gotAnswer, gotBrief := false, false, false
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name != "questioner" {
			return
		}
		if rt.calls[name] == 1 {
			path := stageQuestionsPath(filepath.Join(dir, ".ai-team", "artifacts"), "feat")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("Какой формат результата нужен?\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		delete(rt.blocked, name)
		for _, input := range inputs {
			switch input.Name {
			case "questions":
				gotQuestions = true
			case "clarification-answer":
				gotAnswer = true
				answer, err := os.ReadFile(input.Path)
				if err != nil || !strings.Contains(string(answer), "сводный отчёт") {
					t.Errorf("questioner не получил durable answer: %q err=%v", answer, err)
				}
			case "business-brief":
				gotBrief = true
			}
		}
	}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.MaxVisits["questioner"] = 4
		wf.Edges = append(wf.Edges, config.WorkflowEdgeConfig{
			From: "questioner", Outcome: "blocked", To: "questioner",
			Approval: &config.WorkflowApprovalConfig{
				Roles: []string{"qa"}, Quorum: "any",
				Actions: map[string]string{"answer_questions": "questioner", "stop": "$stop"},
			},
		})
	}, config.AgentConfig{Name: "questioner"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "создать отчёт", TargetDir: dir})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("questioner должен запросить решение человека: result=%+v err=%v", first, err)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || pending.Kind != approval.KindQuestions || pending.FromStage != "questioner" {
		t.Fatalf("request kind/stage: approval=%+v err=%v", pending, err)
	}
	if _, err := store.Decide(first.RunID, required.ApprovalID, approval.Decision{
		ActorID: "qa-1", ActorRole: "qa", Action: "answer_questions",
		Comment: "Нужен сводный отчёт в Markdown.", SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil || second.RunID != first.RunID || second.Outcome != "completed" || rt.calls["questioner"] != 2 {
		t.Fatalf("question loop должен продолжить тот же другой этап: first=%+v second=%+v calls=%v err=%v", first, second, rt.calls, err)
	}
	if !gotQuestions || !gotAnswer || !gotBrief {
		t.Fatalf("questioner inputs: questions=%v answer=%v brief=%v", gotQuestions, gotAnswer, gotBrief)
	}
}
