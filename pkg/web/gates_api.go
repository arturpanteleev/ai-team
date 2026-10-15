package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
	"github.com/go-chi/chi/v5"
)

// gates_api.go — gate state of a task as the API tells it (S06): the real
// gates of every result version with their attempts, the aggregated "can the
// task move on, and why", the administrator overrides and the advisory
// warnings; plus the technical retry of a gate.

// StageGateRetryController resolves the pending check decision of a stage with
// a retry of technically failed gates on the same result version.
type StageGateRetryController interface {
	RetryStageGates(ctx context.Context, runID, stageID string, gates []string, reason, actorID, actorRole string) error
}

// requireGateOverrideRight authorizes the override of a failed required gate:
// a session of a system administrator. A product owner without the system
// right gets 403, whatever the stage function is.
func (s *Server) requireGateOverrideRight(w http.ResponseWriter, r *http.Request) (string, bool) {
	session, ok := s.requestSession(r)
	if !ok || session.Principal.ActorID == "" {
		writeJSONAPIError(w, http.StatusUnauthorized, "web session unavailable", "unauthorized")
		return "", false
	}
	if err := cloudidentity.Authorize(session.Principal, cloudidentity.PermissionGateOverride, ""); err != nil {
		writeJSONAPIError(w, http.StatusForbidden, "only a system administrator may override a failed gate: "+err.Error(), "admin_required")
		return "", false
	}
	return session.Principal.ActorID, true
}

func (s *Server) handleTaskStageGateRetry(w http.ResponseWriter, r *http.Request) {
	runID, stageID, gate := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID"), chi.URLParam(r, "gate")
	state, stage, ok := s.taskStagePreconditions(w, runID, stageID)
	if !ok {
		return
	}
	var command taskStageActionCommand
	if err := decodeOptionalCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	s.retryStageGates(w, r, runID, stageID, state, stage, append([]string{gate}, command.Gates...), command.Reason)
}

// handleTaskGateAttemptRetry is the same retry addressed by the ID of the
// failed attempt: the attempt must be the latest of its gate and a technical
// failure, otherwise there is nothing to retry (409).
func (s *Server) handleTaskGateAttemptRetry(w http.ResponseWriter, r *http.Request) {
	runID, attemptID := chi.URLParam(r, "runID"), chi.URLParam(r, "attemptID")
	if !safeIdentity(runID) || !safeIdentity(attemptID) {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid run or attempt id", "bad_identity")
		return
	}
	waves, _, err := s.recordedGateWaves(runID)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "gate history unavailable", "gates_unavailable")
		return
	}
	var stageID, gate string
	for _, wave := range waves {
		for _, attempt := range wave.Attempts {
			if attempt.AttemptID == attemptID {
				stageID, gate = wave.Key.Stage, attempt.GateName
			}
		}
	}
	if stageID == "" {
		writeJSONAPIError(w, http.StatusNotFound, "gate attempt not found", "not_found")
		return
	}
	state, stage, ok := s.taskStagePreconditions(w, runID, stageID)
	if !ok {
		return
	}
	var command taskStageActionCommand
	if err := decodeOptionalCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	s.retryStageGates(w, r, runID, stageID, state, stage, []string{gate}, command.Reason)
}

func (s *Server) retryStageGates(w http.ResponseWriter, r *http.Request, runID, stageID string, state lifecycle.State, stage config.TemplateStage, gates []string, reason string) {
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" {
		s.taskConflict(w, runID, stageID, "gate retry requires a failed check awaiting a decision")
		return
	}
	known := map[string]bool{}
	for _, ref := range stage.EffectiveGateGroups().Refs() {
		known[ref.InstanceName()] = true
	}
	for _, gate := range gates {
		if !known[strings.TrimSpace(gate)] {
			writeJSONAPIError(w, http.StatusNotFound, "stage "+stageID+" has no gate "+gate, "gate_not_found")
			return
		}
	}
	actorID, actorRole, ok := s.taskActionActor(w, r, stage, cloudidentity.StageActionTake)
	if !ok {
		return
	}
	controller, ok := s.controller.(StageGateRetryController)
	if !ok {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "gate retry is not connected to the task controller", "controller_action_unavailable")
		return
	}
	if err := controller.RetryStageGates(r.Context(), runID, stageID, gates, reason, actorID, actorRole); err != nil {
		s.taskConflict(w, runID, stageID, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]any{"run_id": runID, "stage_id": stageID, "gates": gates, "actor_id": actorID})
}

