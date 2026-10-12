package web

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/oidc"
	"github.com/arturpanteleev/ai-team/pkg/oidc/oidctest"
)

const ssoTestSecret = "sso-test-hmac-secret-0123456789abcdef"

type ssoFixture struct {
	t      *testing.T
	idp    *oidctest.Provider
	srv    *Server
	dbPath string
	target string
	now    time.Time
}

func newSSOFixture(t *testing.T) *ssoFixture {
	t.Helper()
	idp, err := oidctest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(idp.Close)
	f := &ssoFixture{t: t, idp: idp, target: t.TempDir(), now: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	idp.Now = func() time.Time { return f.now }
	f.dbPath = filepath.Join(f.target, "web.db")
	f.srv = f.start()
	f.addMember("dev@example.test", "developer")
	return f
}

// start opens a server instance over the fixture database, as a restarted
// process would.
func (f *ssoFixture) start() *Server {
	f.t.Helper()
	manager, err := cloudidentity.NewTokenManager([]byte(ssoTestSecret))
	if err != nil {
		f.t.Fatal(err)
	}
	clock := func() time.Time { return f.now }
	provider, err := oidc.New(oidc.Config{
		Issuer: f.idp.Issuer, ClientID: f.idp.ClientID, ClientSecret: f.idp.ClientSecret,
		RedirectURL: "http://127.0.0.1/api/auth/oidc/callback", Now: clock,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	artifacts := filepath.Join(f.target, ".ai-team", "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		f.t.Fatal(err)
	}
	srv, err := NewServer(f.dbPath, "", artifacts, WithTargetDir(f.target),
		WithAuthenticator(manager), WithOIDC(provider), WithClock(clock))
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func (f *ssoFixture) addMember(email string, roles ...string) {
	f.t.Helper()
	token := "activation-" + email
	digest := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(digest[:])
	if err := f.srv.store.InviteTeamMember(email, email, roles, hash, f.now.Add(time.Hour), f.now, "admin@example.test"); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.srv.store.ActivateTeamInvitation(hash, f.now, nil); err != nil {
		f.t.Fatal(err)
	}
}

func (f *ssoFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	return w
}

// beginLogin starts sign-in and returns the provider URL and state cookie.
func (f *ssoFixture) beginLogin() (string, *http.Cookie) {
	f.t.Helper()
	w := f.serve(newLoopbackRequest(http.MethodGet, oidcLoginPath, nil))
	if w.Code != http.StatusFound {
		f.t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	var stateCookie *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == oidcStateCookieName {
			stateCookie = cookie
		}
	}
	if stateCookie == nil || !stateCookie.HttpOnly || stateCookie.SameSite != http.SameSiteLaxMode {
		f.t.Fatalf("state cookie missing or weak: %+v", stateCookie)
	}
	return w.Header().Get("Location"), stateCookie
}

func (f *ssoFixture) callback(code, state string, stateCookie *http.Cookie) *httptest.ResponseRecorder {
	query := url.Values{"code": {code}, "state": {state}}
	req := newLoopbackRequest(http.MethodGet, oidcCallbackPath+"?"+query.Encode(), nil)
	if stateCookie != nil {
		req.AddCookie(stateCookie)
	}
	return f.serve(req)
}

// signIn runs the whole flow and returns the callback response.
func (f *ssoFixture) signIn() *httptest.ResponseRecorder {
	f.t.Helper()
	location, stateCookie := f.beginLogin()
	code, state, err := f.idp.Authorize(location)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.callback(code, state, stateCookie)
}

func sessionCookieFrom(w *httptest.ResponseRecorder) *http.Cookie {
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.Value != "" {
			return cookie
		}
	}
	return nil
}

// recoverSession performs the SPA's same-origin CSRF recovery.
func (f *ssoFixture) recoverSession(cookie *http.Cookie) (*httptest.ResponseRecorder, sessionResponse) {
	req := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(cookie)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := f.serve(req)
	var response sessionResponse
	if w.Code == http.StatusOK {
		_ = json.NewDecoder(w.Body).Decode(&response)
	}
	return w, response
}

func TestOIDCSignInUsesTeamFunctionsAndSurvivesRestart(t *testing.T) {
	f := newSSOFixture(t)
	// Claims that try to grant functions are ignored.
	f.idp.Configure(func(p *oidctest.Provider) {
		p.Email = "Dev@Example.test"
		p.ModifyClaims = func(c map[string]any) { c["roles"] = []string{"product_owner"} }
	})
	w := f.signIn()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	cookie := sessionCookieFrom(w)
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie missing or weak: %+v", cookie)
	}
	recovered, session := f.recoverSession(cookie)
	if recovered.Code != http.StatusOK || session.Principal == nil || session.CSRFToken == "" {
		t.Fatalf("session recovery: %d %s", recovered.Code, recovered.Body.String())
	}
	if session.Principal.ActorID != "dev@example.test" || len(session.Principal.Roles) != 1 || session.Principal.Roles[0] != cloudidentity.RoleDeveloper {
		t.Fatalf("principal must come from the team registry: %+v", session.Principal)
	}

	// Restart: a new server instance over the same database accepts the cookie.
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Hour)
	f.srv = f.start()
	recovered, again := f.recoverSession(cookie)
	if recovered.Code != http.StatusOK || again.CSRFToken != session.CSRFToken || again.Principal.ActorID != "dev@example.test" {
		t.Fatalf("session after restart: %d %s", recovered.Code, recovered.Body.String())
	}
	me := newLoopbackRequest(http.MethodGet, "/api/auth/me", nil)
	me.AddCookie(cookie)
	if w := f.serve(me); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "dev@example.test") {
		t.Fatalf("read after restart: %d %s", w.Code, w.Body.String())
	}
}

