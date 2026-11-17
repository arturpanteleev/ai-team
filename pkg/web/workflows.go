package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// workflows.go — read-only workflow catalog (WF-03).
//
// A workflow is a class: a named, versioned set of graphs with effective entry
// and exit gate chains. A run is one object that travels through those graphs.
// The list is cheap (metadata only); the card carries the graphs themselves and
// the tasks currently pinned to this workflow, so both modes are visible from
// the same catalogue. There is deliberately no write route here: workflows
// enter through the reviewed project configuration, like the gate library.

type workflowListItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Version     int    `json:"version"`
	GraphCount  int    `json:"graph_count"`
	Active      bool   `json:"active"`
}

type workflowsListResponse struct {
	Workflows        []workflowListItem `json:"workflows"`
	ActiveWorkflowID string             `json:"active_workflow_id"`
}

type workflowTaskRef struct {
	RunID        string `json:"run_id"`
	Feature      string `json:"feature"`
	Status       string `json:"status,omitempty"`
	CurrentStage string `json:"current_stage,omitempty"`
}

type workflowDetailView struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Version     int               `json:"version"`
	Active      bool              `json:"active"`
	Graphs      []graphView       `json:"graphs"`
	Tasks       []workflowTaskRef `json:"tasks"`
	// These are the fully resolved chains: inheritance (extends), point
	// overrides and version-conflict checks have already been applied by
	// WorkflowGateGroups, so the client never has to repeat that logic.
	EntryGateGroups workflow.GateGroups `json:"entry_gate_groups"`
	ExitGateGroups  workflow.GateGroups `json:"exit_gate_groups"`
}

// nonNilGateGroups keeps an empty chain as [] rather than null: a workflow may
// lawfully declare no gates, and that is a different answer from "unavailable".
func nonNilGateGroups(groups workflow.GateGroups) workflow.GateGroups {
	if groups == nil {
		return workflow.GateGroups{}
	}
	return groups
}

