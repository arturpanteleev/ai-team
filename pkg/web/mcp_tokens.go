package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// Personal MCP tokens. A signed-in person mints a long-lived bearer token for
// their own agents (Claude, Codex). Only the caller can mint, list or revoke
// tokens, always for themselves; the secret is shown once and only its SHA-256
// digest is stored. The REST routes below are reachable with a browser session
// and its CSRF token only: neither an MCP token nor MCP tools can mint tokens.

const (
	mcpTokenDefaultTTL   = 90 * 24 * time.Hour
	mcpTokenMaxTTLDays   = 365
	mcpTokenMaxLabelRune = 80
)

type issueMCPTokenCommand struct {
	Label   string `json:"label"`
	TTLDays int    `json:"ttl_days"`
}

type issuedMCPToken struct {
	store.MCPToken
	// Token is the secret. It is returned once, at creation.
	Token string `json:"token"`
}

func newMCPTokenSecret() (secret, id string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	idBytes := make([]byte, 8)
	if _, err = rand.Read(idBytes); err != nil {
		return "", "", err
	}
	return store.MCPTokenPrefix + base64.RawURLEncoding.EncodeToString(raw), hex.EncodeToString(idBytes), nil
}

// mcpTokenSession admits only a live, team-resolved browser session in cloud
// mode; mutating calls also need the session's CSRF token.
func (s *Server) mcpTokenSession(mutating bool, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authenticator == nil || s.localAuth {
			writeJSONAPIError(w, http.StatusConflict,
				"personal MCP tokens exist only on a cloud-authenticated server; on a local server use the local web token as the bearer", "tokens_unavailable")
			return
		}
		session, ok := s.requestSession(r)
		if !ok {
			writeJSONAPIError(w, http.StatusUnauthorized, "web session required", "unauthorized")
			return
		}
		if session.AuthMethod == "mcp" {
			// Personal tokens are minted from a browser session only: an MCP
			// credential never manages credentials, not even through a crafted
			// internal request.
			writeJSONAPIError(w, http.StatusForbidden, "MCP credentials cannot manage MCP tokens", "forbidden")
			return
		}
		if mutating && !constantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
			writeJSONAPIError(w, http.StatusForbidden, "invalid CSRF token", "forbidden")
			return
		}
		next(w, r)
	})
}

func (s *Server) handleListMCPTokens(w http.ResponseWriter, r *http.Request) {
	session, _ := s.requestSession(r)
	tokens, err := s.store.ListMCPTokens(session.Principal.ActorID)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "tokens unavailable", "internal_error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONResponse(w, http.StatusOK, map[string]any{"tokens": tokens, "max_active": store.MaxActiveMCPTokens})
}

func (s *Server) handleIssueMCPToken(w http.ResponseWriter, r *http.Request) {
	var command issueMCPTokenCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid token request", "bad_request")
		return
	}
	label := strings.TrimSpace(command.Label)
	if label == "" || utf8.RuneCountInString(label) > mcpTokenMaxLabelRune || strings.ContainsAny(label, "\x00\r\n") || !utf8.ValidString(label) {
		writeJSONAPIError(w, http.StatusBadRequest, "label is required (at most 80 characters, single line)", "bad_label")
		return
	}
	ttl := mcpTokenDefaultTTL
	if command.TTLDays != 0 {
		if command.TTLDays < 1 || command.TTLDays > mcpTokenMaxTTLDays {
			writeJSONAPIError(w, http.StatusBadRequest, "ttl_days must be between 1 and 365", "bad_ttl")
			return
		}
		ttl = time.Duration(command.TTLDays) * 24 * time.Hour
	}
	// The owner is always the authenticated caller; no request field names
	// another person.
	session, _ := s.requestSession(r)
	secret, id, err := newMCPTokenSecret()
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "token generation failed", "internal_error")
		return
	}
	now := s.clock()
	token := store.MCPToken{ID: id, ActorID: session.Principal.ActorID, Label: label,
		Hint: secret[len(secret)-4:], CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err = s.store.CreateMCPToken(token, secret); err != nil {
		if errors.Is(err, store.ErrTooManyMCPTokens) {
			writeJSONAPIError(w, http.StatusConflict, "too many active tokens; revoke one first", "too_many_tokens")
			return
		}
		writeJSONAPIError(w, http.StatusInternalServerError, "token could not be stored", "internal_error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONResponse(w, http.StatusCreated, issuedMCPToken{MCPToken: token, Token: secret})
}

func (s *Server) handleRevokeMCPToken(w http.ResponseWriter, r *http.Request) {
	session, _ := s.requestSession(r)
	err := s.store.RevokeMCPToken(session.Principal.ActorID, chi.URLParam(r, "tokenID"), s.clock())
	if errors.Is(err, store.ErrMCPTokenNotFound) {
		writeJSONAPIError(w, http.StatusNotFound, "token not found", "not_found")
		return
	}
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "token could not be revoked", "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
