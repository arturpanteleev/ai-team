package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// human_gate.go — the human_approval gate in the common gate history.
//
// The human decision is the transition approval of the stage (authorizeTransition):
// it carries the roles, the quorum, the allowed actions and the exact subject
// hash. What the gate contract adds is the uniform record: every wait and every
// decision of the person is a durable attempt of the human_approval gate
// (gate_attempt_waiting, gate_attempt_finished) bound to the hash of the result
// version, next to the attempts of the code and LLM gates of the same chain.
//
//   - approve (the primary action of the edge) finishes the attempt with
//     verdict pass;
//   - reject and every return_to_* action finish it with verdict fail: the
//     result was not accepted, and the reason carries the action, the role,
//     the author and the comment, so a refusal and a return are events and
//     not silent state changes;
//   - the request is raised only after the automatic chain ended without a
//     blocking outcome (runStageChecks runs first), so a person is never asked
//     about a result the machines have not finished checking.

// humanDecisionCommentLimit bounds the comment copied into the attempt reason
// (the approval record keeps the full text).
const humanDecisionCommentLimit = 300

// recordHumanGate writes the human_approval attempt that corresponds to the
// outcome of authorizeTransition: waiting while the approval is pending,
// finished once the person decided. It is a no-op for a stage without the
// gate. The records are idempotent on the result version: a redelivered
// resume or a re-entered transition does not add a second decision.
func (rs *runState) recordHumanGate(stage config.TemplateStage, result notifier.StageResult, edgeTo, trigger, action string, decisionErr error) error {
	ref, ok := stage.HumanGateRef()
	if !ok {
		return nil
	}
	var waiting *ApprovalRequiredError
	switch {
	case errors.As(decisionErr, &waiting):
	case decisionErr != nil:
		return nil // not a decision and not a wait: nothing to record
	case action == "":
		return nil
	}
	def, err := rs.p.cfg.GateLibrary().Lookup(ref.Gate, ref.Version)
	if err != nil {
		return fmt.Errorf("этап %s: %w", stage.ID, err)
	}
	sha, err := rs.resultVersion(result)
	if err != nil {
		return fmt.Errorf("версия результата этапа %s: %w", stage.ID, err)
	}
	key := rs.waveKey(stage, result, sha)
	journal := rs.gateJournal()
	recorded, err := journal.Load(key)
	if err != nil {
		return fmt.Errorf("чтение попыток гейта %s: %w", ref.InstanceName(), err)
	}
	var mine []workflow.GateAttempt
	for _, attempt := range recorded {
		if attempt.GateName == ref.InstanceName() {
			mine = append(mine, attempt)
		}
	}
	var latest *workflow.GateAttempt
	if len(mine) > 0 {
		latest = &mine[len(mine)-1]
	}
	group := humanGateGroup(stage, ref)
	now := time.Now().UTC()

	if waiting != nil {
		if latest != nil && !latest.Execution.Terminal() {
			return nil // already waiting on this very result version
		}
		if latest != nil && latest.Execution == workflow.GateExecutionSucceeded && latest.Verdict == workflow.GateVerdictPass {
			return nil // decided earlier; the transition is only being re-entered
		}
		attempt := gatesched.NewAttempt(key, ref, def, group, len(mine)+1, now)
		if err := journal.Record(gatesched.Record{Type: gatesched.RecordStarted, Key: key, Attempt: attempt}); err != nil {
			return err
		}
		attempt.Execution = workflow.GateExecutionPending
		attempt.Reason = fmt.Sprintf("ожидает решения человека: approval %s, subject %s", waiting.ApprovalID, short(waiting.SubjectHash))
		return journal.Record(gatesched.Record{Type: gatesched.RecordWaiting, Key: key, Attempt: attempt})
	}

	// Decided: close the open attempt or, when the approval was already
	// resolved before the gate was reached (reuse of an exact earlier decision
	// or an automatic approval), record a complete attempt.
	verdict, reason := rs.humanGateVerdict(stage, result, edgeTo, trigger, action)
	var attempt workflow.GateAttempt
	switch {
	case latest != nil && !latest.Execution.Terminal():
		attempt = *latest
	case latest != nil && latest.Execution == workflow.GateExecutionSucceeded && latest.Verdict == verdict:
		return nil
	default:
		attempt = gatesched.NewAttempt(key, ref, def, group, len(mine)+1, now)
		if err := journal.Record(gatesched.Record{Type: gatesched.RecordStarted, Key: key, Attempt: attempt}); err != nil {
			return err
		}
	}
	attempt.Execution = workflow.GateExecutionSucceeded
	attempt.Verdict = verdict
	attempt.Reason = reason
	attempt.FinishedAt = now
	return journal.Record(gatesched.Record{Type: gatesched.RecordFinished, Key: key, Attempt: attempt})
}

// humanGateGroup is the index of the group of the chain that holds the gate.
func humanGateGroup(stage config.TemplateStage, ref workflow.GateRef) int {
	for index, group := range stage.EffectiveGateGroups() {
		for _, candidate := range group {
			if candidate.Key() == ref.Key() && candidate.InstanceName() == ref.InstanceName() {
				return index
			}
		}
	}
	return 0
}

