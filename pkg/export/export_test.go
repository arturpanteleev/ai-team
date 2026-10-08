package export

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/dsse"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/provenance"
	"github.com/arturpanteleev/ai-team/pkg/retention"
)

const (
	testRunID      = "r-export-0001"
	testConfigJSON = `{"profile":"fast","cli":{"cmd":"opencode"}}`
	testWorkJSON   = `{"stages":[{"name":"coder"}]}`
	testFeature    = "feat"
)

func now() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }

// buildTerminalRun создаёт полный terminal run через evidence API + attest.Build
// и возвращает его runDir.
func buildTerminalRun(t *testing.T, runsRoot string) string {
	return buildTerminalRunAt(t, runsRoot, now())
}

func buildTerminalRunAt(t *testing.T, runsRoot string, baseTime time.Time) string {
	return buildTerminalRunAtForTarget(t, runsRoot, baseTime, "", "")
}

func buildTerminalRunAtForTarget(t *testing.T, runsRoot string, baseTime time.Time, targetDir, deliveryStatePath string) string {
	t.Helper()
	runID := testRunID
	at := func(offset time.Duration) time.Time { return baseTime.Add(offset) }
	prov := provenance.New(runID, baseTime)
	prov.Add("runtime", "", provenance.UnknownDigest())
	provJSON, err := json.Marshal(prov)
	if err != nil {
		t.Fatal(err)
	}
	store, err := evidence.Start(runsRoot, evidence.RunManifest{
		RunID: runID, Feature: testFeature, TargetDir: targetDir, StartedAt: baseTime,
		ConfigSnapshot:   json.RawMessage(testConfigJSON),
		WorkflowSnapshot: json.RawMessage(testWorkJSON),
		Provenance:       provJSON,
	})
	if err != nil {
		t.Fatalf("evidence start: %v", err)
	}
	artifactRoot, err := os.MkdirTemp("", "export-artifacts-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(artifactRoot) }()
	outputPath := filepath.Join(artifactRoot, "review.json")
	if err := os.WriteFile(outputPath, []byte(`{"review":"approved"}`), 0644); err != nil {
		t.Fatal(err)
	}
	attemptID := store.NewAttemptID("coder", 1)
	err = store.PublishAttempt(evidence.AttemptManifest{
		AttemptID: attemptID, Stage: "coder", StageIndex: 1,
		StartedAt: at(time.Second), FinishedAt: at(90 * time.Second),
		Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed", Verdict: "APPROVED",
	}, artifactRoot, nil, []evidence.Artifact{{Name: "review", Path: outputPath}})
	if err != nil {
		t.Fatalf("publish attempt: %v", err)
	}
	manifestPath := filepath.Join(store.RunDir(), "attempts", attemptID, "manifest.json")
	_, _, manifestDigest, err := evidence.ArtifactDigest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{
		Type: "run_started", Timestamp: baseTime,
	}); err != nil {
		t.Fatalf("run_started: %v", err)
	}
	if err := store.Append(evidence.Event{
		Type: "attempt_started", Stage: "coder", AttemptID: attemptID, Timestamp: at(time.Second),
		Data: map[string]any{"stage_index": 1},
	}); err != nil {
		t.Fatalf("attempt_started: %v", err)
	}
	if deliveryStatePath != "" {
		if err := store.Append(evidence.Event{
			Type: "delivery_deferred", AttemptID: attemptID, Timestamp: at(30 * time.Second),
			Data: map[string]any{"plan_hash": strings.Repeat("a", 64), "state_path": filepath.ToSlash(deliveryStatePath)},
		}); err != nil {
			t.Fatalf("delivery_deferred: %v", err)
		}
	}
	if err := store.Append(evidence.Event{
		Type: "attempt_finished", Stage: "coder", AttemptID: attemptID, Timestamp: at(90 * time.Second),
		Data: map[string]any{"status": "passed", "execution": "succeeded", "decision": "approved",
			"outcome": "passed", "verdict": "APPROVED", "manifest_sha256": manifestDigest},
	}); err != nil {
		t.Fatalf("attempt_finished: %v", err)
	}
	// The real finalizer publishes supplemental run files before appending the
	// terminal event. Keep this fixture's first anchor faithful to that order.
	if err := os.MkdirAll(filepath.Join(store.RunDir(), "logs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.RunDir(), "logs", "attempt.log"), []byte("runtime output\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store.RunDir(), "reports", testFeature), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.RunDir(), "reports", testFeature, "index.html"), []byte("<p>ok</p>\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.RunDir(), "candidate.json"), []byte(`{"candidate":"stable"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.RunDir(), "usage.json"), []byte(`{"run_id":"r-export-0001"}`), 0644); err != nil {
		t.Fatal(err)
	}
	receipt, err := json.Marshal(containment.DefaultTrustedLocalReceipt())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.RunDir(), "containment.json"), receipt, 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{
		Type: "run_finished", Timestamp: at(2 * time.Minute),
		Data: map[string]any{"status": "completed", "stage_attempts": 1},
	}); err != nil {
		t.Fatalf("run_finished: %v", err)
	}
	statement, err := attest.Build(attest.Options{
		RunDir: store.RunDir(), RunID: runID, FinishedAt: at(2 * time.Minute),
		Outcome: "completed",
		CandidateSubject: []attest.Subject{{
			Name: "candidate", Digest: map[string]string{"sha256": "aa11bb2200000000000000000000000000000000000000000000000000000000"},
		}},
	})
	if err != nil {
		t.Fatalf("attestation build: %v", err)
	}
	data, err := attest.Serialize(statement)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.RunDir(), "attestation.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.SealTerminalEvidence(); err != nil {
		t.Fatalf("seal terminal evidence: %v", err)
	}
	return store.RunDir()
}

