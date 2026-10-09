package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// appendControllerHumanAttemptStarted converts a worker request into a
// controller-owned event. The worker supplies only attempt/stage identity and
// the approval ID; actor and action are reloaded from controller approval
// state.
func (s *workerAPIServer) appendControllerHumanAttemptStarted(request evidence.Event) (evidence.Event, error) {
	if request.Type != "attempt_started" || request.Stage == "" || request.AttemptID == "" || len(request.Data) != 2 {
		return evidence.Event{}, errors.New("human attempt start request has invalid identity or fields")
	}
	for key := range request.Data {
		if key != "stage_index" && key != "human_input_approval_id" {
			return evidence.Event{}, fmt.Errorf("human attempt start request contains unauthorized field %q", key)
		}
	}
	approvalID, ok := request.Data["human_input_approval_id"].(string)
	if !ok || approvalID == "" {
		return evidence.Event{}, errors.New("human attempt start requires an input approval ID")
	}
	events, err := s.eventLogs.Read(s.scope.RunID)
	if err != nil {
		return evidence.Event{}, err
	}
	value, decision, err := s.authorizedHumanInput(events, request.Stage, approvalID)
	if err != nil {
		return evidence.Event{}, err
	}
	if request.Timestamp.IsZero() {
		request.Timestamp = time.Now().UTC()
	}
	if request.Timestamp.Before(decision.DecidedAt) {
		return evidence.Event{}, errors.New("human attempt start predates its approved input decision")
	}
	data := map[string]any{
		"stage_index": request.Data["stage_index"], "executor": "human",
		"actor_id": decision.ActorID, "actor_role": decision.ActorRole,
		"human_input_approval_id": value.ID,
	}
	if value.ResolvedAction == "skip" {
		data["stage_action"] = "skip"
		data["stage_skip_version"] = float64(evidence.StageSkipProtocolVersion)
	}
	candidate := evidence.Event{Type: request.Type, Stage: request.Stage, AttemptID: request.AttemptID, Timestamp: request.Timestamp, Data: data}
	return s.appendControllerHumanEvent(events, candidate, false)
}

// appendControllerHumanAttemptFinished derives the terminal event entirely
// from the controller-published manifest and resolved approval. Worker data
// cannot choose the result or a skip reason.
func (s *workerAPIServer) appendControllerHumanAttemptFinished(request evidence.Event) (evidence.Event, error) {
	if request.Type != "attempt_finished" || request.Stage == "" || request.AttemptID == "" || len(request.Data) != 0 {
		return evidence.Event{}, errors.New("human attempt finish request has invalid identity or fields")
	}
	events, err := s.eventLogs.Read(s.scope.RunID)
	if err != nil {
		return evidence.Event{}, err
	}
	started, ok := controllerEventForAttempt(events, "attempt_started", request.Stage, request.AttemptID)
	if !ok || started.Data["executor"] != "human" {
		return evidence.Event{}, errors.New("human attempt finish has no controller-owned start")
	}
	approvalID, _ := started.Data["human_input_approval_id"].(string)
	value, decision, err := s.authorizedHumanInput(events, request.Stage, approvalID)
	if err != nil {
		return evidence.Event{}, err
	}
	runDir := filepath.Join(s.scope.TargetDir, ".ai-team", "runs", s.scope.RunID)
	manifestSource := evidence.ReservedAttemptManifestSource{TargetDir: s.scope.TargetDir}
	_, manifest, err := evidence.ReadAttemptManifest(manifestSource, runDir, s.scope.RunID, request.AttemptID)
	if err != nil {
		return evidence.Event{}, fmt.Errorf("read controller human attempt manifest: %w", err)
	}
	if manifest.Stage != request.Stage || manifest.Executor != "human" || manifest.ActorID != decision.ActorID ||
		manifest.ActorRole != decision.ActorRole || manifest.HumanInputApprovalID != value.ID ||
		manifest.StageIndex != numericWorkerVersion(started.Data["stage_index"]) || manifest.StartedAt.IsZero() ||
		!manifest.StartedAt.Equal(started.Timestamp) || manifest.FinishedAt.IsZero() {
		return evidence.Event{}, errors.New("human attempt manifest does not match its approved controller start")
	}
	isSkip := value.ResolvedAction == "skip"
	if isSkip {
		if manifest.Outcome != string(workflow.OutcomeSkipped) || manifest.Status != string(workflow.OutcomeSkipped) ||
			manifest.Decision != string(workflow.DecisionNotApplicable) || len(manifest.Outputs) != 0 ||
			manifest.Error != "" || strings.TrimSpace(decision.Comment) == "" {
			return evidence.Event{}, errors.New("human skip manifest does not match the approved skip decision")
		}
	} else if manifest.Outcome == string(workflow.OutcomeSkipped) {
		return evidence.Event{}, errors.New("human attempt cannot be skipped without an approved skip decision")
	}
	digest, _, err := evidence.AttemptManifestDigest(manifestSource, runDir, s.scope.RunID, request.AttemptID)
	if err != nil {
		return evidence.Event{}, fmt.Errorf("digest controller human attempt manifest: %w", err)
	}
	data := map[string]any{
		"status": manifest.Status, "execution": manifest.Execution, "decision": manifest.Decision,
		"outcome": manifest.Outcome, "verdict": manifest.Verdict, "executor": "human",
		"actor_id": decision.ActorID, "actor_role": decision.ActorRole,
		"human_input_approval_id": value.ID, "manifest_sha256": digest,
	}
	if manifest.Blocker != "" {
		data["blocker"] = manifest.Blocker
	}
	if manifest.Error != "" {
		data["error"] = manifest.Error
	}
	if isSkip {
		data["stage_skip_reason"] = strings.TrimSpace(decision.Comment)
	}
	candidate := evidence.Event{Type: request.Type, Stage: request.Stage, AttemptID: request.AttemptID,
		Timestamp: manifest.FinishedAt, Data: data}
	return s.appendControllerHumanEvent(events, candidate, false)
}

