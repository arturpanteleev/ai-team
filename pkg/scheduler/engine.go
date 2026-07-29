package scheduler

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/worker"
)

type QueueEngine struct {
	queue  *Queue
	target string
}

func NewQueueEngine(queue *Queue, target string) (*QueueEngine, error) {
	if queue == nil {
		return nil, errors.New("scheduler queue обязательна")
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	return &QueueEngine{queue: queue, target: filepath.Clean(absolute)}, nil
}

func (e *QueueEngine) Start(_ context.Context, config pipeline.RunConfig) (pipeline.RunResult, error) {
	job := worker.Job{
		SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart,
		RunID: config.RunID, TargetDir: config.TargetDir, Feature: config.Feature, Task: config.TaskDesc,
		ApproveGates: config.ApproveGates, ApprovePlanHash: config.ApprovePlanHash,
	}
	if !sameTarget(config.TargetDir, e.target) {
		return pipeline.RunResult{}, errors.New("scheduler start target mismatch")
	}
	_, err := e.queue.Enqueue(job)
	return pipeline.RunResult{RunID: config.RunID}, err
}

func (e *QueueEngine) Resume(_ context.Context, config pipeline.ResumeConfig) (pipeline.RunResult, error) {
	job := worker.Job{
		SchemaVersion: worker.SchemaVersion, Operation: worker.OperationResume,
		RunID: config.RunID, TargetDir: config.TargetDir,
		ApproveGates: config.ApproveGates, ApprovePlanHash: config.ApprovePlanHash,
	}
	if !sameTarget(config.TargetDir, e.target) {
		return pipeline.RunResult{}, errors.New("scheduler resume target mismatch")
	}
	_, err := e.queue.Enqueue(job)
	return pipeline.RunResult{RunID: config.RunID}, err
}

func (e *QueueEngine) Cancel(config pipeline.CancelConfig) (pipeline.RunResult, error) {
	if !sameTarget(config.TargetDir, e.target) {
		return pipeline.RunResult{}, errors.New("scheduler cancel target mismatch")
	}
	_, err := e.queue.CancelRun(config.RunID)
	if err != nil {
		return pipeline.RunResult{}, err
	}
	job := worker.Job{
		SchemaVersion: worker.SchemaVersion, Operation: worker.OperationCancel,
		RunID: config.RunID, TargetDir: config.TargetDir,
	}
	if _, enqueueErr := e.queue.Enqueue(job); enqueueErr != nil && !errors.Is(enqueueErr, ErrDuplicate) {
		err = enqueueErr
	}
	return pipeline.RunResult{RunID: config.RunID}, err
}

func sameTarget(actual, expected string) bool {
	absolute, err := filepath.Abs(actual)
	if err != nil {
		return false
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	return err == nil && filepath.Clean(canonical) == filepath.Clean(expected)
}
