package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/oidc"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const (
	oidcLoginPath       = "/api/auth/oidc/login"
	oidcCallbackPath    = "/api/auth/oidc/callback"
	oidcStateCookieName = "ai_team_oidc_state"
	oidcStateCookiePath = "/api/auth/oidc/"
	oidcStateTTL        = 10 * time.Minute
	oidcExchangeTimeout = 15 * time.Second
)

// OIDCProvider is the relying-party operations the server needs; *oidc.Provider
// implements it.
type OIDCProvider interface {
	AuthorizationURL(ctx context.Context, state, nonce, codeChallenge string) (string, error)
	Exchange(ctx context.Context, code, codeVerifier, nonce string) (oidc.Identity, error)
}

// WithOIDC enables OpenID Connect sign-in. It is valid only in cloud mode:
// the signed-in user's functions always come from the team registry.
func WithOIDC(provider OIDCProvider) ServerOption {
	return func(server *Server) { server.oidc = provider }
}

// handleOIDCLogin starts the authorization-code flow with PKCE. state is
// bound to this browser by an HttpOnly cookie; nonce and the PKCE verifier
// stay on the server.
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	state, errState := oidc.RandomString()
	nonce, errNonce := oidc.RandomString()
	verifier, challenge, errPKCE := oidc.NewPKCE()
	if errState != nil || errNonce != nil || errPKCE != nil {
		http.Error(w, "sign-in unavailable", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), oidcExchangeTimeout)
	defer cancel()
	target, err := s.oidc.AuthorizationURL(ctx, state, nonce, challenge)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ OIDC sign-in: %v\n", err)
		http.Error(w, "SSO-провайдер недоступен", http.StatusServiceUnavailable)
		return
	}
	now := s.clock()
	if err := s.store.SaveOIDCLoginState(state, store.OIDCLoginState{
		Nonce: nonce, CodeVerifier: verifier, ExpiresAt: now.Add(oidcStateTTL),
	}, now); err != nil {
		http.Error(w, "sign-in unavailable", http.StatusServiceUnavailable)
		return
	}
	// SameSite=Lax: the callback is a top-level navigation from the provider,
	// on which Strict cookies are not sent.
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookieName, Value: state, Path: oidcStateCookiePath,
		MaxAge: int(oidcStateTTL / time.Second), HttpOnly: true,
		Secure: requestScheme(r) == "https", SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, target, http.StatusFound)
}

// handleOIDCCallback completes sign-in. Every check is fail-closed: a
// mismatched or replayed state, a provider error, an ID token that fails
// verification, an unverified e-mail or a user who is not an active team
// member yields no session. The user's functions come from the team
// registry, never from token claims.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	clearCookie(w, r, oidcStateCookieName, oidcStateCookiePath, http.SameSiteLaxMode)
	query := r.URL.Query()
	if query.Get("error") != "" {
		http.Error(w, "вход через SSO отклонён провайдером", http.StatusUnauthorized)
		return
	}
	state := query.Get("state")
	cookie, err := r.Cookie(oidcStateCookieName)
	if err != nil || state == "" || !constantTimeEqual(state, cookie.Value) {
		http.Error(w, "неверный state входа через SSO", http.StatusUnauthorized)
		return
	}
	pending, err := s.store.ConsumeOIDCLoginState(state, s.clock())
	if err != nil {
		http.Error(w, "вход через SSO истёк или уже завершён", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), oidcExchangeTimeout)
	defer cancel()
	identity, err := s.oidc.Exchange(ctx, query.Get("code"), pending.CodeVerifier, pending.Nonce)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ OIDC sign-in: %v\n", err)
		if errors.Is(err, oidc.ErrUnavailable) {
			http.Error(w, "SSO-провайдер недоступен", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}
	email := strings.ToLower(strings.TrimSpace(identity.Email))
	if email == "" || !identity.EmailVerified {
		http.Error(w, "провайдер не подтвердил e-mail", http.StatusUnauthorized)
		return
	}
	principal, epoch, err := s.activeMemberPrincipal(email)
	if err != nil {
		http.Error(w, "участник команды не активен или доступ отозван", http.StatusForbidden)
		return
	}
	sessionToken, _, err := s.createBrowserSession(principal, epoch, sessionMethodOIDC, identity.Issuer+" "+identity.Subject)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	s.setSessionCookie(w, r, sessionToken)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// activeMemberPrincipal returns the server-side functions of an active team
// member. Unlike token sign-in it never bootstraps the first administrator:
// SSO claims carry no team functions.
func (s *Server) activeMemberPrincipal(actorID string) (cloudidentity.Principal, int64, error) {
	member, err := s.store.TeamMember(actorID)
	if err != nil {
		return cloudidentity.Principal{}, 0, err
	}
	if member == nil || member.Status != "active" {
		return cloudidentity.Principal{}, 0, errors.New("пользователь не активен в команде")
	}
	roles, err := cloudidentity.ParseRoles(member.Roles)
	if err != nil {
		return cloudidentity.Principal{}, 0, err
	}
	principal, err := cloudidentity.NewPrincipal(member.ActorID, roles)
	return principal, member.SessionEpoch, err
}
