package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// IdempotencyRetention is how long a request key and its stored response are
// kept. A retry inside this window replays the original answer; afterwards the
// key is forgotten and may be used again.
const IdempotencyRetention = 7 * 24 * time.Hour

// IdempotencyResponse is the durable response cached for one actor-scoped
// write request. A zero status code means the key has been claimed but the
// handler has not stored its response yet.
type IdempotencyResponse struct {
	Fingerprint string
	StatusCode  int
	Headers     map[string][]string
	Body        []byte
	InProgress  bool
	Conflict    bool
}

// BeginIdempotency claims a new request key or returns the response already
// stored for the same fingerprint. A reused key with different request bytes
// and a key whose owner is still processing are reported separately.
func (s *Store) BeginIdempotency(scope, requestKey, fingerprint string) (IdempotencyResponse, bool, error) {
	if scope == "" || requestKey == "" || fingerprint == "" {
		return IdempotencyResponse{}, false, errors.New("idempotency scope, request key, and fingerprint are required")
	}
	// Lazy expiry: issuing a key is the cheap moment to drop the ones past
	// retention, so no separate worker exists. A failed cleanup never blocks
	// the claim; the next claim tries again.
	_, _ = s.db.Exec(`DELETE FROM api_idempotency WHERE created_at < ?`,
		time.Now().UTC().Add(-IdempotencyRetention).Format("2006-01-02 15:04:05"))
	result, err := s.db.Exec(`INSERT INTO api_idempotency(scope, request_key, fingerprint, status_code, created_at)
		VALUES (?, ?, ?, 0, CURRENT_TIMESTAMP) ON CONFLICT(scope, request_key) DO NOTHING`, scope, requestKey, fingerprint)
	if err != nil {
		return IdempotencyResponse{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return IdempotencyResponse{}, false, err
	}
	if inserted == 1 {
		return IdempotencyResponse{}, true, nil
	}
	var response IdempotencyResponse
	var encodedHeaders string
	err = s.db.QueryRow(`SELECT fingerprint, status_code, response_headers, COALESCE(response_body, X'')
		FROM api_idempotency WHERE scope = ? AND request_key = ?`, scope, requestKey).
		Scan(&response.Fingerprint, &response.StatusCode, &encodedHeaders, &response.Body)
	if err != nil {
		return IdempotencyResponse{}, false, err
	}
	if response.Fingerprint != fingerprint {
		response.Conflict = true
		return response, false, nil
	}
	if response.StatusCode == 0 {
		response.InProgress = true
		return response, false, nil
	}
	response.Headers = make(map[string][]string)
	if err := json.Unmarshal([]byte(encodedHeaders), &response.Headers); err != nil {
		return IdempotencyResponse{}, false, fmt.Errorf("decode cached idempotency headers: %w", err)
	}
	return response, false, nil
}

// CompleteIdempotency stores the exact response before a retry can execute the
// mutation again. The status-code guard ensures only the request that claimed
// the key can complete it.
func (s *Store) CompleteIdempotency(scope, requestKey, fingerprint string, status int, headers map[string][]string, body []byte) error {
	if status < 100 || status > 599 {
		return fmt.Errorf("invalid idempotency response status %d", status)
	}
	encodedHeaders, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE api_idempotency SET status_code = ?, response_headers = ?, response_body = ?
		WHERE scope = ? AND request_key = ? AND fingerprint = ? AND status_code = 0`,
		status, string(encodedHeaders), body, scope, requestKey, fingerprint)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return fmt.Errorf("idempotency request %q was not pending", requestKey)
	}
	return nil
}

// DeletePendingIdempotency releases a claim only when a handler failed before
// it could produce any response. This is intentionally narrow: an HTTP 5xx is
// cached because the operation may already have had durable side effects.
func (s *Store) DeletePendingIdempotency(scope, requestKey, fingerprint string) error {
	_, err := s.db.Exec(`DELETE FROM api_idempotency WHERE scope = ? AND request_key = ? AND fingerprint = ? AND status_code = 0`,
		scope, requestKey, fingerprint)
	return err
}
