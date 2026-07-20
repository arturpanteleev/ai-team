// Package workflow contains the deterministic workflow domain state model.
package workflow

import (
	"fmt"

	"github.com/arturpanteleev/ai-team/pkg/verdict"
)

type Execution string
type Decision string
type Outcome string
type RunOutcome string
type RunSignal string

const (
	ExecutionPending     Execution = "pending"
	ExecutionRunning     Execution = "running"
	ExecutionSucceeded   Execution = "succeeded"
	ExecutionInfraFailed Execution = "infra_failed"
	ExecutionTimedOut    Execution = "timed_out"
	ExecutionCanceled    Execution = "canceled"

	DecisionNotApplicable Decision = "not_applicable"
	DecisionApproved      Decision = "approved"
	DecisionRejected      Decision = "rejected"
	DecisionBlocked       Decision = "blocked"
	DecisionWaived        Decision = "waived"

	OutcomePending     Outcome = "pending"
	OutcomePassed      Outcome = "passed"
	OutcomeFailed      Outcome = "failed"
	OutcomeRejected    Outcome = "rejected"
	OutcomeBlocked     Outcome = "blocked"
	OutcomeCanceled    Outcome = "canceled"
	OutcomeSkipped     Outcome = "skipped"
	OutcomeWarning     Outcome = "warning"
	OutcomeInvalidated Outcome = "invalidated"

	RunCompleted             RunOutcome = "completed"
	RunCompletedWithWarnings RunOutcome = "completed_with_warnings"
	RunFailed                RunOutcome = "failed"
	RunBlocked               RunOutcome = "blocked"
	RunStopped               RunOutcome = "stopped"
	RunCanceled              RunOutcome = "canceled"

	SignalCompleted RunSignal = "completed"
	SignalFailed    RunSignal = "failed"
	SignalBlocked   RunSignal = "blocked"
	SignalStopped   RunSignal = "stopped"
	SignalCanceled  RunSignal = "canceled"
)

type AttemptState struct {
	Execution Execution `json:"execution"`
	Decision  Decision  `json:"decision"`
	Outcome   Outcome   `json:"outcome"`
}

type AttemptFacts struct {
	Execution        Execution
	Verdict          verdict.Verdict
	Blocked          bool
	Waived           bool
	ValidationFailed bool
	Skipped          bool
	Superseded       bool
}

func DeriveAttempt(facts AttemptFacts) (AttemptState, error) {
	state := AttemptState{Execution: facts.Execution, Decision: DecisionNotApplicable, Outcome: OutcomePending}
	if facts.Superseded {
		state.Outcome = OutcomeInvalidated
		return state, nil
	}
	switch facts.Execution {
	case ExecutionPending, ExecutionRunning:
		return state, nil
	case ExecutionInfraFailed, ExecutionTimedOut:
		state.Outcome = OutcomeFailed
		return state, nil
	case ExecutionCanceled:
		state.Outcome = OutcomeCanceled
		return state, nil
	case ExecutionSucceeded:
	default:
		return AttemptState{}, fmt.Errorf("unknown execution state %q", facts.Execution)
	}
	if facts.ValidationFailed {
		state.Decision = DecisionRejected
		state.Outcome = OutcomeFailed
		return state, nil
	}
	if facts.Blocked {
		state.Decision = DecisionBlocked
		state.Outcome = OutcomeBlocked
		return state, nil
	}
	if facts.Skipped {
		state.Outcome = OutcomeSkipped
		return state, nil
	}
	if facts.Waived {
		state.Decision = DecisionWaived
		state.Outcome = OutcomeWarning
		return state, nil
	}
	switch {
	case facts.Verdict.IsNegative():
		state.Decision = DecisionRejected
		state.Outcome = OutcomeRejected
	case facts.Verdict == verdict.Approved || facts.Verdict == verdict.Pass:
		state.Decision = DecisionApproved
		state.Outcome = OutcomePassed
	case facts.Verdict == verdict.None:
		state.Outcome = OutcomePassed
	default:
		return AttemptState{}, fmt.Errorf("unsupported verdict %q", facts.Verdict)
	}
	return state, nil
}

func (s AttemptState) LegacyStatus() string { return string(s.Outcome) }

func Invalidate(state AttemptState) AttemptState {
	state.Outcome = OutcomeInvalidated
	return state
}

func DeriveRun(signal RunSignal, attempts []AttemptState) RunOutcome {
	switch signal {
	case SignalBlocked:
		return RunBlocked
	case SignalStopped:
		return RunStopped
	case SignalCanceled:
		return RunCanceled
	case SignalFailed:
		return RunFailed
	case SignalCompleted:
	default:
		return RunFailed
	}
	for _, attempt := range attempts {
		switch attempt.Outcome {
		case OutcomeInvalidated, OutcomeSkipped:
			continue
		case OutcomeRejected, OutcomeWarning:
			return RunCompletedWithWarnings
		case OutcomeFailed, OutcomeBlocked, OutcomeCanceled, OutcomePending:
			return RunFailed
		}
	}
	return RunCompleted
}
