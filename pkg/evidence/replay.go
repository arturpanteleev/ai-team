package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// StageSkipProtocolVersion identifies current explicit skip events that bind
// their reason in both attempt_finished and stage_skipped. Version zero is the
// historical B-34 shape, whose durable reason exists only in stage_skipped.
const StageSkipProtocolVersion = 1

// ReplayedRun is the deterministic lifecycle projection reconstructed from a
// verified event chain. Artifact contents remain in attempt manifests; the
// event carries and verifies each manifest identity.
type ReplayedRun struct {
	RunID             string                     `json:"run_id"`
	StartedAt         time.Time                  `json:"started_at"`
	FinishedAt        time.Time                  `json:"finished_at,omitempty"`
	Status            workflow.RunOutcome        `json:"status,omitempty"`
	Attempts          []ReplayedAttempt          `json:"attempts"`
	Transitions       []ReplayedTransition       `json:"transitions,omitempty"`
	StageSkips        []ReplayedStageSkip        `json:"stage_skips,omitempty"`
	ApprovalDecisions []ReplayedApprovalDecision `json:"approval_decisions,omitempty"`
	ApprovalReuses    []ReplayedApprovalReuse    `json:"approval_reuses,omitempty"`
	LastEventSHA256   string                     `json:"last_event_sha256"`
}

// ReplayedTransition retains the identity-bearing fields from verified
// transition_selected events for recovery decisions that must reconcile a
// lifecycle checkpoint with its event journal.
type ReplayedTransition struct {
	Sequence   uint64 `json:"sequence"`
	AttemptID  string `json:"attempt_id"`
	From       string `json:"from"`
	Outcome    string `json:"outcome"`
	EdgeTarget string `json:"edge_target"`
	Action     string `json:"action,omitempty"`
	Target     string `json:"target"`
}

type ReplayedStageSkip struct {
	Sequence  uint64 `json:"sequence"`
	AttemptID string `json:"attempt_id"`
	Stage     string `json:"stage"`
	Reason    string `json:"reason"`
}

// ReplayedApprovalDecision retains the event identity needed to bind a
// recovered approval-store record to its verified approval_decided event.
type ReplayedApprovalDecision struct {
	Sequence          uint64 `json:"sequence"`
	AttemptID         string `json:"attempt_id"`
	ID                string `json:"approval_id"`
	Kind              string `json:"kind,omitempty"`
	SubjectHash       string `json:"subject_hash"`
	FromStage         string `json:"from_stage"`
	ToStage           string `json:"to_stage"`
	Trigger           string `json:"trigger"`
	Action            string `json:"action"`
	DecisionSetSHA256 string `json:"decision_set_sha256,omitempty"`
}

// DecisionSetDigest returns a stable digest for the full decisions array in
// an approval record or event. It binds submitted bytes and actor metadata in
// addition to the action and subject hash.
func DecisionSetDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var canonical any
	if err := json.Unmarshal(data, &canonical); err != nil {
		return "", err
	}
	data, err = json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// ReplayedApprovalReuse retains the exact controller event that applied a
// previously resolved decision to a later attempt of the same graph edge.
type ReplayedApprovalReuse struct {
	Sequence    uint64 `json:"sequence"`
	AttemptID   string `json:"attempt_id"`
	ID          string `json:"approval_id"`
	SubjectHash string `json:"subject_hash"`
	PriorStatus string `json:"prior_status"`
	FromStage   string `json:"from_stage"`
	ToStage     string `json:"to_stage"`
	Trigger     string `json:"trigger"`
}

