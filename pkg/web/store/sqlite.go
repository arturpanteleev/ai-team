package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 3

type PipelineRun struct {
	ID             int64      `json:"id"`
	RunID          string     `json:"run_id"`
	Feature        string     `json:"feature"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ConfigSnapshot string     `json:"config_snapshot,omitempty"`
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
	defer transaction.Rollback()
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

func (s *Store) GetPipelineRuns() ([]PipelineRun, error) { return s.GetPipelineRunsPage(100, 0) }

func (s *Store) GetPipelineRunsPage(limit, offset int) ([]PipelineRun, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, fmt.Errorf("invalid pagination limit=%d offset=%d", limit, offset)
	}
	rows, err := s.db.Query(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at, completed_at, COALESCE(config_snapshot, '')
		FROM pipeline_runs ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []PipelineRun
	for rows.Next() {
		var run PipelineRun
		if err := rows.Scan(&run.ID, &run.RunID, &run.Feature, &run.Status, &run.StartedAt, &run.CompletedAt, &run.ConfigSnapshot); err != nil {
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
	return s.scanRun(s.db.QueryRow(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at, completed_at, COALESCE(config_snapshot, '') FROM pipeline_runs WHERE id = ?`, id))
}

func (s *Store) GetPipelineRunByRunID(runID string) (*PipelineRun, error) {
	return s.scanRun(s.db.QueryRow(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at, completed_at, COALESCE(config_snapshot, '') FROM pipeline_runs WHERE run_uid = ?`, runID))
}

func (s *Store) scanRun(row *sql.Row) (*PipelineRun, error) {
	var run PipelineRun
	if err := row.Scan(&run.ID, &run.RunID, &run.Feature, &run.Status, &run.StartedAt, &run.CompletedAt, &run.ConfigSnapshot); err != nil {
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
	defer rows.Close()
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
	defer tx.Rollback()
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
	defer tx.Rollback()
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
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, CURRENT_TIMESTAMP), (2, CURRENT_TIMESTAMP), (3, CURRENT_TIMESTAMP)`,
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
			rows.Close()
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
