package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
	"github.com/go-chi/chi/v5"
)

type templateCommand struct {
	YAML            string `json:"yaml"`
	ExpectedVersion string `json:"expected_version,omitempty"`
}

type templateValidation struct {
	YAML       string               `json:"yaml,omitempty"`
	Valid      bool                 `json:"valid"`
	Diagnostic string               `json:"diagnostic,omitempty"`
	Template   string               `json:"template,omitempty"`
	Title      string               `json:"title,omitempty"`
	Version    string               `json:"version,omitempty"`
	Graph      *workflow.Graph      `json:"graph,omitempty"`
	Stages     []templateStageView  `json:"stages,omitempty"`
	Returns    []templateReturnView `json:"returns,omitempty"`
}

type templateStageView struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Function  string `json:"function"`
	Result    string `json:"result"`
	Executor  string `json:"executor"`
	Agent     string `json:"agent,omitempty"`
	MaxVisits int    `json:"max_visits,omitempty"`
}

type templateReturnView struct {
	From      string `json:"from"`
	To        string `json:"to"`
	MaxVisits int    `json:"max_visits,omitempty"`
}

func (s *Server) templateStore() (*config.TemplateStore, error) {
	return config.NewTemplateStore(s.targetDir)
}

func (s *Server) parseTemplateYAML(raw []byte) (*config.Config, workflow.Graph, error) {
	cfg, err := config.ParseYAML(raw)
	if err != nil {
		return nil, workflow.Graph{}, err
	}
	if s.templateAgentLookup == nil {
		for _, stage := range cfg.Stages {
			if stage.Agent != "" {
				return nil, workflow.Graph{}, fmt.Errorf("agent registry недоступен: нельзя проверить агента %q", stage.Agent)
			}
		}
	}
	if err := cfg.Validate(s.templateAgentLookup); err != nil {
		return nil, workflow.Graph{}, err
	}
	graph, err := cfg.CompiledGraph()
	if err != nil {
		return nil, workflow.Graph{}, err
	}
	return cfg, graph, nil
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, _ *http.Request) {
	store, err := s.templateStore()
	if err != nil {
		http.Error(w, "template store unavailable", http.StatusInternalServerError)
		return
	}
	raw, version, err := store.ReadCurrent()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "project template not found", http.StatusNotFound)
		} else {
			http.Error(w, "project template unavailable", http.StatusInternalServerError)
		}
		return
	}
	cfg, graph, validationErr := s.parseTemplateYAML(raw)
	response := templateValidation{YAML: string(raw), Version: version, Valid: validationErr == nil}
	if validationErr != nil {
		response.Diagnostic = validationErr.Error()
	} else {
		response = describeTemplate(response, cfg, graph)
	}
	versions, listErr := store.ListVersions()
	if listErr != nil {
		http.Error(w, "template versions unavailable", http.StatusInternalServerError)
		return
	}
	found := false
	for _, value := range versions {
		found = found || value.ID == version
	}
	if !found {
		versions = append(versions, config.TemplateVersion{ID: version})
		sort.Slice(versions, func(i, j int) bool { return versions[i].ID > versions[j].ID })
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"template": response, "versions": versions})
}

func (s *Server) handleGetTemplateVersions(w http.ResponseWriter, _ *http.Request) {
	store, err := s.templateStore()
	if err != nil {
		http.Error(w, "template store unavailable", http.StatusInternalServerError)
		return
	}
	values, err := store.ListVersions()
	if err != nil {
		http.Error(w, "template versions unavailable", http.StatusInternalServerError)
		return
	}
	_, currentVersion, currentErr := store.ReadCurrent()
	if currentErr == nil {
		found := false
		for _, value := range values {
			found = found || value.ID == currentVersion
		}
		if !found {
			values = append(values, config.TemplateVersion{ID: currentVersion})
			sort.Slice(values, func(i, j int) bool { return values[i].ID > values[j].ID })
		}
	} else if !errors.Is(currentErr, os.ErrNotExist) {
		http.Error(w, "project template unavailable", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"versions": values})
}

func (s *Server) handleValidateTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionTemplateEdit, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	command, err := decodeTemplateCommand(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result := templateValidation{Version: config.TemplateVersionID([]byte(command.YAML))}
	cfg, graph, err := s.parseTemplateYAML([]byte(command.YAML))
	if err != nil {
		result.Diagnostic = err.Error()
		writeJSONResponse(w, http.StatusOK, result)
		return
	}
	result = describeTemplate(result, cfg, graph)
	result.Valid = true
	writeJSONResponse(w, http.StatusOK, result)
}

