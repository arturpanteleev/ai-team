package web

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/stagestatus"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// stage_status_api.go — the stage status projection of a task (S11) as REST
// serves it. The projection is computed on every read from the durable facts
// (the event journal, the approval store, the lifecycle checkpoint, the pinned
// template); nothing is stored and there is no route that writes a status.

// stageStatusInput gathers the durable facts of one run for the projection.
// events must be the whole journal of the run; cfg its pinned template.
func (s *Server) stageStatusInput(run *store.PipelineRun, cfg *config.Config, state *lifecycle.State, events []store.Event, allowed []string) stagestatus.Input {
	in := stagestatus.Input{RunStatus: run.Status, AllowedActions: allowed, Events: make([]stagestatus.Event, 0, len(events))}
	for _, stage := range cfg.Stages {
		in.Stages = append(in.Stages, stagestatus.StageDef{
			ID: stage.ID, Title: stage.Title, Function: stage.Function,
			Executor: stage.Executor, Agent: stage.Agent, Adapter: stage.Adapter,
		})
	}
	if graph, err := cfg.CompiledGraph(); err == nil {
		in.Entry = graph.Entry
	}
	for _, value := range events {
		event := stagestatus.Event{
			Sequence: value.Sequence, Type: value.Type, Stage: value.Stage, AttemptID: value.AttemptID,
			Actor: value.Actor, Executor: value.Executor, Timestamp: value.Timestamp,
		}
		if value.DataJSON != "" {
			_ = json.Unmarshal([]byte(value.DataJSON), &event.Data)
		}
		in.Events = append(in.Events, event)
	}
	if state != nil {
		lifecycleFacts := &stagestatus.Lifecycle{
			Phase: string(state.Phase), NextStage: state.NextStage, PendingApprovalID: state.PendingApprovalID,
			ExecutorOverrides: map[string]stagestatus.LifecycleOverride{},
		}
		for stageID, override := range state.ExecutorOverrides {
			lifecycleFacts.ExecutorOverrides[stageID] = stagestatus.LifecycleOverride{Executor: override.Executor, ApprovalID: override.ApprovalID}
		}
		in.Lifecycle = lifecycleFacts
	}
	if s.controller != nil {
		// Without the approval store the journal alone decides; a decision
		// that waits for the run to resume is only visible here.
		if approvals, err := s.controller.Approvals(run.RunID); err == nil {
			in.Approvals = make([]stagestatus.Approval, 0, len(approvals))
			for _, value := range approvals {
				in.Approvals = append(in.Approvals, stagestatus.Approval{
					ID: value.ID, Kind: string(value.Kind), Trigger: value.Trigger,
					FromStage: value.FromStage, ToStage: value.ToStage, Status: string(value.Status),
					RequiredRoles: value.RequiredRoles, Actions: value.Actions,
				})
			}
		}
	}
	return in
}

// taskStageStatuses projects the stages of one run. ok is false when the run
// has no valid pinned template: without the route there is nothing to project.
func (s *Server) taskStageStatuses(templates *taskTemplateLookup, run *store.PipelineRun, state *lifecycle.State, allowed []string) (stagestatus.Projection, bool) {
	cfg, found, err := templates.config(run.RunID)
	if err != nil || !found || cfg == nil {
		return stagestatus.Projection{}, false
	}
	events, err := s.allRunEvents(run.RunID)
	if err != nil {
		return stagestatus.Projection{}, false
	}
	return stagestatus.Project(s.stageStatusInput(run, cfg, state, events, allowed)), true
}

func (s *Server) handleGetTaskStageStatuses(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if !safeIdentity(runID) {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid run id", "bad_identity")
		return
	}
	run, err := s.store.GetPipelineRunByRunID(runID)
	if err != nil || run == nil {
		writeJSONAPIError(w, http.StatusNotFound, "run not found", "not_found")
		return
	}
	templates := s.newTaskTemplateLookup()
	var state *lifecycle.State
	var allowed []string
	if stateStore, stateErr := lifecycle.NewStore(s.targetDir); stateErr == nil {
		if loaded, loadErr := stateStore.Load(runID); loadErr == nil {
			state = &loaded
			allowed = s.taskAllowedActionsWith(templates, runID, loaded.NextStage, loaded)
		}
	}
	projection, ok := s.taskStageStatuses(templates, run, state, allowed)
	if !ok {
		writeJSONAPIError(w, http.StatusConflict, "run has no valid pinned template", "template_pin_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusOK, projection)
}
