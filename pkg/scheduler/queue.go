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
		`CREATE UNIQUE INDEX IF NOT EXISTS worker_jobs_active_identity
		 ON worker_jobs(run_id, operation)
		 WHERE status IN ('queued', 'running')`,
		`CREATE INDEX IF NOT EXISTS worker_jobs_claim
		 ON worker_jobs(status, created_ms, id)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("scheduler migration: %w", err)
		}
	}
	return &Queue{db: db, options: options}, nil
}

func (q *Queue) Close() error { return q.db.Close() }

func (q *Queue) Enqueue(job worker.Job) (int64, error) {
	if err := job.Validate(job.TargetDir); err != nil {
		return 0, err
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return 0, err
	}
	now := q.options.Now().UTC().UnixMilli()
	result, err := q.db.Exec(
		`INSERT INTO worker_jobs
		 (run_id, operation, target_dir, payload_json, status, created_ms, updated_ms)
		 VALUES (?, ?, ?, ?, 'queued', ?, ?)`,
		job.RunID, job.Operation, job.TargetDir, string(payload), now, now,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return result.LastInsertId()
}

func (q *Queue) Claim(ctx context.Context, owner string) (Record, bool, error) {
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
		 WHERE status = 'queued' ORDER BY created_ms, id LIMIT 32`)
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
			rows.Close()
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
		return q.Get(value.id)
	}
	return Record{}, false, nil
}

func (q *Queue) Renew(ctx context.Context, id int64, owner, token string) (bool, error) {
	var cancelRequested bool
	var status, actualOwner, actualToken string
	if err := q.db.QueryRowContext(ctx,
		`SELECT status, lease_owner, lease_token, cancel_requested
		 FROM worker_jobs WHERE id = ?`, id,
	).Scan(&status, &actualOwner, &actualToken, &cancelRequested); err != nil {
		return false, err
	}
	if status != string(StatusRunning) || actualOwner != owner || actualToken != token {
		return false, ErrLeaseLost
	}
	if cancelRequested {
		return true, nil
	}
	now := q.options.Now().UTC()
	result, err := q.db.ExecContext(ctx,
		`UPDATE worker_jobs SET lease_expires_ms = ?, updated_ms = ?
		 WHERE id = ? AND status = 'running' AND lease_owner = ? AND lease_token = ?`,
		now.Add(q.options.LeaseDuration).UnixMilli(), now.UnixMilli(), id, owner, token,
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
		 WHERE id = ? AND status = 'running' AND lease_owner = ? AND lease_token = ?`,
		status, diagnostic, now, id, owner, token,
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
			rows.Close()
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
