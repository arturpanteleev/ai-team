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
	})
}

func (e *RunEngine) Cancel(config CancelConfig) (RunResult, error) {
	if config.RunID == "" {
		return RunResult{}, errors.New("RunEngine.Cancel требует run_id")
	}
	lock, err := evidence.AcquireWorkspaceLock(config.TargetDir)
	if err != nil {
		return RunResult{}, err
	}
	defer lock.Close()
	stateStore, err := lifecycle.NewStore(config.TargetDir)
	if err != nil {
		return RunResult{}, err
	}
	state, err := stateStore.Load(config.RunID)
	if err != nil {
		return RunResult{}, err
	}
	if state.Phase == lifecycle.PhaseTerminal {
		return RunResult{}, fmt.Errorf("run %s уже terminal", config.RunID)
	}
	evidenceStore, _, replayed, err := evidence.Resume(
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
