package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var trackerTestTime = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func newTrackerStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestTrackerLinkIsOneToOneEnforcedByTheDatabase(t *testing.T) {
	s := newTrackerStore(t)
	if _, err := s.SetTrackerLink(1, "jira", "APP-1", "po", trackerTestTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTrackerLink(1, "jira", "APP-1", "po", trackerTestTime); err != nil {
		t.Fatalf("same pair must be idempotent: %v", err)
	}
	if _, err := s.SetTrackerLink(1, "jira", "APP-2", "po", trackerTestTime); !errors.Is(err, ErrTaskAlreadyLinked) {
		t.Fatalf("second issue for a task: %v", err)
	}
	if _, err := s.SetTrackerLink(2, "jira", "APP-1", "po", trackerTestTime); !errors.Is(err, ErrIssueAlreadyLinked) {
		t.Fatalf("second task for an issue: %v", err)
	}
	// Even a writer that skips the Go checks cannot break the invariant.
	if _, err := s.db.Exec(`INSERT INTO tracker_links (task_id, tracker, issue_key, linked_at) VALUES (1, 'jira', 'APP-9', CURRENT_TIMESTAMP)`); err == nil {
		t.Fatal("database allowed a second issue for one task")
	}
	if _, err := s.db.Exec(`INSERT INTO tracker_links (task_id, tracker, issue_key, linked_at) VALUES (3, 'jira', 'APP-1', CURRENT_TIMESTAMP)`); err == nil {
		t.Fatal("database allowed a second task for one issue")
	}
	// The same key in another tracker is a different card.
	if _, err := s.SetTrackerLink(2, "fake", "APP-1", "po", trackerTestTime); err != nil {
		t.Fatalf("same key in a different tracker: %v", err)
	}
	if removed, err := s.DeleteTrackerLink(1); err != nil || !removed {
		t.Fatalf("delete: %v %v", removed, err)
	}
	if removed, _ := s.DeleteTrackerLink(1); removed {
		t.Fatal("deleting twice must report nothing removed")
	}
}

func TestTrackerDeliveryRecordsAttemptsAndKeepsErrorUntilDelivered(t *testing.T) {
	s := newTrackerStore(t)
	d := TrackerDelivery{EventID: 5, TaskID: 1, Tracker: "jira", IssueKey: "APP-1", EventType: "attempt_finished", Stage: "qa",
		Status: TrackerDeliveryFailed, LastError: "tracker down"}
	first, err := s.RecordTrackerDelivery(d, trackerTestTime)
	if err != nil || first.Attempts != 1 || first.DeliveredAt != nil || first.LastError != "tracker down" {
		t.Fatalf("first=%+v %v", first, err)
	}
	second, err := s.RecordTrackerDelivery(d, trackerTestTime.Add(time.Minute))
	if err != nil || second.Attempts != 2 || !second.CreatedAt.Equal(trackerTestTime) {
		t.Fatalf("second=%+v %v", second, err)
	}
	d.Status, d.LastError = TrackerDeliveryDelivered, ""
	done, err := s.RecordTrackerDelivery(d, trackerTestTime.Add(2*time.Minute))
	if err != nil || done.Status != TrackerDeliveryDelivered || done.LastError != "" || done.DeliveredAt == nil || done.Attempts != 3 {
		t.Fatalf("done=%+v %v", done, err)
	}
	if failed, _ := s.ListTrackerDeliveries(0, TrackerDeliveryFailed, 10); len(failed) != 0 {
		t.Fatalf("delivered event still listed as failed: %+v", failed)
	}
	if _, err := s.GetTrackerDelivery(404); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatalf("missing delivery: %v", err)
	}
}

func TestVersion15DatabaseMigratesToTrackerLinks(t *testing.T) {
	if SchemaVersion < 16 {
		t.Fatalf("test written for the 15 -> 16 step, SchemaVersion=%d", SchemaVersion)
	}
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePipelineRun(&PipelineRun{RunID: "old", Feature: "f", Status: "completed", StartedAt: trackerTestTime,
		ConfigSnapshot: `{"external_links":["APP-1"]}`}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DROP TABLE tracker_links`, `DROP TABLE tracker_link_conflicts`, `DROP TABLE tracker_link_imports`,
		`DROP TABLE tracker_deliveries`, `DELETE FROM schema_migrations WHERE version > 15`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatalf("v15 database must migrate: %v", err)
	}
	defer func() { _ = s.Close() }()
	var latest int64
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&latest); err != nil || latest != SchemaVersion {
		t.Fatalf("schema version=%d (%v)", latest, err)
	}
	// Existing tasks are untouched and unlinked until the import runs.
	run, err := s.GetPipelineRunByRunID("old")
	if err != nil || run.ConfigSnapshot == "" {
		t.Fatalf("old run lost data: %+v %v", run, err)
	}
	if link, err := s.GetTrackerLink(run.ID); err != nil || link != nil {
		t.Fatalf("migration must not link by itself: %+v %v", link, err)
	}
	extract := func(snapshot string) []string {
		if strings.Contains(snapshot, "APP-1") {
			return []string{"APP-1"}
		}
		return nil
	}
	result, err := s.ImportTrackerLinks("jira", extract, trackerTestTime)
	if err != nil || result.Linked != 1 || result.Conflicts != 0 {
		t.Fatalf("import=%+v %v", result, err)
	}
	if again, err := s.ImportTrackerLinks("jira", extract, trackerTestTime); err != nil || !again.Skipped {
		t.Fatalf("second import must be skipped: %+v %v", again, err)
	}
}