func buildTerminalRunWithoutAttempts(t *testing.T, target string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	prov := provenance.New(testRunID, now())
	prov.Add("runtime", "", provenance.UnknownDigest())
	provJSON, err := json.Marshal(prov)
	if err != nil {
		t.Fatal(err)
	}
	store, err := evidence.Start(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: testRunID, Feature: testFeature, TargetDir: target, StartedAt: now(),
		ConfigSnapshot: json.RawMessage(testConfigJSON), WorkflowSnapshot: json.RawMessage(testWorkJSON), Provenance: provJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "run_started", Timestamp: now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "run_finished", Timestamp: now().Add(time.Minute), Data: map[string]any{"status": "completed", "stage_attempts": 0}}); err != nil {
		t.Fatal(err)
	}
	statement, err := attest.Build(attest.Options{
		RunDir: store.RunDir(), RunID: testRunID, FinishedAt: now().Add(time.Minute), Outcome: "completed",
		CandidateSubject: []attest.Subject{{Name: "candidate", Digest: map[string]string{"sha256": "aa11bb2200000000000000000000000000000000000000000000000000000000"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := attest.Serialize(statement)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.RunDir(), "attestation.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.SealTerminalEvidence(); err != nil {
		t.Fatalf("seal terminal evidence: %v", err)
	}
	return store.RunDir()
}

func TestBundleBuildVerifyAndDeterminism(t *testing.T) {
	base := t.TempDir()
	runsRoot := filepath.Join(base, "runs")
	runDir := buildTerminalRun(t, runsRoot)

	bundleA := filepath.Join(base, "bundleA")
	bundleB := filepath.Join(base, "bundleB")
	indexA, err := Build(runDir, bundleA)
	if err != nil {
		t.Fatalf("build A: %v", err)
	}
	indexB, err := Build(runDir, bundleB)
	if err != nil {
		t.Fatalf("build B: %v", err)
	}
	if err := VerifyBundle(bundleA); err != nil {
		t.Fatalf("VerifyBundle(A): %v", err)
	}
	if err := VerifyBundle(bundleB); err != nil {
		t.Fatalf("VerifyBundle(B): %v", err)
	}
	digestA, err := BundleDigest(indexA)
	if err != nil {
		t.Fatalf("BundleDigest(A): %v", err)
	}
	digestB, err := BundleDigest(indexB)
	if err != nil {
		t.Fatalf("BundleDigest(B): %v", err)
	}
	if digestA != digestB {
		t.Fatal("BundleDigest должен быть детерминированным для одинакового evidence")
	}
	if digestA == "" {
		t.Fatal("пустой BundleDigest")
	}
	// BundleDigest обязан равняться sha256 файла index.json на диске (контракт,
	// чтобы внешний проверяющий воспроизвёл bundle_sha256 без Go).
	diskData, readErr := os.ReadFile(filepath.Join(bundleA, indexFileName))
	if readErr != nil {
		t.Fatal(readErr)
	}
	sum := sha256.Sum256(diskData)
	if digestA != hex.EncodeToString(sum[:]) {
		t.Fatal("BundleDigest != sha256(файла index.json)")
	}
	if indexA.RunID != testRunID || indexA.SchemaVersion != BundleSchema || indexA.Type != BundleType {
		t.Fatalf("index identity: %+v", indexA)
	}
	kinds := map[string]int{}
	attemptRecords := 0
	for _, record := range indexA.Records {
		kinds[record.Type]++
		if record.Type == RecordAttemptManifest {
			attemptRecords++
		}
	}
	if kinds[RecordAttemptManifest] != 1 || kinds[RecordAttestation] != 1 || kinds[RecordEventLog] != 1 {
		t.Fatalf("records неполны: %+v", kinds)
	}
	if _, err := os.Stat(filepath.Join(bundleA, indexFileName)); err != nil {
		t.Fatalf("index.json отсутствует: %v", err)
	}
}

func TestVerifyBundleReplaysDeliveryDeferredAgainstRecordedTarget(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(target, ".ai-team", "delivery", "prepared.json")
	runDir := buildTerminalRunAtForTarget(t, filepath.Join(target, ".ai-team", "runs"), now(), target, statePath)
	bundleDir := filepath.Join(t.TempDir(), "moved-bundle")
	if _, err := Build(runDir, bundleDir); err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	if err := VerifyBundle(bundleDir); err != nil {
		t.Fatalf("VerifyBundle should validate historical delivery path from manifest without opening it: %v", err)
	}
}

func TestVerifyBundleReplaysDeliveryDeferredFromSameRunCandidateWorktree(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(target, ".ai-team", "worktrees", testRunID, ".ai-team", "delivery", testFeature+".json")
	runDir := buildTerminalRunAtForTarget(t, filepath.Join(target, ".ai-team", "runs"), now(), target, statePath)
	bundleDir := filepath.Join(t.TempDir(), "moved-candidate-bundle")
	if _, err := Build(runDir, bundleDir); err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	if err := VerifyBundle(bundleDir); err != nil {
		t.Fatalf("VerifyBundle should validate the same run's candidate-worktree delivery path lexically without opening it: %v", err)
	}
	if _, err := os.Lstat(statePath); !os.IsNotExist(err) {
		t.Fatalf("fixture must leave the event-referenced path absent to prove replay does not open it, err=%v", err)
	}
}

func TestVerifyBundleUsesBundleLocalAnchorSourcesAtRunShapedPath(t *testing.T) {
	target := t.TempDir()
	attemptID := testRunID + "-001-coder"
	localRun := buildTerminalRunAt(t, filepath.Join(t.TempDir(), "runs"), now())
	bundleDir := filepath.Join(target, ".ai-team", "runs", testRunID)
	if _, err := Build(localRun, bundleDir); err != nil {
		t.Fatalf("build bundle: %v", err)
	}

	externalRun := buildTerminalRunAt(t, filepath.Join(t.TempDir(), "runs"), now().Add(time.Hour))
	localEvents, err := os.ReadFile(filepath.Join(bundleDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	externalEvents, err := os.ReadFile(filepath.Join(externalRun, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(localEvents, externalEvents) {
		t.Fatal("fixture requires distinct bundle and controller event chains")
	}
	localManifest, err := os.ReadFile(filepath.Join(bundleDir, "attempts", attemptID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	externalManifest, err := os.ReadFile(filepath.Join(externalRun, "attempts", attemptID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(localManifest, externalManifest) {
		t.Fatal("fixture requires distinct bundle and controller attempt manifests")
	}

	// Establish both live controller authorities for the same run identity.
	controllerEvents := evidence.ControllerEventStore{TargetDir: target}
	if err := controllerEvents.Reserve(testRunID); err != nil {
		t.Fatal(err)
	}
	controllerManifests := evidence.ControllerAttemptManifestStore{TargetDir: target}
	if err := controllerManifests.Reserve(testRunID); err != nil {
		t.Fatal(err)
	}
	_, externalAttempt, err := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(), externalRun, testRunID, attemptID)
	if err != nil {
		t.Fatalf("read external fixture attempt manifest: %v", err)
	}
	if err := controllerManifests.Write(testRunID, externalAttempt); err != nil {
		t.Fatalf("write external controller attempt manifest: %v", err)
	}
	parsedExternalEvents, err := evidence.VerifyEventLogBytes(externalEvents, testRunID)
	if err != nil {
		t.Fatalf("verify external fixture chain: %v", err)
	}
	previous := parsedExternalEvents[0].PreviousSHA256
	for i, event := range parsedExternalEvents {
		stored, appendErr := controllerEvents.Append(testRunID, event, uint64(i), previous)
		if appendErr != nil {
			t.Fatalf("append external controller event %d: %v", i, appendErr)
		}
		previous = stored.SHA256
	}
	canonicalManifest, _, err := evidence.ReadAttemptManifest(controllerManifests, bundleDir, testRunID, externalAttempt.AttemptID)
	if err != nil || !bytes.Equal(canonicalManifest, externalManifest) {
		t.Fatalf("controller manifest bytes differ from external fixture: err=%v", err)
	}

	// An event-only override still auto-resolves the live manifest reservation
	// at this path and therefore rejects the bundle-local event/manifest pair.
	bundleEventSource := evidence.NewFileEventLog(filepath.Join(bundleDir, "events.jsonl"))
	if err := evidence.VerifyAnchorWithEventSource(bundleDir, bundleEventSource); err == nil {
		t.Fatal("event-only anchor verification unexpectedly selected bundle-local manifests")
	}
	if err := evidence.VerifyBundleAnchor(bundleDir, bundleEventSource, evidence.FilesystemAttemptManifestSource(), ""); err != nil {
		t.Fatalf("portable anchor should validate bundle-local authorities: %v", err)
	}
	if err := VerifyBundle(bundleDir); err != nil {
		t.Fatalf("VerifyBundle should use local event and manifest bytes despite live reservations: %v", err)
	}

	// Replace only the anchor with one belonging to the live controller chain.
	// Its index record is updated, while local events, manifests and attestation
	// remain internally consistent with each other. No signature is required.
	externalAnchor, err := os.ReadFile(filepath.Join(externalRun, "anchor.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(bundleDir, "anchor.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "anchor.json"), externalAnchor, 0644); err != nil {
		t.Fatal(err)
	}
	indexData, err := os.ReadFile(filepath.Join(bundleDir, indexFileName))
	if err != nil {
		t.Fatal(err)
	}
	var index Index
	if err := json.Unmarshal(indexData, &index); err != nil {
		t.Fatal(err)
	}
	foundAnchor := false
	for i := range index.Records {
		if index.Records[i].Path == "anchor.json" {
			index.Records[i].SHA256 = sha256Bytes(externalAnchor)
			foundAnchor = true
		}
	}
	if !foundAnchor {
		t.Fatal("bundle index omitted anchor record")
	}
	indexData, err = indexBytes(&index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(bundleDir, indexFileName), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, indexFileName), indexData, 0644); err != nil {
		t.Fatal(err)
	}
	if err := evidence.VerifyAnchor(bundleDir); err == nil {
		t.Fatal("live verification must reject a bundle-shaped directory with omitted supplemental evidence")
	}
	if err := VerifyBundle(bundleDir); err == nil {
		t.Fatal("VerifyBundle accepted an anchor from live controller sources instead of bundle-local evidence")
	}
}

func TestVerifyBundleAttemptArtifactsStayBundleLocalAtRunShapedPath(t *testing.T) {
	target := t.TempDir()
	localRun := buildTerminalRunAt(t, filepath.Join(t.TempDir(), "runs"), now())
	bundleDir := filepath.Join(target, ".ai-team", "runs", testRunID)
	if _, err := Build(localRun, bundleDir); err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	attemptID := testRunID + "-001-coder"
	controllerManifests := evidence.ControllerAttemptManifestStore{TargetDir: target}
	if err := controllerManifests.Reserve(testRunID); err != nil {
		t.Fatal(err)
	}
	if err := controllerManifests.Write(testRunID, evidence.AttemptManifest{
		SchemaVersion: evidence.SchemaVersion, RunID: testRunID, AttemptID: attemptID,
		Stage: "coder", StageIndex: 1, StartedAt: now().Add(time.Second), FinishedAt: now().Add(2 * time.Second),
		Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed",
	}); err != nil {
		t.Fatalf("write reserved empty controller manifest: %v", err)
	}

	indexPath := filepath.Join(bundleDir, indexFileName)
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var index Index
	if err := json.Unmarshal(indexData, &index); err != nil {
		t.Fatal(err)
	}
	var removedArtifact string
	filtered := index.Records[:0]
	for _, record := range index.Records {
		if record.Type == RecordArtifact {
			removedArtifact = record.Path
			continue
		}
		filtered = append(filtered, record)
	}
	if removedArtifact == "" {
		t.Fatal("fixture did not contain an attempt artifact")
	}
	index.Records = filtered
	if err := os.RemoveAll(filepath.Join(bundleDir, filepath.FromSlash(removedArtifact))); err != nil {
		t.Fatal(err)
	}
	indexData, err = indexBytes(&index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(indexPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, indexData, 0644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(bundleDir); err == nil {
		t.Fatal("VerifyBundle accepted a bundle after its manifest-referenced artifact was removed")
	}
}

func TestVerifyBundleRejectsAliasedRecordPaths(t *testing.T) {
	base := t.TempDir()
	runDir := buildTerminalRun(t, filepath.Join(base, "runs"))
	bundleDir := filepath.Join(base, "bundle")
	if _, err := Build(runDir, bundleDir); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(bundleDir, indexFileName)
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	index.Records = append(index.Records, Record{Type: RecordRunManifest, Path: "./run.json", SHA256: index.Records[0].SHA256})
	data, err = indexBytes(&index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(indexPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(bundleDir); err == nil {
		t.Fatal("VerifyBundle accepted a noncanonical alias path for run.json")
	}
}

func TestBuildExportsControllerStoredAttestation(t *testing.T) {
	base := t.TempDir()
	runsRoot := filepath.Join(base, ".ai-team", "runs")
	if err := os.MkdirAll(runsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	runDir := buildTerminalRun(t, runsRoot)
	legacyPath := filepath.Join(runDir, "attestation.json")
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := (attest.ControllerStore{TargetDir: base}).Write(testRunID, data); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(legacyPath); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(base, "bundle")
	if _, err := Build(runDir, bundle); err != nil {
		t.Fatalf("build from controller attestation: %v", err)
	}
	exported, err := os.ReadFile(filepath.Join(bundle, "attestation.json"))
	if err != nil {
		t.Fatal(err)
	}
	statement, err := attest.Parse(exported)
	if err != nil || statement.Predicate.RunID != testRunID {
		t.Fatalf("exported attestation invalid: run=%v err=%v", statement, err)
	}
}

func TestVerifyEvidenceLiveRun(t *testing.T) {
	base := t.TempDir()
	runDir := buildTerminalRun(t, filepath.Join(base, "runs"))
	if err := VerifyEvidence(runDir); err != nil {
		t.Fatalf("VerifyEvidence live run: %v", err)
	}
}

func TestArtifactParentSymlinkRejectedByLiveVerifyAndBuild(t *testing.T) {
	runDir := buildTerminalRun(t, filepath.Join(t.TempDir(), "runs"))
	attemptArtifacts := filepath.Join(runDir, "attempts", testRunID+"-001-coder", "artifacts")
	outside := filepath.Join(t.TempDir(), "artifacts")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "review.json"), []byte(`{"review":"approved"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(attemptArtifacts); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, attemptArtifacts); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidence(runDir); err == nil {
		t.Fatal("VerifyEvidence accepted an attempt artifact below a symlink parent")
	}
	if _, err := Build(runDir, filepath.Join(t.TempDir(), "bundle")); err == nil {
		t.Fatal("Build accepted an attempt artifact below a symlink parent")
	}
}

func TestVerifyBundleRejectsArtifactSymlinkParent(t *testing.T) {
	runDir := buildTerminalRun(t, filepath.Join(t.TempDir(), "runs"))
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	if _, err := Build(runDir, bundleDir); err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	attemptArtifacts := filepath.Join(bundleDir, "attempts", testRunID+"-001-coder", "artifacts")
	outside := filepath.Join(t.TempDir(), "artifacts")
	if err := os.Rename(attemptArtifacts, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, attemptArtifacts); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(bundleDir); err == nil {
		t.Fatal("VerifyBundle accepted an artifact below a symlink parent")
	}
}

func TestVerifyEvidenceAndExportRejectTamperedRunContents(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(string) error
	}{
		{"attempt artifact content and size", func(runDir string) error {
			return os.WriteFile(filepath.Join(runDir, "attempts", "r-export-0001-001-coder", "artifacts", "review.json"), []byte(`{"review":"changed and longer"}`), 0644)
		}},
		{"attempt artifacts removed", func(runDir string) error {
			return os.RemoveAll(filepath.Join(runDir, "attempts", "r-export-0001-001-coder", "artifacts"))
		}},
		{"logs changed", func(runDir string) error {
			return os.WriteFile(filepath.Join(runDir, "logs", "attempt.log"), []byte("changed\n"), 0644)
		}},
		{"candidate changed", func(runDir string) error {
			return os.WriteFile(filepath.Join(runDir, "candidate.json"), []byte(`{"candidate":"forged"}`), 0644)
		}},
		{"usage changed", func(runDir string) error {
			return os.WriteFile(filepath.Join(runDir, "usage.json"), []byte(`{"run_id":"forged"}`), 0644)
		}},
		{"reports changed", func(runDir string) error {
			return os.WriteFile(filepath.Join(runDir, "reports", testFeature, "index.html"), []byte("<p>forged</p>\n"), 0644)
		}},
		{"containment changed", func(runDir string) error {
			return os.WriteFile(filepath.Join(runDir, "containment.json"), []byte(`{"axes":{},"profile":"strict"}`), 0644)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runDir := buildTerminalRun(t, filepath.Join(t.TempDir(), "runs"))
			if err := tc.mutate(runDir); err != nil {
				t.Fatal(err)
			}
			if err := VerifyEvidence(runDir); err == nil {
				t.Fatal("VerifyEvidence accepted modified run evidence")
			}
			if _, err := Build(runDir, filepath.Join(t.TempDir(), "bundle")); err == nil {
				t.Fatal("export accepted modified run evidence")
			}
		})
	}
}

func TestVerifyEvidenceAndExportBindControllerAndProvenanceFields(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any) error
	}{
		{"controller.executable_sha256", func(run map[string]any) error {
			controller, ok := run["controller"].(map[string]any)
			if !ok {
				return fmt.Errorf("missing controller identity")
			}
			controller["executable_sha256"] = strings.Repeat("0", 64)
			return nil
		}},
		{"provenance.items[0].digest.value", func(run map[string]any) error {
			provenance, ok := run["provenance"].(map[string]any)
			if !ok {
				return fmt.Errorf("missing provenance")
			}
			items, ok := provenance["items"].([]any)
			if !ok || len(items) == 0 {
				return fmt.Errorf("missing provenance items")
			}
			item, ok := items[0].(map[string]any)
			if !ok {
				return fmt.Errorf("invalid provenance item")
			}
			digest, ok := item["digest"].(map[string]any)
			if !ok {
				return fmt.Errorf("missing provenance digest")
			}
			digest["value"] = strings.Repeat("0", 64)
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runDir := buildTerminalRun(t, filepath.Join(t.TempDir(), "runs"))
			path := filepath.Join(runDir, "run.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			if err := tc.edit(manifest); err != nil {
				t.Fatal(err)
			}
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}
			if err := VerifyEvidence(runDir); err == nil {
				t.Fatal("verify accepted altered run.json identity")
			}
			if _, err := Build(runDir, filepath.Join(t.TempDir(), "bundle")); err == nil {
				t.Fatal("export accepted altered run.json identity")
			}
		})
	}
}

func TestVerifyEvidenceAndExportRejectTamperedDeliveryFields(t *testing.T) {
	for _, field := range []string{"commit_sha", "pr_url"} {
		t.Run(field, func(t *testing.T) {
			target := t.TempDir()
			if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
				t.Fatal(err)
			}
			runDir := buildTerminalRunAtForTarget(t, filepath.Join(target, ".ai-team", "runs"), now(), target,
				filepath.Join(target, ".ai-team", "delivery", "prepared.json"))
			if err := delivery.WriteTerminalRecord(runDir, delivery.TerminalRecord{
				SchemaVersion: delivery.TerminalRecordSchemaVersion,
				RunID:         testRunID, Feature: testFeature, PlanHash: strings.Repeat("a", 64),
				CommitSHA: strings.Repeat("b", 40), PRURL: "https://example.test/pull/1", PerformedAt: now().Add(3 * time.Minute),
			}); err != nil {
				t.Fatalf("write delivery record: %v", err)
			}
			if err := evidence.ResealTerminalEvidence(runDir, nil); err != nil {
				t.Fatalf("reseal terminal evidence: %v", err)
			}
			path := filepath.Join(runDir, "delivery.json")
			rewriteDeliveryRecord(t, path, func(record *delivery.TerminalRecord) {
				switch field {
				case "commit_sha":
					record.CommitSHA = strings.Repeat("c", 40)
				case "pr_url":
					record.PRURL = "https://example.test/pull/2"
				}
			})
			if _, _, err := delivery.ReadTerminalRecord(runDir); err != nil {
				t.Fatalf("regression setup must preserve a valid self-digest: %v", err)
			}
			if err := VerifyEvidence(runDir); err == nil {
				t.Fatalf("verify accepted delivery.json with changed %s and recomputed record_sha256", field)
			}
			if _, err := Build(runDir, filepath.Join(t.TempDir(), "bundle")); err == nil {
				t.Fatalf("export accepted delivery.json with changed %s and recomputed record_sha256", field)
			}
		})
	}
}

func TestResealRejectsTamperingSinceTerminalAnchor(t *testing.T) {
	for _, mutation := range []string{"run.json", "supplemental", "removed-anchor"} {
		t.Run(mutation, func(t *testing.T) {
			base := t.TempDir()
			runDir := buildTerminalRun(t, filepath.Join(base, "runs"))
			anchorPath := filepath.Join(runDir, "anchor.json")
			oldAnchor, err := os.ReadFile(anchorPath)
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "run.json" {
				path := filepath.Join(runDir, "run.json")
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(data, ' '), 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				path := filepath.Join(runDir, "logs", "attempt.log")
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("changed after terminal seal\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if mutation == "removed-anchor" {
					if err := os.Remove(anchorPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := delivery.WriteTerminalRecord(runDir, delivery.TerminalRecord{
				SchemaVersion: delivery.TerminalRecordSchemaVersion,
				RunID:         testRunID, Feature: testFeature, PlanHash: strings.Repeat("a", 64),
				CommitSHA: strings.Repeat("b", 40), PerformedAt: now().Add(3 * time.Minute),
			}); err != nil {
				t.Fatalf("write post-terminal delivery record: %v", err)
			}
			if err := evidence.ResealTerminalEvidence(runDir, nil); err == nil {
				t.Fatal("reseal accepted altered evidence")
			}
			newAnchor, err := os.ReadFile(anchorPath)
			if mutation == "removed-anchor" {
				if !os.IsNotExist(err) {
					t.Fatalf("failed reseal recreated a removed anchor: err=%v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(oldAnchor, newAnchor) {
					t.Fatal("failed reseal replaced the previous anchor")
				}
			}
			if err := VerifyEvidence(runDir); err == nil {
				t.Fatal("live verification accepted altered evidence with an unsealed delivery")
			}
			if _, err := Build(runDir, filepath.Join(base, "bundle")); err == nil {
				t.Fatal("standalone delivery export accepted altered evidence")
			}
		})
	}
}

func TestAttestationClaimsAreBoundByTerminalAnchor(t *testing.T) {
	for _, field := range []string{"outcome", "timestamps", "subject"} {
		t.Run(field, func(t *testing.T) {
			liveDir := buildTerminalRun(t, filepath.Join(t.TempDir(), "runs"))
			mutateAttestation(t, filepath.Join(liveDir, "attestation.json"), field)
			if err := VerifyEvidence(liveDir); err == nil {
				t.Fatalf("VerifyEvidence accepted mutated attestation %s", field)
			}

			bundleBase := t.TempDir()
			bundleRun := buildTerminalRun(t, filepath.Join(bundleBase, "runs"))
			bundleDir := filepath.Join(bundleBase, "bundle")
			if _, err := Build(bundleRun, bundleDir); err != nil {
				t.Fatalf("build bundle: %v", err)
			}
			mutateAttestation(t, filepath.Join(bundleDir, "attestation.json"), field)
			if err := refreshBundleRecordDigest(bundleDir, "attestation.json"); err != nil {
				t.Fatal(err)
			}
			if err := VerifyBundle(bundleDir); err == nil {
				t.Fatalf("VerifyBundle accepted mutated attestation %s with refreshed index digest", field)
			}
		})
	}
}

func TestVerifyBundleRejectsDeliveryTamperingWithRecomputedSelfDigest(t *testing.T) {
	for _, field := range []string{"commit_sha", "pr_url"} {
		t.Run(field, func(t *testing.T) {
			target := t.TempDir()
			if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
				t.Fatal(err)
			}
			runDir := buildTerminalRunAtForTarget(t, filepath.Join(target, ".ai-team", "runs"), now(), target,
				filepath.Join(target, ".ai-team", "delivery", "prepared.json"))
			if err := delivery.WriteTerminalRecord(runDir, delivery.TerminalRecord{
				SchemaVersion: delivery.TerminalRecordSchemaVersion,
				RunID:         testRunID, Feature: testFeature, PlanHash: strings.Repeat("a", 64),
				CommitSHA: strings.Repeat("b", 40), PRURL: "https://example.test/pull/1", PerformedAt: now().Add(3 * time.Minute),
			}); err != nil {
				t.Fatalf("write delivery record: %v", err)
			}
			if err := evidence.ResealTerminalEvidence(runDir, nil); err != nil {
				t.Fatalf("reseal terminal evidence: %v", err)
			}
			bundle := filepath.Join(target, "bundle")
			if _, err := Build(runDir, bundle); err != nil {
				t.Fatalf("build bundle: %v", err)
			}
			path := filepath.Join(bundle, "delivery.json")
			rewriteDeliveryRecord(t, path, func(record *delivery.TerminalRecord) {
				if field == "commit_sha" {
					record.CommitSHA = strings.Repeat("c", 40)
				} else {
					record.PRURL = "https://example.test/pull/2"
				}
			})
			if err := refreshBundleRecordDigest(bundle, "delivery.json"); err != nil {
				t.Fatal(err)
			}
			if err := VerifyBundle(bundle); err == nil {
				t.Fatalf("VerifyBundle accepted changed %s with recomputed record and index digests", field)
			}
		})
	}
}

func TestVerifyEvidenceRejectsConflictingControllerAndRunLocalDelivery(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	runDir := buildTerminalRunAtForTarget(t, filepath.Join(target, ".ai-team", "runs"), now(), target,
		filepath.Join(target, ".ai-team", "delivery", "prepared.json"))
	controllerRecord := delivery.TerminalRecord{
		SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: testRunID, Feature: testFeature,
		PlanHash: strings.Repeat("a", 64), CommitSHA: strings.Repeat("b", 40), PerformedAt: now().Add(3 * time.Minute),
	}
	if err := delivery.WriteControllerTerminalRecord(target, testRunID, controllerRecord); err != nil {
		t.Fatal(err)
	}
	runRecord := controllerRecord
	runRecord.CommitSHA = strings.Repeat("c", 40)
	if err := delivery.WriteTerminalRecord(runDir, runRecord); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidence(runDir); err == nil {
		t.Fatal("VerifyEvidence ignored conflicting run-local delivery.json when controller state existed")
	}
	if _, err := Build(runDir, filepath.Join(t.TempDir(), "bundle")); err == nil {
		t.Fatal("Build ignored conflicting run-local delivery.json when controller state existed")
	}
}

func TestAttestationControllerAndProvenanceMustMatchRunManifest(t *testing.T) {
	for _, field := range []string{"controller_executable_sha256", "provenance"} {
		t.Run(field, func(t *testing.T) {
			target := t.TempDir()
			runDir := buildTerminalRunWithoutAttempts(t, target)
			mutateAttestation(t, filepath.Join(runDir, "attestation.json"), field)
			if err := VerifyEvidence(runDir); err == nil {
				t.Fatalf("VerifyEvidence accepted attestation mutation of %s", field)
			}
			if _, err := Build(runDir, filepath.Join(target, "bundle")); err == nil {
				t.Fatalf("Build accepted attestation mutation of %s", field)
			}
		})
	}
}

func TestVerifyBundleAttestationControllerAndProvenanceMustMatchRunManifest(t *testing.T) {
	for _, field := range []string{"controller_executable_sha256", "provenance"} {
		t.Run(field, func(t *testing.T) {
			target := t.TempDir()
			runDir := buildTerminalRunWithoutAttempts(t, target)
			bundle := filepath.Join(target, "bundle")
			if _, err := Build(runDir, bundle); err != nil {
				t.Fatalf("build bundle: %v", err)
			}
			mutateAttestation(t, filepath.Join(bundle, "attestation.json"), field)
			if err := refreshBundleRecordDigest(bundle, "attestation.json"); err != nil {
				t.Fatal(err)
			}
			if err := VerifyBundle(bundle); err == nil {
				t.Fatalf("VerifyBundle accepted attestation mutation of %s with refreshed index digest", field)
			}
		})
	}
}

func rewriteDeliveryRecord(t *testing.T, path string, mutate func(*delivery.TerminalRecord)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record delivery.TerminalRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	mutate(&record)
	record.RecordSHA256 = ""
	canonical, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	record.RecordSHA256 = hex.EncodeToString(sum[:])
	data, err = json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}

func mutateAttestation(t *testing.T, path, field string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := attest.Parse(data)
	if err != nil {
		t.Fatalf("parse original attestation: %v", err)
	}
	switch field {
	case "controller_executable_sha256":
		statement.Predicate.Run.ControllerExecutableSHA = strings.Repeat("d", 64)
	case "outcome":
		statement.Predicate.Outcome = "failed"
	case "timestamps":
		statement.Predicate.StartedAt = statement.Predicate.StartedAt.Add(time.Second)
	case "subject":
		if len(statement.Subject) == 0 {
			t.Fatal("attestation fixture has no subject")
		}
		statement.Subject[0].Name = "other-candidate"
	case "provenance":
		if statement.Predicate.Provenance == nil || len(statement.Predicate.Provenance.Items) == 0 {
			t.Fatal("attestation fixture has no provenance item")
		}
		statement.Predicate.Provenance.Items[0].Digest.Value = "mutated-but-valid"
	default:
		t.Fatalf("unsupported attestation mutation %q", field)
	}
	data, err = attest.Serialize(statement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attest.Parse(data); err != nil {
		t.Fatalf("mutated attestation must remain valid JSON: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func refreshBundleRecordDigest(bundleDir, relPath string) error {
	indexData, err := os.ReadFile(filepath.Join(bundleDir, indexFileName))
	if err != nil {
		return err
	}
	var index Index
	if err := json.Unmarshal(indexData, &index); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(bundleDir, relPath))
	if err != nil {
		return err
	}
	for i := range index.Records {
		if index.Records[i].Path == relPath {
			index.Records[i].SHA256 = sha256Bytes(data)
			serialized, marshalErr := indexBytes(&index)
			if marshalErr != nil {
				return marshalErr
			}
			if chmodErr := os.Chmod(filepath.Join(bundleDir, indexFileName), 0644); chmodErr != nil {
				return chmodErr
			}
			return os.WriteFile(filepath.Join(bundleDir, indexFileName), serialized, 0644)
		}
	}
	return fmt.Errorf("bundle index has no record %s", relPath)
}

func TestVerifyEvidenceUsesControllerEventAndAttestationStores(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	runDir := buildTerminalRunWithoutAttempts(t, target)
	eventStore := evidence.ControllerEventStore{TargetDir: target}
	if err := eventStore.MigrateLegacy(testRunID, runDir); err != nil {
		t.Fatal(err)
	}
	attestationBytes, err := os.ReadFile(filepath.Join(runDir, "attestation.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := (attest.ControllerStore{TargetDir: target}).Write(testRunID, attestationBytes); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(runDir, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(runDir, "attestation.json")); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidence(runDir); err != nil {
		t.Fatalf("VerifyEvidence should read controller-owned event and attestation data: %v", err)
	}
}

func TestVerifyBundleReadsBundleAttestationEvenWhenControllerCopyExists(t *testing.T) {
	target := t.TempDir()
	runDir := buildTerminalRunWithoutAttempts(t, target)
	eventStore := evidence.ControllerEventStore{TargetDir: target}
	if err := eventStore.MigrateLegacy(testRunID, runDir); err != nil {
		t.Fatal(err)
	}
	attestationBytes, err := os.ReadFile(filepath.Join(runDir, "attestation.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := (attest.ControllerStore{TargetDir: target}).Write(testRunID, attestationBytes); err != nil {
		t.Fatal(err)
	}
	bundleTemp := filepath.Join(target, "bundle-temp")
	if _, err := Build(runDir, bundleTemp); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(runDir); err != nil {
		t.Fatal(err)
	}
	bundleDir := filepath.Join(target, ".ai-team", "runs", testRunID)
	if err := os.Rename(bundleTemp, bundleDir); err != nil {
		t.Fatal(err)
	}
	attestationPath := filepath.Join(bundleDir, "attestation.json")
	if err := os.Chmod(attestationPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attestationPath, []byte("not an attestation"), 0644); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(bundleDir, indexFileName)
	if err := os.Chmod(indexPath, 0644); err != nil {
		t.Fatal(err)
	}
	rawIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var index Index
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatal(err)
	}
	for i := range index.Records {
		if index.Records[i].Type == RecordAttestation {
			index.Records[i].SHA256 = sha256Bytes([]byte("not an attestation"))
		}
	}
	rawIndex, err = indexBytes(&index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, rawIndex, 0644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(bundleDir); err == nil {
		t.Fatal("VerifyBundle accepted invalid bundle attestation by reading the external controller copy")
	}
}

func TestExportRejectsNonTerminalRun(t *testing.T) {
	base := t.TempDir()
	runsRoot := filepath.Join(base, "runs")
	runID := "r-export-nt-01"
	store, err := evidence.Start(runsRoot, evidence.RunManifest{
		RunID: runID, Feature: testFeature, StartedAt: now(),
		ConfigSnapshot: json.RawMessage(testConfigJSON), WorkflowSnapshot: json.RawMessage(testWorkJSON),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(store.RunDir(), filepath.Join(base, "bundle")); err == nil {
		t.Fatal("Build должен отказывать non-terminal run (нет anchor.json)")
	}
}

func TestVerifyBundleDetectsTampering(t *testing.T) {
	overwrite := func(path string, data []byte) error {
		if err := os.Chmod(path, 0644); err != nil {
			return err
		}
		return os.WriteFile(path, data, 0644)
	}
	cases := []struct {
		name   string
		mutate func(bundleDir string) error
	}{
		{"event log изменён", func(dir string) error {
			return overwrite(filepath.Join(dir, "events.jsonl"), []byte("{}tampered\n"))
		}},
		{"attempt manifest изменён", func(dir string) error {
			return overwrite(filepath.Join(dir, "attempts", "r-export-0001-001-coder", "manifest.json"), []byte("{}"))
		}},
		{"attestation удалена", func(dir string) error {
			return os.Remove(filepath.Join(dir, "attestation.json"))
		}},
		{"run manifest изменён", func(dir string) error {
			return overwrite(filepath.Join(dir, "run.json"), []byte("{}"))
		}},
		{"config snapshot удалён", func(dir string) error {
			return os.Remove(filepath.Join(dir, "config.json"))
		}},
		{"anchor удалён", func(dir string) error {
			return os.Remove(filepath.Join(dir, "anchor.json"))
		}},
		{"лишний attempt manifest", func(dir string) error {
			return os.MkdirAll(filepath.Join(dir, "attempts", "x-fake"), 0755)
		}},
		{"лишний файл вне index", func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "leaked-secret.txt"), []byte("x"), 0644)
		}},
		{"record с не-whitelisted типом", func(dir string) error {
			var idx Index
			raw, err := os.ReadFile(filepath.Join(dir, indexFileName))
			if err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &idx); err != nil {
				return err
			}
			if len(idx.Records) == 0 {
				return fmt.Errorf("нет records")
			}
			idx.Records = append(idx.Records, Record{Type: "rm -rf /", Path: idx.Records[0].Path, SHA256: idx.Records[0].SHA256})
			out, err := json.MarshalIndent(idx, "", "  ")
			if err != nil {
				return err
			}
			return overwrite(filepath.Join(dir, indexFileName), append(out, '\n'))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			runDir := buildTerminalRun(t, filepath.Join(base, "runs"))
			bundle := filepath.Join(base, "bundle")
			if _, err := Build(runDir, bundle); err != nil {
				t.Fatalf("build: %v", err)
			}
			if err := tc.mutate(bundle); err != nil {
				t.Fatalf("mutate: %v", err)
			}
			if err := VerifyBundle(bundle); err == nil {
				t.Fatal("VerifyBundle обязан обнаружить подмену")
			}
		})
	}
}

func TestVerifyBundleRejectsForeignIndex(t *testing.T) {
	dir := t.TempDir()
	index := Index{SchemaVersion: BundleSchema, Type: "not-a-bundle", RunID: testRunID}
	data, _ := json.MarshalIndent(index, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, indexFileName), append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(dir); err == nil {
		t.Fatal("чужой index.json должен отклоняться")
	}
}

func TestPublishVerifiedRecord(t *testing.T) {
	base := t.TempDir()
	aiTeam := filepath.Join(base, ".ai-team")
	sha := "f1b2c3d4"
	exportedAt := now()
	if err := PublishVerified(aiTeam, testRunID, "/tmp/bundle", sha, exportedAt); err != nil {
		t.Fatalf("PublishVerified: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(aiTeam, "state", "exports", testRunID+".json"))
	if err != nil {
		t.Fatalf("запись не создана: %v", err)
	}
	var record retention.ExportRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.SchemaVersion != retention.ExportSchema || record.RunID != testRunID || !record.Verified ||
		record.BundleSHA256 != sha || record.Bundle != "/tmp/bundle" {
		t.Fatalf("запись не соответствует контракту V0-0: %+v", record)
	}
	if err := PublishVerified(aiTeam, "../escape", "/tmp/bundle", sha, exportedAt); err == nil {
		t.Fatal("недопустимый run_id должен отклоняться")
	}
}

func TestPublishVerifiedRoundtripRetention(t *testing.T) {
	base := t.TempDir()
	aiTeam := filepath.Join(base, ".ai-team")
	sha := "deadbeef00"
	if err := PublishVerified(aiTeam, testRunID, "/b", sha, now()); err != nil {
		t.Fatal(err)
	}
	// Тот же JSON-контракт, который читает gc retention (полевый порядок не
	// важен — отражение). Проверяем совместимость значений.
	raw, err := os.ReadFile(filepath.Join(aiTeam, "state", "exports", testRunID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record retention.ExportRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("retention decode: %v", err)
	}
	if !record.Verified || record.BundleSHA256 != sha {
		t.Fatalf("roundtrip mismatch: %+v", record)
	}
}

func TestBundleSignAndVerify(t *testing.T) {
	base := t.TempDir()
	runDir := buildTerminalRun(t, filepath.Join(base, "runs"))
	bundle := filepath.Join(base, "bundle")
	if _, err := Build(runDir, bundle); err != nil {
		t.Fatalf("build: %v", err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignBundle(bundle, priv); err != nil {
		t.Fatalf("SignBundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bundle, dsse.EnvelopeFileName)); err != nil {
		t.Fatalf("dsse.json не создан: %v", err)
	}

	// Верификация с правильным ключом — успех.
	if err := VerifyBundle(bundle); err != nil {
		t.Fatalf("VerifyBundle unsigned-key (integrity): %v", err)
	}
	if err := VerifyBundle(bundle, pub); err != nil {
		t.Fatalf("VerifyBundle correct key: %v", err)
	}

	// Подмена payload (индекс) при валидной подписи другого содержимого →
	// digest изменится и подпись fail.
	wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := VerifyBundle(bundle, wrongPub); err == nil {
		t.Fatal("VerifyBundle wrong key должен FAIL")
	}

	// Удаление подписи при заданном ключе → fail-closed.
	sigPath := filepath.Join(bundle, dsse.EnvelopeFileName)
	if err := os.Remove(sigPath); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(bundle, pub); err == nil {
		t.Fatal("VerifyBundle с ключом без dsse.json должен FAIL (fail-closed)")
	}
	if err := VerifyBundle(bundle); err != nil {
		t.Fatalf("VerifyBundle без подписи и без ключа должен PASS: %v", err)
	}
}

func TestVerifyBundleTamperedSignature(t *testing.T) {
	base := t.TempDir()
	runDir := buildTerminalRun(t, filepath.Join(base, "runs"))
	bundle := filepath.Join(base, "bundle")
	if _, err := Build(runDir, bundle); err != nil {
		t.Fatalf("build: %v", err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := SignBundle(bundle, priv); err != nil {
		t.Fatalf("SignBundle: %v", err)
	}
	// Порти ском подписи в dsse.json.
	if err := os.Chmod(filepath.Join(bundle, dsse.EnvelopeFileName), 0644); err != nil {
		t.Fatal(err)
	}
	env, present, err := dsse.ReadEnvelopeFile(bundle)
	if err != nil || !present {
		t.Fatalf("read envelope: present=%v err=%v", present, err)
	}
	env.Signature[0] ^= 0xff
	data, _ := dsse.Marshal(env)
	if err := os.WriteFile(filepath.Join(bundle, dsse.EnvelopeFileName), append(data, '\n'), 0444); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(bundle, pub); err == nil {
		t.Fatal("tampered signature с ключом должен FAIL")
	}
}

func TestVerifyBundleEmptySignatureFailsClosed(t *testing.T) {
	base := t.TempDir()
	runDir := buildTerminalRun(t, filepath.Join(base, "runs"))
	bundle := filepath.Join(base, "bundle")
	if _, err := Build(runDir, bundle); err != nil {
		t.Fatalf("build: %v", err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sigPath := filepath.Join(bundle, dsse.EnvelopeFileName)

	writeEnv := func(t *testing.T, env *dsse.Envelope) {
		t.Helper()
		data, err := dsse.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sigPath, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Пустая подпись: с ключом — fail-closed (не молчаливый pass).
	writeEnv(t, &dsse.Envelope{PayloadType: dsse.SignaturePayloadType, Payload: []byte("x"), Signature: nil})
	if err := VerifyBundle(bundle, pub); err == nil {
		t.Fatal("VerifyBundle с пустой подписью и ключом должен FAIL (fail-closed)")
	}

	// Искажённый dsse.json (не валидный envelope) — с ключом fail.
	if err := os.WriteFile(sigPath, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBundle(bundle, pub); err == nil {
		t.Fatal("VerifyBundle с повреждённым dsse.json и ключом должен FAIL")
	}
}
