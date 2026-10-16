package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedOwnerAndPO(t *testing.T, s *Store) {
	t.Helper()
	now := time.Now().UTC()
	if err := s.BootstrapTeamAdmin("owner@example.com", now); err != nil {
		t.Fatal(err)
	}
	if err := s.InviteTeamMember("po2@example.com", "po2@example.com", []string{"product_owner"}, "hash", now.Add(time.Hour), now, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
}

// Opening a database that is already current never changes roles: an
// administrator removed (or never granted) stays that way. The grant belongs
// to the migration steps, not to every open.
func TestOpeningACurrentDatabaseNeverGrantsAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	seedOwnerAndPO(t, s)
	if _, err := s.db.Exec(`UPDATE team_members SET roles_json='["product_owner"]', status='active'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		s, err = New(path)
		if err != nil {
			t.Fatal(err)
		}
		owner, _ := s.TeamMember("owner@example.com")
		if containsRole(owner.Roles, "admin") {
			t.Fatalf("open #%d granted admin to %v", attempt, owner.Roles)
		}
		audit, err := s.ListTeamAudit(50)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range audit {
			if strings.Contains(entry.Action, "admin_granted_by_migration") {
				t.Fatalf("open #%d wrote a grant record: %+v", attempt, entry)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// The 18 -> 19 step keeps the chain intact: it creates the idempotency index,
// marks the version, and repairs a missing administrator exactly once.
func TestMigrationV19AddsIdempotencyIndexAndRepairsAdminOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v18.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	seedOwnerAndPO(t, s)
	for _, stmt := range []string{
		`UPDATE team_members SET roles_json='["product_owner"]', status='active'`,
		`DROP INDEX idx_api_idempotency_created`,
		`DELETE FROM schema_migrations WHERE version = 19`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var indexes, latest int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_api_idempotency_created'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("index count = %d (%v)", indexes, err)
	}
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&latest); err != nil || latest != 19 || SchemaVersion != 19 {
		t.Fatalf("version = %d (%v), SchemaVersion = %d", latest, err, SchemaVersion)
	}
	var versions int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil || versions != 19 {
		t.Fatalf("migration chain must stay contiguous: %d rows (%v)", versions, err)
	}
	owner, _ := s.TeamMember("owner@example.com")
	other, _ := s.TeamMember("po2@example.com")
	if !containsRole(owner.Roles, "admin") || containsRole(other.Roles, "admin") {
		t.Fatalf("owner=%v other=%v: the one-time repair must grant only the owner", owner.Roles, other.Roles)
	}
	// The lazy expiry query uses the index instead of scanning the table.
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN DELETE FROM api_idempotency WHERE created_at < ?`, "2000-01-01 00:00:00")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	used := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "idx_api_idempotency_created") {
			used = true
		}
	}
	if !used {
		t.Fatal("the idempotency expiry must use idx_api_idempotency_created")
	}
}
