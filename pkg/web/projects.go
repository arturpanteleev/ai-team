package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

type projectCommand struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type projectUpdateCommand struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// writeProjectError переводит ошибки проекта в HTTP-ответ. Возвращает false,
// если ошибка не относится к проектам и вызывающий должен обработать её сам.
func writeProjectError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrProjectRequired):
		writeJSONAPIError(w, http.StatusBadRequest, "project_id is required: choose a project for the task", "project_required")
	case errors.Is(err, store.ErrProjectNotFound):
		writeJSONAPIError(w, http.StatusNotFound, "project not found", "project_not_found")
	case errors.Is(err, store.ErrProjectArchived):
		writeJSONAPIError(w, http.StatusConflict, err.Error(), "project_archived")
	case errors.Is(err, store.ErrProjectNameTaken):
		writeJSONAPIError(w, http.StatusConflict, err.Error(), "project_name_taken")
	default:
		return false
	}
	return true
}

func (s *Server) projectActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	if err := s.authorize(r, cloudidentity.PermissionProjectManage, ""); err != nil {
		writeJSONAPIError(w, http.StatusForbidden, err.Error(), "forbidden")
		return "", false
	}
	actorID := "local-user"
	if session, ok := s.requestSession(r); ok && session.Principal.ActorID != "" {
		actorID = session.Principal.ActorID
	}
	return actorID, true
}

func parseProjectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "projectID"), 10, 64)
	if err != nil || id <= 0 {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid project id", "bad_request")
		return 0, false
	}
	return id, true
}

func validProjectDescription(description string) bool {
	return len(description) <= store.MaxProjectDescriptionBytes
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("include_archived") == "true" || r.URL.Query().Get("include_archived") == "1"
	projects, err := s.store.ListProjects(includeArchived)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "projects are unavailable", "projects_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseProjectID(w, r)
	if !ok {
		return
	}
	project, err := s.store.GetProject(id)
	if err != nil {
		if !writeProjectError(w, err) {
			writeJSONAPIError(w, http.StatusInternalServerError, "project is unavailable", "projects_unavailable")
		}
		return
	}
	writeJSONResponse(w, http.StatusOK, project)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	actorID, ok := s.projectActor(w, r)
	if !ok {
		return
	}
	var command projectCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	description := ""
	if command.Description != nil {
		description = *command.Description
	}
	if !store.ValidProjectName(command.Name) || !validProjectDescription(description) {
		writeJSONAPIError(w, http.StatusBadRequest, "project requires a non-empty name and a description within limits", "bad_project")
		return
	}
	project, err := s.store.CreateProject(command.Name, strings.TrimSpace(description), actorID, s.clock())
	if err != nil {
		if !writeProjectError(w, err) {
			writeJSONAPIError(w, http.StatusInternalServerError, "project could not be saved", "projects_unavailable")
		}
		return
	}
	writeJSONResponse(w, http.StatusCreated, project)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.projectActor(w, r); !ok {
		return
	}
	id, ok := parseProjectID(w, r)
	if !ok {
		return
	}
	var command projectUpdateCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	current, err := s.store.GetProject(id)
	if err != nil {
		if !writeProjectError(w, err) {
			writeJSONAPIError(w, http.StatusInternalServerError, "project is unavailable", "projects_unavailable")
		}
		return
	}
	name, description := current.Name, current.Description
	if command.Name != nil {
		name = *command.Name
	}
	if command.Description != nil {
		description = strings.TrimSpace(*command.Description)
	}
	if command.Name == nil && command.Description == nil || !store.ValidProjectName(name) || !validProjectDescription(description) {
		writeJSONAPIError(w, http.StatusBadRequest, "project update requires a non-empty name or a description within limits", "bad_project")
		return
	}
	project, err := s.store.UpdateProject(id, name, description, s.clock())
	if err != nil {
		if !writeProjectError(w, err) {
			writeJSONAPIError(w, http.StatusInternalServerError, "project could not be updated", "projects_unavailable")
		}
		return
	}
	writeJSONResponse(w, http.StatusOK, project)
}

func (s *Server) handleArchiveProject(archived bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.projectActor(w, r); !ok {
			return
		}
		id, ok := parseProjectID(w, r)
		if !ok {
			return
		}
		project, err := s.store.SetProjectArchived(id, archived, s.clock())
		if err != nil {
			if !writeProjectError(w, err) {
				writeJSONAPIError(w, http.StatusInternalServerError, "project could not be updated", "projects_unavailable")
			}
			return
		}
		writeJSONResponse(w, http.StatusOK, project)
	}
}

// projectFilter разбирает необязательный фильтр ?project_id= списков.
// Некорректное значение — 400, а не молчаливый показ всех проектов.
func projectFilter(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeJSONAPIError(w, http.StatusBadRequest, "project_id must be a positive integer", "bad_request")
		return 0, false
	}
	return id, true
}