func TestOIDCCallbackFailsClosed(t *testing.T) {
	forged, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		configure func(*oidctest.Provider)
		want      int
	}{
		{"forged signature", func(p *oidctest.Provider) { p.SigningKey = forged }, http.StatusUnauthorized},
		{"expired id_token", func(p *oidctest.Provider) {
			p.ModifyClaims = func(c map[string]any) { c["exp"] = p.Now().Add(-time.Hour).Unix() }
		}, http.StatusUnauthorized},
		{"wrong audience", func(p *oidctest.Provider) { p.ModifyClaims = func(c map[string]any) { c["aud"] = "someone-else" } }, http.StatusUnauthorized},
		{"wrong issuer", func(p *oidctest.Provider) {
			p.ModifyClaims = func(c map[string]any) { c["iss"] = "https://evil.example" }
		}, http.StatusUnauthorized},
		{"wrong nonce", func(p *oidctest.Provider) { p.ModifyClaims = func(c map[string]any) { c["nonce"] = "attacker-nonce" } }, http.StatusUnauthorized},
		{"unverified email", func(p *oidctest.Provider) { p.EmailVerified = false }, http.StatusUnauthorized},
		{"not a team member", func(p *oidctest.Provider) { p.Email = "stranger@example.test" }, http.StatusForbidden},
		{"jwks unavailable", func(p *oidctest.Provider) { p.FailJWKS = true }, http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newSSOFixture(t)
			f.idp.Configure(test.configure)
			w := f.signIn()
			if w.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, test.want, w.Body.String())
			}
			if sessionCookieFrom(w) != nil {
				t.Fatal("failed sign-in must not set a session cookie")
			}
		})
	}
}