// humanGateVerdict maps the chosen action to the gate verdict and describes
// the decision from the resolved approval.
func (rs *runState) humanGateVerdict(stage config.TemplateStage, result notifier.StageResult, edgeTo, trigger, action string) (workflow.GateVerdict, string) {
	verdict := workflow.GateVerdictFail
	if value := rs.resolvedTransitionApproval(stage.ID, result.AttemptID, trigger); value != nil && value.Targets[action] == edgeTo {
		verdict = workflow.GateVerdictPass
	} else if value == nil && action == "approve" {
		verdict = workflow.GateVerdictPass
	}
	reason := "действие " + action
	if value := rs.resolvedTransitionApproval(stage.ID, result.AttemptID, trigger); value != nil {
		reason += fmt.Sprintf(", approval %s, subject %s", value.ID, short(value.SubjectHash))
		for _, decision := range value.Decisions {
			if decision.Action != action {
				continue
			}
			reason += fmt.Sprintf(", роль %s, автор %s", decision.ActorRole, decision.ActorID)
			if comment := strings.TrimSpace(decision.Comment); comment != "" {
				if len(comment) > humanDecisionCommentLimit {
					comment = strings.ToValidUTF8(comment[:humanDecisionCommentLimit], "") + "…"
				}
				reason += ": " + comment
			}
		}
	}
	return verdict, reason
}

// recordHumanGateOutcome records the human_approval attempt for the forward
// transition of a stage whose chain has the gate. Other approvals (questions,
// returns after a failed outcome) are not the gate.
func (rs *runState) recordHumanGateOutcome(stageID string, edge workflow.Edge, kind approval.Kind, result notifier.StageResult, action string, decisionErr error) error {
	if edge.Outcome != workflow.OutcomePassed || (kind != "" && kind != approval.KindApprove) {
		return nil
	}
	stage, ok := rs.p.templateStage(stageID)
	if !ok {
		return nil
	}
	return rs.recordHumanGate(stage, result, edge.To, "graph_outcome:"+string(edge.Outcome), action, decisionErr)
}

// rejectKeepsStage reports whether the resolved approval is a rejection that
// leaves the task at its stage (human_approval on_reject: stay) instead of
// ending the run: the rework starts from a new version, which repeats the
// whole chain.
func rejectKeepsStage(value *approval.PendingApproval) bool {
	return value != nil && value.Kind == approval.KindApprove && value.ResolvedAction == "reject" &&
		approval.IsFeedbackRoute(*value, "reject")
}

// recordResumedHumanDecision records the human_approval attempt for a decision
// the person made while the process was not running (Web, REST): the run
// resumes straight into the target of the decision, so the in-process
// transition code never sees it.
func (rs *runState) recordResumedHumanDecision() error {
	value := rs.resumedApproval
	if value == nil || value.Kind != approval.KindApprove || value.Status != approval.StatusResolved ||
		value.Trigger != "graph_outcome:"+string(workflow.OutcomePassed) || value.ResolvedAction == "" {
		return nil
	}
	stage, ok := rs.p.templateStage(value.FromStage)
	if !ok {
		return nil
	}
	if _, has := stage.HumanGateRef(); !has {
		return nil
	}
	for i := len(rs.results) - 1; i >= 0; i-- {
		result := rs.results[i]
		if result.AttemptID == value.AttemptID && result.Name == value.FromStage {
			return rs.recordHumanGate(stage, result, value.ToStage, value.Trigger, value.ResolvedAction, nil)
		}
	}
	return nil
}

// resolvedTransitionApproval returns the latest resolved transition approval
// of the stage attempt.
func (rs *runState) resolvedTransitionApproval(stageID, attemptID, trigger string) *approval.PendingApproval {
	list, err := rs.approvalStore.List(rs.runID)
	if err != nil {
		return nil
	}
	var found *approval.PendingApproval
	for index := range list {
		value := &list[index]
		if value.FromStage != stageID || value.AttemptID != attemptID || value.Trigger != trigger || value.Status != approval.StatusResolved {
			continue
		}
		if found == nil || !value.ResolvedAt.Before(found.ResolvedAt) {
			found = value
		}
	}
	return found
}

// discountStayRejections removes from the restored visit counters the attempts
// that ended with a resolved human_approval on_reject: stay refusal of their
// own stage. It mirrors the live decrement in the graph loop, so the count
// after a resume does not depend on whether the decision was made in the
// process or through Web/REST while the run was stopped.
func (rs *runState) discountStayRejections() error {
	list, err := rs.approvalStore.List(rs.runID)
	if err != nil {
		return err
	}
	counted := make(map[string]bool, len(rs.results))
	for _, result := range rs.results {
		if result.AttemptID != "" {
			counted[result.AttemptID+"\x00"+result.Name] = true
		}
	}
	for _, value := range list {
		if value.Status != approval.StatusResolved || value.Trigger != "graph_outcome:"+string(workflow.OutcomePassed) ||
			!rejectKeepsStage(&value) || !counted[value.AttemptID+"\x00"+value.FromStage] {
			continue
		}
		if rs.visits[value.FromStage] > 0 {
			rs.visits[value.FromStage]--
		}
	}
	return nil
}
