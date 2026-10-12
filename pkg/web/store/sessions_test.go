package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testSession(now time.Time) WebSession {
	return WebSession{
		ActorID: "dev@example.test", Roles: []string{"developer"}, SessionEpoch: 3, AuthMethod: "oidc",
		Subject: "https://idp user-1", CSRFToken: "csrf-value", CredentialBinding: "binding", CreatedAt: now,
		ExpiresAt: now.Add(8 * time.Hour), AbsoluteExpiresAt: now.Add(24 * time.Hour),
	}
}

func TestWebSessionLifecycle(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	if err := s.CreateWebSession("raw-cookie", testSession(now)); err != nil {
		t.Fatal(err)
	}
	got, err := s.WebSession("raw-cookie", now.Add(time.Hour))
	if err != nil || got.ActorID != "dev@example.test" || got.SessionEpoch != 3 || got.CSRFToken != "csrf-value" || got.Roles[0] != "developer" {
		t.Fatalf("session = %+v err=%v", got, err)
	}
	if _, err := s.WebSession("other-cookie", now); !errors.Is(err, ErrWebSessionNotFound) {
		t.Fatalf("unknown cookie: %v", err)
	}
	// Sliding expiry is capped by the absolute expiry.
	if err := s.ExtendWebSession("raw-cookie", now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.WebSession("raw-cookie", now.Add(time.Hour))
	if !got.ExpiresAt.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("extended expiry = %v, want absolute cap", got.ExpiresAt)
	}
	if _, err := s.WebSession("raw-cookie", now.Add(24*time.Hour)); !errors.Is(err, ErrWebSessionNotFound) {
		t.Fatalf("expired session must not be returned: %v", err)
	}
	if err := s.RevokeWebSession("raw-cookie", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WebSession("raw-cookie", now.Add(2*time.Hour)); !errors.Is(err, ErrWebSessionNotFound) {
		t.Fatalf("revoked session must not be returned: %v", err)
	}
	if err := s.ExtendWebSession("raw-cookie", now.Add(10*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WebSession("raw-cookie", now.Add(2*time.Hour)); !errors.Is(err, ErrWebSessionNotFound) {
		t.Fatalf("extension must not revive a revoked session: %v", err)
	}
}

func TestWebSessionStoresOnlyCookieDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.CreateWebSession("raw-cookie-secret", testSession(now)); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT token_hash FROM web_sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "raw-cookie-secret" || stored != SessionDigest("raw-cookie-secret") {
		t.Fatalf("stored key %q must be the digest", stored)
	}
	_ = s.Close()
}

func TestOIDCLoginStateIsSingleUseAndExpires(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	state := OIDCLoginState{Nonce: "n", CodeVerifier: "v", ExpiresAt: now.Add(10 * time.Minute)}
	if err := s.SaveOIDCLoginState("state-1", state, now, 10); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConsumeOIDCLoginState("state-1", now.Add(time.Minute))
	if err != nil || got.Nonce != "n" || got.CodeVerifier != "v" {
		t.Fatalf("consume = %+v %v", got, err)
	}
	if _, err := s.ConsumeOIDCLoginState("state-1", now.Add(time.Minute)); !errors.Is(err, ErrOIDCLoginStateNotFound) {
		t.Fatalf("second consume must fail: %v", err)
	}
	if err := s.SaveOIDCLoginState("state-2", state, now, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOIDCLoginState("state-2", now.Add(11*time.Minute)); !errors.Is(err, ErrOIDCLoginStateNotFound) {
		t.Fatalf("expired state must fail: %v", err)
	}
}

func TestSchemaVersionTenRecordedAndOldBinaryRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != SchemaVersion || SchemaVersion < 10 {
		t.Fatalf("schema version = %d (%v), want %d >= 10", version, err, SchemaVersion)
	}
	_ = s.Close()
	// A binary built before sessions existed checks MAX(version) against its
	// own SchemaVersion (9) and must refuse the upgraded file.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var latest int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&latest); err != nil || latest <= 9 {
		t.Fatalf("upgraded database must be newer than v9 binaries: %d %v", latest, err)
	}
}

// TestRestartKeepsRunsEventsRequestsAndSessions covers §12 (BL-12): reopening
// the database after a server restart loses no tasks, events, recorded
// requests or sessions.
func TestRestartKeepsRunsEventsRequestsAndSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.CreatePipelineRun(&PipelineRun{RunID: "run-restart", Feature: "f", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(&Event{RunID: "run-restart", Sequence: 1, Type: "stage_started", Timestamp: now, DataJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := s.BeginIdempotency("actor", "key-1", "fp"); err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if err := s.CompleteIdempotency("actor", "key-1", "fp", 202, map[string][]string{}, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWebSession("cookie", testSession(now)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if run, err := reopened.GetPipelineRunByRunID("run-restart"); err != nil || run == nil {
		t.Fatalf("run lost on restart: %v", err)
	}
	if events, err := reopened.GetRunEventsAfter("run-restart", 0, 10); err != nil || len(events) != 1 {
		t.Fatalf("events lost on restart: %d %v", len(events), err)
	}
	stored, claimed, err := reopened.BeginIdempotency("actor", "key-1", "fp")
	if err != nil || claimed || stored.StatusCode != 202 {
		t.Fatalf("recorded request lost on restart: %+v claimed=%v err=%v", stored, claimed, err)
	}
	if _, err := reopened.WebSession("cookie", now.Add(time.Minute)); err != nil {
		t.Fatalf("session lost on restart: %v", err)
	}
}

func TestOIDCLoginStatesAreBounded(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	state := OIDCLoginState{Nonce: "n", CodeVerifier: "v", ExpiresAt: now.Add(10 * time.Minute)}
	for _, id := range []string{"a", "b"} {
		if err := s.SaveOIDCLoginState(id, state, now, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveOIDCLoginState("c", state, now, 2); !errors.Is(err, ErrTooManyPendingLogins) {
		t.Fatalf("third pending sign-in must be refused: %v", err)
	}
	// Expired sign-ins free their slots.
	if err := s.SaveOIDCLoginState("c", OIDCLoginState{Nonce: "n", CodeVerifier: "v", ExpiresAt: now.Add(30 * time.Minute)}, now.Add(11*time.Minute), 2); err != nil {
		t.Fatalf("expired states must be purged first: %v", err)
	}
}