func TestOIDCStateIsBoundToBrowserAndSingleUse(t *testing.T) {
	f := newSSOFixture(t)
	location, stateCookie := f.beginLogin()
	code, state, err := f.idp.Authorize(location)
	if err != nil {
		t.Fatal(err)
	}
	// Login CSRF: a callback without this browser's state cookie is rejected.
	if w := f.callback(code, state, nil); w.Code != http.StatusUnauthorized || sessionCookieFrom(w) != nil {
		t.Fatalf("missing state cookie: %d", w.Code)
	}
	other := &http.Cookie{Name: oidcStateCookieName, Value: "another-browser-state"}
	if w := f.callback(code, state, other); w.Code != http.StatusUnauthorized || sessionCookieFrom(w) != nil {
		t.Fatalf("mismatched state: %d", w.Code)
	}
	if w := f.callback(code, "", stateCookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("empty state: %d", w.Code)
	}
	if w := f.callback(code, state, stateCookie); w.Code != http.StatusSeeOther || sessionCookieFrom(w) == nil {
		t.Fatalf("legitimate callback: %d %s", w.Code, w.Body.String())
	}
	// Replay of the same state is rejected even with the right cookie.
	if w := f.callback(code, state, stateCookie); w.Code != http.StatusUnauthorized || sessionCookieFrom(w) != nil {
		t.Fatalf("replayed state: %d", w.Code)
	}
	// Expired pending sign-in.
	location, stateCookie = f.beginLogin()
	code, state, _ = f.idp.Authorize(location)
	f.now = f.now.Add(oidcStateTTL + time.Second)
	if w := f.callback(code, state, stateCookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired state: %d", w.Code)
	}
	// Provider-reported error.
	location, stateCookie = f.beginLogin()
	_, state, _ = f.idp.Authorize(location)
	req := newLoopbackRequest(http.MethodGet, oidcCallbackPath+"?error=access_denied&state="+url.QueryEscape(state), nil)
	req.AddCookie(stateCookie)
	if w := f.serve(req); w.Code != http.StatusUnauthorized {
		t.Fatalf("provider error: %d", w.Code)
	}
}

func TestOIDCDiscoveryFailureRejectsLogin(t *testing.T) {
	f := newSSOFixture(t)
	f.idp.Configure(func(p *oidctest.Provider) { p.FailDiscovery = true })
	w := f.serve(newLoopbackRequest(http.MethodGet, oidcLoginPath, nil))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Location") != "" {
		t.Fatalf("login with provider down: %d %s", w.Code, w.Body.String())
	}
}

func TestSessionLogoutRequiresCSRFAndRevokes(t *testing.T) {
	f := newSSOFixture(t)
	cookie := sessionCookieFrom(f.signIn())
	_, session := f.recoverSession(cookie)

	logout := newLoopbackRequest(http.MethodDelete, "/api/session", nil)
	logout.AddCookie(cookie)
	if w := f.serve(logout); w.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF: %d", w.Code)
	}
	logout = newLoopbackRequest(http.MethodDelete, "/api/session", nil)
	logout.AddCookie(cookie)
	logout.Header.Set("X-CSRF-Token", session.CSRFToken)
	logout.Header.Set("Origin", "http://evil.example")
	if w := f.serve(logout); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin logout: %d", w.Code)
	}
	logout = newLoopbackRequest(http.MethodDelete, "/api/session", nil)
	logout.AddCookie(cookie)
	logout.Header.Set("X-CSRF-Token", session.CSRFToken)
	if w := f.serve(logout); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", w.Code, w.Body.String())
	}
	if w, _ := f.recoverSession(cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session must be rejected: %d", w.Code)
	}
	me := newLoopbackRequest(http.MethodGet, "/api/auth/me", nil)
	me.AddCookie(cookie)
	if w := f.serve(me); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session read: %d", w.Code)
	}
	// Revocation is durable across restart.
	_ = f.srv.Close()
	f.srv = f.start()
	if w, _ := f.recoverSession(cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session after restart: %d", w.Code)
	}
}

func TestSessionWriteStillRequiresCSRF(t *testing.T) {
	f := newSSOFixture(t)
	f.addMember("owner@example.test", "product_owner")
	f.idp.Configure(func(p *oidctest.Provider) { p.Email = "owner@example.test" })
	cookie := sessionCookieFrom(f.signIn())
	_, session := f.recoverSession(cookie)
	body := `{"email":"new@example.test","roles":["qa"]}`
	if w := teamRequest(f.srv, cookie, "wrong-token", http.MethodPost, "/api/team/invitations", body); w.Code != http.StatusForbidden {
		t.Fatalf("write without valid CSRF: %d %s", w.Code, w.Body.String())
	}
	if w := teamRequest(f.srv, cookie, session.CSRFToken, http.MethodPost, "/api/team/invitations", body); w.Code != http.StatusCreated {
		t.Fatalf("write with CSRF: %d %s", w.Code, w.Body.String())
	}
}

