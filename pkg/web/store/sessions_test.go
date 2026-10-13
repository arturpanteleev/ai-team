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

// TestEarlyV10DatabaseGainsLateColumns opens a database created by an early
// version 10 build: web_sessions without credential_binding, sign-in states
// without previous_session_hash and members without an SSO binding. The
// migration adds the columns; a pre-existing session carries the empty
// binding (never valid) and the existing member keeps working and can bind.
func TestEarlyV10DatabaseGainsLateColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	if err := s.InviteTeamMember("dev@example.test", "dev@example.test", []string{"developer"}, "h", now.Add(time.Hour), now, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateTeamInvitation("h", now, nil); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM schema_migrations WHERE version > 10`,
		`DROP INDEX idx_team_members_oidc`,
		`ALTER TABLE team_members DROP COLUMN oidc_subject`,
		`ALTER TABLE team_members DROP COLUMN oidc_issuer`,
		`DROP TABLE web_sessions`,
		`CREATE TABLE web_sessions (
			token_hash TEXT PRIMARY KEY, actor_id TEXT NOT NULL, roles_json TEXT NOT NULL,
			session_epoch INTEGER NOT NULL DEFAULT 0, auth_method TEXT NOT NULL, subject TEXT NOT NULL DEFAULT '',
			csrf_token TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
			absolute_expires_at INTEGER NOT NULL, revoked_at INTEGER)`,
		`DROP TABLE oidc_login_states`,
		`CREATE TABLE oidc_login_states (state_hash TEXT PRIMARY KEY, nonce TEXT NOT NULL, code_verifier TEXT NOT NULL,
			created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO web_sessions(token_hash,actor_id,roles_json,auth_method,csrf_token,created_at,expires_at,absolute_expires_at)
		VALUES(?,?,?,?,?,?,?,?)`, SessionDigest("old-cookie"), "dev@example.test", `["developer"]`, "token", "csrf",
		now.UnixMilli(), now.Add(time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = New(path)
	if err != nil {
		t.Fatalf("early v10 database must migrate: %v", err)
	}
	defer func() { _ = s.Close() }()
	old, err := s.WebSession("old-cookie", now)
	if err != nil || old.CredentialBinding != "" {
		t.Fatalf("pre-existing session must load with an empty binding: %+v %v", old, err)
	}
	if err := s.CreateWebSession("new-cookie", testSession(now)); err != nil {
		t.Fatalf("new session after migration: %v", err)
	}
	if err := s.SaveOIDCLoginState("state", OIDCLoginState{Nonce: "n", CodeVerifier: "v", PreviousSessionHash: "p",
		ExpiresAt: now.Add(time.Minute)}, now, 10); err != nil {
		t.Fatal(err)
	}
	if consumed, err := s.ConsumeOIDCLoginState("state", now); err != nil || consumed.PreviousSessionHash != "p" {
		t.Fatalf("previous session digest must round-trip: %+v %v", consumed, err)
	}
	member, err := s.TeamMember("dev@example.test")
	if err != nil || member == nil || member.Status != "active" {
		t.Fatalf("existing member must survive: %+v %v", member, err)
	}
	if err := s.BindTeamMemberOIDC("dev@example.test", "https://idp", "sub-1", now); err != nil {
		t.Fatalf("existing member binds on first SSO sign-in: %v", err)
	}
	if err := s.BindTeamMemberOIDC("dev@example.test", "https://idp", "sub-2", now); !errors.Is(err, ErrOIDCAccountMismatch) {
		t.Fatalf("different subject must be refused: %v", err)
	}
	if err := s.BindTeamMemberOIDC("dev@example.test", "https://other-idp", "sub-1", now); !errors.Is(err, ErrOIDCAccountMismatch) {
		t.Fatalf("different issuer must be refused: %v", err)
	}
}

// activeTestMember invites and activates a member with the given id/e-mail.
func activeTestMember(t *testing.T, s *Store, actorID string, now time.Time) {
	t.Helper()
	if err := s.InviteTeamMember(actorID, actorID, []string{"developer"}, "invite-"+actorID, now.Add(time.Hour), now, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateTeamInvitation("invite-"+actorID, now, nil); err != nil {
		t.Fatal(err)
	}
}

// BL-35 (#292): idx_team_members_oidc is the last line against two members
// bound to one provider account: a direct write past BindTeamMemberOIDC's
// check must fail on the constraint.
func TestTeamMemberOIDCUniqueIndexRejectsSecondBinding(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	activeTestMember(t, s, "first@example.test", now)
	activeTestMember(t, s, "second@example.test", now)
	if err := s.BindTeamMemberOIDC("first@example.test", "https://idp", "sub-1", now); err != nil {
		t.Fatal(err)
	}
	_, err := s.db.Exec(`UPDATE team_members SET oidc_issuer=?,oidc_subject=? WHERE actor_id=?`, "https://idp", "sub-1", "second@example.test")
	if !isUniqueConstraint(err) {
		t.Fatalf("second member bound to the same (iss, sub) must violate the unique index: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE team_members SET oidc_issuer=?,oidc_subject=? WHERE actor_id=?`, "https://other-idp", "sub-1", "second@example.test"); err != nil {
		t.Fatalf("the same subject at another issuer is another account: %v", err)
	}
}

// BL-35 (#292): when a concurrent first sign-in binds the account to another
// member between the check and the update, the unique index refuses the
// update; the caller must see ErrOIDCAccountMismatch (403), not a store
// failure (503). A trigger reproduces that interleaving deterministically.
func TestBindTeamMemberOIDCMapsUniqueViolationToMismatch(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	activeTestMember(t, s, "first@example.test", now)
	activeTestMember(t, s, "second@example.test", now)
	if _, err := s.db.Exec(`CREATE TRIGGER concurrent_first_bind BEFORE UPDATE OF oidc_subject ON team_members
		WHEN NEW.actor_id = 'second@example.test'
		BEGIN
			UPDATE team_members SET oidc_issuer = NEW.oidc_issuer, oidc_subject = NEW.oidc_subject
			WHERE actor_id = 'first@example.test';
		END`); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTeamMemberOIDC("second@example.test", "https://idp", "sub-1", now); !errors.Is(err, ErrOIDCAccountMismatch) {
		t.Fatalf("a lost race for the same account must be a mismatch: %v", err)
	}
	if issuer, subject, err := s.TeamMemberOIDC("second@example.test"); err != nil || issuer != "" || subject != "" {
		t.Fatalf("the refused member must stay unbound: %q %q %v", issuer, subject, err)
	}
}
