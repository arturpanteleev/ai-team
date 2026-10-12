package web

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/go-chi/chi/v5"
)

// Optional action interfaces let the REST surface land independently from the
// engine changes that own executor switching, agent dispatch, check overrides,
// skips, returns, grill turns, and delivery-plan continuation.
type StageExecutorController interface {
	ChangeStageExecutor(runID, stageID, executor, actorID string) error
}

// TaskCreateController owns the new task metadata and mode semantics. Legacy
// controllers continue to support the original feature/task request shape.
type TaskCreateController interface {
	StartTask(ctx context.Context, feature, task, title, idea, mode string, externalLinks []string, admit func(runID string) error) (string, error)
}

type StageAgentController interface {
	RunStageAgent(context.Context, string, string, string, bool) error
}

type StageOverrideController interface {
	OverrideStageCheck(context.Context, string, string, string, string, string) error
}

type StageSkipController interface {
	SkipStage(context.Context, string, string, string, string, string) error
}

type StageReturnController interface {
	ReturnStage(context.Context, string, string, string, string, string, string) error
}

type StageGrillController interface {
	AnswerStageGrill(context.Context, string, string, string, string, string) error
	SubmitStageGrillVersion(context.Context, string, string, string, string, string) error
	CompleteStageGrill(context.Context, string, string, string, string) error
}

type taskStageActionCommand struct {
	Executor string `json:"executor,omitempty"`
	Action   string `json:"action,omitempty"`
	Reason   string `json:"reason,omitempty"`
	ToStage  string `json:"to_stage,omitempty"`
	Answer   string `json:"answer,omitempty"`
	PlanHash string `json:"plan_hash,omitempty"`
}

type taskRunListItem struct {
	ID              int64      `json:"id"`
	RunID           string     `json:"run_id"`
	Feature         string     `json:"feature"`
	Task            string     `json:"task,omitempty"`
	Status          string     `json:"status"`
	Phase           string     `json:"phase,omitempty"`
	CurrentStage    string     `json:"current_stage,omitempty"`
	CurrentFunction string     `json:"current_function,omitempty"`
	Executor        string     `json:"executor,omitempty"`
	ExecutorAgent   string     `json:"executor_agent,omitempty"`
	Mode            string     `json:"mode,omitempty"`
	AllowedActions  []string   `json:"allowed_actions"`
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	StageEnteredAt  *time.Time `json:"stage_entered_at,omitempty"`
	Warnings        int        `json:"warnings"`
	ExternalLinks   []string   `json:"external_links,omitempty"`
}

type taskRunDetails struct {
	Run            *store.PipelineRun                  `json:"run"`
	Task           string                              `json:"task,omitempty"`
	Mode           string                              `json:"mode,omitempty"`
	ExternalLinks  []string                            `json:"external_links,omitempty"`
	Template       *templateValidation                 `json:"template,omitempty"`
	State          *lifecycle.State                    `json:"state,omitempty"`
	Stages         []store.Stage                       `json:"stages"`
	Approvals      []approval.PendingApproval          `json:"approvals"`
	StageOwners    map[string]store.StageOwner         `json:"stage_owners"`
	Results        map[string][]humanartifact.Revision `json:"results"`
	AllowedActions []string                            `json:"allowed_actions"`
	Delivery       webDeliveryProjection               `json:"delivery"`
}

func (s *Server) handleListTemplates(w http.ResponseWriter, _ *http.Request) {
	templateStore, err := s.templateStore()
	if err != nil {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "template store unavailable", "template_unavailable")
		return
	}
	data, version, err := templateStore.ReadCurrent()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSONResponse(w, http.StatusOK, map[string]any{"templates": []any{}, "versions": []config.TemplateVersion{}})
		} else {
			writeJSONAPIError(w, http.StatusServiceUnavailable, "project template unavailable", "template_unavailable")
		}
		return
	}
	cfg, graph, parseErr := s.parseTemplateYAML(data)
	versions, listErr := templateStore.ListVersions()
	if listErr != nil {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "template versions unavailable", "template_unavailable")
		return
	}
	template := templateValidation{YAML: string(data), Version: version, Valid: parseErr == nil}
	if parseErr == nil {
		template = describeTemplate(template, cfg, graph)
	} else {
		template.Diagnostic = parseErr.Error()
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"templates": []templateValidation{template}, "active_template": cfgTemplateID(cfg), "versions": versions,
	})
}

func cfgTemplateID(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Template
}

func (s *Server) handleListTaskRuns(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := pagination(r)
	if err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_pagination")
		return
	}
	filter := map[string]string{
		"status":   strings.TrimSpace(r.URL.Query().Get("status")),
		"stage":    strings.TrimSpace(r.URL.Query().Get("stage")),
		"function": strings.TrimSpace(r.URL.Query().Get("function")),
		"mode":     strings.TrimSpace(r.URL.Query().Get("mode")),
	}
	all, err := s.listTaskRuns()
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "run list unavailable", "run_list_unavailable")
		return
	}
	filtered := make([]taskRunListItem, 0, len(all))
	for _, item := range all {
		if filter["status"] != "" && item.Status != filter["status"] && item.Phase != filter["status"] {
			continue
		}
		if filter["stage"] != "" && item.CurrentStage != filter["stage"] {
			continue
		}
		if filter["function"] != "" && item.CurrentFunction != filter["function"] {
			continue
		}
		if filter["mode"] != "" && item.Mode != filter["mode"] {
			continue
		}
		filtered = append(filtered, item)
	}
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := filtered[offset:end]
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	writeJSONResponse(w, http.StatusOK, map[string]any{"runs": page, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) listTaskRuns() ([]taskRunListItem, error) {
	var runs []store.PipelineRun
	for offset := 0; ; offset += 100 {
		page, err := s.store.GetPipelineRunsPage(100, offset)
		if err != nil {
			return nil, err
		}
		runs = append(runs, page...)
		if len(page) < 100 {
			break
		}
	}
	items := make([]taskRunListItem, 0, len(runs))
	for _, run := range runs {
		item := taskRunListItem{ID: run.ID, RunID: run.RunID, Feature: run.Feature, Status: run.Status,
			StartedAt: run.StartedAt, CompletedAt: run.CompletedAt, AllowedActions: []string{}}
		var snapshot startRunCommand
		if json.Unmarshal([]byte(run.ConfigSnapshot), &snapshot) == nil {
			item.Task, item.Mode = snapshot.Task, snapshot.Mode
			item.ExternalLinks = snapshot.ExternalLinks
		}
		if stateStore, err := lifecycle.NewStore(s.targetDir); err == nil {
			if state, loadErr := stateStore.Load(run.RunID); loadErr == nil {
				item.Phase, item.CurrentStage = string(state.Phase), state.NextStage
				enteredAt := state.UpdatedAt
				item.StageEnteredAt = &enteredAt
				item.AllowedActions = s.taskAllowedActions(run.RunID, state.NextStage, state)
				if stage, stageErr := s.pinnedTaskStage(run.RunID, state.NextStage); stageErr == nil {
					item.CurrentFunction = stage.Function
					item.Executor = stage.Executor
					item.ExecutorAgent = stage.Agent
				}
			}
		}
		items = append(items, item)
	}
	runIDs := make([]string, 0, len(items))
	for _, item := range items {
		runIDs = append(runIDs, item.RunID)
	}
	warnings, warnErr := s.store.CountRunWarningEvents(runIDs)
	if warnErr == nil {
		for index := range items {
			items[index].Warnings = warnings[items[index].RunID]
		}
	}
	return items, nil
}

func (s *Server) handleGetTaskRun(w http.ResponseWriter, r *http.Request) {
	details, status, err := s.loadTaskRunDetails(chi.URLParam(r, "runID"))
	if err != nil {
		writeJSONAPIError(w, status, err.Error(), apiErrorCode(status))
		return
	}
	writeJSONResponse(w, http.StatusOK, details)
}

func (s *Server) loadTaskRunDetails(runID string) (taskRunDetails, int, error) {
	if !safeIdentity(runID) {
		return taskRunDetails{}, http.StatusBadRequest, errors.New("invalid run id")
	}
	run, err := s.store.GetPipelineRunByRunID(runID)
	if err != nil || run == nil {
		return taskRunDetails{}, http.StatusNotFound, errors.New("run not found")
	}
	stages, err := s.store.GetStagesByPipelineRunID(run.ID)
	if err != nil {
		return taskRunDetails{}, http.StatusInternalServerError, errors.New("stage history unavailable")
	}
	details := taskRunDetails{Run: run, Stages: stages, Approvals: []approval.PendingApproval{},
		StageOwners: map[string]store.StageOwner{}, Results: map[string][]humanartifact.Revision{}, AllowedActions: []string{}}
	var taskMetadata startRunCommand
	if json.Unmarshal([]byte(run.ConfigSnapshot), &taskMetadata) == nil {
		details.Task, details.Mode, details.ExternalLinks = taskMetadata.Task, taskMetadata.Mode, taskMetadata.ExternalLinks
	}
	if stateStore, stateErr := lifecycle.NewStore(s.targetDir); stateErr == nil {
		if state, loadErr := stateStore.Load(runID); loadErr == nil {
			details.State = &state
			if details.Task == "" {
				details.Task = state.Task
			}
			details.AllowedActions = s.taskAllowedActions(runID, state.NextStage, state)
		}
	}
	owners, ownerErr := s.store.GetStageOwners(runID)
	if ownerErr == nil {
		details.StageOwners = owners
	}
	if s.controller != nil {
		approvals, approvalErr := s.controller.Approvals(runID)
		if approvalErr != nil {
			return taskRunDetails{}, http.StatusInternalServerError, errors.New("approval history unavailable")
		}
		details.Approvals = approvals
	}
	if data, version, found, pinErr := s.pinnedTaskTemplate(runID); pinErr == nil && found {
		if cfg, parseErr := config.ParseYAML(data); parseErr == nil {
			if graph, graphErr := cfg.CompiledGraph(); graphErr == nil {
				template := describeTemplate(templateValidation{Valid: true, Version: version}, cfg, graph)
				details.Template = &template
			}
			for _, stage := range cfg.Stages {
				for _, resultKind := range []string{stage.Result} {
					path := humanStageResultPath(stage.ID, resultKind)
					if path == "" {
						continue
					}
					if revisions, listErr := s.humanArtifacts.List(runID, path); listErr == nil {
						details.Results[stage.ID] = append(details.Results[stage.ID], revisions...)
					}
				}
			}
		}
	}
	details.Delivery = s.deliveryProjection(run, stages, s.targetDir)
	return details, http.StatusOK, nil
}

func (s *Server) handleGetTaskStageResult(w http.ResponseWriter, r *http.Request) {
	runID, stageID, version := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID"), chi.URLParam(r, "version")
	if !safeIdentity(runID) || !safeIdentity(stageID) || !safeIdentity(version) {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid run, stage, or result version", "bad_identity")
		return
	}
	if _, err := s.store.GetPipelineRunByRunID(runID); err != nil {
		writeJSONAPIError(w, http.StatusNotFound, "run not found", "not_found")
		return
	}
	cfg, found, err := s.pinnedTaskConfig(runID)
	if err != nil || !found {
		writeJSONAPIError(w, http.StatusConflict, "run has no valid pinned template", "template_pin_unavailable")
		return
	}
	var resultKind string
	for _, stage := range cfg.Stages {
		if stage.ID == stageID {
			resultKind = stage.Result
			break
		}
	}
	path := humanStageResultPath(stageID, resultKind)
	if path == "" {
		writeJSONAPIError(w, http.StatusNotFound, "stage result not found", "not_found")
		return
	}
	revisions, err := s.humanArtifacts.List(runID, path)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "result history unavailable", "result_unavailable")
		return
	}
	for _, revision := range revisions {
		if revision.ID == version || strconv.Itoa(revision.Revision) == version {
			writeJSONResponse(w, http.StatusOK, map[string]any{"result": revision})
			return
		}
	}
	writeJSONAPIError(w, http.StatusNotFound, "result version not found", "not_found")
}

