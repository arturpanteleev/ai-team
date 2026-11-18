package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/approval"
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
	// The named-gate route addresses the exit chain: a check decision is raised
	// after the stage result, and the (workflow-level) exit gates are the ones
	// that can be retried on that version.
	s.retryStageGates(w, r, runID, stageID, state, stage, append([]string{gate}, command.Gates...), command.Reason, workflow.GatePhaseExit)
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
	var phase workflow.GatePhase
	for _, wave := range waves {
		for _, attempt := range wave.Attempts {
			if attempt.AttemptID == attemptID {
				stageID, gate = wave.Key.Stage, attempt.GateName
				// The phase belongs to the wave (and to the attempt): an entry
				// attempt must be checked against the entry chain, never treated
				// as a retry of a same-named exit gate.
				phase = wave.Phase
				if phase == "" {
					phase = attempt.Phase
				}
				phase = phase.Effective()
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
	s.retryStageGates(w, r, runID, stageID, state, stage, []string{gate}, command.Reason, phase)
}

func (s *Server) retryStageGates(w http.ResponseWriter, r *http.Request, runID, stageID string, state lifecycle.State, stage config.TemplateStage, gates []string, reason string, phase workflow.GatePhase) {
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" {
		s.taskConflict(w, runID, stageID, "gate retry requires a failed check awaiting a decision")
		return
	}
	cfg, found, err := s.pinnedTaskConfig(runID)
	if err != nil || !found || cfg == nil {
		writeJSONAPIError(w, http.StatusConflict, "run has no valid pinned template", "template_pin_unavailable")
		return
	}
	exitGroups, err := cfg.ExitGateGroupsFor(stage)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "gate chain unavailable", "gates_unavailable")
		return
	}
	// The retry list is the effective chain of the pinned config, not just the
	// stage's own groups: workflow-level exit gates are real gates too. An
	// attempt of the entry chain is validated against the entry chain only, so
	// a same-named exit gate cannot stand in for it.
	chain := exitGroups
	if phase.Effective() == workflow.GatePhaseEntry {
		chain, err = cfg.EntryGateGroupsFor(stage)
		if err != nil {
			writeJSONAPIError(w, http.StatusInternalServerError, "gate chain unavailable", "gates_unavailable")
			return
		}
	}
	known := map[string]bool{}
	for _, ref := range chain.Refs() {
		known[ref.InstanceName()] = true
	}
	for _, gate := range gates {
		if !known[strings.TrimSpace(gate)] {
			writeJSONAPIError(w, http.StatusNotFound, "stage "+stageID+" has no "+string(phase.Effective())+" gate "+gate, "gate_not_found")
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

// gateChainView is one effective chain of a stage — entry or exit — as the API
// resolves it: the groups the UI renders, the flat refs for Resolve and the
// instance-name to group index map.
type gateChainView struct {
	Groups  [][]string
	Refs    []workflow.GateRef
	GroupOf map[string]int
}

func newGateChainView(groups workflow.GateGroups) gateChainView {
	chain := gateChainView{
		Groups:  make([][]string, len(groups)),
		Refs:    groups.Refs(),
		GroupOf: map[string]int{},
	}
	for index, group := range groups {
		for _, ref := range group {
			chain.Groups[index] = append(chain.Groups[index], ref.InstanceName())
			chain.GroupOf[ref.InstanceName()] = index
		}
	}
	return chain
}

// gateWaveView is one result version of one stage visit with its gates, the
// transition decision and the override recorded for exactly this version.
type gateWaveView struct {
	StageID        string `json:"stage_id"`
	Visit          int    `json:"visit"`
	StageAttemptID string `json:"stage_attempt_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	// Phase is the gate chain of the wave: entry (before the executor) or exit
	// (after it). Old records with no phase are the exit chain.
	Phase workflow.GatePhase `json:"phase"`
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
	StageID string `json:"stage_id"`
	Title   string `json:"title,omitempty"`
	// Groups is the effective exit chain: the stage's own gates plus the
	// workflow-level exit gates of the pinned config.
	Groups [][]string `json:"groups"`
	// EntryGroups is the effective entry chain, when the stage has one. The
	// phase on each wave says which chain its gates were resolved against.
	EntryGroups [][]string     `json:"entry_groups,omitempty"`
	Current     *gateWaveView  `json:"current,omitempty"`
	Waves       []gateWaveView `json:"waves"`
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
	var pending []approval.PendingApproval
	if s.controller != nil {
		// Fail closed: without the approval state no retry is offered.
		pending, _ = s.controller.Approvals(runID)
	}
	if overrides != nil {
		response.Overrides = overrides
	}
	for _, stage := range cfg.Stages {
		// The effective chains of the pinned config, not just the stage's own
		// groups: workflow-level entry/exit gates are part of both views.
		exitGroups, err := cfg.ExitGateGroupsFor(stage)
		if err != nil {
			return response, err
		}
		entryGroups, err := cfg.EntryGateGroupsFor(stage)
		if err != nil {
			return response, err
		}
		if len(exitGroups) == 0 && len(entryGroups) == 0 {
			continue
		}
		chains := map[workflow.GatePhase]gateChainView{
			workflow.GatePhaseExit: newGateChainView(exitGroups),
		}
		if len(entryGroups) > 0 {
			chains[workflow.GatePhaseEntry] = newGateChainView(entryGroups)
		}
		view := gateStageView{
			StageID: stage.ID, Title: stage.Title,
			Groups: chains[workflow.GatePhaseExit].Groups,
			Waves:  []gateWaveView{},
		}
		if entry, ok := chains[workflow.GatePhaseEntry]; ok {
			view.EntryGroups = entry.Groups
		}
		var stageWaves []gatesched.RecordedWave
		for _, wave := range waves {
			if wave.Key.Stage == stage.ID {
				stageWaves = append(stageWaves, wave)
			}
		}
		type scopePhase struct {
			scope string
			phase workflow.GatePhase
		}
		lastOfScope := map[scopePhase]int{}
		for index, wave := range stageWaves {
			lastOfScope[scopePhase{wave.Key.Scope, wave.Phase.Effective()}] = index
		}
		for waveIndex, wave := range stageWaves {
			phase := wave.Phase.Effective()
			chain, ok := chains[phase]
			if !ok {
				// A chain may be gone from the pinned config while its attempts
				// stay in history: keep the wave visible, with no configured
				// statuses to resolve.
				chain = gateChainView{GroupOf: map[string]int{}}
			}
			var override *gatesched.Override
			for index := range overrides {
				candidate := overrides[index]
				if candidate.Stage != stage.ID || candidate.Scope != wave.Key.Scope {
					continue
				}
				if candidate.ArtifactSHA256 == wave.Key.ArtifactSHA256 && !candidate.IsLegacy() {
					override = &candidate
				} else if candidate.IsLegacy() && lastOfScope[scopePhase{wave.Key.Scope, phase}] == waveIndex && override == nil {
					// A pre-S06 override has no version: it is attached to the
					// last version of its stage attempt, as history only.
					override = &candidate
				}
			}
			statuses, resolution := gatesched.Resolve(chain.Refs, func(ref workflow.GateRef) int { return chain.GroupOf[ref.InstanceName()] },
				wave.Key.ArtifactSHA256, wave.Attempts, override)
			view.Waves = append(view.Waves, gateWaveView{
				StageID: stage.ID, Visit: wave.Key.Visit, StageAttemptID: wave.Key.Scope, ArtifactSHA256: wave.Key.ArtifactSHA256,
				Phase: phase, Gates: statuses, Resolution: resolution, Override: override, Attempts: wave.Attempts,
			})
		}
		if count := len(view.Waves); count > 0 {
			current := &view.Waves[count-1]
			current.Current = true
			offered := offeredGateRetries(pending, stage.ID, current.ArtifactSHA256, current.Phase)
			for index := range view.Waves {
				for position := range view.Waves[index].Gates {
					status := &view.Waves[index].Gates[position]
					status.Retryable = status.Retryable && index == count-1 &&
						offered[gateRetryKey{Phase: view.Waves[index].Phase, Gate: status.GateName}]
				}
			}
			view.Current = current
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

// gateRetryKey identifies a retry offer within one wave: the same gate name may
// be configured in both chains, but an entry attempt is not an exit attempt.
type gateRetryKey struct {
	Phase workflow.GatePhase
	Gate  string
}

// offeredGateRetries lists the gates a pending check decision of the stage
// really offers to retry on the given result version and chain phase. A gate
// that is technically failed but not in the payload (an advisory one, an older
// version, no pending decision) must not get a retry button: the retry would
// end in a conflict.
func offeredGateRetries(pending []approval.PendingApproval, stageID, sha string, phase workflow.GatePhase) map[gateRetryKey]bool {
	offered := map[gateRetryKey]bool{}
	for _, value := range pending {
		if value.Kind != approval.KindCheckDecision || value.Trigger != approval.StageCheckTrigger ||
			value.FromStage != stageID || value.Status != approval.StatusPending ||
			!actionAllowed(value.Actions, approval.ActionRetryGate) {
			continue
		}
		var payload approval.CheckDecisionPayload
		if json.Unmarshal(value.Payload, &payload) != nil || (payload.ArtifactSHA256 != "" && payload.ArtifactSHA256 != sha) {
			continue
		}
		for _, gate := range payload.RetryableGates {
			offered[gateRetryKey{Phase: phase.Effective(), Gate: gate}] = true
		}
	}
	return offered
}
