package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openRawWithoutBusyWait opens the file without a busy handler, so any lock
// conflict surfaces immediately as SQLITE_BUSY instead of being masked by
// a wait.
func openRawWithoutBusyWait(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	return db
}

func migratedDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// BL-33: opening an already migrated database must not take the write lock.
// Another connection holds the write lock for the whole open; any write in
// migrate would fail with SQLITE_BUSY.
func TestOpenMigratedDatabaseDoesNotTakeWriteLock(t *testing.T) {
	path := migratedDatabase(t)
	holder := openRawWithoutBusyWait(t, path)
	conn, err := holder.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `ROLLBACK`) }()

	s, err := New(path)
	if err == nil {
		err = s.Close()
	}
	if err != nil {
		t.Fatalf("open of migrated database under a foreign write lock: %v", err)
	}
}

// BL-33 (TestE2E_DistributedSchedulerDispatchesAndArchivesRun flake): while
// one process keeps writing events, other processes repeatedly open the same
// database. A deferred migration transaction upgraded from a stale read
// snapshot fails with SQLITE_BUSY without waiting for busy_timeout.
func TestParallelOpensDuringWritesDoNotFailWithBusy(t *testing.T) {
	path := migratedDatabase(t)
	writer, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()

	stop := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		for sequence := int64(1); ; sequence++ {
			select {
			case <-stop:
				writerDone <- nil
				return
			default:
			}
			event := &Event{RunID: "busy-run", Sequence: sequence, Type: "stage_started", Timestamp: time.Now().UTC(), DataJSON: `{"stage":"code"}`}
			if err := writer.AppendEvent(event); err != nil {
				writerDone <- fmt.Errorf("writer: %w", err)
				return
			}
		}
	}()

	const openers, opensPerOpener = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, openers*opensPerOpener)
	for range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range opensPerOpener {
				s, err := New(path)
				if err != nil {
					errs <- err
					continue
				}
				if err := s.Close(); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	close(errs)
	var failures []error
	for err := range errs {
		failures = append(failures, err)
	}
	if len(failures) > 0 {
		t.Fatalf("%d of %d parallel opens failed, first: %v", len(failures), openers*opensPerOpener, failures[0])
	}
}

// previousVersionDatabase builds a v10 database (before the B-86 backlog)
// holding a run and an event, as a v10 binary would have left it.
func previousVersionDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePipelineRun(&PipelineRun{RunID: "run-kept", Feature: "kept", Status: "completed", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(&Event{RunID: "run-kept", Sequence: 1, Type: "stage_started", Timestamp: time.Now().UTC(), DataJSON: `{"stage":"code"}`}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DROP INDEX idx_backlog_queue`,
		`DROP TABLE backlog_items`,
		`DELETE FROM schema_migrations WHERE version > 10`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertMigratedWithData(t *testing.T, path string) {
	t.Helper()
	db := openRawWithoutBusyWait(t, path)
	var versions, latest int64
	if err := db.QueryRow(`SELECT COUNT(*), MAX(version) FROM schema_migrations`).Scan(&versions, &latest); err != nil {
		t.Fatal(err)
	}
	if versions != SchemaVersion || latest != SchemaVersion {
		t.Fatalf("schema_migrations count=%d max=%d, want %d", versions, latest, SchemaVersion)
	}
	var backlog int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'backlog_items'`).Scan(&backlog); err != nil || backlog != 1 {
		t.Fatalf("backlog_items after migration: %d %v", backlog, err)
	}
	var runs, events int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM pipeline_runs WHERE run_uid = 'run-kept'), (SELECT COUNT(*) FROM events WHERE run_uid = 'run-kept' AND stage = 'code')`).Scan(&runs, &events); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || events != 1 {
		t.Fatalf("data lost in migration: runs=%d events=%d", runs, events)
	}
}

func TestOpenPreviousVersionMigratesAndKeepsData(t *testing.T) {
	path := previousVersionDatabase(t)
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertMigratedWithData(t, path)
}

// Several processes start against the same old database: exactly one
// migrates under BEGIN IMMEDIATE, the others wait for the lock, re-read the
// version and open without migrating again.
func TestParallelOpensOfPreviousVersionMigrateOnce(t *testing.T) {
	path := previousVersionDatabase(t)
	const openers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, openers)
	for range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := New(path)
			if err == nil {
				err = s.Close()
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("parallel open of previous version: %v", err)
		}
	}
	assertMigratedWithData(t, path)
}

// The version check happens before any write: a newer database is refused
// even while another connection holds the write lock, and is left untouched.
func TestNewerSchemaRefusedWithoutWriting(t *testing.T) {
	path := migratedDatabase(t)
	db := openRawWithoutBusyWait(t, path)
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, CURRENT_TIMESTAMP)`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `ROLLBACK`) }()

	s, err := New(path)
	if s != nil {
		_ = s.Close()
	}
	want := fmt.Sprintf("database schema version %d is newer than supported version %d", SchemaVersion+1, SchemaVersion)
	if err == nil || err.Error() != want {
		t.Fatalf("newer schema open error = %v, want %q", err, want)
	}
}