func humanStageResultPath(stageID, resultKind string) string {
	switch resultKind {
	case "md":
		return "stages/" + stageID + "/result.md"
	case "link":
		return "stages/" + stageID + "/result.link"
	case "approve":
		return "stages/" + stageID + "/result.txt"
	default:
		return ""
	}
}

func (s *Server) pinnedTaskTemplate(runID string) ([]byte, string, bool, error) {
	templateStore, err := s.templateStore()
	if err != nil {
		return nil, "", false, err
	}
	return templateStore.ReadPinnedRun(runID)
}

func (s *Server) pinnedTaskConfig(runID string) (*config.Config, bool, error) {
	data, _, found, err := s.pinnedTaskTemplate(runID)
	if err != nil || !found {
		return nil, found, err
	}
	cfg, err := config.ParseYAML(data)
	return cfg, true, err
}

func (s *Server) pinnedTaskStage(runID, stageID string) (config.TemplateStage, error) {
	cfg, found, err := s.pinnedTaskConfig(runID)
	if err != nil || !found {
		return config.TemplateStage{}, errors.New("pinned template unavailable")
	}
	for _, stage := range cfg.Stages {
		if stage.ID == stageID {
			return stage, nil
		}
	}
	return config.TemplateStage{}, fmt.Errorf("stage %q not found in pinned template", stageID)
}

func (s *Server) taskAllowedActions(runID, stageID string, state lifecycle.State) []string {
	actions := make([]string, 0, 6)
	if stageID == "" {
		return actions
	}
	stage, stageErr := s.pinnedTaskStage(runID, stageID)
	if state.Phase == lifecycle.PhaseResumable && stageErr == nil && stage.Agent != "" {
		actions = append(actions, "run-agent")
	}
	if state.Phase == lifecycle.PhaseWaiting && state.PendingApprovalID != "" && s.controller != nil {
		if approvals, err := s.controller.Approvals(runID); err == nil {
			for _, pending := range approvals {
				if pending.ID != state.PendingApprovalID || pending.Status != approval.StatusPending {
					continue
				}
				switch {
				case pending.Kind == approval.KindInput && pending.Trigger == humanInputTrigger:
					actions = append(actions, "take", "results")
					if stageErr == nil && stage.Agent != "" {
						actions = append(actions, "executor", "run-agent")
					}
				case pending.Kind == approval.KindGrillQuestions:
					actions = append(actions, "grill/answer", "grill/new-version", "grill/done")
				case pending.Trigger == "delivery_plan":
					actions = append(actions, "plan/approve")
				default:
					if actionAllowed(pending.Actions, "approve") {
						actions = append(actions, "confirm")
					}
					if actionAllowed(pending.Actions, "override_approve") {
						actions = append(actions, "override")
					}
					if stageErr == nil && stage.Agent != "" && actionAllowed(pending.Actions, "retry") {
						actions = append(actions, "run-agent")
					}
				}
				break
			}
		}
	}
	if stageErr == nil && stage.Skippable && (state.Phase == lifecycle.PhaseWaiting || state.Phase == lifecycle.PhaseResumable) {
		actions = append(actions, "skip")
	}
	if stageErr == nil && state.Phase == lifecycle.PhaseWaiting {
		cfg, found, err := s.pinnedTaskConfig(runID)
		if err == nil && found {
			for _, route := range cfg.Returns {
				if route.From == stageID {
					actions = append(actions, "return")
					break
				}
			}
		}
	}
	return uniqueStrings(actions)
}

func actionAllowed(actions []string, wanted string) bool {
	for _, action := range actions {
		if action == wanted {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func (s *Server) taskConflict(w http.ResponseWriter, runID, stageID, message string) {
	allowed := []string{}
	if stateStore, err := lifecycle.NewStore(s.targetDir); err == nil {
		if state, loadErr := stateStore.Load(runID); loadErr == nil {
			if stageID == "" {
				stageID = state.NextStage
			}
			allowed = s.taskAllowedActions(runID, stageID, state)
		}
	}
	writeJSONResponse(w, http.StatusConflict, struct {
		Error          string   `json:"error"`
		Code           string   `json:"code"`
		AllowedActions []string `json:"allowed_actions"`
	}{Error: message, Code: "invalid_state", AllowedActions: allowed})
}

// taskActionActor authorizes a stage action by the pinned stage functions
// (B-82) and returns the trusted actor ID plus the approval actor role. Take,
// submit and bypass belong to the executor function; confirm/return belong to
// approver_function when configured. A missing function assignment fails
// closed with 403.
func (s *Server) taskActionActor(w http.ResponseWriter, r *http.Request, stage config.TemplateStage, action cloudidentity.StageAction) (string, string, bool) {
	session, ok := s.requestSession(r)
	if !ok || session.Principal.ActorID == "" {
		writeJSONAPIError(w, http.StatusUnauthorized, "web session unavailable", "unauthorized")
		return "", "", false
	}
	role, err := cloudidentity.AuthorizeStageAction(session.Principal, action, stageFunctions(stage))
	if err == nil {
		err = s.authorize(r, cloudidentity.PermissionDecision, role)
	}
	if err != nil {
		writeJSONAPIError(w, http.StatusForbidden, err.Error(), "forbidden")
		return "", "", false
	}
	actorRole := stage.Function
	if action == cloudidentity.StageActionConfirm && stage.ApproverFunction != "" {
		// Generated stage approvals carry the canonical approver role.
		actorRole = string(role)
	}
	return session.Principal.ActorID, actorRole, true
}

func stageFunctions(stage config.TemplateStage) cloudidentity.StageFunctions {
	return cloudidentity.StageFunctions{Executor: stage.Function, Approver: stage.ApproverFunction}
}

func (s *Server) taskStagePreconditions(w http.ResponseWriter, runID, stageID string) (lifecycle.State, config.TemplateStage, bool) {
	if !safeIdentity(runID) || !safeIdentity(stageID) {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid run or stage id", "bad_identity")
		return lifecycle.State{}, config.TemplateStage{}, false
	}
	stateStore, err := lifecycle.NewStore(s.targetDir)
	if err != nil {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "lifecycle state unavailable", "lifecycle_unavailable")
		return lifecycle.State{}, config.TemplateStage{}, false
	}
	state, err := stateStore.Load(runID)
	if err != nil {
		writeJSONAPIError(w, http.StatusNotFound, "run lifecycle state not found", "not_found")
		return lifecycle.State{}, config.TemplateStage{}, false
	}
	if state.NextStage != stageID {
		s.taskConflict(w, runID, stageID, "stage is not current")
		return lifecycle.State{}, config.TemplateStage{}, false
	}
	stage, err := s.pinnedTaskStage(runID, stageID)
	if err != nil {
		writeJSONAPIError(w, http.StatusConflict, "pinned stage unavailable", "template_pin_unavailable")
		return lifecycle.State{}, config.TemplateStage{}, false
	}
	return state, stage, true
}

func (s *Server) handleTaskStageExecutor(w http.ResponseWriter, r *http.Request) {
	runID, stageID := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID")
	state, stage, ok := s.taskStagePreconditions(w, runID, stageID)
	if !ok {
		return
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" {
		s.taskConflict(w, runID, stageID, "executor can only change while the current stage is ready")
		return
	}
	var command taskStageActionCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if command.Executor != "human" && command.Executor != "agent" {
		writeJSONAPIError(w, http.StatusBadRequest, "executor must be human or agent", "bad_executor")
		return
	}
	if command.Executor == "agent" && stage.Agent == "" {
		s.taskConflict(w, runID, stageID, "stage has no configured agent")
		return
	}
	actorID, _, ok := s.taskActionActor(w, r, stage, cloudidentity.StageActionTake)
	if !ok {
		return
	}
	controller, ok := s.controller.(StageExecutorController)
	if !ok {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "stage executor action is not connected to the task controller", "controller_action_unavailable")
		return
	}
	// Record the switching actor before the controller changes the executor so
	// the switch always counts as stage authorship for forbid_self_approval.
	if err := s.appendRunEventRequired(runID, "executor_changed", time.Now().UTC(), map[string]any{
		"stage_id": stageID, "to_executor": command.Executor, "actor_id": actorID,
		"approval_id": state.PendingApprovalID,
	}); err != nil {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "executor change could not be recorded", "event_unavailable")
		return
	}
	if err := controller.ChangeStageExecutor(runID, stageID, command.Executor, actorID); err != nil {
		s.taskConflict(w, runID, stageID, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"run_id": runID, "stage_id": stageID, "executor": command.Executor, "actor_id": actorID})
}