type ReplayedAttempt struct {
	AttemptID            string                `json:"attempt_id"`
	Stage                string                `json:"stage"`
	Executor             string                `json:"executor,omitempty"`
	StageAction          string                `json:"stage_action,omitempty"`
	StageSkipVersion     int                   `json:"stage_skip_version,omitempty"`
	ActorID              string                `json:"actor_id,omitempty"`
	ActorRole            string                `json:"actor_role,omitempty"`
	HumanInputApprovalID string                `json:"human_input_approval_id,omitempty"`
	StageIndex           int                   `json:"stage_index"`
	StartedAt            time.Time             `json:"started_at"`
	FinishedAt           time.Time             `json:"finished_at,omitempty"`
	Status               string                `json:"status,omitempty"`
	State                workflow.AttemptState `json:"state"`
	Verdict              string                `json:"verdict,omitempty"`
	Blocker              string                `json:"blocker,omitempty"`
	Error                string                `json:"error,omitempty"`
	SkipReason           string                `json:"skip_reason,omitempty"`
	ManifestSHA256       string                `json:"manifest_sha256,omitempty"`
	Superseded           bool                  `json:"superseded,omitempty"`
}

// ReplayEventLog verifies the hash chain and rebuilds the run lifecycle. It
// fails closed on impossible transitions or manifest identity mismatches.
func ReplayEventLog(path, runID string) (ReplayedRun, error) {
	return ReplayEventLogWithAttemptManifestSource(path, runID, nil)
}

// ReplayEventLogWithAttemptManifestSource replays a verified event log using
// the supplied source for attempt manifest bytes. A nil source preserves the
// filesystem-backed streaming digest used by ReplayEventLog; supplied sources
// are rejected when they return manifests larger than MaxAttemptManifestSize.
func ReplayEventLogWithAttemptManifestSource(path, runID string, source AttemptManifestSource) (ReplayedRun, error) {
	return ReplayEventLogWithEventSources(path, runID, nil, source)
}

// ReplayEventLogWithEventSources replays a journal from the supplied event
// authority and attempt-manifest source. Nil sources retain local behavior.
func ReplayEventLogWithEventSources(path, runID string, eventsSource EventLog, manifestsSource AttemptManifestSource) (ReplayedRun, error) {
	return ReplayEventLogWithEventSourcesAndTarget(path, runID, eventsSource, manifestsSource, "")
}

// ReplayEventLogWithEventSourcesAndTarget replays a journal using explicitly
// selected authorities and, when provided, the run target recorded in its
// manifest. Bundle verification supplies that target so absolute historical
// delivery paths remain verifiable after the bundle is moved.
func ReplayEventLogWithEventSourcesAndTarget(path, runID string, eventsSource EventLog, manifestsSource AttemptManifestSource, deliveryTargetDir string) (ReplayedRun, error) {
	events, err := VerifyEventLogWithSource(path, runID, eventsSource)
	if err != nil {
		return ReplayedRun{}, err
	}
	return replayEventsWithAttemptManifestSourceAndTarget(events, runID, filepath.Dir(path), manifestsSource, deliveryTargetDir)
}

