package web

import (
	"errors"
	"fmt"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
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

// stageWorkAuthors returns the humans who worked on the stage confirmed by
// pending: every actor that resolved the latest human-input approval of the
// same stage created before the confirmation, and the stage owner who took
// that input. An agent-only visit has no human authors.
func (s *Server) stageWorkAuthors(runID string, pending approval.PendingApproval) ([]string, error) {
	if s.controller == nil || pending.FromStage == "" {
		return nil, fmt.Errorf("stage confirmation context unavailable")
	}
	values, err := s.controller.Approvals(runID)
	if err != nil {
		return nil, err
	}
	var input *approval.PendingApproval
	for index := range values {
		value := values[index]
		if value.Kind != approval.KindInput || value.Trigger != humanInputTrigger || value.FromStage != pending.FromStage ||
			value.Status != approval.StatusResolved || value.ID == pending.ID || value.CreatedAt.After(pending.CreatedAt) {
			continue
		}
		if input == nil || value.CreatedAt.After(input.CreatedAt) {
			input = &values[index]
		}
	}
	if input == nil {
		return nil, nil
	}
	authors := make([]string, 0, len(input.Decisions)+1)
	for _, decision := range input.Decisions {
		authors = append(authors, decision.ActorID)
	}
	owners, err := s.store.GetStageOwners(runID)
	if err != nil {
		return nil, err
	}
	if owner, ok := owners[pending.FromStage]; ok && owner.ApprovalID == input.ID {
		authors = append(authors, owner.ActorID)
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
