package evidence

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type memoryAttemptManifestSource struct {
	data map[string][]byte
}

type recordingAttemptManifestSource struct {
	calls int
}

func (s *recordingAttemptManifestSource) ReadAttemptManifest(_, _, _ string) ([]byte, error) {
	s.calls++
	return []byte(`{}`), nil
}

func TestAttemptManifestSourcesRejectDotTraversalIDs(t *testing.T) {
	const runID = "run-manifest-path-traversal"
	runDir := t.TempDir()
	for _, test := range []struct {
		attemptID string
		path      string
	}{
		{attemptID: ".", path: filepath.Join(runDir, "attempts", "manifest.json")},
		{attemptID: "..", path: filepath.Join(runDir, "manifest.json")},
	} {
		t.Run(test.attemptID, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(test.path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(test.path, []byte(`{"outside":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := FilesystemAttemptManifestSource().ReadAttemptManifest(runDir, runID, test.attemptID); err == nil {
				t.Fatalf("filesystem source accepted traversal attempt id %q", test.attemptID)
			}

			source := &recordingAttemptManifestSource{}
			if _, err := readAttemptManifestBytes(source, runDir, runID, test.attemptID); err == nil {
				t.Fatalf("custom source path accepted traversal attempt id %q", test.attemptID)
			}
			if source.calls != 0 {
				t.Fatalf("custom source was called %d times for invalid attempt id %q", source.calls, test.attemptID)
			}
		})
	}
}

func TestAttemptManifestSourceRejectsOversizedBytes(t *testing.T) {
	const runID = "run-oversized-manifest"
	const attemptID = "attempt-oversized-manifest"
	source := memoryAttemptManifestSource{data: map[string][]byte{
		runID + "/" + attemptID: bytes.Repeat([]byte("x"), maxAttemptManifestSize+1),
	}}

	if _, _, err := readAttemptManifest(source, "", runID, attemptID); err == nil || !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("oversized custom-source manifest should fail at the size limit, got %v", err)
	}
}

func TestPublishAttemptBoundsManifestsAndReplayStreamsLegacyOversizedFiles(t *testing.T) {
	target := t.TempDir()
	started := time.Now().UTC()

	tooLargeStore, err := Start(filepath.Join(target, "bounded-runs"), testRunManifest("run-bounded-manifest"))
	if err != nil {
		t.Fatal(err)
	}
	largeAttemptID := "run-bounded-manifest-001-check"
	if err := tooLargeStore.Append(Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	if err := tooLargeStore.Append(Event{Type: "attempt_started", Stage: "check", AttemptID: largeAttemptID, Timestamp: started.Add(time.Second), Data: map[string]any{"stage_index": 1}}); err != nil {
		t.Fatal(err)
	}
	mutations := make([]string, 70_000)
	for index := range mutations {
		mutations[index] = strings.Repeat("x", 128)
	}
	if err := tooLargeStore.PublishAttempt(AttemptManifest{
		AttemptID: largeAttemptID, Stage: "check", StageIndex: 1, StartedAt: started.Add(time.Second),
		FinishedAt: started.Add(2 * time.Second), Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed",
		Mutations: mutations,
	}, filepath.Join(target, "artifacts"), nil, nil); err == nil || !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("PublishAttempt should reject manifests above %d bytes, got %v", MaxAttemptManifestSize, err)
	}

	legacyStore, err := Start(filepath.Join(target, "legacy-runs"), testRunManifest("run-legacy-large-manifest"))
	if err != nil {
		t.Fatal(err)
	}
	legacyAttemptID := "run-legacy-large-manifest-001-check"
	if err := legacyStore.Append(Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	if err := legacyStore.Append(Event{Type: "attempt_started", Stage: "check", AttemptID: legacyAttemptID, Timestamp: started.Add(time.Second), Data: map[string]any{"stage_index": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := legacyStore.PublishAttempt(AttemptManifest{
		AttemptID: legacyAttemptID, Stage: "check", StageIndex: 1, StartedAt: started.Add(time.Second),
		FinishedAt: started.Add(2 * time.Second), Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed",
	}, filepath.Join(target, "artifacts"), nil, nil); err != nil {
		t.Fatal(err)
	}
	legacyManifestPath := filepath.Join(legacyStore.RunDir(), "attempts", legacyAttemptID, "manifest.json")
	legacyManifest, err := os.OpenFile(legacyManifestPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	padding := strings.Repeat(" ", MaxAttemptManifestSize+1)
	if _, err := legacyManifest.WriteString(padding); err != nil {
		_ = legacyManifest.Close()
		t.Fatal(err)
	}
	if err := legacyManifest.Close(); err != nil {
		t.Fatal(err)
	}
	_, size, digest, err := ArtifactDigest(legacyManifestPath)
	if err != nil || size <= MaxAttemptManifestSize {
		t.Fatalf("legacy manifest should exceed the read limit: size=%d err=%v", size, err)
	}
	if err := legacyStore.Append(Event{Type: "attempt_finished", Stage: "check", AttemptID: legacyAttemptID, Timestamp: started.Add(2 * time.Second), Data: map[string]any{
		"status": "passed", "execution": "succeeded", "decision": "approved", "outcome": "passed", "manifest_sha256": digest,
	}}); err != nil {
		t.Fatal(err)
	}
	eventPath := filepath.Join(legacyStore.RunDir(), "events.jsonl")
	if _, err := ReplayEventLog(eventPath, "run-legacy-large-manifest"); err != nil {
		t.Fatalf("standalone replay should preserve streaming compatibility with oversized legacy manifests: %v", err)
	}
	if _, err := ReplayEventLogWithAttemptManifestSource(eventPath, "run-legacy-large-manifest", FilesystemAttemptManifestSource()); err == nil {
		t.Fatal("source-backed replay should enforce MaxAttemptManifestSize")
	}
}

func (s memoryAttemptManifestSource) ReadAttemptManifest(_, runID, attemptID string) ([]byte, error) {
	data, ok := s.data[runID+"/"+attemptID]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func TestAttemptManifestSourceParityAndManifestWithoutFilesystemFile(t *testing.T) {
	target := t.TempDir()
	runID := "run-manifest-source"
	store, err := Start(filepath.Join(target, "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if err := store.Append(Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	attemptID := runID + "-001-check"
	if err := store.Append(Event{Type: "attempt_started", Stage: "check", AttemptID: attemptID, Timestamp: started.Add(time.Second), Data: map[string]any{"stage_index": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{
		AttemptID: attemptID, Stage: "check", StageIndex: 1, StartedAt: started.Add(time.Second),
		FinishedAt: started.Add(2 * time.Second), Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed",
	}, filepath.Join(target, "artifacts"), nil, nil); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(store.RunDir(), "attempts", attemptID, "manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	_, _, manifestDigest, err := ArtifactDigest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_finished", Stage: "check", AttemptID: attemptID, Timestamp: started.Add(2 * time.Second), Data: map[string]any{
		"status": "passed", "execution": "succeeded", "decision": "approved", "outcome": "passed", "manifest_sha256": manifestDigest,
	}}); err != nil {
		t.Fatal(err)
	}
	eventPath := filepath.Join(store.RunDir(), "events.jsonl")

	filesystemReplay, err := ReplayEventLog(eventPath, runID)
	if err != nil {
		t.Fatal(err)
	}
	filesystemSource := FilesystemAttemptManifestSource()
	filesystemReplayWithSource, err := ReplayEventLogWithAttemptManifestSource(eventPath, runID, filesystemSource)
	if err != nil || !reflect.DeepEqual(filesystemReplay, filesystemReplayWithSource) {
		t.Fatalf("filesystem source replay differs: err=%v\nplain=%+v\nsource=%+v", err, filesystemReplay, filesystemReplayWithSource)
	}
	if err := VerifyResumeEvidenceWithAttemptManifestSource(store.RunDir(), filesystemSource); err != nil {
		t.Fatalf("filesystem source verification: %v", err)
	}
	_, _, plainResume, err := Resume(filepath.Join(target, "runs"), runID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, sourceResume, err := ResumeWithAttemptManifestSource(filepath.Join(target, "runs"), runID, filesystemSource)
	if err != nil || !reflect.DeepEqual(plainResume, sourceResume) {
		t.Fatalf("filesystem source resume differs: err=%v\nplain=%+v\nsource=%+v", err, plainResume, sourceResume)
	}

	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	customSource := memoryAttemptManifestSource{data: map[string][]byte{runID + "/" + attemptID: manifestBytes}}
	customReplay, err := ReplayEventLogWithAttemptManifestSource(eventPath, runID, customSource)
	if err != nil || !reflect.DeepEqual(filesystemReplay, customReplay) {
		t.Fatalf("memory source replay differs: err=%v\nfilesystem=%+v\nmemory=%+v", err, filesystemReplay, customReplay)
	}
	if err := VerifyResumeEvidenceWithAttemptManifestSource(store.RunDir(), customSource); err != nil {
		t.Fatalf("memory source verification without manifest file: %v", err)
	}
	_, _, resumed, err := ResumeWithAttemptManifestSource(filepath.Join(target, "runs"), runID, customSource)
	if err != nil || !reflect.DeepEqual(plainResume, resumed) {
		t.Fatalf("memory source resume without manifest file differs: err=%v\nfilesystem=%+v\nmemory=%+v", err, plainResume, resumed)
	}
	if _, err := ReplayEventLog(eventPath, runID); err == nil {
		t.Fatal("default filesystem replay unexpectedly succeeded without the manifest file")
	}
}
