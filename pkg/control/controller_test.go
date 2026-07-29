package control

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
)

type fakeEngine struct {
	mu          sync.Mutex
	started     chan struct{}
	cancelCalls int
}

func (f *fakeEngine) Start(ctx context.Context, config pipeline.RunConfig) (pipeline.RunResult, error) {
	close(f.started)
	<-ctx.Done()
	return pipeline.RunResult{RunID: config.RunID}, ctx.Err()
}
func (f *fakeEngine) Resume(ctx context.Context, config pipeline.ResumeConfig) (pipeline.RunResult, error) {
	close(f.started)
	<-ctx.Done()
	return pipeline.RunResult{RunID: config.RunID}, ctx.Err()
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

func TestControllerStartAndCancelActiveWorker(t *testing.T) {
	engine := &fakeEngine{started: make(chan struct{})}
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
	deadline := time.Now().Add(time.Second)
	for {
		engine.mu.Lock()
		calls := engine.cancelCalls
		engine.mu.Unlock()
		if calls == 1 {
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
	engine := &fakeEngine{started: make(chan struct{})}
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
