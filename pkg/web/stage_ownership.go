package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/go-chi/chi/v5"
)

const humanInputTrigger = "human_input"

type takeStageResponse struct {
	Owner   store.StageOwner `json:"owner"`
	Changed bool             `json:"changed"`
}

func (s *Server) handleTakeStage(w http.ResponseWriter, r *http.Request) {
	runID, stageID := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID")
	if !safeIdentity(runID) || !safeIdentity(stageID) {
		http.Error(w, "invalid run or stage id", http.StatusBadRequest)
		return
	}
	if err := requireEmptyCommand(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetPipelineRunByRunID(runID); err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	stateStore, err := lifecycle.NewStore(s.targetDir)
	if err != nil {
		http.Error(w, "stage state unavailable", http.StatusInternalServerError)
		return
	}
	state, err := stateStore.Load(runID)
	if err != nil || state.Phase != lifecycle.PhaseWaiting || state.NextStage != stageID {
		http.Error(w, "stage is not the current waiting stage", http.StatusConflict)
		return
	}
	if s.controller == nil {
		http.Error(w, "run controller unavailable", http.StatusServiceUnavailable)
		return
	}
	approvals, err := s.controller.Approvals(runID)
	if err != nil {
		http.Error(w, "human stage approval unavailable", http.StatusInternalServerError)
		return
	}
	pending := pendingHumanInputForStage(approvals, stageID, state.PendingApprovalID)
	if pending == nil {
		http.Error(w, "stage has no pending human input", http.StatusConflict)
		return
	}
	session, ok := s.requestSession(r)
	if !ok || session.Principal.ActorID == "" {
		http.Error(w, "web session unavailable", http.StatusUnauthorized)
		return
	}
	rawRole, authorizationRole, ok := matchingApprovalRole(session.Principal, pending.RequiredRoles)
	if !ok {
		http.Error(w, "участник не имеет назначенной роли этапа", http.StatusForbidden)
		return
	}
	if err := s.authorize(r, cloudidentity.PermissionDecision, authorizationRole); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	owner, changed, err := s.store.TakeStage(runID, stageID, state.PendingApprovalID, session.Principal.ActorID, rawRole, time.Now().UTC())
	if err != nil {
		http.Error(w, "stage ownership could not be recorded", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, takeStageResponse{Owner: owner, Changed: changed})
}

func pendingHumanInputForStage(values []approval.PendingApproval, stageID, approvalID string) *approval.PendingApproval {
	for index := range values {
		value := values[index]
		if value.ID != approvalID || value.Kind != approval.KindInput || value.Status != approval.StatusPending ||
			value.Trigger != humanInputTrigger || value.FromStage != stageID || value.ToStage != stageID {
			continue
		}
		var payload approval.InputPayload
		if json.Unmarshal(value.Payload, &payload) != nil || payload.Kind != string(approval.KindInput) || payload.StageID != stageID {
			continue
		}
		return &value
	}
	return nil
}

func matchingApprovalRole(principal cloudidentity.Principal, required []string) (string, cloudidentity.Role, bool) {
	for _, roleName := range required {
		role := canonicalStageRole(roleName)
		if principal.Has(role) {
			return roleName, role, true
		}
	}
	return "", "", false
}

func canonicalStageRole(roleName string) cloudidentity.Role {
	switch strings.ToLower(strings.TrimSpace(roleName)) {
	case "bo", "business_owner", "po":
		return cloudidentity.RoleProductOwner
	case "deployer":
		return cloudidentity.RoleReleaseManager
	default:
		return cloudidentity.Role(roleName)
	}
}
