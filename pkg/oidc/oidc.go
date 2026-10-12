// Package oidc implements the relying-party side of OpenID Connect sign-in:
// discovery, the authorization-code flow with PKCE (S256) and verification
// of the ID token against the provider's JWKS.
//
// The package is deliberately small and fail-closed. It accepts only RS256
// and ES256 signatures, requires an exact issuer match, the configured client
// in the audience, an unexpired token and the nonce bound to the sign-in.
// Any discovery, network or key-set failure rejects the sign-in; there is no
// fallback to unverified claims.
package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBody    = 1 << 20
	maxIDTokenSize     = 16 << 10
	clockSkew          = 30 * time.Second
	discoveryCacheTTL  = time.Hour
	jwksCacheTTL       = time.Hour
	jwksRefreshPeriod  = time.Minute
	defaultHTTPTimeout = 10 * time.Second
)

// ErrUnavailable marks failures to reach or understand the provider
// (discovery, JWKS, token endpoint). Callers MUST reject the sign-in.
var ErrUnavailable = errors.New("oidc: provider unavailable")

// ErrInvalidToken marks an ID token that failed verification.
var ErrInvalidToken = errors.New("oidc: invalid id_token")

// Config describes one relying-party registration.
type Config struct {
	// Issuer is the exact issuer identifier; discovery is fetched from
	// Issuer + "/.well-known/openid-configuration".
	Issuer string
	// ClientID is the registered client and the required ID token audience.
	ClientID string
	// ClientSecret authenticates the client at the token endpoint. It is
	// supplied by the caller from the process environment, never from config.
	ClientSecret string
	// RedirectURL is the registered callback URL.
	RedirectURL string
	// Scopes are requested in addition to "openid". Defaults to email, profile.
	Scopes []string
	// HTTPClient is used for discovery, JWKS and token requests.
	HTTPClient *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Identity is the verified subset of ID token claims.
type Identity struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
}

// Provider is a configured relying party. It is safe for concurrent use.
type Provider struct {
	cfg    Config
	client *http.Client
	now    func() time.Time

	mu            sync.Mutex
	metadata      *providerMetadata
	metadataAt    time.Time
	keys          map[string]crypto.PublicKey
	keysAt        time.Time
	keysRefreshAt time.Time
}

type providerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	JWKSURI                           string   `json:"jwks_uri"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// New validates the configuration. It performs no network I/O; discovery
// happens on first use so a provider outage cannot prevent server start.
func New(cfg Config) (*Provider, error) {
	cfg.Issuer = strings.TrimSpace(cfg.Issuer)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.RedirectURL = strings.TrimSpace(cfg.RedirectURL)
	if err := validateEndpoint(cfg.Issuer, "issuer"); err != nil {
		return nil, err
	}
	if issuer, _ := url.Parse(cfg.Issuer); issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, errors.New("oidc: issuer must not contain query or fragment")
	}
	if cfg.ClientID == "" || strings.ContainsAny(cfg.ClientID, "\r\n\x00") {
		return nil, errors.New("oidc: client ID is required")
	}
	if cfg.ClientSecret == "" {
		return nil, errors.New("oidc: client secret is required")
	}
	if err := validateEndpoint(cfg.RedirectURL, "redirect URL"); err != nil {
		return nil, err
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"email", "profile"}
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	// Never follow redirects: endpoints come from validated discovery and a
	// redirect would let a compromised hop choose where secrets are sent.
	copied := *client
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copied.Timeout == 0 {
		copied.Timeout = defaultHTTPTimeout
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Provider{cfg: cfg, client: &copied, now: now}, nil
}

// validateEndpoint requires https, except for loopback hosts used by local
// development and tests.
func validateEndpoint(raw, name string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("oidc: %s must be an absolute URL", name)
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(parsed.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("oidc: %s must use https", name)
}

// validateDiscoveredEndpoint requires https for endpoints named by discovery.
// Plain http on loopback is accepted only when the issuer itself is a
// loopback http URL (local development); otherwise a tampered discovery
// document could direct the client secret to a cleartext local listener.
// The host is not required to match the issuer: real providers split hosts.
func (p *Provider) validateDiscoveredEndpoint(raw, name string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("oidc: %s must be an absolute URL", name)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	issuer, _ := url.Parse(p.cfg.Issuer)
	if parsed.Scheme == "http" && issuer.Scheme == "http" && isLoopback(issuer.Hostname()) && isLoopback(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("oidc: %s must use https", name)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Issuer returns the configured issuer identifier.
func (p *Provider) Issuer() string { return p.cfg.Issuer }

// ClientID returns the configured client ID.
func (p *Provider) ClientID() string { return p.cfg.ClientID }

// NewPKCE returns a code verifier and its S256 challenge.
func NewPKCE() (verifier, challenge string, err error) {
	verifier, err = RandomString()
	if err != nil {
		return "", "", err
	}
	return verifier, S256Challenge(verifier), nil
}

// S256Challenge computes the PKCE S256 code challenge.
func S256Challenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// RandomString returns 256 bits of randomness, base64url encoded.
func RandomString() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// AuthorizationURL returns the provider URL that starts the sign-in.
func (p *Provider) AuthorizationURL(ctx context.Context, state, nonce, codeChallenge string) (string, error) {
	metadata, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	target, err := url.Parse(metadata.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("%w: authorization endpoint", ErrUnavailable)
	}
	query := target.Query()
	query.Set("response_type", "code")
	query.Set("client_id", p.cfg.ClientID)
	query.Set("redirect_uri", p.cfg.RedirectURL)
	query.Set("scope", strings.Join(append([]string{"openid"}, p.cfg.Scopes...), " "))
	query.Set("state", state)
	query.Set("nonce", nonce)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	target.RawQuery = query.Encode()
	return target.String(), nil
}

// Exchange redeems an authorization code and returns the verified identity.
// The ID token is taken only from the token endpoint response (back channel)
// and verified exactly like any other token.
func (p *Provider) Exchange(ctx context.Context, code, codeVerifier, nonce string) (Identity, error) {
	if code == "" || len(code) > 4096 || codeVerifier == "" || nonce == "" {
		return Identity{}, fmt.Errorf("%w: incomplete sign-in", ErrInvalidToken)
	}
	metadata, err := p.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.cfg.RedirectURL},
		"code_verifier": {codeVerifier},
		"client_id":     {p.cfg.ClientID},
	}
	usePost := len(metadata.TokenEndpointAuthMethodsSupported) > 0 &&
		!contains(metadata.TokenEndpointAuthMethodsSupported, "client_secret_basic") &&
		contains(metadata.TokenEndpointAuthMethodsSupported, "client_secret_post")
	if usePost {
		form.Set("client_secret", p.cfg.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, fmt.Errorf("%w: token request", ErrUnavailable)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if !usePost {
		request.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret))
	}
	var tokenResponse struct {
		IDToken string `json:"id_token"`
	}
	if err := p.doJSON(request, &tokenResponse); err != nil {
		var status *statusError
		if errors.As(err, &status) && status.code >= 400 && status.code < 500 {
			// invalid_grant and friends: the code, verifier or client was rejected.
			return Identity{}, fmt.Errorf("%w: token endpoint rejected the authorization code", ErrInvalidToken)
		}
		return Identity{}, err
	}
	if tokenResponse.IDToken == "" {
		return Identity{}, fmt.Errorf("%w: token response has no id_token", ErrInvalidToken)
	}
	return p.VerifyIDToken(ctx, tokenResponse.IDToken, nonce)
}

type idTokenHeader struct {
	Alg  string          `json:"alg"`
	Kid  string          `json:"kid"`
	Typ  string          `json:"typ"`
	Crit json.RawMessage `json:"crit"`
}

type idTokenClaims struct {
	Issuer        string          `json:"iss"`
	Subject       string          `json:"sub"`
	Audience      json.RawMessage `json:"aud"`
	AuthorizedBy  string          `json:"azp"`
	ExpiresAt     *json.Number    `json:"exp"`
	IssuedAt      *json.Number    `json:"iat"`
	NotBefore     *json.Number    `json:"nbf"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
}

