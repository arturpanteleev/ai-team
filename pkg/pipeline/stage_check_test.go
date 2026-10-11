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
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/report"
)

// stage_check_test.go — интегрированные проверки результата этапа (B-40).

func stageCheckRegistry(withVerifier bool) *agent.Registry {
	files := fstest.MapFS{
		"tech/def.yaml": def(`name: tech
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  result: '{feature}/tech.md'
`),
		"tech/prompt.md": def("test"),
	}
	if withVerifier {
		files["verifier/def.yaml"] = def(`name: verifier
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs:
  verification: '{feature}/verification.md'
`)
		files["verifier/prompt.md"] = def("test")
	}
	return agent.NewFS(files)
}

func stageCheckConfig(check *config.TemplateCheck) *config.Config {
	return &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "stage-check-test",
		Title:         "Stage check test",
		PipelineAgents: []config.AgentConfig{
			{Name: "tech"},
			{Name: "verifier"},
		},
		Stages: []config.TemplateStage{
			{ID: "tech", Title: "Техническое решение", Function: "architect", Result: "md", Executor: "agent", Agent: "tech", Confirm: "auto", Check: check},
		},
		CLI: "opencode",
	}
}

func currentSchemaVersionForTest() int {
	return config.CurrentSchemaVersion
}

func runStageCheckPipeline(t *testing.T, dir string, cfg *config.Config, rt *scriptedRuntime) error {
	t.Helper()
	withVerifier := cfg.Stages[0].Check != nil && cfg.Stages[0].Check.Agent != ""
	p := New(cfg, stageCheckRegistry(withVerifier),
		WithRuntimeFactory(rt.factory),
		WithNotifier(&captureNotifier{}),
		WithPrompter(&scriptedPrompter{}),
	)
	return p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true,
	})
}

func readEvents(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(onlyRunDir(t, dir), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func checkReportFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, ".ai-team", "artifacts", "feat", ".stage-check", "tech.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("отчёт проверки не сохранён: %v", err)
	}
	return string(data)
}

func findCheckDecision(t *testing.T, dir string, kind string) *approval.PendingApproval {
	t.Helper()
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	values, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	for index := range values {
		if values[index].Kind == approval.KindCheckDecision && (kind == "" || values[index].Trigger == kind) {
			return &values[index]
		}
	}
	t.Fatalf("check_decision approval не найден: %+v", values)
	return nil
}

func TestRun_HardCheckPassAllowsTransition(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Решение\n\nвсё ок\n\n**Verdict:** APPROVED\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	if err := runStageCheckPipeline(t, dir, cfg, rt); err != nil {
		t.Fatalf("проверка должна пройти и не блокировать переход: %v", err)
	}
	events := readEvents(t, dir)
	for _, want := range []string{"check_started", "check_passed"} {
		if !strings.Contains(events, want) {
			t.Fatalf("ожидали событие %s:\n%s", want, events)
		}
	}
	rendered := checkReportFile(t, dir)
	if !strings.Contains(rendered, "- Итог: passed") {
		t.Fatalf("отчёт должен фиксировать passed: %s", rendered)
	}
	if got := report.ReadStageCheckReport(filepath.Join(dir, ".ai-team", "artifacts"), "feat", "tech"); got == "" {
		t.Fatal("отчёт проверки должен читаться через report.ReadStageCheckReport")
	}
}

func TestRun_HardCheckFailureBlocksWithCheckDecision(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Решение\n\nбез маркера вердикта\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	err := runStageCheckPipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("ожидали блокировку (ErrUserStopped), got: %v", err)
	}
	value := findCheckDecision(t, dir, approval.StageCheckTrigger)
	if value.Status != approval.StatusPending {
		t.Fatalf("check_decision должен оставаться pending, got %q", value.Status)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_failed") {
		t.Fatalf("ожидали событие check_failed:\n%s", events)
	}
	rendered := checkReportFile(t, dir)
	if !strings.Contains(rendered, "- Итог: failed") {
		t.Fatalf("отчёт должен фиксировать failed: %s", rendered)
	}
	store, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(filepath.Base(onlyRunDir(t, dir)))
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" {
		t.Fatalf("run должен ждать решения check_decision: %+v", state)
	}
}

func TestRun_HardCheckRequiredSections(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\n## Критерии приёмки\n\nвсё ок\n\n**Verdict:** APPROVED\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	cfg.Stages[0].RequiredSections = []string{"Критерии приёмки"}
	if err := runStageCheckPipeline(t, dir, cfg, rt); err != nil {
		t.Fatalf("needed sections present: %v", err)
	}
	dir2 := env(t)
	rt2 := newScripted()
	rt2.content["tech"] = map[string]string{"result": "# Решение\n\nбез раздела\n"}
	err := runStageCheckPipeline(t, dir2, cfg, rt2)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("missing required section should block: %v", err)
	}
}

