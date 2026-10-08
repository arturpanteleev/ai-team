package evidence

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/metrics"
)

func TestControllerEventStoreReservationAppendRetryAndTamper(t *testing.T) {
	target := t.TempDir()
	runID := "controller-event-store"
	store := ControllerEventStore{TargetDir: target}
	if err := store.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	if reserved, err := store.IsReserved(runID); err != nil || !reserved {
		t.Fatalf("reservation status reserved=%v err=%v", reserved, err)
	}
	started := Event{Type: "run_started", Timestamp: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	first, err := store.Append(runID, started, 0, chainGenesis(runID))
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := store.Append(runID, started, 0, chainGenesis(runID)); err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatalf("exact retry=%+v err=%v want %+v", retry, err, first)
	}
	conflict := started
	conflict.Type = "run_resumed"
	if _, err := store.Append(runID, conflict, 0, chainGenesis(runID)); err == nil {
		t.Fatal("stale conflicting append was accepted")
	}
	if events, err := store.Read(runID); err != nil || len(events) != 1 || !reflect.DeepEqual(events[0], first) {
		t.Fatalf("stored events=%+v err=%v", events, err)
	}
	path, err := store.Path(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(runID); err == nil {
		t.Fatal("tampered canonical chain was accepted")
	}
}

func TestControllerEventStoreExactRetryNormalizesMissingTimestamp(t *testing.T) {
	target := t.TempDir()
	runID := "controller-event-zero-timestamp-retry"
	store := ControllerEventStore{TargetDir: target}
	if err := store.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	requested := Event{Type: "run_started"}
	first, err := store.Append(runID, requested, 0, chainGenesis(runID))
	if err != nil || first.Timestamp.IsZero() {
		t.Fatalf("append omitted-timestamp event=%+v err=%v", first, err)
	}
	retry, err := store.Append(runID, requested, 0, chainGenesis(runID))
	if err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatalf("exact omitted-timestamp retry=%+v err=%v want=%+v", retry, err, first)
	}
}

func TestControllerEventStoreMissingOrCorruptReservationFailsClosed(t *testing.T) {
	target := t.TempDir()
	runID := "controller-event-reservation"
	store := ControllerEventStore{TargetDir: target}
	if err := store.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, ".ai-team", "state", "runs", "event-log-reservations", runID+".json")
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IsReserved(runID); err == nil {
		t.Fatal("missing reservation fell back despite existing canonical event data")
	}
	if err := os.WriteFile(marker, []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IsReserved(runID); err == nil {
		t.Fatal("corrupt reservation was accepted")
	}
	path, err := store.Path(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IsReserved(runID); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reserved run with missing canonical log must fail closed: %v", err)
	}
}

