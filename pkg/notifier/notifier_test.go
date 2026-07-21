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

func TestNotifierChain_Single(t *testing.T) {
	m := &mockNotifier{}
	chain := NewNotifierChain(m)

	ctx := context.Background()
	stage := StageResult{Name: "a", Status: StatusPassed}

	err := chain.Notify(ctx, stage)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if len(m.calls) != 1 {
		t.Errorf("expected 1 call, got %d", len(m.calls))
	}
	if m.calls[0].Name != "a" {
		t.Errorf("expected name 'a', got %q", m.calls[0].Name)
	}
}

func TestNotifierChain_Multiple(t *testing.T) {
	m1 := &mockNotifier{}
	m2 := &mockNotifier{}
	chain := NewNotifierChain(m1, m2)

	ctx := context.Background()
	stage := StageResult{Name: "test", Status: StatusPassed}

	err := chain.Notify(ctx, stage)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if len(m1.calls) != 1 {
		t.Errorf("expected 1 call on m1, got %d", len(m1.calls))
	}
	if len(m2.calls) != 1 {
		t.Errorf("expected 1 call on m2, got %d", len(m2.calls))
	}
}

func TestNotifierChain_ErrorPropagation(t *testing.T) {
	m1 := &mockNotifier{err: errors.New("first error")}
	m2 := &mockNotifier{}
	chain := NewNotifierChain(m1, m2)

	ctx := context.Background()
	stage := StageResult{Name: "test", Status: StatusFailed}

	err := chain.Notify(ctx, stage)
	if err == nil {
		t.Error("expected error from chain")
	}
	if err.Error() != "first error" {
		t.Errorf("expected 'first error', got %q", err.Error())
	}
	// m2 should still be called even if m1 fails
	if len(m2.calls) != 1 {
		t.Error("expected m2 to be called despite m1 error")
	}
}

func TestNotifierChain_LastErrorWins(t *testing.T) {
	m1 := &mockNotifier{err: errors.New("first")}
	m2 := &mockNotifier{err: errors.New("second")}
	chain := NewNotifierChain(m1, m2)

	ctx := context.Background()
	stage := StageResult{Name: "test"}

	err := chain.Notify(ctx, stage)
	if err == nil {
		t.Error("expected error")
	}
	// Last error should win
	if err.Error() != "second" {
		t.Errorf("expected 'second', got %q", err.Error())
	}
}

func TestNotifierChain_Empty(t *testing.T) {
	chain := NewNotifierChain()
	ctx := context.Background()
	stage := StageResult{Name: "test"}

	err := chain.Notify(ctx, stage)
	if err != nil {
		t.Errorf("expected no error from empty chain, got %v", err)
	}
}

func TestNotifierChain_ContextCancellation(t *testing.T) {
	m := &mockNotifier{delay: 5 * time.Second}
	chain := NewNotifierChain(m)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	stage := StageResult{Name: "test"}

	err := chain.Notify(ctx, stage)
	if err == nil {
		t.Error("expected error for cancelled context")
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