// TestVersion12DatabaseMigratesTo13WithData opens a database exactly as a
// version 12 binary left it (no SSO binding columns, no previous-session
// digest): the version 13 step adds them, keeps runs, events, members and
// sessions, and the existing member can bind on the next SSO sign-in.
func TestVersion12DatabaseMigratesTo13WithData(t *testing.T) {
	if SchemaVersion != 13 {
		t.Fatalf("test written for the 12 -> 13 step, SchemaVersion=%d", SchemaVersion)
	}
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	if err := s.CreatePipelineRun(&PipelineRun{RunID: "run-kept", Feature: "kept", Status: "completed", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(&Event{RunID: "run-kept", Sequence: 1, Type: "stage_started", Timestamp: now, DataJSON: `{"stage":"code"}`}); err != nil {
		t.Fatal(err)
	}
	if err := s.InviteTeamMember("dev@example.test", "dev@example.test", []string{"developer"}, "h", now.Add(time.Hour), now, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateTeamInvitation("h", now, nil); err != nil {
		t.Fatal(err)
	}
	// A second member without SSO: after the step both carry an empty binding,
	// which the partial unique index must not treat as a duplicate.
	if err := s.InviteTeamMember("qa@example.test", "qa@example.test", []string{"tester"}, "h2", now.Add(time.Hour), now, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateTeamInvitation("h2", now, nil); err != nil {
		t.Fatal(err)
	}
	session := WebSession{ActorID: "dev@example.test", Roles: []string{"developer"}, SessionEpoch: 1, AuthMethod: "oidc",
		CSRFToken: "csrf", CredentialBinding: "binding", CreatedAt: now, ExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(time.Hour)}
	if err := s.CreateWebSession("cookie-kept", session); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DROP INDEX idx_team_members_oidc`,
		`ALTER TABLE team_members DROP COLUMN oidc_subject`,
		`ALTER TABLE team_members DROP COLUMN oidc_issuer`,
		`ALTER TABLE oidc_login_states DROP COLUMN previous_session_hash`,
		`DELETE FROM schema_migrations WHERE version > 12`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = New(path)
	if err != nil {
		t.Fatalf("v12 database must migrate: %v", err)
	}
	defer func() { _ = s.Close() }()
	var latest int64
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&latest); err != nil || latest != 13 {
		t.Fatalf("schema version after migration = %d (%v), want 13", latest, err)
	}
	var runs, events int
	if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM pipeline_runs WHERE run_uid = 'run-kept'), (SELECT COUNT(*) FROM events WHERE run_uid = 'run-kept')`).Scan(&runs, &events); err != nil || runs != 1 || events != 1 {
		t.Fatalf("data lost in migration: runs=%d events=%d err=%v", runs, events, err)
	}
	if kept, err := s.WebSession("cookie-kept", now); err != nil || kept.CredentialBinding != "binding" {
		t.Fatalf("session lost in migration: %+v %v", kept, err)
	}
	for _, actorID := range []string{"dev@example.test", "qa@example.test"} {
		if member, err := s.TeamMember(actorID); err != nil || member == nil || member.Status != "active" {
			t.Fatalf("member %s lost in migration: %+v %v", actorID, member, err)
		}
		if issuer, subject, err := s.TeamMemberOIDC(actorID); err != nil || issuer != "" || subject != "" {
			t.Fatalf("migrated member %s must start unbound: %q %q %v", actorID, issuer, subject, err)
		}
	}
	if err := s.BindTeamMemberOIDC("dev@example.test", "https://idp", "sub-1", now); err != nil {
		t.Fatalf("migrated member must bind: %v", err)
	}
	if err := s.BindTeamMemberOIDC("qa@example.test", "https://idp", "sub-1", now); !errors.Is(err, ErrOIDCAccountMismatch) {
		t.Fatalf("second migrated member must not take the first one's account: %v", err)
	}
	if err := s.BindTeamMemberOIDC("qa@example.test", "https://idp", "sub-2", now); err != nil {
		t.Fatalf("second migrated member must bind its own account: %v", err)
	}
	if err := s.SaveOIDCLoginState("state", OIDCLoginState{Nonce: "n", CodeVerifier: "v", PreviousSessionHash: "p",
		ExpiresAt: now.Add(time.Minute)}, now, 10); err != nil {
		t.Fatalf("sign-in state after migration: %v", err)
	}
}

// TestVersion13DatabaseRefusedByVersion12Binary pins the policy: a version 12
// binary compares MAX(version) with its own SchemaVersion and refuses the
// migrated file, so it cannot ignore SSO bindings or session digests.
func TestVersion13DatabaseRefusedByVersion12Binary(t *testing.T) {
	path := migratedDatabase(t)
	db := openRawWithoutBusyWait(t, path)
	version, err := storedSchemaVersion(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if version != 13 {
		t.Fatalf("migrated database version = %d, want 13", version)
	}
	const previousBinarySchemaVersion = 12
	if version <= previousBinarySchemaVersion {
		t.Fatalf("a v%d binary would open the v%d database", previousBinarySchemaVersion, version)
	}
}
