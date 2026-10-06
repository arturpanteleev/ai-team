package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
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
	if !allowedRunArtifactPath(artifactPath) {
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
