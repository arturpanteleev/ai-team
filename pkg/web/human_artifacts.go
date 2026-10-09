package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"github.com/go-chi/chi/v5"
)

type artifactRevisionCommand struct {
	ArtifactPath string `json:"artifact_path"`
	BaseRevision string `json:"base_revision,omitempty"`
	BaseSHA256   string `json:"base_sha256,omitempty"`
	Content      string `json:"content,omitempty"`
	Comment      string `json:"comment,omitempty"`
	ActorID      string `json:"actor_id,omitempty"`
}

func (s *Server) handleListArtifactRevisions(w http.ResponseWriter, r *http.Request) {
	if !safeIdentity(chi.URLParam(r, "runID")) {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	artifactPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if !allowedRunArtifactPath(artifactPath) && !allowedStageSubmissionPath(artifactPath) {
		http.Error(w, "invalid artifact path", http.StatusBadRequest)
		return
	}
	values, err := s.humanArtifacts.List(chi.URLParam(r, "runID"), artifactPath)
	if err != nil {
		http.Error(w, "artifact revision history unavailable", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"revisions": values})
}

type stageSubmissionCommand struct {
	Result        string `json:"result"`
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	URL           string `json:"url,omitempty"`
	LinkKind      string `json:"link_kind,omitempty"`
	Note          string `json:"note,omitempty"`
	Description   string `json:"description,omitempty"`
	ActorID       string `json:"actor_id,omitempty"`
	ActorRole     string `json:"actor_role,omitempty"`
}

func (s *Server) handleSubmitHumanStage(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		http.Error(w, "run controller unavailable", http.StatusServiceUnavailable)
		return
	}
	runID, stageID := chi.URLParam(r, "runID"), chi.URLParam(r, "stageID")
	if !safeIdentity(runID) || !safeIdentity(stageID) {
		http.Error(w, "invalid run or stage id", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSubmissionBody)
	var command stageSubmissionCommand
	if err := strictjson.Decode(r.Body, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	approvals, err := s.controller.Approvals(runID)
	if err != nil {
		http.Error(w, "stage approval state unavailable", http.StatusInternalServerError)
		return
	}
	pending, err := findStageInputApproval(approvals, stageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	content, err := stageSubmissionContent(command)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	actorID := strings.TrimSpace(command.ActorID)
	actorRole := strings.TrimSpace(command.ActorRole)
	if s.authenticator != nil {
		session, ok := s.requestSession(r)
		if !ok {
			http.Error(w, "требуется web session", http.StatusUnauthorized)
			return
		}
		actorID = session.Principal.ActorID
		if s.localAuth {
			actorRole = firstRequiredRole(pending.RequiredRoles)
		} else if actorRole == "" {
			for _, required := range pending.RequiredRoles {
				if session.Principal.Has(cloudidentity.Role(required)) {
					actorRole = required
					break
				}
			}
		}
		if err := s.authorize(r, cloudidentity.PermissionDecision, cloudidentity.Role(actorRole)); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	} else if actorID == "" {
		actorID = "local-user"
	}
	if actorRole == "" {
		actorRole = firstRequiredRole(pending.RequiredRoles)
	}
	result, err := s.humanArtifacts.Submit(s.controller, runID, humanartifact.SubmissionCommand{
		StageID: stageID, Result: command.Result, LinkKind: command.LinkKind, Content: content,
		Note: command.Note, Description: command.Description,
		ActorID: actorID, ActorRole: actorRole, ControllerAuthenticated: s.authenticator != nil,
	})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "conflict") || strings.Contains(err.Error(), "no pending") || strings.Contains(err.Error(), "already") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSONResponse(w, http.StatusCreated, map[string]any{
		"approval": result.Approval,
		"submission": map[string]any{"version": result.Revision.Revision, "sha256": result.Revision.SHA256,
			"id": result.Revision.ID, "artifact_path": result.Revision.ArtifactPath},
	})
}

func stageSubmissionContent(command stageSubmissionCommand) (string, error) {
	if command.Result == "approve" {
		if command.Content != "" || command.ContentBase64 != "" || command.URL != "" {
			return "", errors.New("approve result accepts optional text through note only")
		}
		return "", nil
	}
	if command.Content != "" && command.ContentBase64 != "" {
		return "", errors.New("use content or content_base64, not both")
	}
	if command.ContentBase64 != "" {
		data, err := base64.StdEncoding.DecodeString(command.ContentBase64)
		if err != nil {
			data, err = base64.RawStdEncoding.DecodeString(command.ContentBase64)
		}
		if err != nil || !utf8.Valid(data) {
			return "", errors.New("content_base64 must contain valid UTF-8 markdown")
		}
		return string(data), nil
	}
	if command.URL != "" {
		if command.Content != "" {
			return "", errors.New("use url or content, not both")
		}
		return command.URL, nil
	}
	if command.Content == "" && command.Result == "link" {
		return "", errors.New("link result requires url")
	}
	return command.Content, nil
}

func findStageInputApproval(values []approval.PendingApproval, stageID string) (approval.PendingApproval, error) {
	var selected *approval.PendingApproval
	for index := range values {
		value := &values[index]
		if value.Kind == approval.KindInput && value.Trigger == "human_input" && value.FromStage == stageID &&
			value.ToStage == stageID && value.Status == approval.StatusPending {
			if selected != nil {
				return approval.PendingApproval{}, errors.New("more than one pending human input exists for this stage")
			}
			selected = value
		}
	}
	if selected == nil {
		for index := range values {
			value := &values[index]
			if value.Kind == approval.KindInput && value.Trigger == "human_input" && value.FromStage == stageID &&
				value.ToStage == stageID && value.Status == approval.StatusResolved &&
				(selected == nil || value.CreatedAt.After(selected.CreatedAt)) {
				selected = value
			}
		}
	}
	if selected == nil {
		return approval.PendingApproval{}, errors.New("no human input for requested stage")
	}
	return *selected, nil
}

func firstRequiredRole(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	return roles[0]
}

func allowedStageSubmissionPath(value string) bool {
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if cleaned != value {
		return false
	}
	parts := strings.Split(cleaned, "/")
	return len(parts) == 3 && parts[0] == "stages" && safeIdentity(parts[1]) &&
		(parts[2] == "result.md" || parts[2] == "result.link" || parts[2] == "result.txt")
}

func (s *Server) handleCreateArtifactRevision(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionArtifactEdit, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if !safeIdentity(chi.URLParam(r, "runID")) {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	var command artifactRevisionCommand
	if err := decodeCommand(w, r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !allowedRunArtifactPath(command.ArtifactPath) {
		http.Error(w, "invalid artifact path", http.StatusBadRequest)
		return
	}
	parts := strings.Split(command.ArtifactPath, "/")
	if len(parts) >= 3 && parts[0] == "attempts" {
		approvals, approvalErr := s.controller.Approvals(chi.URLParam(r, "runID"))
		if approvalErr != nil {
			http.Error(w, "approval state unavailable", http.StatusInternalServerError)
			return
		}
		for _, pending := range approvals {
			if pending.AttemptID == parts[1] && pending.Status == "resolved" {
				http.Error(w, "этот handoff уже подтверждён; создайте новый цикл возврата для изменения артефакта", http.StatusConflict)
				return
			}
		}
	}
	actorID := strings.TrimSpace(command.ActorID)
	if session, ok := s.requestSession(r); ok && session.Principal.ActorID != "" {
		actorID = session.Principal.ActorID
	}
	if actorID == "" {
		actorID = "local-user"
	}
	// The first revision is anchored to the immutable source artifact. Later
	// revisions are anchored to the preceding human revision by the store.
	if command.BaseRevision == "" {
		path, err := resolveArtifactPath(filepath.Join(s.runRoot, chi.URLParam(r, "runID")), command.ArtifactPath)
		if err != nil {
			http.Error(w, "source artifact unavailable", http.StatusNotFound)
			return
		}
		file, err := safeio.ReadRegularFile(path, maxArtifactSize)
		if err != nil {
			http.Error(w, "source artifact unavailable", http.StatusNotFound)
			return
		}
		digest := sha256.Sum256(file)
		if command.BaseSHA256 == "" || !strings.EqualFold(command.BaseSHA256, hex.EncodeToString(digest[:])) {
			http.Error(w, "source artifact changed; reload before editing", http.StatusConflict)
			return
		}
	}
	value, err := s.humanArtifacts.Append(chi.URLParam(r, "runID"), command.ArtifactPath,
		command.BaseRevision, command.BaseSHA256, command.Content, command.Comment, actorID)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "conflict") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSONResponse(w, http.StatusCreated, value)
}
