package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

type stageExecutorCommand struct {
	StageID  string `json:"stage_id"`
	Executor string `json:"executor"`
}

func (s *Server) handleChangeStageExecutor(w http.ResponseWriter, r *http.Request) {
	var command stageExecutorCommand
	if err := decodeCommand(w, r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	command.StageID = strings.TrimSpace(command.StageID)
	command.Executor = strings.TrimSpace(command.Executor)
	if command.StageID == "" || (command.Executor != "human" && command.Executor != "agent") {
		http.Error(w, "stage_id и executor (human|agent) обязательны", http.StatusBadRequest)
		return
	}
	if s.controller == nil {
		http.Error(w, "web control plane не настроен", http.StatusServiceUnavailable)
		return
	}
	runID := chi.URLParam(r, "runID")
	stateStore, err := lifecycle.NewStore(s.targetDir)
	if err != nil {
		http.Error(w, "lifecycle state недоступен", http.StatusServiceUnavailable)
		return
	}
	state, err := stateStore.Load(runID)
	if err != nil {
		http.Error(w, "run lifecycle state не найден", http.StatusNotFound)
		return
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" || state.NextStage != command.StageID {
		http.Error(w, "executor можно менять только у текущего ready stage с ожидающим approval", http.StatusConflict)
		return
	}
	stage, err := s.pinnedStage(runID, command.StageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if command.Executor == "agent" && stage.Agent == "" {
		http.Error(w, "у stage нет привязанного агента", http.StatusConflict)
		return
	}
	approvals, err := s.controller.Approvals(runID)
	if err != nil {
		http.Error(w, "approval недоступен", http.StatusServiceUnavailable)
		return
	}
	readyApproval := false
	for _, value := range approvals {
		if value.ID != state.PendingApprovalID || value.RunID != runID || value.Status != "pending" {
			continue
		}
		for _, target := range value.Targets {
			if target == command.StageID {
				readyApproval = true
				break
			}
		}
	}
	if !readyApproval {
		http.Error(w, "approval больше не открывает этот stage", http.StatusConflict)
		return
	}
	role := stageFunctionRole(stage.Function)
	if err := s.authorize(r, cloudidentity.PermissionDecision, role); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	session, _ := s.requestSession(r)
	actorID := session.Principal.ActorID
	changedAt := time.Now().UTC()
	if state.ExecutorOverrides == nil {
		state.ExecutorOverrides = make(map[string]lifecycle.ExecutorOverride)
	}
	previousState := state
	previousExecutor := stage.Executor
	if previous, ok := state.ExecutorOverrides[command.StageID]; ok && previous.ApprovalID == state.PendingApprovalID {
		previousExecutor = previous.Executor
	}
	state.ExecutorOverrides[command.StageID] = lifecycle.ExecutorOverride{
		Executor: command.Executor, PreviousExecutor: previousExecutor, ActorID: actorID,
		ApprovalID: state.PendingApprovalID, VisitID: state.PendingApprovalID, ChangedAt: changedAt,
	}
	if err := stateStore.Save(previousState, state); err != nil {
		http.Error(w, "не удалось сохранить executor stage", http.StatusConflict)
		return
	}
	if err := s.appendRunEventRequired(runID, "executor_changed", changedAt, map[string]any{
		"stage_id": command.StageID, "from_executor": previousExecutor, "to_executor": command.Executor,
		"actor_id": actorID, "approval_id": state.PendingApprovalID,
	}); err != nil {
		http.Error(w, "изменение сохранено, но событие executor_changed не записано", http.StatusServiceUnavailable)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"run_id": runID, "stage_id": command.StageID, "executor": command.Executor,
		"approval_id": state.PendingApprovalID, "changed_at": changedAt,
	})
}

func (s *Server) pinnedStage(runID, stageID string) (config.TemplateStage, error) {
	store, err := config.NewTemplateStore(s.targetDir)
	if err != nil {
		return config.TemplateStage{}, errors.New("pinned template store недоступен")
	}
	data, _, found, err := store.ReadPinnedRun(runID)
	if err != nil || !found {
		return config.TemplateStage{}, errors.New("у run нет проверяемого pinned template")
	}
	template, err := config.ParseYAML(data)
	if err != nil {
		return config.TemplateStage{}, errors.New("pinned template повреждён")
	}
	for _, stage := range template.Stages {
		if stage.ID == stageID {
			return stage, nil
		}
	}
	return config.TemplateStage{}, errors.New("stage отсутствует в pinned template")
}

func stageFunctionRole(function string) cloudidentity.Role {
	switch strings.TrimSpace(strings.ToLower(function)) {
	case "bo", "po", "business_owner", "product_owner":
		return cloudidentity.RoleProductOwner
	case "architect":
		return cloudidentity.RoleArchitect
	case "developer":
		return cloudidentity.RoleDeveloper
	case "reviewer":
		return cloudidentity.RoleReviewer
	case "qa":
		return cloudidentity.RoleQA
	case "deployer", "release_manager":
		return cloudidentity.RoleReleaseManager
	default:
		return ""
	}
}
