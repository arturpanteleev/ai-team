package store

import (
	"fmt"
)

// GetRunEventsAfter pages the global event cursor for one run. Cursor always
// refers to the SQLite event ID so clients can resume the same stream across
// both task-scoped and global feeds.
func (s *Store) GetRunEventsAfter(runID string, cursor int64, limit int) ([]Event, error) {
	if runID == "" || cursor < 0 || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid run event query run=%q cursor=%d limit=%d", runID, cursor, limit)
	}
	rows, err := s.db.Query(`SELECT id, run_uid, sequence, type, COALESCE(attempt_uid, ''), timestamp, COALESCE(data_json, '')
		FROM events WHERE run_uid = ? AND id > ? ORDER BY id ASC LIMIT ?`, runID, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := make([]Event, 0)
	for rows.Next() {
		var value Event
		if err := rows.Scan(&value.ID, &value.RunID, &value.Sequence, &value.Type, &value.AttemptID, &value.Timestamp, &value.DataJSON); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
