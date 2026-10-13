package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

type fakeEngine struct {
	mu          sync.Mutex
	started     chan struct{}
	release     chan struct{}
	observed    bool
	cancelCalls int
	skipCalls   int
	skipConfig  pipeline.SkipStageConfig
	resumeGrant *pipeline.BudgetAllowanceGrant
	// finished, если задан, закрывается после того, как Resume закончил
	// обращаться к target: тест ждёт его до удаления TempDir.
	finished chan struct{}
}

func (f *fakeEngine) Start(_ context.Context, config pipeline.RunConfig) (pipeline.RunResult, error) {
	close(f.started)
	<-f.release // model an agent that can finish before cancellation is observed.
	if config.CancelRequested != nil && config.CancelRequested() {
		f.mu.Lock()
		f.observed = true
		f.mu.Unlock()
		return pipeline.RunResult{RunID: config.RunID}, context.Canceled
	}
	return pipeline.RunResult{RunID: config.RunID}, nil
}
func (f *fakeEngine) Resume(_ context.Context, config pipeline.ResumeConfig) (pipeline.RunResult, error) {
	f.mu.Lock()
	f.resumeGrant = config.BudgetAllowance
	f.mu.Unlock()
	if f.finished != nil {
		defer close(f.finished)
	}
	close(f.started)
	<-f.release
	if config.CancelRequested != nil && config.CancelRequested() {
		f.mu.Lock()
		f.observed = true
		f.mu.Unlock()
		return pipeline.RunResult{RunID: config.RunID}, context.Canceled
	}
	return pipeline.RunResult{RunID: config.RunID}, nil
}
func (f *fakeEngine) Cancel(config pipeline.CancelConfig) (pipeline.RunResult, error) {
	f.mu.Lock()
	f.cancelCalls++
	f.mu.Unlock()
	return pipeline.RunResult{RunID: config.RunID}, nil
}

func (f *fakeEngine) SkipStage(_ context.Context, config pipeline.SkipStageConfig) (pipeline.RunResult, error) {
	if f.started != nil {
		close(f.started)
	}
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	f.skipCalls++
	f.skipConfig = config
	f.mu.Unlock()
	return pipeline.RunResult{RunID: config.RunID, Outcome: workflow.RunCompleted}, nil
}

func TestNewRequiresEngine(t *testing.T) {
	if _, err := New(nil, t.TempDir()); err == nil {
		t.Fatal("controller без engine принят")
	}
}

func TestDeliverDeferredUsesEvidenceRunIDValidationBeforePaths(t *testing.T) {
	controller := &Controller{target: t.TempDir()}
	invalid := []string{"", ".", "..", "a/b", `a\b`, "bad\x00id", "bad\nname", strings.Repeat("x", 256)}
	for _, runID := range invalid {
		if err := evidence.ValidateRunID(runID); err == nil {
			t.Fatalf("test fixture unexpectedly passes the shared validator: %q", runID)
		}
		if _, err := controller.DeliverDeferred(context.Background(), runID); err == nil || !strings.Contains(err.Error(), "invalid run id") {
			t.Errorf("DeliverDeferred(%q) = %v, want shared run ID rejection", runID, err)
		}
	}
}

