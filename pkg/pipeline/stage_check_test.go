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
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/report"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
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
		files["spec-checker/def.yaml"] = def(`name: spec-checker
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs:
  spec-check: '{feature}/spec-check.md'
`)
		files["spec-checker/prompt.md"] = def("test")
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
			{Name: "spec-checker"},
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

// grillStageCheckConfig reuses the built-in product_spec grill check so the
// grill tests run the same checker agent as the shipped template. Scripted
// output exists only for spec-checker: a template that wires grill to another
// agent (for example the deploy-readiness verifier) fails these tests.
func grillStageCheckConfig(maxRounds int) *config.Config {
	builtIn, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		panic(err)
	}
	var check config.TemplateCheck
	for _, stage := range builtIn.Stages {
		if stage.Check != nil && stage.Check.Mode == "grill" {
			check = *stage.Check
		}
	}
	check.MaxRounds = maxRounds
	cfg := stageCheckConfig(&check)
	cfg.Stages[0].Function = "product_owner"
	return cfg
}

func grillApproval(t *testing.T, store *approval.Store, runID string) approval.PendingApproval {
	t.Helper()
	values, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value.Kind == approval.KindGrillQuestions {
			return value
		}
	}
	t.Fatalf("grill approval not found in %+v", values)
	return approval.PendingApproval{}
}

func newGrillPipeline(cfg *config.Config, rt *scriptedRuntime, store *approval.Store) *Pipeline {
	return New(cfg, stageCheckRegistry(true),
		WithRuntimeFactory(rt.factory), WithNotifier(&captureNotifier{}),
		WithPrompter(&scriptedPrompter{}), WithApprovalStore(store))
}

func runGrillPipeline(t *testing.T, p *Pipeline, dir, runID string) error {
	t.Helper()
	return p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "test", TargetDir: dir, ResumeRunID: runID})
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

