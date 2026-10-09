// Package control связывает долговечный RunEngine с process-local workers.
package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

var ErrActive = errors.New("run уже исполняется")

type worker struct {
	cancel          context.CancelFunc
	cancelRequested chan struct{}
}

type runEngine interface {
	Start(context.Context, pipeline.RunConfig) (pipeline.RunResult, error)
	Resume(context.Context, pipeline.ResumeConfig) (pipeline.RunResult, error)
	Cancel(pipeline.CancelConfig) (pipeline.RunResult, error)
}

type PreflightChecker interface {
	Check(context.Context) preflight.Report
}

type Option func(*Controller)

func WithPreflight(checker PreflightChecker) Option {
	return func(controller *Controller) { controller.preflight = checker }
}

// WithApprovalStore uses the same approval persistence as the worker pipeline.
// In web mode this lets authenticated controller decisions survive process
// restarts and be observed by scheduler workers.
func WithApprovalStore(store pipeline.ApprovalStore) Option {
	return func(controller *Controller) {
		if store != nil {
			controller.approvals = store
		}
	}
}

type Controller struct {
	engine        runEngine
	target        string
	approvals     pipeline.ApprovalStore
	preflight     PreflightChecker
	failureSink   FailureSink
	admissionSink func(string, int64) error

	mu     sync.Mutex
	active map[string]*worker
}

func New(engine runEngine, target string, options ...Option) (*Controller, error) {
	if engine == nil {
		return nil, errors.New("run engine обязателен")
	}
	controller := &Controller{
		engine: engine, target: target,
		active: make(map[string]*worker),
	}
	for _, option := range options {
		option(controller)
	}
	if controller.approvals == nil {
		store, err := approval.NewStore(target)
		if err != nil {
			return nil, err
		}
		controller.approvals = store
	}
	return controller, nil
}

func (c *Controller) Start(feature, task string) (string, error) {
	return c.start(feature, task, nil)
}

// StartWithAdmission persists the caller's visible admission record before a
// worker can enqueue or execute the run. A failed admission therefore never
// leaves a scheduler job behind a 503 response.
func (c *Controller) StartWithAdmission(feature, task string, admit func(runID string) error) (string, error) {
	if admit == nil {
		return "", errors.New("admission callback обязателен")
	}
	return c.start(feature, task, admit)
}

