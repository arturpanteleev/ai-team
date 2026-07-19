package store

import (
	"database/sql"
	"time"
)

type PipelineRun struct {
	ID             int64     `json:"id"`
	Feature        string    `json:"feature"`
	Status         string    `json:"status"`
	StartedAt      time.Time `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ConfigSnapshot string    `json:"config_snapshot,omitempty"`
}

type Stage struct {
	ID             int64     `json:"id"`
	PipelineRunID  int64     `json:"pipeline_run_id"`
	AgentName      string    `json:"agent_name"`
	Status         string    `json:"status"`
	StartedAt      time.Time `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	DurationMs     int64     `json:"duration_ms"`
	Error          string    `json:"error,omitempty"`
	InputsJSON     string    `json:"inputs_json,omitempty"`
	OutputsJSON    string    `json:"outputs_json,omitempty"`
}

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CreatePipelineRun(run *PipelineRun) error {
	result, err := s.db.Exec(
		"INSERT INTO pipeline_runs (feature, status, started_at, config_snapshot) VALUES (?, ?, ?, ?)",
		run.Feature, run.Status, run.StartedAt, run.ConfigSnapshot,
	)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	run.ID = id
	return nil
}

func (s *Store) UpdatePipelineRun(run *PipelineRun) error {
	_, err := s.db.Exec(
		"UPDATE pipeline_runs SET status = ?, completed_at = ? WHERE id = ?",
		run.Status, run.CompletedAt, run.ID,
	)
	return err
}

func (s *Store) CreateStage(stage *Stage) error {
	result, err := s.db.Exec(
		"INSERT INTO stages (pipeline_run_id, agent_name, status, started_at, error, inputs_json, outputs_json) VALUES (?, ?, ?, ?, ?, ?, ?)",
		stage.PipelineRunID, stage.AgentName, stage.Status, stage.StartedAt, stage.Error, stage.InputsJSON, stage.OutputsJSON,
	)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	stage.ID = id
	return nil
}

func (s *Store) UpdateStage(stage *Stage) error {
	_, err := s.db.Exec(
		"UPDATE stages SET status = ?, completed_at = ?, duration_ms = ?, error = ?, inputs_json = ?, outputs_json = ? WHERE id = ?",
		stage.Status, stage.CompletedAt, stage.DurationMs, stage.Error, stage.InputsJSON, stage.OutputsJSON, stage.ID,
	)
	return err
}

func (s *Store) GetPipelineRuns() ([]PipelineRun, error) {
	rows, err := s.db.Query("SELECT id, feature, status, started_at, completed_at, COALESCE(config_snapshot, '') FROM pipeline_runs ORDER BY started_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs = make([]PipelineRun, 0)
	for rows.Next() {
		var run PipelineRun
		if err := rows.Scan(&run.ID, &run.Feature, &run.Status, &run.StartedAt, &run.CompletedAt, &run.ConfigSnapshot); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func (s *Store) GetPipelineRunByID(id int64) (*PipelineRun, error) {
	var run PipelineRun
	err := s.db.QueryRow(
		"SELECT id, feature, status, started_at, completed_at, COALESCE(config_snapshot, '') FROM pipeline_runs WHERE id = ?", id,
	).Scan(&run.ID, &run.Feature, &run.Status, &run.StartedAt, &run.CompletedAt, &run.ConfigSnapshot)
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) GetStagesByPipelineRunID(pipelineRunID int64) ([]Stage, error) {
	rows, err := s.db.Query(
		"SELECT id, pipeline_run_id, agent_name, status, started_at, completed_at, duration_ms, COALESCE(error, ''), COALESCE(inputs_json, ''), COALESCE(outputs_json, '') FROM stages WHERE pipeline_run_id = ? ORDER BY id",
		pipelineRunID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stages = make([]Stage, 0)
	for rows.Next() {
		var stage Stage
		if err := rows.Scan(&stage.ID, &stage.PipelineRunID, &stage.AgentName, &stage.Status, &stage.StartedAt, &stage.CompletedAt, &stage.DurationMs, &stage.Error, &stage.InputsJSON, &stage.OutputsJSON); err != nil {
			return nil, err
		}
		stages = append(stages, stage)
	}
	return stages, nil
}

func migrate(db *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS pipeline_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			feature TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'running',
			started_at DATETIME NOT NULL,
			completed_at DATETIME,
			config_snapshot TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS stages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pipeline_run_id INTEGER NOT NULL,
			agent_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'running',
			started_at DATETIME NOT NULL,
			completed_at DATETIME,
			duration_ms INTEGER DEFAULT 0,
			error TEXT,
			inputs_json TEXT,
			outputs_json TEXT,
			FOREIGN KEY (pipeline_run_id) REFERENCES pipeline_runs(id)
		)`,
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
