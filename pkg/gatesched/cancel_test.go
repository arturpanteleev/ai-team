package gatesched

import (
	"context"
	"errors"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestCanceledContextMidGroupReturnsContextError(t *testing.T) {
	groups := workflow.GateGroups{{ref("a"), ref("b")}, {ref("c")}}
	definitions := defs(def("a", workflow.GateKindBuiltin), def("b", workflow.GateKindBuiltin), def("c", workflow.GateKindBuiltin))
	journal := &MemoryJournal{}
	calls := newCounters()
	ctx, cancel := context.WithCancel(context.Background())
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		calls.hit(in.Ref.Gate)
		if in.Ref.Gate == "a" {
			cancel()
		}
		if ctx.Err() != nil {
			return Outcome{Execution: workflow.GateExecutionCanceled, Reason: "отмена"}
		}
		return Pass("")
	})
	// Concurrency 1 makes b start only after a cancelled the context.
	s := newScheduler(t, journal, Options{MaxParallel: 1}, runner)
	result, err := s.Run(ctx, Wave{Key: key(hashV1), Groups: groups, Definitions: definitions})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (status %s)", err, result.Status)
	}
	if calls.count("b") != 0 || calls.count("c") != 0 {
		t.Fatalf("no gate may start after cancellation: %v", calls.order)
	}
	for _, attempt := range result.Attempts {
		if attempt.Execution == workflow.GateExecutionError || attempt.Execution == workflow.GateExecutionRunning {
			t.Fatalf("interrupted gate must be canceled, got %+v", attempt)
		}
	}
	if len(result.NotStarted) != 2 {
		t.Fatalf("b and c must be reported as not started: %+v", result.NotStarted)
	}
}

func TestAttemptIDDiffersBetweenScopes(t *testing.T) {
	k1 := key(hashV1)
	k2 := key(hashV1)
	k2.Scope = "attempt-2"
	if AttemptID(k1, "a", 1) == AttemptID(k2, "a", 1) {
		t.Fatal("attempt ID must depend on the scope")
	}
	again := key(hashV1)
	if AttemptID(k1, "a", 1) != AttemptID(again, "a", 1) {
		t.Fatal("attempt ID must be deterministic")
	}
}