// handleListWorkflows serves GET /api/workflows: the catalogue cards.
func (s *Server) handleListWorkflows(w http.ResponseWriter, _ *http.Request) {
	cfg, err := s.currentConfig()
	if err != nil {
		writeJSONAPIError(w, http.StatusNotFound, "workflow configuration unavailable", "config_unavailable")
		return
	}
	active := cfg.ActiveWorkflowID()
	declared := cfg.AllWorkflows()
	items := make([]workflowListItem, 0, len(declared))
	for _, meta := range declared {
		items = append(items, workflowListItem{
			ID: meta.ID, Title: meta.Title, Description: meta.Description,
			Version: meta.Version, GraphCount: len(meta.Graphs), Active: meta.ID == active,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONResponse(w, http.StatusOK, workflowsListResponse{Workflows: items, ActiveWorkflowID: active})
}

// handleGetWorkflow serves GET /api/workflows/{workflowID}: one card with its
// graphs, effective gate chains and the runs pinned to this workflow.
func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "workflowID")
	if !safeIdentity(id) {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid workflow id", "bad_identity")
		return
	}
	cfg, err := s.currentConfig()
	if err != nil {
		writeJSONAPIError(w, http.StatusNotFound, "workflow configuration unavailable", "config_unavailable")
		return
	}
	var meta *config.WorkflowMeta
	active := cfg.ActiveWorkflowID()
	for _, declared := range cfg.AllWorkflows() {
		if declared.ID == id {
			meta = &declared
			break
		}
	}
	if meta == nil {
		writeJSONAPIError(w, http.StatusNotFound, "workflow not found", "not_found")
		return
	}
	entry, err := cfg.WorkflowGateGroups(id, true)
	if err != nil {
		writeJSONAPIError(w, http.StatusConflict, "workflow entry gates cannot be resolved", "gate_chain_unavailable")
		return
	}
	exit, err := cfg.WorkflowGateGroups(id, false)
	if err != nil {
		writeJSONAPIError(w, http.StatusConflict, "workflow exit gates cannot be resolved", "gate_chain_unavailable")
		return
	}
	graphs := make([]graphView, 0, len(meta.Graphs))
	for _, declared := range meta.Graphs {
		view, err := graphViewForGraph(cfg, id, &declared, graphModeClass)
		if err != nil {
			writeJSONAPIError(w, http.StatusConflict, "workflow graph cannot be compiled", "graph_unavailable")
			return
		}
		view.GraphID = declared.ID
		view.WorkflowID = id
		view.Title = declared.Title
		graphs = append(graphs, view)
	}

	detail := workflowDetailView{
		ID: meta.ID, Title: meta.Title, Description: meta.Description,
		Version: meta.Version, Active: meta.ID == active,
		Graphs: graphs, Tasks: s.workflowTasks(id),
		EntryGateGroups: nonNilGateGroups(entry), ExitGateGroups: nonNilGateGroups(exit),
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONResponse(w, http.StatusOK, detail)
}

// WF-06: the workflow card edits only the process fields of one workflow. The
// browser never sends YAML and never sends commands, gates, graphs or other
// infrastructure: it sends a structured patch (workflow_id, title,
// description, expected_version). The server loads the active file
// configuration itself, applies the allow-list to the chosen workflow's
// title/description in memory and publishes a new version through the normal
// TemplateStore path. Everything the patch does not mention is taken from the
// server-loaded file and never from the request, including the command gates
// that a whole-document publish would refuse (the F2 case).
type workflowProcessPatch struct {
	WorkflowID      string  `json:"workflow_id"`
	Title           *string `json:"title,omitempty"`
	Description     *string `json:"description,omitempty"`
	ExpectedVersion string  `json:"expected_version"`
}

// disallowedWorkflowPatchField reports a key outside the tiny allow-list. It
// is classified as trusted_config_only: the web may not even name a command,
// gate, graph or another workflow in this request.
type disallowedWorkflowPatchField struct{ name string }

func (e *disallowedWorkflowPatchField) Error() string {
	return fmt.Sprintf("process patch не принимает поле %q: команды, гейты, графы и другие workflow правятся только в файловом конфиге", e.name)
}

func decodeWorkflowProcessPatch(w http.ResponseWriter, r *http.Request) (workflowProcessPatch, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBody)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return workflowProcessPatch{}, err
	}
	// Decode once into keys to reject anything outside the allow-list with a
	// domain code, then decode into the typed patch for value/type checks.
	var keys map[string]json.RawMessage
	if err := strictjson.Unmarshal(data, maxCommandBody, &keys); err != nil {
		return workflowProcessPatch{}, err
	}
	for name := range keys {
		switch name {
		case "workflow_id", "title", "description", "expected_version":
		default:
			return workflowProcessPatch{}, &disallowedWorkflowPatchField{name: name}
		}
	}
	var patch workflowProcessPatch
	if err := strictjson.Unmarshal(data, maxCommandBody, &patch); err != nil {
		return workflowProcessPatch{}, err
	}
	if patch.WorkflowID == "" {
		return workflowProcessPatch{}, errors.New("workflow_id обязателен")
	}
	if len(patch.ExpectedVersion) != 64 {
		return workflowProcessPatch{}, errors.New("expected_version должен быть SHA-256 hex")
	}
	return patch, nil
}