func describeTemplate(result templateValidation, cfg *config.Config, graph workflow.Graph) templateValidation {
	result.Template, result.Title, result.Graph = cfg.Template, cfg.Title, &graph
	result.Stages = make([]templateStageView, 0, len(cfg.Stages))
	for _, stage := range cfg.Stages {
		visits := 0
		if cfg.MaxVisits != nil {
			visits = cfg.MaxVisits[stage.ID]
		}
		if visits == 0 {
			for _, route := range cfg.Returns {
				if route.To == stage.ID {
					visits = route.MaxVisits
					if visits == 0 {
						visits = 3
					}
					break
				}
			}
		}
		result.Stages = append(result.Stages, templateStageView{
			ID: stage.ID, Title: stage.Title, Function: stage.Function, Result: stage.Result,
			Executor: stage.Executor, Agent: stage.Agent, MaxVisits: visits,
		})
	}
	for _, route := range cfg.Returns {
		result.Returns = append(result.Returns, templateReturnView{From: route.From, To: route.To, MaxVisits: route.MaxVisits})
	}
	return result
}

func (s *Server) handlePublishTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionTemplateEdit, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	command, err := decodeTemplateCommand(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, _, err := s.parseTemplateYAML([]byte(command.YAML)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	store, err := s.templateStore()
	if err != nil {
		http.Error(w, "template store unavailable", http.StatusInternalServerError)
		return
	}
	current, currentVersion, err := store.ReadCurrent()
	if err != nil {
		http.Error(w, "project template unavailable", http.StatusInternalServerError)
		return
	}
	if command.ExpectedVersion == "" || command.ExpectedVersion != currentVersion {
		http.Error(w, "активный шаблон изменился: обновите страницу перед публикацией", http.StatusConflict)
		return
	}
	if err := s.pinUnversionedInFlightRuns(store, current); err != nil {
		http.Error(w, "не удалось закрепить версии текущих задач: "+err.Error(), http.StatusInternalServerError)
		return
	}
	version, err := store.Publish([]byte(command.YAML), command.ExpectedVersion)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"version": version})
}

func (s *Server) pinUnversionedInFlightRuns(templates *config.TemplateStore, current []byte) error {
	count, err := s.store.CountPipelineRuns()
	if err != nil {
		return err
	}
	for offset := 0; offset < count; offset += 100 {
		runs, err := s.store.GetPipelineRunsPage(100, offset)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.RunID == "" || run.CompletedAt != nil {
				continue
			}
			if _, _, found, err := templates.ReadPinnedRun(run.RunID); err != nil {
				return fmt.Errorf("run %s: %w", run.RunID, err)
			} else if found {
				continue
			}
			if _, err := templates.PinDataForRun(run.RunID, run.RunID, current); err != nil {
				return fmt.Errorf("run %s: %w", run.RunID, err)
			}
		}
	}
	return nil
}

func (s *Server) handleGetRunTemplateVersion(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if !safeIdentity(runID) {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetPipelineRunByRunID(runID); err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	store, err := s.templateStore()
	if err != nil {
		http.Error(w, "template store unavailable", http.StatusInternalServerError)
		return
	}
	_, version, found, err := store.ReadPinnedRun(runID)
	if err != nil {
		http.Error(w, "task template pin unavailable", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "task template pin not found", http.StatusNotFound)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]string{"version": version})
}

func decodeTemplateCommand(w http.ResponseWriter, r *http.Request) (templateCommand, error) {
	r.Body = http.MaxBytesReader(w, r.Body, config.MaxTemplateYAMLBytes+8192)
	var command templateCommand
	if err := strictTemplateDecode(r, &command); err != nil {
		return templateCommand{}, err
	}
	if strings.TrimSpace(command.YAML) == "" {
		return templateCommand{}, fmt.Errorf("YAML шаблона обязателен")
	}
	if command.ExpectedVersion != "" && len(command.ExpectedVersion) != 64 {
		return templateCommand{}, fmt.Errorf("expected_version должен быть SHA-256 hex")
	}
	return command, nil
}

func strictTemplateDecode(r *http.Request, destination any) error {
	// Keep the editor's 1 MiB YAML allowance while retaining strict JSON
	// decoding and one-document semantics.
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return strictjson.Unmarshal(data, config.MaxTemplateYAMLBytes+8192, destination)
}
