package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRunManifest(runID string) RunManifest {
	return RunManifest{RunID: runID, ConfigSnapshot: json.RawMessage(`{"schema_version":1}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`)}
}

func TestStorePublishesImmutableAttemptWithHashes(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(target, "artifacts")
	input := filepath.Join(artifactRoot, "feature", "input.md")
	outputDir := filepath.Join(artifactRoot, "feature", "specs")
	output := filepath.Join(outputDir, "spec.md")
	for path, content := range map[string]string{input: "input-v1", output: "output-v1"} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	runManifest := testRunManifest("run-1")
	runManifest.Feature, runManifest.TargetDir, runManifest.StartedAt = "feature", target, time.Now()
	store, err := Start(filepath.Join(target, "runs"), runManifest)
	if err != nil {
		t.Fatal(err)
	}
	attempt := AttemptManifest{
		AttemptID: "001-analyst", Stage: "analyst", StageIndex: 1,
		StartedAt: time.Now(), FinishedAt: time.Now(), Status: "passed",
	}
	if err := store.PublishAttempt(attempt, artifactRoot,
		[]Artifact{{Name: "input", Path: input}},
		[]Artifact{{Name: "specs", Path: outputDir}, {Name: "spec", Path: output}},
	); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(store.RunDir(), "attempts", attempt.AttemptID, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest AttemptManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != SchemaVersion || len(manifest.Inputs) != 1 || len(manifest.Outputs) != 2 {
		t.Fatalf("неполный manifest: %+v", manifest)
	}
	if inputRecord := manifest.Inputs[0]; len(inputRecord.SHA256) != 64 || inputRecord.ConsumedByRunID != "run-1" ||
		inputRecord.ConsumedByAttemptID != attempt.AttemptID || !inputRecord.ExternalOrLegacyInput || inputRecord.EvidencePath == "" {
		t.Errorf("невалидная input provenance record: %+v", inputRecord)
	}
	for _, record := range manifest.Outputs {
		if len(record.SHA256) != 64 || record.ProducerRunID != "run-1" || record.ProducerAttemptID != attempt.AttemptID || record.ProducerStage != "analyst" {
			t.Errorf("невалидная output provenance record: %+v", record)
		}
	}

	evidenceOutput := filepath.Join(store.RunDir(), filepath.FromSlash(manifest.Outputs[1].EvidencePath))
	if err := os.WriteFile(output, []byte("output-v2"), 0644); err != nil {
		t.Fatal(err)
	}
	immutable, err := os.ReadFile(evidenceOutput)
	if err != nil {
		t.Fatal(err)
	}
	if string(immutable) != "output-v1" {
		t.Fatalf("evidence изменился вместе с live artifact: %q", immutable)
	}
	evidenceInput := filepath.Join(store.RunDir(), filepath.FromSlash(manifest.Inputs[0].EvidencePath))
	if err := os.WriteFile(input, []byte("input-v2"), 0644); err != nil {
		t.Fatal(err)
	}
	immutableInput, err := os.ReadFile(evidenceInput)
	if err != nil || string(immutableInput) != "input-v1" {
		t.Fatalf("immutable input copy: %q err=%v", immutableInput, err)
	}
}

func TestRunManifestBindsExactSnapshotsAndController(t *testing.T) {
	target := t.TempDir()
	manifest := testRunManifest("run-manifest")
	store, err := Start(filepath.Join(target, "runs"), manifest)
	if err != nil {
		t.Fatal(err)
	}

	rawManifest, err := os.ReadFile(filepath.Join(store.RunDir(), "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded RunManifest
	if err := json.Unmarshal(rawManifest, &recorded); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(store.RunDir(), recorded.ConfigEvidence))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile(filepath.Join(store.RunDir(), recorded.ResolvedWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	if recorded.SchemaVersion != SchemaVersion || recorded.ConfigSHA256 != sha256Bytes(config) ||
		recorded.ResolvedWorkflowSHA256 != sha256Bytes(workflow) {
		t.Fatalf("manifest does not bind exact snapshots: %+v", recorded)
	}
	if len(recorded.Controller.ExecutableSHA256) != 64 || recorded.Controller.GoVersion == "" ||
		recorded.Controller.GOOS == "" || recorded.Controller.GOARCH == "" {
		t.Fatalf("controller identity is incomplete: %+v", recorded.Controller)
	}
}

func TestEventLogHashChainDetectsTampering(t *testing.T) {
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-events"))
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{"run_started", "run_finished"} {
		if err := store.Append(Event{Type: eventType, Timestamp: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(store.RunDir(), "events.jsonl")
	events, err := VerifyEventLog(path, "run-events")
	if err != nil || len(events) != 2 || events[0].PreviousSHA256 != genesisEventHash ||
		events[1].PreviousSHA256 != events[0].SHA256 {
		t.Fatalf("valid event chain: events=%+v err=%v", events, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"type":"run_started"`, `"type":"run_changed"`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyEventLog(path, "run-events"); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("tampered event chain must fail, got %v", err)
	}
	if err := store.Append(Event{Type: "after_tamper"}); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("store must refuse appending to tampered log, got %v", err)
	}
}

func TestReplayEventLogReconstructsAttemptsAndVerifiesManifest(t *testing.T) {
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-replay"))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if err := store.Append(Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	attemptID := "run-replay-001-check"
	if err := store.Append(Event{Type: "attempt_started", Stage: "check", AttemptID: attemptID, Timestamp: started.Add(time.Second), Data: map[string]any{"stage_index": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{
		AttemptID: attemptID, Stage: "check", StageIndex: 1, StartedAt: started.Add(time.Second),
		FinishedAt: started.Add(2 * time.Second), Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed",
	}, filepath.Join(target, "artifacts"), nil, nil); err != nil {
		t.Fatal(err)
	}
	_, _, manifestDigest, err := ArtifactDigest(filepath.Join(store.RunDir(), "attempts", attemptID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_finished", Stage: "check", AttemptID: attemptID, Timestamp: started.Add(2 * time.Second), Data: map[string]any{
		"status": "passed", "execution": "succeeded", "decision": "approved", "outcome": "passed", "verdict": "PASS", "manifest_sha256": manifestDigest,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempts_invalidated", Timestamp: started.Add(3 * time.Second), Data: map[string]any{"attempt_ids": []string{attemptID}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_finished", Timestamp: started.Add(4 * time.Second), Data: map[string]any{"status": "completed", "stage_attempts": 1}}); err != nil {
		t.Fatal(err)
	}
	replayed, err := ReplayEventLog(filepath.Join(store.RunDir(), "events.jsonl"), "run-replay")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Status != "completed" || replayed.LastEventSHA256 == "" || len(replayed.Attempts) != 1 ||
		!replayed.Attempts[0].Superseded || replayed.Attempts[0].Status != "invalidated" || replayed.Attempts[0].ManifestSHA256 != manifestDigest {
		t.Fatalf("unexpected replay projection: %+v", replayed)
	}

	manifestPath := filepath.Join(store.RunDir(), "attempts", attemptID, "manifest.json")
	if err := os.Chmod(manifestPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayEventLog(filepath.Join(store.RunDir(), "events.jsonl"), "run-replay"); err == nil || !strings.Contains(err.Error(), "manifest identity mismatch") {
		t.Fatalf("tampered attempt manifest must invalidate replay: %v", err)
	}
}

func TestReplayEventLogRejectsImpossibleTransition(t *testing.T) {
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-invalid-replay"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_started", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_finished", Stage: "ghost", AttemptID: "ghost-1", Timestamp: time.Now().UTC(), Data: map[string]any{
		"status": "failed", "execution": "infra_failed", "decision": "not_applicable", "outcome": "failed", "error": "missing start",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayEventLog(filepath.Join(store.RunDir(), "events.jsonl"), "run-invalid-replay"); err == nil || !strings.Contains(err.Error(), "no matching active attempt") {
		t.Fatalf("impossible event transition must be rejected: %v", err)
	}
}

func TestStoreLinksCurrentRunProducerToConsumer(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(target, "artifacts")
	artifact := filepath.Join(artifactRoot, "feature", "proposal.md")
	if err := os.MkdirAll(filepath.Dir(artifact), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("proposal"), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{AttemptID: "run-1-001-analyst", Stage: "analyst"}, artifactRoot, nil,
		[]Artifact{{Name: "proposal", Path: artifact}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{AttemptID: "run-1-002-reviewer", Stage: "reviewer"}, artifactRoot,
		[]Artifact{{Name: "proposal", Path: artifact}}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.RunDir(), "attempts", "run-1-002-reviewer", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest AttemptManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	input := manifest.Inputs[0]
	if input.ProducerRunID != "run-1" || input.ProducerAttemptID != "run-1-001-analyst" || input.ProducerStage != "analyst" || input.ExternalOrLegacyInput {
		t.Fatalf("producer lineage missing: %+v", input)
	}
}

func TestStoreRejectsInputBytesThatNoLongerMatchProducer(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(target, "artifacts")
	artifact := filepath.Join(artifactRoot, "feature", "proposal.md")
	if err := os.MkdirAll(filepath.Dir(artifact), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("producer-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{AttemptID: "run-1-001-producer", Stage: "producer"}, artifactRoot, nil,
		[]Artifact{{Name: "proposal", Path: artifact}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	err = store.PublishAttempt(AttemptManifest{AttemptID: "run-1-002-consumer", Stage: "consumer"}, artifactRoot,
		[]Artifact{{Name: "proposal", Path: artifact}}, nil)
	if err == nil || !strings.Contains(err.Error(), "provenance mismatch") {
		t.Fatalf("tampered live artifact must not inherit producer identity: %v", err)
	}
}

func TestStoreRejectsSymlinkOutsideArtifactRoot(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(target, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(target, "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(artifactRoot, "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	err = store.PublishAttempt(AttemptManifest{AttemptID: "001-stage", Stage: "stage"}, artifactRoot, nil,
		[]Artifact{{Name: "link", Path: link}})
	if err == nil || !strings.Contains(err.Error(), "вне artifact root") {
		t.Fatalf("symlink outside должен быть отклонён: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(store.RunDir(), "attempts", "001-stage")); !os.IsNotExist(statErr) {
		t.Fatalf("неуспешная публикация не должна оставлять final attempt: %v", statErr)
	}
}

func TestWorkspaceLockIsExclusiveAndReleasable(t *testing.T) {
	target := t.TempDir()
	first, err := AcquireWorkspaceLock(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireWorkspaceLock(target); err == nil {
		t.Fatal("второй lock должен быть отклонён")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := AcquireWorkspaceLock(target)
	if err != nil {
		t.Fatalf("lock должен переиспользоваться после release: %v", err)
	}
	_ = third.Close()
}

func TestWorkspaceLockRejectsSymlinkFile(t *testing.T) {
	target := t.TempDir()
	initial, err := AcquireWorkspaceLock(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(target, ".ai-team", "locks", "workspace.lock")
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	victim := filepath.Join(target, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, lockPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := AcquireWorkspaceLock(target); err == nil {
		t.Fatal("lock symlink must fail closed")
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "keep" {
		t.Fatalf("victim must not be truncated: %q err=%v", data, err)
	}
}

func TestRunIDIsSortableByTimestamp(t *testing.T) {
	first, err := NewRunID(time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRunID(time.Date(2026, 7, 19, 1, 2, 4, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if first >= second {
		t.Fatalf("run IDs должны сортироваться по timestamp: %s >= %s", first, second)
	}
}
