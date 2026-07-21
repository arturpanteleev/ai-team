package evidence

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// ReplayedRun is the deterministic lifecycle projection reconstructed from a
// verified event chain. Artifact contents remain in attempt manifests; the
// event carries and verifies each manifest identity.
type ReplayedRun struct {
	RunID           string              `json:"run_id"`
	StartedAt       time.Time           `json:"started_at"`
	FinishedAt      time.Time           `json:"finished_at,omitempty"`
	Status          workflow.RunOutcome `json:"status,omitempty"`
	Attempts        []ReplayedAttempt   `json:"attempts"`
	LastEventSHA256 string              `json:"last_event_sha256"`
}

type ReplayedAttempt struct {
	AttemptID      string                `json:"attempt_id"`
	Stage          string                `json:"stage"`
	StageIndex     int                   `json:"stage_index"`
	StartedAt      time.Time             `json:"started_at"`
	FinishedAt     time.Time             `json:"finished_at,omitempty"`
	Status         string                `json:"status,omitempty"`
	State          workflow.AttemptState `json:"state"`
	Verdict        string                `json:"verdict,omitempty"`
	Blocker        string                `json:"blocker,omitempty"`
	Error          string                `json:"error,omitempty"`
	ManifestSHA256 string                `json:"manifest_sha256,omitempty"`
	Superseded     bool                  `json:"superseded,omitempty"`
}

