package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// MCPTokenPrefix marks personal MCP access tokens so a verifier can route a
// bearer value to the token table before trying other authenticators.
const MCPTokenPrefix = "aitm_"

// MaxActiveMCPTokens bounds the live tokens one actor may hold.
const MaxActiveMCPTokens = 20

var (
	// ErrMCPTokenNotFound covers unknown, expired and revoked tokens alike:
	// callers must not distinguish them (fail closed, no oracle).
	ErrMCPTokenNotFound = errors.New("mcp token not found")
	// ErrTooManyMCPTokens is returned when the actor already holds
	// MaxActiveMCPTokens live tokens.
	ErrTooManyMCPTokens = errors.New("too many active mcp tokens")
)

// MCPToken is the stored metadata of a personal MCP token. The secret itself
// is never stored: rows are keyed by its SHA-256 digest.
type MCPToken struct {
	ID         string     `json:"id"`
	ActorID    string     `json:"actor_id"`
	Label      string     `json:"label"`
	Hint       string     `json:"hint"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// MCPTokenDigest returns the storage key of a raw token.
func MCPTokenDigest(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

// CreateMCPToken stores a new token for token.ActorID and records the issue in
// the team audit trail. rawToken must start with MCPTokenPrefix.
func (s *Store) CreateMCPToken(token MCPToken, rawToken string) error {
	if !strings.HasPrefix(rawToken, MCPTokenPrefix) || token.ID == "" || token.ActorID == "" ||
		!token.ExpiresAt.After(token.CreatedAt) {
		return errors.New("mcp token is incomplete")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var active int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM mcp_tokens WHERE actor_id=? AND revoked_at IS NULL AND expires_at>?`,
		token.ActorID, token.CreatedAt.UnixMilli()).Scan(&active); err != nil {
		return err
	}
	if active >= MaxActiveMCPTokens {
		return ErrTooManyMCPTokens
	}
	if _, err = tx.Exec(`INSERT INTO mcp_tokens(id,token_hash,actor_id,label,hint,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?)`, token.ID, MCPTokenDigest(rawToken), token.ActorID, token.Label, token.Hint,
		token.CreatedAt.UnixMilli(), token.ExpiresAt.UnixMilli()); err != nil {
		return err
	}
	if err = teamAudit(tx, token.ActorID, "mcp_token_issued", token.ActorID, `{"token_id":"`+token.ID+`"}`, token.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// MCPTokenByRaw resolves a raw token to its live row. Unknown, expired and
// revoked tokens yield ErrMCPTokenNotFound. last_used_at is refreshed at most
// once a minute so reads do not write on every call.
func (s *Store) MCPTokenByRaw(rawToken string, now time.Time) (MCPToken, error) {
	if !strings.HasPrefix(rawToken, MCPTokenPrefix) || len(rawToken) > 256 {
		return MCPToken{}, ErrMCPTokenNotFound
	}
	digest := MCPTokenDigest(rawToken)
	var token MCPToken
	var created, expires int64
	var used sql.NullInt64
	err := s.db.QueryRow(`SELECT id,actor_id,label,hint,created_at,expires_at,last_used_at FROM mcp_tokens
		WHERE token_hash=? AND revoked_at IS NULL AND expires_at>?`, digest, now.UnixMilli()).
		Scan(&token.ID, &token.ActorID, &token.Label, &token.Hint, &created, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPToken{}, ErrMCPTokenNotFound
	}
	if err != nil {
		return MCPToken{}, err
	}
	token.CreatedAt, token.ExpiresAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
	if !used.Valid || now.UnixMilli()-used.Int64 > time.Minute.Milliseconds() {
		// Best effort: a failed bookkeeping write must not fail authentication.
		_, _ = s.db.Exec(`UPDATE mcp_tokens SET last_used_at=? WHERE token_hash=?`, now.UnixMilli(), digest)
	}
	return token, nil
}

// ListMCPTokens returns the actor's tokens, newest first, including revoked
// and expired ones so the owner can see what was issued.
func (s *Store) ListMCPTokens(actorID string) ([]MCPToken, error) {
	rows, err := s.db.Query(`SELECT id,actor_id,label,hint,created_at,expires_at,last_used_at,revoked_at
		FROM mcp_tokens WHERE actor_id=? ORDER BY created_at DESC, id LIMIT 200`, actorID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	tokens := make([]MCPToken, 0)
	for rows.Next() {
		var token MCPToken
		var created, expires int64
		var used, revoked sql.NullInt64
		if err = rows.Scan(&token.ID, &token.ActorID, &token.Label, &token.Hint, &created, &expires, &used, &revoked); err != nil {
			return nil, err
		}
		token.CreatedAt, token.ExpiresAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
		if used.Valid {
			value := time.UnixMilli(used.Int64).UTC()
			token.LastUsedAt = &value
		}
		if revoked.Valid {
			value := time.UnixMilli(revoked.Int64).UTC()
			token.RevokedAt = &value
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// RevokeMCPToken revokes one of actorID's own tokens. A token that does not
// exist or belongs to someone else yields ErrMCPTokenNotFound, so ownership
// cannot be probed. Revoking an already revoked token is a no-op success.
func (s *Store) RevokeMCPToken(actorID, id string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var revoked sql.NullInt64
	err = tx.QueryRow(`SELECT revoked_at FROM mcp_tokens WHERE id=? AND actor_id=?`, id, actorID).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMCPTokenNotFound
	}
	if err != nil {
		return err
	}
	if revoked.Valid {
		return nil
	}
	if _, err = tx.Exec(`UPDATE mcp_tokens SET revoked_at=? WHERE id=? AND actor_id=? AND revoked_at IS NULL`, now.UnixMilli(), id, actorID); err != nil {
		return err
	}
	if err = teamAudit(tx, actorID, "mcp_token_revoked", actorID, `{"token_id":"`+id+`"}`, now); err != nil {
		return err
	}
	return tx.Commit()
}