// appendControllerHumanStageSkip writes the mandatory warning from the same
// approved decision as the finished human attempt. The worker sends no reason,
// actor, or warning payload.
func (s *workerAPIServer) appendControllerHumanStageSkip(request evidence.Event, events []evidence.Event) (evidence.Event, error) {
	if request.Type != "stage_skipped" || request.Stage == "" || request.AttemptID == "" || len(request.Data) != 0 {
		return evidence.Event{}, errors.New("controller stage skip request has invalid identity or fields")
	}
	started, ok := controllerEventForAttempt(events, "attempt_started", request.Stage, request.AttemptID)
	if !ok || started.Data["executor"] != "human" {
		return evidence.Event{}, errors.New("controller stage skip requires a human attempt")
	}
	approvalID, _ := started.Data["human_input_approval_id"].(string)
	value, decision, err := s.authorizedHumanInput(events, request.Stage, approvalID)
	if err != nil {
		return evidence.Event{}, err
	}
	if value.ResolvedAction != "skip" || strings.TrimSpace(decision.Comment) == "" {
		return evidence.Event{}, errors.New("stage skip is not authorized by the human input approval")
	}
	finished, ok := controllerEventForAttempt(events, "attempt_finished", request.Stage, request.AttemptID)
	if !ok || finished.Data["executor"] != "human" || finished.Data["outcome"] != string(workflow.OutcomeSkipped) ||
		finished.Data["stage_skip_reason"] != strings.TrimSpace(decision.Comment) {
		return evidence.Event{}, errors.New("stage skip warning has no matching approved finished attempt")
	}
	data := map[string]any{
		"reason": strings.TrimSpace(decision.Comment), "warning": true,
		"actor_id": decision.ActorID, "actor_role": decision.ActorRole,
	}
	return s.appendControllerHumanEvent(events, evidence.Event{
		Type: "stage_skipped", Stage: request.Stage, AttemptID: request.AttemptID,
		Timestamp: finished.Timestamp, Data: data,
	}, true)
}