func (s *Server) handleTaskStageRunAgent(w http.ResponseWriter, r *http.Request) {
	runID, stageID := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID")
	state, stage, ok := s.taskStagePreconditions(w, runID, stageID)
	if !ok {
		return
	}
	if state.Phase != lifecycle.PhaseWaiting && state.Phase != lifecycle.PhaseResumable {
		s.taskConflict(w, runID, stageID, "agent run requires a ready or resumable stage")
		return
	}
	if stage.Agent == "" {
		s.taskConflict(w, runID, stageID, "stage has no configured agent")
		return
	}
	var command taskStageActionCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if command.Action != "" && command.Action != "run" && command.Action != "refine" {
		writeJSONAPIError(w, http.StatusBadRequest, "action must be run or refine", "bad_action")
		return
	}
	actorID, _, ok := s.taskActionActor(w, r, stage, cloudidentity.StageActionTake)
	if !ok {
		return
	}
	controller, ok := s.controller.(StageAgentController)
	if !ok {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "agent dispatch is not connected to the task controller", "controller_action_unavailable")
		return
	}
	// Record the launching actor before dispatch: the launch counts as stage
	// authorship for forbid_self_approval, so an unrecorded launch must not run.
	if err := s.appendRunEventRequired(runID, stageAgentRequestedEvent, time.Now().UTC(), map[string]any{
		"stage_id": stageID, "actor_id": actorID, "refine": command.Action == "refine",
	}); err != nil {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "agent launch could not be recorded", "event_unavailable")
		return
	}
	if err := controller.RunStageAgent(r.Context(), runID, stageID, actorID, command.Action == "refine"); err != nil {
		s.taskConflict(w, runID, stageID, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "stage_id": stageID, "action": command.Action, "actor_id": actorID})
}

func (s *Server) handleTaskStageOverride(w http.ResponseWriter, r *http.Request) {
	runID, stageID := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID")
	state, stage, ok := s.taskStagePreconditions(w, runID, stageID)
	if !ok {
		return
	}
	if state.Phase != lifecycle.PhaseWaiting {
		s.taskConflict(w, runID, stageID, "check override requires a failed check awaiting a decision")
		return
	}
	var command taskStageActionCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if strings.TrimSpace(command.Reason) == "" {
		writeJSONAPIError(w, http.StatusBadRequest, "override reason is required", "reason_required")
		return
	}
	actorID, actorRole, ok := s.taskActionActor(w, r, stage, cloudidentity.StageActionBypass)
	if !ok {
		return
	}
	controller, ok := s.controller.(StageOverrideController)
	if !ok {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "check override is not connected to the task controller", "controller_action_unavailable")
		return
	}
	if err := controller.OverrideStageCheck(r.Context(), runID, stageID, command.Reason, actorID, actorRole); err != nil {
		s.taskConflict(w, runID, stageID, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "stage_id": stageID, "actor_id": actorID})
}

func (s *Server) handleTaskStageConfirm(w http.ResponseWriter, r *http.Request) {
	runID, stageID := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID")
	state, stage, ok := s.taskStagePreconditions(w, runID, stageID)
	if !ok {
		return
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" || s.controller == nil {
		s.taskConflict(w, runID, stageID, "stage has no confirmation pending")
		return
	}
	if err := requireEmptyCommand(w, r); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	values, err := s.controller.Approvals(runID)
	if err != nil {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "approval state unavailable", "approval_unavailable")
		return
	}
	var pending *approval.PendingApproval
	for index := range values {
		if values[index].ID == state.PendingApprovalID && values[index].Status == approval.StatusPending {
			pending = &values[index]
			break
		}
	}
	if pending == nil || pending.Kind == approval.KindInput || pending.Trigger == "delivery_plan" || !actionAllowed(pending.Actions, "approve") {
		s.taskConflict(w, runID, stageID, "current approval is not a stage confirmation")
		return
	}
	actorID, actorRole, authOK := s.taskActionActor(w, r, stage, cloudidentity.StageActionConfirm)
	if !authOK {
		return
	}
	if err := s.selfApprovalViolation(runID, *pending, actorID); err != nil {
		writeJSONAPIError(w, http.StatusForbidden, err.Error(), "self_approval_forbidden")
		return
	}
	revisions, err := s.confirmationArtifactRevisions(runID, pending)
	if err != nil {
		s.taskConflict(w, runID, stageID, err.Error())
		return
	}
	decision := approval.Decision{ActorID: actorID, ActorRole: actorRole, Action: "approve",
		SubjectHash: pending.SubjectHash, ArtifactRevisions: revisions, ControllerAuthenticated: true}
	resolved, err := s.controller.Decide(runID, pending.ID, decision)
	if err != nil {
		s.taskConflict(w, runID, stageID, err.Error())
		return
	}
	if resolved.Status != approval.StatusResolved {
		writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "stage_id": stageID,
			"approval": resolved, "actor_id": actorID})
		return
	}
	if err := s.controller.Resume(runID); err != nil {
		writeJSONAPIError(w, http.StatusConflict, "confirmation was recorded but run could not resume", "resume_failed")
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "stage_id": stageID, "approval_id": pending.ID, "actor_id": actorID})
}

func (s *Server) handleTaskStageSkip(w http.ResponseWriter, r *http.Request) {
	runID, stageID := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID")
	state, stage, ok := s.taskStagePreconditions(w, runID, stageID)
	if !ok {
		return
	}
	if state.Phase != lifecycle.PhaseWaiting && state.Phase != lifecycle.PhaseResumable || !stage.Skippable {
		s.taskConflict(w, runID, stageID, "stage cannot be skipped in its current state")
		return
	}
	var command taskStageActionCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if strings.TrimSpace(command.Reason) == "" {
		writeJSONAPIError(w, http.StatusBadRequest, "skip reason is required", "reason_required")
		return
	}
	actorID, actorRole, ok := s.taskActionActor(w, r, stage, cloudidentity.StageActionBypass)
	if !ok {
		return
	}
	controller, ok := s.controller.(StageSkipController)
	if !ok {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "skip action is not connected to the task controller", "controller_action_unavailable")
		return
	}
	if err := controller.SkipStage(r.Context(), runID, stageID, command.Reason, actorID, actorRole); err != nil {
		s.taskConflict(w, runID, stageID, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "stage_id": stageID, "actor_id": actorID})
}

func (s *Server) handleTaskStageReturn(w http.ResponseWriter, r *http.Request) {
	runID, stageID := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID")
	state, stage, ok := s.taskStagePreconditions(w, runID, stageID)
	if !ok {
		return
	}
	if state.Phase != lifecycle.PhaseWaiting {
		s.taskConflict(w, runID, stageID, "return requires a current waiting stage")
		return
	}
	var command taskStageActionCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if strings.TrimSpace(command.Reason) == "" || !s.taskReturnAllowed(runID, stageID, command.ToStage) {
		s.taskConflict(w, runID, stageID, "return target is not configured for this stage")
		return
	}
	actorID, actorRole, ok := s.taskActionActor(w, r, stage, cloudidentity.StageActionConfirm)
	if !ok {
		return
	}
	controller, ok := s.controller.(StageReturnController)
	if !ok {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "return action is not connected to the task controller", "controller_action_unavailable")
		return
	}
	if err := controller.ReturnStage(r.Context(), runID, stageID, command.ToStage, command.Reason, actorID, actorRole); err != nil {
		s.taskConflict(w, runID, stageID, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "from_stage": stageID, "to_stage": command.ToStage, "actor_id": actorID})
}

