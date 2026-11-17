package gatesched

import (
	"context"
	"fmt"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// entry.go — the entry-gate guard (ARC-02). An entry gate checks the resolved
// stage inputs before StageExecutor.Execute and returns a verdict, never a
// changed artifact. The guard below is the point where a failed required gate
// prevents the executor from starting. The pipeline adapter owns building the
// Wave from the resolved inputs; this package owns ordering, journaling and
// the allow/deny decision.

// ExecuteFunc is the stage executor callback. It is invoked at most once, and
// only when every required entry gate passed.
type ExecuteFunc func(ctx context.Context) error

// EntryGateResult is the decision of the entry chain and the executor call.
type EntryGateResult struct {
	// Allowed reports whether the executor was called. It is false for a
	// failed, errored or waiting required gate, and for a wave error.
	Allowed bool `json:"allowed"`
	// Status is the aggregate status of the gate wave (empty when there were
	// no entry gates at all).
	Status Status `json:"status,omitempty"`
	// Blocking are the effective attempts that prevented execution.
	Blocking []workflow.GateAttempt `json:"blocking,omitempty"`
	// NotStarted lists gates that did not start because an earlier group
	// blocked.
	NotStarted []string `json:"not_started,omitempty"`
	// Reasons explain the decision in words.
	Reasons []string `json:"reasons,omitempty"`
}

// BlockReason is a compact explanation for a blocked executor.
func (r EntryGateResult) BlockReason() string {
	return strings.Join(r.Reasons, "; ")
}

// GuardExecute runs the entry chain and calls execute only when the chain
// allows it. A required gate that fails, errors or waits blocks the executor:
// it is never called and the returned result carries the blocking attempts.
// A wave that cannot be carried out reliably (journal failure, changed
// inputs) is fail-closed as well.
//
// The caller supplies Wave.Key with the hash of the resolved inputs and the
// pinned definitions of the entry refs. Definitions and refs are validated at
// configuration time; this function does not edit artifacts.
func GuardExecute(ctx context.Context, s *Scheduler, wave Wave, execute ExecuteFunc) (EntryGateResult, error) {
	wave.Phase = workflow.GatePhaseEntry
	if len(wave.Groups) == 0 {
		// No entry gates: preserve the historical behavior and run the executor.
		result := EntryGateResult{Allowed: true}
		if execute != nil {
			if err := execute(ctx); err != nil {
				return result, err
			}
		}
		return result, nil
	}
	res, err := s.Run(ctx, wave)
	if err != nil {
		result := EntryGateResult{
			Status:  StatusError,
			Reasons: []string{fmt.Sprintf("входные гейты не завершились надёжно: %v", err)},
		}
		return result, err
	}
	result := EntryGateResult{
		Status:     res.Status,
		Blocking:   res.Blocking,
		NotStarted: res.NotStarted,
		Allowed:    res.Status == StatusPassed,
	}
	if !result.Allowed {
		for _, attempt := range res.Blocking {
			result.Reasons = append(result.Reasons, fmt.Sprintf("%s: %s/%s (%s)",
				attempt.GateName, attempt.Execution, attempt.Verdict, strings.TrimSpace(attempt.Reason)))
		}
		if len(res.NotStarted) > 0 {
			result.Reasons = append(result.Reasons, "не запускались: "+strings.Join(res.NotStarted, ", "))
		}
		return result, nil
	}
	if execute != nil {
		if err := execute(ctx); err != nil {
			return result, err
		}
	}
	return result, nil
}
