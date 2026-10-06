// Package scheduler реализует persistent queue и leases distributed workers.
package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/arturpanteleev/ai-team/pkg/worker"
)

var ErrDuplicate = errors.New("active worker job уже существует")
var ErrLeaseLost = errors.New("worker lease потерян")

type Options struct {
	LeaseDuration time.Duration
	MaxConcurrent int
	PerTarget     int
	Now           func() time.Time
}

type Queue struct {
	db      *sql.DB
	options Options
}

type Status string

const (
	StatusPending   Status = "pending"
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

type Record struct {
	ID              int64
	Job             worker.Job
	Status          Status
	Attempts        int
	LeaseOwner      string
	LeaseToken      string
	LeaseExpiresAt  time.Time
	CancelRequested bool
	Error           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func Open(path string, options Options) (*Queue, error) {
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = 30 * time.Second
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = 4
	}
	if options.PerTarget <= 0 {
		options.PerTarget = 1
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE IF NOT EXISTS worker_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			target_dir TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_token TEXT NOT NULL DEFAULT '',
			lease_expires_ms INTEGER NOT NULL DEFAULT 0,
			cancel_requested INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			created_ms INTEGER NOT NULL,
			updated_ms INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS worker_jobs_claim
		 ON worker_jobs(status, created_ms, id)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			// аварийный путь инициализации: значимая ошибка возвращается вызывающему.
			_ = db.Close()
			return nil, fmt.Errorf("scheduler migration: %w", err)
		}
	}
	if err := ensureActiveIdentityIndex(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("scheduler queue identity migration: %w", err)
	}
	return &Queue{db: db, options: options}, nil
}

