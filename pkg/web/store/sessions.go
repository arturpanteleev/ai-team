package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrWebSessionNotFound is returned when a session is unknown, expired or
// revoked. Callers MUST treat all three the same way (fail closed).
var ErrWebSessionNotFound = errors.New("web session not found")

// ErrOIDCLoginStateNotFound is returned when a sign-in state is unknown,
// already consumed or expired.
var ErrOIDCLoginStateNotFound = errors.New("oidc login state not found")

// WebSession is a durable browser session. The raw cookie value is never
// stored: rows are keyed by its SHA-256 digest, so a database read does not
// yield a usable cookie.
type WebSession struct {
	ActorID           string
	Roles             []string
	SessionEpoch      int64
	AuthMethod        string
	Subject           string
	CSRFToken         string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	AbsoluteExpiresAt time.Time
}

// OIDCLoginState is the server half of one pending authorization-code flow.
type OIDCLoginState struct {
	Nonce        string
	CodeVerifier string
	ExpiresAt    time.Time
}

// SessionDigest returns the storage key for a raw session or state token.
func SessionDigest(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

// CreateWebSession stores a new session under the digest of rawToken and
// purges sessions whose lifetime is over.
func (s *Store) CreateWebSession(rawToken string, session WebSession) error {
	if rawToken == "" || session.CSRFToken == "" || session.AuthMethod == "" ||
		!session.ExpiresAt.After(session.CreatedAt) || session.AbsoluteExpiresAt.Before(session.ExpiresAt) {
		return errors.New("web session is incomplete")
	}
	roles, err := json.Marshal(session.Roles)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DELETE FROM web_sessions WHERE expires_at <= ?`, session.CreatedAt.UnixMilli()); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO web_sessions(token_hash,actor_id,roles_json,session_epoch,auth_method,subject,
		csrf_token,created_at,expires_at,absolute_expires_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		SessionDigest(rawToken), session.ActorID, string(roles), session.SessionEpoch, session.AuthMethod,
		session.Subject, session.CSRFToken, session.CreatedAt.UnixMilli(), session.ExpiresAt.UnixMilli(),
		session.AbsoluteExpiresAt.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// WebSession returns an unrevoked session whose sliding and absolute expiry
// are both after now.
func (s *Store) WebSession(rawToken string, now time.Time) (WebSession, error) {
	if rawToken == "" || len(rawToken) > 512 {
		return WebSession{}, ErrWebSessionNotFound
	}
	var session WebSession
	var roles string
	var created, expires, absolute int64
	err := s.db.QueryRow(`SELECT actor_id,roles_json,session_epoch,auth_method,subject,csrf_token,created_at,expires_at,absolute_expires_at
		FROM web_sessions WHERE token_hash=? AND revoked_at IS NULL AND expires_at>? AND absolute_expires_at>?`,
		SessionDigest(rawToken), now.UnixMilli(), now.UnixMilli()).Scan(&session.ActorID, &roles, &session.SessionEpoch,
		&session.AuthMethod, &session.Subject, &session.CSRFToken, &created, &expires, &absolute)
	if errors.Is(err, sql.ErrNoRows) {
		return WebSession{}, ErrWebSessionNotFound
	}
	if err != nil {
		return WebSession{}, err
	}
	if err = json.Unmarshal([]byte(roles), &session.Roles); err != nil {
		return WebSession{}, err
	}
	session.CreatedAt = time.UnixMilli(created).UTC()
	session.ExpiresAt = time.UnixMilli(expires).UTC()
	session.AbsoluteExpiresAt = time.UnixMilli(absolute).UTC()
	return session, nil
}

// ExtendWebSession moves the sliding expiry forward; it never extends a
// revoked session or one beyond its absolute expiry.
func (s *Store) ExtendWebSession(rawToken string, expiresAt time.Time) error {
	_, err := s.db.Exec(`UPDATE web_sessions SET expires_at=MIN(?, absolute_expires_at)
		WHERE token_hash=? AND revoked_at IS NULL AND expires_at < ?`,
		expiresAt.UnixMilli(), SessionDigest(rawToken), expiresAt.UnixMilli())
	return err
}

// RevokeWebSession marks a session revoked. Revoking an unknown session is
// not an error: the outcome (no usable session) is the same.
func (s *Store) RevokeWebSession(rawToken string, now time.Time) error {
	if rawToken == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE web_sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL`,
		now.UnixMilli(), SessionDigest(rawToken))
	return err
}

// SetWebSessionIdentity rebinds a live session to another actor and role
// set. It exists for administrative tooling and tests that need a session
// for a specific principal without a full sign-in.
func (s *Store) SetWebSessionIdentity(rawToken, actorID string, roles []string) error {
	encoded, err := json.Marshal(roles)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE web_sessions SET actor_id=?, roles_json=? WHERE token_hash=?`,
		strings.TrimSpace(actorID), string(encoded), SessionDigest(rawToken))
	if err != nil {
		return err
	}
	return requireAffected(result, "web session")
}

// SaveOIDCLoginState stores a pending sign-in under the digest of state.
func (s *Store) SaveOIDCLoginState(state string, value OIDCLoginState, now time.Time) error {
	if state == "" || value.Nonce == "" || value.CodeVerifier == "" || !value.ExpiresAt.After(now) {
		return errors.New("oidc login state is incomplete")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DELETE FROM oidc_login_states WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO oidc_login_states(state_hash,nonce,code_verifier,created_at,expires_at) VALUES(?,?,?,?,?)`,
		SessionDigest(state), value.Nonce, value.CodeVerifier, now.UnixMilli(), value.ExpiresAt.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// ConsumeOIDCLoginState atomically deletes and returns a pending sign-in.
// A state can be consumed at most once, and never after it expired.
func (s *Store) ConsumeOIDCLoginState(state string, now time.Time) (OIDCLoginState, error) {
	if state == "" || len(state) > 512 {
		return OIDCLoginState{}, ErrOIDCLoginStateNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return OIDCLoginState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var value OIDCLoginState
	var expires int64
	err = tx.QueryRow(`SELECT nonce,code_verifier,expires_at FROM oidc_login_states WHERE state_hash=?`,
		SessionDigest(state)).Scan(&value.Nonce, &value.CodeVerifier, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return OIDCLoginState{}, ErrOIDCLoginStateNotFound
	}
	if err != nil {
		return OIDCLoginState{}, err
	}
	if _, err = tx.Exec(`DELETE FROM oidc_login_states WHERE state_hash=?`, SessionDigest(state)); err != nil {
		return OIDCLoginState{}, err
	}
	if err = tx.Commit(); err != nil {
		return OIDCLoginState{}, err
	}
	value.ExpiresAt = time.UnixMilli(expires).UTC()
	if !value.ExpiresAt.After(now) {
		return OIDCLoginState{}, ErrOIDCLoginStateNotFound
	}
	return value, nil
}
