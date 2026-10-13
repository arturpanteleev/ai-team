package web

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// openCursorStore returns a file-backed store with a few events and a second
// raw handle for injecting SQLite failures.
func openCursorStore(t *testing.T) (*store.Store, *sql.DB, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var tail int64
	for range 3 {
		event := &store.Event{RunID: "cursor-run", Type: "attempt_finished", Stage: "qa", Timestamp: time.Now().UTC(), DataJSON: `{}`}
		if err := s.AppendEventNext(event); err != nil {
			t.Fatal(err)
		}
		tail = event.ID
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return s, raw, tail
}

func TestIntegrationStartCursorInitializesAtTailAndResumes(t *testing.T) {
	s, _, tail := openCursorStore(t)
	cursor, err := integrationStartCursor(s, "tracker", tail)
	if err != nil || cursor != tail {
		t.Fatalf("first enable cursor=%d err=%v, want tail %d", cursor, err, tail)
	}
	if err := s.SetIntegrationCursor("tracker", tail-1); err != nil {
		t.Fatal(err)
	}
	cursor, err = integrationStartCursor(s, "tracker", tail+10)
	if err != nil || cursor != tail {
		t.Fatalf("restart cursor=%d err=%v, want saved %d", cursor, err, tail)
	}
}

func TestIntegrationStartCursorFailsInsteadOfReplayingFromStart(t *testing.T) {
	t.Run("initialization write fails", func(t *testing.T) {
		s, raw, tail := openCursorStore(t)
		if _, err := raw.Exec(`CREATE TRIGGER deny_cursor BEFORE INSERT ON integration_cursors
			BEGIN SELECT RAISE(ABORT, 'cursor storage unavailable'); END`); err != nil {
			t.Fatal(err)
		}
		if cursor, err := integrationStartCursor(s, "tracker", tail); err == nil {
			t.Fatalf("cursor=%d, want an error instead of a replay position", cursor)
		}
	})
	t.Run("read fails", func(t *testing.T) {
		s, raw, tail := openCursorStore(t)
		if _, err := raw.Exec(`ALTER TABLE integration_cursors RENAME TO integration_cursors_unavailable`); err != nil {
			t.Fatal(err)
		}
		if cursor, err := integrationStartCursor(s, "tracker", tail); err == nil {
			t.Fatalf("cursor=%d, want an error instead of a replay position", cursor)
		}
	})
}

func TestIntegrationConsumerIsNotStartedWithoutDurableCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	target := t.TempDir()
	srv, err := NewServer(path, "", target, WithTargetDir(target))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`CREATE TRIGGER deny_cursor BEFORE INSERT ON integration_cursors
		BEGIN SELECT RAISE(ABORT, 'cursor storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	handled := false
	if srv.startIntegrationConsumer(context.Background(), "tracker", 0, func(context.Context, store.Event) bool {
		handled = true
		return true
	}) {
		t.Fatal("consumer must not start when its cursor cannot be made durable")
	}
	if handled {
		t.Fatal("no event may be handled without a durable cursor")
	}
	if _, found, err := srv.store.IntegrationCursor("tracker"); err != nil || found {
		t.Fatalf("cursor found=%v err=%v", found, err)
	}
}

func TestDurableNotificationWorkerFailsWithoutDurableCursor(t *testing.T) {
	s, raw, _ := openCursorStore(t)
	if _, err := raw.Exec(`CREATE TRIGGER deny_cursor BEFORE INSERT ON integration_cursors
		BEGIN SELECT RAISE(ABORT, 'cursor storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	dispatcher := notifier.NewDispatcher(nil, nil, "")
	worker, err := NewNotificationWorker(s, dispatcher, WithDurableNotificationCursor("notifier:test"))
	if err == nil || worker != nil {
		t.Fatalf("worker=%v err=%v, want no worker without a durable cursor", worker, err)
	}
}