// confirmationArtifactRevisions mirrors the legacy quorum decision path for the
// task confirm endpoint. The first vote pins the exact artifact revisions that
// were reviewed (including an intentionally empty selection); later voters reuse
// that binding even if newer human revisions appeared while the approval stayed
// pending. A human revision appended after the first vote is therefore never
// silently adopted.
func (s *Server) confirmationArtifactRevisions(runID string, pending *approval.PendingApproval) (map[string]string, error) {
	if len(pending.Decisions) > 0 {
		pinned := pending.Decisions[0].ArtifactRevisions
		revisions := make(map[string]string, len(pinned))
		for path, revisionID := range pinned {
			revisions[path] = revisionID
		}
		return revisions, nil
	}
	attemptID, err := s.approvalAttemptID(runID, pending.ID)
	if err != nil {
		return nil, err
	}
	return s.latestAttemptRevisions(runID, attemptID)
}

func (s *Server) taskReturnAllowed(runID, from, to string) bool {
	cfg, found, err := s.pinnedTaskConfig(runID)
	if err != nil || !found {
		return false
	}
	for _, route := range cfg.Returns {
		if route.From == from && route.To == to {
			return true
		}
	}
	return false
}

func (s *Server) handleTaskGrillAnswer(w http.ResponseWriter, r *http.Request) {
	s.handleTaskGrillAction(w, r, "")
}

func (s *Server) handleTaskGrillNewVersion(w http.ResponseWriter, r *http.Request) {
	s.handleTaskGrillAction(w, r, approval.GrillActionNewVersion)
}

func (s *Server) handleTaskGrillAction(w http.ResponseWriter, r *http.Request, forcedAction string) {
	runID, stageID := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID")
	state, stage, ok := s.taskStagePreconditions(w, runID, stageID)
	if !ok {
		return
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" || !s.pendingGrillApproval(runID, state.PendingApprovalID) {
		s.taskConflict(w, runID, stageID, "grill answer requires a pending grill")
		return
	}
	var command taskStageActionCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid grill action", "bad_answer")
		return
	}
	if forcedAction != "" {
		if command.Action != "" && command.Action != forcedAction {
			writeJSONAPIError(w, http.StatusBadRequest, "action does not match grill route", "bad_answer")
			return
		}
		command.Action = forcedAction
	} else if command.Action == "" {
		command.Action = approval.GrillActionAnswer
	}
	if command.Action != approval.GrillActionAnswer && command.Action != approval.GrillActionNewVersion {
		writeJSONAPIError(w, http.StatusBadRequest, "action must be answer or new_version", "bad_answer")
		return
	}
	if command.Action == approval.GrillActionAnswer && strings.TrimSpace(command.Answer) == "" {
		writeJSONAPIError(w, http.StatusBadRequest, "answer is required", "bad_answer")
		return
	}
	actorID, actorRole, ok := s.taskActionActor(w, r, stage, cloudidentity.StageActionSubmit)
	if !ok {
		return
	}
	controller, ok := s.controller.(StageGrillController)
	if !ok {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "grill action is not connected to the task controller", "controller_action_unavailable")
		return
	}
	var actionErr error
	if command.Action == approval.GrillActionNewVersion {
		actionErr = controller.SubmitStageGrillVersion(r.Context(), runID, stageID, command.Answer, actorID, actorRole)
	} else {
		actionErr = controller.AnswerStageGrill(r.Context(), runID, stageID, command.Answer, actorID, actorRole)
	}
	if actionErr != nil {
		s.taskConflict(w, runID, stageID, actionErr.Error())
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "stage_id": stageID, "actor_id": actorID})
}

func (s *Server) handleTaskGrillDone(w http.ResponseWriter, r *http.Request) {
	runID, stageID := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID")
	state, stage, ok := s.taskStagePreconditions(w, runID, stageID)
	if !ok {
		return
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" || !s.pendingGrillApproval(runID, state.PendingApprovalID) {
		s.taskConflict(w, runID, stageID, "grill completion requires a pending grill")
		return
	}
	if err := requireEmptyCommand(w, r); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	actorID, actorRole, ok := s.taskActionActor(w, r, stage, cloudidentity.StageActionSubmit)
	if !ok {
		return
	}
	controller, ok := s.controller.(StageGrillController)
	if !ok {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "grill action is not connected to the task controller", "controller_action_unavailable")
		return
	}
	if err := controller.CompleteStageGrill(r.Context(), runID, stageID, actorID, actorRole); err != nil {
		s.taskConflict(w, runID, stageID, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "stage_id": stageID, "actor_id": actorID})
}

func (s *Server) pendingGrillApproval(runID, approvalID string) bool {
	if s.controller == nil {
		return false
	}
	values, err := s.controller.Approvals(runID)
	if err != nil {
		return false
	}
	for _, value := range values {
		if value.ID == approvalID {
			return value.Kind == approval.KindGrillQuestions && value.Trigger == approval.GrillTrigger && value.Status == approval.StatusPending
		}
	}
	return false
}

func (s *Server) handleTaskPlanApprove(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if !safeIdentity(runID) {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid run id", "bad_identity")
		return
	}
	if s.controller == nil {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "task controller unavailable", "controller_unavailable")
		return
	}
	var command taskStageActionCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if len(command.PlanHash) != 64 {
		writeJSONAPIError(w, http.StatusBadRequest, "plan_hash must be a SHA-256 digest", "bad_plan_hash")
		return
	}
	if decoded, err := hex.DecodeString(command.PlanHash); err != nil || len(decoded) != 32 {
		writeJSONAPIError(w, http.StatusBadRequest, "plan_hash must be a SHA-256 digest", "bad_plan_hash")
		return
	}
	values, err := s.controller.Approvals(runID)
	if err != nil {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "approval state unavailable", "approval_unavailable")
		return
	}
	var pending *approval.PendingApproval
	for index := range values {
		if values[index].Trigger == "delivery_plan" && values[index].SubjectHash == strings.ToLower(command.PlanHash) {
			pending = &values[index]
			break
		}
	}
	if pending == nil || pending.Status != approval.StatusPending || !actionAllowed(pending.Actions, "approve") {
		s.taskConflict(w, runID, "", "delivery plan is not awaiting this approval")
		return
	}
	session, ok := s.requestSession(r)
	if !ok || session.Principal.ActorID == "" {
		writeJSONAPIError(w, http.StatusUnauthorized, "web session unavailable", "unauthorized")
		return
	}
	votedRoles := map[string]bool{}
	for _, prior := range pending.Decisions {
		if prior.ActorID == session.Principal.ActorID {
			votedRoles[prior.ActorRole] = true
		}
	}
	roleName := ""
	var role cloudidentity.Role
	for _, requiredRole := range pending.RequiredRoles {
		candidate := canonicalStageRole(requiredRole)
		if votedRoles[requiredRole] || !session.Principal.Has(candidate) {
			continue
		}
		roleName, role = requiredRole, candidate
		break
	}
	if roleName == "" {
		writeJSONAPIError(w, http.StatusForbidden, "no unvoted delivery-plan role is assigned to this actor", "forbidden")
		return
	}
	if err := s.authorize(r, cloudidentity.PermissionDecision, role); err != nil {
		writeJSONAPIError(w, http.StatusForbidden, err.Error(), "forbidden")
		return
	}
	decision := approval.Decision{ActorID: session.Principal.ActorID, ActorRole: roleName, Action: "approve",
		SubjectHash: pending.SubjectHash, ControllerAuthenticated: true}
	resolved, err := s.controller.Decide(runID, pending.ID, decision)
	if err != nil {
		s.taskConflict(w, runID, "", err.Error())
		return
	}
	if resolved.Status != approval.StatusResolved {
		writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "approval": resolved,
			"plan_hash": pending.SubjectHash, "actor_id": session.Principal.ActorID})
		return
	}
	if err := s.controller.Resume(runID); err != nil {
		writeJSONAPIError(w, http.StatusConflict, "plan approval was recorded but run could not resume", "resume_failed")
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "plan_hash": pending.SubjectHash, "actor_id": session.Principal.ActorID})
}

func (s *Server) handleGetRunEvents(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if !safeIdentity(runID) {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid run id", "bad_identity")
		return
	}
	if _, err := s.store.GetPipelineRunByRunID(runID); err != nil {
		writeJSONAPIError(w, http.StatusNotFound, "run not found", "not_found")
		return
	}
	events, next, err := s.taskEventPage(runID, r)
	if err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_event_query")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"events": events, "next_cursor": next})
}

