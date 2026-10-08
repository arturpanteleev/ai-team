package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// RunEngine — process-independent entrypoint жизненного цикла run. Pipeline
// исполняет stages, а engine различает создание новой identity и resume
// существующей.
type RunEngine struct {
	pipeline *Pipeline
}

type ResumeConfig struct {
	RunID           string
	TargetDir       string
	ApproveGates    bool
	ApprovePlanHash string
	CancelRequested func() bool
}

type CancelConfig struct {
	RunID     string
	TargetDir string
}

func NewRunEngine(pipeline *Pipeline) *RunEngine {
	return &RunEngine{pipeline: pipeline}
}

func (e *RunEngine) Start(ctx context.Context, config RunConfig) (RunResult, error) {
	if config.ResumeRunID != "" {
		return RunResult{}, errors.New("RunEngine.Start не принимает resume_run_id")
	}
	return e.pipeline.RunWithResult(ctx, config)
}

func (e *RunEngine) Resume(ctx context.Context, config ResumeConfig) (RunResult, error) {
	if config.RunID == "" {
		return RunResult{}, errors.New("RunEngine.Resume требует run_id")
	}
	return e.pipeline.RunWithResult(ctx, RunConfig{
		ResumeRunID:     config.RunID,
		TargetDir:       config.TargetDir,
		ApproveGates:    config.ApproveGates,
		ApprovePlanHash: config.ApprovePlanHash,
		CancelRequested: config.CancelRequested,
	})
}

// RecoverInitialLifecycle reconstructs the lifecycle checkpoint when a worker
// died after durable run_started evidence was written but before lifecycle
// creation. It accepts only the exact initial evidence prefix, so later or
// ambiguous run state is never mistaken for a fresh admission.
func (e *RunEngine) RecoverInitialLifecycle(runID, targetDir, feature, task string) error {
	return e.pipeline.recoverInitialLifecycle(runID, targetDir, feature, task)
}

// ReconcileTerminalDelivery verifies that a terminal run's approved deferred
// delivery reached its durable terminal record. If delivery was interrupted,
// it resumes through the same validated delivery state machine.
func (e *RunEngine) ReconcileTerminalDelivery(ctx context.Context, runID, targetDir string) error {
	return e.pipeline.ReconcileTerminalDelivery(ctx, runID, targetDir)
}

func (e *RunEngine) Cancel(config CancelConfig) (RunResult, error) {
	if config.RunID == "" {
		return RunResult{}, errors.New("RunEngine.Cancel требует run_id")
	}
	lock, err := evidence.AcquireWorkspaceLock(config.TargetDir)
	if err != nil {
		return RunResult{}, err
	}
	defer func() { _ = lock.Close() }() // снятие файловой блокировки: значимый результат уже посчитан.
	stateStore := e.pipeline.lifecycle
	if stateStore == nil {
		stateStore, err = lifecycle.NewStore(config.TargetDir)
		if err != nil {
			return RunResult{}, err
		}
	}
	state, err := stateStore.Load(config.RunID)
	if err != nil {
		return RunResult{}, err
	}
	if state.Phase == lifecycle.PhaseTerminal {
		return RunResult{}, fmt.Errorf("run %s уже terminal", config.RunID)
	}
	evidenceStore, _, replayed, err := e.pipeline.resumeEvidence(
		filepath.Join(config.TargetDir, ".ai-team", "runs"), config.RunID,
	)
	if err != nil {
		return RunResult{}, err
	}
	now := time.Now().UTC()
	if err := evidenceStore.Append(evidence.Event{
		Type: "run_canceled", Timestamp: now,
		Data: map[string]any{"reason": "human_cancel"},
	}); err != nil {
		return RunResult{}, err
	}
	if err := evidenceStore.Append(evidence.Event{
		Type: "run_finished", Timestamp: now,
		Data: map[string]any{"status": string(workflow.RunCanceled), "stage_attempts": len(replayed.Attempts)},
	}); err != nil {
		return RunResult{}, err
	}
	terminal := state
	terminal.Phase = lifecycle.PhaseTerminal
	terminal.NextStage = ""
	terminal.PendingApprovalID = ""
	terminal.AttemptOrdinal = len(replayed.Attempts)
	if err := stateStore.Save(state, terminal); err != nil {
		return RunResult{}, err
	}
	if e.pipeline.recorder != nil {
		e.pipeline.recorder.ReconcileInterrupted(now)
		e.pipeline.recorder.RunAttached(config.RunID)
		e.pipeline.recorder.RunCanceled(config.RunID, now)
		e.pipeline.recorder.RunFinished(config.RunID, string(workflow.RunCanceled), now)
	}
	return RunResult{RunID: config.RunID, Outcome: workflow.RunCanceled}, nil
}

// LoadLifecycle and SaveLifecycle expose the same narrow
// checkpoint port to the worker recovery dispatcher without opening the
// lifecycle directory in the child process.
func (e *RunEngine) LoadLifecycle(targetDir, runID string) (lifecycle.State, error) {
	store, err := e.lifecycleStore(targetDir)
	if err != nil {
		return lifecycle.State{}, err
	}
	return store.Load(runID)
}

func (e *RunEngine) SaveLifecycle(targetDir string, previous, next lifecycle.State) error {
	store, err := e.lifecycleStore(targetDir)
	if err != nil {
		return err
	}
	return store.Save(previous, next)
}

func (e *RunEngine) lifecycleStore(targetDir string) (lifecycle.StorePort, error) {
	if e.pipeline.lifecycle != nil {
		return e.pipeline.lifecycle, nil
	}
	return lifecycle.NewStore(targetDir)
}
