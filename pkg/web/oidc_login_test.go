package web

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
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
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const ssoTestSecret = "sso-test-hmac-secret-0123456789abcdef"

type ssoFixture struct {
	t      *testing.T
	idp    *oidctest.Provider
	srv    *Server
	dbPath string
	target string
	now    time.Time
	// secret, clientID and noOIDC configure the next start().
	secret   string
	clientID string
	noOIDC   bool
}

func newSSOFixture(t *testing.T) *ssoFixture {
	t.Helper()
	idp, err := oidctest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(idp.Close)
	f := &ssoFixture{t: t, idp: idp, target: t.TempDir(), now: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC),
		secret: ssoTestSecret, clientID: idp.ClientID}
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
	manager, err := cloudidentity.NewTokenManager([]byte(f.secret))
	if err != nil {
		f.t.Fatal(err)
	}
	clock := func() time.Time { return f.now }
	options := []ServerOption{WithTargetDir(f.target), WithAuthenticator(manager), WithClock(clock)}
	if !f.noOIDC {
		provider, err := oidc.New(oidc.Config{
			Issuer: f.idp.Issuer, ClientID: f.clientID, ClientSecret: f.idp.ClientSecret,
			RedirectURL: "http://127.0.0.1/api/auth/oidc/callback", Now: clock,
		})
		if err != nil {
			f.t.Fatal(err)
		}
		options = append(options, WithOIDC(provider))
	}
	artifacts := filepath.Join(f.target, ".ai-team", "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		f.t.Fatal(err)
	}
	srv, err := NewServer(f.dbPath, "", artifacts, options...)
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

func (f *ssoFixture) restart() {
	f.t.Helper()
	if err := f.srv.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.srv = f.start()
}

// tokenSession signs in with a bearer token issued under the current secret.
func (f *ssoFixture) tokenSession(actor string, roles ...cloudidentity.Role) *http.Cookie {
	f.t.Helper()
	manager, err := cloudidentity.NewTokenManager([]byte(f.secret))
	if err != nil {
		f.t.Fatal(err)
	}
	cookie, _ := cloudSessionForTest(f.t, f.srv, manager, actor, roles...)
	return cookie
}

func TestSecretRotationRevokesAllSessions(t *testing.T) {
	f := newSSOFixture(t)
	tokenCookie := f.tokenSession("dev@example.test", cloudidentity.RoleDeveloper)
	ssoCookie := sessionCookieFrom(f.signIn())
	f.restart()
	for name, cookie := range map[string]*http.Cookie{"token": tokenCookie, "oidc": ssoCookie} {
		if w, _ := f.recoverSession(cookie); w.Code != http.StatusOK {
			t.Fatalf("%s session must survive a restart with the same secret: %d", name, w.Code)
		}
	}
	// The HMAC secret leaked and is rotated: every existing session dies.
	f.secret = "rotated-hmac-secret-0123456789abcdef0123"
	f.restart()
	for name, cookie := range map[string]*http.Cookie{"token": tokenCookie, "oidc": ssoCookie} {
		if w, _ := f.recoverSession(cookie); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s session must not survive secret rotation: %d", name, w.Code)
		}
	}
	if w, _ := f.recoverSession(f.tokenSession("dev@example.test", cloudidentity.RoleDeveloper)); w.Code != http.StatusOK {
		t.Fatalf("new session under the rotated secret: %d", w.Code)
	}
}

func TestOIDCSessionsEndWhenOIDCIsReconfigured(t *testing.T) {
	f := newSSOFixture(t)
	cookie := sessionCookieFrom(f.signIn())
	f.clientID = "another-client"
	f.restart()
	if w, _ := f.recoverSession(cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("session from another OIDC registration: %d", w.Code)
	}
	f.clientID = f.idp.ClientID
	f.restart()
	if w, _ := f.recoverSession(cookie); w.Code != http.StatusOK {
		t.Fatalf("original registration restored: %d", w.Code)
	}
	f.noOIDC = true
	f.restart()
	if w, _ := f.recoverSession(cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("OIDC session after OIDC was disabled: %d", w.Code)
	}
}