// gateWaveView is one result version of one stage visit with its gates, the
// transition decision and the override recorded for exactly this version.
type gateWaveView struct {
	StageID        string `json:"stage_id"`
	Visit          int    `json:"visit"`
	StageAttemptID string `json:"stage_attempt_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	// Current is true for the latest wave of the stage: the one the
	// transition is decided on.
	Current    bool                   `json:"current"`
	Gates      []gatesched.GateStatus `json:"gates"`
	Resolution gatesched.Resolution   `json:"transition"`
	Override   *gatesched.Override    `json:"override,omitempty"`
	// Attempts is the full attempt history of the version, superseded and
	// retried attempts included.
	Attempts []workflow.GateAttempt `json:"attempts"`
}

type gateWarningView struct {
	StageID   string                 `json:"stage_id"`
	Gate      string                 `json:"gate"`
	AttemptID string                 `json:"attempt_id"`
	Execution workflow.GateExecution `json:"execution"`
	Verdict   workflow.GateVerdict   `json:"verdict,omitempty"`
	Reason    string                 `json:"reason,omitempty"`
}

type gateStageView struct {
	StageID string         `json:"stage_id"`
	Title   string         `json:"title,omitempty"`
	Groups  [][]string     `json:"groups"`
	Current *gateWaveView  `json:"current,omitempty"`
	Waves   []gateWaveView `json:"waves"`
}

type taskGatesResponse struct {
	RunID string `json:"run_id"`
	// CanOverride says whether the caller holds the system administrator right.
	CanOverride bool            `json:"can_override"`
	Stages      []gateStageView `json:"stages"`
	// Overrides lists every recorded administrator override, newest last. An
	// override never changes the outcome of the gate it let pass.
	Overrides []gatesched.Override `json:"overrides"`
	// Warnings lists the advisory gates of the current versions that did not
	// pass: they never block, and they are never hidden.
	Warnings []gateWarningView `json:"warnings"`
}

// recordedGateWaves folds the task's event feed into waves and overrides.
func (s *Server) recordedGateWaves(runID string) ([]gatesched.RecordedWave, []gatesched.Override, error) {
	values, err := s.allRunEvents(runID)
	if err != nil {
		return nil, nil, err
	}
	events := make([]evidence.Event, 0, len(values))
	for _, value := range values {
		switch value.Type {
		case gatesched.EventAttemptStarted, gatesched.EventAttemptWaiting, gatesched.EventAttemptFinished, gatesched.EventCheckOverridden:
			events = append(events, storeEventToEvidence(value))
		}
	}
	waves, err := gatesched.RecordedWaves(events)
	if err != nil {
		return nil, nil, err
	}
	var overrides []gatesched.Override
	for _, event := range events {
		if override, ok := gatesched.DecodeOverride(event); ok {
			overrides = append(overrides, override)
		}
	}
	return waves, overrides, nil
}

func storeEventToEvidence(value store.Event) evidence.Event {
	event := evidence.Event{
		Sequence: uint64(value.Sequence), RunID: value.RunID, Type: value.Type, Stage: value.Stage,
		AttemptID: value.AttemptID, Timestamp: value.Timestamp,
	}
	if value.DataJSON != "" {
		_ = json.Unmarshal([]byte(value.DataJSON), &event.Data)
	}
	return event
}

func (s *Server) handleGetTaskGates(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if !safeIdentity(runID) {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid run id", "bad_identity")
		return
	}
	if run, err := s.store.GetPipelineRunByRunID(runID); err != nil || run == nil {
		writeJSONAPIError(w, http.StatusNotFound, "run not found", "not_found")
		return
	}
	response, err := s.buildTaskGates(r, runID)
	if err != nil {
		var unavailable errGatesUnavailable
		if errors.As(err, &unavailable) {
			writeJSONAPIError(w, http.StatusConflict, err.Error(), "template_pin_unavailable")
			return
		}
		writeJSONAPIError(w, http.StatusInternalServerError, "gate history unavailable", "gates_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusOK, response)
}

type errGatesUnavailable struct{ message string }

func (e errGatesUnavailable) Error() string { return e.message }

func (s *Server) buildTaskGates(r *http.Request, runID string) (taskGatesResponse, error) {
	response := taskGatesResponse{RunID: runID, Stages: []gateStageView{}, Overrides: []gatesched.Override{}, Warnings: []gateWarningView{}}
	if session, ok := s.requestSession(r); ok {
		response.CanOverride = cloudidentity.Authorize(session.Principal, cloudidentity.PermissionGateOverride, "") == nil
	}
	cfg, found, err := s.pinnedTaskConfig(runID)
	if err != nil || !found || cfg == nil {
		return response, errGatesUnavailable{"run has no valid pinned template"}
	}
	waves, overrides, err := s.recordedGateWaves(runID)
	if err != nil {
		return response, err
	}
	if overrides != nil {
		response.Overrides = overrides
	}
	for _, stage := range cfg.Stages {
		groups := stage.EffectiveGateGroups()
		if len(groups) == 0 {
			continue
		}
		view := gateStageView{StageID: stage.ID, Title: stage.Title, Groups: make([][]string, len(groups)), Waves: []gateWaveView{}}
		groupOf := map[string]int{}
		for index, group := range groups {
			for _, ref := range group {
				view.Groups[index] = append(view.Groups[index], ref.InstanceName())
				groupOf[ref.InstanceName()] = index
			}
		}
		refs := groups.Refs()
		for _, wave := range waves {
			if wave.Key.Stage != stage.ID {
				continue
			}
			var override *gatesched.Override
			for index := range overrides {
				candidate := overrides[index]
				if candidate.Stage == stage.ID && candidate.Scope == wave.Key.Scope && candidate.ArtifactSHA256 == wave.Key.ArtifactSHA256 {
					override = &candidate
				}
			}
			statuses, resolution := gatesched.Resolve(refs, func(ref workflow.GateRef) int { return groupOf[ref.InstanceName()] },
				wave.Key.ArtifactSHA256, wave.Attempts, override)
			view.Waves = append(view.Waves, gateWaveView{
				StageID: stage.ID, Visit: wave.Key.Visit, StageAttemptID: wave.Key.Scope, ArtifactSHA256: wave.Key.ArtifactSHA256,
				Gates: statuses, Resolution: resolution, Override: override, Attempts: wave.Attempts,
			})
		}
		if count := len(view.Waves); count > 0 {
			view.Waves[count-1].Current = true
			current := view.Waves[count-1]
			view.Current = &current
			for _, status := range current.Gates {
				if status.Warning && status.Attempt != nil {
					response.Warnings = append(response.Warnings, gateWarningView{
						StageID: stage.ID, Gate: status.GateName, AttemptID: status.Attempt.AttemptID,
						Execution: status.Attempt.Execution, Verdict: status.Attempt.Verdict, Reason: status.Attempt.Reason,
					})
				}
			}
		}
		response.Stages = append(response.Stages, view)
	}
	return response, nil
}