// VerifyIDToken checks signature, issuer, audience, time claims and nonce.
func (p *Provider) VerifyIDToken(ctx context.Context, raw, expectedNonce string) (Identity, error) {
	invalid := func(reason string) (Identity, error) {
		return Identity{}, fmt.Errorf("%w: %s", ErrInvalidToken, reason)
	}
	if raw == "" || len(raw) > maxIDTokenSize {
		return invalid("size")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return invalid("format")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return invalid("header encoding")
	}
	var header idTokenHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return invalid("header")
	}
	if len(header.Crit) > 0 {
		return invalid("unsupported crit header")
	}
	if header.Alg != "RS256" && header.Alg != "ES256" {
		return invalid("unsupported alg " + header.Alg)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return invalid("signature encoding")
	}
	key, err := p.key(ctx, header.Kid, header.Alg)
	if err != nil {
		return Identity{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch typed := key.(type) {
	case *rsa.PublicKey:
		if header.Alg != "RS256" || rsa.VerifyPKCS1v15(typed, crypto.SHA256, digest[:], signature) != nil {
			return invalid("signature")
		}
	case *ecdsa.PublicKey:
		if header.Alg != "ES256" || len(signature) != 64 {
			return invalid("signature")
		}
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:])
		if !ecdsa.Verify(typed, digest[:], r, s) {
			return invalid("signature")
		}
	default:
		return invalid("key type")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return invalid("payload encoding")
	}
	var claims idTokenClaims
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		return invalid("claims")
	}
	if claims.Issuer != p.cfg.Issuer {
		return invalid("issuer")
	}
	audiences, err := parseAudience(claims.Audience)
	if err != nil || !contains(audiences, p.cfg.ClientID) {
		return invalid("audience")
	}
	if len(audiences) > 1 && claims.AuthorizedBy != p.cfg.ClientID {
		return invalid("authorized party")
	}
	if claims.AuthorizedBy != "" && claims.AuthorizedBy != p.cfg.ClientID {
		return invalid("authorized party")
	}
	now := p.now()
	expiresAt, ok := numericDate(claims.ExpiresAt)
	if !ok || !now.Before(expiresAt.Add(clockSkew)) {
		return invalid("expired")
	}
	issuedAt, ok := numericDate(claims.IssuedAt)
	if !ok || issuedAt.After(now.Add(clockSkew)) {
		return invalid("issued in the future")
	}
	if claims.NotBefore != nil {
		notBefore, ok := numericDate(claims.NotBefore)
		if !ok || notBefore.After(now.Add(clockSkew)) {
			return invalid("not yet valid")
		}
	}
	if expectedNonce == "" || len(claims.Nonce) != len(expectedNonce) ||
		subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(expectedNonce)) != 1 {
		return invalid("nonce")
	}
	if claims.Subject == "" {
		return invalid("subject")
	}
	identity := Identity{Issuer: claims.Issuer, Subject: claims.Subject, Email: claims.Email}
	// Some providers encode the flag as the string "true".
	verified := strings.TrimSpace(string(claims.EmailVerified))
	identity.EmailVerified = verified == "true" || verified == `"true"`
	return identity, nil
}

func numericDate(value *json.Number) (time.Time, bool) {
	if value == nil {
		return time.Time{}, false
	}
	seconds, err := value.Float64()
	// Reject non-positive values and anything past year ~5138 so the
	// nanosecond conversion below cannot overflow.
	if err != nil || seconds <= 0 || seconds > 1e11 {
		return time.Time{}, false
	}
	return time.Unix(0, int64(seconds*float64(time.Second))), true
}

