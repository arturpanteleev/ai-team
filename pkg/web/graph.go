package web

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/stagestatus"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// graph.go — read-only graph API (GRA-02).
//
// A graph has two modes in the data, not two implementations:
//
//   - class: the graph declared by a workflow's template, read from the active
//     configuration. There is no run and therefore no computed stage status.
//   - run: the same graph pinned by one task, with the stage statuses derived
//     from that task's durable facts (the projection GET /stage-statuses shows).
//
// Both serve the same wire shape so a standalone graph page and the task page
// render one contract. The graph itself is workflow.Graph: its nodes carry
// max_visits and its edges carry outcome and approval. Returns are a separate
// template field and are added next to the compiled graph.

const (
	graphModeClass = "class"
	graphModeRun   = "run"
)

// graphReturn is one backward route of a template graph. Returns are not part
// of workflow.Graph, so the read model carries them beside the compiled graph.
type graphReturn struct {
	From      string `json:"from"`
	To        string `json:"to"`
	MaxVisits int    `json:"max_visits,omitempty"`
}

// graphView is the read model of one graph. workflow.Graph is embedded so the
// wire shape keeps the entry/nodes/edges contract the task page already reads.
// The fields after it are metadata and run-only projection data.
type graphView struct {
	workflow.Graph
	Mode       string        `json:"mode"`
	GraphID    string        `json:"graph_id,omitempty"`
	WorkflowID string        `json:"workflow_id,omitempty"`
	Title      string        `json:"title,omitempty"`
	Returns    []graphReturn `json:"returns,omitempty"`

	// Run mode only. stage_statuses is the same projection the task page gets,
	// so the graph page can show the current position without a second source.
	RunID         string                  `json:"run_id,omitempty"`
	RunStatus     string                  `json:"run_status,omitempty"`
	CurrentStage  string                  `json:"current_stage,omitempty"`
	StageStatuses *stagestatus.Projection `json:"stage_statuses,omitempty"`
}

// currentConfig parses the active project configuration. The graph endpoints
// only read: agent validation is a run concern, while compilation and the
// workflow catalog need the parsed structure alone.
func (s *Server) currentConfig() (*config.Config, error) {
	store, err := s.templateStore()
	if err != nil {
		return nil, err
	}
	data, _, err := store.ReadCurrent()
	if err != nil {
		return nil, err
	}
	return config.ParseYAML(data)
}

func graphReturns(cfg *config.Config) []graphReturn {
	if cfg == nil || len(cfg.Returns) == 0 {
		return nil
	}
	out := make([]graphReturn, 0, len(cfg.Returns))
	for _, route := range cfg.Returns {
		out = append(out, graphReturn{From: route.From, To: route.To, MaxVisits: route.MaxVisits})
	}
	return out
}

// graphViewForGraph compiles the graph for one declared graph of one workflow.
// It compiles the config's template with that workflow's gate chains — not the
// active workflow's — and honors the graph's declared entry. A shallow copy
// carries the target workflow id into compilation without mutating the parsed
// config, so two workflows that differ only in their gate chains produce
// different edges, and two graphs of one workflow can differ by their entry.
//
// The graph's `template` names the config's single active template: there is
// no template catalog yet, so stages and returns always come from `cfg`, while
// the gate chains (per workflow) and the entry (per graph) are the graph's own.
func graphViewForGraph(cfg *config.Config, workflowID string, declared *config.WorkflowGraph, mode string) (graphView, error) {
	workflowCfg := *cfg
	workflowCfg.ActiveWorkflow = workflowID
	compiled, err := workflowCfg.CompiledGraph()
	if err != nil {
		return graphView{}, err
	}
	if declared != nil && declared.Entry != "" {
		known := false
		for _, node := range compiled.Nodes {
			if node.Key() == declared.Entry {
				known = true
				break
			}
		}
		if !known {
			return graphView{}, fmt.Errorf("workflow graph: entry %q не существует", declared.Entry)
		}
		compiled.Entry = declared.Entry
	}
	return graphView{Graph: compiled, Mode: mode, Returns: graphReturns(cfg)}, nil
}

