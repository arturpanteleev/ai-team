package store

import (
	"reflect"
	"testing"
	"time"
)

func TestIdempotencyClaimReplayAndConflict(t *testing.T) {
	s := newTestStore(t)
	if _, claimed, err := s.BeginIdempotency("alice", "request-1", "fingerprint-a"); err != nil || !claimed {
		t.Fatalf("initial claim: claimed=%v err=%v", claimed, err)
	}
	response, claimed, err := s.BeginIdempotency("alice", "request-1", "fingerprint-a")
	if err != nil || claimed || !response.InProgress {
		t.Fatalf("concurrent retry should not claim the same key: %+v claimed=%v err=%v", response, claimed, err)
	}
	if err := s.CompleteIdempotency("alice", "request-1", "fingerprint-a", 201,
		map[string][]string{"Content-Type": {"application/json"}}, []byte(`{"created":true}`)); err != nil {
		t.Fatal(err)
	}
	response, claimed, err = s.BeginIdempotency("alice", "request-1", "fingerprint-a")
	if err != nil || claimed || response.StatusCode != 201 || string(response.Body) != `{"created":true}` ||
		!reflect.DeepEqual(response.Headers, map[string][]string{"Content-Type": {"application/json"}}) {
		t.Fatalf("retry should replay the exact result: %+v claimed=%v err=%v", response, claimed, err)
	}
	response, claimed, err = s.BeginIdempotency("alice", "request-1", "fingerprint-b")
	if err != nil || claimed || !response.Conflict {
		t.Fatalf("different request must conflict on a reused key: %+v claimed=%v err=%v", response, claimed, err)
	}
}

func TestIdempotencyKeysExpireAfterRetention(t *testing.T) {
	s := newTestStore(t)
	for _, key := range []string{"old-done", "old-pending", "fresh"} {
		if _, claimed, err := s.BeginIdempotency("alice", key, "fp"); err != nil || !claimed {
			t.Fatalf("claim %s: %v %v", key, claimed, err)
		}
	}
	if err := s.CompleteIdempotency("alice", "old-done", "fp", 200, nil, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().UTC().Add(-IdempotencyRetention - time.Hour).Format("2006-01-02 15:04:05")
	if _, err := s.db.Exec(`UPDATE api_idempotency SET created_at = ? WHERE request_key IN ('old-done','old-pending')`, stale); err != nil {
		t.Fatal(err)
	}
	// The next claim sweeps the expired keys, so an expired key can be reused.
	if _, claimed, err := s.BeginIdempotency("alice", "old-done", "fp-new"); err != nil || !claimed {
		t.Fatalf("expired key must be claimable again: claimed=%v err=%v", claimed, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_idempotency WHERE request_key = 'old-pending'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired pending claim must be removed: count=%d err=%v", count, err)
	}
	response, claimed, err := s.BeginIdempotency("alice", "fresh", "fp")
	if err != nil || claimed || !response.InProgress {
		t.Fatalf("a fresh key must be kept: %+v claimed=%v err=%v", response, claimed, err)
	}
}
