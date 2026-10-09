package approval

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	_ "modernc.org/sqlite"
)

// SQLiteStore keeps approvals in the controller database. It implements the
// same validation and decision rules as Store while making each read/modify/
// write atomic across processes. The caller owns the database lifecycle.
type SQLiteStore struct{ db *sql.DB }

// HasAuthenticatedControllerDecision reports whether this approval carries
// provenance set by the authenticated controller decision endpoint. Merely
// storing a record in SQLite (including a legacy import) is not sufficient.
func (s *SQLiteStore) HasAuthenticatedControllerDecision(value PendingApproval) bool {
	if s == nil || s.db == nil || value.Status != StatusResolved || len(value.Decisions) == 0 {
		return false
	}
	for _, decision := range value.Decisions {
		if !decision.ControllerAuthenticated || decision.ApprovalID != value.ID ||
			decision.SubjectHash != value.SubjectHash || decision.Action != value.ResolvedAction {
			return false
		}
	}
	return true
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{`PRAGMA busy_timeout = 5000`, `PRAGMA foreign_keys = ON`} {
		if _, err := db.Exec(q); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if path != ":memory:" {
		if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS approval_schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("approval migration: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS approval_records (
		run_id TEXT NOT NULL, approval_id TEXT NOT NULL, record_json TEXT NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY(run_id, approval_id))`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("approval migration: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS approval_records_run_created ON approval_records(run_id, created_at, approval_id)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO approval_schema_migrations(version, applied_at) VALUES(1, CURRENT_TIMESTAMP)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("approval migration version: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

// ImportLegacy copies file-backed approvals into SQLite without removing the
// source files. It is safe to rerun: an existing row is accepted only when it
// represents the same approval request and contains at least the legacy
// decision history. Conflicting rows fail closed and are never overwritten.
func (s *SQLiteStore) ImportLegacy(root string) error {
	legacyStore := &Store{root: root}
	return s.importLegacy(root, legacyStore.lockRun)
}

// importLegacy accepts the lock acquisition function so its concurrency
// contract can be exercised without timing guesses in tests.
func (s *SQLiteStore) importLegacy(root string, acquireRunLock func(string) (func(), error)) error {
	rootInfo, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("legacy approvals root must be a directory without symlink: %s", root)
	}
	var legacy []PendingApproval
	runs, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	// Keep the same per-run lock used by the file store from the start of the
	// snapshot until the SQLite transaction commits. Otherwise a concurrent
	// file-backed Decide could publish after we read a pending record and have
	// its human decision silently omitted from the imported state.
	var unlocks []func()
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()
	for _, run := range runs {
		runID := run.Name()
		if !safeName(runID) || run.Type()&os.ModeSymlink != 0 || !run.IsDir() {
			return fmt.Errorf("legacy approval run directory is invalid: %s", filepath.Join(root, runID))
		}
		unlock, lockErr := acquireRunLock(runID)
		if lockErr != nil {
			return fmt.Errorf("lock legacy approval run %s: %w", runID, lockErr)
		}
		unlocks = append(unlocks, unlock)
		entries, readErr := os.ReadDir(filepath.Join(root, runID))
		if readErr != nil {
			return readErr
		}
		for _, entry := range entries {
			if entry.Name() == ".decide.lock" {
				lockInfo, statErr := os.Lstat(filepath.Join(root, runID, entry.Name()))
				if statErr != nil || lockInfo.Mode()&os.ModeSymlink != 0 || !lockInfo.Mode().IsRegular() {
					return fmt.Errorf("legacy approval lock must be a regular file without symlink: %s", filepath.Join(root, runID, entry.Name()))
				}
				continue
			}
			if !strings.HasSuffix(entry.Name(), ".json") {
				return fmt.Errorf("unexpected legacy approval entry: %s", filepath.Join(root, runID, entry.Name()))
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			if !safeName(id) || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				return fmt.Errorf("legacy approval must be a regular JSON file: %s", filepath.Join(root, runID, entry.Name()))
			}
			data, readErr := safeio.ReadRegularFile(filepath.Join(root, runID, entry.Name()), MaxApprovalRecordBytes)
			if readErr != nil {
				return readErr
			}
			var value PendingApproval
			if decodeErr := strictjson.Unmarshal(data, MaxApprovalRecordBytes, &value); decodeErr != nil {
				return fmt.Errorf("legacy approval %s: %w", id, decodeErr)
			}
			normalize(&value)
			if validateErr := validate(value); validateErr != nil {
				return fmt.Errorf("legacy approval %s: %w", id, validateErr)
			}
			if value.RunID != runID || value.ID != id {
				return fmt.Errorf("legacy approval identity mismatch: %s/%s", runID, id)
			}
			// Legacy JSON decisions predate authenticated-controller provenance.
			// Never let an imported file claim to have passed the web auth path.
			for i := range value.Decisions {
				value.Decisions[i].ControllerAuthenticated = false
			}
			legacy = append(legacy, value)
		}
	}
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	rollback := func() { _, _ = conn.ExecContext(ctx, `ROLLBACK`) }
	for _, old := range legacy {
		current, loadErr := loadApproval(conn, old.RunID, old.ID)
		if loadErr == nil {
			if !legacyCompatibleWithCurrent(old, current) {
				rollback()
				return fmt.Errorf("legacy approval %s/%s conflicts with SQLite row; resolve manually before switching stores", old.RunID, old.ID)
			}
			continue
		}
		if !errors.Is(loadErr, sql.ErrNoRows) {
			rollback()
			return loadErr
		}
		data, marshalErr := marshalApprovalRecord(old)
		if marshalErr != nil {
			rollback()
			return marshalErr
		}
		stamp := old.CreatedAt.UTC().Format(time.RFC3339Nano)
		if _, err = conn.ExecContext(ctx, `INSERT INTO approval_records(run_id,approval_id,record_json,created_at,updated_at) VALUES(?,?,?,?,?)`, old.RunID, old.ID, string(data), stamp, stamp); err != nil {
			rollback()
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		rollback()
		return err
	}
	return nil
}

func legacyCompatibleWithCurrent(legacy, current PendingApproval) bool {
	if !sameRequest(legacy, current) || len(current.Decisions) < len(legacy.Decisions) {
		return false
	}
	for i := range legacy.Decisions {
		if !reflect.DeepEqual(legacy.Decisions[i], current.Decisions[i]) {
			return false
		}
	}
	if legacy.Status == StatusResolved {
		return current.Status == StatusResolved && current.ResolvedAction == legacy.ResolvedAction &&
			current.ResolvedAt.Equal(legacy.ResolvedAt) && len(current.Decisions) == len(legacy.Decisions)
	}
	return true
}

func (s *SQLiteStore) Create(value PendingApproval) (PendingApproval, error) {
	value.SchemaVersion = SchemaVersion
	value.Status, value.ResolvedAction, value.ResolvedAt, value.Decisions = StatusPending, "", time.Time{}, nil
	if value.Quorum == "" {
		value.Quorum = QuorumAny
	}
	if value.ID == "" {
		value.ID = NewID(value.RunID, value.AttemptID, value.FromStage, value.ToStage, value.Trigger, value.SubjectHash)
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC()
	} else {
		value.CreatedAt = value.CreatedAt.UTC()
	}
	normalize(&value)
	if err := validate(value); err != nil {
		return PendingApproval{}, err
	}
	return s.withWrite(func(conn *sql.Conn) (PendingApproval, error) {
		old, err := loadApproval(conn, value.RunID, value.ID)
		if err == nil {
			if sameRequest(old, value) {
				return old, nil
			}
			return PendingApproval{}, fmt.Errorf("approval %s уже существует с другим subject", value.ID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return PendingApproval{}, err
		}
		data, err := marshalApprovalRecord(value)
		if err != nil {
			return PendingApproval{}, err
		}
		_, err = conn.ExecContext(context.Background(), `INSERT INTO approval_records(run_id,approval_id,record_json,created_at,updated_at) VALUES(?,?,?,?,?)`, value.RunID, value.ID, string(data), value.CreatedAt.UTC().Format(time.RFC3339Nano), value.CreatedAt.UTC().Format(time.RFC3339Nano))
		return value, err
	})
}

func (s *SQLiteStore) Load(runID, approvalID string) (PendingApproval, error) {
	if !safeName(runID) || !safeName(approvalID) {
		return PendingApproval{}, errors.New("недопустимый approval path identity")
	}
	return loadApproval(s.db, runID, approvalID)
}

func (s *SQLiteStore) List(runID string) ([]PendingApproval, error) {
	if !safeName(runID) {
		return nil, errors.New("недопустимый run_id")
	}
	rows, err := s.db.Query(`SELECT record_json FROM approval_records WHERE run_id = ? ORDER BY created_at, approval_id`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := []PendingApproval{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var value PendingApproval
		if err := strictjson.Unmarshal([]byte(data), MaxApprovalRecordBytes, &value); err != nil {
			return nil, fmt.Errorf("approval record: %w", err)
		}
		normalize(&value)
		if err := validate(value); err != nil {
			return nil, err
		}
		if value.RunID != runID {
			return nil, errors.New("approval identity mismatch")
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) Decide(runID, approvalID string, decision Decision) (PendingApproval, error) {
	return s.mutate(runID, approvalID, func(value PendingApproval) (PendingApproval, error) {
		return applyDecision(value, approvalID, decision)
	})
}

func (s *SQLiteStore) ResolveDeferred(runID, approvalID string, decision Decision) (PendingApproval, error) {
	return s.mutate(runID, approvalID, func(value PendingApproval) (PendingApproval, error) {
		return applyDeferredDecision(value, approvalID, decision)
	})
}

func (s *SQLiteStore) mutate(runID, approvalID string, fn func(PendingApproval) (PendingApproval, error)) (PendingApproval, error) {
	return s.withWrite(func(conn *sql.Conn) (PendingApproval, error) {
		value, err := loadApproval(conn, runID, approvalID)
		if err != nil {
			return PendingApproval{}, err
		}
		updated, err := fn(value)
		if err != nil {
			return PendingApproval{}, err
		}
		if updated.ID == value.ID && updated.Status == value.Status && len(updated.Decisions) == len(value.Decisions) {
			return updated, nil
		}
		data, err := marshalApprovalRecord(updated)
		if err != nil {
			return PendingApproval{}, err
		}
		_, err = conn.ExecContext(context.Background(), `UPDATE approval_records SET record_json=?, updated_at=? WHERE run_id=? AND approval_id=?`, string(data), time.Now().UTC().Format(time.RFC3339Nano), runID, approvalID)
		return updated, err
	})
}

func marshalApprovalRecord(value PendingApproval) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxApprovalRecordBytes {
		return nil, fmt.Errorf("approval record exceeds maximum size of %d bytes", MaxApprovalRecordBytes)
	}
	return data, nil
}

func (s *SQLiteStore) withWrite(fn func(*sql.Conn) (PendingApproval, error)) (PendingApproval, error) {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return PendingApproval{}, err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return PendingApproval{}, err
	}
	value, err := fn(conn)
	if err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return PendingApproval{}, err
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return PendingApproval{}, err
	}
	return value, nil
}

func loadApproval(q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, runID, id string) (PendingApproval, error) {
	var data string
	if err := q.QueryRowContext(context.Background(), `SELECT record_json FROM approval_records WHERE run_id=? AND approval_id=?`, runID, id).Scan(&data); err != nil {
		return PendingApproval{}, err
	}
	var value PendingApproval
	if err := strictjson.Unmarshal([]byte(data), MaxApprovalRecordBytes, &value); err != nil {
		return PendingApproval{}, fmt.Errorf("approval %s: %w", id, err)
	}
	normalize(&value)
	if err := validate(value); err != nil {
		return PendingApproval{}, err
	}
	if value.RunID != runID || value.ID != id {
		return PendingApproval{}, errors.New("approval identity mismatch")
	}
	return value, nil
}

// Keep compile-time contract local to the approval package, which is imported
// by pipeline and control stores.
var _ interface {
	Create(PendingApproval) (PendingApproval, error)
	Load(string, string) (PendingApproval, error)
	List(string) ([]PendingApproval, error)
	Decide(string, string, Decision) (PendingApproval, error)
	ResolveDeferred(string, string, Decision) (PendingApproval, error)
} = (*SQLiteStore)(nil)

var _ TrustedDecisionAuthority = (*SQLiteStore)(nil)