func ensureActiveIdentityIndex(db *sql.DB) error {
	var definition sql.NullString
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'worker_jobs_active_identity'`).Scan(&definition)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if definition.Valid && strings.Contains(definition.String, "'pending'") {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DROP INDEX IF EXISTS worker_jobs_active_identity`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX worker_jobs_active_identity ON worker_jobs(run_id, operation) WHERE status IN ('pending', 'queued', 'running')`); err != nil {
		return err
	}
	return tx.Commit()
}

func (q *Queue) Close() error { return q.db.Close() }

func (q *Queue) Enqueue(job worker.Job) (int64, error) {
	return q.enqueue(job, string(StatusQueued))
}

// EnqueuePending persists a job without making it claimable. Admission code
// activates it only after the visible run projection has its queue identity.
func (q *Queue) EnqueuePending(job worker.Job) (int64, error) {
	return q.enqueue(job, "pending")
}

// EnsureStartJob makes start admission retryable after a process crash. It
// returns the existing durable job for this run or creates one non-claimable
// until its projection is correlated.
func (q *Queue) EnsureStartJob(job worker.Job) (int64, error) {
	if job.Operation != worker.OperationStart {
		return 0, errors.New("EnsureStartJob requires start operation")
	}
	if err := job.Validate(job.TargetDir); err != nil {
		return 0, err
	}
	canonical, err := canonicalTargetPath(job.TargetDir)
	if err != nil {
		return 0, err
	}
	job.TargetDir = canonical
	payload, err := json.Marshal(job)
	if err != nil {
		return 0, err
	}
	now := q.options.Now().UTC().UnixMilli()
	// The partial unique index is the serialization point shared by foreground
	// admission and startup reconciliation. Insert first, then read the active
	// identity in the same transaction so simultaneous callers converge on one
	// durable queue ID instead of racing through ListRun + INSERT.
	tx, err := q.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(
		`INSERT INTO worker_jobs
		 (run_id, operation, target_dir, payload_json, status, created_ms, updated_ms)
		 VALUES (?, ?, ?, ?, 'pending', ?, ?)
		 ON CONFLICT(run_id, operation) WHERE status IN ('pending', 'queued', 'running') DO NOTHING`,
		job.RunID, job.Operation, job.TargetDir, string(payload), now, now,
	); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM worker_jobs
		WHERE run_id = ? AND operation = ? AND status IN ('pending', 'queued', 'running')
		ORDER BY id DESC LIMIT 1`, job.RunID, job.Operation).Scan(&id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (q *Queue) enqueue(job worker.Job, status string) (int64, error) {
	if err := job.Validate(job.TargetDir); err != nil {
		return 0, err
	}
	canonical, err := canonicalTargetPath(job.TargetDir)
	if err != nil {
		return 0, err
	}
	job.TargetDir = canonical
	payload, err := json.Marshal(job)
	if err != nil {
		return 0, err
	}
	now := q.options.Now().UTC().UnixMilli()
	result, err := q.db.Exec(
		`INSERT INTO worker_jobs
		 (run_id, operation, target_dir, payload_json, status, created_ms, updated_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		job.RunID, job.Operation, job.TargetDir, string(payload), status, now, now,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return result.LastInsertId()
}

func canonicalTargetPath(target string) (string, error) {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

func (q *Queue) Activate(id int64) error {
	result, err := q.db.Exec(`UPDATE worker_jobs SET status = 'queued', updated_ms = ? WHERE id = ? AND status = 'pending'`, q.options.Now().UTC().UnixMilli(), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		var status string
		if err := q.db.QueryRow(`SELECT status FROM worker_jobs WHERE id = ?`, id).Scan(&status); err != nil {
			return err
		}
		if status != string(StatusQueued) && status != string(StatusRunning) {
			return fmt.Errorf("scheduler job %d cannot be activated from %s", id, status)
		}
	}
	return nil
}

func (q *Queue) Claim(ctx context.Context, owner string) (Record, bool, error) {
	return q.claim(ctx, owner, "")
}

// ClaimForTarget restricts a worker to the exact persistent workspace it has
// mounted. Paths are canonicalized before matching so a worker can never take
// a job for a different project/volume.
func (q *Queue) ClaimForTarget(ctx context.Context, owner, target string) (Record, bool, error) {
	if strings.TrimSpace(target) == "" {
		return Record{}, false, errors.New("scheduler worker target обязателен")
	}
	canonical, err := filepath.Abs(target)
	if err != nil {
		return Record{}, false, err
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		return Record{}, false, err
	}
	return q.claim(ctx, owner, filepath.Clean(canonical))
}

func (q *Queue) claim(ctx context.Context, owner, target string) (Record, bool, error) {
	if strings.TrimSpace(owner) == "" {
		return Record{}, false, errors.New("scheduler worker owner обязателен")
	}
	now := q.options.Now().UTC()
	if _, err := q.db.ExecContext(ctx,
		`UPDATE worker_jobs
		 SET status = CASE WHEN cancel_requested = 1 THEN 'canceled' ELSE 'queued' END,
		     lease_owner = '', lease_token = '', lease_expires_ms = 0, updated_ms = ?
		 WHERE status = 'running' AND lease_expires_ms <= ?`,
		now.UnixMilli(), now.UnixMilli(),
	); err != nil {
		return Record{}, false, err
	}
	rows, err := q.db.QueryContext(ctx,
		`SELECT id, payload_json FROM worker_jobs
		 WHERE status = 'queued' AND (? = '' OR target_dir = ?)
	 ORDER BY created_ms, id LIMIT 32`, target, target)
	if err != nil {
		return Record{}, false, err
	}
	type candidate struct {
		id      int64
		payload string
	}
	var candidates []candidate
	for rows.Next() {
		var value candidate
		if err := rows.Scan(&value.id, &value.payload); err != nil {
			// аварийный путь: ошибка уже возвращается, Close только освобождает курсор.
			_ = rows.Close()
			return Record{}, false, err
		}
		candidates = append(candidates, value)
	}
	if err := rows.Close(); err != nil {
		return Record{}, false, err
	}
	for _, value := range candidates {
		token, err := leaseToken()
		if err != nil {
			return Record{}, false, err
		}
		expires := now.Add(q.options.LeaseDuration)
		result, err := q.db.ExecContext(ctx,
			`UPDATE worker_jobs
			 SET status = 'running', attempts = attempts + 1,
			     lease_owner = ?, lease_token = ?, lease_expires_ms = ?, updated_ms = ?
			 WHERE id = ? AND status = 'queued'
			   AND (SELECT COUNT(*) FROM worker_jobs WHERE status = 'running') < ?
			   AND (SELECT COUNT(*) FROM worker_jobs running
			        WHERE running.status = 'running'
			          AND running.target_dir = worker_jobs.target_dir) < ?`,
			owner, token, expires.UnixMilli(), now.UnixMilli(), value.id,
			q.options.MaxConcurrent, q.options.PerTarget,
		)
		if err != nil {
			return Record{}, false, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return Record{}, false, err
		}
		if affected == 0 {
			continue
		}
		record, exists, getErr := q.Get(value.id)
		if getErr != nil || !exists {
			return record, exists, getErr
		}
		// An expired start lease is not replayed as Start. Recovery is an
		// explicit worker operation that checks durable lifecycle state first.
		if record.Attempts > 1 && record.Job.Operation == worker.OperationStart {
			record.Job.Operation = worker.OperationRecover
		}
		return record, true, nil
	}
	return Record{}, false, nil
}

func (q *Queue) Renew(ctx context.Context, id int64, owner, token string) (bool, error) {
	var cancelRequested bool
	var leaseExpires int64
	var status, actualOwner, actualToken string
	if err := q.db.QueryRowContext(ctx,
		`SELECT status, lease_owner, lease_token, cancel_requested, lease_expires_ms
		 FROM worker_jobs WHERE id = ?`, id,
	).Scan(&status, &actualOwner, &actualToken, &cancelRequested, &leaseExpires); err != nil {
		return false, err
	}
	now := q.options.Now().UTC()
	if status != string(StatusRunning) || actualOwner != owner || actualToken != token || leaseExpires <= now.UnixMilli() {
		return false, ErrLeaseLost
	}
	if cancelRequested {
		return true, nil
	}
	result, err := q.db.ExecContext(ctx,
		`UPDATE worker_jobs SET lease_expires_ms = ?, updated_ms = ?
		 WHERE id = ? AND status = 'running' AND lease_owner = ? AND lease_token = ? AND lease_expires_ms > ?`,
		now.Add(q.options.LeaseDuration).UnixMilli(), now.UnixMilli(), id, owner, token, now.UnixMilli(),
	)
	if err != nil {
		return false, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return false, ErrLeaseLost
	}
	return false, nil
}

func (q *Queue) Complete(id int64, owner, token string, success bool, diagnostic string) error {
	status := StatusCompleted
	if !success {
		status = StatusFailed
	}
	if len(diagnostic) > 4096 {
		diagnostic = diagnostic[:4096] + " [truncated]"
	}
	now := q.options.Now().UTC().UnixMilli()
	result, err := q.db.Exec(
		`UPDATE worker_jobs
		 SET status = CASE WHEN cancel_requested = 1 THEN 'canceled' ELSE ? END,
		     error = ?, lease_owner = '', lease_token = '', lease_expires_ms = 0, updated_ms = ?
		 WHERE id = ? AND status = 'running' AND lease_owner = ? AND lease_token = ? AND lease_expires_ms > ?`,
		status, diagnostic, now, id, owner, token, now,
	)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (q *Queue) CancelRun(runID string) (int64, error) {
	now := q.options.Now().UTC().UnixMilli()
	result, err := q.db.Exec(
		`UPDATE worker_jobs
		 SET status = CASE WHEN status = 'queued' THEN 'canceled' ELSE status END,
		     cancel_requested = 1, updated_ms = ?
		 WHERE run_id = ? AND status IN ('queued', 'running')`,
		now, runID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (q *Queue) Get(id int64) (Record, bool, error) {
	var value Record
	var payload string
	var leaseMS, createdMS, updatedMS int64
	err := q.db.QueryRow(
		`SELECT id, payload_json, status, attempts, lease_owner, lease_token,
		        lease_expires_ms, cancel_requested, error, created_ms, updated_ms
		 FROM worker_jobs WHERE id = ?`, id,
	).Scan(
		&value.ID, &payload, &value.Status, &value.Attempts, &value.LeaseOwner,
		&value.LeaseToken, &leaseMS, &value.CancelRequested, &value.Error,
		&createdMS, &updatedMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if err := json.Unmarshal([]byte(payload), &value.Job); err != nil {
		return Record{}, false, err
	}
	if leaseMS > 0 {
		value.LeaseExpiresAt = time.UnixMilli(leaseMS).UTC()
	}
	value.CreatedAt = time.UnixMilli(createdMS).UTC()
	value.UpdatedAt = time.UnixMilli(updatedMS).UTC()
	return value, true, nil
}

func (q *Queue) ListRun(runID string) ([]Record, error) {
	rows, err := q.db.Query(`SELECT id FROM worker_jobs WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			// аварийный путь: ошибка уже возвращается, Close только освобождает курсор.
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(ids))
	for _, id := range ids {
		record, exists, err := q.Get(id)
		if err != nil {
			return nil, err
		}
		if exists {
			records = append(records, record)
		}
	}
	return records, nil
}

func leaseToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