func (s *workerAPIServer) appendControllerHumanEvent(events []evidence.Event, candidate evidence.Event, stageSkip bool) (evidence.Event, error) {
	for _, stored := range events {
		if stored.Type != candidate.Type || stored.Stage != candidate.Stage || stored.AttemptID != candidate.AttemptID {
			continue
		}
		if stored.Timestamp.Equal(candidate.Timestamp) && controllerEventDataEqual(stored.Data, candidate.Data) {
			return stored, nil
		}
		return evidence.Event{}, errors.New("conflicting controller human attempt event already exists")
	}
	if len(events) == 0 || s.eventLogs == nil {
		return evidence.Event{}, errors.New("controller human event requires an initialized run event chain")
	}
	previous := events[len(events)-1].SHA256
	runDir := filepath.Join(s.scope.TargetDir, ".ai-team", "runs", s.scope.RunID)
	manifestSource := evidence.ReservedAttemptManifestSource{TargetDir: s.scope.TargetDir}
	var validated evidence.Event
	var err error
	if stageSkip {
		validated, _, err = evidence.ValidateControllerStageSkipAppend(events, s.scope.RunID, runDir, candidate,
			uint64(len(events)), previous, manifestSource)
	} else {
		validated, _, err = evidence.ValidateControllerHumanAttemptAppend(events, s.scope.RunID, runDir, candidate,
			uint64(len(events)), previous, manifestSource)
	}
	if err != nil {
		return evidence.Event{}, err
	}
	appended, err := s.eventLogs.Append(s.scope.RunID, validated, uint64(len(events)), previous)
	if err != nil {
		return evidence.Event{}, err
	}
	s.eventSnapshot = nil
	s.eventSnapshotToken = ""
	return appended, nil
}

func controllerEventDataEqual(left, right map[string]any) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return bytes.Equal(leftBytes, rightBytes)
}

func controllerEventForAttempt(events []evidence.Event, eventType, stage, attemptID string) (evidence.Event, bool) {
	for _, event := range events {
		if event.Type == eventType && event.Stage == stage && event.AttemptID == attemptID {
			return event, true
		}
	}
	return evidence.Event{}, false
}

func (s *workerAPIServer) authorizedHumanInput(events []evidence.Event, stage, approvalID string) (approval.PendingApproval, approval.Decision, error) {
	if s.approvals == nil || approvalID == "" {
		return approval.PendingApproval{}, approval.Decision{}, errors.New("human input approval authority is unavailable")
	}
	value, err := s.approvals.Load(s.scope.RunID, approvalID)
	if err != nil {
		return approval.PendingApproval{}, approval.Decision{}, fmt.Errorf("load human input approval: %w", err)
	}
	if value.RunID != s.scope.RunID || value.ID != approvalID || value.Kind != approval.KindInput ||
		value.Trigger != workerHumanInputTrigger || value.Status != approval.StatusResolved ||
		value.FromStage != stage || value.ToStage != stage || value.AttemptID == "" || value.SubjectHash == "" ||
		value.ResolvedAt.IsZero() || value.ResolvedAction == "" || !containsWorkerString(value.Actions, value.ResolvedAction) ||
		value.Targets[value.ResolvedAction] != stage || len(value.Decisions) == 0 {
		return approval.PendingApproval{}, approval.Decision{}, errors.New("human input approval does not authorize this stage action")
	}
	var payload approval.InputPayload
	if err := json.Unmarshal(value.Payload, &payload); err != nil || payload.Kind != string(approval.KindInput) || payload.StageID != stage ||
		payload.OutputName == "" || payload.OutputPath == "" {
		return approval.PendingApproval{}, approval.Decision{}, errors.New("human input approval payload does not match its stage")
	}
	decision := value.Decisions[len(value.Decisions)-1]
	if decision.ApprovalID != value.ID || decision.SubjectHash != value.SubjectHash || decision.Action != value.ResolvedAction ||
		strings.TrimSpace(decision.ActorID) == "" || strings.TrimSpace(decision.ActorRole) == "" || decision.DecidedAt.IsZero() {
		return approval.PendingApproval{}, approval.Decision{}, errors.New("human input approval has no matching actor decision")
	}
	if value.ResolvedAction == "skip" && strings.TrimSpace(decision.Comment) == "" {
		return approval.PendingApproval{}, approval.Decision{}, errors.New("human input skip requires an approved reason")
	}
	if !containsWorkerString(value.RequiredRoles, decision.ActorRole) {
		return approval.PendingApproval{}, approval.Decision{}, errors.New("human input decision actor role is not authorized")
	}
	decisionDigest, err := evidence.DecisionSetDigest(value.Decisions)
	if err != nil {
		return approval.PendingApproval{}, approval.Decision{}, err
	}
	for _, event := range events {
		if event.Type != "approval_decided" || event.AttemptID != value.AttemptID || event.Data["approval_id"] != value.ID {
			continue
		}
		if event.Data["kind"] != string(approval.KindInput) || event.Data["subject_hash"] != value.SubjectHash ||
			event.Data["status"] != string(approval.StatusResolved) || event.Data["resolved_action"] != value.ResolvedAction ||
			event.Data["from_stage"] != stage || event.Data["to_stage"] != stage || event.Data["trigger"] != workerHumanInputTrigger {
			continue
		}
		eventDigest, digestErr := evidence.DecisionSetDigest(event.Data["decisions"])
		if digestErr == nil && eventDigest == decisionDigest {
			return value, decision, nil
		}
	}
	return approval.PendingApproval{}, approval.Decision{}, errors.New("human input approval has no matching durable approval_decided event")
}

