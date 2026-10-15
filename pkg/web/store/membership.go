package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	ErrInvalidInvitation  = errors.New("invalid or expired team invitation")
	ErrTeamMemberNotFound = errors.New("team member not found")
	ErrLastTeamAdmin      = errors.New("cannot remove the last active Product Owner")
	// ErrLastSystemAdmin: the team must keep at least one active member with the
	// system administrator right (gate override), otherwise nobody could ever
	// pass a task after a required gate failed.
	ErrLastSystemAdmin         = errors.New("cannot remove the last active system administrator")
	ErrTeamMemberRolesRequired = errors.New("team member must have at least one role")
	// ErrOIDCAccountMismatch is returned when a provider account (iss, sub)
	// does not match the account bound to the member, or is already bound to
	// another member.
	ErrOIDCAccountMismatch = errors.New("oidc account does not match the team member binding")
)

// TeamMember is the server-authoritative membership. Roles from access-token
// claims are ignored after this row exists.
type TeamMember struct {
	ActorID      string    `json:"actor_id"`
	Email        string    `json:"email"`
	Roles        []string  `json:"roles"`
	Status       string    `json:"status"`
	SessionEpoch int64     `json:"-"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type TeamAuditEntry struct {
	ID            int64           `json:"id"`
	ActorID       string          `json:"actor_id"`
	Action        string          `json:"action"`
	TargetActorID string          `json:"target_actor_id"`
	Details       json.RawMessage `json:"details"`
	CreatedAt     time.Time       `json:"created_at"`
}

func (s *Store) ListTeamAudit(limit int) ([]TeamAuditEntry, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id,actor_id,action,target_actor_id,detail_json,created_at FROM team_audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := make([]TeamAuditEntry, 0)
	for rows.Next() {
		var v TeamAuditEntry
		var details string
		if err = rows.Scan(&v.ID, &v.ActorID, &v.Action, &v.TargetActorID, &details, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Details = json.RawMessage(details)
		entries = append(entries, v)
	}
	return entries, rows.Err()
}

func (s *Store) TeamMember(actorID string) (*TeamMember, error) {
	return scanTeamMember(s.db.QueryRow(`SELECT actor_id,email,roles_json,status,session_epoch,updated_at
		FROM team_members WHERE actor_id=?`, actorID))
}

type rowScanner interface{ Scan(...any) error }

func scanTeamMember(row rowScanner) (*TeamMember, error) {
	var member TeamMember
	var roles string
	err := row.Scan(&member.ActorID, &member.Email, &roles, &member.Status, &member.SessionEpoch, &member.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(roles), &member.Roles); err != nil {
		return nil, err
	}
	return &member, nil
}

func (s *Store) TeamMemberCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM team_members`).Scan(&count)
	return count, err
}

