package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootstrapTeamAdminGetsSystemAdminRight(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	if err := s.BootstrapTeamAdmin("owner@example.com", now); err != nil {
		t.Fatal(err)
	}
	member, err := s.TeamMember("owner@example.com")
	if err != nil || member == nil {
		t.Fatalf("member: %v %v", member, err)
	}
	if !containsRole(member.Roles, "admin") || !containsRole(member.Roles, "product_owner") {
		t.Fatalf("owner roles = %v, want admin and product_owner", member.Roles)
	}
	// An invited product owner does not get the system right.
	if err := s.InviteTeamMember("po2@example.com", "po2@example.com", []string{"product_owner"}, "hash", now.Add(time.Hour), now, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	invited, _ := s.TeamMember("po2@example.com")
	if containsRole(invited.Roles, "admin") {
		t.Fatalf("invited PO got admin: %v", invited.Roles)
	}
}

func TestLastSystemAdminCannotBeRemoved(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	if err := s.BootstrapTeamAdmin("owner@example.com", now); err != nil {
		t.Fatal(err)
	}
	if err := s.InviteTeamMember("po2@example.com", "po2@example.com", []string{"product_owner"}, "hash", now.Add(time.Hour), now, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE team_members SET status='active' WHERE actor_id='po2@example.com'`); err != nil {
		t.Fatal(err)
	}
	// Dropping admin from the only administrator is refused...
	err := s.SetTeamMember("owner@example.com", []string{"product_owner"}, true, "po2@example.com", "roles_changed", now)
	if !errors.Is(err, ErrLastSystemAdmin) {
		t.Fatalf("dropping the last admin: %v", err)
	}
	// ...and so is revoking the membership.
	if err = s.SetTeamMember("owner@example.com", nil, false, "po2@example.com", "membership_revoked", now); err == nil {
		t.Fatal("revoking the last admin must fail")
	}
	// With a second administrator the first can be demoted.
	if err := s.SetTeamMember("po2@example.com", []string{"admin", "product_owner"}, true, "owner@example.com", "roles_changed", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTeamMember("owner@example.com", []string{"product_owner"}, true, "po2@example.com", "roles_changed", now); err != nil {
		t.Fatalf("demoting a non-last admin: %v", err)
	}
}

func TestMigrationV17GrantsAdminToTheTeamOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.BootstrapTeamAdmin("owner@example.com", now); err != nil {
		t.Fatal(err)
	}
	if err := s.InviteTeamMember("po2@example.com", "po2@example.com", []string{"product_owner"}, "hash", now.Add(time.Hour), now, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	// Rewind to the state before S06: product_owner only, schema v15.
	for _, stmt := range []string{
		`UPDATE team_members SET roles_json='["product_owner"]', status='active'`,
		`DELETE FROM schema_migrations WHERE version >= 16`,
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
	owner, _ := s.TeamMember("owner@example.com")
	other, _ := s.TeamMember("po2@example.com")
	if !containsRole(owner.Roles, "admin") {
		t.Fatalf("owner roles after migration = %v", owner.Roles)
	}
	if containsRole(other.Roles, "admin") {
		t.Fatalf("a regular product owner must not receive admin: %v", other.Roles)
	}
	audit, err := s.ListTeamAudit(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range audit {
		if strings.Contains(entry.Action, "admin_granted_by_migration") {
			found = true
		}
	}
	if !found {
		t.Fatalf("migration must leave an audit record: %+v", audit)
	}
	// Reopening is idempotent: no second grant, no change.
	before := owner.SessionEpoch
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	again, _ := s.TeamMember("owner@example.com")
	if again.SessionEpoch != before {
		t.Fatalf("migration reran: epoch %d -> %d", before, again.SessionEpoch)
	}
}

// A database that a build with a later schema version brought up without the
// v17 step (the version is marked, the administrator is not) gets its
// administrator once, on the upgrade to v19: the grant looks at the state, not
// the mark. Later opens never grant (see TestOpeningACurrentDatabaseNeverGrantsAdmin).
func TestMigrationV17GrantIsStateBasedNotVersionMarked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v18-without-v17.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.BootstrapTeamAdmin("owner@example.com", now); err != nil {
		t.Fatal(err)
	}
	if err := s.InviteTeamMember("po2@example.com", "po2@example.com", []string{"product_owner"}, "hash", now.Add(time.Hour), now, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE team_members SET roles_json='["product_owner"]', status='active'`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (17, CURRENT_TIMESTAMP)`,
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
	owner, _ := s.TeamMember("owner@example.com")
	other, _ := s.TeamMember("po2@example.com")
	if !containsRole(owner.Roles, "admin") || containsRole(other.Roles, "admin") {
		t.Fatalf("owner=%v other=%v: only the owner must get admin", owner.Roles, other.Roles)
	}
	// Idempotent: a further open changes nothing.
	epoch := owner.SessionEpoch
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	again, _ := s.TeamMember("owner@example.com")
	if again.SessionEpoch != epoch {
		t.Fatalf("grant reran: %d -> %d", epoch, again.SessionEpoch)
	}
}
