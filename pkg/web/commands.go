package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

const browserSessionTTL = 8 * time.Hour

type sessionResponse struct {
	CSRFToken string                   `json:"csrf_token"`
	Principal *cloudidentity.Principal `json:"principal,omitempty"`
}

func (s *Server) resolveTeamPrincipal(principal cloudidentity.Principal) (cloudidentity.Principal, int64, error) {
	if s.authenticator == nil {
		return principal, 0, nil
	}
	member, err := s.store.TeamMember(principal.ActorID)
	if err != nil {
		return cloudidentity.Principal{}, 0, err
	}
	if member == nil {
		count, countErr := s.store.TeamMemberCount()
		if countErr != nil {
			return cloudidentity.Principal{}, 0, countErr
		}
		if count == 0 && principal.Has(cloudidentity.RoleProductOwner) {
			if err = s.store.BootstrapTeamAdmin(principal.ActorID, time.Now().UTC()); err != nil {
				// Another first login may have won the bootstrap race.
				member, err = s.store.TeamMember(principal.ActorID)
				if err != nil || member == nil {
					return cloudidentity.Principal{}, 0, errors.New("team registry is already initialized")
				}
			} else {
				var lookupErr error
				member, lookupErr = s.store.TeamMember(principal.ActorID)
				if lookupErr != nil {
					return cloudidentity.Principal{}, 0, lookupErr
				}
			}
		}
	}
	if member == nil || member.Status != "active" {
		return cloudidentity.Principal{}, 0, errors.New("пользователь не активен в команде")
	}
	roles, err := cloudidentity.ParseRoles(member.Roles)
	if err != nil {
		return cloudidentity.Principal{}, 0, err
	}
	canonical, err := cloudidentity.NewPrincipal(member.ActorID, roles)
	return canonical, member.SessionEpoch, err
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSONResponse(w, http.StatusOK, map[string]bool{"authentication_required": s.authenticator != nil})
}

func (s *Server) handleCurrentIdentity(w http.ResponseWriter, r *http.Request) {
	response := map[string]any{"authentication_required": s.authenticator != nil}
	if session, ok := s.requestSession(r); ok && s.authenticator != nil {
		response["principal"] = session.Principal
	}
	writeJSONResponse(w, http.StatusOK, response)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	// An authenticated browser recovers its CSRF token from the HttpOnly
	// session cookie after reload. This endpoint is deliberately same-origin:
	// don't disclose the token to a request that cannot prove it came from this
	// origin, even though the cookie is also SameSite=Strict.
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" &&
		strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		if !isSameOriginSessionRequest(r) {
			http.Error(w, "требуется same-origin session request", http.StatusForbidden)
			return
		}
		if session, ok := s.requestSession(r); ok {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Add("Vary", "Cookie")
			response := sessionResponse{CSRFToken: session.CSRFToken}
			if s.authenticator != nil {
				response.Principal = &session.Principal
			}
			writeJSONResponse(w, http.StatusOK, response)
			return
		}
		if s.authenticator != nil {
			http.Error(w, "сессия истекла, войдите снова", http.StatusUnauthorized)
			return
		}
	}

	var principal cloudidentity.Principal
	var sessionEpoch int64
	if s.authenticator != nil {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "требуется Bearer token", http.StatusUnauthorized)
			return
		}
		var err error
		principal, err = s.authenticator.Verify(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
		if err != nil {
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}
		principal, sessionEpoch, err = s.resolveTeamPrincipal(principal)
		if err != nil {
			http.Error(w, "участник команды не активен или доступ отозван", http.StatusForbidden)
			return
		}
	}
	sessionToken, err := randomToken()
	if err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	csrfToken, err := randomToken()
	if err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	s.sessionMu.Lock()
	s.sessions[sessionToken] = browserSession{
		CSRFToken: csrfToken, Principal: principal, SessionEpoch: sessionEpoch, ExpiresAt: time.Now().UTC().Add(browserSessionTTL),
	}
	s.sessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: sessionToken, Path: "/",
		HttpOnly: true, Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		SameSite: http.SameSiteStrictMode,
	})
	response := sessionResponse{CSRFToken: csrfToken}
	if s.authenticator != nil {
		response.Principal = &principal
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Cookie")
	writeJSONResponse(w, http.StatusOK, response)
}

