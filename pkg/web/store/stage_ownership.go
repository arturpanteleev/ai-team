package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StageOwner is the latest durable claim for one stage. Claims are projected
// from the append-only stage_taken events, so the takeover history remains
// available in the same event stream.
type StageOwner struct {
	StageID   string    `json:"stage_id"`
	ActorID   string    `json:"actor_id"`
	ActorRole string    `json:"actor_role"`
	TakenAt   time.Time `json:"taken_at"`
}

type stageTakenData struct {
	StageID           string `json:"stage_id"`
	ActorID           string `json:"actor_id"`
	ActorRole         string `json:"actor_role"`
	ActorName         string `json:"actor_name"`
	PreviousActorID   string `json:"previous_actor_id,omitempty"`
	PreviousActorRole string `json:"previous_actor_role,omitempty"`
	PreviousActorName string `json:"previous_actor_name,omitempty"`
}

// TakeStage appends a claim event and returns the new current owner. The
// previous owner is read and the new event is appended within one SQLite
// transaction, so concurrent requests through this store form one takeover
// sequence. Repeating a claim by the current owner is idempotent.
func (s *Store) TakeStage(runID, stageID, actorID, actorRole string, at time.Time) (StageOwner, bool, error) {
	runID, stageID, actorID, actorRole = strings.TrimSpace(runID), strings.TrimSpace(stageID), strings.TrimSpace(actorID), strings.TrimSpace(actorRole)
	if runID == "" || stageID == "" || actorID == "" || actorRole == "" || strings.ContainsAny(runID+stageID+actorID+actorRole, "\r\n\x00") {
		return StageOwner{}, false, errors.New("stage ownership identity is incomplete")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return StageOwner{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM pipeline_runs WHERE run_uid = ?`, runID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StageOwner{}, false, fmt.Errorf("run %s not found", runID)
		}
		return StageOwner{}, false, err
	}

	rows, err := tx.Query(`SELECT timestamp, COALESCE(data_json, '') FROM events
		WHERE run_uid = ? AND type = 'stage_taken' ORDER BY sequence ASC`, runID)
	if err != nil {
		return StageOwner{}, false, err
	}
	var previous *StageOwner
	for rows.Next() {
		var timestamp time.Time
		var encoded string
		if err := rows.Scan(&timestamp, &encoded); err != nil {
			_ = rows.Close()
			return StageOwner{}, false, err
		}
		var data stageTakenData
		if err := json.Unmarshal([]byte(encoded), &data); err != nil {
			_ = rows.Close()
			return StageOwner{}, false, fmt.Errorf("decode stage_taken event: %w", err)
		}
		if data.StageID == stageID {
			previous = &StageOwner{StageID: stageID, ActorID: data.ActorID, ActorRole: data.ActorRole, TakenAt: timestamp.UTC()}
		}
	}
	if err := rows.Close(); err != nil {
		return StageOwner{}, false, err
	}
	if previous != nil && previous.ActorID == actorID {
		if err := tx.Commit(); err != nil {
			return StageOwner{}, false, err
		}
		return *previous, false, nil
	}

	data := stageTakenData{StageID: stageID, ActorID: actorID, ActorRole: actorRole, ActorName: actorID}
	if previous != nil {
		data.PreviousActorID = previous.ActorID
		data.PreviousActorRole = previous.ActorRole
		data.PreviousActorName = previous.ActorID
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return StageOwner{}, false, err
	}
	var sequence int64
	if err := tx.QueryRow(`INSERT INTO events(run_uid, sequence, type, attempt_uid, timestamp, data_json)
		SELECT ?, COALESCE(MAX(sequence), 0) + 1, 'stage_taken', NULL, ?, ? FROM events WHERE run_uid = ? RETURNING sequence`,
		runID, at, string(encoded), runID).Scan(&sequence); err != nil {
		return StageOwner{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return StageOwner{}, false, err
	}
	return StageOwner{StageID: stageID, ActorID: actorID, ActorRole: actorRole, TakenAt: at}, true, nil
}

// GetStageOwners folds stage_taken events into the latest owner per stage.
func (s *Store) GetStageOwners(runID string) (map[string]StageOwner, error) {
	rows, err := s.db.Query(`SELECT timestamp, COALESCE(data_json, '') FROM events
		WHERE run_uid = ? AND type = 'stage_taken' ORDER BY sequence ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	owners := make(map[string]StageOwner)
	for rows.Next() {
		var timestamp time.Time
		var encoded string
		if err := rows.Scan(&timestamp, &encoded); err != nil {
			return nil, err
		}
		var data stageTakenData
		if err := json.Unmarshal([]byte(encoded), &data); err != nil {
			return nil, fmt.Errorf("decode stage_taken event: %w", err)
		}
		if data.StageID == "" || data.ActorID == "" || data.ActorRole == "" {
			return nil, errors.New("stage_taken event has incomplete owner")
		}
		owners[data.StageID] = StageOwner{StageID: data.StageID, ActorID: data.ActorID, ActorRole: data.ActorRole, TakenAt: timestamp.UTC()}
	}
	return owners, rows.Err()
}
