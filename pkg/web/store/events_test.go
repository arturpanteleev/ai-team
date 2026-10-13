package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func queryPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "; ")
}

// BL-25: the task-scoped feed filters by run_uid and pages by event ID, so
// both directions must be served by the (run_uid, id) index.
func TestRunEventQueriesUseRunIDIndex(t *testing.T) {
	s := newTestStore(t)
	for _, plan := range []string{
		queryPlan(t, s.db, runEventsBeforeQuery, "run", 10, 5),
		queryPlan(t, s.db, runEventsAfterQuery, "run", 10, 5),
	} {
		if !strings.Contains(plan, "idx_events_run_id") || strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("run event page must use idx_events_run_id without sorting, plan: %s", plan)
		}
	}
}

// Schema 12 adds the index to an existing version-11 file, and reopening an
// upgraded file is a no-op.
func TestSchemaTwelveAddsRunEventIndexIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEventNext(&Event{RunID: "run-a", Type: "run_started", Timestamp: time.Now().UTC(), DataJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	// Downgrade the file to what a version-11 binary left behind.
	if _, err := s.db.Exec(`DROP INDEX idx_events_run_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version = 12`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	for range 2 {
		reopened, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		var indexes, latest, events int
		if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_events_run_id'`).Scan(&indexes); err != nil {
			t.Fatal(err)
		}
		if err := reopened.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&latest); err != nil {
			t.Fatal(err)
		}
		if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
			t.Fatal(err)
		}
		_ = reopened.Close()
		if indexes != 1 || latest != 12 || events != 1 {
			t.Fatalf("reopen: index=%d version=%d events=%d, want 1/12/1", indexes, latest, events)
		}
	}
}

func TestGetEventsBeforeFiltersRunAndPagesDown(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	for _, runID := range []string{"run-a", "run-b", "run-a", "run-b", "run-a"} {
		if err := s.AppendEventNext(&Event{RunID: runID, Type: "note", Timestamp: now, DataJSON: `{}`}); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(values []Event) []int64 {
		out := make([]int64, 0, len(values))
		for _, value := range values {
			out = append(out, value.ID)
		}
		return out
	}
	newest, err := s.GetEventsBefore("run-a", 0, 2)
	if err != nil || len(newest) != 2 || newest[0].ID != 5 || newest[1].ID != 3 {
		t.Fatalf("newest run-a page = %v (%v), want [5 3]", ids(newest), err)
	}
	older, err := s.GetEventsBefore("run-a", 3, 10)
	if err != nil || len(older) != 1 || older[0].ID != 1 {
		t.Fatalf("older run-a page = %v (%v), want [1]", ids(older), err)
	}
	all, err := s.GetEventsBefore("", 4, 10)
	if err != nil || len(all) != 3 || all[0].ID != 3 || all[2].ID != 1 {
		t.Fatalf("global page before 4 = %v (%v), want [3 2 1]", ids(all), err)
	}
}
