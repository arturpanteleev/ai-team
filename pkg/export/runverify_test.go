package export

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/provenance"
)

// runverify_test.go — регрессии QS-05/QS-23/QS-24. Каждый тест воспроизводит
// подделку, которая до фикса давала `✓ Run OK`.

const fullRunID = "r-verify-0001"

// fullRun строит терминальный run со всем, что пишет контроллер: попытка с
// файловым и директорным артефактом и входом, attestation, delivery record,
// containment receipt и candidate evidence.
func fullRun(t *testing.T, root string) string {
	t.Helper()
	runsRoot := filepath.Join(root, "runs")
	prov := provenance.New(fullRunID, now())
	prov.Add("runtime", "", provenance.UnknownDigest())
	provJSON, err := json.Marshal(prov)
	if err != nil {
		t.Fatal(err)
	}
	store, err := evidence.Start(runsRoot, evidence.RunManifest{
		RunID: fullRunID, Feature: testFeature, StartedAt: now(),
		ConfigSnapshot:   json.RawMessage(testConfigJSON),
		WorkflowSnapshot: json.RawMessage(testWorkJSON),
		Provenance:       provJSON,
	})
	if err != nil {
		t.Fatalf("evidence start: %v", err)
	}
	runDir := store.RunDir()

	artifactRoot := filepath.Join(root, "artifacts")
	writeFile(t, filepath.Join(artifactRoot, testFeature, "review.md"), "APPROVED: всё хорошо\n")
	writeFile(t, filepath.Join(artifactRoot, testFeature, "specs", "product", "spec.md"), "# spec\n")
	writeFile(t, filepath.Join(artifactRoot, "tasks", testFeature, "task.md"), "сделай дело\n")

	attemptID := store.NewAttemptID("reviewer", 1)
	err = store.PublishAttempt(evidence.AttemptManifest{
		AttemptID: attemptID, Stage: "reviewer", StageIndex: 0,
		StartedAt: now(), FinishedAt: now().Add(time.Minute),
		Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed", Verdict: "APPROVED",
	}, artifactRoot,
		[]evidence.Artifact{{Name: "task", Path: filepath.Join(artifactRoot, "tasks", testFeature, "task.md")}},
		[]evidence.Artifact{
			{Name: "review", Path: filepath.Join(artifactRoot, testFeature, "review.md")},
			{Name: "specs", Path: filepath.Join(artifactRoot, testFeature, "specs")},
		})
	if err != nil {
		t.Fatalf("publish attempt: %v", err)
	}
	_, _, manifestDigest, err := evidence.ArtifactDigest(filepath.Join(runDir, "attempts", attemptID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	planHash := strings.Repeat("ab", 32)
	events := []evidence.Event{
		{Type: "run_started", Timestamp: now()},
		{Type: "attempt_started", Stage: "reviewer", AttemptID: attemptID, Timestamp: now().Add(time.Second),
			Data: map[string]any{"stage_index": 1}},
		{Type: "delivery_deferred", AttemptID: attemptID, Timestamp: now().Add(30 * time.Second),
			Data: map[string]any{"plan_hash": planHash, "state_path": "/tmp/ws/.ai-team/delivery/feat.json"}},
		{Type: "attempt_finished", Stage: "reviewer", AttemptID: attemptID, Timestamp: now().Add(time.Minute),
			Data: map[string]any{"status": "passed", "execution": "succeeded", "decision": "approved",
				"outcome": "passed", "verdict": "APPROVED", "manifest_sha256": manifestDigest}},
		{Type: "run_finished", Timestamp: now().Add(2 * time.Minute),
			Data: map[string]any{"status": "completed", "stage_attempts": 1}},
	}
	for _, event := range events {
		if err := store.Append(event); err != nil {
			t.Fatalf("append %s: %v", event.Type, err)
		}
	}

	workspaceDigest := strings.Repeat("cd", 32)
	statement, err := attest.Build(attest.Options{
		RunDir: runDir, RunID: fullRunID, FinishedAt: now().Add(2 * time.Minute), Outcome: "completed",
		CandidateSubject: []attest.Subject{{Name: "candidate", Digest: map[string]string{"sha256": workspaceDigest}}},
	})
	if err != nil {
		t.Fatalf("attestation build: %v", err)
	}
	statementData, err := attest.Serialize(statement)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "attestation.json"), append(statementData, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	attestationDigest, err := attest.Digest(statement)
	if err != nil {
		t.Fatal(err)
	}

	// candidate evidence и containment receipt — ровно та форма, что пишет
	// контроллер в finalize.
	writeJSONFile(t, filepath.Join(runDir, "candidate.json"), map[string]any{
		"schema_version": 1, "run_id": fullRunID, "purpose": "run_candidate",
		"workspace_sha256": workspaceDigest,
	})
	writeJSONFile(t, filepath.Join(runDir, "containment.json"), containment.CanonicalReceipt("trusted-local"))

	runtimeIdentity := runProvenanceDigest(t, runDir)
	record := delivery.TerminalRecord{
		SchemaVersion: delivery.TerminalRecordSchemaVersion,
		RunID:         fullRunID, Feature: testFeature, PlanHash: planHash,
		CommitSHA: strings.Repeat("1a", 20), PRURL: "https://example.test/pr/1",
		Trailers: []string{
			delivery.TrailerRunID + ": " + fullRunID,
			delivery.TrailerRuntime + ": " + runtimeIdentity,
			delivery.TrailerAttestation + ": " + attestationDigest,
		},
		AttestationSHA256: attestationDigest, RuntimeIdentity: runtimeIdentity,
		PerformedAt: now().Add(3 * time.Minute),
	}
	if err := delivery.WriteTerminalRecord(runDir, record); err != nil {
		t.Fatalf("delivery record: %v", err)
	}
	return runDir
}

// runProvenanceDigest — sha256 raw provenance-байт run.json (так контроллер
// считает runtime identity).
func runProvenanceDigest(t *testing.T, runDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Provenance json.RawMessage `json:"provenance"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifest.Provenance)
	return hex.EncodeToString(sum[:])
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data)+"\n")
}

// rewriteJSON применяет mutate к JSON-объекту файла и записывает результат.
func rewriteJSON(t *testing.T, path string, mutate func(document map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, path, document)
}

func TestVerifyEvidenceAcceptsIntactRun(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	if err := VerifyEvidence(runDir); err != nil {
		t.Fatalf("нетронутая evidence обязана проходить verify: %v", err)
	}
}

// TestVerifyEvidenceDetectsRunManifestTampering — QS-05: до фикса anchor не
// фиксировал digest run.json, и любая правка его содержимого давала Run OK.
func TestVerifyEvidenceDetectsRunManifestTampering(t *testing.T) {
	cases := map[string]func(document map[string]any){
		"feature": func(document map[string]any) { document["feature"] = "hijacked" },
		"target_dir": func(document map[string]any) {
			document["target_dir"] = "/somewhere/else"
		},
		"controller.executable_sha256": func(document map[string]any) {
			controller, _ := document["controller"].(map[string]any)
			controller["executable_sha256"] = strings.Repeat("0", 64)
		},
		"provenance digests": func(document map[string]any) {
			prov, _ := document["provenance"].(map[string]any)
			items, _ := prov["items"].([]any)
			for _, item := range items {
				entry, _ := item.(map[string]any)
				digest, _ := entry["digest"].(map[string]any)
				digest["value"] = strings.Repeat("0", 64)
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			runDir := fullRun(t, t.TempDir())
			rewriteJSON(t, filepath.Join(runDir, "run.json"), mutate)
			err := VerifyEvidence(runDir)
			if err == nil {
				t.Fatal("подмена run.json обязана детектироваться")
			}
			if !strings.Contains(err.Error(), "run_manifest_sha256") {
				t.Fatalf("ожидалась ошибка про run_manifest_sha256, получено: %v", err)
			}
		})
	}
}

// TestVerifyEvidenceDetectsArtifactTampering — QS-05: манифест попытки хранит
// sha256 и size артефакта, но их никто не читал.
func TestVerifyEvidenceDetectsArtifactTampering(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	path := attemptArtifact(t, runDir, filepath.Join("artifacts", testFeature, "review.md"))
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	// Другое содержимое И другая длина — ловится и digest, и size.
	if err := os.WriteFile(path, []byte("REJECTED: подделанный вердикт другой длины\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := VerifyEvidence(runDir)
	if err == nil {
		t.Fatal("подмена архивного артефакта обязана детектироваться")
	}
	if !strings.Contains(err.Error(), "не совпадает с манифестом") {
		t.Fatalf("ожидалась ошибка сверки с манифестом, получено: %v", err)
	}
}

// TestVerifyEvidenceDetectsArtifactInsideDirectoryTampering — директорный
// артефакт сверяется целиком (tree hash), а не по имени каталога.
func TestVerifyEvidenceDetectsArtifactInsideDirectoryTampering(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	path := attemptArtifact(t, runDir, filepath.Join("artifacts", testFeature, "specs", "product", "spec.md"))
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# подменённая спека\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidence(runDir); err == nil {
		t.Fatal("подмена файла внутри директорного артефакта обязана детектироваться")
	}
}

// TestVerifyEvidenceDetectsArtifactDeletion — QS-05: удаление каталога
// артефактов попытки давало Run OK.
func TestVerifyEvidenceDetectsArtifactDeletion(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	attemptID := singleAttemptID(t, runDir)
	if err := os.RemoveAll(filepath.Join(runDir, "attempts", attemptID, "artifacts")); err != nil {
		t.Fatal(err)
	}
	err := VerifyEvidence(runDir)
	if err == nil {
		t.Fatal("удаление артефактов попытки обязано детектироваться")
	}
	if !strings.Contains(err.Error(), "недоступен") {
		t.Fatalf("ожидалась ошибка про недоступный артефакт, получено: %v", err)
	}
}

// TestVerifyEvidenceDetectsInjectedArtifact — подброшенный в evidence файл не
// покрыт ни одним record манифеста.
func TestVerifyEvidenceDetectsInjectedArtifact(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	attemptID := singleAttemptID(t, runDir)
	injected := filepath.Join(runDir, "attempts", attemptID, "artifacts", testFeature, "extra.md")
	if err := os.Chmod(filepath.Dir(injected), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(injected, []byte("подброшено\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := VerifyEvidence(runDir)
	if err == nil {
		t.Fatal("подброшенный артефакт обязан детектироваться")
	}
	if !strings.Contains(err.Error(), "не покрытый манифестом") {
		t.Fatalf("ожидалась ошибка про непокрытый файл, получено: %v", err)
	}
}

// TestVerifyEvidenceDetectsDeliveryTampering — QS-23.
func TestVerifyEvidenceDetectsDeliveryTampering(t *testing.T) {
	cases := map[string]struct {
		mutate func(document map[string]any)
		want   string
	}{
		"commit_sha и pr_url": {
			mutate: func(document map[string]any) {
				document["commit_sha"] = strings.Repeat("9b", 20)
				document["pr_url"] = "https://evil.test/pr/1"
			},
			want: "record_sha256 mismatch",
		},
		"record_sha256 удалён": {
			mutate: func(document map[string]any) {
				document["pr_url"] = "https://evil.test/pr/1"
				delete(document, "record_sha256")
			},
			want: "отсутствует record_sha256",
		},
		"plan_hash из другого run": {
			mutate: func(document map[string]any) {
				document["plan_hash"] = strings.Repeat("ef", 32)
				delete(document, "record_sha256")
			},
			want: "отсутствует record_sha256",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			runDir := fullRun(t, t.TempDir())
			rewriteJSON(t, filepath.Join(runDir, "delivery.json"), testCase.mutate)
			err := VerifyEvidence(runDir)
			if err == nil {
				t.Fatal("подмена delivery record обязана детектироваться")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("ожидалось %q, получено: %v", testCase.want, err)
			}
		})
	}
}

// TestVerifyEvidenceDetectsDeliveryPlanHashMismatch — согласованно
// пересобранный record (с корректным self-digest) всё равно обязан упасть:
// plan_hash сверяется с delivery_deferred событием в hash-цепочке.
func TestVerifyEvidenceDetectsDeliveryPlanHashMismatch(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	record := readDeliveryRecord(t, runDir)
	record.PlanHash = strings.Repeat("ef", 32)
	writeResignedDelivery(t, runDir, record)
	err := VerifyEvidence(runDir)
	if err == nil {
		t.Fatal("plan_hash, не совпадающий с цепочкой, обязан детектироваться")
	}
	if !strings.Contains(err.Error(), "plan_hash") {
		t.Fatalf("ожидалась ошибка про plan_hash, получено: %v", err)
	}
}

// TestVerifyEvidenceDetectsDeliveryRuntimeIdentityMismatch — пересобранный
// record с чужим runtime identity ловится через provenance в run.json.
func TestVerifyEvidenceDetectsDeliveryRuntimeIdentityMismatch(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	record := readDeliveryRecord(t, runDir)
	record.RuntimeIdentity = strings.Repeat("ab", 32)
	record.Trailers = []string{
		delivery.TrailerRunID + ": " + record.RunID,
		delivery.TrailerRuntime + ": " + record.RuntimeIdentity,
		delivery.TrailerAttestation + ": " + record.AttestationSHA256,
	}
	writeResignedDelivery(t, runDir, record)
	err := VerifyEvidence(runDir)
	if err == nil {
		t.Fatal("чужой runtime_identity обязан детектироваться")
	}
	if !strings.Contains(err.Error(), "runtime_identity") {
		t.Fatalf("ожидалась ошибка про runtime_identity, получено: %v", err)
	}
}

// TestVerifyEvidenceDetectsContainmentTampering — QS-24.
func TestVerifyEvidenceDetectsContainmentTampering(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	rewriteJSON(t, filepath.Join(runDir, "containment.json"), func(document map[string]any) {
		axes, _ := document["axes"].(map[string]any)
		for axis := range axes {
			axes[axis] = string(containment.LevelENFORCED)
		}
	})
	err := VerifyEvidence(runDir)
	if err == nil {
		t.Fatal("переворот осей containment receipt обязан детектироваться")
	}
	if !strings.Contains(err.Error(), "containment") {
		t.Fatalf("ожидалась ошибка про containment receipt, получено: %v", err)
	}
}

// TestVerifyEvidenceDetectsCandidateTampering — candidate.json сверяется с
// candidate subject attestation'а.
func TestVerifyEvidenceDetectsCandidateTampering(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	rewriteJSON(t, filepath.Join(runDir, "candidate.json"), func(document map[string]any) {
		document["workspace_sha256"] = strings.Repeat("0", 64)
	})
	err := VerifyEvidence(runDir)
	if err == nil {
		t.Fatal("подмена candidate identity обязана детектироваться")
	}
	if !strings.Contains(err.Error(), "candidate") {
		t.Fatalf("ожидалась ошибка про candidate, получено: %v", err)
	}
}

// TestVerifyEvidenceDoesNotClaimDerivedFiles фиксирует сознательное решение:
// logs/, reports/ и usage.json не покрыты digest'ами и в проверяемый набор не
// входят. Тест существует, чтобы решение нельзя было изменить молча — вместе с
// ним придётся менять и сообщение verify, и docs/ARCHITECTURE.md.
func TestVerifyEvidenceDoesNotClaimDerivedFiles(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	writeFile(t, filepath.Join(runDir, "logs", "stage.log"), "подделанный лог\n")
	writeFile(t, filepath.Join(runDir, "reports", testFeature, "index.html"), "<html>подделка</html>\n")
	writeJSONFile(t, filepath.Join(runDir, "usage.json"), map[string]any{
		"schema_version": 1, "run_id": fullRunID, "total_duration_ms": 0,
	})
	if err := VerifyEvidence(runDir); err != nil {
		t.Fatalf("производные файлы не входят в проверяемый набор, verify не должен падать: %v", err)
	}
}

// TestVerifyBundleIgnoresRunOnlyChecks — bundle не несёт raw-артефактов и
// post-terminal записей, поэтому run-only проверки к нему не применяются.
func TestVerifyBundleIgnoresRunOnlyChecks(t *testing.T) {
	root := t.TempDir()
	runDir := fullRun(t, root)
	bundleDir := filepath.Join(root, "bundle")
	if _, err := Build(runDir, bundleDir); err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := VerifyBundle(bundleDir); err != nil {
		t.Fatalf("verify bundle: %v", err)
	}
}

func attemptArtifact(t *testing.T, runDir, rel string) string {
	t.Helper()
	return filepath.Join(runDir, "attempts", singleAttemptID(t, runDir), rel)
}

func singleAttemptID(t *testing.T, runDir string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(runDir, "attempts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			return entry.Name()
		}
	}
	t.Fatal("в run нет попыток")
	return ""
}

func readDeliveryRecord(t *testing.T, runDir string) delivery.TerminalRecord {
	t.Helper()
	record, ok, err := delivery.ReadTerminalRecord(runDir)
	if err != nil || !ok {
		t.Fatalf("delivery record: ok=%v err=%v", ok, err)
	}
	return *record
}

// writeResignedDelivery перезаписывает delivery.json с корректно пересчитанным
// record_sha256 — модель подделки, которая self-integrity digest обходит.
func writeResignedDelivery(t *testing.T, runDir string, record delivery.TerminalRecord) {
	t.Helper()
	path := filepath.Join(runDir, "delivery.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	record.RecordSHA256 = "" // digest пересчитает писатель
	if err := delivery.WriteTerminalRecord(runDir, record); err != nil {
		t.Fatalf("перезапись delivery record: %v", err)
	}
}

// проверка, что фикстура делает то, что обещает: манифест попытки реально
// содержит digest и size артефактов.
func TestFixtureManifestCarriesArtifactDigests(t *testing.T) {
	runDir := fullRun(t, t.TempDir())
	attemptID := singleAttemptID(t, runDir)
	data, err := os.ReadFile(filepath.Join(runDir, "attempts", attemptID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest evidence.AttemptManifest
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Outputs) != 2 || len(manifest.Inputs) != 1 {
		t.Fatalf("ожидались 2 output и 1 input, получено %d/%d", len(manifest.Outputs), len(manifest.Inputs))
	}
	for _, record := range append(append([]evidence.ArtifactRecord{}, manifest.Outputs...), manifest.Inputs...) {
		if record.SHA256 == "" || record.Size == 0 || record.EvidencePath == "" {
			t.Fatalf("record %s без digest/size/evidence_path", record.Name)
		}
	}
}
