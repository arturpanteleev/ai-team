package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const (
	maxQuestionBytes  = 32 << 10
	maxAnswerBytes    = 16 << 10
	maxQuestionRounds = 3
)

type questionPayload struct {
	Kind     string `json:"kind"`
	Markdown string `json:"markdown"`
}

func stageQuestionsPath(artifactRoot, feature string) string {
	return filepath.Join(artifactRoot, "tasks", feature, "questions.md")
}

func questionsPayload(resultOutputs []runtime.Artifact) (json.RawMessage, bool, error) {
	for _, output := range resultOutputs {
		if output.Name != "questions" {
			continue
		}
		data, err := safeio.ReadRegularFile(output.Path, maxQuestionBytes)
		if err != nil {
			return nil, false, fmt.Errorf("read stage questions: %w", err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return nil, false, errors.New("stage questions artifact is empty")
		}
		encoded, err := json.Marshal(questionPayload{Kind: "questions", Markdown: string(data)})
		return encoded, true, err
	}
	return nil, false, nil
}

func questionAnswer(decisions []approval.Decision) string {
	for index := len(decisions) - 1; index >= 0; index-- {
		if decisions[index].Action == "answer_questions" {
			return strings.TrimSpace(decisions[index].Comment)
		}
	}
	return ""
}

// recoveredQuestionApproval restores a generic questions-enabled stage's
// durable answer after a crash between lifecycle persistence and input
// materialization. Analyst clarification answers use the stricter recovery
// path below because they have controller-owned worker inputs.
func recoveredQuestionApproval(store questionApprovalLookup, runID, nextStage string) (*approval.PendingApproval, error) {
	if nextStage == "" {
		return nil, nil
	}
	values, err := store.List(runID)
	if err != nil {
		return nil, err
	}
	for index := len(values) - 1; index >= 0; index-- {
		value := &values[index]
		if value.Status != approval.StatusResolved || value.Kind != approval.KindQuestions ||
			value.ResolvedAction != "answer_questions" || value.Targets[value.ResolvedAction] != nextStage ||
			value.Trigger != "graph_outcome:blocked" {
			continue
		}
		var payload questionPayload
		if json.Unmarshal(value.Payload, &payload) != nil || payload.Kind != "questions" || strings.TrimSpace(payload.Markdown) == "" {
			continue
		}
		if questionAnswer(value.Decisions) == "" {
			return nil, fmt.Errorf("resolved clarification approval %s has no durable answer", value.ID)
		}
		return value, nil
	}
	return nil, nil
}

type questionApprovalLookup interface {
	Load(string, string) (approval.PendingApproval, error)
	List(string) ([]approval.PendingApproval, error)
}

// ErrStaleQuestionApproval means a clarification answer's target analyst
// attempt has already completed. Resume callers must reconcile a verified
// graph transition before deciding whether this stale answer blocks recovery.
var ErrStaleQuestionApproval = errors.New("clarification approval is stale after its target attempt completed")

// ValidateQuestionAnswerApproval binds the selected approval to its durable
// source attempt and rejects reuse after a later analyst attempt completed.
// A source attempt invalidated by its own approved answer loopback remains a
// valid source; that is the expected state during crash recovery.
func ValidateQuestionAnswerApproval(value approval.PendingApproval, replayed evidence.ReplayedRun) error {
	if _, err := CanonicalQuestionAnswerContent(value); err != nil {
		return err
	}
	if replayed.RunID != value.RunID {
		return errors.New("clarification approval run does not match replay")
	}
	if !replayed.FinishedAt.IsZero() {
		return errors.New("clarification approval cannot be resumed after run completion")
	}
	sourceIndex := -1
	for index, attempt := range replayed.Attempts {
		if attempt.AttemptID == value.AttemptID && attempt.Stage == "analyst" {
			if sourceIndex >= 0 {
				return errors.New("clarification source attempt is ambiguous")
			}
			sourceIndex = index
		}
	}
	if sourceIndex < 0 {
		return fmt.Errorf("clarification approval %s has no matching analyst attempt in evidence", value.ID)
	}
	source := replayed.Attempts[sourceIndex]
	if source.FinishedAt.IsZero() ||
		(source.State.Outcome != workflow.OutcomeBlocked && !(source.Superseded && source.State.Outcome == workflow.OutcomeInvalidated)) {
		return errors.New("clarification approval source attempt was not a completed blocked analyst attempt")
	}
	activeTargets := 0
	for _, attempt := range replayed.Attempts[sourceIndex+1:] {
		if attempt.Stage != "analyst" {
			continue
		}
		if !attempt.FinishedAt.IsZero() && attempt.State.Execution == workflow.ExecutionSucceeded {
			return ErrStaleQuestionApproval
		}
		if attempt.FinishedAt.IsZero() {
			activeTargets++
		}
	}
	if activeTargets > 1 {
		return errors.New("clarification recovery has multiple incomplete analyst target attempts")
	}
	return nil
}

func recoveredApprovalEventsMatch(value approval.PendingApproval, replayed evidence.ReplayedRun) bool {
	var matchedDecision *evidence.ReplayedApprovalDecision
	for _, record := range replayed.ApprovalDecisions {
		if record.ID == value.ID {
			if record.SubjectHash != value.SubjectHash || record.AttemptID != value.AttemptID ||
				record.FromStage != value.FromStage || record.ToStage != value.ToStage ||
				record.Trigger != value.Trigger || record.Action != value.ResolvedAction {
				return false
			}
			copy := record
			matchedDecision = &copy
			break
		}
	}
	if matchedDecision == nil {
		return false
	}
	for _, transition := range replayed.Transitions {
		if transition.AttemptID == value.AttemptID && transition.From == value.FromStage &&
			transition.Outcome == strings.TrimPrefix(value.Trigger, "graph_outcome:") &&
			transition.EdgeTarget == value.ToStage && transition.Action == value.ResolvedAction &&
			transition.Target == value.Targets[value.ResolvedAction] && transition.Sequence > matchedDecision.Sequence {
			return true
		}
	}
	return false
}

// RecoveredQuestionApproval finds the latest durable clarification that still
// targets the current analyst stage. An absent or incomplete target attempt can
// be retried after a crash with the same immutable answer. A completed target
// attempt is stale; if lifecycle still points at analyst, fail closed instead
// of starting that stage again without its answer.
func RecoveredQuestionApproval(store questionApprovalLookup, runID, nextStage string, replayed evidence.ReplayedRun) (*approval.PendingApproval, error) {
	if nextStage != "analyst" {
		return nil, nil
	}
	values, err := store.List(runID)
	if err != nil {
		return nil, err
	}
	for index := len(values) - 1; index >= 0; index-- {
		value := &values[index]
		if value.RunID != runID || value.Status != approval.StatusResolved || value.Kind != approval.KindQuestions || value.FromStage != "analyst" ||
			value.ResolvedAction != "answer_questions" || value.Targets[value.ResolvedAction] != nextStage ||
			value.Trigger != "graph_outcome:blocked" {
			continue
		}
		if !recoveredApprovalEventsMatch(*value, replayed) {
			return nil, fmt.Errorf("clarification approval %s has no matching verified decision and transition", value.ID)
		}
		if err := ValidateQuestionAnswerApproval(*value, replayed); err != nil {
			return nil, err
		}
		return value, nil
	}
	return nil, nil
}

// ReconcileResumeNextStage advances a stale running lifecycle checkpoint only
// when the last verified graph transition was emitted by a completed attempt
// at that same stage and agrees with the pinned graph. Without that event the
// caller keeps the old checkpoint so stage-specific recovery can fail closed.
func ReconcileResumeNextStage(nextStage string, graph workflow.Graph, replayed evidence.ReplayedRun) (string, bool, error) {
	if nextStage == "" || len(replayed.Transitions) == 0 {
		return nextStage, false, nil
	}
	transition := replayed.Transitions[len(replayed.Transitions)-1]
	if transition.From != nextStage {
		return nextStage, false, nil
	}
	var source *evidence.ReplayedAttempt
	for index := range replayed.Attempts {
		if replayed.Attempts[index].AttemptID == transition.AttemptID {
			if source != nil {
				return nextStage, false, errors.New("resume transition attempt identity is ambiguous")
			}
			source = &replayed.Attempts[index]
		}
	}
	if source == nil || source.Stage != nextStage {
		return nextStage, false, errors.New("resume transition has no matching completed source attempt")
	}
	if source.Superseded {
		return nextStage, false, nil
	}
	if source.FinishedAt.IsZero() || transition.Outcome != string(source.State.Outcome) {
		return nextStage, false, errors.New("resume transition has no matching completed source attempt")
	}
	edge, found := graph.Edge(transition.From, workflow.Outcome(transition.Outcome))
	if !found || edge.To != transition.EdgeTarget {
		return nextStage, false, errors.New("resume transition does not match the pinned graph edge")
	}
	if edge.Approval == nil {
		if transition.Action != "" || transition.Target != edge.To {
			return nextStage, false, errors.New("resume transition target does not match its unapproved graph edge")
		}
	} else {
		if edge.Approval.Actions[transition.Action] != transition.Target || transition.Action == "" {
			return nextStage, false, errors.New("resume transition action does not match its approved graph edge")
		}
		decisionFound := false
		for _, decision := range replayed.ApprovalDecisions {
			if decision.AttemptID == transition.AttemptID && decision.FromStage == transition.From &&
				decision.ToStage == edge.To && decision.Trigger == "graph_outcome:"+transition.Outcome &&
				decision.Action == transition.Action && decision.Sequence < transition.Sequence {
				decisionFound = true
				break
			}
		}
		if !decisionFound {
			for _, reuse := range replayed.ApprovalReuses {
				if reuse.AttemptID != transition.AttemptID || reuse.PriorStatus != string(approval.StatusResolved) ||
					reuse.FromStage != transition.From || reuse.ToStage != edge.To ||
					reuse.Trigger != "graph_outcome:"+transition.Outcome || reuse.Sequence >= transition.Sequence {
					continue
				}
				for _, decision := range replayed.ApprovalDecisions {
					if decision.ID == reuse.ID && decision.SubjectHash == reuse.SubjectHash &&
						decision.FromStage == reuse.FromStage && decision.ToStage == reuse.ToStage &&
						decision.Trigger == reuse.Trigger && decision.Action == transition.Action &&
						decision.Sequence < reuse.Sequence {
						decisionFound = true
						break
					}
				}
				if decisionFound {
					break
				}
			}
		}
		if !decisionFound {
			return nextStage, false, errors.New("resume transition has no matching verified approval decision")
		}
	}
	if !workflow.IsTerminal(transition.Target) {
		if _, exists := graph.Node(transition.Target); !exists {
			return nextStage, false, errors.New("resume transition target is not present in the pinned graph")
		}
	}
	return transition.Target, true, nil
}

// recoveredGraphInputApproval restores the most recent resolved graph handoff
// whose selected target is still the current lifecycle stage. A crash can occur
// after the handoff decision and running lifecycle state are durable but before
// executeGraph starts that target; lifecycle state no longer contains the
// approval ID, so its immutable revision selection and feedback must be
// reconstructed from the approval store. A started but unfinished target
// attempt is abandoned and retried on resume, so it must keep the handoff
// inputs. A completed target attempt means execution advanced past this
// approval and makes it stale.
func recoveredGraphInputApproval(store ApprovalStore, runID, nextStage string, graph workflow.Graph, replayed evidence.ReplayedRun) (*approval.PendingApproval, error) {
	if nextStage == "" || workflow.IsTerminal(nextStage) {
		return nil, nil
	}
	values, err := store.List(runID)
	if err != nil {
		return nil, err
	}
	for index := len(values) - 1; index >= 0; index-- {
		value := &values[index]
		if value.RunID != runID || value.Status != approval.StatusResolved || value.Targets[value.ResolvedAction] != nextStage ||
			!strings.HasPrefix(value.Trigger, "graph_outcome:") || value.Kind == approval.KindQuestions || value.ResolvedAction == "answer_questions" {
			continue
		}
		if !recoveredApprovalEventsMatch(*value, replayed) {
			return nil, fmt.Errorf("graph approval %s has no matching verified decision and transition", value.ID)
		}
		outcome := workflow.Outcome(strings.TrimPrefix(value.Trigger, "graph_outcome:"))
		edge, found := graph.Edge(value.FromStage, outcome)
		if !found || edge.To != value.ToStage || edge.Approval == nil || edge.Approval.Actions[value.ResolvedAction] != nextStage {
			continue
		}
		sourceAttempt := -1
		for attemptIndex, attempt := range replayed.Attempts {
			if attempt.AttemptID == value.AttemptID && attempt.Stage == value.FromStage {
				sourceAttempt = attemptIndex
				break
			}
		}
		if sourceAttempt < 0 {
			return nil, fmt.Errorf("resolved graph approval %s has no matching source attempt in evidence", value.ID)
		}
		source := replayed.Attempts[sourceAttempt]
		if source.FinishedAt.IsZero() || source.Superseded || source.State.Outcome != outcome {
			return nil, fmt.Errorf("resolved graph approval %s disagrees with its completed source attempt", value.ID)
		}
		stale := false
		for _, attempt := range replayed.Attempts[sourceAttempt+1:] {
			if attempt.Stage == nextStage && !attempt.FinishedAt.IsZero() &&
				attempt.Status != "canceled" && attempt.State.Execution != workflow.ExecutionCanceled {
				stale = true
				break
			}
		}
		if stale {
			continue
		}
		return value, nil
	}
	return nil, nil
}

func countQuestionApprovals(store ApprovalStore, runID, stage string) (int, error) {
	values, err := store.List(runID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, value := range values {
		if value.Trigger == "graph_outcome:blocked" && value.FromStage == stage && value.Kind == approval.KindQuestions && value.Payload != nil {
			var payload questionPayload
			if json.Unmarshal(value.Payload, &payload) == nil && payload.Kind == "questions" {
				count++
			}
		}
	}
	return count, nil
}

// writeQuestionAnswerInput materializes the durable approval comment as an
// immutable run input. The approval remains the source of truth; on recovery
// the same input can be reconstructed from its persisted decision.
func writeQuestionAnswerInput(targetDir, runID, approvalID, answer string) (runtime.Artifact, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" || len(answer) > maxAnswerBytes {
		return runtime.Artifact{}, errors.New("answer must contain 1..16384 bytes")
	}
	path := filepath.Join(targetDir, ".ai-team", "runs", runID, "inputs", approvalID+"-answer.md")
	content := []byte("# Ответ на вопросы\n\n" + answer + "\n")
	if err := safeio.WriteRegularFileNoFollow(path, content, 0o444); err != nil {
		if info, statErr := os.Lstat(path); statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return runtime.Artifact{}, err
		}
		existing, readErr := safeio.ReadRegularFile(path, maxAnswerBytes+128)
		if readErr != nil || string(existing) != string(content) {
			return runtime.Artifact{}, fmt.Errorf("existing clarification input differs from durable decision: %w", err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return runtime.Artifact{}, err
	}
	return runtime.Artifact{Name: "clarification-answer", Path: path, Size: info.Size(), ModTime: info.ModTime()}, nil
}
