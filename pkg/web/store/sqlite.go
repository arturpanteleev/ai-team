package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 5

type PipelineRun struct {
	ID             int64      `json:"id"`
	RunID          string     `json:"run_id"`
	Feature        string     `json:"feature"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ConfigSnapshot string     `json:"config_snapshot,omitempty"`
	QueueJobID     int64      `json:"queue_job_id,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type Stage struct {
	ID            int64      `json:"id"`
	PipelineRunID int64      `json:"pipeline_run_id"`
	AttemptID     string     `json:"attempt_id"`
	StageIndex    int        `json:"stage_index"`
	AgentName     string     `json:"agent_name"`
	Status        string     `json:"status"`
	Execution     string     `json:"execution,omitempty"`
	Decision      string     `json:"decision,omitempty"`
	Outcome       string     `json:"outcome,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	DurationMs    int64      `json:"duration_ms"`
	Error         string     `json:"error,omitempty"`
	Verdict       string     `json:"verdict,omitempty"`
	InputsJSON    string     `json:"inputs_json,omitempty"`
	OutputsJSON   string     `json:"outputs_json,omitempty"`
	ChecksJSON    string     `json:"checks_json,omitempty"`
	MutationsJSON string     `json:"mutations_json,omitempty"`
	DeliveryJSON  string     `json:"delivery_json,omitempty"`
}

type Event struct {
	ID        int64     `json:"id"`
	RunID     string    `json:"run_id"`
	Sequence  int64     `json:"sequence"`
	Type      string    `json:"type"`
	AttemptID string    `json:"attempt_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	DataJSON  string    `json:"data_json,omitempty"`
}

type Store struct{ db *sql.DB }

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	// A single pooled connection makes PRAGMA settings deterministic and still
	// allows cross-process readers/writers through WAL + busy_timeout.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA synchronous = NORMAL`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if dbPath != ":memory:" {
		if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreatePipelineRun(run *PipelineRun) error {
	result, err := s.db.Exec(
		"INSERT INTO pipeline_runs (run_uid, feature, status, started_at, config_snapshot) VALUES (?, ?, ?, ?, ?)",
		run.RunID, run.Feature, run.Status, run.StartedAt, run.ConfigSnapshot,
	)
	if err != nil {
		return err
	}
	run.ID, err = result.LastInsertId()
	return err
}