func TestDeliverDeferredUsesInjectedPinnedAwareRunner(t *testing.T) {
	target := t.TempDir()
	var gotRunID, gotTarget string
	controller, err := New(&fakeEngine{}, target, WithDeferredDelivery(func(_ context.Context, runID, targetDir string) (delivery.TerminalRecord, error) {
		gotRunID, gotTarget = runID, targetDir
		return delivery.TerminalRecord{RunID: runID, CommitSHA: strings.Repeat("c", 40)}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	record, err := controller.DeliverDeferred(context.Background(), "pinned-run")
	if err != nil || record.RunID != "pinned-run" || gotRunID != "pinned-run" || gotTarget != target {
		t.Fatalf("deferred delivery bypassed pinned-aware runner: record=%+v run=%q target=%q err=%v", record, gotRunID, gotTarget, err)
	}
}

func TestControllerDecisionsUseInjectedApprovalStore(t *testing.T) {
	target := t.TempDir()
	store, err := approval.NewSQLiteStore(filepath.Join(target, "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	value, err := store.Create(approval.PendingApproval{
		RunID: "controller-run", AttemptID: "attempt-1", FromStage: "reviewer", ToStage: "coder",
		Trigger: "stage_completed", SubjectHash: strings.Repeat("a", 64),
		RequiredRoles: []string{"reviewer"}, Actions: []string{"approve"}, Targets: map[string]string{"approve": "coder"},
	})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := New(&fakeEngine{}, target, WithApprovalStore(store))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, ".ai-team", "state", "approvals")); !os.IsNotExist(err) {
		t.Fatalf("injected store must not construct filesystem default, stat err=%v", err)
	}
	resolved, err := controller.Decide(value.RunID, value.ID, approval.Decision{
		ActorID: "human-1", ActorRole: "reviewer", Action: "approve", SubjectHash: value.SubjectHash,
	})
	if err != nil || resolved.Status != approval.StatusResolved {
		t.Fatalf("controller decision: %+v, %v", resolved, err)
	}
	loaded, err := store.Load(value.RunID, value.ID)
	if err != nil || loaded.Status != approval.StatusResolved {
		t.Fatalf("decision not in injected store: %+v, %v", loaded, err)
	}
}

type fakePreflight struct{ report preflight.Report }

func (f fakePreflight) Check(context.Context) preflight.Report { return f.report }

func TestControllerStartAppliesPreflightGate(t *testing.T) {
	engine := &fakeEngine{started: make(chan struct{})}
	report := preflight.Report{Checks: []preflight.Check{{
		ID: "opencode", Status: preflight.StatusFailed, Required: true, Message: "не найден",
	}}}
	controller, err := New(engine, t.TempDir(), WithPreflight(fakePreflight{report: report}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Start("feature", "задача"); err == nil || !strings.Contains(err.Error(), "opencode") {
		t.Fatalf("failed preflight принят: %v", err)
	}
	select {
	case <-engine.started:
		t.Fatal("engine запущен после failed preflight")
	default:
	}
}

func TestControllerAllowsEnqueueWhenReadinessIsUnknown(t *testing.T) {
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	controller, err := New(engine, t.TempDir(), WithPreflight(fakePreflight{report: preflight.Report{Unknown: true}}))
	if err != nil {
		t.Fatal(err)
	}
	runID, err := controller.Start("feature", "задача")
	if err != nil {
		t.Fatalf("unknown readiness blocked enqueue: %v", err)
	}
	<-engine.started
	close(engine.release)
	// Start returns before the process-local worker releases the workspace lock.
	// Wait for removal from active: run() only deletes that entry after closing
	// the lock, so t.TempDir cannot race the asynchronous cleanup.
	deadline := time.Now().Add(2 * time.Second)
	for {
		controller.mu.Lock()
		_, active := controller.active[runID]
		controller.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("controller worker did not finish after releasing the fake engine")
		}
		time.Sleep(time.Millisecond)
	}
}

type recordingModeEngine struct {
	*fakeEngine
	mu       sync.Mutex
	startCfg pipeline.RunConfig
}

func (e *recordingModeEngine) SupportsRunMode(string) bool { return true }

func (e *recordingModeEngine) Start(ctx context.Context, config pipeline.RunConfig) (pipeline.RunResult, error) {
	e.mu.Lock()
	e.startCfg = config
	e.mu.Unlock()
	return e.fakeEngine.Start(ctx, config)
}

func waitForControllerIdle(t *testing.T, controller *Controller, runID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		controller.mu.Lock()
		_, active := controller.active[runID]
		controller.mu.Unlock()
		if !active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("controller worker did not finish after releasing the fake engine")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestControllerStartTaskAppliesIdeaAndManualMode(t *testing.T) {
	engine := &recordingModeEngine{fakeEngine: &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}}
	controller, err := New(engine, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	admitted := false
	runID, err := controller.StartTask(context.Background(), "feature", "task text", "Название",
		"  Идея из браузера  ", "manual", []string{"JIRA-1 https://tracker.example/JIRA-1"}, func(run string) error {
			admitted = true
			if run == "" {
				t.Fatal("admission получил пустой run id")
			}
			return nil
		})
	if err != nil || runID == "" || !admitted {
		t.Fatalf("StartTask manual: run=%q admitted=%v err=%v", runID, admitted, err)
	}
	<-engine.started
	engine.mu.Lock()
	observed := engine.startCfg
	engine.mu.Unlock()
	if observed.TaskDesc != "Идея из браузера" {
		t.Fatalf("idea text must feed the run description: %q", observed.TaskDesc)
	}
	if observed.Mode != "" {
		t.Fatalf("manual mode must keep the default run mode, got %q", observed.Mode)
	}
	close(engine.release)
	waitForControllerIdle(t, controller, runID)
}

func TestControllerStartTaskFallsBackToTaskWhenIdeaEmpty(t *testing.T) {
	engine := &recordingModeEngine{fakeEngine: &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}}
	controller, err := New(engine, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runID, err := controller.StartTask(context.Background(), "feature", "task text", "", "", "", nil, func(string) error { return nil })
	if err != nil {
		t.Fatalf("StartTask without metadata: %v", err)
	}
	<-engine.started
	engine.mu.Lock()
	observed := engine.startCfg
	engine.mu.Unlock()
	if observed.TaskDesc != "task text" {
		t.Fatalf("empty idea must keep the task description: %q", observed.TaskDesc)
	}
	close(engine.release)
	waitForControllerIdle(t, controller, runID)
}

func TestControllerStartTaskFailsClosedOnUnsupportedMode(t *testing.T) {
	engine := &fakeEngine{}
	controller, err := New(engine, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartTask(context.Background(), "feature", "task", "", "", "turbo", nil, func(string) error { return nil }); err == nil {
		t.Fatal("unsupported mode должен быть отклонён")
	}
	// fakeEngine does not implement runModeEngine: auto must fail closed
	// instead of silently starting a manual run.
	admitted := false
	if _, err := controller.StartTask(context.Background(), "feature", "task", "", "идея", "auto", nil, func(string) error {
		admitted = true
		return nil
	}); err == nil || !strings.Contains(err.Error(), "auto mode") {
		t.Fatalf("auto mode on a mode-less engine must fail closed: %v", err)
	}
	if admitted {
		t.Fatal("admission не должен вызываться при отклонённом mode")
	}
}

func TestControllerStartAndCancelActiveWorker(t *testing.T) {
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	controller, err := New(engine, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runID, err := controller.Start("feature", "задача")
	if err != nil {
		t.Fatal(err)
	}
	<-engine.started
	if err := controller.Cancel(runID); err != nil {
		t.Fatal(err)
	}
	marker, markerErr := cancellationRequestPath(controller.target, runID)
	if markerErr != nil {
		t.Fatal(markerErr)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("cancel marker missing: %v", statErr)
	}
	if !cancellationRequested(controller.target, runID, make(chan struct{})) {
		t.Fatal("safe-boundary cancellation request was not durably persisted")
	}
	engine.mu.Lock()
	if engine.cancelCalls != 0 || engine.observed {
		engine.mu.Unlock()
		t.Fatal("cancel interrupted the active stage")
	}
	engine.mu.Unlock()
	close(engine.release)
	deadline := time.Now().Add(time.Second)
	for {
		engine.mu.Lock()
		calls := engine.cancelCalls
		engine.mu.Unlock()
		if calls == 1 {
			engine.mu.Lock()
			observed := engine.observed
			engine.mu.Unlock()
			if !observed {
				t.Fatal("engine did not observe cancellation at the stage boundary")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active cancel не доведён до RunEngine.Cancel")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestControllerRejectsDuplicateResume(t *testing.T) {
	target := t.TempDir()
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("identity"))
	if err := stateStore.Create(lifecycle.State{
		RunID: "run-resume", Feature: "feature", TargetDir: filepath.Clean(target), Task: "задача",
		Phase: lifecycle.PhaseResumable, NextStage: "analyst",
		ConfigSHA256: stringHex(digest[:]), WorkflowSHA256: stringHex(digest[:]),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	controller, err := New(engine, target)
	if err != nil {
		t.Fatal(err)
	}
	if controller.RunActive("run-resume") {
		t.Fatal("run is active before Resume")
	}
	if err := controller.Resume("run-resume"); err != nil {
		t.Fatal(err)
	}
	<-engine.started
	if !controller.RunActive("run-resume") || controller.RunActive("other-run") {
		t.Fatal("RunActive must report exactly the executing run")
	}
	if err := controller.Resume("run-resume"); !errors.Is(err, ErrActive) {
		t.Fatalf("duplicate resume: %v", err)
	}
	if err := controller.Cancel("run-resume"); err != nil {
		t.Fatal(err)
	}
	close(engine.release)
}

func TestControllerResumeWithBudgetAllowancePassesGrant(t *testing.T) {
	target := t.TempDir()
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("identity"))
	if err := stateStore.Create(lifecycle.State{
		RunID: "run-grant", Feature: "feature", TargetDir: filepath.Clean(target), Task: "задача",
		Phase: lifecycle.PhaseResumable, NextStage: "analyst",
		ConfigSHA256: stringHex(digest[:]), WorkflowSHA256: stringHex(digest[:]),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	controller, err := New(engine, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.ResumeWithBudgetAllowance("run-grant", "alice", " "); err == nil {
		t.Fatal("grant without a reason must be refused")
	}
	if err := controller.ResumeWithBudgetAllowance("run-grant", "alice", "why"); err != nil {
		t.Fatal(err)
	}
	<-engine.started
	engine.mu.Lock()
	grant := engine.resumeGrant
	engine.mu.Unlock()
	close(engine.release)
	<-engine.finished
	if grant == nil || grant.ActorID != "alice" || grant.Reason != "why" {
		t.Fatalf("engine resume grant = %+v", grant)
	}
}

type recordingApprovalStore struct {
	values    map[string]approval.PendingApproval
	decisions []approval.Decision
}

func newRecordingApprovalStore(values ...approval.PendingApproval) *recordingApprovalStore {
	store := &recordingApprovalStore{values: make(map[string]approval.PendingApproval)}
	for _, value := range values {
		store.values[value.RunID+"/"+value.ID] = value
	}
	return store
}

func (s *recordingApprovalStore) Create(value approval.PendingApproval) (approval.PendingApproval, error) {
	s.values[value.RunID+"/"+value.ID] = value
	return value, nil
}

func (s *recordingApprovalStore) Load(runID, approvalID string) (approval.PendingApproval, error) {
	value, ok := s.values[runID+"/"+approvalID]
	if !ok {
		return approval.PendingApproval{}, errors.New("approval not found")
	}
	return value, nil
}

func (s *recordingApprovalStore) List(runID string) ([]approval.PendingApproval, error) {
	var result []approval.PendingApproval
	for _, value := range s.values {
		if value.RunID == runID {
			result = append(result, value)
		}
	}
	return result, nil
}

func (s *recordingApprovalStore) Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error) {
	s.decisions = append(s.decisions, decision)
	value, ok := s.values[runID+"/"+approvalID]
	if !ok {
		return approval.PendingApproval{}, errors.New("approval not found")
	}
	value.Status = approval.StatusResolved
	value.ResolvedAction = decision.Action
	value.ResolvedAt = time.Now().UTC()
	value.Decisions = append(value.Decisions, decision)
	s.values[runID+"/"+approvalID] = value
	return value, nil
}

func (s *recordingApprovalStore) ResolveDeferred(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error) {
	return s.Decide(runID, approvalID, decision)
}

func newStageActionFixture(t *testing.T, state lifecycle.State, engine *fakeEngine, store *recordingApprovalStore) (*Controller, string) {
	t.Helper()
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	templateStore, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templateStore.PinCurrentForRun(state.RunID, state.RunID); err != nil {
		t.Fatal(err)
	}
	state.TargetDir = target
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now().UTC()
	}
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Create(state); err != nil {
		t.Fatal(err)
	}
	controller, err := New(engine, target, WithApprovalStore(store))
	if err != nil {
		t.Fatal(err)
	}
	return controller, target
}

func stageActionState(runID, stageID, approvalID string, phase lifecycle.Phase) lifecycle.State {
	return lifecycle.State{
		RunID: runID, Feature: "feature", Task: "task", Phase: phase,
		NextStage: stageID, PendingApprovalID: approvalID,
		ConfigSHA256: strings.Repeat("0", 64), WorkflowSHA256: strings.Repeat("1", 64),
	}
}

func waitControllerIdle(t *testing.T, controller *Controller, runID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		controller.mu.Lock()
		_, active := controller.active[runID]
		controller.mu.Unlock()
		if !active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("controller worker for %s did not finish", runID)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestControllerChangeStageExecutorBindsOverrideToPendingVisit(t *testing.T) {
	engine := &fakeEngine{}
	store := newRecordingApprovalStore(approval.PendingApproval{
		ID: "approval-executor", RunID: "run-executor", AttemptID: "attempt-1", Status: approval.StatusPending,
		FromStage: "tech_design", ToStage: "tech_design", Trigger: "stage_completed", SubjectHash: strings.Repeat("a", 64),
		RequiredRoles: []string{"architect"}, Actions: []string{"approve"}, Targets: map[string]string{"approve": "tech_design"},
	})
	controller, target := newStageActionFixture(t, stageActionState("run-executor", "tech_design", "approval-executor", lifecycle.PhaseWaiting), engine, store)

	if err := controller.ChangeStageExecutor("run-executor", "tech_design", "human", "actor-1"); err != nil {
		t.Fatal(err)
	}
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state, err := stateStore.Load("run-executor")
	if err != nil {
		t.Fatal(err)
	}
	override, ok := state.ExecutorOverrides["tech_design"]
	if !ok || override.Executor != "human" || override.PreviousExecutor != "agent" ||
		override.ActorID != "actor-1" || override.ApprovalID != "approval-executor" || override.VisitID != "approval-executor" {
		t.Fatalf("executor override not bound to the pending visit: %+v", state.ExecutorOverrides)
	}
	if err := controller.ChangeStageExecutor("missing-run", "tech_design", "human", "actor-1"); err == nil {
		t.Fatal("executor change for an unknown run must fail")
	}
}

func TestControllerSkipPendingHumanStagePreservesActorAndReason(t *testing.T) {
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	store := newRecordingApprovalStore(approval.PendingApproval{
		ID: "approval-skip", RunID: "run-skip-human", AttemptID: "attempt-1", Status: approval.StatusPending,
		Kind: approval.KindInput, FromStage: "design_review", ToStage: "design_review", Trigger: "human_input",
		SubjectHash: strings.Repeat("a", 64), RequiredRoles: []string{"reviewer"}, Quorum: approval.QuorumAny,
		Actions: []string{"reject", "submit", "skip"},
		Targets: map[string]string{"reject": "design_review", "submit": "design_review", "skip": "design_review"},
	})
	controller, _ := newStageActionFixture(t, stageActionState("run-skip-human", "design_review", "approval-skip", lifecycle.PhaseWaiting), engine, store)

	if err := controller.SkipStage(context.Background(), "run-skip-human", "design_review", "out of scope", "actor-1", "reviewer"); err != nil {
		t.Fatal(err)
	}
	if len(store.decisions) != 1 || store.decisions[0].Action != "skip" ||
		store.decisions[0].ActorID != "actor-1" || store.decisions[0].ActorRole != "reviewer" ||
		store.decisions[0].Comment != "out of scope" || !store.decisions[0].ControllerAuthenticated {
		t.Fatalf("skip decision lost actor or reason: %+v", store.decisions)
	}
	<-engine.started
	close(engine.release)
	waitControllerIdle(t, controller, "run-skip-human")
}

func TestControllerAnswerStageGrillResolvesAndResumesCurrentQuestion(t *testing.T) {
	payload, err := json.Marshal(approval.GrillQuestionsPayload{
		Kind: string(approval.KindGrillQuestions), Markdown: "**Question:** Уточните критерий.", Round: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	store := newRecordingApprovalStore(approval.PendingApproval{
		ID: "approval-grill", RunID: "run-grill", AttemptID: "attempt-grill", Status: approval.StatusPending,
		Kind: approval.KindGrillQuestions, FromStage: "product_spec", ToStage: "product_spec", Trigger: approval.GrillTrigger,
		SubjectHash: strings.Repeat("a", 64), RequiredRoles: []string{"po"}, Quorum: approval.QuorumAny,
		Actions: []string{approval.GrillActionAnswer, approval.GrillActionNewVersion, approval.GrillActionDone},
		Targets: map[string]string{
			approval.GrillActionAnswer: "product_spec", approval.GrillActionNewVersion: "product_spec", approval.GrillActionDone: "product_spec",
		}, Payload: payload,
	})
	controller, _ := newStageActionFixture(t, stageActionState("run-grill", "product_spec", "approval-grill", lifecycle.PhaseWaiting), engine, store)
	if err := controller.AnswerStageGrill(context.Background(), "run-grill", "product_spec", "Критерий измеряется ростом на 20%.", "owner", "po"); err != nil {
		t.Fatal(err)
	}
	if len(store.decisions) != 1 || store.decisions[0].Action != approval.GrillActionAnswer ||
		store.decisions[0].Comment != "Критерий измеряется ростом на 20%." || !store.decisions[0].ControllerAuthenticated {
		t.Fatalf("grill answer did not preserve the authenticated decision: %+v", store.decisions)
	}
	<-engine.started
	close(engine.release)
	waitControllerIdle(t, controller, "run-grill")
}

func TestControllerGrillNewVersionRequiresEditedArtifact(t *testing.T) {
	payload, err := json.Marshal(approval.GrillQuestionsPayload{
		Kind: string(approval.KindGrillQuestions), Markdown: "**Question:** Уточните критерий.", Round: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{}
	store := newRecordingApprovalStore(approval.PendingApproval{
		ID: "approval-grill-edit", RunID: "run-grill-edit", AttemptID: "missing-attempt", Status: approval.StatusPending,
		Kind: approval.KindGrillQuestions, FromStage: "product_spec", ToStage: "product_spec", Trigger: approval.GrillTrigger,
		SubjectHash: strings.Repeat("b", 64), RequiredRoles: []string{"po"}, Quorum: approval.QuorumAny,
		Actions: []string{approval.GrillActionAnswer, approval.GrillActionNewVersion, approval.GrillActionDone},
		Targets: map[string]string{
			approval.GrillActionAnswer: "product_spec", approval.GrillActionNewVersion: "product_spec", approval.GrillActionDone: "product_spec",
		}, Payload: payload,
	})
	controller, _ := newStageActionFixture(t, stageActionState("run-grill-edit", "product_spec", "approval-grill-edit", lifecycle.PhaseWaiting), engine, store)
	if err := controller.SubmitStageGrillVersion(context.Background(), "run-grill-edit", "product_spec", "", "owner", "po"); err == nil {
		t.Fatal("new_version must fail when no edited artifact revision exists")
	}
	if len(store.decisions) != 0 {
		t.Fatalf("invalid new_version should not be decided: %+v", store.decisions)
	}
}

func TestControllerGrillNewVersionRequiresChangedPinnedContent(t *testing.T) {
	const runID, stageID = "run-grill-content-change", "product_spec"
	const attemptID, approvalID = "run-grill-content-change-001-product_spec", "approval-grill-content-change"
	artifactPath := "stages/product_spec/result.md"
	baselineContent := "# Product spec\n\nКритерии приёмки\nInitial criteria.\n"
	baselineSHA := humanartifact.Digest([]byte(baselineContent))
	baselineID := "rev-000001-" + baselineSHA[:16]
	payload, err := json.Marshal(approval.GrillQuestionsPayload{
		Kind: string(approval.KindGrillQuestions), Markdown: "**Question:** Clarify acceptance criteria.", Round: 1,
		ArtifactRevisions: map[string]string{artifactPath: baselineID},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	store := newRecordingApprovalStore(approval.PendingApproval{
		ID: approvalID, RunID: runID, AttemptID: attemptID, Status: approval.StatusPending,
		Kind: approval.KindGrillQuestions, FromStage: stageID, ToStage: stageID, Trigger: approval.GrillTrigger,
		SubjectHash: strings.Repeat("c", 64), RequiredRoles: []string{"po"}, Quorum: approval.QuorumAny,
		Actions: []string{approval.GrillActionAnswer, approval.GrillActionNewVersion, approval.GrillActionDone},
		Targets: map[string]string{
			approval.GrillActionAnswer: stageID, approval.GrillActionNewVersion: stageID, approval.GrillActionDone: stageID,
		}, Payload: payload,
	})
	controller, _ := newStageActionFixture(t, stageActionState(runID, stageID, approvalID, lifecycle.PhaseWaiting), engine, store)
	target := controller.target
	artifacts, err := humanartifact.New(target)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := artifacts.Append(runID, artifactPath, "", "", baselineContent, "", "owner")
	if err != nil || baseline.ID != baselineID {
		t.Fatalf("create pinned baseline revision: %+v err=%v, want ID %s", baseline, err, baselineID)
	}
	commentOnlyRevision, err := artifacts.Append(runID, artifactPath, baseline.ID, "", baselineContent,
		"Comment without content changes", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if commentOnlyRevision.ID == baseline.ID || commentOnlyRevision.SHA256 != baseline.SHA256 {
		t.Fatalf("fixture must represent a new revision with unchanged bytes: baseline=%+v candidate=%+v", baseline, commentOnlyRevision)
	}
	manifest := evidence.AttemptManifest{
		SchemaVersion: evidence.SchemaVersion, RunID: runID, AttemptID: attemptID,
		Stage: stageID, StageIndex: 1, StartedAt: time.Now().UTC().Add(-time.Minute), FinishedAt: time.Now().UTC(),
		Status: "completed", Execution: "success", Decision: "continue", Outcome: "success",
	}
	manifestPath := filepath.Join(target, ".ai-team", "runs", runID, "attempts", attemptID, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := controller.SubmitStageGrillVersion(context.Background(), runID, stageID, "comment only", "owner", "po"); err == nil || !strings.Contains(err.Error(), "content to differ") {
		t.Fatalf("new_version must reject a different revision ID with unchanged bytes: %v", err)
	}
	if len(store.decisions) != 0 {
		t.Fatalf("unchanged content must not resolve the grill question: %+v", store.decisions)
	}

	updatedContent := "# Product spec\n\nКритерии приёмки\nUpdated measurable criteria.\n"
	updated, err := artifacts.Append(runID, artifactPath, commentOnlyRevision.ID, "", updatedContent, "Actual edit", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.SubmitStageGrillVersion(context.Background(), runID, stageID, "updated", "owner", "po"); err != nil {
		t.Fatalf("new_version should accept content changed from its pinned baseline: %v", err)
	}
	if len(store.decisions) != 1 || store.decisions[0].ArtifactRevisions[artifactPath] != updated.ID {
		t.Fatalf("changed revision was not pinned on the grill decision: %+v", store.decisions)
	}
	<-engine.started
	close(engine.release)
	waitControllerIdle(t, controller, runID)
}

func TestControllerReviewedRevisionsIncludesStageScopedHumanResult(t *testing.T) {
	target := t.TempDir()
	const runID, attemptID = "run-grill-human-revision", "run-grill-human-revision-001-product_spec"
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	manifest := evidence.AttemptManifest{
		SchemaVersion: evidence.SchemaVersion, RunID: runID, AttemptID: attemptID,
		Stage: "product_spec", StageIndex: 1, StartedAt: time.Now().UTC().Add(-time.Minute), FinishedAt: time.Now().UTC(),
		Status: "completed", Execution: "success", Decision: "continue", Outcome: "success",
		Outputs: []evidence.ArtifactRecord{{Name: "result", EvidencePath: "attempts/" + attemptID + "/artifacts/result.md"}},
	}
	manifestPath := filepath.Join(runDir, "attempts", attemptID, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts, err := humanartifact.New(target)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := artifacts.Append(runID, "stages/product_spec/result.md", "", "", "## Updated\nMeasurable criteria\n", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	controller := &Controller{target: target}
	selection, err := controller.reviewedRevisions(runID, approval.PendingApproval{AttemptID: attemptID})
	if err != nil {
		t.Fatal(err)
	}
	if selection["stages/product_spec/result.md"] != revision.ID {
		t.Fatalf("controller did not pin the stage-scoped human revision: got %v, want %s", selection, revision.ID)
	}
}

func TestControllerSkipResumableAgentStageUsesEngine(t *testing.T) {
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	store := newRecordingApprovalStore()
	controller, _ := newStageActionFixture(t, stageActionState("run-skip-agent", "tech_design", "", lifecycle.PhaseResumable), engine, store)

	if err := controller.SkipStage(context.Background(), "run-skip-agent", "tech_design", "not needed", "actor-1", "architect"); err != nil {
		t.Fatal(err)
	}
	<-engine.started
	close(engine.release)
	waitControllerIdle(t, controller, "run-skip-agent")
	engine.mu.Lock()
	config := engine.skipConfig
	calls := engine.skipCalls
	engine.mu.Unlock()
	if calls != 1 || config.RunID != "run-skip-agent" || config.StageID != "tech_design" || config.Reason != "not needed" {
		t.Fatalf("engine skip config mismatch: calls=%d config=%+v", calls, config)
	}
}

func TestControllerRunStageAgentRefineFeedsCurrentResult(t *testing.T) {
	payload, err := json.Marshal(approval.InputPayload{
		Kind: string(approval.KindInput), StageID: "design_review", Result: "md",
		OutputName: "result", OutputPath: "feature/result.md", AgentEnabled: true, InitialResult: "current agent result",
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	store := newRecordingApprovalStore(approval.PendingApproval{
		ID: "approval-agent", RunID: "run-agent", AttemptID: "attempt-1", Status: approval.StatusPending,
		Kind: approval.KindInput, FromStage: "design_review", ToStage: "design_review", Trigger: "human_input",
		SubjectHash: strings.Repeat("a", 64), RequiredRoles: []string{"reviewer"}, Quorum: approval.QuorumAny,
		Actions: []string{"reject", "submit", "run_agent", "refine_agent"},
		Targets: map[string]string{"reject": "design_review", "submit": "design_review", "run_agent": "design_review", "refine_agent": "design_review"},
		Payload: payload,
	})
	controller, _ := newStageActionFixture(t, stageActionState("run-agent", "design_review", "approval-agent", lifecycle.PhaseWaiting), engine, store)

	if err := controller.RunStageAgent(context.Background(), "run-agent", "design_review", "actor-1", true); err != nil {
		t.Fatal(err)
	}
	if len(store.decisions) != 1 || store.decisions[0].Action != "refine_agent" ||
		store.decisions[0].ActorRole != "reviewer" || store.decisions[0].Comment != "current agent result" {
		t.Fatalf("refine decision did not carry the current result: %+v", store.decisions)
	}
	<-engine.started
	close(engine.release)
	waitControllerIdle(t, controller, "run-agent")
}

func TestControllerReturnStageResolvesConfiguredRoute(t *testing.T) {
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	store := newRecordingApprovalStore(approval.PendingApproval{
		ID: "approval-return", RunID: "run-return", AttemptID: "attempt-1", Status: approval.StatusPending,
		FromStage: "design_review", ToStage: "design_review", Trigger: "graph_outcome:passed",
		SubjectHash: strings.Repeat("a", 64), RequiredRoles: []string{"reviewer"}, Quorum: approval.QuorumAny,
		Actions: []string{"approve", "return_to_tech_design"},
		Targets: map[string]string{"approve": "implementation", "return_to_tech_design": "tech_design"},
	})
	controller, _ := newStageActionFixture(t, stageActionState("run-return", "design_review", "approval-return", lifecycle.PhaseWaiting), engine, store)

	if err := controller.ReturnStage(context.Background(), "run-return", "design_review", "tech_design", "redo design", "actor-1", "reviewer"); err != nil {
		t.Fatal(err)
	}
	if len(store.decisions) != 1 || store.decisions[0].Action != "return_to_tech_design" ||
		store.decisions[0].Comment != "redo design" || !store.decisions[0].ControllerAuthenticated {
		t.Fatalf("return decision mismatch: %+v", store.decisions)
	}
	if err := controller.ReturnStage(context.Background(), "run-return", "design_review", "qa", "nope", "actor-1", "reviewer"); err == nil {
		t.Fatal("return to an unconfigured target must fail")
	}
}

func stringHex(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = digits[item>>4]
		result[index*2+1] = digits[item&15]
	}
	return string(result)
}
