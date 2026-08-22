// Package control связывает долговечный RunEngine с process-local workers.
package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

var ErrActive = errors.New("run уже исполняется")

type worker struct {
	cancel          context.CancelFunc
	cancelRequested bool
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

type Controller struct {
	engine      runEngine
	target      string
	approvals   *approval.Store
	preflight   PreflightChecker
	failureSink FailureSink

	mu     sync.Mutex
	active map[string]*worker
}

func New(engine runEngine, target string, options ...Option) (*Controller, error) {
	if engine == nil {
		return nil, errors.New("run engine обязателен")
	}
	store, err := approval.NewStore(target)
	if err != nil {
		return nil, err
	}
	controller := &Controller{
		engine: engine, target: target, approvals: store,
		active: make(map[string]*worker),
	}
	for _, option := range options {
		option(controller)
	}
	return controller, nil
}

func (c *Controller) Start(feature, task string) (string, error) {
	if !workflow.ValidFeature(feature) || strings.TrimSpace(task) == "" {
		return "", errors.New("feature и непустой task обязательны")
	}
	if c.preflight != nil {
		if report := c.Preflight(context.Background()); !report.Ready {
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
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.active[runID] = &worker{cancel: cancel}
	c.mu.Unlock()
	go c.run(runID, ctx, lock, func(ctx context.Context) (pipeline.RunResult, error) {
		return c.engine.Start(ctx, pipeline.RunConfig{
			RunID: runID, Feature: feature, TaskDesc: task, TargetDir: c.target,
			WorkspaceLock: lock,
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
	c.mu.Lock()
	if _, exists := c.active[runID]; exists {
		c.mu.Unlock()
		cancel()
		return ErrActive
	}
	c.active[runID] = &worker{cancel: cancel}
	c.mu.Unlock()
	go c.run(runID, ctx, nil, func(ctx context.Context) (pipeline.RunResult, error) {
		return c.engine.Resume(ctx, pipeline.ResumeConfig{RunID: runID, TargetDir: c.target})
	})
	return nil
}

func (c *Controller) Cancel(runID string) error {
	c.mu.Lock()
	if active := c.active[runID]; active != nil {
		active.cancelRequested = true
		active.cancel()
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

func (c *Controller) run(runID string, ctx context.Context, lock *evidence.WorkspaceLock, execute func(context.Context) (pipeline.RunResult, error)) {
	_, execErr := execute(ctx)
	if lock != nil {
		_ = lock.Close()
	}
	c.mu.Lock()
	active := c.active[runID]
	c.mu.Unlock()
	if active != nil && active.cancelRequested {
		_, _ = c.engine.Cancel(pipeline.CancelConfig{RunID: runID, TargetDir: c.target})
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

// FailureSink получает фоновые ошибки run для durable-фиксации.
type FailureSink func(runID, cause string)

func WithFailureSink(sink FailureSink) Option {
	return func(controller *Controller) { controller.failureSink = sink }
}

// SetFailureSink доустанавливает sink после сборки (web-сервер создаёт
// SQLite store позже контроллера).
func (c *Controller) SetFailureSink(sink FailureSink) { c.failureSink = sink }
