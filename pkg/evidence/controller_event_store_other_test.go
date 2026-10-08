//go:build !linux && !darwin

package evidence

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnsupportedControllerEventStoreFallsBackToLocalFiles(t *testing.T) {
	target := t.TempDir()
	runID := "unsupported-platform-local-events"
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := Start(filepath.Join(target, ".ai-team", "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_started", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.RunDir(), "events.jsonl")
	events, err := VerifyEventLog(path, runID)
	if err != nil || len(events) != 1 || events[0].Type != "run_started" {
		t.Fatalf("file-backed local event log: events=%+v err=%v", events, err)
	}
	if _, err := ReadEventLogBytesForRunDir(store.RunDir(), runID); err != nil {
		t.Fatalf("file-backed local archive read: %v", err)
	}
	if source, reserved, err := ResolveEventLogSource(target, store.RunDir(), runID); err != nil || reserved || source == nil {
		t.Fatalf("local event source=%v reserved=%v err=%v", source, reserved, err)
	}
	if candidate, err := (ControllerEventStore{TargetDir: target}).IsLegacyMigrationCandidate(runID); candidate || !errors.Is(err, errControllerEventStoreUnsupported) {
		t.Fatalf("legacy migration candidate on unsupported platform: candidate=%v err=%v", candidate, err)
	}
}

func TestUnsupportedControllerEventAuthorityFailsClosed(t *testing.T) {
	target := t.TempDir()
	for _, runID := range []string{"unsupported-marker", "unsupported-canonical-data"} {
		if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
			t.Fatal(err)
		}
		run, err := Start(filepath.Join(target, ".ai-team", "runs"), testRunManifest(runID))
		if err != nil {
			t.Fatal(err)
		}
		if err := run.Append(Event{Type: "run_started", Timestamp: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if runID == "unsupported-marker" {
			marker := filepath.Join(target, ".ai-team", "state", "runs", "event-log-reservations", runID+".json")
			if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte("unsupported authority marker"), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			canonical := filepath.Join(target, ".ai-team", "state", "events", runID)
			if err := os.MkdirAll(canonical, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(canonical, "events.jsonl"), []byte("controller data"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := VerifyEventLog(filepath.Join(run.RunDir(), "events.jsonl"), runID); !errors.Is(err, errControllerEventStoreUnsupported) {
			t.Fatalf("VerifyEventLog(%s) should reject unsupported controller authority, got %v", runID, err)
		}
		if _, err := ReadEventLogBytesForRunDir(run.RunDir(), runID); !errors.Is(err, errControllerEventStoreUnsupported) {
			t.Fatalf("archive read(%s) should reject unsupported controller authority, got %v", runID, err)
		}
	}
}