// RecoverMissingStageSkipEvents repairs the single durable crash window where
// a skipped attempt's manifest and attempt_finished event were committed but
// its mandatory warning event was not. The skip reason is taken from the
// finished attempt event, then strict replay validates the repaired journal.
func RecoverMissingStageSkipEvents(runDir, runID string, eventsSource EventLog, manifestsSource AttemptManifestSource) error {
	if err := ValidateRunID(runID); err != nil {
		return err
	}
	eventsPath := filepath.Join(runDir, "events.jsonl")
	writer := eventsSource
	if writer == nil {
		resolved, reserved, err := defaultEventLogSource(eventsPath, runID)
		if err != nil {
			return err
		}
		if reserved {
			writer = resolved
		} else {
			writer = newFileEventLog(eventsPath)
		}
	}
	events, err := VerifyEventLogWithSource(eventsPath, runID, eventsSource)
	if err != nil {
		return err
	}
	replayed, err := replayEventsForAppend(events, runID, runDir, manifestsSource)
	if err != nil {
		return err
	}
	if !replayed.FinishedAt.IsZero() {
		_, strictErr := replayEventsWithAttemptManifestSourceAndTarget(events, runID, runDir, manifestsSource, "")
		return strictErr
	}
	haveWarning := make(map[string]bool, len(replayed.StageSkips))
	for _, skipped := range replayed.StageSkips {
		haveWarning[skipped.AttemptID] = true
	}
	lastHash := chainGenesis(runID)
	if len(events) > 0 {
		lastHash = events[len(events)-1].SHA256
	}
	for _, attempt := range replayed.Attempts {
		if attempt.State.Outcome != workflow.OutcomeSkipped ||
			(attempt.StageAction != "skip" && attempt.Executor != "human") || haveWarning[attempt.AttemptID] {
			continue
		}
		reason := attempt.SkipReason
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("finished skipped attempt %q has no durable reason for recovery", attempt.AttemptID)
		}
		data := map[string]any{"reason": reason, "warning": true}
		if attempt.Executor == "human" {
			data["actor_id"], data["actor_role"] = attempt.ActorID, attempt.ActorRole
		}
		timestamp := time.Now().UTC()
		if timestamp.Before(attempt.FinishedAt) {
			timestamp = attempt.FinishedAt
		}
		appended, appendErr := writer.Append(runID, Event{
			Type: "stage_skipped", Stage: attempt.Stage, AttemptID: attempt.AttemptID,
			Timestamp: timestamp, Data: data,
		}, uint64(len(events)), lastHash)
		if appendErr != nil {
			return fmt.Errorf("recover stage_skipped for %s: %w", attempt.AttemptID, appendErr)
		}
		events = append(events, appended)
		lastHash = appended.SHA256
	}
	_, err = replayEventsWithAttemptManifestSourceAndTarget(events, runID, runDir, manifestsSource, "")
	return err
}

// replayEvents rebuilds lifecycle state from an already verified event chain.
// Keeping replay separate from filesystem access lets the package persistence
// seam share the exact existing transition validation.
func replayEvents(events []Event, runID, runDir string) (ReplayedRun, error) {
	return replayEventsWithAttemptManifestSource(events, runID, runDir, nil)
}

func replayEventsWithAttemptManifestSource(events []Event, runID, runDir string, source AttemptManifestSource) (ReplayedRun, error) {
	return replayEventsWithAttemptManifestSourceAndTarget(events, runID, runDir, source, "")
}

func replayEventsWithAttemptManifestSourceAndTarget(events []Event, runID, runDir string, source AttemptManifestSource, deliveryTargetDir string) (ReplayedRun, error) {
	return replayEventsWithAttemptManifestSourceAndTargetMode(events, runID, runDir, source, deliveryTargetDir, true)
}

// replayEventsForAppend allows the durable prefix between attempt_finished and
// stage_skipped. The skip reason is persisted in attempt_finished so recovery
// can append the warning event before normal strict replay resumes.
func replayEventsForAppend(events []Event, runID, runDir string, source AttemptManifestSource) (ReplayedRun, error) {
	return replayEventsWithAttemptManifestSourceAndTargetMode(events, runID, runDir, source, "", false)
}

