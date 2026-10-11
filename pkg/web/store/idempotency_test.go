package store

import (
	"reflect"
	"testing"
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