// handleGetGraph serves a workflow class graph by its declared graph id
// (GET /api/graphs/{graphID}). It never reads a task and never writes a status.
func (s *Server) handleGetGraph(w http.ResponseWriter, r *http.Request) {
	graphID := chi.URLParam(r, "graphID")
	if !safeIdentity(graphID) {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid graph id", "bad_identity")
		return
	}
	cfg, err := s.currentConfig()
	if err != nil {
		writeJSONAPIError(w, http.StatusNotFound, "graph configuration unavailable", "config_unavailable")
		return
	}
	// A graph id is unique within a workflow, not across workflows, so the same
	// id may name one graph in two different workflows. Resolve every match and
	// refuse an ambiguous id instead of silently serving the first one.
	var declared *config.WorkflowGraph
	workflowID := ""
	matches := 0
	for _, meta := range cfg.AllWorkflows() {
		for i := range meta.Graphs {
			if meta.Graphs[i].ID == graphID {
				matches++
				if declared == nil {
					graph := meta.Graphs[i]
					declared = &graph
					workflowID = meta.ID
				}
			}
		}
	}
	switch matches {
	case 0:
		writeJSONAPIError(w, http.StatusNotFound, "graph not found", "not_found")
		return
	case 1:
		// resolved uniquely below
	default:
		writeJSONAPIError(w, http.StatusConflict, "graph id is ambiguous across workflows", "graph_ambiguous")
		return
	}
	view, err := graphViewForGraph(cfg, workflowID, declared, graphModeClass)
	if err != nil {
		writeJSONAPIError(w, http.StatusConflict, "graph cannot be compiled", "graph_unavailable")
		return
	}
	view.GraphID, view.WorkflowID, view.Title = declared.ID, workflowID, declared.Title
	w.Header().Set("Cache-Control", "no-store")
	writeJSONResponse(w, http.StatusOK, view)
}

// handleGetRunGraph serves one task's graph (GET /api/runs/{runID}/graph).
// It is the run counterpart of handleGetGraph: the pinned graph plus the
// computed stage statuses of that run, including its current position.
func (s *Server) handleGetRunGraph(w http.ResponseWriter, r *http.Request) {
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
	cfg, found, err := templates.config(runID)
	if err != nil || !found || cfg == nil {
		writeJSONAPIError(w, http.StatusConflict, "run has no valid pinned template", "template_pin_unavailable")
		return
	}
	// The run's graph is the pinned template compiled for the run's own
	// workflow (its ActiveWorkflowID), matching the task page's graph. The
	// graph id and title are the metadata of that workflow's first graph; the
	// run does not re-root at a declared graph entry, because the run travels
	// the template stages in order.
	workflowID := cfg.ActiveWorkflowID()
	view, err := graphViewForGraph(cfg, workflowID, nil, graphModeRun)
	if err != nil {
		writeJSONAPIError(w, http.StatusConflict, "pinned graph cannot be compiled", "graph_unavailable")
		return
	}
	view.RunID, view.RunStatus = runID, run.Status
	view.WorkflowID = workflowID
	if active := cfg.ActiveWorkflowMeta(); active != nil && len(active.Graphs) > 0 {
		view.GraphID = active.Graphs[0].ID
		view.Title = active.Graphs[0].Title
	}

	// The same facts and the same projection the stage-status route uses: the
	// graph page never computes a status of its own.
	var state *lifecycle.State
	var allowed []string
	if stateStore, stateErr := lifecycle.NewStore(s.targetDir); stateErr == nil {
		if loaded, loadErr := stateStore.Load(runID); loadErr == nil {
			state = &loaded
			allowed = s.taskAllowedActionsWith(templates, runID, loaded.NextStage, loaded)
		}
	}
	if projection, ok := s.taskStageStatuses(templates, run, state, allowed); ok {
		view.StageStatuses = &projection
		view.CurrentStage = projection.CurrentStage
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONResponse(w, http.StatusOK, view)
}
