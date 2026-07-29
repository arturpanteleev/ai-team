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
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
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
	var principal cloudidentity.Principal
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
		CSRFToken: csrfToken, Principal: principal, ExpiresAt: time.Now().UTC().Add(browserSessionTTL),
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
	writeJSONResponse(w, http.StatusOK, response)
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
	runID, err := s.controller.Start(command.Feature, command.Task)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
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
	if err := s.controller.Cancel(runID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

type decisionCommand struct {
	ActorID     string `json:"actor_id"`
	ActorRole   string `json:"actor_role"`
	Action      string `json:"action"`
	Comment     string `json:"comment,omitempty"`
	SubjectHash string `json:"subject_hash"`
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
	value, err := s.controller.Decide(
		chi.URLParam(r, "runID"), chi.URLParam(r, "approvalID"),
		approval.Decision{
			ActorID: actorID, ActorRole: command.ActorRole,
			Action: command.Action, Comment: command.Comment,
			SubjectHash: command.SubjectHash,
		},
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSONResponse(w, http.StatusOK, value)
}

func decodeCommand(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("невалидный JSON command: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("command должен содержать один JSON document")
	}
	return nil
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