func TestRun_AgentCheckPassAllowsTransition(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
	rt.content["verifier"] = map[string]string{"verification": "# Отчёт проверки\n\n**Verdict:** PASSED\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "agent", Agent: "verifier"})
	if err := runStageCheckPipeline(t, dir, cfg, rt); err != nil {
		t.Fatalf("агент-проверяющий PASSED должен пропустить этап дальше: %v", err)
	}
	if strings.Join(rt.executed, ",") != "tech,verifier" {
		t.Fatalf("проверяющий не запущен: executed=%v", rt.executed)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_passed") {
		t.Fatalf("ожидали check_passed:\n%s", events)
	}
}

func TestRun_AgentCheckFailureBlocksWithCheckDecision(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
	rt.content["verifier"] = map[string]string{"verification": "# Отчёт проверки\n\n**Verdict:** FAILED\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "agent", Agent: "verifier"})
	err := runStageCheckPipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("агент-проверяющий FAILED должен заблокировать переход: %v", err)
	}
	value := findCheckDecision(t, dir, approval.StageCheckTrigger)
	if value.Status != approval.StatusPending {
		t.Fatalf("check_decision должен оставаться pending, got %q", value.Status)
	}
}

func TestRun_CheckerAgentErrorRaisesCheckError(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nок\n"}
	rt.content["verifier"] = map[string]string{"verification": "# Отчёт\n\nбез вердикта\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "agent", Agent: "verifier"})
	err := runStageCheckPipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("сбой агента-проверяющего должен поднять check_decision: %v", err)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_error") {
		t.Fatalf("ожидали событие check_error:\n%s", events)
	}
	value := findCheckDecision(t, dir, approval.StageCheckTrigger)
	if value.Payload == nil {
		t.Fatal("check_decision должен содержать payload")
	}
}

func TestRun_CheckDecisionNewVersionResumesStage(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nбез маркера\n"}
	cfg := stageCheckConfig(&config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}})
	err := runStageCheckPipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("первый запуск должен заблокироваться: %v", err)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	values, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	pending := values[0]
	if pending.Kind != approval.KindCheckDecision || len(pending.Decisions) != 0 {
		t.Fatalf("ожидали pending check_decision, got %+v", pending)
	}
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "local-user", ActorRole: "architect", Action: "new_version",
		SubjectHash: pending.SubjectHash, DecidedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("resolve new_version: %v", err)
	}

	// Новая версия результата — маркер на месте; resume перезапускает этап.
	rt.content["tech"] = map[string]string{"result": "# Решение\n\n**Verdict:** APPROVED\n"}
	p := New(cfg, stageCheckRegistry(false),
		WithRuntimeFactory(rt.factory),
		WithNotifier(&captureNotifier{}),
		WithPrompter(&scriptedPrompter{}),
	)
	if err := p.Run(context.Background(), RunConfig{Feature: "", TargetDir: dir, ResumeRunID: runID}); err != nil {
		t.Fatalf("resume новой версии должен пройти проверку: %v", err)
	}
	runDir := onlyRunDir(t, dir)
	events, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), "check_passed") {
		t.Fatalf("повторная проверка должна пройти:\n%s", events)
	}
}

// humanStageCheckConfig — человеческий этап с hard-проверкой на маркер вердикта.
// Раздел/версия не заданы, поэтому любое непустое содержимое проходит
// человеческую валидацию, а вердикт определяет только проверка этапа.
func humanStageCheckConfig() *config.Config {
	return &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "human-stage-check",
		Title:         "Human stage check",
		Stages: []config.TemplateStage{{
			ID: "spec", Title: "Spec", Function: "product_owner", Result: "md", Executor: "human",
			Confirm: "auto", Check: &config.TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}},
		}},
	}
}

func newHumanStageCheckPipeline(opts ...Option) *Pipeline {
	base := []Option{WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{})}
	return New(humanStageCheckConfig(), agent.NewFS(fstest.MapFS{}), append(base, opts...)...)
}

