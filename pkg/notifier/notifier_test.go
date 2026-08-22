package notifier

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

type mockNotifier struct {
	calls []StageResult
	err   error
	delay time.Duration
}

func (m *mockNotifier) Notify(ctx context.Context, stage StageResult) error {
	m.calls = append(m.calls, stage)
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.err
}

func TestConsoleNotifier(t *testing.T) {
	n := NewConsoleNotifier()
	ctx := context.Background()
	stage := StageResult{
		Name:   "test-agent",
		Status: StatusPassed,
	}

	err := n.Notify(ctx, stage)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestConsoleNotifier_WithError(t *testing.T) {
	n := NewConsoleNotifier()
	ctx := context.Background()
	stage := StageResult{
		Name:   "test-agent",
		Status: StatusFailed,
		Err:    errors.New("test failure"),
	}

	err := n.Notify(ctx, stage)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestStageResult_Artifacts(t *testing.T) {
	stage := StageResult{
		Name:        "tester",
		Status:      StatusPassed,
		Duration:    5 * time.Second,
		StageIndex:  2,
		TotalStages: 6,
		Inputs: []runtime.Artifact{
			{Name: "design", Path: "/tmp/design.md"},
		},
		Outputs: []runtime.Artifact{
			{Name: "report", Path: "/tmp/report.md"},
		},
	}

	if len(stage.Inputs) != 1 {
		t.Errorf("expected 1 input, got %d", len(stage.Inputs))
	}
	if len(stage.Outputs) != 1 {
		t.Errorf("expected 1 output, got %d", len(stage.Outputs))
	}
	if stage.StageIndex != 2 {
		t.Errorf("expected stage index 2, got %d", stage.StageIndex)
	}
}

func TestStatusConstants(t *testing.T) {
	if StatusPassed != "passed" {
		t.Errorf("StatusPassed = %q", StatusPassed)
	}
	if StatusFailed != "failed" {
		t.Errorf("StatusFailed = %q", StatusFailed)
	}
	if StatusBlocked != "blocked" {
		t.Errorf("StatusBlocked = %q", StatusBlocked)
	}
}
