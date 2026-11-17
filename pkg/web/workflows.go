package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
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
	base, err := graphViewForConfig(cfg, graphModeClass)
	if err != nil {
		writeJSONAPIError(w, http.StatusConflict, "workflow graph cannot be compiled", "graph_unavailable")
		return
	}
	graphs := make([]graphView, 0, len(meta.Graphs))
	for _, declared := range meta.Graphs {
		view := base
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

// workflowTasks links the class to its objects: a run belongs to a workflow
// when its immutable pin selects that workflow id. The pin is the only durable
// association, so a workflow renamed after a run keeps its old tasks.
func (s *Server) workflowTasks(workflowID string) []workflowTaskRef {
	runs, err := s.store.GetPipelineRuns()
	if err != nil {
		return []workflowTaskRef{}
	}
	templates := s.newTaskTemplateLookup()
	stateStore, stateErr := lifecycle.NewStore(s.targetDir)
	refs := make([]workflowTaskRef, 0)
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
	return refs
}
