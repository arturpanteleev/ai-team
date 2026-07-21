package workflow

import (
	"reflect"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/verdict"
)

func TestDeriveAttempt(t *testing.T) {
	tests := []struct {
		name  string
		facts AttemptFacts
		want  AttemptState
	}{
		{"plain pass", AttemptFacts{Execution: ExecutionSucceeded}, AttemptState{ExecutionSucceeded, DecisionNotApplicable, OutcomePassed}},
		{"approved", AttemptFacts{Execution: ExecutionSucceeded, Verdict: verdict.Approved}, AttemptState{ExecutionSucceeded, DecisionApproved, OutcomePassed}},
		{"rejected", AttemptFacts{Execution: ExecutionSucceeded, Verdict: verdict.ChangesRequested}, AttemptState{ExecutionSucceeded, DecisionRejected, OutcomeRejected}},
		{"blocked", AttemptFacts{Execution: ExecutionSucceeded, Blocked: true}, AttemptState{ExecutionSucceeded, DecisionBlocked, OutcomeBlocked}},
		{"failed", AttemptFacts{Execution: ExecutionInfraFailed}, AttemptState{ExecutionInfraFailed, DecisionNotApplicable, OutcomeFailed}},
		{"validation failed", AttemptFacts{Execution: ExecutionSucceeded, ValidationFailed: true}, AttemptState{ExecutionSucceeded, DecisionRejected, OutcomeFailed}},
		{"validation dominates blocked", AttemptFacts{Execution: ExecutionSucceeded, Blocked: true, ValidationFailed: true}, AttemptState{ExecutionSucceeded, DecisionRejected, OutcomeFailed}},
		{"skipped", AttemptFacts{Execution: ExecutionSucceeded, Skipped: true}, AttemptState{ExecutionSucceeded, DecisionNotApplicable, OutcomeSkipped}},
		{"canceled", AttemptFacts{Execution: ExecutionCanceled}, AttemptState{ExecutionCanceled, DecisionNotApplicable, OutcomeCanceled}},
		{"superseded", AttemptFacts{Execution: ExecutionSucceeded, Verdict: verdict.Rejected, Superseded: true}, AttemptState{ExecutionSucceeded, DecisionNotApplicable, OutcomeInvalidated}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first, err := DeriveAttempt(tc.facts)
			if err != nil {
				t.Fatal(err)
			}
			second, err := DeriveAttempt(tc.facts)
			if err != nil || !reflect.DeepEqual(first, second) || !reflect.DeepEqual(first, tc.want) {
				t.Fatalf("nondeterministic/incorrect state: first=%+v second=%+v want=%+v err=%v", first, second, tc.want, err)
			}
		})
	}
}

func TestDeriveRunIgnoresInvalidatedAttempts(t *testing.T) {
	attempts := []AttemptState{
		{Execution: ExecutionSucceeded, Decision: DecisionRejected, Outcome: OutcomeInvalidated},
		{Execution: ExecutionSucceeded, Decision: DecisionApproved, Outcome: OutcomePassed},
	}
	if got := DeriveRun(SignalCompleted, attempts); got != RunCompleted {
		t.Fatalf("исправленный loopback run должен быть completed, got %s", got)
	}
	if got := DeriveRun(SignalCanceled, attempts); got != RunCanceled {
		t.Fatalf("signal canceled должен доминировать, got %s", got)
	}
}
