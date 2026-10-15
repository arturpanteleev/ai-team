package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSetTrackerDeliveryStatusKeepsTheOriginalErrorAndStoresTheReasonSeparately(t *testing.T) {
	s := newTrackerStore(t)
	d := TrackerDelivery{EventID: 7, TaskID: 1, Tracker: "jira", IssueKey: "APP-1", EventType: "attempt_finished",
		Status: TrackerDeliveryFailed, LastError: "tracker down"}
	if _, err := s.RecordTrackerDelivery(d, trackerTestTime); err != nil {
		t.Fatal(err)
	}
	closed, err := s.SetTrackerDeliveryStatus(7, TrackerDeliverySuperseded, "newer transition", trackerTestTime)
	if err != nil || closed.LastError != "tracker down" || closed.Note != "newer transition" || closed.Status != TrackerDeliverySuperseded {
		t.Fatalf("closed=%+v %v", closed, err)
	}
}

func TestRetryUpsertDoesNotResurrectAnAbandonedDelivery(t *testing.T) {
	s := newTrackerStore(t)
	if _, err := s.SetTrackerLink(1, "jira", "APP-1", "po", trackerTestTime); err != nil {
		t.Fatal(err)
	}
	d := TrackerDelivery{EventID: 9, TaskID: 1, Tracker: "jira", IssueKey: "APP-1", EventType: "attempt_finished",
		Status: TrackerDeliveryFailed, LastError: "tracker down"}
	if _, err := s.RecordTrackerDelivery(d, trackerTestTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteTrackerLink(1); err != nil {
		t.Fatal(err)
	}
	// A retry that was already in flight finishes after the unlink.
	for _, status := range []string{TrackerDeliveryDelivered, TrackerDeliveryFailed} {
		d.Status = status
		got, err := s.RecordTrackerDelivery(d, trackerTestTime.Add(time.Minute))
		if err != nil || got.Status != TrackerDeliveryAbandoned || got.Attempts != 1 {
			t.Fatalf("status %s resurrected an abandoned row: %+v %v", status, got, err)
		}
	}
}

func TestNewerTransitionCountsOnlyDeliveriesThatExecutedOne(t *testing.T) {
	s := newTrackerStore(t)
	record := func(id int64, status, issue, transition string) {
		t.Helper()
		d := TrackerDelivery{EventID: id, TaskID: 1, Tracker: "jira", IssueKey: issue, EventType: "attempt_finished",
			Status: status, TransitionTo: transition}
		if _, err := s.RecordTrackerDelivery(d, trackerTestTime); err != nil {
			t.Fatal(err)
		}
	}
	record(1, TrackerDeliveryFailed, "APP-1", "")
	record(2, TrackerDeliveryDelivered, "APP-1", "")     // comment only
	record(3, TrackerDeliveryDelivered, "X-1", "Done")   // another card
	record(5, TrackerDeliveryAbandoned, "APP-1", "Done") // closed without the tracker
	if newer, err := s.HasNewerExecutedTrackerTransition(1, "jira", "APP-1", 1); err != nil || newer {
		t.Fatalf("comment-only, other-card and abandoned rows are not newer transitions: %v %v", newer, err)
	}
	record(4, TrackerDeliveryDelivered, "APP-1", "Done")
	if newer, err := s.HasNewerExecutedTrackerTransition(1, "jira", "APP-1", 1); err != nil || !newer {
		t.Fatalf("an executed newer transition must count: %v %v", newer, err)
	}
	if newer, err := s.HasNewerExecutedTrackerTransition(1, "jira", "APP-1", 4); err != nil || newer {
		t.Fatalf("nothing is newer than event 4: %v %v", newer, err)
	}
}

func TestEarlyVersion16DeliveriesTableGainsTransitionColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DROP TABLE tracker_deliveries`,
		`CREATE TABLE tracker_deliveries (
			event_id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL, tracker TEXT NOT NULL, issue_key TEXT NOT NULL,
			event_type TEXT NOT NULL, stage TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, delivered_at DATETIME)`,
		`DELETE FROM schema_migrations WHERE version > 15`,
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
		t.Fatalf("migration: %v", err)
	}
	defer func() { _ = s.Close() }()
	d := TrackerDelivery{EventID: 1, TaskID: 1, Tracker: "jira", IssueKey: "APP-1", EventType: "attempt_finished",
		Status: TrackerDeliveryDelivered, TransitionTo: "Done", Note: "n"}
	if got, err := s.RecordTrackerDelivery(d, trackerTestTime); err != nil || got.TransitionTo != "Done" || got.Note != "n" {
		t.Fatalf("got=%+v %v", got, err)
	}
}
