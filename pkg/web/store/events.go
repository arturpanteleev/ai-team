package store

import (
	"fmt"
	"math"
)

const eventColumns = `SELECT id, run_uid, sequence, type, COALESCE(stage, ''), COALESCE(actor, ''), COALESCE(executor, ''), COALESCE(severity, ''), COALESCE(attempt_uid, ''), timestamp, COALESCE(data_json, '')`

// Run-scoped pages are served by idx_events_run_id (run_uid, id).
const (
	runEventsAfterQuery  = eventColumns + ` FROM events WHERE run_uid = ? AND id > ? ORDER BY id ASC LIMIT ?`
	runEventsBeforeQuery = eventColumns + ` FROM events WHERE run_uid = ? AND id < ? ORDER BY id DESC LIMIT ?`
	eventsBeforeQuery    = eventColumns + ` FROM events WHERE id < ? ORDER BY id DESC LIMIT ?`
)

// GetRunEventsAfter pages the global event cursor for one run. Cursor always
// refers to the SQLite event ID so clients can resume the same stream across
// both task-scoped and global feeds.
func (s *Store) GetRunEventsAfter(runID string, cursor int64, limit int) ([]Event, error) {
	if runID == "" || cursor < 0 || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid run event query run=%q cursor=%d limit=%d", runID, cursor, limit)
	}
	rows, err := s.db.Query(runEventsAfterQuery, runID, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := make([]Event, 0)
	for rows.Next() {
		var value Event
		if err := rows.Scan(&value.ID, &value.RunID, &value.Sequence, &value.Type, &value.Stage, &value.Actor, &value.Executor, &value.Severity, &value.AttemptID, &value.Timestamp, &value.DataJSON); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// GetEventsBefore pages the global event cursor newest-first. An empty runID
// selects every run; cursor 0 starts from the newest persisted event, and any
// positive cursor returns only events with a smaller SQLite event ID.
func (s *Store) GetEventsBefore(runID string, cursor int64, limit int) ([]Event, error) {
	if cursor < 0 || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid descending event query run=%q cursor=%d limit=%d", runID, cursor, limit)
	}
	// Concrete predicates instead of "(? = '' OR run_uid = ?)": SQLite cannot
	// choose idx_events_run_id (run_uid, id) for an OR with a bound parameter.
	upper := cursor
	if upper == 0 {
		upper = math.MaxInt64
	}
	query, args := eventsBeforeQuery, []any{upper, limit}
	if runID != "" {
		query, args = runEventsBeforeQuery, []any{runID, upper, limit}
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := make([]Event, 0)
	for rows.Next() {
		var value Event
		if err := rows.Scan(&value.ID, &value.RunID, &value.Sequence, &value.Type, &value.Stage, &value.Actor, &value.Executor, &value.Severity, &value.AttemptID, &value.Timestamp, &value.DataJSON); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