func isSameOriginSessionRequest(r *http.Request) bool {
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		u, err := url.Parse(origin)
		requestScheme := "http"
		if r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
			requestScheme = "https"
		}
		return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host) && u.Scheme == requestScheme
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "same-origin")
}

func (s *Server) writeSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.requestSession(r)
		if !ok {
			http.Error(w, "требуется web session", http.StatusUnauthorized)
			return
		}
		if !constantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
			http.Error(w, "неверный CSRF token", http.StatusForbidden)
			return
		}
		if s.controller == nil {
			http.Error(w, "web control plane не настроен", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) readSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authenticator != nil {
			if _, ok := s.requestSession(r); !ok {
				http.Error(w, "требуется web session", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestSession(r *http.Request) (browserSession, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return browserSession{}, false
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	session, ok := s.sessions[cookie.Value]
	if !ok {
		return browserSession{}, false
	}
	if !session.ExpiresAt.After(time.Now().UTC()) {
		delete(s.sessions, cookie.Value)
		return browserSession{}, false
	}
	if s.authenticator != nil && session.Principal.ActorID != "" {
		member, err := s.store.ValidateTeamSession(session.Principal.ActorID, session.SessionEpoch)
		if err != nil || member == nil {
			delete(s.sessions, cookie.Value)
			return browserSession{}, false
		}
	}
	return session, true
}

func (s *Server) authorize(r *http.Request, permission cloudidentity.Permission, role cloudidentity.Role) error {
	if s.authenticator == nil {
		return nil
	}
	session, ok := s.requestSession(r)
	if !ok {
		return errors.New("требуется web session")
	}
	return cloudidentity.Authorize(session.Principal, permission, role)
}

func constantTimeEqual(actual, expected string) bool {
	if len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

type startRunCommand struct {
	Feature string `json:"feature"`
	Task    string `json:"task"`
}

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionStart, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var command startRunCommand
	if err := decodeCommand(w, r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	actorID := "local-user"
	if session, ok := s.requestSession(r); ok && session.Principal.ActorID != "" {
		actorID = session.Principal.ActorID
	}
	now := time.Now().UTC()
	admissionSnapshot, err := json.Marshal(command)
	if err != nil {
		http.Error(w, "invalid admission snapshot", http.StatusBadRequest)
		return
	}
	admissionFailed := false
	runID, err := s.controller.StartWithAdmission(command.Feature, command.Task, func(runID string) error {
		if err := s.store.AdmitPipelineRun(&store.PipelineRun{
			RunID: runID, Feature: command.Feature, Status: "queued", StartedAt: now, ConfigSnapshot: string(admissionSnapshot),
		}); err != nil {
			admissionFailed = true
			return err
		}
		return nil
	})
	if err != nil {
		status := http.StatusBadRequest
		if admissionFailed {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.appendRunEvent(runID, "run_queued", now, map[string]any{"status": "queued", "actor_id": actorID})
	writeJSONResponse(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

func (s *Server) handleResumeRun(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionResume, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := requireEmptyCommand(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runID := chi.URLParam(r, "runID")
	if err := s.controller.Resume(runID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

func (s *Server) handleRetryDelivery(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionDeliver, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := requireEmptyCommand(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runID := chi.URLParam(r, "runID")
	run, err := s.store.GetPipelineRunByRunID(runID)
	if err != nil || run == nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	actorID := "local-user"
	if session, ok := s.requestSession(r); ok && session.Principal.ActorID != "" {
		actorID = session.Principal.ActorID
	}
	requestedAt := time.Now().UTC()
	// Persist intent before the controller can run Git/hosting commands. Later
	// outcome events are projections; a missing one cannot erase this audit row.
	if err := s.appendRunEventRequired(runID, "delivery_retry_requested", requestedAt, map[string]any{"actor_id": actorID}); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ delivery retry intent audit for run %s: %v\n", runID, err)
		http.Error(w, "delivery retry could not be recorded", http.StatusServiceUnavailable)
		return
	}
	record, err := s.controller.DeliverDeferred(r.Context(), runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ delivery retry failed for run %s: %v\n", runID, err)
		s.appendRunEvent(runID, "delivery_retry_failed", time.Now().UTC(), map[string]any{
			"actor_id": actorID, "error": "delivery retry failed; see controller diagnostics",
		})
		http.Error(w, "delivery retry failed", http.StatusConflict)
		return
	}
	s.appendRunEvent(runID, "delivery_retry_succeeded", time.Now().UTC(), map[string]any{
		"actor_id": actorID, "plan_hash": record.PlanHash, "commit_sha": record.CommitSHA, "pr_url": record.PRURL,
	})
	writeJSONResponse(w, http.StatusOK, map[string]any{"run_id": runID, "delivery": record})
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionCancel, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := requireEmptyCommand(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runID := chi.URLParam(r, "runID")
	actorID := "local-user"
	if session, ok := s.requestSession(r); ok && session.Principal.ActorID != "" {
		actorID = session.Principal.ActorID
	}
	if err := s.controller.Cancel(runID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.appendRunEvent(runID, "run_cancel_requested", time.Now().UTC(), map[string]any{"actor_id": actorID})
	if canceled, err := s.store.MarkQueuedCanceled(runID, time.Now().UTC()); err == nil && canceled {
		s.appendRunEvent(runID, "run_canceled", time.Now().UTC(), map[string]any{"status": "canceled"})
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

type decisionCommand struct {
	ActorID           string            `json:"actor_id"`
	ActorRole         string            `json:"actor_role"`
	Action            string            `json:"action"`
	Comment           string            `json:"comment,omitempty"`
	SubjectHash       string            `json:"subject_hash"`
	ArtifactRevisions map[string]string `json:"artifact_revisions,omitempty"`
}

func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	var command decisionCommand
	if err := decodeCommand(w, r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	actorID := command.ActorID
	if s.authenticator != nil {
		role := cloudidentity.Role(command.ActorRole)
		if err := s.authorize(r, cloudidentity.PermissionDecision, role); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		session, _ := s.requestSession(r)
		actorID = session.Principal.ActorID
	}
	if command.Action == "answer_questions" || command.Action == "approve_spec" || command.Action == "approve" || command.Action == "reject" {
		approvals, listErr := s.controller.Approvals(chi.URLParam(r, "runID"))
		if listErr != nil {
			http.Error(w, "не удалось проверить human approval", http.StatusInternalServerError)
			return
		}
		var matched *approval.PendingApproval
		for _, pending := range approvals {
			if pending.ID != chi.URLParam(r, "approvalID") {
				continue
			}
			matched = &pending
			break
		}
		if matched != nil {
			var payload struct {
				Kind string `json:"kind"`
			}
			_ = json.Unmarshal(matched.Payload, &payload)
			if command.Action == "answer_questions" {
				if payload.Kind != "questions" || !containsApprovalRole(matched.RequiredRoles, "product_owner") {
					http.Error(w, "answer_questions разрешён только для Product Owner вопроса analyst", http.StatusConflict)
					return
				}
				if command.ActorRole != "product_owner" {
					http.Error(w, "на вопрос analyst может ответить только Product Owner", http.StatusForbidden)
					return
				}
				if strings.TrimSpace(command.Comment) == "" || len(command.Comment) > 16<<10 {
					http.Error(w, "ответ должен содержать от 1 до 16384 байт", http.StatusBadRequest)
					return
				}
			}
			if payload.Kind == "agreed_spec" {
				if !containsApprovalRole(matched.RequiredRoles, "product_owner") {
					http.Error(w, "согласование ТЗ не назначено Product Owner", http.StatusConflict)
					return
				}
				if command.ActorRole != "product_owner" {
					http.Error(w, "согласовать ТЗ может только Product Owner", http.StatusForbidden)
					return
				}
			} else if command.Action == "approve_spec" {
				http.Error(w, "approve_spec разрешён только для Product Owner согласования ТЗ", http.StatusConflict)
				return
			}
		} else if command.Action == "answer_questions" || command.Action == "approve_spec" {
			http.Error(w, "не найден соответствующий Product Owner approval", http.StatusConflict)
			return
		}
	}
	selectedRevisions := command.ArtifactRevisions
	pinnedSelection := false
	if len(selectedRevisions) == 0 {
		// A quorum-all approval pins its artifacts on the first vote. Reuse that
		// exact selection for later voters even if somebody has appended a newer
		// human revision while the approval remains pending. The first decision
		// is authoritative even when it pinned an empty selection (for example,
		// when the artifact had no human revision yet).
		values, listErr := s.controller.Approvals(chi.URLParam(r, "runID"))
		if listErr != nil {
			http.Error(w, "approval state unavailable", http.StatusInternalServerError)
			return
		}
		for _, pending := range values {
			if pending.ID == chi.URLParam(r, "approvalID") && len(pending.Decisions) > 0 {
				pinned := pending.Decisions[0].ArtifactRevisions
				pinnedSelection = true
				selectedRevisions = make(map[string]string, len(pinned))
				for path, revisionID := range pinned {
					selectedRevisions[path] = revisionID
				}
				break
			}
		}
	}
	if len(selectedRevisions) == 0 && !pinnedSelection {
		attemptID, attemptErr := s.approvalAttemptID(chi.URLParam(r, "runID"), chi.URLParam(r, "approvalID"))
		if attemptErr != nil {
			http.Error(w, "не удалось определить артефакты approval", http.StatusInternalServerError)
			return
		}
		revisionSelection, revisionErr := s.latestAttemptRevisions(chi.URLParam(r, "runID"), attemptID)
		if revisionErr != nil {
			http.Error(w, "не удалось закрепить текущие версии артефактов", http.StatusConflict)
			return
		}
		selectedRevisions = revisionSelection
	}
	value, err := s.controller.Decide(
		chi.URLParam(r, "runID"), chi.URLParam(r, "approvalID"),
		approval.Decision{
			ActorID: actorID, ActorRole: command.ActorRole,
			Action: command.Action, Comment: command.Comment,
			SubjectHash:       command.SubjectHash,
			ArtifactRevisions: selectedRevisions,
		},
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSONResponse(w, http.StatusOK, value)
}

func (s *Server) approvalAttemptID(runID, approvalID string) (string, error) {
	values, err := s.controller.Approvals(runID)
	if err != nil {
		return "", err
	}
	for _, value := range values {
		if value.ID == approvalID {
			return value.AttemptID, nil
		}
	}
	return "", nil
}

func (s *Server) latestAttemptRevisions(runID, attemptID string) (map[string]string, error) {
	if !safeIdentity(runID) || !safeIdentity(attemptID) {
		return nil, nil
	}
	_, manifest, err := evidence.ReadAttemptManifest(nil, filepath.Join(s.runRoot, runID), runID, attemptID)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if manifest.RunID != runID || manifest.AttemptID != attemptID {
		return nil, errors.New("approval attempt manifest identity mismatch")
	}
	selection := make(map[string]string)
	for _, output := range manifest.Outputs {
		revisions, listErr := s.humanArtifacts.List(runID, output.EvidencePath)
		if listErr != nil {
			return nil, listErr
		}
		if len(revisions) > 0 {
			selection[output.EvidencePath] = revisions[len(revisions)-1].ID
		}
	}
	return selection, nil
}

func containsApprovalRole(roles []string, expected string) bool {
	for _, role := range roles {
		if role == expected {
			return true
		}
	}
	return false
}

func decodeCommand(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBody)
	return strictjson.Decode(r.Body, destination)
}

func requireEmptyCommand(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBody)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != "" && strings.TrimSpace(string(data)) != "{}" {
		return errors.New("command body должен быть пустым")
	}
	return nil
}

func writeJSONResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