// AdmitPipelineRun makes an accepted command visible before a worker claims it.
func (s *Store) AdmitPipelineRun(run *PipelineRun) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`INSERT INTO pipeline_runs (run_uid, feature, status, started_at, config_snapshot)
		VALUES (?, ?, 'queued', ?, ?) ON CONFLICT DO NOTHING`, run.RunID, run.Feature, run.StartedAt, run.ConfigSnapshot)
	if err != nil {
		return err
	}
	stored, err := s.scanRun(tx.QueryRow(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at,
		completed_at, COALESCE(config_snapshot, ''), COALESCE(queue_job_id, 0), COALESCE(admission_error, '')
		FROM pipeline_runs WHERE run_uid = ?`, run.RunID))
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*run = *stored
	return nil
}

// MarkRunQueued correlates the durable queue identity with the dashboard row.
func (s *Store) MarkRunQueued(runID string, queueJobID int64) error {
	return s.MarkRunQueuedStatus(runID, queueJobID, "pending", "", time.Now().UTC())
}

func (s *Store) MarkRunQueuedStatus(runID string, queueJobID int64, status string, cause string, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE pipeline_runs SET queue_job_id = ?,
		status = CASE
			WHEN status = 'queued' AND ? IN ('running', 'failed', 'canceled') THEN ?
			WHEN status = 'failed' AND COALESCE(queue_job_id, 0) = 0 AND ? IN ('pending', 'queued') THEN 'queued'
			WHEN status = 'failed' AND COALESCE(queue_job_id, 0) = 0 AND ? = 'running' THEN 'running'
			WHEN status = 'failed' AND COALESCE(queue_job_id, 0) = 0 AND ? IN ('failed', 'canceled') THEN ?
			ELSE status END,
		completed_at = CASE
			WHEN status = 'failed' AND COALESCE(queue_job_id, 0) = 0 AND ? IN ('pending', 'queued', 'running') THEN NULL
			WHEN ? IN ('failed', 'canceled') THEN ? ELSE completed_at END,
		admission_error = CASE
			WHEN status = 'failed' AND COALESCE(queue_job_id, 0) = 0 AND ? IN ('pending', 'queued', 'running') THEN ''
			WHEN ? IN ('failed', 'canceled') THEN ?
			WHEN status = 'queued' AND ? IN ('pending', 'queued', 'running') THEN ''
			ELSE admission_error END
		WHERE run_uid = ?`, queueJobID,
		status, status, status, status, status, status,
		status, status, at,
		status, status, cause, status, runID)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "pipeline run queue identity"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkQueuedCanceled(runID string, at time.Time) (bool, error) {
	result, err := s.db.Exec(`UPDATE pipeline_runs SET status = 'canceled', completed_at = ?
		WHERE run_uid = ? AND status = 'queued'`, at, runID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) UpdateQueueProjection(queueJobID int64, status, cause string, at time.Time) (string, bool, error) {
	var runID string
	var current string
	err := s.db.QueryRow(`SELECT COALESCE(run_uid, ''), status FROM pipeline_runs WHERE queue_job_id = ?`, queueJobID).Scan(&runID, &current)
	if err != nil {
		return "", false, err
	}
	if current == status {
		return runID, false, nil
	}
	var result sql.Result
	if status == "failed" || status == "canceled" {
		result, err = s.db.Exec(`UPDATE pipeline_runs SET status = ?, completed_at = ?, admission_error = ?
			WHERE queue_job_id = ? AND status IN ('queued', 'running')`, status, at, cause, queueJobID)
	} else if status == "running" {
		result, err = s.db.Exec(`UPDATE pipeline_runs SET status = 'running', completed_at = NULL, admission_error = '' WHERE queue_job_id = ? AND status = 'queued'`, queueJobID)
	}
	if err != nil {
		return runID, false, err
	}
	if result == nil {
		return runID, false, nil
	}
	affected, err := result.RowsAffected()
	return runID, affected > 0, err
}

func (s *Store) QueuedRunIDs() ([]struct {
	RunID      string
	QueueJobID int64
}, error) {
	rows, err := s.db.Query(`SELECT run_uid, queue_job_id FROM pipeline_runs WHERE queue_job_id > 0 AND status IN ('queued', 'running')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var values []struct {
		RunID      string
		QueueJobID int64
	}
	for rows.Next() {
		var value struct {
			RunID      string
			QueueJobID int64
		}
		if err := rows.Scan(&value.RunID, &value.QueueJobID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// PendingAdmissions are visible start requests whose queue identity has not
// yet been correlated. The task is stored with the admission so startup can
// safely recreate a missing scheduler job after a process crash.
func (s *Store) PendingAdmissions() ([]PipelineRun, error) {
	rows, err := s.db.Query(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at,
		completed_at, COALESCE(config_snapshot, ''), COALESCE(queue_job_id, 0), COALESCE(admission_error, '')
		FROM pipeline_runs WHERE status IN ('queued', 'failed') AND COALESCE(queue_job_id, 0) = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var runs []PipelineRun
	for rows.Next() {
		var run PipelineRun
		if err := rows.Scan(&run.ID, &run.RunID, &run.Feature, &run.Status, &run.StartedAt,
			&run.CompletedAt, &run.ConfigSnapshot, &run.QueueJobID, &run.Error); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Store) MarkRunAdmissionFailure(runID, cause string, at time.Time) (bool, error) {
	result, err := s.db.Exec(`UPDATE pipeline_runs SET status = 'failed', completed_at = ?, admission_error = ?
		WHERE run_uid = ? AND COALESCE(queue_job_id, 0) = 0 AND status IN ('queued', 'running')`, at, cause, runID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

// StartAdmittedRun changes the pre-created queue projection to running, or
// inserts a normal local run if no admission projection exists.
func (s *Store) StartAdmittedRun(run *PipelineRun) error {
	result, err := s.db.Exec(`INSERT INTO pipeline_runs (run_uid, feature, status, started_at, config_snapshot)
		VALUES (?, ?, 'running', ?, ?) ON CONFLICT DO UPDATE SET
		feature = excluded.feature, status = 'running', started_at = excluded.started_at,
		completed_at = NULL, config_snapshot = excluded.config_snapshot, admission_error = ''`,
		run.RunID, run.Feature, run.StartedAt, run.ConfigSnapshot)
	if err != nil {
		return err
	}
	stored, err := s.GetPipelineRunByRunID(run.RunID)
	if err != nil {
		return err
	}
	run.ID = stored.ID
	_, err = result.RowsAffected()
	return err
}

func (s *Store) UpdatePipelineRun(run *PipelineRun) error {
	result, err := s.db.Exec(
		"UPDATE pipeline_runs SET status = ?, completed_at = ? WHERE id = ? AND (? = '' OR run_uid = ?)",
		run.Status, run.CompletedAt, run.ID, run.RunID, run.RunID,
	)
	if err != nil {
		return err
	}
	return requireAffected(result, "pipeline run")
}

func (s *Store) ResumePipelineRun(runID string) (*PipelineRun, error) {
	run, err := s.GetPipelineRunByRunID(runID)
	if err != nil {
		return nil, err
	}
	result, err := s.db.Exec(`UPDATE pipeline_runs SET status = 'running', completed_at = NULL WHERE id = ? AND run_uid = ?`, run.ID, runID)
	if err != nil {
		return nil, err
	}
	if err := requireAffected(result, "resumed pipeline run"); err != nil {
		return nil, err
	}
	run.Status, run.CompletedAt = "running", nil
	return run, nil
}

func (s *Store) LatestRunEventSequence(runID string) (int64, error) {
	var sequence int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(sequence), 0) FROM events WHERE run_uid = ?`, runID).Scan(&sequence)
	return sequence, err
}

func (s *Store) CreateStage(stage *Stage) error {
	result, err := s.db.Exec(`INSERT INTO stages
		(pipeline_run_id, attempt_uid, stage_index, agent_name, status, execution, decision, outcome, started_at, error, verdict, inputs_json, outputs_json, checks_json, mutations_json, delivery_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		stage.PipelineRunID, stage.AttemptID, stage.StageIndex, stage.AgentName, stage.Status,
		stage.Execution, stage.Decision, stage.Outcome, stage.StartedAt, stage.Error, stage.Verdict,
		stage.InputsJSON, stage.OutputsJSON, stage.ChecksJSON, stage.MutationsJSON, stage.DeliveryJSON,
	)
	if err != nil {
		return err
	}
	stage.ID, err = result.LastInsertId()
	return err
}

func (s *Store) UpdateStage(stage *Stage) error {
	result, err := s.db.Exec(`UPDATE stages SET status = ?, execution = ?, decision = ?, outcome = ?, completed_at = ?, duration_ms = ?,
		error = ?, verdict = ?, inputs_json = ?, outputs_json = ?, checks_json = ?, mutations_json = ?, delivery_json = ?
		WHERE id = ? AND (? = '' OR attempt_uid = ?)`,
		stage.Status, stage.Execution, stage.Decision, stage.Outcome, stage.CompletedAt, stage.DurationMs,
		stage.Error, stage.Verdict, stage.InputsJSON, stage.OutputsJSON, stage.ChecksJSON, stage.MutationsJSON, stage.DeliveryJSON,
		stage.ID, stage.AttemptID, stage.AttemptID,
	)
	if err != nil {
		return err
	}
	return requireAffected(result, "stage")
}

func (s *Store) InvalidateAttempts(pipelineRunID int64, attemptIDs []string, at time.Time) error {
	transaction, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }() // Rollback после успешного Commit возвращает ErrTxDone — штатный исход этого defer.
	for _, attemptID := range attemptIDs {
		result, updateErr := transaction.Exec(`UPDATE stages SET status = 'invalidated', outcome = 'invalidated', completed_at = COALESCE(completed_at, ?)
			WHERE pipeline_run_id = ? AND attempt_uid = ?`, at, pipelineRunID, attemptID)
		if updateErr != nil {
			return updateErr
		}
		if err := requireAffected(result, "invalidated stage"); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (s *Store) AppendEvent(event *Event) error {
	result, err := s.db.Exec(`INSERT INTO events (run_uid, sequence, type, attempt_uid, timestamp, data_json) VALUES (?, ?, ?, ?, ?, ?)`,
		event.RunID, event.Sequence, event.Type, event.AttemptID, event.Timestamp, event.DataJSON)
	if err != nil {
		return err
	}
	event.ID, err = result.LastInsertId()
	return err
}

// AppendEventNext allocates the next per-run sequence inside one transaction,
// so asynchronous queue projection and pipeline recorder events cannot collide.
func (s *Store) AppendEventNext(event *Event) error {
	return s.db.QueryRow(`INSERT INTO events(run_uid, sequence, type, attempt_uid, timestamp, data_json)
		SELECT ?, COALESCE(MAX(sequence), 0) + 1, ?, ?, ?, ? FROM events WHERE run_uid = ? RETURNING sequence`,
		event.RunID, event.Type, event.AttemptID, event.Timestamp, event.DataJSON, event.RunID).Scan(&event.Sequence)
}

func (s *Store) GetEventsAfter(cursor int64, limit int) ([]Event, error) {
	if cursor < 0 || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid event query cursor=%d limit=%d", cursor, limit)
	}
	rows, err := s.db.Query(`SELECT id, run_uid, sequence, type, COALESCE(attempt_uid, ''), timestamp, COALESCE(data_json, '')
		FROM events WHERE id > ? ORDER BY id ASC LIMIT ?`, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // курсор освобождается на выходе; значимые ошибки чтения уже обработаны выше.
	events := make([]Event, 0)
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.RunID, &event.Sequence, &event.Type, &event.AttemptID, &event.Timestamp, &event.DataJSON); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) LatestEventCursor() (int64, error) {
	var cursor int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&cursor)
	return cursor, err
}

func (s *Store) GetPipelineRuns() ([]PipelineRun, error) { return s.GetPipelineRunsPage(100, 0) }

func (s *Store) GetPipelineRunsPage(limit, offset int) ([]PipelineRun, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, fmt.Errorf("invalid pagination limit=%d offset=%d", limit, offset)
	}
	rows, err := s.db.Query(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at, completed_at, COALESCE(config_snapshot, ''), COALESCE(queue_job_id, 0), COALESCE(admission_error, '')
		FROM pipeline_runs ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // курсор освобождается на выходе; значимые ошибки чтения уже обработаны выше.
	var runs []PipelineRun
	for rows.Next() {
		var run PipelineRun
		if err := rows.Scan(&run.ID, &run.RunID, &run.Feature, &run.Status, &run.StartedAt, &run.CompletedAt, &run.ConfigSnapshot, &run.QueueJobID, &run.Error); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if runs == nil {
		runs = make([]PipelineRun, 0)
	}
	return runs, rows.Err()
}

func (s *Store) CountPipelineRuns() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs`).Scan(&count)
	return count, err
}

func (s *Store) GetPipelineRunByID(id int64) (*PipelineRun, error) {
	return s.scanRun(s.db.QueryRow(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at, completed_at, COALESCE(config_snapshot, ''), COALESCE(queue_job_id, 0), COALESCE(admission_error, '') FROM pipeline_runs WHERE id = ?`, id))
}

func (s *Store) GetPipelineRunByRunID(runID string) (*PipelineRun, error) {
	return s.scanRun(s.db.QueryRow(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at, completed_at, COALESCE(config_snapshot, ''), COALESCE(queue_job_id, 0), COALESCE(admission_error, '') FROM pipeline_runs WHERE run_uid = ?`, runID))
}

func (s *Store) scanRun(row *sql.Row) (*PipelineRun, error) {
	var run PipelineRun
	if err := row.Scan(&run.ID, &run.RunID, &run.Feature, &run.Status, &run.StartedAt, &run.CompletedAt, &run.ConfigSnapshot, &run.QueueJobID, &run.Error); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) GetStagesByPipelineRunID(pipelineRunID int64) ([]Stage, error) {
	rows, err := s.db.Query(`SELECT id, pipeline_run_id, COALESCE(attempt_uid, ''), COALESCE(stage_index, 0), agent_name, status,
		COALESCE(execution, ''), COALESCE(decision, ''), COALESCE(outcome, ''), started_at, completed_at, duration_ms,
		COALESCE(error, ''), COALESCE(verdict, ''), COALESCE(inputs_json, ''), COALESCE(outputs_json, ''),
		COALESCE(checks_json, ''), COALESCE(mutations_json, ''), COALESCE(delivery_json, '')
		FROM stages WHERE pipeline_run_id = ? ORDER BY stage_index, id`, pipelineRunID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // курсор освобождается на выходе; значимые ошибки чтения уже обработаны выше.
	var stages []Stage
	for rows.Next() {
		var stage Stage
		if err := rows.Scan(&stage.ID, &stage.PipelineRunID, &stage.AttemptID, &stage.StageIndex, &stage.AgentName, &stage.Status,
			&stage.Execution, &stage.Decision, &stage.Outcome, &stage.StartedAt, &stage.CompletedAt, &stage.DurationMs,
			&stage.Error, &stage.Verdict, &stage.InputsJSON, &stage.OutputsJSON, &stage.ChecksJSON, &stage.MutationsJSON, &stage.DeliveryJSON); err != nil {
			return nil, err
		}
		stages = append(stages, stage)
	}
	if stages == nil {
		stages = make([]Stage, 0)
	}
	return stages, rows.Err()
}

// ReconcileInterrupted is called only after the workspace lock is held.
func (s *Store) ReconcileInterrupted(at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // Rollback после успешного Commit возвращает ErrTxDone — штатный исход этого defer.
	if _, err := tx.Exec(`UPDATE stages SET status = 'interrupted', outcome = 'failed', completed_at = ? WHERE status = 'running'`, at); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE pipeline_runs SET status = 'interrupted', completed_at = ? WHERE status = 'running'`, at); err != nil {
		return err
	}
	return tx.Commit()
}

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // Rollback после успешного Commit возвращает ErrTxDone — штатный исход этого defer.
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL)`); err != nil {
		return err
	}
	var latest sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&latest); err != nil {
		return err
	}
	if latest.Valid && latest.Int64 > SchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", latest.Int64, SchemaVersion)
	}
	base := []string{
		`CREATE TABLE IF NOT EXISTS pipeline_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, feature TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'running',
			started_at DATETIME NOT NULL, completed_at DATETIME, config_snapshot TEXT)`,
		`CREATE TABLE IF NOT EXISTS stages (
			id INTEGER PRIMARY KEY AUTOINCREMENT, pipeline_run_id INTEGER NOT NULL, agent_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'running', started_at DATETIME NOT NULL, completed_at DATETIME,
			duration_ms INTEGER DEFAULT 0, error TEXT, verdict TEXT, inputs_json TEXT, outputs_json TEXT,
			FOREIGN KEY (pipeline_run_id) REFERENCES pipeline_runs(id))`,
	}
	for _, query := range base {
		if _, err := tx.Exec(query); err != nil {
			return err
		}
	}
	columns := []struct{ table, name, declaration string }{
		{"pipeline_runs", "run_uid", "TEXT"},
		{"pipeline_runs", "queue_job_id", "INTEGER DEFAULT 0"}, {"pipeline_runs", "admission_error", "TEXT"},
		{"stages", "attempt_uid", "TEXT"}, {"stages", "stage_index", "INTEGER DEFAULT 0"},
		{"stages", "execution", "TEXT"}, {"stages", "decision", "TEXT"}, {"stages", "outcome", "TEXT"},
		{"stages", "checks_json", "TEXT"}, {"stages", "mutations_json", "TEXT"}, {"stages", "delivery_json", "TEXT"},
	}
	for _, column := range columns {
		if err := ensureColumn(tx, column.table, column.name, column.declaration); err != nil {
			return err
		}
	}
	queries := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_runs_uid ON pipeline_runs(run_uid) WHERE run_uid IS NOT NULL AND run_uid <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_pipeline_runs_started ON pipeline_runs(started_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_stages_run ON stages(pipeline_run_id, stage_index, id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_stages_attempt ON stages(attempt_uid) WHERE attempt_uid IS NOT NULL AND attempt_uid <> ''`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT, run_uid TEXT NOT NULL, sequence INTEGER NOT NULL, type TEXT NOT NULL,
			attempt_uid TEXT, timestamp DATETIME NOT NULL, data_json TEXT,
			UNIQUE(run_uid, sequence))`,
		`CREATE INDEX IF NOT EXISTS idx_events_run ON events(run_uid, sequence)`,
		`CREATE TABLE IF NOT EXISTS team_members (
			actor_id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE, roles_json TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('invited','active','revoked')),
			session_epoch INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS team_invitations (
			token_hash TEXT PRIMARY KEY, actor_id TEXT NOT NULL, expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL, used_at DATETIME, invited_by TEXT NOT NULL,
			FOREIGN KEY(actor_id) REFERENCES team_members(actor_id))`,
		`CREATE TABLE IF NOT EXISTS team_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT, actor_id TEXT NOT NULL, action TEXT NOT NULL,
			target_actor_id TEXT NOT NULL, detail_json TEXT NOT NULL DEFAULT '{}', created_at DATETIME NOT NULL)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, CURRENT_TIMESTAMP), (2, CURRENT_TIMESTAMP), (3, CURRENT_TIMESTAMP), (4, CURRENT_TIMESTAMP), (5, CURRENT_TIMESTAMP)`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ensureColumn(tx *sql.Tx, table, name, declaration string) error {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	exists := false
	for rows.Next() {
		var cid int
		var columnName, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		exists = exists || columnName == name
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + declaration)
	return err
}

func requireAffected(result sql.Result, entity string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%s update affected %d rows", entity, affected)
	}
	return nil
}