// handlePatchWorkflowProcess serves POST /api/workflows/{workflowID}/process.
// It is the only write route of the workflow card; the read routes stay
// read-only and the full /api/template/publish document route is untouched.
func (s *Server) handlePatchWorkflowProcess(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "workflowID")
	if !safeIdentity(id) {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid workflow id", "bad_identity")
		return
	}
	if err := s.authorize(r, cloudidentity.PermissionTemplateEdit, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	patch, err := decodeWorkflowProcessPatch(w, r)
	if err != nil {
		var disallowed *disallowedWorkflowPatchField
		if errors.As(err, &disallowed) {
			writeJSONAPIError(w, http.StatusBadRequest, err.Error(), config.ErrCodeTrustedConfigOnly)
			return
		}
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if patch.WorkflowID != id {
		writeJSONAPIError(w, http.StatusBadRequest,
			"process patch может менять только выбранный workflow: другой workflow правится в файловом конфиге",
			config.ErrCodeTrustedConfigOnly)
		return
	}
	if patch.Title != nil && strings.TrimSpace(*patch.Title) == "" {
		writeJSONAPIError(w, http.StatusBadRequest, "title workflow не может быть пустым", "invalid_process_patch")
		return
	}
	if patch.Description != nil && strings.ContainsAny(*patch.Description, "\r\n\x00") {
		writeJSONAPIError(w, http.StatusBadRequest, "description не должен содержать управляющие символы", "invalid_process_patch")
		return
	}

	s.templateMu.Lock()
	defer s.templateMu.Unlock()

	// The file is the authority for everything but the two fields above: it is
	// loaded here, never taken from the request body.
	store, err := s.templateStore()
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "template store unavailable", "template_store_unavailable")
		return
	}
	current, currentVersion, err := store.ReadCurrent()
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "project template unavailable", "template_unavailable")
		return
	}
	if patch.ExpectedVersion != currentVersion {
		writeJSONAPIError(w, http.StatusConflict, "активный шаблон изменился: обновите страницу перед правкой", "version_conflict")
		return
	}
	cfg, err := config.ParseYAML(current)
	if err != nil {
		writeJSONAPIError(w, http.StatusConflict, "workflow configuration unavailable", "config_unavailable")
		return
	}

	var target *config.WorkflowMeta
	for i := range cfg.Workflows {
		if cfg.Workflows[i].ID == id {
			target = &cfg.Workflows[i]
			break
		}
	}
	if target == nil {
		// The synthesized default is not written in the file yet; materialize
		// it before editing so the publication keeps the edited value.
		for _, declared := range cfg.AllWorkflows() {
			if declared.ID == id {
				cfg.Workflows = append(cfg.Workflows, declared)
				target = &cfg.Workflows[len(cfg.Workflows)-1]
				break
			}
		}
	}
	if target == nil {
		writeJSONAPIError(w, http.StatusNotFound, "workflow not found", "not_found")
		return
	}
	if patch.Title != nil {
		target.Title = *patch.Title
	}
	if patch.Description != nil {
		target.Description = *patch.Description
	}

	// ParseYAML derives Confirm from Result for stages with no explicit gate
	// chain. A v7 document must not carry that legacy key, so drop the derived
	// spelling before re-rendering: the next parse derives it again from Result
	// and the effective chain is unchanged.
	if cfg.SchemaVersion >= config.WorkflowSchemaVersion {
		for i := range cfg.Stages {
			cfg.Stages[i].RequiredSections = nil
			cfg.Stages[i].Check = nil
			cfg.Stages[i].Confirm = ""
		}
	}

	updated, err := cfg.Marshal()
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "не удалось сериализовать конфиг: "+err.Error(), "template_unavailable")
		return
	}
	// Fail closed if the patch (or the round trip) produced an invalid config.
	if _, _, err := s.parseTemplateYAML(updated); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, "process patch сделал конфиг невалидным: "+err.Error(), "invalid_process_patch")
		return
	}
	// Same pinning and provenance as a full publication: running tasks keep
	// their version, the file baseline is saved and web-written bytes are
	// registered (TemplateStore.Publish).
	if err := s.pinUnversionedInFlightRuns(store, current); err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "не удалось закрепить версии текущих задач: "+err.Error(), "pin_failed")
		return
	}
	version, err := store.Publish(updated, patch.ExpectedVersion)
	if err != nil {
		writeJSONAPIError(w, http.StatusConflict, err.Error(), "version_conflict")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"version": version, "workflow_id": id})
}

// workflowTasks links the class to its objects: a run belongs to a workflow
// when its immutable pin selects that workflow id. The pin is the only durable
// association, so a workflow renamed after a run keeps its old tasks.
//
// GetPipelineRuns returns at most one page, so the association walks every page
// of the run store like the other projections do, otherwise a workflow with
// more than one page of runs silently drops its older tasks.
func (s *Server) workflowTasks(workflowID string) []workflowTaskRef {
	templates := s.newTaskTemplateLookup()
	stateStore, stateErr := lifecycle.NewStore(s.targetDir)
	refs := make([]workflowTaskRef, 0)
	for offset := 0; ; offset += 100 {
		runs, err := s.store.GetPipelineRunsPage(100, offset)
		if err != nil {
			return refs
		}
		for _, run := range runs {
			runCfg, found, configErr := templates.config(run.RunID)
			if configErr != nil || !found || runCfg == nil || runCfg.ActiveWorkflowID() != workflowID {
				continue
			}
			ref := workflowTaskRef{RunID: run.RunID, Feature: run.Feature, Status: run.Status}
			if stateErr == nil {
				if state, loadErr := stateStore.Load(run.RunID); loadErr == nil && state.NextStage != "" {
					ref.CurrentStage = state.NextStage
				}
			}
			refs = append(refs, ref)
		}
		if len(runs) < 100 {
			break
		}
	}
	return refs
}
