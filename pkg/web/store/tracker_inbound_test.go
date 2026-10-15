package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestTrackerInboundChangeIsIdempotentByChangeID(t *testing.T) {
	s := newTrackerStore(t)
	record := TrackerInbound{Tracker: "fake", ChangeID: "42", TaskID: 7, IssueKey: "APP-1", FromStatus: "To Do", ToStatus: "Done",
		Origin: "external", ChangedAt: trackerTestTime}
	first, created, err := s.BeginTrackerInbound(record, trackerTestTime)
	if err != nil || !created || first.State != TrackerInboundReceived {
		t.Fatalf("first: %+v created=%v err=%v", first, created, err)
	}
	resolved := first
	resolved.State, resolved.Reason = TrackerInboundPending, "waits"
	if _, err := s.ResolveTrackerInbound(resolved, trackerTestTime); err != nil {
		t.Fatal(err)
	}
	again, created, err := s.BeginTrackerInbound(record, trackerTestTime.Add(time.Hour))
	if err != nil || created || again.State != TrackerInboundPending || !again.DetectedAt.Equal(trackerTestTime) {
		t.Fatalf("replay must return the stored decision: %+v created=%v err=%v", again, created, err)
	}
	// The same ID of another tracker is another change.
	if _, created, err = s.BeginTrackerInbound(TrackerInbound{Tracker: "jira", ChangeID: "42", IssueKey: "APP-1", ChangedAt: trackerTestTime}, trackerTestTime); err != nil || !created {
		t.Fatalf("other tracker: created=%v err=%v", created, err)
	}
}

func TestTrackerInboundDismissedAndSupersededStayClosed(t *testing.T) {
	s := newTrackerStore(t)
	for _, id := range []string{"1", "2"} {
		record, _, err := s.BeginTrackerInbound(TrackerInbound{Tracker: "fake", ChangeID: id, TaskID: 1, IssueKey: "APP-1", ChangedAt: trackerTestTime}, trackerTestTime)
		if err != nil {
			t.Fatal(err)
		}
		record.State = TrackerInboundPending
		if _, err = s.ResolveTrackerInbound(record, trackerTestTime); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DismissTrackerInbound("fake", "1", "po", "mistake", trackerTestTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DismissTrackerInbound("fake", "1", "po", "again", trackerTestTime); !errors.Is(err, ErrInboundNotOpen) {
		t.Fatalf("second dismissal: %v", err)
	}
	if _, err := s.DismissTrackerInbound("fake", "nope", "po", "x", trackerTestTime); !errors.Is(err, ErrInboundNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// Removing the link supersedes what is still open and nothing else.
	if _, err := s.SetTrackerLink(1, "fake", "APP-1", "po", trackerTestTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteTrackerLink(1); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	items, err := s.ListTrackerInbound(1, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		states[item.ChangeID] = item.State
	}
	if states["1"] != TrackerInboundDismissed || states["2"] != TrackerInboundSuperseded {
		t.Fatalf("states after unlink: %v", states)
	}
	// A late decision must not revive a closed request.
	late, _ := s.GetTrackerInbound("fake", "1")
	late.State = TrackerInboundApplied
	if got, err := s.ResolveTrackerInbound(late, trackerTestTime); err != nil || got.State != TrackerInboundDismissed {
		t.Fatalf("closed request revived: %+v err=%v", got, err)
	}
}

func TestTrackerSyncStateKeepsCursorAcrossFailures(t *testing.T) {
	s := newTrackerStore(t)
	if state, err := s.GetTrackerSyncState("fake"); err != nil || state != nil {
		t.Fatalf("unknown tracker: %+v %v", state, err)
	}
	if err := s.SaveTrackerSyncCursor("fake", "10", 3, trackerTestTime); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.RecordTrackerSyncFailure("fake", "timeout", trackerTestTime.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	state, err := s.GetTrackerSyncState("fake")
	if err != nil || state.Cursor != "10" || state.ConsecutiveFailures != 2 || state.LastError != "timeout" ||
		state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(trackerTestTime) {
		t.Fatalf("after failures: %+v err=%v", state, err)
	}
	if err := s.SaveTrackerSyncCursor("fake", "11", 1, trackerTestTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if state, _ = s.GetTrackerSyncState("fake"); state.Cursor != "11" || state.LastError != "" || state.ConsecutiveFailures != 0 || state.LastErrorAt != nil {
		t.Fatalf("after recovery: %+v", state)
	}
}

func TestVersion17DatabaseMigratesToTrackerInbound(t *testing.T) {
	if SchemaVersion < 18 {
		t.Fatalf("test written for the 17 -> 18 step, SchemaVersion=%d", SchemaVersion)
	}
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DROP TABLE tracker_inbound_changes`, `DROP TABLE tracker_sync_state`, `DELETE FROM schema_migrations WHERE version > 17`,
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
		t.Fatalf("v17 database must migrate: %v", err)
	}
	defer func() { _ = s.Close() }()
	var latest, count int64
	if err := s.db.QueryRow(`SELECT MAX(version), COUNT(*) FROM schema_migrations`).Scan(&latest, &count); err != nil ||
		latest != SchemaVersion || count != SchemaVersion {
		t.Fatalf("schema version=%d count=%d (%v)", latest, count, err)
	}
	record := TrackerInbound{Tracker: "fake", ChangeID: "1", TaskID: 1, IssueKey: "APP-1", ChangedAt: trackerTestTime}
	if _, created, err := s.BeginTrackerInbound(record, trackerTestTime); err != nil || !created {
		t.Fatalf("inbound table unusable after migration: created=%v err=%v", created, err)
	}
}
