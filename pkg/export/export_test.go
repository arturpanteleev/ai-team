package export

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/attest"
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
	t.Helper()
	runID := testRunID
	prov := provenance.New(runID, now())
	prov.Add("runtime", "", provenance.UnknownDigest())
	provJSON, err := json.Marshal(prov)
	if err != nil {
		t.Fatal(err)
	}
	store, err := evidence.Start(runsRoot, evidence.RunManifest{
		RunID: runID, Feature: testFeature, StartedAt: now(),
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
	defer os.RemoveAll(artifactRoot)
	attemptID := store.NewAttemptID("coder", 1)
	err = store.PublishAttempt(evidence.AttemptManifest{
		AttemptID: attemptID, Stage: "coder", StageIndex: 0,
		StartedAt: now(), FinishedAt: now().Add(90 * time.Second),
		Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed", Verdict: "APPROVED",
	}, artifactRoot, nil, nil)
	if err != nil {
		t.Fatalf("publish attempt: %v", err)
	}
	manifestPath := filepath.Join(store.RunDir(), "attempts", attemptID, "manifest.json")
	_, _, manifestDigest, err := evidence.ArtifactDigest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{
		Type: "run_started", Timestamp: now(),
	}); err != nil {
		t.Fatalf("run_started: %v", err)
	}
	if err := store.Append(evidence.Event{
		Type: "attempt_started", Stage: "coder", AttemptID: attemptID, Timestamp: now().Add(time.Second),
		Data: map[string]any{"stage_index": 1},
	}); err != nil {
		t.Fatalf("attempt_started: %v", err)
	}
	if err := store.Append(evidence.Event{
		Type: "attempt_finished", Stage: "coder", AttemptID: attemptID, Timestamp: now().Add(90 * time.Second),
		Data: map[string]any{"status": "passed", "execution": "succeeded", "decision": "approved",
			"outcome": "passed", "verdict": "APPROVED", "manifest_sha256": manifestDigest},
	}); err != nil {
		t.Fatalf("attempt_finished: %v", err)
	}
	if err := store.Append(evidence.Event{
		Type: "run_finished", Timestamp: now().Add(2 * time.Minute),
		Data: map[string]any{"status": "completed", "stage_attempts": 1},
	}); err != nil {
		t.Fatalf("run_finished: %v", err)
	}
	statement, err := attest.Build(attest.Options{
		RunDir: store.RunDir(), RunID: runID, FinishedAt: now().Add(2 * time.Minute),
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

func TestVerifyEvidenceLiveRun(t *testing.T) {
	base := t.TempDir()
	runDir := buildTerminalRun(t, filepath.Join(base, "runs"))
	if err := VerifyEvidence(runDir); err != nil {
		t.Fatalf("VerifyEvidence live run: %v", err)
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
			return os.WriteFile(filepath.Join(dir, indexFileName), append(out, '\n'), 0644)
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
