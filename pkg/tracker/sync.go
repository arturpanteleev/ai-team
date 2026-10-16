package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// Outcome is the honest result of a stage as it is reported to the tracker.
// It is an open string type: the values below are the ones ai-team emits
// today, and a new event kind (advisory failure, admin override from S06)
// adds a constant without changing the planner. Anything that is not exactly
// OutcomePassed is never presented as a pass.
type Outcome string

const (
	OutcomePassed     Outcome = "passed"
	OutcomeFailed     Outcome = "failed"
	OutcomeRejected   Outcome = "rejected"
	OutcomeBlocked    Outcome = "blocked"
	OutcomeCanceled   Outcome = "canceled"
	OutcomeSkipped    Outcome = "skipped"
	OutcomeWarning    Outcome = "warning"
	OutcomeOverridden Outcome = "overridden"
	OutcomeUnknown    Outcome = "unknown"
)

// Event types that produce tracker operations.
const (
	EventAttemptStarted  = "attempt_started"
	EventAttemptFinished = "attempt_finished"
	EventCheckOverridden = "check_overridden"
)

// Relevant reports whether an event type can produce tracker operations.
func Relevant(eventType string) bool {
	switch eventType {
	case EventAttemptStarted, EventAttemptFinished, EventCheckOverridden:
		return true
	}
	return false
}

// ClassifyOutcome derives the honest outcome of an event. For
// attempt_finished a pass requires status passed, outcome passed and a
// decision other than rejected; missing fields are never a pass.
func ClassifyOutcome(eventType string, data map[string]any) Outcome {
	field := func(key string) string {
		value, _ := data[key].(string)
		return strings.TrimSpace(value)
	}
	switch eventType {
	case EventCheckOverridden:
		return OutcomeOverridden
	case EventAttemptFinished:
		if field("decision") == string(workflow.DecisionRejected) {
			return OutcomeRejected
		}
		if field("status") == workflow.StatusPassed && field("outcome") == string(workflow.OutcomePassed) {
			return OutcomePassed
		}
		if outcome := field("outcome"); outcome != "" && outcome != string(workflow.OutcomePassed) {
			return Outcome(outcome)
		}
		if status := field("status"); status != "" && status != workflow.StatusPassed {
			return Outcome(status)
		}
	}
	return OutcomeUnknown
}

// OpKind is the kind of a tracker operation.
type OpKind string

const (
	OpTransition OpKind = "transition"
	OpComment    OpKind = "comment"
)

// Operation is one planned tracker call.
type Operation struct {
	Kind    OpKind
	Target  string  // OpTransition: status or transition name
	Comment Comment // OpComment
}

// Maps are the configured stage -> tracker status maps.
type Maps struct {
	OnEnter map[string]string
	OnPass  map[string]string
}

// EventKey is the idempotency key of an event: it is derived from the run and
// the event ID, so replaying or manually retrying the same event reuses it.
func EventKey(event TaskEvent) string {
	return fmt.Sprintf("%s#%d", event.RunID, event.Cursor)
}

// Plan turns one task event into tracker operations.
//
//   - attempt_started: a transition when on_enter maps the stage; nothing else.
//   - attempt_finished: a transition when on_pass maps the stage AND the
//     attempt really passed; a comment with the honest outcome always.
//   - check_overridden: a comment only. An override is never a pass.
//
// An empty result means the event is not a tracker concern.
func Plan(event TaskEvent, maps Maps, taskURL string) []Operation {
	var ops []Operation
	switch event.Type {
	case EventAttemptStarted:
		if target := strings.TrimSpace(maps.OnEnter[event.Stage]); target != "" {
			ops = append(ops, Operation{Kind: OpTransition, Target: target})
		}
	case EventAttemptFinished:
		outcome := ClassifyOutcome(event.Type, event.Data)
		if target := strings.TrimSpace(maps.OnPass[event.Stage]); target != "" && outcome == OutcomePassed {
			ops = append(ops, Operation{Kind: OpTransition, Target: target})
		}
		ops = append(ops, Operation{Kind: OpComment, Comment: stageComment(event, outcome, taskURL)})
	case EventCheckOverridden:
		ops = append(ops, Operation{Kind: OpComment, Comment: stageComment(event, OutcomeOverridden, taskURL)})
	}
	return ops
}

func stageComment(event TaskEvent, outcome Outcome, taskURL string) Comment {
	lines := []string{"ai-team task: " + taskURL}
	parts := []string{"Stage result: " + event.Stage, "outcome=" + string(outcome)}
	for _, key := range []string{"status", "decision", "execution", "check_status", "reason", "revision"} {
		if value, ok := event.Data[key]; ok && value != nil {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
				parts = append(parts, key+"="+oneLine(text))
			}
		}
	}
	lines = append(lines, strings.Join(parts, "; "))
	if outcome != OutcomePassed {
		lines = append(lines, "This stage did not pass; the outcome above is reported as is.")
	}
	if revision, ok := event.Data["revision"]; ok && revision != nil && fmt.Sprint(revision) != "1" {
		lines = append(lines, "This corrects the earlier result of the same attempt; the tracker status is not rolled back.")
	}
	if event.AttemptID != "" {
		lines = append(lines, "ai-team attempt: "+event.AttemptID)
	}
	return Comment{Key: EventKey(event), Lines: lines}
}

func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 300 {
		cut := 300
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "..."
	}
	return text
}

// Execute runs the operations in order against one issue. A failed operation
// does not stop the next one: a transition that cannot be made must not
// hide the comment that explains the stage outcome. All failures are joined.
func Execute(ctx context.Context, t Tracker, key string, ops []Operation) error {
	_, err := ExecuteReport(ctx, t, key, ops)
	return err
}

// ExecuteReport is Execute that also returns the target of the last transition
// the tracker actually accepted (empty if none was performed).
func ExecuteReport(ctx context.Context, t Tracker, key string, ops []Operation) (string, error) {
	var errs []error
	var transitioned string
	for _, op := range ops {
		switch op.Kind {
		case OpTransition:
			if _, err := t.Transition(ctx, key, op.Target); err != nil {
				errs = append(errs, fmt.Errorf("transition to %q: %w", op.Target, err))
			} else {
				transitioned = op.Target
			}
		case OpComment:
			if _, err := t.AddComment(ctx, key, op.Comment); err != nil {
				errs = append(errs, fmt.Errorf("comment: %w", err))
			}
		default:
			errs = append(errs, errors.New("unknown tracker operation"))
		}
	}
	return transitioned, errors.Join(errs...)
}