// BootstrapTeamAdmin installs the first Product Owner as the single-team
// administrator. It succeeds only while the registry is empty.
func (s *Store) BootstrapTeamAdmin(actorID string, now time.Time) error {
	// The creator of the team is the owner: product_owner plus the system
	// administrator right (S06), which is a separate right and not implied by
	// product_owner for anybody invited later.
	roles, _ := json.Marshal([]string{"admin", "product_owner"})
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM team_members`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("team registry is already initialized")
	}
	email := actorID
	if _, err = tx.Exec(`INSERT INTO team_members(actor_id,email,roles_json,status,session_epoch,created_at,updated_at) VALUES(?,?,?,'active',1,?,?)`, actorID, email, string(roles), now, now); err != nil {
		return err
	}
	if err = teamAudit(tx, actorID, "bootstrap", actorID, `{"roles":["admin","product_owner"]}`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListTeamMembers() ([]TeamMember, error) {
	rows, err := s.db.Query(`SELECT actor_id,email,roles_json,status,session_epoch,updated_at FROM team_members ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var members []TeamMember
	for rows.Next() {
		var m TeamMember
		var roles string
		if err := rows.Scan(&m.ActorID, &m.Email, &roles, &m.Status, &m.SessionEpoch, &m.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(roles), &m.Roles); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// InviteTeamMember creates an inactive member and stores only a hash of the
// one-time activation token. The caller returns the raw token once.
func (s *Store) InviteTeamMember(actorID, email string, roles []string, tokenHash string, expires, now time.Time, by string) error {
	if len(roles) == 0 {
		return ErrTeamMemberRolesRequired
	}
	encoded, err := json.Marshal(roles)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`INSERT INTO team_members(actor_id,email,roles_json,status,session_epoch,created_at,updated_at)
		VALUES(?,?,?,'invited',1,?,?)`, actorID, email, string(encoded), now, now); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO team_invitations(token_hash,actor_id,expires_at,created_at,invited_by) VALUES(?,?,?,?,?)`, tokenHash, actorID, expires, now, by); err != nil {
		return err
	}
	if err = teamAudit(tx, by, "invite", actorID, `{"roles":`+string(encoded)+`}`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ActivateTeamInvitation(tokenHash string, now time.Time, prepare func(*TeamMember) error) (*TeamMember, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var actorID string
	err = tx.QueryRow(`SELECT actor_id FROM team_invitations WHERE token_hash=? AND used_at IS NULL AND expires_at>?`, tokenHash, now).Scan(&actorID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidInvitation
	}
	if err != nil {
		return nil, err
	}
	member, err := scanTeamMember(tx.QueryRow(`SELECT actor_id,email,roles_json,status,session_epoch,updated_at FROM team_members WHERE actor_id=?`, actorID))
	if err != nil {
		return nil, err
	}
	if member == nil || member.Status != "invited" {
		return nil, ErrInvalidInvitation
	}
	if prepare != nil {
		if err = prepare(member); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(`UPDATE team_invitations SET used_at=? WHERE token_hash=? AND used_at IS NULL`, now, tokenHash); err != nil {
		return nil, err
	}
	result, err := tx.Exec(`UPDATE team_members SET status='active',updated_at=? WHERE actor_id=? AND status='invited'`, now, actorID)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		return nil, ErrInvalidInvitation
	}
	if err = teamAudit(tx, actorID, "activate", actorID, `{}`, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.TeamMember(actorID)
}

func (s *Store) SetTeamMember(actorID string, roles []string, active bool, by, action string, now time.Time) error {
	if active && len(roles) == 0 {
		return ErrTeamMemberRolesRequired
	}
	encoded, err := json.Marshal(roles)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var previousRoles string
	var previousStatus string
	if err = tx.QueryRow(`SELECT roles_json,status FROM team_members WHERE actor_id=?`, actorID).Scan(&previousRoles, &previousStatus); errors.Is(err, sql.ErrNoRows) {
		return ErrTeamMemberNotFound
	} else if err != nil {
		return err
	}
	var oldRoles []string
	if err = json.Unmarshal([]byte(previousRoles), &oldRoles); err != nil {
		return err
	}
	removesOwner := containsRole(oldRoles, "product_owner") && (!active || !containsRole(roles, "product_owner") || previousStatus != "active")
	if removesOwner && previousStatus == "active" {
		var ownerCount int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM team_members WHERE status='active' AND roles_json LIKE '%"product_owner"%'`).Scan(&ownerCount); err != nil {
			return err
		}
		if ownerCount <= 1 {
			return ErrLastTeamAdmin
		}
	}
	removesAdmin := containsRole(oldRoles, "admin") && (!active || !containsRole(roles, "admin") || previousStatus != "active")
	if removesAdmin && previousStatus == "active" {
		var adminCount int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM team_members WHERE status='active' AND roles_json LIKE '%"admin"%'`).Scan(&adminCount); err != nil {
			return err
		}
		if adminCount <= 1 {
			return ErrLastSystemAdmin
		}
	}
	status := "active"
	if !active {
		status = "revoked"
	}
	// Revocation also clears the SSO account binding: restoring the member
	// lets the next sign-in bind a new provider account (e.g. after the
	// provider or the person's account changed).
	result, err := tx.Exec(`UPDATE team_members SET roles_json=?,status=?,session_epoch=session_epoch+1,updated_at=?,
		oidc_issuer=CASE WHEN ?='revoked' THEN '' ELSE oidc_issuer END,
		oidc_subject=CASE WHEN ?='revoked' THEN '' ELSE oidc_subject END WHERE actor_id=?`,
		string(encoded), status, now, status, status, actorID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTeamMemberNotFound
	}
	if status == "revoked" {
		// Personal MCP tokens die with the membership: re-inviting the same
		// actor must not bring an old token back to life.
		if _, err = tx.Exec(`UPDATE mcp_tokens SET revoked_at=? WHERE actor_id=? AND revoked_at IS NULL`, now.UnixMilli(), actorID); err != nil {
			return err
		}
	}
	if err = teamAudit(tx, by, action, actorID, `{"roles":`+string(encoded)+`}`, now); err != nil {
		return err
	}
	return tx.Commit()
}

// BindTeamMemberOIDC binds an active member to a provider account on the
// first SSO sign-in and verifies the binding on every later one. The member
// is found by verified e-mail; once bound, only the same (issuer, subject)
// signs in as that member, so a provider that reassigns the address to
// another account cannot take the membership over. An account already bound
// to another member is refused. Revoking the member clears the binding.
func (s *Store) BindTeamMemberOIDC(actorID, issuer, subject string, now time.Time) error {
	if actorID == "" || issuer == "" || subject == "" {
		return ErrOIDCAccountMismatch
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var boundIssuer, boundSubject, status string
	err = tx.QueryRow(`SELECT oidc_issuer,oidc_subject,status FROM team_members WHERE actor_id=?`, actorID).
		Scan(&boundIssuer, &boundSubject, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTeamMemberNotFound
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return ErrTeamMemberNotFound
	}
	if boundSubject != "" {
		if boundIssuer != issuer || boundSubject != subject {
			return ErrOIDCAccountMismatch
		}
		return nil
	}
	var other int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM team_members WHERE oidc_issuer=? AND oidc_subject=? AND actor_id<>?`,
		issuer, subject, actorID).Scan(&other); err != nil {
		return err
	}
	if other != 0 {
		return ErrOIDCAccountMismatch
	}
	result, err := tx.Exec(`UPDATE team_members SET oidc_issuer=?,oidc_subject=? WHERE actor_id=? AND oidc_subject=''`,
		issuer, subject, actorID)
	if isUniqueConstraint(err) {
		// Another member took the same account between the check above and
		// this update (a concurrent first sign-in): idx_team_members_oidc
		// refuses it. That is the same refusal as the check, not a store
		// failure, so the caller answers 403 rather than 503.
		return ErrOIDCAccountMismatch
	}
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return ErrOIDCAccountMismatch
	}
	details, err := json.Marshal(map[string]string{"issuer": issuer, "subject": subject})
	if err != nil {
		return err
	}
	if err = teamAudit(tx, actorID, "oidc_bind", actorID, string(details), now); err != nil {
		return err
	}
	return tx.Commit()
}

// TeamMemberOIDC returns the provider account bound to the member, if any.
func (s *Store) TeamMemberOIDC(actorID string) (string, string, error) {
	var issuer, subject string
	err := s.db.QueryRow(`SELECT oidc_issuer,oidc_subject FROM team_members WHERE actor_id=?`, actorID).Scan(&issuer, &subject)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrTeamMemberNotFound
	}
	return issuer, subject, err
}

func containsRole(roles []string, role string) bool {
	for _, value := range roles {
		if value == role {
			return true
		}
	}
	return false
}

func (s *Store) ValidateTeamSession(actorID string, epoch int64) (*TeamMember, error) {
	m, err := s.TeamMember(actorID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	if m.Status != "active" || m.SessionEpoch != epoch {
		return nil, nil
	}
	return m, nil
}

func teamAudit(tx *sql.Tx, actor, action, target, details string, at time.Time) error {
	_, err := tx.Exec(`INSERT INTO team_audit(actor_id,action,target_actor_id,detail_json,created_at) VALUES(?,?,?,?,?)`, actor, action, target, details, at)
	return err
}

// isUniqueConstraint reports whether err is a SQLite UNIQUE constraint
// violation.
func isUniqueConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}
