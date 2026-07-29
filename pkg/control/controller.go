// Package control связывает долговечный RunEngine с process-local workers.
package control

import (
	"context"
	"errors"
	"fmt"
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
	engine    runEngine
	target    string
	approvals *approval.Store
	preflight PreflightChecker

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
	if report := c.Preflight(context.Background()); !report.Ready {
		return "", report.Error()
	}
	runID, err := evidence.NewRunID(time.Now().UTC())
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.active[runID] = &worker{cancel: cancel}
	c.mu.Unlock()
	go c.run(runID, ctx, func(ctx context.Context) {
		_, _ = c.engine.Start(ctx, pipeline.RunConfig{
			RunID: runID, Feature: feature, TaskDesc: task, TargetDir: c.target,
		})
	})
	return runID, nil
}

func (c *Controller) Preflight(ctx context.Context) preflight.Report {
	if c.preflight == nil {
		return preflight.Report{Ready: true, CheckedAt: time.Now().UTC()}
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
	go c.run(runID, ctx, func(ctx context.Context) {
		_, _ = c.engine.Resume(ctx, pipeline.ResumeConfig{RunID: runID, TargetDir: c.target})
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

func (c *Controller) run(runID string, ctx context.Context, execute func(context.Context)) {
	execute(ctx)
	c.mu.Lock()
	active := c.active[runID]
	c.mu.Unlock()
	if active != nil && active.cancelRequested {
		_, _ = c.engine.Cancel(pipeline.CancelConfig{RunID: runID, TargetDir: c.target})
	}
	c.mu.Lock()
	delete(c.active, runID)
	c.mu.Unlock()
}
