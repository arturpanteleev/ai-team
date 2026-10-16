package web

import (
	"net/http"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

// viewerActions answers "which of the task's allowed_actions may THIS
// principal perform". allowed_actions says what the task state offers to
// anyone; the handlers then authorize by the stage function (take, submit,
// bypass, confirm), the system right (override) or the approval's required
// roles (delivery plan). The same checks run here, so a client can hide a
// button the server would answer with 403. The list is additive: it never
// adds an action that is not in allowed_actions. The second result names the
// reason for denials that are not a plain missing role (today only
// self_approval_forbidden), so a client does not infer it from absence.
func (s *Server) viewerActions(r *http.Request, state *lifecycle.State, approvals []approval.PendingApproval, allowed []string) ([]string, map[string]string) {
	result := []string{}
	var reasons map[string]string
	if state == nil || state.NextStage == "" || len(allowed) == 0 {
		return result, reasons
	}
	session, ok := s.requestSession(r)
	if !ok || session.Principal.ActorID == "" || s.authenticator == nil {
		return result, reasons
	}
	principal := session.Principal
	var pending *approval.PendingApproval
	for index := range approvals {
		if approvals[index].ID == state.PendingApprovalID && approvals[index].Status == approval.StatusPending {
			pending = &approvals[index]
			break
		}
	}
	templates := s.newTaskTemplateLookup()
	stageAllowed := func(stageID string, action cloudidentity.StageAction) bool {
		stage, err := templates.stage(state.RunID, stageID)
		if err != nil {
			return false
		}
		role, err := cloudidentity.AuthorizeStageAction(principal, action, stageFunctions(stage))
		if err != nil {
			return false
		}
		return cloudidentity.Authorize(principal, cloudidentity.PermissionDecision, role) == nil
	}
	// A transition confirmation is about the stage that was just completed
	// (from_stage), not the one the run moves to.
	confirmed := state.NextStage
	if pending != nil && pending.FromStage != "" {
		confirmed = pending.FromStage
	}
	for _, action := range allowed {
		permitted := false
		switch action {
		case "take", "run-agent", "executor", "gate-retry":
			permitted = stageAllowed(state.NextStage, cloudidentity.StageActionTake)
		case "results", "grill/answer", "grill/new-version", "grill/done":
			permitted = stageAllowed(state.NextStage, cloudidentity.StageActionSubmit)
		case "skip":
			permitted = stageAllowed(state.NextStage, cloudidentity.StageActionBypass)
		case "confirm":
			permitted = stageAllowed(confirmed, cloudidentity.StageActionConfirm)
			if permitted && pending != nil && isStageConfirmation(*pending) &&
				s.selfApprovalViolation(state.RunID, *pending, principal.ActorID) != nil {
				permitted = false
				if reasons == nil {
					reasons = map[string]string{}
				}
				reasons[action] = "self_approval_forbidden"
			}
		case "return":
			permitted = stageAllowed(confirmed, cloudidentity.StageActionConfirm)
			if permitted && pending != nil && isStageConfirmation(*pending) &&
				s.selfApprovalViolation(state.RunID, *pending, principal.ActorID) != nil {
				permitted = false
				if reasons == nil {
					reasons = map[string]string{}
				}
				reasons[action] = "self_approval_forbidden"
			}
		case "override":
			permitted = cloudidentity.Authorize(principal, cloudidentity.PermissionGateOverride, "") == nil
		case "plan/approve":
			permitted = pending != nil && planApprovalRoleAvailable(principal, *pending)
		}
		if permitted {
			result = append(result, action)
		}
	}
	return result, reasons
}

// planApprovalRoleAvailable mirrors handleTaskPlanApprove: the actor needs a
// required role of the plan that it has not voted with yet.
func planApprovalRoleAvailable(principal cloudidentity.Principal, pending approval.PendingApproval) bool {
	voted := map[string]bool{}
	for _, prior := range pending.Decisions {
		if prior.ActorID == principal.ActorID {
			voted[prior.ActorRole] = true
		}
	}
	for _, required := range pending.RequiredRoles {
		candidate := canonicalStageRole(required)
		if voted[required] || !principal.Has(candidate) {
			continue
		}
		return cloudidentity.Authorize(principal, cloudidentity.PermissionDecision, candidate) == nil
	}
	return false
}
