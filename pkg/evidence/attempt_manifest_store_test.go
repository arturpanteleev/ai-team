package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

func testAttemptManifest(runID, attemptID string) AttemptManifest {
	now := time.Now().UTC()
	return AttemptManifest{SchemaVersion: SchemaVersion, RunID: runID, AttemptID: attemptID,
		Stage: "analyst", StageIndex: 1, StartedAt: now.Add(-time.Minute), FinishedAt: now,
		Status: "completed", Execution: "success", Decision: "continue", Outcome: "success"}
}

func TestCleanupUnfinishedAttemptArtifactsValidatesIdentityAndDoesNotFollowLinks(t *testing.T) {
	root := t.TempDir()
	runID, attemptID := "orphan-cleanup-run", "orphan-cleanup-run-001-analyst"
	runDir := filepath.Join(root, "runs", runID)
	attemptDir := filepath.Join(runDir, "attempts", attemptID)
	inputDir := filepath.Join(attemptDir, "inputs", "001-clarification-answer")
	if err := os.MkdirAll(inputDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := testAttemptManifest(runID, attemptID)
	manifest.Inputs = []ArtifactRecord{{Name: "clarification-answer", EvidencePath: filepath.ToSlash(filepath.Join("attempts", attemptID, "inputs", "001-clarification-answer", "answer.md"))}}
	manifestPath := filepath.Join(attemptDir, "manifest.json")
	writeManifest := func(value AttemptManifest) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(manifest)
	answerPath := filepath.Join(inputDir, "answer.md")
	if err := os.WriteFile(answerPath, []byte("approved answer"), 0644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside-sentinel")
	if err := os.WriteFile(outside, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(inputDir, "outside-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	attempt := ReplayedAttempt{AttemptID: attemptID, Stage: manifest.Stage, StageIndex: manifest.StageIndex, StartedAt: manifest.StartedAt}
	if err := CleanupUnfinishedAttemptArtifacts(runDir, runID, attempt); err != nil {
		t.Fatalf("valid orphan attempt cleanup failed: %v", err)
	}
	if _, err := os.Lstat(attemptDir); !os.IsNotExist(err) {
		t.Fatalf("orphan attempt directory remains: err=%v", err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("cleanup followed an internal symlink: data=%q err=%v", data, err)
	}

	if err := os.MkdirAll(inputDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeManifest(manifest)
	wrongIdentity := attempt
	wrongIdentity.Stage = "coder"
	if err := CleanupUnfinishedAttemptArtifacts(runDir, runID, wrongIdentity); err == nil {
		t.Fatal("attempt/manifest identity mismatch was accepted")
	}
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("mismatched orphan must fail closed without deleting evidence: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte(`{"schema_version":7,"unexpected":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CleanupUnfinishedAttemptArtifacts(runDir, runID, attempt); err == nil {
		t.Fatal("malformed orphan manifest was accepted")
	}
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("malformed orphan must fail closed without deleting evidence: %v", err)
	}
}

func TestReservedAttemptManifestStoreReadsCanonicalAndPreservesLegacyFallback(t *testing.T) {
	target := t.TempDir()
	runID, attemptID := "manifest-source-run", "attempt-1"
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	local := filepath.Join(runDir, "attempts", attemptID, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(local), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte(`{"schema_version":7,"run_id":"manifest-source-run","attempt_id":"attempt-1","stage":"legacy"}`), 0644); err != nil {
		t.Fatal(err)
	}
	_, got, err := ReadAttemptManifest(nil, runDir, runID, attemptID)
	if err != nil || got.Stage != "legacy" {
		t.Fatalf("legacy filesystem fallback failed: manifest=%+v err=%v", got, err)
	}

	store := ControllerAttemptManifestStore{TargetDir: target}
	if err := store.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadAttemptManifest(nil, runDir, runID, attemptID); err == nil {
		t.Fatal("reserved run fell back to target-side manifest when canonical record was missing")
	}
	manifest := testAttemptManifest(runID, attemptID)
	if err := store.Write(runID, manifest); err != nil {
		t.Fatal(err)
	}
	_, got, err = ReadAttemptManifest(nil, runDir, runID, attemptID)
	if err != nil || got.Stage != manifest.Stage {
		t.Fatalf("reserved canonical manifest read failed: manifest=%+v err=%v", got, err)
	}
	reservation := filepath.Join(target, ".ai-team", "state", "attempt-manifests", runID, attemptManifestReservationName)
	if err := os.Remove(reservation); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadAttemptManifest(nil, runDir, runID, attemptID); err == nil {
		t.Fatal("missing reservation marker fell back to a target-side manifest")
	}
}

func TestControllerAttemptManifestStoreEnforcesIdentityRetryConflictAndSize(t *testing.T) {
	target := t.TempDir()
	store := ControllerAttemptManifestStore{TargetDir: target}
	runID, attemptID := "manifest-write-run", "attempt-1"
	if err := store.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	manifest := testAttemptManifest(runID, attemptID)
	if err := store.Write(runID, manifest); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(runID, manifest); err != nil {
		t.Fatalf("exact retry must be idempotent: %v", err)
	}
	conflict := manifest
	conflict.Status = "failed"
	if err := store.Write(runID, conflict); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting overwrite was accepted: %v", err)
	}
	if err := store.Write("other-run", manifest); err == nil {
		t.Fatal("cross-run write was accepted")
	}
	invalid := manifest
	invalid.AttemptID = "../escape"
	if err := store.Write(runID, invalid); err == nil {
		t.Fatal("path traversal attempt id was accepted")
	}
	large := manifest
	large.Error = strings.Repeat("x", MaxAttemptManifestSize)
	if err := store.Write(runID, large); err == nil || !strings.Contains(err.Error(), "maximum size") {
		t.Fatalf("oversized typed manifest was accepted: %v", err)
	}
}

func TestControllerAttemptManifestBindsTypedHumanSubmissionToOutputHash(t *testing.T) {
	target := t.TempDir()
	store := ControllerAttemptManifestStore{TargetDir: target}
	const runID, attemptID = "typed-human-manifest", "typed-human-manifest-001-writer"
	if err := store.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	submittedSHA := strings.Repeat("a", 64)
	manifest := testAttemptManifest(runID, attemptID)
	manifest.Stage, manifest.Executor = "writer", "human"
	manifest.ActorID, manifest.ActorRole, manifest.HumanInputApprovalID = "writer-1", "developer", "approval-writer"
	manifest.HumanSubmissionVersion, manifest.HumanSubmissionSHA256 = 1, submittedSHA
	manifest.HumanSubmissionResult, manifest.HumanSubmissionDescription = "md", "finished"
	manifest.Outputs = []ArtifactRecord{{Name: "result", Type: "file", SourcePath: "/workspace/result.md",
		EvidencePath: filepath.ToSlash(filepath.Join("attempts", attemptID, "artifacts", "result.md")),
		Size:         17, SHA256: submittedSHA}}
	if err := store.Write(runID, manifest); err != nil {
		t.Fatalf("valid typed human attempt manifest: %v", err)
	}
	conflict := manifest
	conflict.Outputs = append([]ArtifactRecord(nil), manifest.Outputs...)
	conflict.Outputs[0].SHA256 = strings.Repeat("b", 64)
	if err := store.Write(runID, conflict); err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("attempt output hash diverging from submission metadata was accepted: %v", err)
	}
}

func TestControllerAttemptManifestStoreRejectsSymlinkedRunDirectory(t *testing.T) {
	target := t.TempDir()
	store := ControllerAttemptManifestStore{TargetDir: target}
	root, err := safeio.EnsureDir(target, ".ai-team", "state", "attempt-manifests")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "manifest-symlink-run")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := store.Reserve("manifest-symlink-run"); err == nil {
		t.Fatal("reservation followed a run-directory symlink")
	}
}

func TestReservedCanonicalAttemptManifestSupportsReplayAndResumeWithoutLocalManifest(t *testing.T) {
	target := t.TempDir()
	runID, attemptID := "canonical-resume-run", "attempt-1"
	root := filepath.Join(target, ".ai-team", "runs")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := Start(root, testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if err := store.Append(Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_started", Stage: "analyst", AttemptID: attemptID, Timestamp: started.Add(time.Second), Data: map[string]any{"stage_index": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{AttemptID: attemptID, Stage: "analyst", StageIndex: 1, StartedAt: started.Add(time.Second),
		FinishedAt: started.Add(2 * time.Second), Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed"}, target, nil, nil); err != nil {
		t.Fatal(err)
	}
	digest, _, err := AttemptManifestDigest(nil, store.RunDir(), runID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_finished", Stage: "analyst", AttemptID: attemptID, Timestamp: started.Add(2 * time.Second), Data: map[string]any{
		"status": "passed", "execution": "succeeded", "decision": "approved", "outcome": "passed", "manifest_sha256": digest,
	}}); err != nil {
		t.Fatal(err)
	}
	_, manifest, err := ReadAttemptManifest(FilesystemAttemptManifestSource(), store.RunDir(), runID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	canonical := ControllerAttemptManifestStore{TargetDir: target}
	if err := canonical.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	if err := canonical.Write(runID, manifest); err != nil {
		t.Fatal(err)
	}
	controllerEvents := ControllerEventStore{TargetDir: target}
	if err := controllerEvents.MigrateLegacy(runID, store.RunDir()); err != nil {
		t.Fatalf("migrate legacy events into controller authority: %v", err)
	}
	if err := os.Remove(filepath.Join(store.RunDir(), "attempts", attemptID, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayEventLog(filepath.Join(store.RunDir(), "events.jsonl"), runID); err != nil {
		t.Fatalf("replay did not use canonical manifest: %v", err)
	}
	if err := VerifyResumeEvidence(store.RunDir()); err != nil {
		t.Fatalf("resume verification did not use canonical manifest: %v", err)
	}
	if _, _, replayed, err := Resume(root, runID); err != nil || len(replayed.Attempts) != 1 {
		t.Fatalf("resume did not use canonical manifest: attempts=%+v err=%v", replayed.Attempts, err)
	}
}
