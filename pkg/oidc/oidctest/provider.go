// Package oidctest provides an in-process OpenID Connect provider for tests:
// discovery, a JWKS with a generated RSA key and a token endpoint that
// enforces PKCE. It never touches the network beyond the loopback listener.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Provider is a fake identity provider.
type Provider struct {
	Server       *httptest.Server
	Issuer       string
	ClientID     string
	ClientSecret string
	KeyID        string
	Key          *rsa.PrivateKey

	mu sync.Mutex
	// Now is the provider clock used for iat/exp.
	Now func() time.Time
	// Email and EmailVerified describe the signed-in user.
	Email         string
	EmailVerified bool
	Subject       string
	// ModifyClaims edits the ID token claims before signing.
	ModifyClaims func(map[string]any)
	// SigningKey, when set, signs with this key instead of Key (forged token)
	// while still advertising KeyID.
	SigningKey *rsa.PrivateKey
	// FailJWKS makes the JWKS endpoint return 500.
	FailJWKS bool
	// FailDiscovery makes discovery return 500.
	FailDiscovery bool
	// DiscoveryIssuer overrides the issuer advertised by discovery.
	DiscoveryIssuer string

	codes  map[string]pendingCode
	issued int
	// TokenRequests counts token endpoint calls.
	TokenRequests int
}

type pendingCode struct {
	nonce, challenge, redirectURI string
}

// New starts a provider. The caller must Close it.
func New() (*Provider, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	p := &Provider{
		ClientID: "ai-team-test", ClientSecret: "test-client-secret-value",
		KeyID: "test-key-1", Key: key, Now: time.Now,
		Email: "dev@example.test", EmailVerified: true, Subject: "user-123",
		codes: make(map[string]pendingCode),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("/jwks", p.jwks)
	mux.HandleFunc("/token", p.token)
	p.Server = httptest.NewServer(mux)
	p.Issuer = p.Server.URL
	return p, nil
}

// Close stops the provider.
func (p *Provider) Close() { p.Server.Close() }

// Configure applies f under the provider lock.
func (p *Provider) Configure(f func(*Provider)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

// Authorize simulates the user approving the sign-in at the authorization
// URL and returns the code the provider would redirect back with.
func (p *Provider) Authorize(authorizationURL string) (code, state string, err error) {
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		return "", "", err
	}
	query := parsed.Query()
	if query.Get("client_id") != p.ClientID || query.Get("response_type") != "code" ||
		query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" ||
		query.Get("nonce") == "" || query.Get("state") == "" ||
		!strings.Contains(" "+query.Get("scope")+" ", " openid ") {
		return "", "", fmt.Errorf("oidctest: malformed authorization request %s", authorizationURL)
	}
	p.mu.Lock()
	p.issued++
	code = fmt.Sprintf("code-%d", p.issued)
	p.codes[code] = pendingCode{nonce: query.Get("nonce"), challenge: query.Get("code_challenge"), redirectURI: query.Get("redirect_uri")}
	p.mu.Unlock()
	return code, query.Get("state"), nil
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	fail, issuer := p.FailDiscovery, p.DiscoveryIssuer
	p.mu.Unlock()
	if fail {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if issuer == "" {
		issuer = p.Issuer
	}
	writeJSON(w, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                p.Issuer + "/authorize",
		"token_endpoint":                        p.Issuer + "/token",
		"jwks_uri":                              p.Issuer + "/jwks",
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	fail := p.FailJWKS
	p.mu.Unlock()
	if fail {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": p.KeyID, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(p.Key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.Key.E)).Bytes()),
	}}})
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.TokenRequests++
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		writeError(w, "invalid_client")
		return
	}
	id, _ = url.QueryUnescape(id)
	secret, _ = url.QueryUnescape(secret)
	if id != p.ClientID || secret != p.ClientSecret {
		writeError(w, "invalid_client")
		return
	}
	if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "authorization_code" {
		writeError(w, "invalid_request")
		return
	}
	code := r.PostForm.Get("code")
	pending, found := p.codes[code]
	delete(p.codes, code) // codes are single-use
	digest := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || base64.RawURLEncoding.EncodeToString(digest[:]) != pending.challenge ||
		r.PostForm.Get("redirect_uri") != pending.redirectURI {
		writeError(w, "invalid_grant")
		return
	}
	now := p.Now()
	claims := map[string]any{
		"iss": p.Issuer, "sub": p.Subject, "aud": p.ClientID,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"nonce": pending.nonce, "email": p.Email, "email_verified": p.EmailVerified,
	}
	if p.ModifyClaims != nil {
		p.ModifyClaims(claims)
	}
	signing := p.Key
	if p.SigningKey != nil {
		signing = p.SigningKey
	}
	idToken, err := SignRS256(signing, p.KeyID, claims)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"access_token": "opaque", "token_type": "Bearer", "id_token": idToken})
}

// SignRS256 builds a compact JWS over claims.
func SignRS256(key *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
