package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/go-chi/chi/v5"
)

type identityIssuer interface {
	Issue(cloudidentity.Principal, time.Duration) (string, error)
}

func (s *Server) teamReadSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.requestSession(r)
		if !ok {
			http.Error(w, "требуется активная web session", http.StatusUnauthorized)
			return
		}
		if err := cloudidentity.Authorize(session.Principal, cloudidentity.PermissionTeamManage, ""); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) teamWriteSecurity(next http.Handler) http.Handler {
	return s.teamReadSecurity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.requestSession(r)
		if !ok {
			http.Error(w, "требуется активная web session", http.StatusUnauthorized)
			return
		}
		if !constantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
			http.Error(w, "неверный CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) handleTeamMembers(w http.ResponseWriter, _ *http.Request) {
	members, err := s.store.ListTeamMembers()
	if err != nil {
		http.Error(w, "team registry unavailable", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) handleTeamAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListTeamAudit(100)
	if err != nil {
		http.Error(w, "team audit unavailable", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"events": entries})
}

type inviteMemberCommand struct {
	Email string   `json:"email"`
	Roles []string `json:"roles"`
}

func (s *Server) handleTeamInvite(w http.ResponseWriter, r *http.Request) {
	var command inviteMemberCommand
	if err := decodeCommand(w, r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	address, err := mail.ParseAddress(strings.TrimSpace(command.Email))
	if err != nil || address.Address != strings.TrimSpace(command.Email) || len(address.Address) > 200 {
		http.Error(w, "нужен корректный email", http.StatusBadRequest)
		return
	}
	roles, err := cloudidentity.ParseRoles(command.Roles)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	roleNames := make([]string, len(roles))
	for i, role := range roles {
		roleNames[i] = string(role)
	}
	actorID := strings.ToLower(address.Address)
	activation, err := randomToken()
	if err != nil {
		http.Error(w, "activation token unavailable", http.StatusInternalServerError)
		return
	}
	digest := sha256.Sum256([]byte(activation))
	session, _ := s.requestSession(r)
	now := time.Now().UTC()
	if err = s.store.InviteTeamMember(actorID, actorID, roleNames, hex.EncodeToString(digest[:]), now.Add(48*time.Hour), now, session.Principal.ActorID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			http.Error(w, "участник или приглашение уже существует", http.StatusConflict)
			return
		}
		http.Error(w, "не удалось создать приглашение", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusCreated, map[string]any{"actor_id": actorID, "email": actorID, "roles": roleNames, "activation_token": activation, "expires_at": now.Add(48 * time.Hour)})
}

type activateTeamCommand struct {
	Token string `json:"token"`
}

func (s *Server) handleTeamActivation(w http.ResponseWriter, r *http.Request) {
	var command activateTeamCommand
	if err := decodeCommand(w, r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(command.Token) < 32 || len(command.Token) > 256 {
		http.Error(w, "invalid invitation", http.StatusBadRequest)
		return
	}
	issuer, ok := s.authenticator.(identityIssuer)
	if !ok {
		http.Error(w, "identity issuer unavailable", http.StatusServiceUnavailable)
		return
	}
	digest := sha256.Sum256([]byte(command.Token))
	var accessToken string
	member, err := s.store.ActivateTeamInvitation(hex.EncodeToString(digest[:]), time.Now().UTC(), func(member *store.TeamMember) error {
		roles, parseErr := cloudidentity.ParseRoles(member.Roles)
		if parseErr != nil {
			return parseErr
		}
		principal, principalErr := cloudidentity.NewPrincipal(member.ActorID, roles)
		if principalErr != nil {
			return principalErr
		}
		var issueErr error
		accessToken, issueErr = issuer.Issue(principal, 8*time.Hour)
		return issueErr
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidInvitation) {
			http.Error(w, "приглашение истекло или уже использовано", http.StatusGone)
			return
		}
		http.Error(w, "не удалось активировать приглашение", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"member": member, "access_token": accessToken, "message": "Участник активирован."})
}

type setRolesCommand struct {
	Roles []string `json:"roles"`
}

func (s *Server) handleTeamRoles(w http.ResponseWriter, r *http.Request) {
	actorID := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "actorID")))
	var command setRolesCommand
	if err := decodeCommand(w, r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	roles, err := cloudidentity.ParseRoles(command.Roles)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	roleNames := make([]string, len(roles))
	for i, role := range roles {
		roleNames[i] = string(role)
	}
	session, _ := s.requestSession(r)
	if actorID == session.Principal.ActorID {
		http.Error(w, "нельзя менять собственные роли", http.StatusConflict)
		return
	}
	existing, lookupErr := s.store.TeamMember(actorID)
	if lookupErr != nil {
		http.Error(w, "team registry unavailable", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		http.Error(w, "участник не найден", http.StatusNotFound)
		return
	}
	if existing.Status != "active" {
		http.Error(w, "сначала активируйте приглашение", http.StatusConflict)
		return
	}
	if err = s.store.SetTeamMember(actorID, roleNames, true, session.Principal.ActorID, "roles_changed", time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrTeamMemberNotFound) {
			http.Error(w, "участник не найден", http.StatusNotFound)
			return
		}
		if errors.Is(err, store.ErrLastTeamAdmin) {
			http.Error(w, "нельзя удалить последнего Product Owner", http.StatusConflict)
			return
		}
		http.Error(w, "не удалось изменить роли", http.StatusInternalServerError)
		return
	}
	member, _ := s.store.TeamMember(actorID)
	writeJSONResponse(w, http.StatusOK, map[string]any{"member": member})
}

func (s *Server) handleTeamRevoke(w http.ResponseWriter, r *http.Request) {
	actorID := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "actorID")))
	session, _ := s.requestSession(r)
	if actorID == session.Principal.ActorID {
		http.Error(w, "нельзя отзывать собственное членство", http.StatusConflict)
		return
	}
	if err := s.store.SetTeamMember(actorID, nil, false, session.Principal.ActorID, "membership_revoked", time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrTeamMemberNotFound) {
			http.Error(w, "участник не найден", http.StatusNotFound)
			return
		}
		if errors.Is(err, store.ErrLastTeamAdmin) {
			http.Error(w, "нельзя удалить последнего Product Owner", http.StatusConflict)
			return
		}
		http.Error(w, "не удалось отозвать членство", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]string{"actor_id": actorID, "status": "revoked"})
}