func TestSessionExpiresSlidesAndFollowsTeamRevocation(t *testing.T) {
	f := newSSOFixture(t)
	cookie := sessionCookieFrom(f.signIn())
	// Activity within the idle window slides the expiry forward.
	for i := 0; i < 4; i++ {
		f.now = f.now.Add(browserSessionTTL * 3 / 4)
		if w, _ := f.recoverSession(cookie); w.Code != http.StatusOK {
			t.Fatalf("active session expired at step %d: %d", i, w.Code)
		}
	}
	// Idle longer than the TTL: expired.
	f.now = f.now.Add(browserSessionTTL + time.Second)
	if w, _ := f.recoverSession(cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("idle session must expire: %d", w.Code)
	}

	// The absolute lifetime bounds even an active session.
	cookie = sessionCookieFrom(f.signIn())
	for elapsed := time.Duration(0); elapsed < browserSessionMaxAge-browserSessionTTL; elapsed += browserSessionTTL / 2 {
		f.now = f.now.Add(browserSessionTTL / 2)
		if w, _ := f.recoverSession(cookie); w.Code != http.StatusOK {
			t.Fatalf("session expired early after %s: %d", elapsed, w.Code)
		}
	}
	f.now = f.now.Add(browserSessionTTL)
	if w, _ := f.recoverSession(cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("session must end at its absolute lifetime: %d", w.Code)
	}

	// Revoking the member invalidates the persisted session immediately.
	cookie = sessionCookieFrom(f.signIn())
	if err := f.srv.store.SetTeamMember("dev@example.test", nil, false, "admin@example.test", "membership_revoked", f.now); err != nil {
		t.Fatal(err)
	}
	if w, _ := f.recoverSession(cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked member session: %d", w.Code)
	}
}

func TestLocalModeSessionsUnchangedAndIsolatedFromCloud(t *testing.T) {
	target := t.TempDir()
	artifacts := filepath.Join(target, ".ai-team", "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(target, "web.db")
	const token = "local-mode-token-0123456789abcdef0123456789"
	local, err := NewLocalAuthenticator(token)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := oidc.New(oidc.Config{Issuer: "https://idp.example", ClientID: "c", ClientSecret: "s", RedirectURL: "http://127.0.0.1/cb"})
	if err != nil {
		t.Fatal(err)
	}
	if srv, err := NewServer(dbPath, "", artifacts, WithTargetDir(target), WithLocalAuthenticator(local), WithOIDC(provider)); err == nil {
		_ = srv.Close()
		t.Fatal("OIDC must not be enabled in local mode")
	}
	srv, err := NewServer(dbPath, "", artifacts, WithTargetDir(target), WithLocalAuthenticator(local))
	if err != nil {
		t.Fatal(err)
	}
	config := httptest.NewRecorder()
	srv.router.ServeHTTP(config, newLoopbackRequest(http.MethodGet, "/api/auth/config", nil))
	if !strings.Contains(config.Body.String(), `"sso_enabled":false`) {
		t.Fatalf("auth config: %s", config.Body.String())
	}
	login := httptest.NewRecorder()
	srv.router.ServeHTTP(login, newLoopbackRequest(http.MethodGet, oidcLoginPath, nil))
	if login.Code == http.StatusFound {
		t.Fatal("local mode must not expose OIDC sign-in")
	}
	request := authenticatedRequest(t, srv, token, http.MethodGet, "/api/auth/me", "")
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		t.Fatal(err)
	}
	me := httptest.NewRecorder()
	srv.router.ServeHTTP(me, request)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), "local-user") {
		t.Fatalf("local session: %d %s", me.Code, me.Body.String())
	}
	_ = srv.Close()

	// The same database reopened in cloud mode must not honour the local session.
	manager, err := cloudidentity.NewTokenManager([]byte(ssoTestSecret))
	if err != nil {
		t.Fatal(err)
	}
	cloud, err := NewServer(dbPath, "", artifacts, WithTargetDir(target), WithAuthenticator(manager))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cloud.Close() }()
	req := newLoopbackRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	cloud.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("local session accepted in cloud mode: %d %s", w.Code, w.Body.String())
	}
}
