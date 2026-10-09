package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
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
	if config.RunID == "" {
		runID, err := evidence.NewRunID(time.Now().UTC())
		if err != nil {
			return RunResult{}, err
		}
		config.RunID = runID
	}
	runPipeline, err := e.pipelineForTask(config.RunID, config.TargetDir, true)
	if err != nil {
		return RunResult{RunID: config.RunID, Outcome: workflow.RunFailed}, err
	}
	return runPipeline.RunWithResult(ctx, config)
}

func (e *RunEngine) Resume(ctx context.Context, config ResumeConfig) (RunResult, error) {
	if config.RunID == "" {
		return RunResult{}, errors.New("RunEngine.Resume требует run_id")
	}
	runPipeline, err := e.pipelineForTask(config.RunID, config.TargetDir, false)
	if err != nil {
		return RunResult{RunID: config.RunID, Outcome: workflow.RunFailed}, err
	}
	return runPipeline.RunWithResult(ctx, RunConfig{
		ResumeRunID:     config.RunID,
		TargetDir:       config.TargetDir,
		ApproveGates:    config.ApproveGates,
		ApprovePlanHash: config.ApprovePlanHash,
		CancelRequested: config.CancelRequested,
	})
}

func (e *RunEngine) pipelineForTask(runID, targetDir string, createPin bool) (*Pipeline, error) {
	return e.resolvePipelineForTask(runID, targetDir, createPin, false)
}

// pipelineForPinnedDelivery resolves the immutable pin even when the engine
// was constructed with a legacy in-memory config. Retry and recovery must
// retain the originating run's delivery timeout; Start/Resume must retain an
// explicitly supplied legacy workflow override.
func (e *RunEngine) pipelineForPinnedDelivery(runID, targetDir string) (*Pipeline, error) {
	return e.resolvePipelineForTask(runID, targetDir, false, true)
}

func (e *RunEngine) resolvePipelineForTask(runID, targetDir string, createPin, forcePinned bool) (*Pipeline, error) {
	if targetDir == "" || runID == "" || e == nil || e.pipeline == nil {
		if e == nil || e.pipeline == nil {
			return nil, errors.New("RunEngine pipeline is unavailable")
		}
		return e.pipeline, nil
	}
	cfg := e.pipeline.cfg
	if cfg == nil {
		cfg = config.Default()
	}
	// Preserve explicit legacy in-memory configuration overrides across Start
	// and Resume. Delivery recovery opts into pinned resolution separately so a
	// default CLI engine can still honor an existing task pin.
	if cfg.Template == "" && !forcePinned {
		return e.pipeline, nil
	}
	store, err := config.NewTemplateStore(targetDir)
	if err != nil {
		return nil, fmt.Errorf("template task pin store: %w", err)
	}
	data, _, found, err := store.ReadPinnedRun(runID)
	if err != nil {
		return nil, fmt.Errorf("read template task pin: %w", err)
	}
	if !found && createPin {
		data, _, err = store.ReadCurrent()
		if errors.Is(err, os.ErrNotExist) {
			data, err = cfg.Marshal()
		}
		if err != nil {
			return nil, fmt.Errorf("resolve template for new task: %w", err)
		}
		if _, err := store.PinDataForRun(runID, runID, data); err != nil {
			return nil, fmt.Errorf("pin template for new task: %w", err)
		}
		data, _, found, err = store.ReadPinnedRun(runID)
		if err != nil || !found {
			if err == nil {
				err = errors.New("task template pin was not created")
			}
			return nil, fmt.Errorf("read created template task pin: %w", err)
		}
	}
	if !found {
		// Existing pre-editor runs have no task pin. Preserve their historical
		// resume behavior, whose evidence digest still rejects config drift.
		return e.pipeline, nil
	}
	pinned, err := config.ParseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("pinned task template YAML: %w", err)
	}
	var registry config.AgentLookup
	if e.pipeline.reg != nil {
		registry = e.pipeline.reg
	}
	if err := pinned.Validate(registry); err != nil {
		return nil, fmt.Errorf("pinned task template validation: %w", err)
	}
	runPipeline := *e.pipeline
	runPipeline.cfg = pinned
	return &runPipeline, nil
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
	runPipeline, err := e.pipelineForPinnedDelivery(runID, targetDir)
	if err != nil {
		return fmt.Errorf("resolve template for delivery recovery: %w", err)
	}
	return runPipeline.ReconcileTerminalDelivery(ctx, runID, targetDir)
}

// DeliverDeferred retries terminal delivery using the run's immutable task
// template when one exists. Legacy runs without a task pin keep the engine's
// configured delivery timeout.
func (e *RunEngine) DeliverDeferred(ctx context.Context, runID, targetDir string) (delivery.TerminalRecord, error) {
	return e.DeliverDeferredForFeature(ctx, runID, "", targetDir)
}

// DeliverDeferredForFeature preserves the legacy feature override accepted by
// the CLI while resolving the immutable template pin in the same way.
func (e *RunEngine) DeliverDeferredForFeature(ctx context.Context, runID, feature, targetDir string) (delivery.TerminalRecord, error) {
	if err := evidence.ValidateRunID(runID); err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: invalid run id: %w", err)
	}
	runPipeline, err := e.pipelineForPinnedDelivery(runID, targetDir)
	if err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("resolve template for deferred delivery: %w", err)
	}
	runDir := filepath.Join(targetDir, ".ai-team", "runs", runID)
	return runPipeline.DeliverDeferred(ctx, runDir, feature, targetDir)
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
