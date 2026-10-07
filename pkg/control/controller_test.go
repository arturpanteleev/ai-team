package control

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
)

type fakeEngine struct {
	mu          sync.Mutex
	started     chan struct{}
	release     chan struct{}
	observed    bool
	cancelCalls int
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

func TestNewRequiresEngine(t *testing.T) {
	if _, err := New(nil, t.TempDir()); err == nil {
		t.Fatal("controller без engine принят")
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
	if err := controller.Resume("run-resume"); err != nil {
		t.Fatal(err)
	}
	<-engine.started
	if err := controller.Resume("run-resume"); !errors.Is(err, ErrActive) {
		t.Fatalf("duplicate resume: %v", err)
	}
	if err := controller.Cancel("run-resume"); err != nil {
		t.Fatal(err)
	}
	close(engine.release)
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