// TestOIDCCallbackRevokesPreviousSession models real browser behaviour: the
// SameSite=Strict session cookie accompanies the same-site login request but
// not the provider's cross-site redirect to the callback.
func TestOIDCCallbackRevokesPreviousSession(t *testing.T) {
	f := newSSOFixture(t)
	first := sessionCookieFrom(f.signIn())
	beginWithSession := func() (string, *http.Cookie) {
		req := newLoopbackRequest(http.MethodGet, oidcLoginPath, nil)
		req.AddCookie(first)
		w := f.serve(req)
		if w.Code != http.StatusFound {
			t.Fatalf("login: %d %s", w.Code, w.Body.String())
		}
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == oidcStateCookieName {
				return w.Header().Get("Location"), cookie
			}
		}
		t.Fatal("state cookie missing")
		return "", nil
	}

	// An abandoned sign-in keeps the current session.
	beginWithSession()
	if w, _ := f.recoverSession(first); w.Code != http.StatusOK {
		t.Fatalf("abandoned sign-in must not revoke: %d", w.Code)
	}

	location, stateCookie := beginWithSession()
	code, state, err := f.idp.Authorize(location)
	if err != nil {
		t.Fatal(err)
	}
	callback := f.callback(code, state, stateCookie) // no session cookie, as from the provider
	second := sessionCookieFrom(callback)
	if second == nil || second.Value == first.Value {
		t.Fatalf("callback must issue a new session: %d %s", callback.Code, callback.Body.String())
	}
	if w, _ := f.recoverSession(first); w.Code != http.StatusUnauthorized {
		t.Fatalf("replaced session must be revoked: %d", w.Code)
	}
	if w, _ := f.recoverSession(second); w.Code != http.StatusOK {
		t.Fatalf("new session: %d", w.Code)
	}
}