func (s *Server) handleGetGlobalEvents(w http.ResponseWriter, r *http.Request) {
	if runID := strings.TrimSpace(r.URL.Query().Get("run_id")); runID != "" {
		if !safeIdentity(runID) {
			writeJSONAPIError(w, http.StatusBadRequest, "invalid run id", "bad_identity")
			return
		}
	}
	events, next, err := s.taskEventPage(strings.TrimSpace(r.URL.Query().Get("run_id")), r)
	if err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_event_query")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"events": events, "next_cursor": next})
}

func (s *Server) taskEventPage(runID string, r *http.Request) ([]Event, int64, error) {
	cursor, limit := int64(0), 100
	var err error
	if raw := r.URL.Query().Get("after"); raw != "" {
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 0 {
			return nil, cursor, errors.New("after must be a non-negative event cursor")
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			return nil, cursor, errors.New("limit must be between 1 and 1000")
		}
	}
	descending := false
	switch order := r.URL.Query().Get("order"); order {
	case "", "asc":
	case "desc":
		descending = true
	default:
		return nil, cursor, errors.New("order must be asc or desc")
	}
	if raw := r.URL.Query().Get("before"); raw != "" {
		if !descending {
			return nil, cursor, errors.New("before requires order=desc")
		}
		if r.URL.Query().Get("after") != "" {
			return nil, cursor, errors.New("after and before cannot be combined")
		}
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 0 {
			return nil, cursor, errors.New("before must be a non-negative event cursor")
		}
	} else if descending && r.URL.Query().Get("after") != "" {
		return nil, cursor, errors.New("order=desc pages with before, not after")
	}
	warningsOnly := strings.EqualFold(r.URL.Query().Get("warnings"), "true")
	values := make([]Event, 0, limit)
	for len(values) < limit {
		batchSize := limit
		if warningsOnly {
			batchSize = 1000
		}
		var batch []store.Event
		switch {
		case descending:
			batch, err = s.store.GetEventsBefore(runID, cursor, batchSize)
		case runID == "":
			batch, err = s.store.GetEventsAfter(cursor, batchSize)
		default:
			batch, err = s.store.GetRunEventsAfter(runID, cursor, batchSize)
		}
		if err != nil {
			return nil, cursor, err
		}
		for _, value := range batch {
			cursor = value.ID
			event, wireErr := wireEvent(value)
			if wireErr != nil {
				return nil, cursor, wireErr
			}
			if warningsOnly && !isTaskWarningEvent(event) {
				continue
			}
			values = append(values, event)
			if len(values) == limit {
				break
			}
		}
		if len(batch) < batchSize || len(values) == limit {
			break
		}
	}
	return values, cursor, nil
}