func TestControllerEventStorePartialRootsFailClosedAndAbsentStoreFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name       string
		removeRoot string
	}{
		{name: "event root missing with reservation", removeRoot: "events"},
		{name: "reservation root missing with canonical data", removeRoot: "reservations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			runID := "partial-event-authority"
			store := ControllerEventStore{TargetDir: target}
			if err := store.Reserve(runID); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(target, ".ai-team", "state", "events")
			if tc.removeRoot == "reservations" {
				root = filepath.Join(target, ".ai-team", "state", "runs", "event-log-reservations")
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			if reserved, err := store.IsReserved(runID); err == nil || reserved {
				t.Fatalf("partial controller authority fell back: reserved=%v err=%v", reserved, err)
			}
			if err := store.Reserve(runID); err == nil {
				t.Fatal("Reserve recreated a controller event root after durable authority proof survived")
			}
		})
	}

	target := t.TempDir()
	runID := "genuine-local-event-authority"
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := Start(filepath.Join(target, ".ai-team", "runs"), RunManifest{
		RunID: runID, StartedAt: time.Now().UTC(), ConfigSnapshot: []byte(`{}`), WorkflowSnapshot: []byte(`{"stages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := (ControllerEventStore{TargetDir: target}).IsReserved(runID)
	if err != nil || reserved {
		t.Fatalf("local run with no controller authority: reserved=%v err=%v", reserved, err)
	}
	if _, err := VerifyEventLog(filepath.Join(store.RunDir(), "events.jsonl"), runID); err != nil {
		t.Fatalf("local event journal should remain readable: %v", err)
	}
}

func TestControllerEventStorePartialRootsWithOtherCloudMarkerFailClosedAndAllowExplicitMigration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		removeRoot string
		markCloud  func(string, string) error
	}{
		{
			name:       "event root missing with usage marker",
			removeRoot: "events",
			markCloud: func(target, runID string) error {
				return (metrics.FileUsageEnvelopeStore{}).Reserve(target, runID)
			},
		},
		{
			name:       "reservation root missing with attempt marker",
			removeRoot: "reservations",
			markCloud: func(target, runID string) error {
				return (ControllerAttemptManifestStore{TargetDir: target}).Reserve(runID)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			runID := "partial-legacy-cloud-run"
			if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
				t.Fatal(err)
			}
			legacy, err := Start(filepath.Join(target, ".ai-team", "runs"), RunManifest{
				RunID: runID, StartedAt: time.Now().UTC(), ConfigSnapshot: []byte(`{}`), WorkflowSnapshot: []byte(`{"stages":[]}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := legacy.Append(Event{Type: "run_started", Timestamp: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			store := ControllerEventStore{TargetDir: target}
			if err := store.Reserve("unrelated-controller-run"); err != nil {
				t.Fatal(err)
			}
			if err := tc.markCloud(target, runID); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(target, ".ai-team", "state", "events")
			if tc.removeRoot == "reservations" {
				root = filepath.Join(target, ".ai-team", "state", "runs", "event-log-reservations")
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}

			if _, err := ReadEventLogBytesForRunDir(legacy.RunDir(), runID); err == nil {
				t.Fatal("ordinary reader selected worker-visible legacy chain when a controller root was partial")
			}
			if reserved, err := store.IsReserved(runID); err == nil || reserved {
				t.Fatalf("partial root plus independent cloud marker fell back: reserved=%v err=%v", reserved, err)
			}

			if err := store.MigrateLegacyWithValidator(runID, legacy.RunDir(), func(events []Event) error {
				if len(events) != 1 || events[0].Type != "run_started" {
					return errors.New("unexpected legacy event snapshot")
				}
				return nil
			}); err != nil {
				t.Fatalf("explicit validated legacy migration failed: %v", err)
			}
			if events, err := store.Read(runID); err != nil || len(events) != 1 || events[0].Type != "run_started" {
				t.Fatalf("migrated canonical events=%+v err=%v", events, err)
			}
		})
	}
}

func TestControllerEventStoreMissingRootsFailClosedWhenOtherCloudMarkerSurvives(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reserve func(string, string) error
	}{
		{
			name: "usage reservation",
			reserve: func(target, runID string) error {
				return (metrics.FileUsageEnvelopeStore{}).Reserve(target, runID)
			},
		},
		{
			name: "attempt manifest reservation",
			reserve: func(target, runID string) error {
				return (ControllerAttemptManifestStore{TargetDir: target}).Reserve(runID)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			runID := "event-roots-missing-cloud-run"
			runsRoot := filepath.Join(target, ".ai-team", "runs")
			if err := os.MkdirAll(filepath.Dir(runsRoot), 0700); err != nil {
				t.Fatal(err)
			}
			legacy, err := Start(runsRoot, RunManifest{
				RunID: runID, StartedAt: time.Now().UTC(), ConfigSnapshot: []byte(`{}`), WorkflowSnapshot: []byte(`{"stages":[]}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := legacy.Append(Event{Type: "run_started", Timestamp: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			controllerEvents := ControllerEventStore{TargetDir: target}
			if err := controllerEvents.Reserve(runID); err != nil {
				t.Fatal(err)
			}
			if err := tc.reserve(target, runID); err != nil {
				t.Fatalf("reserve independent cloud marker: %v", err)
			}

			for _, root := range []string{
				filepath.Join(target, ".ai-team", "state", "events"),
				filepath.Join(target, ".ai-team", "state", "runs", "event-log-reservations"),
			} {
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadEventLogBytesForRunDir(legacy.RunDir(), runID); err == nil {
				t.Fatal("forged worker-visible event chain was selected after controller event roots disappeared")
			}
			if reserved, err := controllerEvents.IsReserved(runID); err == nil || reserved {
				t.Fatalf("missing event roots with independent cloud marker: reserved=%v err=%v", reserved, err)
			}
		})
	}
}

func TestControllerEventStoreRejectsConcurrentStaleAppend(t *testing.T) {
	target := t.TempDir()
	runID := "controller-event-concurrent"
	store := ControllerEventStore{TargetDir: target}
	if err := store.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	previous := chainGenesis(runID)
	started := time.Date(2026, 10, 8, 13, 30, 0, 0, time.UTC)
	events := []Event{{Type: "run_started", Timestamp: started}, {Type: "run_resumed", Timestamp: started}}
	var wait sync.WaitGroup
	errs := make([]error, len(events))
	for i := range events {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, errs[i] = store.Append(runID, events[i], 0, previous)
		}(i)
	}
	wait.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("exactly one conflicting append must win, got errors %v", errs)
	}
}

func TestResolveEventLogSourceKeepsLegacyFileFallback(t *testing.T) {
	target := t.TempDir()
	runID := "legacy-event-source"
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0755); err != nil {
		t.Fatal(err)
	}
	runsRoot := filepath.Join(target, ".ai-team", "runs")
	store, err := Start(runsRoot, testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_started", Timestamp: time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	source, reserved, err := ResolveEventLogSource(target, store.RunDir(), runID)
	if err != nil || reserved {
		t.Fatalf("legacy source reserved=%v err=%v", reserved, err)
	}
	events, err := source.Read(runID)
	if err != nil || len(events) != 1 || events[0].Type != "run_started" {
		t.Fatalf("legacy event read events=%+v err=%v", events, err)
	}
}

func TestVerifyEventLogPreservesRelativeLegacyPaths(t *testing.T) {
	target := t.TempDir()
	runID := "relative-legacy-events"
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0755); err != nil {
		t.Fatal(err)
	}
	store, err := Start(filepath.Join(target, ".ai-team", "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_started", Timestamp: time.Date(2026, 10, 8, 13, 15, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory, err = filepath.EvalSymlinks(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRunDir, err := filepath.EvalSymlinks(store.RunDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Rel(workingDirectory, filepath.Join(canonicalRunDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := VerifyEventLog(path, runID)
	if err != nil || len(events) != 1 || events[0].Type != "run_started" {
		t.Fatalf("relative legacy event verification events=%+v err=%v", events, err)
	}
}

func TestResumeSelectsReservedControllerEventsOverWorkerMirror(t *testing.T) {
	target := t.TempDir()
	runID := "resume-controller-events"
	runsRoot := filepath.Join(target, ".ai-team", "runs")
	controllerEvents := ControllerEventStore{TargetDir: target}
	if err := controllerEvents.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	started := Event{Type: "run_started", Timestamp: time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)}
	if _, err := controllerEvents.Append(runID, started, 0, chainGenesis(runID)); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWithEventLog(runsRoot, testRunManifest(runID), controllerEvents); err != nil {
		t.Fatal(err)
	}
	workerMirrorPath := filepath.Join(runsRoot, runID, "events.jsonl")
	if err := os.WriteFile(workerMirrorPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	mirror := newFileEventLog(workerMirrorPath)
	forged, err := mirror.Append(runID, Event{Type: "run_started", Timestamp: started.Timestamp.Add(time.Hour)}, 0, chainGenesis(runID))
	if err != nil {
		t.Fatal(err)
	}
	resumed, _, _, err := ResumeWithAttemptManifestSource(runsRoot, runID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resumed.eventLog.(ControllerEventStore); !ok {
		t.Fatalf("reserved resume selected event source %T, want operation-scoped controller store", resumed.eventLog)
	}
	if resumed.lastEventHash == forged.SHA256 {
		t.Fatal("reserved resume trusted the worker-visible event mirror")
	}
	if err := resumed.Append(Event{Type: "run_resumed", Timestamp: started.Timestamp.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	canonical, err := controllerEvents.Read(runID)
	if err != nil || len(canonical) != 2 || canonical[1].Type != "run_resumed" {
		t.Fatalf("resume did not append to canonical controller events: events=%+v err=%v", canonical, err)
	}
	mirrorEvents, err := mirror.Read(runID)
	if err != nil || len(mirrorEvents) != 1 || mirrorEvents[0].SHA256 != forged.SHA256 {
		t.Fatalf("resume modified or invalidated worker mirror: events=%+v err=%v", mirrorEvents, err)
	}
}

func TestDefaultReservedEventVerificationDoesNotLeakPinnedDescriptors(t *testing.T) {
	target := t.TempDir()
	runID := "default-event-source-fd-lifetime"
	store := ControllerEventStore{TargetDir: target}
	if err := store.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(runID, Event{Type: "run_started", Timestamp: time.Now().UTC()}, 0, chainGenesis(runID)); err != nil {
		t.Fatal(err)
	}
	eventPath := filepath.Join(target, ".ai-team", "runs", runID, "events.jsonl")
	resolved, ok, err := defaultEventLogSource(eventPath, runID)
	if err != nil || !ok {
		t.Fatalf("default source resolved=%v err=%v", ok, err)
	}
	if _, ok := resolved.(ControllerEventStore); !ok {
		t.Fatalf("default resolver returned %T, want descriptor-scoped ControllerEventStore", resolved)
	}
	countFDs := func() int {
		t.Helper()
		entries, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Skipf("descriptor inventory is unavailable: %v", err)
		}
		return len(entries)
	}
	before := countFDs()
	for i := 0; i < 64; i++ {
		if _, err := VerifyEventLog(eventPath, runID); err != nil {
			t.Fatalf("verification %d: %v", i, err)
		}
	}
	after := countFDs()
	if after != before {
		t.Fatalf("reserved verification leaked descriptors: before=%d after=%d", before, after)
	}
}

func TestConcurrentLegacyEventMigrationsAreIdempotent(t *testing.T) {
	target := t.TempDir()
	runID := "concurrent-event-migration"
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	legacyStore, err := Start(filepath.Join(target, ".ai-team", "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyStore.Append(Event{Type: "run_started", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	legacyBytes, err := os.ReadFile(filepath.Join(legacyStore.RunDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store := ControllerEventStore{TargetDir: target}
	const workers = 12
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.MigrateLegacy(runID, legacyStore.RunDir())
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent legacy migration: %v", err)
		}
	}
	canonical, err := store.ReadBytes(runID)
	if err != nil || !bytes.Equal(canonical, legacyBytes) {
		t.Fatalf("concurrent migrations did not preserve exact legacy bytes: canonical=%d legacy=%d err=%v", len(canonical), len(legacyBytes), err)
	}
}

func TestLegacyMigrationValidatorBindsPolicyToCopiedBytes(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	runID := "legacy-event-validator-snapshot"
	legacy, err := Start(filepath.Join(target, ".ai-team", "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Append(Event{Type: "run_started", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacy.RunDir(), "events.jsonl")
	verifiedBytes, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	store := ControllerEventStore{TargetDir: target}
	err = store.MigrateLegacyWithValidator(runID, legacy.RunDir(), func(events []Event) error {
		if len(events) != 1 || events[0].Type != "run_started" {
			return errors.New("validator did not receive the source snapshot")
		}
		// Simulate an adversarial path replacement after validation. Migration
		// must copy the already-read snapshot without reopening the worker path.
		return os.WriteFile(legacyPath, []byte("changed after validation\n"), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalBytes, err := store.ReadBytes(runID)
	if err != nil || !bytes.Equal(canonicalBytes, verifiedBytes) {
		t.Fatalf("canonical migration bytes differ from validator snapshot: got=%d want=%d err=%v", len(canonicalBytes), len(verifiedBytes), err)
	}
}