// ReplayEventLog verifies the hash chain and rebuilds the run lifecycle. It
// fails closed on impossible transitions or manifest identity mismatches.
func ReplayEventLog(path, runID string) (ReplayedRun, error) {
	events, err := VerifyEventLog(path, runID)
	if err != nil {
		return ReplayedRun{}, err
	}
	result := ReplayedRun{RunID: runID, Attempts: make([]ReplayedAttempt, 0)}
	byID := make(map[string]int)
	finishedCount := 0
	terminal := false
	for _, event := range events {
		if terminal {
			return ReplayedRun{}, fmt.Errorf("event %d occurs after run_finished", event.Sequence)
		}
		switch event.Type {
		case "run_started":
			if event.Sequence != 1 || !result.StartedAt.IsZero() {
				return ReplayedRun{}, fmt.Errorf("run_started must be the first unique event")
			}
			result.StartedAt = event.Timestamp
		case "attempt_started":
			if result.StartedAt.IsZero() || !safeEventIdentifier(event.AttemptID) || strings.TrimSpace(event.Stage) == "" || event.Timestamp.Before(result.StartedAt) {
				return ReplayedRun{}, fmt.Errorf("attempt_started %q has invalid identity", event.AttemptID)
			}
			if _, exists := byID[event.AttemptID]; exists {
				return ReplayedRun{}, fmt.Errorf("attempt_started %q is duplicated", event.AttemptID)
			}
			stageIndex, fieldErr := eventInt(event.Data, "stage_index")
			if fieldErr != nil {
				return ReplayedRun{}, fmt.Errorf("attempt_started %q stage_index: %w", event.AttemptID, fieldErr)
			}
			if stageIndex <= 0 {
				return ReplayedRun{}, fmt.Errorf("attempt_started %q stage_index must be positive", event.AttemptID)
			}
			byID[event.AttemptID] = len(result.Attempts)
			result.Attempts = append(result.Attempts, ReplayedAttempt{
				AttemptID: event.AttemptID, Stage: event.Stage, StageIndex: stageIndex,
				StartedAt: event.Timestamp, State: workflow.AttemptState{Execution: workflow.ExecutionRunning, Outcome: workflow.OutcomePending},
			})
		case "attempt_finished":
			index, exists := byID[event.AttemptID]
			if !exists || !result.Attempts[index].FinishedAt.IsZero() || result.Attempts[index].Stage != event.Stage {
				return ReplayedRun{}, fmt.Errorf("attempt_finished %q has no matching active attempt", event.AttemptID)
			}
			attempt := &result.Attempts[index]
			if event.Timestamp.Before(attempt.StartedAt) {
				return ReplayedRun{}, fmt.Errorf("attempt_finished %q predates attempt_started", event.AttemptID)
			}
			attempt.FinishedAt = event.Timestamp
			attempt.Status, err = eventString(event.Data, "status", true)
			if err != nil {
				return ReplayedRun{}, err
			}
			execution, executionErr := eventString(event.Data, "execution", true)
			decision, decisionErr := eventString(event.Data, "decision", true)
			outcome, outcomeErr := eventString(event.Data, "outcome", true)
			if executionErr != nil || decisionErr != nil || outcomeErr != nil {
				return ReplayedRun{}, fmt.Errorf("attempt_finished %q has incomplete state", event.AttemptID)
			}
			attempt.State = workflow.AttemptState{Execution: workflow.Execution(execution), Decision: workflow.Decision(decision), Outcome: workflow.Outcome(outcome)}
			if !validFinishedAttemptState(attempt.State) || attempt.State.LegacyStatus() != attempt.Status {
				return ReplayedRun{}, fmt.Errorf("attempt_finished %q status/outcome mismatch", event.AttemptID)
			}
			if attempt.Verdict, err = eventString(event.Data, "verdict", false); err != nil {
				return ReplayedRun{}, err
			}
			if attempt.Blocker, err = eventString(event.Data, "blocker", false); err != nil {
				return ReplayedRun{}, err
			}
			if attempt.Error, err = eventString(event.Data, "error", false); err != nil {
				return ReplayedRun{}, err
			}
			if attempt.ManifestSHA256, err = eventString(event.Data, "manifest_sha256", false); err != nil {
				return ReplayedRun{}, err
			}
			if attempt.ManifestSHA256 != "" {
				if !validSHA256(attempt.ManifestSHA256) {
					return ReplayedRun{}, fmt.Errorf("attempt_finished %q manifest digest is invalid", event.AttemptID)
				}
				manifestPath := filepath.Join(filepath.Dir(path), "attempts", event.AttemptID, "manifest.json")
				artifactType, _, digest, digestErr := ArtifactDigest(manifestPath)
				if digestErr != nil || artifactType != "file" || digest != attempt.ManifestSHA256 {
					return ReplayedRun{}, fmt.Errorf("attempt_finished %q manifest identity mismatch", event.AttemptID)
				}
			} else if attempt.Error == "" {
				return ReplayedRun{}, fmt.Errorf("attempt_finished %q has neither manifest evidence nor publication error", event.AttemptID)
			}
			finishedCount++
		case "attempts_invalidated":
			attemptIDs, fieldErr := eventStrings(event.Data, "attempt_ids")
			if fieldErr != nil {
				return ReplayedRun{}, fieldErr
			}
			for _, attemptID := range attemptIDs {
				index, exists := byID[attemptID]
				if !exists || result.Attempts[index].FinishedAt.IsZero() {
					return ReplayedRun{}, fmt.Errorf("cannot invalidate unknown or active attempt %q", attemptID)
				}
				if result.Attempts[index].Superseded {
					return ReplayedRun{}, fmt.Errorf("attempt %q is invalidated more than once", attemptID)
				}
				result.Attempts[index].Superseded = true
				result.Attempts[index].State = workflow.Invalidate(result.Attempts[index].State)
				result.Attempts[index].Status = result.Attempts[index].State.LegacyStatus()
			}
		case "run_finished":
			if result.StartedAt.IsZero() {
				return ReplayedRun{}, fmt.Errorf("run_finished appears before run_started")
			}
			status, fieldErr := eventString(event.Data, "status", true)
			if fieldErr != nil || !validRunOutcome(workflow.RunOutcome(status)) || event.Timestamp.Before(result.StartedAt) {
				return ReplayedRun{}, fmt.Errorf("run_finished status is invalid")
			}
			attemptCount, countErr := eventInt(event.Data, "stage_attempts")
			if countErr != nil || attemptCount != finishedCount {
				return ReplayedRun{}, fmt.Errorf("run_finished stage_attempts=%d, replayed=%d", attemptCount, finishedCount)
			}
			result.Status = workflow.RunOutcome(status)
			if result.Status == workflow.RunCompleted || result.Status == workflow.RunCompletedWithWarnings {
				states := make([]workflow.AttemptState, 0, len(result.Attempts))
				for _, attempt := range result.Attempts {
					states = append(states, attempt.State)
				}
				if derived := workflow.DeriveRun(workflow.SignalCompleted, states); derived != result.Status {
					return ReplayedRun{}, fmt.Errorf("run_finished status %s disagrees with replayed attempts %s", result.Status, derived)
				}
			}
			result.FinishedAt = event.Timestamp
			terminal = true
		}
	}
	if len(events) > 0 {
		result.LastEventSHA256 = events[len(events)-1].SHA256
	}
	if result.StartedAt.IsZero() {
		return ReplayedRun{}, fmt.Errorf("run_started is missing")
	}
	for _, attempt := range result.Attempts {
		if attempt.FinishedAt.IsZero() && terminal {
			return ReplayedRun{}, fmt.Errorf("terminal run contains active attempt %q", attempt.AttemptID)
		}
	}
	return result, nil
}