func (c *Controller) start(feature, task string, admit func(runID string) error) (string, error) {
	if !workflow.ValidFeature(feature) || strings.TrimSpace(task) == "" {
		return "", errors.New("feature и непустой task обязательны")
	}
	if c.preflight != nil {
		if report := c.Preflight(context.Background()); !report.Ready && !report.Unknown {
			return "", report.Error()
		}
	}
	// Синхронное резервирование target до ответа 202: без захваченного
	// lock второй start мог бы получить 202, а затем молча исчезнуть.
	lock, err := evidence.AcquireWorkspaceLock(c.target)
	if err != nil {
		return "", fmt.Errorf("target занят другим run: %w", err)
	}
	runID, err := evidence.NewRunID(time.Now().UTC())
	if err != nil {
		_ = lock.Close()
		return "", err
	}
	if admit != nil {
		if err := admit(runID); err != nil {
			_ = lock.Close()
			return "", fmt.Errorf("run admission: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	active := &worker{cancel: cancel, cancelRequested: make(chan struct{})}
	c.mu.Lock()
	c.active[runID] = active
	c.mu.Unlock()
	go c.run(runID, ctx, lock, func(ctx context.Context) (pipeline.RunResult, error) {
		return c.engine.Start(ctx, pipeline.RunConfig{
			RunID: runID, Feature: feature, TaskDesc: task, TargetDir: c.target,
			WorkspaceLock: lock, CancelRequested: func() bool { return cancellationRequested(c.target, runID, active.cancelRequested) },
		})
	})
	return runID, nil
}

func (c *Controller) Preflight(ctx context.Context) preflight.Report {
	if c.preflight == nil {
		// Enqueue-only режим: готовность runtime известна только воркерам.
		return preflight.Report{Unknown: true, CheckedAt: time.Now().UTC()}
	}
	return c.preflight.Check(ctx)
}

func (c *Controller) Resume(runID string) error {
	stateStore, err := lifecycle.NewStore(c.target)
	if err != nil {
		return err
	}
	state, err := stateStore.Load(runID)
	if err != nil {
		return err
	}
	if state.Phase == lifecycle.PhaseTerminal {
		return fmt.Errorf("run %s уже terminal", runID)
	}
	if state.Phase == lifecycle.PhaseWaiting {
		value, err := c.approvals.Load(runID, state.PendingApprovalID)
		if err != nil {
			return err
		}
		if value.Status != approval.StatusResolved {
			return errors.New("run всё ещё ожидает human decision")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	active := &worker{cancel: cancel, cancelRequested: make(chan struct{})}
	c.mu.Lock()
	if _, exists := c.active[runID]; exists {
		c.mu.Unlock()
		cancel()
		return ErrActive
	}
	c.active[runID] = active
	c.mu.Unlock()
	go c.run(runID, ctx, nil, func(ctx context.Context) (pipeline.RunResult, error) {
		return c.engine.Resume(ctx, pipeline.ResumeConfig{RunID: runID, TargetDir: c.target,
			CancelRequested: func() bool { return cancellationRequested(c.target, runID, active.cancelRequested) }})
	})
	return nil
}

func (c *Controller) Cancel(runID string) error {
	c.mu.Lock()
	if active := c.active[runID]; active != nil {
		if err := writeCancellationRequest(c.target, runID); err != nil {
			c.mu.Unlock()
			return err
		}
		select {
		case <-active.cancelRequested:
		default:
			close(active.cancelRequested)
		}
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	_, err := c.engine.Cancel(pipeline.CancelConfig{RunID: runID, TargetDir: c.target})
	return err
}

func (c *Controller) Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error) {
	return c.approvals.Decide(runID, approvalID, decision)
}

func (c *Controller) Approvals(runID string) ([]approval.PendingApproval, error) {
	return c.approvals.List(runID)
}

// DeliverDeferred retries the exact deferred delivery prepared by a terminal
// run. Delivery executes in this trusted controller process and reuses the
// pipeline's marker, plan, candidate, and workspace-lock checks.
func (c *Controller) DeliverDeferred(ctx context.Context, runID string) (delivery.TerminalRecord, error) {
	if err := evidence.ValidateRunID(runID); err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: invalid run id: %w", err)
	}
	c.mu.Lock()
	_, active := c.active[runID]
	c.mu.Unlock()
	if active {
		return delivery.TerminalRecord{}, ErrActive
	}
	runDir := filepath.Join(c.target, ".ai-team", "runs", runID)
	return pipeline.New(nil, nil, pipeline.WithApprovalStore(c.approvals)).DeliverDeferred(ctx, runDir, "", c.target)
}

func (c *Controller) run(runID string, ctx context.Context, lock *evidence.WorkspaceLock, execute func(context.Context) (pipeline.RunResult, error)) {
	result, execErr := execute(ctx)
	if execErr == nil && result.QueueJobID > 0 && c.admissionSink != nil {
		if err := c.admissionSink(runID, result.QueueJobID); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ queue identity projection for run %s: %v\n", runID, err)
		} else if activator, ok := c.engine.(interface{ ActivateQueuedJob(int64) error }); ok {
			if err := activator.ActivateQueuedJob(result.QueueJobID); err != nil {
				fmt.Fprintf(os.Stderr, "⚠ queue activation for run %s: %v\n", runID, err)
			}
		}
	}
	if lock != nil {
		_ = lock.Close()
	}
	c.mu.Lock()
	active := c.active[runID]
	c.mu.Unlock()
	if active != nil && requested(active.cancelRequested) {
		_, _ = c.engine.Cancel(pipeline.CancelConfig{RunID: runID, TargetDir: c.target})
	}
	if active != nil && active.cancel != nil {
		active.cancel()
	}
	if execErr != nil && !errors.Is(execErr, pipeline.ErrUserStopped) {
		// Фоновая ошибка не должна «проглатываться» в 202: отдаём её в
		// failure sink (SQLite projection web-сервера), иначе run остаётся
		// невидимым «призраком».
		if c.failureSink != nil {
			c.failureSink(runID, execErr.Error())
		} else {
			fmt.Fprintf(os.Stderr, "⚠ run %s завершился ошибкой: %v\n", runID, execErr)
		}
	}
	c.mu.Lock()
	delete(c.active, runID)
	c.mu.Unlock()
}

func requested(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

func cancellationRequestPath(targetDir, runID string) (string, error) {
	root, err := safeio.EnsureDir(targetDir, ".ai-team", "runs", runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "cancel.requested"), nil
}

func writeCancellationRequest(targetDir, runID string) error {
	path, err := cancellationRequestPath(targetDir, runID)
	if err != nil {
		return err
	}
	if err := safeio.WriteRegularFileNoFollow(path, []byte("safe-boundary cancellation requested\n"), 0o444); err != nil {
		data, readErr := safeio.ReadRegularFile(path, 1024)
		if readErr != nil || string(data) != "safe-boundary cancellation requested\n" {
			return err
		}
	}
	return nil
}

func cancellationRequested(targetDir, runID string, signal <-chan struct{}) bool {
	if requested(signal) {
		return true
	}
	path, err := cancellationRequestPath(targetDir, runID)
	if err != nil {
		return false
	}
	data, err := safeio.ReadRegularFile(path, 1024)
	return err == nil && string(data) == "safe-boundary cancellation requested\n"
}

// FailureSink получает фоновые ошибки run для durable-фиксации.
type FailureSink func(runID, cause string)

func WithFailureSink(sink FailureSink) Option {
	return func(controller *Controller) { controller.failureSink = sink }
}

// SetFailureSink доустанавливает sink после сборки (web-сервер создаёт
// SQLite store позже контроллера).
func (c *Controller) SetFailureSink(sink FailureSink) { c.failureSink = sink }

// SetAdmissionSink links the queue's durable job identity to its run projection.
func (c *Controller) SetAdmissionSink(sink func(string, int64) error) { c.admissionSink = sink }