func replayEventsWithAttemptManifestSourceAndTargetMode(events []Event, runID, runDir string, source AttemptManifestSource, deliveryTargetDir string, requireStageSkip bool) (ReplayedRun, error) {
	result := ReplayedRun{RunID: runID, Attempts: make([]ReplayedAttempt, 0)}
	var err error
	byID := make(map[string]int)
	approvalSubjects := make(map[string]string)
	decidedApprovals := make(map[string]bool)
	selectedTransitions := make(map[string]bool)
	stageSkippedAttempts := make(map[string]bool)
	finishedCount := 0
	terminal := false
	canceled := false
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
			executor, executorErr := eventString(event.Data, "executor", false)
			if executorErr != nil || (executor != "" && executor != "agent" && executor != "human") {
				return ReplayedRun{}, fmt.Errorf("attempt_started %q executor is invalid", event.AttemptID)
			}
			if executor == "" {
				executor = "agent" // legacy attempt events predate executor metadata.
			}
			stageAction, stageActionErr := eventString(event.Data, "stage_action", false)
			if stageActionErr != nil || (stageAction != "" && stageAction != "skip") {
				return ReplayedRun{}, fmt.Errorf("attempt_started %q stage action is invalid", event.AttemptID)
			}
			stageSkipVersion, versionErr := optionalEventInt(event.Data, "stage_skip_version")
			if versionErr != nil || (stageSkipVersion != 0 && stageSkipVersion != StageSkipProtocolVersion) ||
				(stageSkipVersion != 0 && stageAction != "skip") {
				return ReplayedRun{}, fmt.Errorf("attempt_started %q skip protocol version is invalid", event.AttemptID)
			}
			actorID, actorIDErr := eventString(event.Data, "actor_id", false)
			actorRole, actorRoleErr := eventString(event.Data, "actor_role", false)
			humanInputApprovalID, approvalIDErr := eventString(event.Data, "human_input_approval_id", false)
			if actorIDErr != nil || actorRoleErr != nil || approvalIDErr != nil {
				return ReplayedRun{}, fmt.Errorf("attempt_started %q has invalid human actor identity", event.AttemptID)
			}
			if executor == "human" && (actorID == "" || actorRole == "" || !safeEventIdentifier(humanInputApprovalID)) {
				return ReplayedRun{}, fmt.Errorf("human attempt_started %q has no actor or input approval identity", event.AttemptID)
			}
			result.Attempts = append(result.Attempts, ReplayedAttempt{
				AttemptID: event.AttemptID, Stage: event.Stage, StageIndex: stageIndex,
				Executor: executor, StageAction: stageAction, StageSkipVersion: stageSkipVersion,
				ActorID: actorID, ActorRole: actorRole, HumanInputApprovalID: humanInputApprovalID,
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
			actorID, actorErr := eventString(event.Data, "actor_id", false)
			if actorErr != nil {
				return ReplayedRun{}, actorErr
			}
			actorRole, roleErr := eventString(event.Data, "actor_role", false)
			if roleErr != nil {
				return ReplayedRun{}, roleErr
			}
			humanInputApprovalID, approvalIDErr := eventString(event.Data, "human_input_approval_id", false)
			if approvalIDErr != nil {
				return ReplayedRun{}, approvalIDErr
			}
			if executor, executorErr := eventString(event.Data, "executor", false); executorErr != nil || (executor != "" && executor != attempt.Executor) {
				return ReplayedRun{}, fmt.Errorf("attempt_finished %q executor mismatch", event.AttemptID)
			}
			if attempt.Executor == "human" && (attempt.ActorID == "" || attempt.ActorRole == "" ||
				humanInputApprovalID == "" || actorID != attempt.ActorID ||
				actorRole != attempt.ActorRole || humanInputApprovalID != attempt.HumanInputApprovalID) {
				return ReplayedRun{}, fmt.Errorf("human attempt_finished %q changes its actor or input approval identity", event.AttemptID)
			}
			attempt.ActorID, attempt.ActorRole, attempt.HumanInputApprovalID = actorID, actorRole, humanInputApprovalID
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
			if attempt.SkipReason, err = eventString(event.Data, "stage_skip_reason", false); err != nil {
				return ReplayedRun{}, err
			}
			if attempt.SkipReason != "" {
				if attempt.State.Outcome != workflow.OutcomeSkipped || strings.TrimSpace(attempt.SkipReason) == "" {
					return ReplayedRun{}, fmt.Errorf("attempt_finished %q has invalid stage skip reason", event.AttemptID)
				}
			}
			if attempt.StageAction == "skip" && attempt.StageSkipVersion > 0 && strings.TrimSpace(attempt.SkipReason) == "" {
				return ReplayedRun{}, fmt.Errorf("attempt_finished %q has no durable reason for explicit skip", event.AttemptID)
			}
			if attempt.ManifestSHA256, err = eventString(event.Data, "manifest_sha256", false); err != nil {
				return ReplayedRun{}, err
			}
			if attempt.ManifestSHA256 != "" {
				if !validSHA256(attempt.ManifestSHA256) {
					return ReplayedRun{}, fmt.Errorf("attempt_finished %q manifest digest is invalid", event.AttemptID)
				}
				digest, _, digestErr := attemptManifestDigest(source, runDir, runID, event.AttemptID)
				if digestErr != nil || digest != attempt.ManifestSHA256 {
					return ReplayedRun{}, fmt.Errorf("attempt_finished %q manifest identity mismatch", event.AttemptID)
				}
				if source != nil {
					_, manifest, readErr := ReadAttemptManifest(source, runDir, runID, event.AttemptID)
					if readErr != nil || validateControllerAttemptManifest(manifest) != nil ||
						manifest.Stage != event.Stage || !manifest.StartedAt.Equal(attempt.StartedAt) || !manifest.FinishedAt.Equal(event.Timestamp) ||
						manifest.Status != attempt.Status || manifest.Execution != string(attempt.State.Execution) ||
						manifest.Decision != string(attempt.State.Decision) || manifest.Outcome != string(attempt.State.Outcome) ||
						manifest.Verdict != attempt.Verdict || manifest.Blocker != attempt.Blocker || manifest.Error != attempt.Error ||
						(manifest.Executor != "" && manifest.Executor != attempt.Executor) || manifest.ActorID != attempt.ActorID ||
						manifest.ActorRole != attempt.ActorRole || manifest.HumanInputApprovalID != attempt.HumanInputApprovalID {
						return ReplayedRun{}, fmt.Errorf("attempt_finished %q disagrees with its controller-readable manifest", event.AttemptID)
					}
				}
			} else if attempt.Error == "" || attempt.Status != string(workflow.OutcomeFailed) {
				return ReplayedRun{}, fmt.Errorf("attempt_finished %q without manifest must be an errored failed attempt", event.AttemptID)
			}
			finishedCount++
		case "attempt_abandoned":
			index, exists := byID[event.AttemptID]
			if !exists || !result.Attempts[index].FinishedAt.IsZero() {
				return ReplayedRun{}, fmt.Errorf("attempt_abandoned %q has no matching active attempt", event.AttemptID)
			}
			attempt := &result.Attempts[index]
			attempt.FinishedAt = event.Timestamp
			attempt.Status = "canceled"
			attempt.State = workflow.AttemptState{
				Execution: workflow.ExecutionCanceled,
				Decision:  workflow.DecisionNotApplicable,
				Outcome:   workflow.OutcomeCanceled,
			}
			attempt.Error, _ = eventString(event.Data, "reason", false)
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
		case "approval_requested":
			approvalID, idErr := eventString(event.Data, "approval_id", true)
			subjectHash, hashErr := eventString(event.Data, "subject_hash", true)
			status, statusErr := eventString(event.Data, "status", true)
			if idErr != nil || hashErr != nil || statusErr != nil ||
				!safeEventIdentifier(approvalID) || !validSHA256(subjectHash) ||
				status != "pending" || event.AttemptID == "" {
				return ReplayedRun{}, fmt.Errorf("approval_requested содержит недопустимую identity")
			}
			if previous, exists := approvalSubjects[approvalID]; exists && previous != subjectHash {
				return ReplayedRun{}, fmt.Errorf("approval_requested %s меняет subject", approvalID)
			}
			approvalSubjects[approvalID] = subjectHash
		case "approval_decided":
			approvalID, idErr := eventString(event.Data, "approval_id", true)
			subjectHash, hashErr := eventString(event.Data, "subject_hash", true)
			status, statusErr := eventString(event.Data, "status", true)
			action, actionErr := eventString(event.Data, "resolved_action", true)
			expected, exists := approvalSubjects[approvalID]
			if idErr != nil || hashErr != nil || statusErr != nil || actionErr != nil ||
				!exists || expected != subjectHash || status != "resolved" ||
				action == "" || decidedApprovals[approvalID] {
				return ReplayedRun{}, fmt.Errorf("approval_decided %s не соответствует запросу", approvalID)
			}
			decidedApprovals[approvalID] = true
			fromStage, fromErr := eventString(event.Data, "from_stage", false)
			toStage, toErr := eventString(event.Data, "to_stage", false)
			trigger, triggerErr := eventString(event.Data, "trigger", false)
			kind, kindErr := eventString(event.Data, "kind", false)
			if fromErr != nil || toErr != nil || triggerErr != nil || kindErr != nil {
				return ReplayedRun{}, fmt.Errorf("approval_decided %s has invalid source identity", approvalID)
			}
			decisionSetDigest := ""
			if rawDecisions, present := event.Data["decisions"]; present {
				decisionSetDigest, err = DecisionSetDigest(rawDecisions)
				if err != nil {
					return ReplayedRun{}, fmt.Errorf("approval_decided %s has invalid decisions", approvalID)
				}
			}
			result.ApprovalDecisions = append(result.ApprovalDecisions, ReplayedApprovalDecision{
				Sequence: event.Sequence, AttemptID: event.AttemptID, ID: approvalID, Kind: kind,
				SubjectHash: subjectHash, FromStage: fromStage, ToStage: toStage,
				Trigger: trigger, Action: action, DecisionSetSHA256: decisionSetDigest,
			})
		case "transition_selected":
			index, exists := byID[event.AttemptID]
			from, fromErr := eventString(event.Data, "from", true)
			outcome, outcomeErr := eventString(event.Data, "outcome", true)
			edgeTarget, edgeErr := eventString(event.Data, "edge_target", true)
			target, targetErr := eventString(event.Data, "target", true)
			if !exists || result.Attempts[index].FinishedAt.IsZero() || selectedTransitions[event.AttemptID] ||
				fromErr != nil || outcomeErr != nil || edgeErr != nil || targetErr != nil ||
				from != result.Attempts[index].Stage || outcome != string(result.Attempts[index].State.Outcome) ||
				strings.ContainsAny(edgeTarget, "/\\") || strings.ContainsAny(target, "/\\") {
				return ReplayedRun{}, fmt.Errorf("transition_selected %s не соответствует attempt", event.AttemptID)
			}
			action, actionErr := eventString(event.Data, "action", false)
			if actionErr != nil {
				return ReplayedRun{}, fmt.Errorf("transition_selected %s has invalid action", event.AttemptID)
			}
			selectedTransitions[event.AttemptID] = true
			result.Transitions = append(result.Transitions, ReplayedTransition{
				Sequence: event.Sequence, AttemptID: event.AttemptID, From: from,
				Outcome: outcome, EdgeTarget: edgeTarget, Action: action, Target: target,
			})
		case "stage_skipped":
			index, exists := byID[event.AttemptID]
			reason, reasonErr := eventString(event.Data, "reason", true)
			warning, warningOK := event.Data["warning"].(bool)
			if !exists || stageSkippedAttempts[event.AttemptID] ||
				result.Attempts[index].Stage != event.Stage || result.Attempts[index].FinishedAt.IsZero() ||
				result.Attempts[index].State.Outcome != workflow.OutcomeSkipped ||
				event.Timestamp.Before(result.Attempts[index].FinishedAt) || reasonErr != nil ||
				strings.TrimSpace(reason) == "" || !warningOK || !warning {
				return ReplayedRun{}, fmt.Errorf("stage_skipped %q has invalid attempt, warning, or reason", event.AttemptID)
			}
			attempt := result.Attempts[index]
			// Pre-version B-34 skips bound the reason in this warning rather
			// than in attempt_finished. Agent skips still require stage_action;
			// old human skips are identified by their human executor metadata.
			if attempt.StageAction != "skip" && attempt.Executor != "human" {
				return ReplayedRun{}, fmt.Errorf("stage_skipped %q is not an explicit stage skip", event.AttemptID)
			}
			if attempt.SkipReason != "" && attempt.SkipReason != reason {
				return ReplayedRun{}, fmt.Errorf("stage_skipped %q reason differs from finished attempt", event.AttemptID)
			}
			if attempt.Executor == "human" {
				actorID, actorErr := eventString(event.Data, "actor_id", true)
				actorRole, roleErr := eventString(event.Data, "actor_role", true)
				if actorErr != nil || roleErr != nil || actorID != attempt.ActorID || actorRole != attempt.ActorRole {
					return ReplayedRun{}, fmt.Errorf("stage_skipped %q has invalid human actor", event.AttemptID)
				}
			}
			stageSkippedAttempts[event.AttemptID] = true
			result.StageSkips = append(result.StageSkips, ReplayedStageSkip{
				Sequence: event.Sequence, AttemptID: event.AttemptID, Stage: event.Stage, Reason: reason,
			})
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
			if result.Status == workflow.RunCanceled && !canceled {
				return ReplayedRun{}, errors.New("canceled run не содержит run_canceled")
			}
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
		case "run_paused":
			if result.StartedAt.IsZero() {
				return ReplayedRun{}, fmt.Errorf("run_paused appears before run_started")
			}
		case "run_resumed":
			if result.StartedAt.IsZero() {
				return ReplayedRun{}, fmt.Errorf("run_resumed appears before run_started")
			}
			for _, attempt := range result.Attempts {
				if attempt.FinishedAt.IsZero() {
					return ReplayedRun{}, fmt.Errorf("run_resumed contains active attempt %q", attempt.AttemptID)
				}
			}
		case "run_canceled":
			if result.StartedAt.IsZero() || canceled {
				return ReplayedRun{}, errors.New("run_canceled имеет недопустимую позицию")
			}
			canceled = true
		case "approval_reused":
			approvalID, idErr := eventString(event.Data, "approval_id", true)
			subjectHash, hashErr := eventString(event.Data, "subject_hash", true)
			priorStatus, statusErr := eventString(event.Data, "prior_status", true)
			fromStage, fromErr := eventString(event.Data, "from_stage", false)
			toStage, toErr := eventString(event.Data, "to_stage", false)
			trigger, triggerErr := eventString(event.Data, "trigger", false)
			if idErr != nil || hashErr != nil || statusErr != nil || !safeEventIdentifier(approvalID) ||
				!validSHA256(subjectHash) || approvalSubjects[approvalID] != subjectHash ||
				(priorStatus != "pending" && priorStatus != "resolved") || event.AttemptID == "" ||
				fromErr != nil || toErr != nil || triggerErr != nil {
				return ReplayedRun{}, fmt.Errorf("approval_reused содержит недопустимую identity")
			}
			result.ApprovalReuses = append(result.ApprovalReuses, ReplayedApprovalReuse{
				Sequence: event.Sequence, AttemptID: event.AttemptID, ID: approvalID,
				SubjectHash: subjectHash, PriorStatus: priorStatus,
				FromStage: fromStage, ToStage: toStage, Trigger: trigger,
			})
		case "delivery_deferred":
			planHash, hashErr := eventString(event.Data, "plan_hash", true)
			statePath, pathErr := eventString(event.Data, "state_path", true)
			validStatePath := ValidDeliveryStatePath(runDir, statePath)
			if deliveryTargetDir != "" {
				validStatePath = ValidDeliveryStatePathForTargetAndRun(deliveryTargetDir, runID, statePath)
			}
			if hashErr != nil || pathErr != nil || !validSHA256(planHash) || event.AttemptID == "" || !validStatePath {
				return ReplayedRun{}, errors.New("delivery_deferred содержит недопустимую identity")
			}
		case "delivery_plan_approved":
			planHash, hashErr := eventString(event.Data, "plan_hash", true)
			mode, modeErr := eventString(event.Data, "mode", true)
			approver, approverErr := eventString(event.Data, "approver", true)
			if hashErr != nil || modeErr != nil || approverErr != nil || !validSHA256(planHash) ||
				strings.TrimSpace(approver) == "" || (mode != "hash_flag" && mode != "resolved_approval") || event.AttemptID == "" {
				return ReplayedRun{}, errors.New("delivery_plan_approved содержит недопустимую identity")
			}
		case "deferred_gates_ratified":
			action, actionErr := eventString(event.Data, "action", true)
			approver, approverErr := eventString(event.Data, "approver", true)
			if actionErr != nil || approverErr != nil || strings.TrimSpace(approver) == "" || (action != "approve" && action != "reject") || validateRatifiedGateEvents(event.Data["gates"]) != nil {
				return ReplayedRun{}, errors.New("deferred_gates_ratified содержит недопустимую identity")
			}
		case "test_mutations":
			policy, policyErr := eventString(event.Data, "policy", true)
			index, exists := byID[event.AttemptID]
			if policyErr != nil || (policy != "off" && policy != "required" && policy != "warn") || !exists || result.Attempts[index].Stage != event.Stage {
				return ReplayedRun{}, errors.New("test_mutations не соответствует attempt")
			}
		case "resume_blocked":
			reason, reasonErr := eventString(event.Data, "reason", true)
			if reasonErr != nil || strings.TrimSpace(reason) == "" {
				return ReplayedRun{}, errors.New("resume_blocked содержит недопустимую причину")
			}
		case "description_missing":
			index, exists := byID[event.AttemptID]
			field, fieldErr := eventString(event.Data, "field", true)
			approvalID, approvalErr := eventString(event.Data, "approval_id", true)
			if !exists || result.Attempts[index].Stage != event.Stage || !result.Attempts[index].FinishedAt.IsZero() ||
				result.Attempts[index].Executor != "human" || result.Attempts[index].HumanInputApprovalID != approvalID ||
				fieldErr != nil || field != "description" || approvalErr != nil || event.Timestamp.Before(result.Attempts[index].StartedAt) {
				return ReplayedRun{}, errors.New("description_missing does not match an active human stage attempt")
			}
		default:
			// Preserve historical replay compatibility for old extension events.
			// The worker API boundary has a separate strict allowlist, so new
			// untrusted event types can never enter controller-owned logs.
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
		if requireStageSkip && attempt.State.Outcome == workflow.OutcomeSkipped &&
			(attempt.StageAction == "skip" || attempt.Executor == "human") && !stageSkippedAttempts[attempt.AttemptID] {
			return ReplayedRun{}, fmt.Errorf("finished skipped attempt %q has no stage_skipped warning", attempt.AttemptID)
		}
	}
	return result, nil
}

func validateRatifiedGateEvents(raw any) error {
	gates, ok := raw.([]any)
	if !ok || len(gates) == 0 {
		return errors.New("gates must be a non-empty array")
	}
	seen := make(map[string]bool, len(gates))
	for _, rawGate := range gates {
		gate, ok := rawGate.(map[string]any)
		if !ok {
			return errors.New("gate must be an object")
		}
		id, idOK := gate["approval_id"].(string)
		subject, subjectOK := gate["subject_hash"].(string)
		action, actionOK := gate["action"].(string)
		if !idOK || !safeEventIdentifier(id) || seen[id] || !subjectOK || !validSHA256(subject) || !actionOK || action == "" {
			return errors.New("gate identity is invalid")
		}
		seen[id] = true
	}
	return nil
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

func optionalEventInt(data map[string]any, name string) (int, error) {
	if _, exists := data[name]; !exists {
		return 0, nil
	}
	return eventInt(data, name)
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
