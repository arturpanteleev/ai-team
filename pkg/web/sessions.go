package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const (
	// browserSessionTTL is the idle lifetime: a session unused for this long
	// expires. Activity slides the expiry forward up to browserSessionMaxAge.
	browserSessionTTL = 8 * time.Hour
	// browserSessionMaxAge bounds a session regardless of activity.
	browserSessionMaxAge = 7 * 24 * time.Hour

	sessionMethodLocal = "local"
	sessionMethodToken = "token"
	sessionMethodOIDC  = "oidc"
)

type browserSession struct {
	CSRFToken    string
	Principal    cloudidentity.Principal
	SessionEpoch int64
	AuthMethod   string
	ExpiresAt    time.Time
}

// WithClock overrides the clock used for session and sign-in expiry.
func WithClock(now func() time.Time) ServerOption {
	return func(server *Server) {
		if now != nil {
			server.now = now
		}
	}
}

func (s *Server) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// sessionBinder is implemented by authenticators whose credentials can be
// fingerprinted (cloud HMAC secret, local token).
type sessionBinder interface {
	SessionBinding(label string) string
}

// sessionBinding returns the credential fingerprint a session of method must
// carry. It changes when the HMAC secret or local token rotates and, for OIDC
// sessions, when the provider registration (issuer, client ID) changes or
// OIDC is switched off — any of which invalidates existing sessions.
func (s *Server) sessionBinding(method string) (string, bool) {
	label := method
	if method == sessionMethodOIDC {
		if s.oidc == nil {
			return "", false
		}
		label += "\x00" + s.oidc.Issuer() + "\x00" + s.oidc.ClientID()
	}
	if binder, ok := s.authenticator.(sessionBinder); ok {
		return binder.SessionBinding(label), true
	}
	// Authenticators without a fingerprint get a per-process key: their
	// sessions end with the process, as before B-83.
	return cloudidentity.SessionBinding(s.processBindingKey, label), true
}

// createBrowserSession persists a new session and returns the raw cookie
// value and CSRF token. Only the cookie digest is stored.
func (s *Server) createBrowserSession(principal cloudidentity.Principal, epoch int64, method, subject string) (string, string, error) {
	sessionToken, err := randomToken()
	if err != nil {
		return "", "", err
	}
	csrfToken, err := randomToken()
	if err != nil {
		return "", "", err
	}
	binding, ok := s.sessionBinding(method)
	if !ok {
		return "", "", errors.New("session method is not configured")
	}
	now := s.clock()
	roles := make([]string, len(principal.Roles))
	for i, role := range principal.Roles {
		roles[i] = string(role)
	}
	err = s.store.CreateWebSession(sessionToken, store.WebSession{
		ActorID: principal.ActorID, Roles: roles, SessionEpoch: epoch, AuthMethod: method, Subject: subject,
		CSRFToken: csrfToken, CredentialBinding: binding, CreatedAt: now, ExpiresAt: now.Add(browserSessionTTL),
		AbsoluteExpiresAt: now.Add(browserSessionMaxAge),
	})
	if err != nil {
		return "", "", err
	}
	return sessionToken, csrfToken, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, sessionToken string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: sessionToken, Path: "/",
		HttpOnly: true, Secure: requestScheme(r) == "https",
		SameSite: http.SameSiteStrictMode,
	})
}

func clearCookie(w http.ResponseWriter, r *http.Request, name, path string, sameSite http.SameSite) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: path, MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: true, Secure: requestScheme(r) == "https", SameSite: sameSite,
	})
}

// lookupBrowserSession resolves a cookie value to a live session. Every
// failure — unknown, expired, revoked, wrong mode, team epoch changed or a
// storage error — yields no session (fail closed).
func (s *Server) lookupBrowserSession(sessionToken string) (browserSession, bool) {
	now := s.clock()
	stored, err := s.store.WebSession(sessionToken, now)
	if err != nil {
		if !errors.Is(err, store.ErrWebSessionNotFound) {
			fmt.Fprintf(os.Stderr, "⚠ web session lookup: %v\n", err)
		}
		return browserSession{}, false
	}
	cloudMode := s.authenticator != nil && !s.localAuth
	if (stored.AuthMethod == sessionMethodLocal) == cloudMode {
		// A session minted in one auth mode is never valid in the other.
		return browserSession{}, false
	}
	binding, ok := s.sessionBinding(stored.AuthMethod)
	if !ok || !constantTimeEqual(stored.CredentialBinding, binding) {
		// Credentials rotated (or OIDC reconfigured/disabled) since sign-in.
		return browserSession{}, false
	}
	roles, err := cloudidentity.ParseRoles(stored.Roles)
	if err != nil {
		return browserSession{}, false
	}
	principal, err := cloudidentity.NewPrincipal(stored.ActorID, roles)
	if err != nil {
		return browserSession{}, false
	}
	if cloudMode {
		member, err := s.store.ValidateTeamSession(principal.ActorID, stored.SessionEpoch)
		if err != nil || member == nil {
			return browserSession{}, false
		}
	}
	// Slide the idle expiry once at least half of it has elapsed, so active
	// sessions survive without a write on every request.
	if stored.ExpiresAt.Sub(now) <= browserSessionTTL/2 {
		if err := s.store.ExtendWebSession(sessionToken, now.Add(browserSessionTTL)); err == nil {
			stored.ExpiresAt = now.Add(browserSessionTTL)
			if stored.ExpiresAt.After(stored.AbsoluteExpiresAt) {
				stored.ExpiresAt = stored.AbsoluteExpiresAt
			}
		}
	}
	return browserSession{
		CSRFToken: stored.CSRFToken, Principal: principal, SessionEpoch: stored.SessionEpoch,
		AuthMethod: stored.AuthMethod, ExpiresAt: stored.ExpiresAt,
	}, true
}

// handleLogout revokes the current session. It requires the session-bound
// CSRF token, so a cross-site request cannot sign the user out.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		http.Error(w, "требуется web session", http.StatusUnauthorized)
		return
	}
	session, ok := s.lookupBrowserSession(cookie.Value)
	if !ok {
		clearCookie(w, r, sessionCookieName, "/", http.SameSiteStrictMode)
		http.Error(w, "требуется web session", http.StatusUnauthorized)
		return
	}
	if !constantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		http.Error(w, "неверный CSRF token", http.StatusForbidden)
		return
	}
	if err := s.store.RevokeWebSession(cookie.Value, s.clock()); err != nil {
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	clearCookie(w, r, sessionCookieName, "/", http.SameSiteStrictMode)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
