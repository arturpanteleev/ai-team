package cloudidentity

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const tokenVersion = "v1"
const maxTokenLifetime = 24 * time.Hour
const maxTokenSize = 8 << 10

type TokenManager struct {
	secret []byte
	now    func() time.Time
}

type claims struct {
	ActorID   string `json:"actor_id"`
	Roles     []Role `json:"roles"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
}

func NewTokenManager(secret []byte) (*TokenManager, error) {
	if len(secret) < 32 {
		return nil, errors.New("cloud identity: signing secret должен содержать минимум 32 байта")
	}
	return &TokenManager{secret: append([]byte(nil), secret...), now: time.Now}, nil
}

func (m *TokenManager) Issue(principal Principal, ttl time.Duration) (string, error) {
	normalized, err := NewPrincipal(principal.ActorID, principal.Roles)
	if err != nil {
		return "", err
	}
	if ttl <= 0 || ttl > maxTokenLifetime {
		return "", fmt.Errorf("cloud identity: token TTL должен быть в диапазоне (0, %s]", maxTokenLifetime)
	}
	now := m.now().UTC()
	payload, err := json.Marshal(claims{
		ActorID: normalized.ActorID, Roles: normalized.Roles,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix(),
	})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	unsigned := tokenVersion + "." + encoded
	signature := m.sign(unsigned)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (m *TokenManager) Verify(token string) (Principal, error) {
	if token == "" || len(token) > maxTokenSize {
		return Principal{}, errors.New("cloud identity: невалидный token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != tokenVersion {
		return Principal{}, errors.New("cloud identity: неизвестный token format")
	}
	actualSignature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Principal{}, errors.New("cloud identity: невалидная token signature")
	}
	expectedSignature := m.sign(parts[0] + "." + parts[1])
	if len(actualSignature) != len(expectedSignature) ||
		subtle.ConstantTimeCompare(actualSignature, expectedSignature) != 1 {
		return Principal{}, errors.New("cloud identity: token signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > maxTokenSize {
		return Principal{}, errors.New("cloud identity: невалидный token payload")
	}
	var value claims
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Principal{}, errors.New("cloud identity: невалидные token claims")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Principal{}, errors.New("cloud identity: token содержит trailing JSON")
	}
	now := m.now().UTC().Unix()
	if value.IssuedAt <= 0 || value.ExpiresAt <= value.IssuedAt ||
		value.ExpiresAt-value.IssuedAt > int64(maxTokenLifetime/time.Second) ||
		value.IssuedAt > now+60 || value.ExpiresAt <= now {
		return Principal{}, errors.New("cloud identity: token истёк или имеет недопустимый срок")
	}
	return NewPrincipal(value.ActorID, value.Roles)
}

func (m *TokenManager) sign(value string) []byte {
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}
