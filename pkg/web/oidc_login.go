package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
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
	// oidcMaxPendingLogins bounds unfinished sign-ins: the login endpoint is
	// unauthenticated and each request stores one row.
	oidcMaxPendingLogins = 1000
	// oidcLoginsPerIP bounds sign-in starts from one client address (an IPv6
	// /64) per oidcStateTTL, so one client cannot fill the global bound above
	// and lock everybody else out of SSO.
	oidcLoginsPerIP = 20
	// oidcRateLimitKeys bounds the limiter's memory; when every slot holds an
	// unexpired window, new addresses are refused (fail closed).
	oidcRateLimitKeys = 10000
)

// OIDCProvider is the relying-party operations the server needs; *oidc.Provider
// implements it.
type OIDCProvider interface {
	AuthorizationURL(ctx context.Context, state, nonce, codeChallenge string) (string, error)
	Exchange(ctx context.Context, code, codeVerifier, nonce string) (oidc.Identity, error)
	// Issuer and ClientID identify the registration; sessions are bound to them.
	Issuer() string
	ClientID() string
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
	// The per-address limit is checked before the provider call and before
	// anything is written to the database.
	if !s.oidcLoginLimiter.allow(clientAddressKey(r.RemoteAddr), s.clock()) {
		http.Error(w, "слишком много входов с этого адреса, повторите позже", http.StatusTooManyRequests)
		return
	}
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
	// This request is a same-site navigation, so the SameSite=Strict session
	// cookie is present here (unlike on the provider's redirect back). Keep
	// only its digest; the callback revokes that session once the new one
	// exists. An abandoned sign-in leaves the current session untouched.
	previousSession := ""
	if previous, err := r.Cookie(sessionCookieName); err == nil && previous.Value != "" {
		previousSession = store.SessionDigest(previous.Value)
	}
	now := s.clock()
	if err := s.store.SaveOIDCLoginState(state, store.OIDCLoginState{
		Nonce: nonce, CodeVerifier: verifier, PreviousSessionHash: previousSession, ExpiresAt: now.Add(oidcStateTTL),
	}, now, s.oidcMaxPending); err != nil {
		if errors.Is(err, store.ErrTooManyPendingLogins) {
			http.Error(w, "слишком много незавершённых входов, повторите позже", http.StatusTooManyRequests)
			return
		}
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
	email := strings.TrimSpace(identity.Email)
	// Only ASCII e-mails are matched: Unicode case folding (e.g. KELVIN SIGN
	// to "k") could otherwise map a provider address onto another member.
	if email == "" || !identity.EmailVerified || !isASCII(email) {
		http.Error(w, "провайдер не подтвердил e-mail", http.StatusUnauthorized)
		return
	}
	email = asciiLower(email)
	principal, epoch, err := s.activeMemberPrincipal(email)
	if err != nil {
		http.Error(w, "участник команды не активен или доступ отозван", http.StatusForbidden)
		return
	}
	// The e-mail finds the member; the provider account (iss, sub) bound on
	// the first sign-in must match on every later one.
	if err := s.store.BindTeamMemberOIDC(principal.ActorID, identity.Issuer, identity.Subject, s.clock()); err != nil {
		if errors.Is(err, store.ErrOIDCAccountMismatch) || errors.Is(err, store.ErrTeamMemberNotFound) {
			http.Error(w, "учётная запись SSO не совпадает с привязанной к участнику", http.StatusForbidden)
			return
		}
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionToken, _, err := s.createBrowserSession(principal, epoch, sessionMethodOIDC, identity.Issuer+" "+identity.Subject)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	// The new session replaces the one this browser had when it started
	// sign-in; revoke it rather than leave it usable until it expires.
	if err := s.store.RevokeWebSessionDigest(pending.PreviousSessionHash, s.clock()); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ OIDC sign-in: revoke previous session: %v\n", err)
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

// loginRateLimiter is a fixed-window counter per client address.
type loginRateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maxKeys int
	entries map[string]loginRateWindow
}

type loginRateWindow struct {
	start time.Time
	count int
}

func newLoginRateLimiter(limit int, window time.Duration, maxKeys int) *loginRateLimiter {
	return &loginRateLimiter{limit: limit, window: window, maxKeys: maxKeys, entries: map[string]loginRateWindow{}}
}

func (l *loginRateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if ok && !now.Before(entry.start.Add(l.window)) {
		ok = false
	}
	if !ok {
		if _, tracked := l.entries[key]; !tracked && len(l.entries) >= l.maxKeys {
			for other, window := range l.entries {
				if !now.Before(window.start.Add(l.window)) {
					delete(l.entries, other)
				}
			}
			if len(l.entries) >= l.maxKeys {
				return false
			}
		}
		entry = loginRateWindow{start: now}
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	l.entries[key] = entry
	return true
}

// clientAddressKey returns the rate-limit key of the direct peer: the IPv4
// address or the IPv6 /64. Forwarding headers are not trusted; behind a
// reverse proxy every client shares the proxy's key.
func clientAddressKey(remoteAddr string) string {
	host := remoteAddr
	if split, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = split
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return "unparsed:" + host
	}
	address = address.Unmap()
	if address.Is6() {
		prefix, err := address.Prefix(64)
		if err == nil {
			return prefix.String()
		}
	}
	return address.String()
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}

func asciiLower(value string) string {
	result := []byte(value)
	for i, c := range result {
		if 'A' <= c && c <= 'Z' {
			result[i] = c + ('a' - 'A')
		}
	}
	return string(result)
}