func TestRun_GrillAnswerFeedsNextRoundAndPassedCompletes(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Спецификация\n\nКритерии не проверяемы.\n"}
	rt.contentFn["spec-checker"] = func(call int) map[string]string {
		if call == 1 {
			return map[string]string{"spec-check": "**Question:** Как измерить критерий успеха?\n"}
		}
		return map[string]string{"spec-check": "Спецификация проверена после уточнения.\n**Verdict:** PASSED\n"}
	}
	answerWasPassedToChecker := false
	rt.onExec = func(agentName string, inputs []runtime.Artifact) {
		if agentName != "spec-checker" {
			return
		}
		for _, input := range inputs {
			if strings.HasPrefix(input.Name, "grill-round-") {
				data, err := os.ReadFile(input.Path)
				if err == nil && strings.Contains(string(data), "Порог — рост конверсии") {
					answerWasPassedToChecker = true
				}
			}
		}
	}
	cfg := grillStageCheckConfig(3)
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := newGrillPipeline(cfg, rt, store)
	if err := runGrillPipeline(t, p, dir, ""); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("first grill round should wait for an answer: %v", err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	pending := grillApproval(t, store, runID)
	var payload approval.GrillQuestionsPayload
	if err := json.Unmarshal(pending.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if pending.Status != approval.StatusPending || payload.Round != 1 || !strings.Contains(payload.Markdown, "Как измерить") {
		t.Fatalf("unexpected first grill approval: %+v payload=%+v", pending, payload)
	}
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: approval.GrillActionAnswer,
		Comment: "Порог — рост конверсии минимум на 20%.", SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runGrillPipeline(t, p, dir, runID); err != nil {
		t.Fatalf("passed second round should resume the run: %v", err)
	}
	if !answerWasPassedToChecker || rt.calls["spec-checker"] != 2 {
		t.Fatalf("checker did not receive the answer on round two: answer=%v calls=%d", answerWasPassedToChecker, rt.calls["spec-checker"])
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, `"type":"grill_completed"`) || !strings.Contains(events, `"open_questions":0`) {
		t.Fatalf("passed grill completion must record zero open questions:\n%s", events)
	}
}

func TestRun_GrillResolvedApprovalRecoversAfterCrashBeforeCheckerDispatch(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Спецификация\n\nПроверяемый критерий.\n"}
	rt.contentFn["spec-checker"] = func(call int) map[string]string {
		if call == 1 {
			return map[string]string{"spec-check": "**Question:** Как измерить критерий?\n"}
		}
		return map[string]string{"spec-check": "**Verdict:** PASSED\n"}
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := newGrillPipeline(grillStageCheckConfig(3), rt, store)
	if err := runGrillPipeline(t, p, dir, ""); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("first grill round should wait: %v", err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	pending := grillApproval(t, store, runID)
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: approval.GrillActionAnswer,
		Comment: "Порог — рост конверсии минимум на 5%.", SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	crashed := false
	rt.onExec = func(agentName string, _ []runtime.Artifact) {
		if agentName == "spec-checker" && !crashed {
			crashed = true
			panic("simulated crash before grill checker dispatch")
		}
	}
	func() {
		defer func() {
			if recover() != nil {
				crashed = true
			}
		}()
		_ = runGrillPipeline(t, p, dir, runID)
	}()
	if !crashed {
		t.Fatal("expected simulated crash during resumed grill check")
	}
	lifecycleStore, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := lifecycleStore.Load(runID)
	if err != nil || checkpoint.Phase != lifecycle.PhaseRunning || checkpoint.ActiveApprovalID != pending.ID {
		t.Fatalf("resolved grill approval checkpoint was not retained: state=%+v err=%v", checkpoint, err)
	}
	rt.onExec = nil
	if err := runGrillPipeline(t, p, dir, runID); err != nil {
		t.Fatalf("verified grill approval should recover and finish: %v", err)
	}
	if !strings.Contains(readEvents(t, dir), `"type":"grill_completed"`) {
		t.Fatal("recovered grill should record completion")
	}
}

func TestRun_GrillDoneRecordsOpenQuestionAndCompletes(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Спецификация\n\nТекст.\n"}
	rt.content["spec-checker"] = map[string]string{"spec-check": "**Question:** Как проверить критерий?\n"}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := newGrillPipeline(grillStageCheckConfig(3), rt, store)
	if err := runGrillPipeline(t, p, dir, ""); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("first grill round should wait: %v", err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	pending := grillApproval(t, store, runID)
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: approval.GrillActionDone,
		SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	if updated, err := store.Load(runID, pending.ID); err != nil || updated.Status != approval.StatusResolved {
		t.Fatalf("done action was not durably resolved: approval=%+v err=%v", updated, err)
	}
	if err := runGrillPipeline(t, p, dir, runID); err != nil {
		t.Fatalf("done action should finish grill and continue: %v", err)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, `"type":"grill_completed"`) || !strings.Contains(events, `"open_questions":1`) || !strings.Contains(events, `"reason":"done"`) {
		t.Fatalf("done completion must record the one unresolved question:\n%s", events)
	}
}

func TestRun_GrillMaxRoundsCompletesAutomatically(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Спецификация\n\nТекст.\n"}
	rt.content["spec-checker"] = map[string]string{"spec-check": "**Question:** Как проверить критерий?\n"}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := runGrillPipeline(t, newGrillPipeline(grillStageCheckConfig(1), rt, store), dir, ""); err != nil {
		t.Fatalf("max_rounds should complete the advisory grill automatically: %v", err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	values, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value.Kind == approval.KindGrillQuestions {
			t.Fatalf("last question should not leave a pending approval at max_rounds: %+v", value)
		}
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, `"reason":"max_rounds"`) || !strings.Contains(events, `"open_questions":1`) {
		t.Fatalf("automatic grill completion must report its remaining question:\n%s", events)
	}
}

func TestRun_GrillRejectsMultipleQuestionsInOneRound(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Спецификация\n\nТекст.\n"}
	rt.content["spec-checker"] = map[string]string{"spec-check": "**Question:** Что значит быстро?\n**Question:** Как измерить надёжность?\n"}
	err := runStageCheckPipeline(t, dir, grillStageCheckConfig(3), rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("malformed grill output must fail closed for human review: %v", err)
	}
	if value := findCheckDecision(t, dir, approval.StageCheckTrigger); value.Status != approval.StatusPending {
		t.Fatalf("malformed grill output should create a pending check decision: %+v", value)
	}
	events := readEvents(t, dir)
	if !strings.Contains(events, "check_error") || strings.Contains(events, `"kind":"grill_questions"`) {
		t.Fatalf("multiple questions must be rejected as a checker error, not offered together:\n%s", events)
	}
}

func TestRun_GrillNewVersionUsesPinnedArtifactRevision(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Спецификация\n\nИсходный текст.\n"}
	rt.content["spec-checker"] = map[string]string{"spec-check": "**Question:** Уточните критерий.\n"}
	var revisedArtifactSeen bool
	rt.onExec = func(agentName string, inputs []runtime.Artifact) {
		if agentName != "spec-checker" {
			return
		}
		for _, input := range inputs {
			if input.Name == "result" {
				data, err := os.ReadFile(input.Path)
				if err == nil && strings.Contains(string(data), "Новая версия") {
					revisedArtifactSeen = true
				}
			}
		}
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := newGrillPipeline(grillStageCheckConfig(3), rt, store)
	if err := runGrillPipeline(t, p, dir, ""); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("first grill round should wait: %v", err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	pending := grillApproval(t, store, runID)
	var payload approval.GrillQuestionsPayload
	if err := json.Unmarshal(pending.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	runDir := onlyRunDir(t, dir)
	_, manifest, err := evidence.ReadAttemptManifest(nil, runDir, runID, pending.AttemptID)
	if err != nil || len(manifest.Outputs) != 1 {
		t.Fatalf("read source output manifest: outputs=%d err=%v", len(manifest.Outputs), err)
	}
	if payload.ArtifactContentSHA256[manifest.Outputs[0].EvidencePath] != manifest.Outputs[0].SHA256 {
		t.Fatalf("grill approval did not bind the immutable agent-output baseline: payload=%+v output=%+v",
			payload.ArtifactContentSHA256, manifest.Outputs[0])
	}
	artifactPath := "stages/tech/result.md"
	artifacts, err := humanartifact.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := artifacts.Append(runID, artifactPath, "", "", "## Спецификация\n\nНовая версия с измеримым критерием.\n", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: approval.GrillActionNewVersion,
		ArtifactRevisions: map[string]string{artifactPath: revision.ID}, SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	rt.contentFn["spec-checker"] = func(call int) map[string]string {
		return map[string]string{"spec-check": "**Verdict:** PASSED\n"}
	}
	if err := runGrillPipeline(t, p, dir, runID); err != nil {
		values, _ := store.List(runID)
		var reportText string
		for _, value := range values {
			if value.Kind != approval.KindCheckDecision {
				continue
			}
			var payload approval.CheckDecisionPayload
			_ = json.Unmarshal(value.Payload, &payload)
			data, _ := os.ReadFile(filepath.Join(dir, ".ai-team", "artifacts", filepath.FromSlash(payload.ReportPath)))
			reportText = string(data)
		}
		t.Fatalf("new version should be checked and accepted: %v; report=%s", err, reportText)
	}
	if !revisedArtifactSeen {
		t.Fatal("checker did not receive the pinned human revision")
	}
}

func TestRun_GrillNewVersionFeedsNextStageAfterResume(t *testing.T) {
	dir := env(t)
	registry := agent.NewFS(fstest.MapFS{
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
		"spec-checker/def.yaml": def(`name: spec-checker
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs:
  spec-check: '{feature}/spec-check.md'
`),
		"spec-checker/prompt.md": def("test"),
		"reviewer/def.yaml": def(`name: reviewer
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  result: '{feature}/tech.md'
outputs:
  review: '{feature}/review.md'
`),
		"reviewer/prompt.md": def("test"),
	})
	cfg := grillStageCheckConfig(3)
	cfg.PipelineAgents = append(cfg.PipelineAgents, config.AgentConfig{Name: "reviewer"})
	cfg.Stages = append(cfg.Stages, config.TemplateStage{
		ID: "reviewer", Title: "Review", Function: "reviewer", Result: "md", Executor: "agent", Agent: "reviewer", Confirm: "auto",
	})
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Спецификация\n\nИсходная версия.\n"}
	rt.contentFn["spec-checker"] = func(call int) map[string]string {
		if call == 1 {
			return map[string]string{"spec-check": "**Question:** Уточните критерий.\n"}
		}
		return map[string]string{"spec-check": "**Verdict:** PASSED\n"}
	}
	var checkerSawRevision, nextStageSawRevision bool
	var nextStageRevisionPath, nextStageRevisionDigest string
	var expectedRevision humanartifact.Revision
	rt.onExec = func(agentName string, inputs []runtime.Artifact) {
		if agentName != "spec-checker" && agentName != "reviewer" {
			return
		}
		for _, input := range inputs {
			if input.Name != "result" {
				continue
			}
			data, err := os.ReadFile(input.Path)
			if err != nil {
				t.Errorf("read %s result input: %v", agentName, err)
				continue
			}
			if strings.Contains(string(data), "Новая версия с проверяемым критерием") {
				if agentName == "spec-checker" {
					checkerSawRevision = true
				} else {
					nextStageSawRevision = true
					nextStageRevisionPath = input.Path
					_, _, nextStageRevisionDigest, err = evidence.ArtifactDigest(input.Path)
					if err != nil {
						t.Errorf("digest downstream revision input: %v", err)
					}
				}
			}
		}
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithApprovalStore(store),
		WithNotifier(&captureNotifier{}), WithPrompter(&scriptedPrompter{}))
	if err := runGrillPipeline(t, p, dir, ""); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("first grill round should wait: %v", err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	pending := grillApproval(t, store, runID)
	runDir := onlyRunDir(t, dir)
	manifestPath := filepath.Join(runDir, "attempts", pending.AttemptID, "manifest.json")
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := humanartifact.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := "stages/tech/result.md"
	expectedRevision, err = revisions.Append(runID, artifactPath, "", "",
		"## Спецификация\n\nНовая версия с проверяемым критерием.\n", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: approval.GrillActionNewVersion,
		ArtifactRevisions: map[string]string{artifactPath: expectedRevision.ID}, SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}

	// Stop after transition_selected is durable but before lifecycle advances.
	// Replay must reconstruct the exact pinned revision for the downstream
	// stage without rerunning or mutating the immutable source attempt.
	crashFactory := &humanCrashEvidenceFactory{point: "after-transition"}
	crashing := New(cfg, registry, WithRuntimeFactory(rt.factory), WithApprovalStore(store),
		WithEvidenceStoreFactory(crashFactory), WithNotifier(&captureNotifier{}), WithPrompter(&scriptedPrompter{}))
	var crashErr error
	crashCaught := false
	func() {
		defer func() { crashCaught = recover() != nil }()
		_, crashErr = resumeHumanCrashRun(t, crashing, dir, runID)
	}()
	if !crashCaught {
		values, _ := store.List(runID)
		var reportText string
		for _, value := range values {
			if value.Kind != approval.KindCheckDecision {
				continue
			}
			var payload approval.CheckDecisionPayload
			_ = json.Unmarshal(value.Payload, &payload)
			data, _ := os.ReadFile(filepath.Join(dir, ".ai-team", "artifacts", filepath.FromSlash(payload.ReportPath)))
			reportText = string(data)
		}
		t.Fatalf("expected post-transition crash, got err=%v report=%s", crashErr, reportText)
	}
	if !crashFactory.crashed || !checkerSawRevision {
		t.Fatalf("expected crash after checker accepted the selected revision: crashed=%v checkerSawRevision=%v", crashFactory.crashed, checkerSawRevision)
	}
	resumed := New(cfg, registry, WithRuntimeFactory(rt.factory), WithApprovalStore(store),
		WithNotifier(&captureNotifier{}), WithPrompter(&scriptedPrompter{}))
	if _, err := resumeHumanCrashRun(t, resumed, dir, runID); err != nil {
		t.Fatalf("resume should pass the pinned revision to the next stage: %v", err)
	}
	if !nextStageSawRevision {
		t.Fatal("next stage consumed the immutable source output instead of the approved human revision")
	}
	if !strings.Contains(nextStageRevisionPath, expectedRevision.ID) {
		t.Fatalf("downstream projection path does not bind the pinned revision ID %s: %s", expectedRevision.ID, nextStageRevisionPath)
	}
	if nextStageRevisionDigest != expectedRevision.SHA256 {
		t.Fatalf("downstream projection digest=%s want=%s", nextStageRevisionDigest, expectedRevision.SHA256)
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil || string(manifestAfter) != string(manifestBefore) {
		t.Fatalf("source attempt manifest must remain immutable: err=%v", err)
	}
}

func TestValidateRecordedGrillDecisionRequiresVerifiedSourceAndDecision(t *testing.T) {
	now := time.Now().UTC()
	decision := approval.Decision{
		ApprovalID: "grill-approval", ActorID: "owner", ActorRole: "product_owner",
		Action: approval.GrillActionAnswer, SubjectHash: strings.Repeat("a", 64), DecidedAt: now,
	}
	value := approval.PendingApproval{
		ID: decision.ApprovalID, RunID: "run-grill-recovery", AttemptID: "attempt-source",
		Kind: approval.KindGrillQuestions, Status: approval.StatusResolved,
		FromStage: "product_spec", ToStage: "product_spec", Trigger: approval.GrillTrigger,
		SubjectHash: decision.SubjectHash, ResolvedAction: approval.GrillActionAnswer,
		Targets: map[string]string{approval.GrillActionAnswer: "product_spec"}, Decisions: []approval.Decision{decision},
	}
	digest, err := evidence.DecisionSetDigest(value.Decisions)
	if err != nil {
		t.Fatal(err)
	}
	replayed := evidence.ReplayedRun{
		RunID:    value.RunID,
		Attempts: []evidence.ReplayedAttempt{{AttemptID: value.AttemptID, Stage: value.FromStage, FinishedAt: now}},
		ApprovalDecisions: []evidence.ReplayedApprovalDecision{{
			ID: value.ID, Kind: string(value.Kind), AttemptID: value.AttemptID, SubjectHash: value.SubjectHash,
			FromStage: value.FromStage, ToStage: value.ToStage, Trigger: value.Trigger,
			Action: value.ResolvedAction, DecisionSetSHA256: digest,
		}},
	}
	if err := validateRecordedGrillDecision(replayed, value); err != nil {
		t.Fatalf("verified grill approval should recover: %v", err)
	}
	replayed.ApprovalDecisions = nil
	if err := validateRecordedGrillDecision(replayed, value); err == nil {
		t.Fatal("grill approval without a verified decision event must not recover")
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

// new_version on a human stage must clear the previous output and check report
// so the resubmitted version can be written and re-checked.
func TestHumanStageCheckNewVersionResubmits(t *testing.T) {
	dir := env(t)
	store, runID, pending := startHumanStageCheckRun(t, dir)
	submitHumanStage(t, store, pending, "## Спека\n\nбез маркера\n")
	if _, err := resumeHumanCrashRun(t, newHumanStageCheckPipeline(WithApprovalStore(store)), dir, runID); err == nil {
		t.Fatal("first failing check should block the run")
	}
	check, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	var decision approval.PendingApproval
	for _, value := range check {
		if value.Kind == approval.KindCheckDecision {
			decision = value
		}
	}
	if decision.ID == "" {
		t.Fatal("check_decision approval not found")
	}
	if _, err := store.Decide(runID, decision.ID, approval.Decision{
		ActorID: "alice", ActorRole: "product_owner", Action: "new_version", SubjectHash: decision.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := resumeHumanCrashRun(t, newHumanStageCheckPipeline(WithApprovalStore(store)), dir, runID)
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("new_version must reopen the human input: result=%+v err=%v", result, err)
	}
	newInput, err := store.Load(runID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if newInput.Kind != approval.KindInput {
		t.Fatalf("new_version must ask for a fresh human input, got %+v", newInput)
	}
	submitHumanStage(t, store, newInput, "## Спека v2\n\n**Verdict:** APPROVED\n")
	if _, err := resumeHumanCrashRun(t, newHumanStageCheckPipeline(WithApprovalStore(store)), dir, runID); err != nil {
		t.Fatalf("resubmitted version must pass the stage check: %v", err)
	}
	events := readEvents(t, dir)
	if countEvent(events, "check_passed") != 1 {
		t.Fatalf("the resubmitted version must pass exactly one check:\n%s", events)
	}
}