func startHumanStageCheckRun(t *testing.T, dir string) (*approval.Store, string, approval.PendingApproval) {
	t.Helper()
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := newHumanStageCheckPipeline(WithApprovalStore(store)).RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "build", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(runErr, &required) {
		t.Fatalf("human stage should wait for input: result=%+v err=%v", result, runErr)
	}
	pending, err := store.Load(result.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	return store, result.RunID, pending
}

func submitHumanStage(t *testing.T, store *approval.Store, pending approval.PendingApproval, content string) {
	t.Helper()
	if _, err := store.Decide(pending.RunID, pending.ID, approval.Decision{
		ActorID: "alice", ActorRole: "product_owner", Action: "submit", Comment: content, SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
}

func countEvent(events, eventType string) int {
	return strings.Count(events, `"type":"`+eventType+`"`)
}

// A crash between a durably finished human attempt and its stage check must not
// let the failed check be skipped on resume: the check has to run and block.
func TestHumanStageCheckRunsAfterCrashBeforeCheck(t *testing.T) {
	dir := env(t)
	store, runID, pending := startHumanStageCheckRun(t, dir)
	submitHumanStage(t, store, pending, "## Спека\n\nбез маркера вердикта\n")

	factory := &humanCrashEvidenceFactory{point: "after-human-attempt-finished"}
	crashing := newHumanStageCheckPipeline(WithApprovalStore(store), WithEvidenceStoreFactory(factory))
	expectHumanCrash(t, func() { _, _ = resumeHumanCrashRun(t, crashing, dir, runID) })
	if !factory.crashed {
		t.Fatal("crash was not injected after the durable human attempt_finished")
	}

	resumed, err := newHumanStageCheckPipeline(WithApprovalStore(store)).RunWithResult(context.Background(), RunConfig{
		ResumeRunID: runID, TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("a failed check must block after crash-before-check resume: result=%+v err=%v", resumed, err)
	}
	check, err := store.Load(runID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if check.Kind != approval.KindCheckDecision || check.Status != approval.StatusPending || check.FromStage != "spec" {
		t.Fatalf("expected a pending check_decision for spec, got %+v", check)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_failed") {
		t.Fatalf("the check must run after crash-before-check resume:\n%s", events)
	}
}

// The same crash window with a passing result must resume to completion and
// record the check exactly once.
func TestHumanStageCheckPassesAfterCrashBeforeCheck(t *testing.T) {
	dir := env(t)
	store, runID, pending := startHumanStageCheckRun(t, dir)
	submitHumanStage(t, store, pending, "## Спека\n\n**Verdict:** APPROVED\n")

	factory := &humanCrashEvidenceFactory{point: "after-human-attempt-finished"}
	crashing := newHumanStageCheckPipeline(WithApprovalStore(store), WithEvidenceStoreFactory(factory))
	expectHumanCrash(t, func() { _, _ = resumeHumanCrashRun(t, crashing, dir, runID) })
	if !factory.crashed {
		t.Fatal("crash was not injected at the pre-check boundary")
	}
	if _, err := resumeHumanCrashRun(t, newHumanStageCheckPipeline(WithApprovalStore(store)), dir, runID); err != nil {
		t.Fatalf("resume of a passing result must complete: %v", err)
	}
	events := readEvents(t, dir)
	if countEvent(events, "check_passed") != 1 {
		t.Fatalf("the check must run exactly once after resume:\n%s", events)
	}
}

// A crash right after the durable check_failed event (before the human decision
// is recorded) must still block on resume instead of advancing.
func TestHumanStageCheckFailureBlocksAfterCrashBeforeDecision(t *testing.T) {
	dir := env(t)
	store, runID, pending := startHumanStageCheckRun(t, dir)
	submitHumanStage(t, store, pending, "## Спека\n\nбез маркера\n")

	factory := &humanCrashEvidenceFactory{point: "after-check-event"}
	crashing := newHumanStageCheckPipeline(WithApprovalStore(store), WithEvidenceStoreFactory(factory))
	expectHumanCrash(t, func() { _, _ = resumeHumanCrashRun(t, crashing, dir, runID) })
	if !factory.crashed {
		t.Fatal("crash was not injected after the durable check event")
	}
	result, err := resumeHumanCrashRun(t, newHumanStageCheckPipeline(WithApprovalStore(store)), dir, runID)
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("a durable failed check must block on resume: result=%+v err=%v", result, err)
	}
	check, err := store.Load(runID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if check.Kind != approval.KindCheckDecision || check.Status != approval.StatusPending {
		t.Fatalf("recovery must raise a pending check_decision, got %+v", check)
	}
	events := readEvents(t, dir)
	if countEvent(events, "check_failed") != 1 {
		t.Fatalf("recovery must not re-emit check_failed:\n%s", events)
	}
}

// A crash after the durable check_decision request but before the lifecycle
// checkpoint must reuse the existing pending approval instead of raising a
// duplicate.
func TestHumanStageCheckReusesPendingDecisionAfterCrash(t *testing.T) {
	dir := env(t)
	store, runID, pending := startHumanStageCheckRun(t, dir)
	submitHumanStage(t, store, pending, "## Спека\n\nбез маркера\n")

	factory := &humanCrashEvidenceFactory{point: "after-check-decision-requested"}
	crashing := newHumanStageCheckPipeline(WithApprovalStore(store), WithEvidenceStoreFactory(factory))
	expectHumanCrash(t, func() { _, _ = resumeHumanCrashRun(t, crashing, dir, runID) })
	if !factory.crashed {
		t.Fatal("crash was not injected after the check_decision request")
	}
	before, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resumeHumanCrashRun(t, newHumanStageCheckPipeline(WithApprovalStore(store)), dir, runID)
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("resume must keep blocking on the pending decision: result=%+v err=%v", result, err)
	}
	check, err := store.Load(runID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if check.Kind != approval.KindCheckDecision || check.Status != approval.StatusPending {
		t.Fatalf("recovery must reuse the pending check_decision, got %+v", check)
	}
	after, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("recovery must not create a duplicate approval: before=%d after=%d", len(before), len(after))
	}
	events := readEvents(t, dir)
	if count := strings.Count(events, `"trigger":"`+approval.StageCheckTrigger+`"`); count != 1 {
		t.Fatalf("expected exactly one check_decision request, got %d:\n%s", count, events)
	}
}
