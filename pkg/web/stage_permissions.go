package web

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

// errSelfApprovalUnverifiable is returned when the self-approval policy is
// enabled but the stage authors cannot be determined. The check fails closed.
var errSelfApprovalUnverifiable = errors.New("cloud RBAC: не удалось определить авторов этапа для проверки самоподтверждения")

// selfApprovalViolation enforces the pinned template's forbid_self_approval
// setting (B-82) for a stage confirmation. It returns nil when the policy is
// disabled, the run has no pinned template, or actorID did not take or submit
// the stage in the visit being confirmed.
func (s *Server) selfApprovalViolation(runID string, pending approval.PendingApproval, actorID string) error {
	cfg, found, err := s.pinnedTaskConfig(runID)
	if err != nil {
		return errSelfApprovalUnverifiable
	}
	if !found || cfg == nil || !cfg.ForbidSelfApproval {
		return nil
	}
	authors, err := s.stageWorkAuthors(runID, pending)
	if err != nil {
		return errSelfApprovalUnverifiable
	}
	return cloudidentity.CheckSelfApproval(true, actorID, authors)
}

// stageAgentRequestedEvent records the actor who launched a stage agent, so
// the launch counts as authorship for forbid_self_approval.
const stageAgentRequestedEvent = "stage_agent_requested"

// stageAuthorEventTypes are run events whose actor worked on the stage.
var stageAuthorEventTypes = map[string]bool{stageAgentRequestedEvent: true, "executor_changed": true}

// stageWorkAuthors returns every human who worked on the stage in the visit
// confirmed by pending. The visit starts after the previous resolved
// confirmation of the same stage (or at the run start). Authors are:
//   - actors of decisions on human input, grill (answer/new_version/done) and
//     check-override approvals of the stage in the visit;
//   - the owner who took a human input of the visit;
//   - actors who launched the stage agent or switched its executor;
//   - actors who appended a revision of the stage result or of an output of
//     any stage attempt in the visit (agent output edits included).
//
// Any read error is returned so the caller fails closed.
func (s *Server) stageWorkAuthors(runID string, pending approval.PendingApproval) ([]string, error) {
	stageID := pending.FromStage
	if s.controller == nil || stageID == "" {
		return nil, fmt.Errorf("stage confirmation context unavailable")
	}
	values, err := s.controller.Approvals(runID)
	if err != nil {
		return nil, err
	}
	var visitStart time.Time
	for _, value := range values {
		if value.ID == pending.ID || value.FromStage != stageID || !isStageConfirmation(value) ||
			value.Status != approval.StatusResolved || !value.CreatedAt.Before(pending.CreatedAt) {
			continue
		}
		resolvedAt := value.ResolvedAt
		if resolvedAt.IsZero() {
			resolvedAt = value.CreatedAt
		}
		if resolvedAt.After(visitStart) {
			visitStart = resolvedAt
		}
	}
	inVisit := func(at time.Time) bool { return visitStart.IsZero() || at.After(visitStart) }

	authors := []string{}
	inputIDs := map[string]bool{}
	attempts := map[string]bool{}
	if pending.AttemptID != "" {
		attempts[pending.AttemptID] = true
	}
	for _, value := range values {
		if value.ID == pending.ID || value.FromStage != stageID || !inVisit(value.CreatedAt) {
			continue
		}
		switch value.Kind {
		case approval.KindInput, approval.KindGrillQuestions, approval.KindCheckDecision:
		default:
			continue
		}
		if value.Kind == approval.KindInput {
			inputIDs[value.ID] = true
		}
		if value.AttemptID != "" {
			attempts[value.AttemptID] = true
		}
		for _, decision := range value.Decisions {
			authors = append(authors, decision.ActorID)
		}
	}

	owners, err := s.store.GetStageOwners(runID)
	if err != nil {
		return nil, err
	}
	if owner, ok := owners[stageID]; ok && inputIDs[owner.ApprovalID] {
		authors = append(authors, owner.ActorID)
	}

	events, err := s.allRunEvents(runID)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if stageAuthorEventTypes[event.Type] && event.Stage == stageID && inVisit(event.Timestamp) {
			if event.Actor == "" {
				return nil, fmt.Errorf("stage event %s has no actor", event.Type)
			}
			authors = append(authors, event.Actor)
		}
	}

	paths := []string{humanStageResultPath(stageID, "md"), humanStageResultPath(stageID, "link"), humanStageResultPath(stageID, "approve")}
	for attemptID := range attempts {
		if !safeIdentity(attemptID) {
			return nil, fmt.Errorf("invalid attempt id %q", attemptID)
		}
		_, manifest, readErr := evidence.ReadAttemptManifest(nil, filepath.Join(s.runRoot, runID), runID, attemptID)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		for _, output := range manifest.Outputs {
			paths = append(paths, output.EvidencePath)
		}
	}
	for _, path := range paths {
		revisions, listErr := s.humanArtifacts.List(runID, path)
		if listErr != nil {
			return nil, listErr
		}
		for _, revision := range revisions {
			if inVisit(revision.CreatedAt) || inputIDs[revision.ApprovalID] {
				authors = append(authors, revision.ActorID)
			}
		}
	}
	return authors, nil
}

// isStageConfirmation reports whether pending is a generated stage transition
// approval (as opposed to human input, check decisions, grill, questions or the
// delivery plan).
func isStageConfirmation(pending approval.PendingApproval) bool {
	return (pending.Kind == "" || pending.Kind == approval.KindApprove) &&
		pending.Trigger != "delivery_plan" && pending.FromStage != ""
}