// validateMissingHumanSkipAuthorities prevents controller recovery from
// blessing a worker-created skipped manifest. It runs before repair and only
// examines the crash window where a human skip is finished but its warning is
// absent.
func (s *workerAPIServer) validateMissingHumanSkipAuthorities(events []evidence.Event) error {
	starts := make(map[string]evidence.Event)
	finishes := make(map[string]evidence.Event)
	warnings := make(map[string]bool)
	for _, event := range events {
		switch event.Type {
		case "attempt_started":
			starts[event.AttemptID] = event
		case "attempt_finished":
			finishes[event.AttemptID] = event
		case "stage_skipped":
			warnings[event.AttemptID] = true
		}
	}
	for attemptID, start := range starts {
		if warnings[attemptID] {
			continue
		}
		finish, exists := finishes[attemptID]
		if !exists || finish.Data["outcome"] != string(workflow.OutcomeSkipped) ||
			(start.Data["stage_action"] != "skip" && start.Data["executor"] != "human") {
			continue
		}
		if start.Data["executor"] != "human" {
			return fmt.Errorf("worker-backed controller has no authority to repair agent skip %s", attemptID)
		}
		approvalID, _ := start.Data["human_input_approval_id"].(string)
		value, decision, err := s.authorizedHumanInput(events, start.Stage, approvalID)
		if err != nil {
			return fmt.Errorf("authorize human skip recovery for %s: %w", attemptID, err)
		}
		action, hasAction := start.Data["stage_action"]
		version, hasVersion := start.Data["stage_skip_version"]
		legacyShape := !hasAction && !hasVersion
		currentShape := action == "skip" && numericWorkerVersion(version) == evidence.StageSkipProtocolVersion
		if value.ResolvedAction != "skip" || (!legacyShape && !currentShape) ||
			start.Data["actor_id"] != decision.ActorID || start.Data["actor_role"] != decision.ActorRole ||
			finish.Data["actor_id"] != decision.ActorID || finish.Data["actor_role"] != decision.ActorRole ||
			finish.Data["human_input_approval_id"] != value.ID ||
			finish.Data["stage_skip_reason"] != strings.TrimSpace(decision.Comment) {
			return fmt.Errorf("human skip attempt %s does not match its durable input approval", attemptID)
		}
		runDir := filepath.Join(s.scope.TargetDir, ".ai-team", "runs", s.scope.RunID)
		_, manifest, err := evidence.ReadAttemptManifest(
			evidence.ReservedAttemptManifestSource{TargetDir: s.scope.TargetDir}, runDir, s.scope.RunID, attemptID)
		if err != nil || manifest.Stage != start.Stage || manifest.Executor != "human" ||
			manifest.ActorID != decision.ActorID || manifest.ActorRole != decision.ActorRole ||
			manifest.HumanInputApprovalID != value.ID || manifest.StageIndex != numericWorkerVersion(start.Data["stage_index"]) ||
			!manifest.StartedAt.Equal(start.Timestamp) || !manifest.FinishedAt.Equal(finish.Timestamp) ||
			manifest.Outcome != string(workflow.OutcomeSkipped) || manifest.Status != string(workflow.OutcomeSkipped) ||
			manifest.Decision != string(workflow.DecisionNotApplicable) || manifest.Error != "" || len(manifest.Outputs) != 0 {
			return fmt.Errorf("human skip attempt %s does not have an approval-matched empty-output manifest", attemptID)
		}
	}
	return nil
}

func numericWorkerVersion(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case float64:
		if number == float64(int(number)) {
			return int(number)
		}
	}
	return 0
}
