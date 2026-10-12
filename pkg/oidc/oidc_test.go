package oidc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/oidc"
	"github.com/arturpanteleev/ai-team/pkg/oidc/oidctest"
)

func newProvider(t *testing.T) (*oidctest.Provider, *oidc.Provider, *time.Time) {
	t.Helper()
	idp, err := oidctest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(idp.Close)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	idp.Now = func() time.Time { return now }
	rp, err := oidc.New(oidc.Config{
		Issuer: idp.Issuer, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
		RedirectURL: "http://127.0.0.1:8080/api/auth/oidc/callback",
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return idp, rp, &now
}

func signIn(t *testing.T, idp *oidctest.Provider, rp *oidc.Provider, nonce string) (oidc.Identity, error) {
	t.Helper()
	verifier, challenge, err := oidc.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	authURL, err := rp.AuthorizationURL(context.Background(), "state-0123456789", nonce, challenge)
	if err != nil {
		return oidc.Identity{}, err
	}
	code, _, err := idp.Authorize(authURL)
	if err != nil {
		t.Fatal(err)
	}
	return rp.Exchange(context.Background(), code, verifier, nonce)
}

func TestExchangeVerifiesIDToken(t *testing.T) {
	idp, rp, _ := newProvider(t)
	identity, err := signIn(t, idp, rp, "nonce-abc")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Email != "dev@example.test" || !identity.EmailVerified || identity.Subject != "user-123" || identity.Issuer != idp.Issuer {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestExchangeRejectsInvalidTokens(t *testing.T) {
	forged, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		configure func(*oidctest.Provider)
	}{
		{"forged signature", func(p *oidctest.Provider) { p.SigningKey = forged }},
		{"expired", func(p *oidctest.Provider) {
			p.ModifyClaims = func(c map[string]any) { c["exp"] = p.Now().Add(-10 * time.Minute).Unix() }
		}},
		{"issued in the future", func(p *oidctest.Provider) {
			p.ModifyClaims = func(c map[string]any) { c["iat"] = p.Now().Add(10 * time.Minute).Unix() }
		}},
		{"missing exp", func(p *oidctest.Provider) { p.ModifyClaims = func(c map[string]any) { delete(c, "exp") } }},
		{"wrong audience", func(p *oidctest.Provider) { p.ModifyClaims = func(c map[string]any) { c["aud"] = "other-client" } }},
		{"multi audience without azp", func(p *oidctest.Provider) {
			p.ModifyClaims = func(c map[string]any) { c["aud"] = []string{p.ClientID, "other"} }
		}},
		{"foreign azp", func(p *oidctest.Provider) { p.ModifyClaims = func(c map[string]any) { c["azp"] = "other" } }},
		{"wrong issuer", func(p *oidctest.Provider) {
			p.ModifyClaims = func(c map[string]any) { c["iss"] = "https://evil.example" }
		}},
		{"wrong nonce", func(p *oidctest.Provider) { p.ModifyClaims = func(c map[string]any) { c["nonce"] = "replayed" } }},
		{"missing nonce", func(p *oidctest.Provider) { p.ModifyClaims = func(c map[string]any) { delete(c, "nonce") } }},
		{"empty subject", func(p *oidctest.Provider) { p.ModifyClaims = func(c map[string]any) { c["sub"] = "" } }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			idp, rp, _ := newProvider(t)
			idp.Configure(test.configure)
			if _, err := signIn(t, idp, rp, "nonce-abc"); !errors.Is(err, oidc.ErrInvalidToken) {
				t.Fatalf("want ErrInvalidToken, got %v", err)
			}
		})
	}
}

func TestMultiAudienceWithMatchingAZPIsAccepted(t *testing.T) {
	idp, rp, _ := newProvider(t)
	idp.Configure(func(p *oidctest.Provider) {
		p.ModifyClaims = func(c map[string]any) { c["aud"] = []string{p.ClientID, "other"}; c["azp"] = p.ClientID }
	})
	if _, err := signIn(t, idp, rp, "nonce-abc"); err != nil {
		t.Fatal(err)
	}
}

func TestProviderFailuresRejectSignIn(t *testing.T) {
	t.Run("jwks down", func(t *testing.T) {
		idp, rp, _ := newProvider(t)
		idp.Configure(func(p *oidctest.Provider) { p.FailJWKS = true })
		if _, err := signIn(t, idp, rp, "nonce-abc"); !errors.Is(err, oidc.ErrUnavailable) {
			t.Fatalf("want ErrUnavailable, got %v", err)
		}
	})
	t.Run("discovery down", func(t *testing.T) {
		idp, rp, _ := newProvider(t)
		idp.Configure(func(p *oidctest.Provider) { p.FailDiscovery = true })
		if _, err := signIn(t, idp, rp, "nonce-abc"); !errors.Is(err, oidc.ErrUnavailable) {
			t.Fatalf("want ErrUnavailable, got %v", err)
		}
	})
	t.Run("discovery issuer mismatch", func(t *testing.T) {
		idp, rp, _ := newProvider(t)
		idp.Configure(func(p *oidctest.Provider) { p.DiscoveryIssuer = "https://evil.example" })
		if _, err := signIn(t, idp, rp, "nonce-abc"); !errors.Is(err, oidc.ErrUnavailable) {
			t.Fatalf("want ErrUnavailable, got %v", err)
		}
	})
	t.Run("wrong PKCE verifier", func(t *testing.T) {
		idp, rp, _ := newProvider(t)
		_, challenge, _ := oidc.NewPKCE()
		authURL, err := rp.AuthorizationURL(context.Background(), "state-0123456789", "nonce", challenge)
		if err != nil {
			t.Fatal(err)
		}
		code, _, err := idp.Authorize(authURL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rp.Exchange(context.Background(), code, "not-the-verifier", "nonce"); !errors.Is(err, oidc.ErrInvalidToken) {
			t.Fatalf("want ErrInvalidToken, got %v", err)
		}
	})
}

func TestVerifyRejectsUnsupportedAlgorithms(t *testing.T) {
	idp, rp, now := newProvider(t)
	claims := map[string]any{"iss": idp.Issuer, "sub": "u", "aud": idp.ClientID, "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "nonce": "n"}
	payload, _ := json.Marshal(claims)
	for _, alg := range []string{"none", "HS256", "RS512"} {
		header, _ := json.Marshal(map[string]string{"alg": alg, "kid": idp.KeyID})
		token := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
		if _, err := rp.VerifyIDToken(context.Background(), token, "n"); !errors.Is(err, oidc.ErrInvalidToken) {
			t.Fatalf("alg %s: want ErrInvalidToken, got %v", alg, err)
		}
	}
	valid, err := oidctest.SignRS256(idp.Key, idp.KeyID, claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rp.VerifyIDToken(context.Background(), valid, "n"); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	parts := strings.Split(valid, ".")
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(payload), `"sub":"u"`, `"sub":"admin"`, 1))) + "." + parts[2]
	if _, err := rp.VerifyIDToken(context.Background(), tampered, "n"); !errors.Is(err, oidc.ErrInvalidToken) {
		t.Fatalf("tampered payload: want ErrInvalidToken, got %v", err)
	}
}

func TestVerifyES256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": server.URL, "authorization_endpoint": server.URL + "/a",
				"token_endpoint": server.URL + "/t", "jwks_uri": server.URL + "/k"})
		case "/k":
			x, y := key.X.FillBytes(make([]byte, 32)), key.Y.FillBytes(make([]byte, 32))
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "EC", "crv": "P-256", "kid": "ec",
				"x": base64.RawURLEncoding.EncodeToString(x), "y": base64.RawURLEncoding.EncodeToString(y)}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	rp, err := oidc.New(oidc.Config{Issuer: server.URL, ClientID: "c", ClientSecret: "s",
		RedirectURL: "http://localhost/cb", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": "ec"})
	payload, _ := json.Marshal(map[string]any{"iss": server.URL, "sub": "u", "aud": "c", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "nonce": "n"})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	token := unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
	if _, err := rp.VerifyIDToken(context.Background(), token, "n"); err != nil {
		t.Fatalf("ES256 token rejected: %v", err)
	}
	signature[10] ^= 0xff
	if _, err := rp.VerifyIDToken(context.Background(), unsigned+"."+base64.RawURLEncoding.EncodeToString(signature), "n"); !errors.Is(err, oidc.ErrInvalidToken) {
		t.Fatalf("corrupted ES256 signature: want ErrInvalidToken, got %v", err)
	}
}

func TestNewRejectsInsecureConfiguration(t *testing.T) {
	base := oidc.Config{Issuer: "https://idp.example", ClientID: "c", ClientSecret: "s", RedirectURL: "https://app.example/api/auth/oidc/callback"}
	if _, err := oidc.New(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for name, mutate := range map[string]func(*oidc.Config){
		"http issuer":     func(c *oidc.Config) { c.Issuer = "http://idp.example" },
		"no secret":       func(c *oidc.Config) { c.ClientSecret = "" },
		"no client":       func(c *oidc.Config) { c.ClientID = "" },
		"http redirect":   func(c *oidc.Config) { c.RedirectURL = "http://app.example/cb" },
		"relative issuer": func(c *oidc.Config) { c.Issuer = "/idp" },
	} {
		config := base
		mutate(&config)
		if _, err := oidc.New(config); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}