func parseAudience(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	return many, nil
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (p *Provider) discover(ctx context.Context) (*providerMetadata, error) {
	p.mu.Lock()
	if p.metadata != nil && p.now().Sub(p.metadataAt) < discoveryCacheTTL {
		metadata := p.metadata
		p.mu.Unlock()
		return metadata, nil
	}
	p.mu.Unlock()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(p.cfg.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: discovery request", ErrUnavailable)
	}
	var metadata providerMetadata
	if err := p.doJSON(request, &metadata); err != nil {
		return nil, err
	}
	if metadata.Issuer != p.cfg.Issuer {
		return nil, fmt.Errorf("%w: discovery issuer mismatch", ErrUnavailable)
	}
	for name, endpoint := range map[string]string{
		"authorization endpoint": metadata.AuthorizationEndpoint,
		"token endpoint":         metadata.TokenEndpoint,
		"jwks_uri":               metadata.JWKSURI,
	} {
		if err := p.validateDiscoveredEndpoint(endpoint, name); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	if len(metadata.CodeChallengeMethodsSupported) > 0 && !contains(metadata.CodeChallengeMethodsSupported, "S256") {
		return nil, fmt.Errorf("%w: provider does not support PKCE S256", ErrUnavailable)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.metadata == nil || p.metadata.JWKSURI != metadata.JWKSURI {
		p.keys = nil
	}
	p.metadata = &metadata
	p.metadataAt = p.now()
	return &metadata, nil
}

// key returns the verification key for kid, refreshing the JWKS at most once
// per jwksRefreshPeriod when the kid is unknown (key rotation).
func (p *Provider) key(ctx context.Context, kid, alg string) (crypto.PublicKey, error) {
	metadata, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	keys := p.keys
	fresh := keys != nil && p.now().Sub(p.keysAt) < jwksCacheTTL
	canRefresh := p.now().Sub(p.keysRefreshAt) >= jwksRefreshPeriod
	p.mu.Unlock()
	if key, ok := selectKey(keys, kid, alg); ok && fresh {
		return key, nil
	}
	if fresh && !canRefresh {
		return nil, fmt.Errorf("%w: unknown signing key", ErrInvalidToken)
	}
	keys, err = p.fetchKeys(ctx, metadata.JWKSURI)
	if err != nil {
		return nil, err
	}
	if key, ok := selectKey(keys, kid, alg); ok {
		return key, nil
	}
	return nil, fmt.Errorf("%w: unknown signing key", ErrInvalidToken)
}

func selectKey(keys map[string]crypto.PublicKey, kid, alg string) (crypto.PublicKey, bool) {
	if keys == nil {
		return nil, false
	}
	if kid != "" {
		key, ok := keys[kid]
		return key, ok && keyMatchesAlg(key, alg)
	}
	// Without kid, accept only an unambiguous key of the right type.
	var found crypto.PublicKey
	for _, key := range keys {
		if keyMatchesAlg(key, alg) {
			if found != nil {
				return nil, false
			}
			found = key
		}
	}
	return found, found != nil
}

func keyMatchesAlg(key crypto.PublicKey, alg string) bool {
	switch key.(type) {
	case *rsa.PublicKey:
		return alg == "RS256"
	case *ecdsa.PublicKey:
		return alg == "ES256"
	}
	return false
}

type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (p *Provider) fetchKeys(ctx context.Context, jwksURI string) (map[string]crypto.PublicKey, error) {
	p.mu.Lock()
	p.keysRefreshAt = p.now()
	p.mu.Unlock()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURI, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: jwks request", ErrUnavailable)
	}
	var set struct {
		Keys []jsonWebKey `json:"keys"`
	}
	if err := p.doJSON(request, &set); err != nil {
		return nil, err
	}
	keys := make(map[string]crypto.PublicKey, len(set.Keys))
	for index, jwk := range set.Keys {
		if jwk.Use != "" && jwk.Use != "sig" {
			continue
		}
		key, err := parseJWK(jwk)
		if err != nil {
			continue
		}
		if jwk.Alg != "" && !keyMatchesAlg(key, jwk.Alg) {
			continue
		}
		id := jwk.Kid
		if id == "" {
			id = fmt.Sprintf("#%d", index)
		}
		keys[id] = key
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: jwks contains no usable keys", ErrUnavailable)
	}
	p.mu.Lock()
	p.keys = keys
	p.keysAt = p.now()
	p.mu.Unlock()
	return keys, nil
}

func parseJWK(jwk jsonWebKey) (crypto.PublicKey, error) {
	switch jwk.Kty {
	case "RSA":
		n, err := base64.RawURLEncoding.DecodeString(jwk.N)
		if err != nil {
			return nil, err
		}
		e, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil || len(e) == 0 || len(e) > 4 {
			return nil, errors.New("rsa exponent")
		}
		exponent := int(new(big.Int).SetBytes(e).Int64())
		key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}
		if key.N.BitLen() < 2048 || exponent < 3 || exponent%2 == 0 {
			return nil, errors.New("weak rsa key")
		}
		return key, nil
	case "EC":
		if jwk.Crv != "P-256" {
			return nil, errors.New("unsupported curve")
		}
		x, errX := base64.RawURLEncoding.DecodeString(jwk.X)
		y, errY := base64.RawURLEncoding.DecodeString(jwk.Y)
		if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
			return nil, errors.New("ec coordinates")
		}
		uncompressed := append([]byte{4}, append(x, y...)...)
		key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), uncompressed)
		if err != nil {
			return nil, err
		}
		return key, nil
	}
	return nil, errors.New("unsupported key type")
}

func (p *Provider) doJSON(request *http.Request, destination any) error {
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %s %s", ErrUnavailable, request.Method, request.URL.Redacted())
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil || len(body) > maxResponseBody {
		return fmt.Errorf("%w: response body", ErrUnavailable)
	}
	if response.StatusCode != http.StatusOK {
		return &statusError{code: response.StatusCode, endpoint: request.URL.Redacted()}
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("%w: malformed JSON from %s", ErrUnavailable, request.URL.Redacted())
	}
	return nil
}

// statusError reports a non-200 provider response. It unwraps to
// ErrUnavailable; Exchange reclassifies token-endpoint 4xx as a rejection.
type statusError struct {
	code     int
	endpoint string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%v: %s returned %d", ErrUnavailable, e.endpoint, e.code)
}

func (e *statusError) Unwrap() error { return ErrUnavailable }