func TestOIDCLoginIsRateLimitedPerAddressBeforeStorage(t *testing.T) {
	f := newSSOFixture(t)
	f.srv.oidcLoginLimiter = newLoginRateLimiter(2, oidcStateTTL, 3)
	login := func(remote string) int {
		req := newLoopbackRequest(http.MethodGet, oidcLoginPath, nil)
		req.RemoteAddr = remote
		return f.serve(req).Code
	}
	pending := func() int {
		db, err := sql.Open("sqlite", f.dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM oidc_login_states`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	for i := 0; i < 2; i++ {
		if code := login("203.0.113.7:4000"); code != http.StatusFound {
			t.Fatalf("login %d: %d", i, code)
		}
	}
	if code := login("203.0.113.7:4001"); code != http.StatusTooManyRequests {
		t.Fatalf("third login from the same address: %d", code)
	}
	if got := pending(); got != 2 {
		t.Fatalf("a limited login must not be stored: %d pending", got)
	}
	// IPv6 clients are keyed by /64.
	if code := login("[2001:db8::1]:1"); code != http.StatusFound {
		t.Fatalf("ipv6 #1: %d", code)
	}
	if code := login("[2001:db8::2]:1"); code != http.StatusFound {
		t.Fatalf("ipv6 #2: %d", code)
	}
	if code := login("[2001:db8::ffff]:1"); code != http.StatusTooManyRequests {
		t.Fatalf("same /64 over the limit: %d", code)
	}
	// Another address is unaffected.
	if code := login("198.51.100.9:1"); code != http.StatusFound {
		t.Fatalf("other address: %d", code)
	}
	// All key slots hold live windows: a new address is refused.
	if code := login("192.0.2.1:1"); code != http.StatusTooManyRequests {
		t.Fatalf("full limiter must refuse a new address: %d", code)
	}
	f.now = f.now.Add(oidcStateTTL)
	if code := login("203.0.113.7:4002"); code != http.StatusFound {
		t.Fatalf("window must reset: %d", code)
	}
	if code := login("192.0.2.1:1"); code != http.StatusFound {
		t.Fatalf("expired windows must free slots: %d", code)
	}
}

func TestOIDCBindsMemberToProviderAccount(t *testing.T) {
	f := newSSOFixture(t)
	if w := f.signIn(); w.Code != http.StatusSeeOther {
		t.Fatalf("first sign-in: %d %s", w.Code, w.Body.String())
	}
	issuer, subject, err := f.srv.store.TeamMemberOIDC("dev@example.test")
	if err != nil || issuer != f.idp.Issuer || subject != "user-123" {
		t.Fatalf("first sign-in must bind iss/sub: %q %q %v", issuer, subject, err)
	}
	if w := f.signIn(); w.Code != http.StatusSeeOther {
		t.Fatalf("same account again: %d", w.Code)
	}
	// The provider reassigned the verified address to another account.
	f.idp.Configure(func(p *oidctest.Provider) { p.Subject = "attacker-456" })
	if w := f.signIn(); w.Code != http.StatusForbidden || sessionCookieFrom(w) != nil {
		t.Fatalf("other account with the member's e-mail: %d", w.Code)
	}
	// A bound account cannot take over a second, unbound member.
	f.addMember("ops@example.test", "developer")
	f.idp.Configure(func(p *oidctest.Provider) { p.Subject = "user-123"; p.Email = "ops@example.test" })
	if w := f.signIn(); w.Code != http.StatusForbidden || sessionCookieFrom(w) != nil {
		t.Fatalf("account bound to another member: %d", w.Code)
	}
	// Revoking and restoring the member clears the binding.
	now := f.now
	if err := f.srv.store.SetTeamMember("dev@example.test", []string{"developer"}, false, "admin@example.test", "revoke", now); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.store.SetTeamMember("dev@example.test", []string{"developer"}, true, "admin@example.test", "restore", now); err != nil {
		t.Fatal(err)
	}
	f.idp.Configure(func(p *oidctest.Provider) { p.Subject = "user-789"; p.Email = "dev@example.test" })
	if w := f.signIn(); w.Code != http.StatusSeeOther {
		t.Fatalf("restored member binds a new account: %d %s", w.Code, w.Body.String())
	}
}

func TestOIDCRejectsNonASCIIEmail(t *testing.T) {
	f := newSSOFixture(t)
	f.addMember("kate@example.test", "developer")
	// U+212A KELVIN SIGN folds to "k" under Unicode lowering.
	f.idp.Configure(func(p *oidctest.Provider) { p.Email = "\u212Aate@example.test" })
	w := f.signIn()
	if w.Code != http.StatusUnauthorized || sessionCookieFrom(w) != nil {
		t.Fatalf("non-ASCII e-mail must be rejected: %d", w.Code)
	}
	f.idp.Configure(func(p *oidctest.Provider) { p.Email = "KATE@example.test" })
	if w := f.signIn(); w.Code != http.StatusSeeOther {
		t.Fatalf("ASCII case-insensitive match: %d %s", w.Code, w.Body.String())
	}
}

func TestOIDCPendingSignInsAreBounded(t *testing.T) {
	f := newSSOFixture(t)
	f.srv.oidcMaxPending = 2
	for i := 0; i < 2; i++ {
		if w := f.serve(newLoopbackRequest(http.MethodGet, oidcLoginPath, nil)); w.Code != http.StatusFound {
			t.Fatalf("login %d: %d", i, w.Code)
		}
	}
	if w := f.serve(newLoopbackRequest(http.MethodGet, oidcLoginPath, nil)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("login over the bound: %d", w.Code)
	}
	f.now = f.now.Add(oidcStateTTL + time.Second)
	if w := f.serve(newLoopbackRequest(http.MethodGet, oidcLoginPath, nil)); w.Code != http.StatusFound {
		t.Fatalf("expired sign-ins must free slots: %d", w.Code)
	}
}

// TestSessionMethodMustMatchServerMode plants sessions whose credential
// binding is valid for the server but whose method belongs to the other
// mode, so only the mode check can reject them.
func TestSessionMethodMustMatchServerMode(t *testing.T) {
	f := newSSOFixture(t)
	member, err := f.srv.store.TeamMember("dev@example.test")
	if err != nil || member == nil {
		t.Fatal("member missing")
	}
	plant := func(srv *Server, method string, epoch int64) *http.Cookie {
		t.Helper()
		binding, ok := srv.sessionBinding(method)
		if !ok {
			t.Fatal("binding unavailable")
		}
		raw := "planted-" + method + "-cookie"
		if err := srv.store.CreateWebSession(raw, store.WebSession{
			ActorID: "dev@example.test", Roles: []string{"developer"}, SessionEpoch: epoch, AuthMethod: method,
			CSRFToken: "csrf", CredentialBinding: binding, CreatedAt: f.now,
			ExpiresAt: f.now.Add(time.Hour), AbsoluteExpiresAt: f.now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: sessionCookieName, Value: raw}
	}
	// Control: a token-method session with the member's epoch is accepted.
	if w, _ := f.recoverSession(plant(f.srv, sessionMethodToken, member.SessionEpoch)); w.Code != http.StatusOK {
		t.Fatalf("control session: %d", w.Code)
	}
	// local -> cloud: valid binding and epoch, but minted for local mode.
	if w, _ := f.recoverSession(plant(f.srv, sessionMethodLocal, member.SessionEpoch)); w.Code != http.StatusUnauthorized {
		t.Fatalf("local-method session accepted in cloud mode: %d", w.Code)
	}

	// cloud -> local.
	local, err := NewLocalAuthenticator("local-mode-token-0123456789abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := filepath.Join(f.target, ".ai-team", "artifacts")
	localSrv, err := NewServer(":memory:", "", artifacts, WithTargetDir(f.target), WithLocalAuthenticator(local), WithClock(func() time.Time { return f.now }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = localSrv.Close() }()
	recover := func(cookie *http.Cookie) int {
		req := newLoopbackRequest(http.MethodGet, "/api/session", nil)
		req.AddCookie(cookie)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		localSrv.router.ServeHTTP(w, req)
		return w.Code
	}
	if code := recover(plant(localSrv, sessionMethodLocal, 0)); code != http.StatusOK {
		t.Fatalf("local control session: %d", code)
	}
	if code := recover(plant(localSrv, sessionMethodToken, 0)); code != http.StatusUnauthorized {
		t.Fatalf("token-method session accepted in local mode: %d", code)
	}
}

func TestLocalTokenRotationRevokesLocalSessions(t *testing.T) {
	target := t.TempDir()
	artifacts := filepath.Join(target, ".ai-team", "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(target, "web.db")
	open := func(token string) *Server {
		local, err := NewLocalAuthenticator(token)
		if err != nil {
			t.Fatal(err)
		}
		srv, err := NewServer(dbPath, "", artifacts, WithTargetDir(target), WithLocalAuthenticator(local))
		if err != nil {
			t.Fatal(err)
		}
		return srv
	}
	const oldToken = "old-local-token-0123456789abcdef0123456789"
	srv := open(oldToken)
	request := authenticatedRequest(t, srv, oldToken, http.MethodGet, "/api/auth/me", "")
	_ = srv.Close()
	for token, want := range map[string]int{oldToken: http.StatusOK, "new-local-token-0123456789abcdef0123456789": http.StatusUnauthorized} {
		srv = open(token)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, request.Clone(request.Context()))
		_ = srv.Close()
		if w.Code != want {
			t.Fatalf("local session with token %q: %d, want %d", token[:3], w.Code, want)
		}
	}
}