func eventString(data map[string]any, name string, required bool) (string, error) {
	value, exists := data[name]
	if !exists {
		if required {
			return "", fmt.Errorf("event field %s is required", name)
		}
		return "", nil
	}
	text, ok := value.(string)
	if !ok || required && text == "" {
		return "", fmt.Errorf("event field %s must be a string", name)
	}
	return text, nil
}

func eventInt(data map[string]any, name string) (int, error) {
	value, exists := data[name]
	if !exists {
		return 0, fmt.Errorf("event field %s is required", name)
	}
	number, ok := value.(float64)
	if !ok || number < 0 || number > float64(1<<31-1) || number != float64(int(number)) {
		return 0, fmt.Errorf("event field %s must be a non-negative integer", name)
	}
	return int(number), nil
}

func safeEventIdentifier(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\") && filepath.Base(value) == value
}

func validFinishedAttemptState(state workflow.AttemptState) bool {
	switch state.Execution {
	case workflow.ExecutionInfraFailed, workflow.ExecutionTimedOut:
		return state.Decision == workflow.DecisionNotApplicable && state.Outcome == workflow.OutcomeFailed
	case workflow.ExecutionCanceled:
		return state.Decision == workflow.DecisionNotApplicable && state.Outcome == workflow.OutcomeCanceled
	case workflow.ExecutionSucceeded:
		switch state.Decision {
		case workflow.DecisionNotApplicable:
			return state.Outcome == workflow.OutcomePassed || state.Outcome == workflow.OutcomeSkipped
		case workflow.DecisionApproved:
			return state.Outcome == workflow.OutcomePassed
		case workflow.DecisionRejected:
			return state.Outcome == workflow.OutcomeRejected || state.Outcome == workflow.OutcomeFailed
		case workflow.DecisionBlocked:
			return state.Outcome == workflow.OutcomeBlocked
		case workflow.DecisionWaived:
			return state.Outcome == workflow.OutcomeWarning
		}
	}
	return false
}

func eventStrings(data map[string]any, name string) ([]string, error) {
	value, exists := data[name]
	if !exists {
		return nil, fmt.Errorf("event field %s is required", name)
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("event field %s must be a string array", name)
	}
	result := make([]string, 0, len(raw))
	seen := make(map[string]bool)
	for _, item := range raw {
		text, ok := item.(string)
		if !ok || text == "" || seen[text] {
			return nil, fmt.Errorf("event field %s contains invalid attempt id", name)
		}
		seen[text] = true
		result = append(result, text)
	}
	sort.Strings(result)
	return result, nil
}

func validRunOutcome(outcome workflow.RunOutcome) bool {
	switch outcome {
	case workflow.RunCompleted, workflow.RunCompletedWithWarnings, workflow.RunFailed,
		workflow.RunBlocked, workflow.RunStopped, workflow.RunCanceled:
		return true
	default:
		return false
	}
}