func isTaskWarningEvent(event Event) bool {
	if strings.EqualFold(event.Severity, "warning") {
		return true
	}
	if severity, ok := event.Data["severity"].(string); ok && strings.EqualFold(severity, "warning") {
		return true
	}
	for _, eventType := range store.WarningEventTypes {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

type flowDurationStats struct {
	Count    int    `json:"count"`
	MedianMS *int64 `json:"median_ms,omitempty"`
	P80MS    *int64 `json:"p80_ms,omitempty"`
}

type flowStageStats struct {
	Stage                     string                `json:"stage"`
	Attempts                  int                   `json:"attempts"`
	DurationMS                int64                 `json:"duration_ms"`
	Human                     int                   `json:"human_attempts"`
	Agent                     int                   `json:"agent_attempts"`
	AgentShare                *float64              `json:"agent_share"`
	ByState                   []flowStageStateStats `json:"by_state"`
	TokensInput               *int64                `json:"tokens_input"`
	TokensOutput              *int64                `json:"tokens_output"`
	UsageKnownAttempts        int                   `json:"usage_known_attempts"`
	UsageUnknownAttempts      int                   `json:"usage_unknown_attempts"`
	UsageAttestation          string                `json:"usage_attestation"`
	UsageSource               string                `json:"usage_source,omitempty"`
	SubscriptionShareEstimate *float64              `json:"subscription_share_estimate"`
	SubscriptionCurrency      string                `json:"subscription_currency,omitempty"`
	EstimateSource            string                `json:"estimate_source,omitempty"`
	EstimateBasis             string                `json:"estimate_basis,omitempty"`
}

type flowStageStateStats struct {
	Execution  string `json:"execution"`
	Decision   string `json:"decision"`
	Outcome    string `json:"outcome"`
	Attempts   int    `json:"attempts"`
	DurationMS int64  `json:"duration_ms"`
}

type flowStateKey struct {
	execution string
	decision  string
	outcome   string
}

type flowMetricsResponse struct {
	From                         time.Time         `json:"from"`
	To                           time.Time         `json:"to"`
	LeadTime                     flowDurationStats `json:"lead_time"`
	CycleTime                    flowDurationStats `json:"cycle_time"`
	ByStage                      []flowStageStats  `json:"by_stage"`
	Overrides                    map[string]int    `json:"overrides_by_stage"`
	Skips                        map[string]int    `json:"skips_by_stage"`
	Returns                      map[string]int    `json:"returns_by_stage"`
	Usage                        flowUsageStats    `json:"usage"`
	AgentResults                 int               `json:"agent_results"`
	AgentAcceptedWithoutEdit     int               `json:"agent_accepted_without_edit"`
	AgentAcceptedWithoutEditRate *float64          `json:"agent_accepted_without_edit_rate,omitempty"`
}

type flowUsageStats struct {
	KnownRuns                 int      `json:"known_runs"`
	UnknownRuns               int      `json:"unknown_runs"`
	TokensInput               *int64   `json:"tokens_input"`
	TokensOutput              *int64   `json:"tokens_output"`
	Attestation               string   `json:"attestation"`
	Source                    string   `json:"source"`
	SubscriptionShareEstimate *float64 `json:"subscription_share_estimate"`
	SubscriptionCurrency      string   `json:"subscription_currency,omitempty"`
	EstimateSource            string   `json:"estimate_source,omitempty"`
	EstimateBasis             string   `json:"estimate_basis,omitempty"`
}

func (s *Server) handleGetFlowMetrics(w http.ResponseWriter, r *http.Request) {
	selection, ok := s.parseFlowSelection(w, r)
	if !ok {
		return
	}
	metricsValue, err := s.buildFlowMetricsFor(selection)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "flow metrics unavailable", "metrics_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusOK, metricsValue)
}

// flowSelection restricts flow metrics to runs started in [from, to) and,
// when runID is set, to that one run. A run selection without an explicit
// period covers the whole run, so the task timeline can show per-stage tokens
// and subscription estimates computed by the same aggregate as the dashboard.
type flowSelection struct {
	from, to time.Time
	runID    string
	period   bool
}

func (selection flowSelection) includes(run store.PipelineRun) bool {
	if selection.runID != "" && run.RunID != selection.runID {
		return false
	}
	return !selection.period || (!run.StartedAt.Before(selection.from) && run.StartedAt.Before(selection.to))
}

// parseFlowSelection writes the API error itself and reports whether the
// request may proceed.
func (s *Server) parseFlowSelection(w http.ResponseWriter, r *http.Request) (flowSelection, bool) {
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	explicitPeriod := r.URL.Query().Get("from") != "" || r.URL.Query().Get("to") != ""
	selection := flowSelection{runID: runID, period: true}
	if runID != "" {
		if !safeIdentity(runID) {
			writeJSONAPIError(w, http.StatusBadRequest, "invalid run id", "bad_identity")
			return selection, false
		}
		run, err := s.store.GetPipelineRunByRunID(runID)
		if err != nil || run == nil {
			writeJSONAPIError(w, http.StatusNotFound, "run not found", "not_found")
			return selection, false
		}
		if !explicitPeriod {
			selection.period = false
			selection.from = run.StartedAt.UTC()
			selection.to = time.Now().UTC()
			if run.CompletedAt != nil {
				selection.to = run.CompletedAt.UTC()
			}
			return selection, true
		}
	}
	from, to, err := parseFlowPeriod(r)
	if err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_period")
		return selection, false
	}
	selection.from, selection.to = from, to
	return selection, true
}

func parseFlowPeriod(r *http.Request) (time.Time, time.Time, error) {
	to := time.Now().UTC()
	from := to.Add(-30 * 24 * time.Hour)
	parse := func(raw string) (time.Time, error) {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			return parsed.UTC(), nil
		}
		if parsed, err := time.Parse("2006-01-02", raw); err == nil {
			return parsed.UTC(), nil
		}
		return time.Time{}, fmt.Errorf("period value %q must be RFC3339 or YYYY-MM-DD", raw)
	}
	var err error
	if raw := r.URL.Query().Get("from"); raw != "" {
		from, err = parse(raw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		to, err = parse(raw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, errors.New("to must be later than from")
	}
	return from, to, nil
}

func (s *Server) buildFlowMetrics(from, to time.Time) (flowMetricsResponse, error) {
	return s.buildFlowMetricsFor(flowSelection{from: from, to: to, period: true})
}

func (s *Server) buildFlowMetricsFor(selection flowSelection) (flowMetricsResponse, error) {
	from, to := selection.from, selection.to
	response := flowMetricsResponse{From: from, To: to, ByStage: []flowStageStats{}, Overrides: map[string]int{}, Skips: map[string]int{}, Returns: map[string]int{}}
	leadTimes, cycleTimes := make([]int64, 0), make([]int64, 0)
	stageIndex := map[string]int{}
	stageStates := map[string]map[flowStateKey]*flowStageStateStats{}
	stageTokensByMonth := map[string]map[string]int64{}
	stageUsageReady := map[string]bool{}
	agentResults, agentAcceptedWithoutEdit := 0, 0
	runs := make([]store.PipelineRun, 0)
	for offset := 0; ; offset += 100 {
		page, err := s.store.GetPipelineRunsPage(100, offset)
		if err != nil {
			return response, err
		}
		runs = append(runs, page...)
		if len(page) < 100 {
			break
		}
	}
	envelopesByRunID := make(map[string]metrics.UsageEnvelope, len(runs))
	monthTokens := map[string]int64{}
	monthEstimateReady := map[string]bool{}
	for _, run := range runs {
		envelope, readErr := readTaskUsageEnvelope(s, run.RunID)
		if readErr == nil {
			envelopesByRunID[run.RunID] = envelope
		} else {
			finishedAt := run.StartedAt
			if run.CompletedAt != nil {
				finishedAt = *run.CompletedAt
			}
			envelope = metrics.UsageEnvelope{RunID: run.RunID, FinishedAt: finishedAt}
		}
		month := envelope.FinishedAt.UTC().Format("2006-01")
		if _, initialized := monthEstimateReady[month]; !initialized {
			monthEstimateReady[month] = true
		}
		if !envelope.HasCompleteTokenUsage() || envelope.TokensInput < 0 || envelope.TokensOutput < 0 ||
			envelope.TokensInput > math.MaxInt64-envelope.TokensOutput {
			monthEstimateReady[month] = false
			continue
		}
		tokens := envelope.TokensInput + envelope.TokensOutput
		if tokens < 0 || monthTokens[month] > math.MaxInt64-tokens {
			monthEstimateReady[month] = false
			continue
		}
		monthTokens[month] += tokens
	}
	var tokenInputTotal, tokenOutputTotal int64
	requestedRuns := 0
	for _, run := range runs {
		if !selection.includes(run) {
			continue
		}
		requestedRuns++
		stages, err := s.store.GetStagesByPipelineRunID(run.ID)
		if err != nil {
			return response, err
		}
		events, err := s.allRunEvents(run.RunID)
		if err != nil {
			return response, err
		}
		humanEditAttemptIDs, humanEditMetadata := humanEditEvidence(events)
		executors := attemptExecutors(events)
		countedReturns := map[string]bool{}
		acceptanceAt := time.Time{}
		runFinishedAt := run.StartedAt
		if envelope, found := envelopesByRunID[run.RunID]; found && !envelope.FinishedAt.IsZero() {
			runFinishedAt = envelope.FinishedAt
		} else if run.CompletedAt != nil {
			runFinishedAt = *run.CompletedAt
		}
		runMonth := runFinishedAt.UTC().Format("2006-01")
		for stagePosition, stage := range stages {
			if stage.Status == "invalidated" || stage.Outcome == "invalidated" {
				continue
			}
			if stage.AgentName == "acceptance" && stage.CompletedAt != nil && (stage.Outcome == "passed" || stage.Status == "success" || stage.Status == "completed") {
				if acceptanceAt.IsZero() || stage.CompletedAt.Before(acceptanceAt) {
					acceptanceAt = *stage.CompletedAt
				}
			}
			stageName := stage.AgentName
			if stageName == "" {
				stageName = "unknown"
			}
			position, exists := stageIndex[stageName]
			if !exists {
				position = len(response.ByStage)
				stageIndex[stageName] = position
				response.ByStage = append(response.ByStage, flowStageStats{Stage: stageName, ByState: []flowStageStateStats{}})
				stageStates[stageName] = map[flowStateKey]*flowStageStateStats{}
				stageUsageReady[stageName] = true
			}
			stat := &response.ByStage[position]
			stat.Attempts++
			stat.DurationMS += stage.DurationMs
			stateKey := flowStateKey{execution: metricState(stage.Execution), decision: metricState(stage.Decision), outcome: metricState(stage.Outcome)}
			stateStat := stageStates[stageName][stateKey]
			if stateStat == nil {
				stateStat = &flowStageStateStats{Execution: stateKey.execution, Decision: stateKey.decision, Outcome: stateKey.outcome}
				stageStates[stageName][stateKey] = stateStat
			}
			stateStat.Attempts++
			stateStat.DurationMS += stage.DurationMs
			executor := stageExecutor(stage, executors)
			if executor == "human" {
				stat.Human++
			} else if executor == "agent" {
				stat.Agent++
				manifest, manifestErr := readTaskAttemptManifest(s, run.RunID, stage.AttemptID)
				if manifestErr == nil && manifest.Usage != nil && manifest.Usage.Attested {
					stat.UsageKnownAttempts++
					input, output := int64(0), int64(0)
					if stat.TokensInput != nil {
						input = *stat.TokensInput
					}
					if stat.TokensOutput != nil {
						output = *stat.TokensOutput
					}
					if manifest.Usage.TokensInput < 0 || manifest.Usage.TokensOutput < 0 ||
						input > math.MaxInt64-manifest.Usage.TokensInput || output > math.MaxInt64-manifest.Usage.TokensOutput {
						stat.UsageKnownAttempts--
						stat.UsageUnknownAttempts++
					} else {
						input += manifest.Usage.TokensInput
						output += manifest.Usage.TokensOutput
						stat.TokensInput, stat.TokensOutput = &input, &output
						if stageTokensByMonth[stageName] == nil {
							stageTokensByMonth[stageName] = map[string]int64{}
						}
						if manifest.Usage.TokensInput > math.MaxInt64-manifest.Usage.TokensOutput {
							stageUsageReady[stageName] = false
						} else {
							stageTokens := manifest.Usage.TokensInput + manifest.Usage.TokensOutput
							if stageTokensByMonth[stageName][runMonth] > math.MaxInt64-stageTokens {
								stageUsageReady[stageName] = false
							} else {
								stageTokensByMonth[stageName][runMonth] += stageTokens
							}
						}
					}
				} else {
					stat.UsageUnknownAttempts++
					stageUsageReady[stageName] = false
				}
				if taskStageSucceeded(stage) {
					agentResults++
					if !taskStageWasHumanEdited(stages, stagePosition, stage, executors, humanEditAttemptIDs, humanEditMetadata) {
						agentAcceptedWithoutEdit++
					}
				}
			}
		}
		if !acceptanceAt.IsZero() && !acceptanceAt.Before(run.StartedAt) {
			leadTimes = append(leadTimes, acceptanceAt.Sub(run.StartedAt).Milliseconds())
			start := firstCycleStartFromEvents(events, acceptanceAt)
			if !start.IsZero() && !acceptanceAt.Before(start) {
				cycleTimes = append(cycleTimes, acceptanceAt.Sub(start).Milliseconds())
			}
		}
		for _, event := range events {
			var data map[string]any
			_ = json.Unmarshal([]byte(event.DataJSON), &data)
			stageID := firstNonEmpty(event.Stage, metricStringValue(data["stage_id"]), metricStringValue(data["stage"]), metricStringValue(data["from_stage"]), metricStringValue(data["from"]))
			countReturn := func() {
				if stageID == "" {
					return
				}
				key := event.AttemptID
				if key == "" {
					key = fmt.Sprintf("event:%d", event.ID)
				}
				if !countedReturns[key] {
					response.Returns[stageID]++
					countedReturns[key] = true
				}
			}
			switch event.Type {
			case "check_overridden":
				if stageID != "" {
					response.Overrides[stageID]++
				}
			case "stage_skipped":
				if stageID != "" {
					response.Skips[stageID]++
				}
			case "stage_returned", "stage_return_requested", "stage_returned_to":
				countReturn()
			case "transition_selected":
				action := metricStringValue(data["action"])
				if strings.HasPrefix(action, "return_to_") {
					countReturn()
				}
			}
		}
		if envelope, found := envelopesByRunID[run.RunID]; found && envelope.HasCompleteTokenUsage() {
			response.Usage.KnownRuns++
			if envelope.TokensInput < 0 || envelope.TokensOutput < 0 ||
				tokenInputTotal > math.MaxInt64-envelope.TokensInput || tokenOutputTotal > math.MaxInt64-envelope.TokensOutput {
				response.Usage.KnownRuns--
				response.Usage.UnknownRuns++
			} else {
				tokenInputTotal += envelope.TokensInput
				tokenOutputTotal += envelope.TokensOutput
			}
		} else {
			response.Usage.UnknownRuns++
		}
	}
	for index := range response.ByStage {
		stage := &response.ByStage[index]
		if stage.Attempts > 0 {
			agentShare := float64(stage.Agent) / float64(stage.Attempts)
			stage.AgentShare = &agentShare
		}
		for _, stateStat := range stageStates[stage.Stage] {
			stage.ByState = append(stage.ByState, *stateStat)
		}
		sort.Slice(stage.ByState, func(i, j int) bool {
			left, right := stage.ByState[i], stage.ByState[j]
			if left.Execution != right.Execution {
				return left.Execution < right.Execution
			}
			if left.Decision != right.Decision {
				return left.Decision < right.Decision
			}
			return left.Outcome < right.Outcome
		})
		switch {
		case stage.Agent == 0:
			stage.UsageAttestation = "not_applicable"
		case stage.UsageUnknownAttempts == 0 && stage.UsageKnownAttempts > 0:
			stage.UsageAttestation = "complete"
		case stage.UsageKnownAttempts > 0:
			stage.UsageAttestation = "partial"
			stage.TokensInput, stage.TokensOutput = nil, nil
		default:
			stage.UsageAttestation = "unknown"
			stage.TokensInput, stage.TokensOutput = nil, nil
		}
		if stage.Agent > 0 {
			stage.UsageSource = "attempt_manifest"
		}
	}
	response.Usage.Source = "metrics.UsageEnvelope"
	switch {
	case response.Usage.KnownRuns == 0 && response.Usage.UnknownRuns == 0:
		response.Usage.Attestation = "unknown"
	case response.Usage.UnknownRuns == 0:
		response.Usage.Attestation = "complete"
	case response.Usage.KnownRuns > 0:
		response.Usage.Attestation = "partial"
	default:
		response.Usage.Attestation = "unknown"
	}
	if response.Usage.UnknownRuns == 0 && response.Usage.KnownRuns > 0 {
		response.Usage.TokensInput = &tokenInputTotal
		response.Usage.TokensOutput = &tokenOutputTotal
	}
	if requestedRuns > 0 {
		if cfg, cfgErr := config.Load(filepath.Join(s.targetDir, ".ai-team", "config.yaml")); cfgErr == nil &&
			cfg.Usage != nil && cfg.Usage.Validate() == nil && cfg.Usage.MonthlySubscriptionAmount > 0 {
			currency := cfg.Usage.MonthlySubscriptionCurrency
			if currency == "" {
				currency = "USD"
			}
			response.Usage.SubscriptionCurrency = currency
			response.Usage.EstimateSource = "project_config+metrics.UsageEnvelope"
			response.Usage.EstimateBasis = "monthly_subscription_weighted_by_attested_tokens"
			for index := range response.ByStage {
				stage := &response.ByStage[index]
				if stage.Agent > 0 {
					stage.SubscriptionCurrency = currency
					stage.EstimateSource = "project_config+metrics.UsageEnvelope+attempt_manifest"
					stage.EstimateBasis = "monthly_subscription_weighted_by_attested_stage_tokens"
				}
			}
			totalEstimate := float64(0)
			estimateComplete := true
			for _, run := range runs {
				if !selection.includes(run) {
					continue
				}
				envelope, found := envelopesByRunID[run.RunID]
				if !found || !envelope.HasCompleteTokenUsage() || envelope.TokensInput < 0 || envelope.TokensOutput < 0 ||
					envelope.TokensInput > math.MaxInt64-envelope.TokensOutput {
					estimateComplete = false
					break
				}
				month := envelope.FinishedAt.UTC().Format("2006-01")
				denominator := monthTokens[month]
				if !monthEstimateReady[month] || denominator == 0 {
					estimateComplete = false
					break
				}
				tokens := envelope.TokensInput + envelope.TokensOutput
				var estimateOK bool
				totalEstimate, estimateOK = addSubscriptionShareEstimate(totalEstimate, cfg.Usage.MonthlySubscriptionAmount, tokens, denominator)
				if !estimateOK {
					estimateComplete = false
					break
				}
			}
			if estimateComplete {
				response.Usage.SubscriptionShareEstimate = &totalEstimate
			}
			for index := range response.ByStage {
				stage := &response.ByStage[index]
				if stage.Agent == 0 || !stageUsageReady[stage.Stage] || stage.UsageAttestation != "complete" || len(stageTokensByMonth[stage.Stage]) == 0 {
					continue
				}
				stageEstimate := float64(0)
				stageEstimateComplete := true
				for month, tokens := range stageTokensByMonth[stage.Stage] {
					denominator := monthTokens[month]
					if !monthEstimateReady[month] || denominator == 0 {
						stageEstimateComplete = false
						break
					}
					var estimateOK bool
					stageEstimate, estimateOK = addSubscriptionShareEstimate(stageEstimate, cfg.Usage.MonthlySubscriptionAmount, tokens, denominator)
					if !estimateOK {
						stageEstimateComplete = false
						break
					}
				}
				if stageEstimateComplete {
					stage.SubscriptionShareEstimate = &stageEstimate
				}
			}
		}
	}
	response.LeadTime = summarizeDurations(leadTimes)
	response.CycleTime = summarizeDurations(cycleTimes)
	response.AgentResults, response.AgentAcceptedWithoutEdit = agentResults, agentAcceptedWithoutEdit
	if agentResults > 0 {
		rate := float64(agentAcceptedWithoutEdit) / float64(agentResults)
		response.AgentAcceptedWithoutEditRate = &rate
	}
	sort.Slice(response.ByStage, func(i, j int) bool { return response.ByStage[i].Stage < response.ByStage[j].Stage })
	return response, nil
}

func addSubscriptionShareEstimate(total, monthlyAmount float64, tokens, denominator int64) (float64, bool) {
	if total < 0 || monthlyAmount < 0 || tokens < 0 || denominator <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return total, false
	}
	share := float64(tokens) / float64(denominator)
	contribution := monthlyAmount * share
	if math.IsNaN(contribution) || math.IsInf(contribution, 0) || contribution > math.MaxFloat64-total {
		return total, false
	}
	return total + contribution, true
}

func readTaskAttemptManifest(s *Server, runID, attemptID string) (evidence.AttemptManifest, error) {
	if !safeIdentity(runID) || !safeIdentity(attemptID) {
		return evidence.AttemptManifest{}, errors.New("invalid attempt identity")
	}
	_, manifest, err := evidence.ReadAttemptManifest(nil, filepath.Join(s.runRoot, runID), runID, attemptID)
	if err != nil {
		return evidence.AttemptManifest{}, err
	}
	if manifest.RunID != runID || manifest.AttemptID != attemptID {
		return evidence.AttemptManifest{}, errors.New("attempt manifest identity mismatch")
	}
	return manifest, nil
}

func readTaskUsageEnvelope(s *Server, runID string) (metrics.UsageEnvelope, error) {
	if !safeIdentity(runID) {
		return metrics.UsageEnvelope{}, errors.New("invalid run identity")
	}
	reserved, err := metrics.UsageEnvelopeReservation(s.targetDir, runID)
	if err != nil {
		return metrics.UsageEnvelope{}, err
	}
	if reserved {
		return metrics.ReadUsageEnvelope(s.targetDir, runID)
	}
	if _, err := safeio.ExistingDir(s.targetDir, ".ai-team", "runs", runID); err != nil {
		return metrics.UsageEnvelope{}, err
	}
	path := filepath.Join(s.targetDir, ".ai-team", "runs", runID, "usage.json")
	data, err := safeio.ReadRegularFile(path, 8<<20)
	if err != nil {
		return metrics.UsageEnvelope{}, err
	}
	var envelope metrics.UsageEnvelope
	if err := strictjson.Unmarshal(data, 8<<20, &envelope); err != nil {
		return metrics.UsageEnvelope{}, err
	}
	if err := metrics.ValidateUsageEnvelope(runID, envelope); err != nil {
		return metrics.UsageEnvelope{}, err
	}
	return envelope, nil
}

func taskStageSucceeded(stage store.Stage) bool {
	return strings.EqualFold(stage.Outcome, "passed") || strings.EqualFold(stage.Status, "passed") ||
		strings.EqualFold(stage.Status, "success") || strings.EqualFold(stage.Status, "completed")
}

func taskStageWasHumanEdited(stages []store.Stage, agentPosition int, agent store.Stage, executors map[string]string, editedAttemptIDs, metadataKnown map[string]bool) bool {
	if editedAttemptIDs[agent.AttemptID] {
		return true
	}
	if agent.CompletedAt == nil {
		return true
	}
	for position := agentPosition + 1; position < len(stages); position++ {
		candidate := stages[position]
		if candidate.AgentName == agent.AgentName && stageExecutor(candidate, executors) == "human" &&
			(candidate.StartedAt.After(*agent.CompletedAt) || candidate.StageIndex > agent.StageIndex) {
			return !metadataKnown[candidate.AttemptID]
		}
	}
	return false
}

// attemptExecutors maps attempt IDs to the executor kind recorded on their
// attempt_finished event. The recorder stores the workflow execution state
// (running, succeeded, ...) in stages.execution, so the executor kind must come
// from the event stream.
func attemptExecutors(events []store.Event) map[string]string {
	executors := map[string]string{}
	for _, event := range events {
		if event.Type != "attempt_finished" || event.AttemptID == "" {
			continue
		}
		executor := strings.ToLower(event.Executor)
		if executor == "" {
			var data map[string]any
			if json.Unmarshal([]byte(event.DataJSON), &data) == nil {
				executor = strings.ToLower(metricStringValue(data["executor"]))
			}
		}
		if executor == "human" || executor == "agent" {
			executors[event.AttemptID] = executor
		}
	}
	return executors
}

// stageExecutor prefers the executor recorded for the attempt and falls back
// to a stage row whose execution column already holds the executor kind.
func stageExecutor(stage store.Stage, executors map[string]string) string {
	if executor := executors[stage.AttemptID]; executor != "" {
		return executor
	}
	execution := strings.ToLower(stage.Execution)
	if execution == "human" || execution == "agent" {
		return execution
	}
	return ""
}

func humanEditEvidence(events []store.Event) (map[string]bool, map[string]bool) {
	editedAttemptIDs := map[string]bool{}
	metadataKnown := map[string]bool{}
	for _, event := range events {
		if event.Type != "attempt_finished" || event.AttemptID == "" {
			continue
		}
		var data map[string]any
		if json.Unmarshal([]byte(event.DataJSON), &data) != nil {
			continue
		}
		value, exists := data["human_edit_of_agent_attempt_id"]
		if !exists {
			continue
		}
		metadataKnown[event.AttemptID] = true
		if agentAttemptID, ok := value.(string); ok && agentAttemptID != "" {
			editedAttemptIDs[agentAttemptID] = true
		}
	}
	return editedAttemptIDs, metadataKnown
}

func firstCycleStartFromEvents(events []store.Event, before time.Time) time.Time {
	start := time.Time{}
	for _, event := range events {
		if event.Timestamp.After(before) || (event.Type != "stage_taken" && event.Type != "agent_started" && event.Type != "attempt_started") {
			continue
		}
		if start.IsZero() || event.Timestamp.Before(start) {
			start = event.Timestamp
		}
	}
	return start
}

func metricState(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func metricStringValue(value any) string {
	valueString, _ := value.(string)
	return valueString
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) allRunEvents(runID string) ([]store.Event, error) {
	var values []store.Event
	var cursor int64
	for {
		page, err := s.store.GetRunEventsAfter(runID, cursor, 1000)
		if err != nil {
			return nil, err
		}
		values = append(values, page...)
		if len(page) < 1000 {
			return values, nil
		}
		cursor = page[len(page)-1].ID
	}
}

func summarizeDurations(values []int64) flowDurationStats {
	stats := flowDurationStats{Count: len(values)}
	if len(values) == 0 {
		return stats
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	median := sorted[(len(sorted)-1)/2]
	if len(sorted)%2 == 0 {
		lower, upper := sorted[len(sorted)/2-1], sorted[len(sorted)/2]
		median = lower + (upper-lower)/2
	}
	p80 := sorted[(len(sorted)*80+99)/100-1]
	stats.MedianMS, stats.P80MS = &median, &p80
	return stats
}

func (s *Server) handleGetFlowMetricsCSV(w http.ResponseWriter, r *http.Request) {
	selection, ok := s.parseFlowSelection(w, r)
	if !ok {
		return
	}
	value, err := s.buildFlowMetricsFor(selection)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "flow metrics unavailable", "metrics_unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=flow-metrics.csv")
	_, _ = fmt.Fprintln(w, "metric,stage,value,unit,attestation,source,basis")
	writeCSVMetric(w, "lead_time_count", "", int64Pointer(int64(value.LeadTime.Count)), "runs", "not_applicable", "sqlite")
	writeCSVMetric(w, "lead_time_median", "", value.LeadTime.MedianMS, "ms", "not_applicable", "sqlite")
	writeCSVMetric(w, "lead_time_p80", "", value.LeadTime.P80MS, "ms", "not_applicable", "sqlite")
	writeCSVMetric(w, "cycle_time_count", "", int64Pointer(int64(value.CycleTime.Count)), "runs", "not_applicable", "sqlite")
	writeCSVMetric(w, "cycle_time_median", "", value.CycleTime.MedianMS, "ms", "not_applicable", "sqlite")
	writeCSVMetric(w, "cycle_time_p80", "", value.CycleTime.P80MS, "ms", "not_applicable", "sqlite")
	writeCSVMetric(w, "agent_results", "", int64Pointer(int64(value.AgentResults)), "attempts", "not_applicable", "sqlite")
	writeCSVMetric(w, "agent_accepted_without_edit", "", int64Pointer(int64(value.AgentAcceptedWithoutEdit)), "attempts", "not_applicable", "sqlite")
	agentEditRateAttestation := "unknown"
	if value.AgentAcceptedWithoutEditRate != nil {
		agentEditRateAttestation = "complete"
	}
	writeCSVFloatMetric(w, "agent_accepted_without_edit_rate", "", value.AgentAcceptedWithoutEditRate, "ratio", agentEditRateAttestation, "sqlite", "accepted_agent_attempts_without_linked_human_edit / successful_agent_attempts")
	for _, stage := range value.ByStage {
		writeCSVMetric(w, "stage_attempts", stage.Stage, int64Pointer(int64(stage.Attempts)), "attempts", "not_applicable", "sqlite")
		writeCSVMetric(w, "stage_duration", stage.Stage, int64Pointer(stage.DurationMS), "ms", "not_applicable", "sqlite")
		writeCSVMetric(w, "stage_human_attempts", stage.Stage, int64Pointer(int64(stage.Human)), "attempts", "not_applicable", "sqlite")
		writeCSVMetric(w, "stage_agent_attempts", stage.Stage, int64Pointer(int64(stage.Agent)), "attempts", "not_applicable", "sqlite")
		writeCSVFloatMetric(w, "stage_agent_share", stage.Stage, stage.AgentShare, "ratio", "complete", "sqlite", "agent_attempts / all_non_invalidated_attempts")
		writeCSVMetric(w, "stage_tokens_input", stage.Stage, stage.TokensInput, "tokens", stage.UsageAttestation, stage.UsageSource)
		writeCSVMetric(w, "stage_tokens_output", stage.Stage, stage.TokensOutput, "tokens", stage.UsageAttestation, stage.UsageSource)
		writeCSVMetric(w, "stage_usage_known_attempts", stage.Stage, int64Pointer(int64(stage.UsageKnownAttempts)), "attempts", stage.UsageAttestation, stage.UsageSource)
		writeCSVMetric(w, "stage_usage_unknown_attempts", stage.Stage, int64Pointer(int64(stage.UsageUnknownAttempts)), "attempts", stage.UsageAttestation, stage.UsageSource)
		for _, state := range stage.ByState {
			basis := "execution=" + state.Execution + ";decision=" + state.Decision + ";outcome=" + state.Outcome
			writeCSVMetric(w, "stage_state_attempts", stage.Stage, int64Pointer(int64(state.Attempts)), "attempts", "not_applicable", "sqlite", basis)
			writeCSVMetric(w, "stage_state_duration", stage.Stage, int64Pointer(state.DurationMS), "ms", "not_applicable", "sqlite", basis)
		}
		writeCSVFloatMetric(w, "stage_subscription_share_estimate", stage.Stage, stage.SubscriptionShareEstimate, stage.SubscriptionCurrency,
			csvEstimateAttestation(stage.SubscriptionShareEstimate), stage.EstimateSource, stage.EstimateBasis)
	}
	writeCSVMetric(w, "tokens_input", "", value.Usage.TokensInput, "tokens", value.Usage.Attestation, value.Usage.Source)
	writeCSVMetric(w, "tokens_output", "", value.Usage.TokensOutput, "tokens", value.Usage.Attestation, value.Usage.Source)
	writeCSVMetric(w, "usage_known_runs", "", int64Pointer(int64(value.Usage.KnownRuns)), "runs", value.Usage.Attestation, value.Usage.Source)
	writeCSVMetric(w, "usage_unknown_runs", "", int64Pointer(int64(value.Usage.UnknownRuns)), "runs", value.Usage.Attestation, value.Usage.Source)
	writeCSVFloatMetric(w, "subscription_share_estimate", "", value.Usage.SubscriptionShareEstimate, value.Usage.SubscriptionCurrency,
		csvEstimateAttestation(value.Usage.SubscriptionShareEstimate), value.Usage.EstimateSource, value.Usage.EstimateBasis)
	writeCSVCounts(w, "override_count", value.Overrides)
	writeCSVCounts(w, "skip_count", value.Skips)
	writeCSVCounts(w, "return_count", value.Returns)
}

func csvEstimateAttestation(value *float64) string {
	if value == nil {
		return "unavailable"
	}
	return "estimated"
}

func writeCSVMetric(w http.ResponseWriter, name, stage string, value *int64, unit, attestation, source string, basisOverride ...string) {
	valueString := ""
	if value != nil {
		valueString = strconv.FormatInt(*value, 10)
	}
	basis := ""
	if len(basisOverride) > 0 {
		basis = basisOverride[0]
	}
	_, _ = fmt.Fprintf(w, "%s,%s,%s,%s,%s,%s,%s\n", name, csvField(stage), valueString,
		csvField(unit), csvField(attestation), csvField(source), csvField(basis))
}

func writeCSVFloatMetric(w http.ResponseWriter, name, stage string, value *float64, unit, attestation, source, basis string) {
	valueString := ""
	if value != nil {
		valueString = fmt.Sprintf("%.6f", *value)
	}
	_, _ = fmt.Fprintf(w, "%s,%s,%s,%s,%s,%s,%s\n", name, csvField(stage), valueString,
		csvField(unit), csvField(attestation), csvField(source), csvField(basis))
}

func writeCSVCounts(w http.ResponseWriter, name string, values map[string]int) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeCSVMetric(w, name, key, int64Pointer(int64(values[key])), "events", "not_applicable", "sqlite")
	}
}

func int64Pointer(value int64) *int64 { return &value }

func csvField(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
