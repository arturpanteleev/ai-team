// Package export реализует самодостаточный deterministic portable bundle
// терминального run (V0-4): whitelisted typed records и digests без raw
// logs/stdout, перепроверяемые без исходного repo и .ai-team. Bundle хранит
// зеркало run evidence (run.json, config/workflow snapshots, hash-chained
// event log, anchor, attestation v1, attempt manifests) плюс index.json с
// sha256 каждого record. Экспорт публикует verified-запись в state/exports
// (контракт V0-0), что открывает право гс на prune этой evidence.
package export

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/provenance"
	"github.com/arturpanteleev/ai-team/pkg/retention"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// BundleSchema — версия контракта portable bundle.
const BundleSchema = 1

// BundleType — тип index.json (отличает bundle от произвольного каталога).
const BundleType = "ai-team-run-bundle"

// indexFileName — имя манифеста bundle.
const indexFileName = "index.json"

// Типы whitelisted typed records, переносимых в bundle. Raw logs/stdout,
// reports и usage-метрики исключены по умолчанию.
const (
	RecordRunManifest      = "run_manifest"
	RecordConfigSnapshot   = "config_snapshot"
	RecordWorkflowSnapshot = "workflow_snapshot"
	RecordEventLog         = "event_log"
	RecordAnchor           = "anchor"
	RecordAttestation      = "attestation"
	RecordAttemptManifest  = "attempt_manifest"
)

// validRecordType — допускаемый whitelisted тип record bundle'а. Неизвестные
// типы отклоняются fail-closed, чтобы произвольный index.json не мог объявить
// любые файлы частью доказательного bundle.
func validRecordType(t string) bool {
	switch t {
	case RecordRunManifest, RecordConfigSnapshot, RecordWorkflowSnapshot,
		RecordEventLog, RecordAnchor, RecordAttestation, RecordAttemptManifest:
		return true
	}
	return false
}

const (
	maxRunManifestSize  = 1 << 20
	maxIndexSize        = 1 << 20
	maxSnapshotSize     = 8 << 20
	maxAnchorSize       = 64 << 10
	maxEventLogSize     = 64 << 20
	maxAttestationSize  = 1 << 20
	maxAttemptManifest  = 8 << 20
	maxBundleIndexFiles = 1 << 12
)

// Record — один whitelisted файл bundle'а и его sha256.
type Record struct {
	Type   string `json:"type"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Index — детерминированный манифест bundle'а. Без тайм-меток: одни и те же
// evidence дают байт-в-байт одинаковый index.json (и одинаковый BundleDigest).
type Index struct {
	SchemaVersion int      `json:"schema_version"`
	Type          string   `json:"type"`
	RunID         string   `json:"run_id"`
	Records       []Record `json:"records"`
}

// indexBytes — те байты index.json, что пишутся на диск (единственный источник
// истины для BundleDigest, чтобы внешний проверяющий воспроизвёл bundle_sha256
// как sha256-файла index.json).
func indexBytes(index *Index) ([]byte, error) {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// BundleDigest — deterministic содержимость identity bundle'а: sha256 ровно тех
// байтов index.json, что пишутся на диск (всегда одинаков для одинакового
// evidence и равен sha256 файла index.json).
func BundleDigest(index *Index) (string, error) {
	data, err := indexBytes(index)
	if err != nil {
		return "", err
	}
	return sha256Bytes(data), nil
}

func sha256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Build копирует whitelisted evidence терминального run в outDir и строит
// index.json. Run обязан быть terminal (anchor.json). Ошибка не оставляет
// partial-config: каталог не создаётся, все копии все-в-одном файле intо outDir.
func Build(runDir, outDir string) (*Index, error) {
	manifest, err := readRunManifest(runDir)
	if err != nil {
		return nil, err
	}
	if _, err := safeio.ReadRegularFile(filepath.Join(runDir, "anchor.json"), maxAnchorSize); err != nil {
		return nil, fmt.Errorf("export: run %s не terminal (нет anchor.json): %w", manifest.RunID, err)
	}
	files := []struct{ kind, rel string }{
		{RecordRunManifest, "run.json"},
		{RecordConfigSnapshot, fromSlash(manifest.ConfigEvidence)},
		{RecordWorkflowSnapshot, fromSlash(manifest.ResolvedWorkflow)},
		{RecordEventLog, "events.jsonl"},
		{RecordAnchor, "anchor.json"},
		{RecordAttestation, "attestation.json"},
	}
	records := make([]Record, 0, len(files)+16)
	for _, file := range files {
		if err := safePath(file.rel); err != nil {
			return nil, fmt.Errorf("export: небезопасный путь evidence %q: %w", file.rel, err)
		}
		sum, err := copyRecord(runDir, file.rel, outDir, file.kind)
		if err != nil {
			return nil, err
		}
		records = append(records, Record{Type: file.kind, Path: filepath.ToSlash(file.rel), SHA256: sum})
	}
	attemptDirs, err := os.ReadDir(filepath.Join(runDir, "attempts"))
	if err != nil {
		return nil, fmt.Errorf("export: attempts: %w", err)
	}
	ids := make([]string, 0, len(attemptDirs))
	for _, entry := range attemptDirs {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		rel := filepath.Join("attempts", id, "manifest.json")
		sum, err := copyRecord(runDir, rel, outDir, RecordAttemptManifest)
		if err != nil {
			return nil, err
		}
		records = append(records, Record{Type: RecordAttemptManifest, Path: filepath.ToSlash(rel), SHA256: sum})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	index := &Index{SchemaVersion: BundleSchema, Type: BundleType, RunID: manifest.RunID, Records: records}
	data, err := indexBytes(index)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, indexFileName), data, 0644); err != nil {
		return nil, err
	}
	return index, nil
}

func fromSlash(path string) string {
	return filepath.FromSlash(strings.TrimSpace(path))
}

func safePath(rel string) error {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "" || clean == "." || filepath.IsAbs(clean) {
		return fmt.Errorf("path %q", rel)
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q выходит за пределы bundle", rel)
	}
	return nil
}

func copyRecord(runDir, rel, outDir, kind string) (string, error) {
	source := filepath.Join(runDir, rel)
	maxBytes := sizeLimitFor(kind, rel)
	data, err := safeio.ReadRegularFile(source, maxBytes)
	if err != nil {
		return "", fmt.Errorf("export: %s %s: %w", kind, rel, err)
	}
	destination := filepath.Join(outDir, rel)
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(destination, data, 0444); err != nil {
		return "", err
	}
	return sha256Bytes(data), nil
}

func sizeLimitFor(kind, rel string) int64 {
	switch {
	case kind == RecordEventLog:
		return maxEventLogSize
	case kind == RecordAttestation:
		return maxAttestationSize
	case kind == RecordAttemptManifest:
		return maxAttemptManifest
	case kind == RecordAnchor:
		return maxAnchorSize
	case kind == RecordRunManifest:
		return maxRunManifestSize
	default:
		return maxSnapshotSize
	}
}

func readRunManifest(runDir string) (*evidence.RunManifest, error) {
	data, err := safeio.ReadRegularFile(filepath.Join(runDir, "run.json"), maxRunManifestSize)
	if err != nil {
		return nil, fmt.Errorf("export: run manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest evidence.RunManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("export: run manifest decode: %w", err)
	}
	if manifest.RunID == "" || manifest.SchemaVersion != evidence.SchemaVersion {
		return nil, fmt.Errorf("export: run manifest identity/schema mismatch")
	}
	return &manifest, nil
}

// VerifyBundle — самодостаточная проверка bundle без исходного repo и
// .ai-team: каждый record обязан совпадать со своим sha256, а все semantic
// связи (run manifest ↔ config/workflow snapshots ↔ event chain ↔ anchor ↔
// attempt manifests ↔ attestation v1 ↔ provenance) обязаны сойтись.
func VerifyBundle(bundleDir string) error {
	indexData, err := safeio.ReadRegularFile(filepath.Join(bundleDir, indexFileName), maxIndexSize)
	if err != nil {
		return fmt.Errorf("verify: bundle index: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(indexData))
	decoder.DisallowUnknownFields()
	var index Index
	if err := decoder.Decode(&index); err != nil {
		return fmt.Errorf("verify: bundle index decode: %w", err)
	}
	if index.SchemaVersion != BundleSchema || index.Type != BundleType {
		return fmt.Errorf("verify: bundle index не является supported ai-team run bundle")
	}
	if index.RunID == "" || len(index.Records) == 0 || len(index.Records) > maxBundleIndexFiles {
		return fmt.Errorf("verify: bundle index повреждён")
	}
	seen := make(map[string]bool, len(index.Records))
	for _, record := range index.Records {
		if !validRecordType(record.Type) {
			return fmt.Errorf("verify: record %q имеет не-whitelisted тип %q", record.Path, record.Type)
		}
		if err := safePath(filepath.FromSlash(record.Path)); err != nil {
			return fmt.Errorf("verify: record %q небезопасен", record.Path)
		}
		if seen[record.Path] {
			return fmt.Errorf("verify: дублирующийся record %q", record.Path)
		}
		seen[record.Path] = true
		data, readErr := safeio.ReadRegularFile(filepath.Join(bundleDir, filepath.FromSlash(record.Path)), sizeLimitFor(record.Type, record.Path))
		if readErr != nil {
			return fmt.Errorf("verify: record %s %s: %w", record.Type, record.Path, readErr)
		}
		if sum := sha256Bytes(data); sum != record.SHA256 {
			return fmt.Errorf("verify: record %s %s не совпадает со своим sha256 (evidence подменён)", record.Type, record.Path)
		}
	}
	if err := ensureNoExtraneousFiles(bundleDir, &index); err != nil {
		return err
	}
	if err := verifyCore(bundleDir, index.RunID, index.Records); err != nil {
		return err
	}
	return nil
}

// ensureNoExtraneousFiles — bundle не должен содержать файлов вне index.json:
// любой лишний файл вне whitelisted records — повод для подозрения, отклоняется.
func ensureNoExtraneousFiles(bundleDir string, index *Index) error {
	seen := make(map[string]bool, len(index.Records))
	for _, record := range index.Records {
		seen[filepath.FromSlash(record.Path)] = true
	}
	return filepath.WalkDir(bundleDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(bundleDir, path)
		if relErr != nil {
			return relErr
		}
		if rel == indexFileName || seen[rel] {
			return nil
		}
		return fmt.Errorf("verify: неизвестный файл %q вне index.json (лишние файлы не допускаются)", rel)
	})
}

// VerifyEvidence — полная semantic-проверка live run evidence (та же логика,
// что и bundle-verify, но против каталога run без index.json). Используется
// `ai-team verify <run_id>`.
func VerifyEvidence(runDir string) error {
	manifest, err := readRunManifest(runDir)
	if err != nil {
		return err
	}
	if _, err := safeio.ReadRegularFile(filepath.Join(runDir, "anchor.json"), maxAnchorSize); err != nil {
		return fmt.Errorf("verify: run %s не terminal: %w", manifest.RunID, err)
	}
	records, err := collectRunRecords(runDir, manifest)
	if err != nil {
		return err
	}
	return verifyCore(runDir, manifest.RunID, records)
}

func collectRunRecords(runDir string, manifest *evidence.RunManifest) ([]Record, error) {
	records := []Record{
		{Type: RecordRunManifest, Path: "run.json"},
		{Type: RecordConfigSnapshot, Path: manifest.ConfigEvidence},
		{Type: RecordWorkflowSnapshot, Path: manifest.ResolvedWorkflow},
		{Type: RecordEventLog, Path: "events.jsonl"},
		{Type: RecordAnchor, Path: "anchor.json"},
		{Type: RecordAttestation, Path: "attestation.json"},
	}
	attemptDirs, err := os.ReadDir(filepath.Join(runDir, "attempts"))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(attemptDirs))
	for _, entry := range attemptDirs {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		rel := filepath.Join("attempts", id, "manifest.json")
		sum, err := fileDigest(filepath.Join(runDir, rel), maxAttemptManifest)
		if err != nil {
			return nil, err
		}
		records = append(records, Record{Type: RecordAttemptManifest, Path: rel, SHA256: sum})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	return records, nil
}

// verifyCore — semantic-свёртка evidence против каталога с run layout:
// 1) run manifest identity; 2) hash-цепочка + anchor; 3) attempt manifests
// ↔ events; 4) attestation v1 ↔ run/spec/events/attempts/provenance.
func verifyCore(root, runID string, records []Record) error {
	manifest, err := readRunManifest(root)
	if err != nil {
		return err
	}
	if manifest.RunID != runID {
		return fmt.Errorf("verify: run manifest run_id %q не совпадает с bundle %q", manifest.RunID, runID)
	}
	configSHA, err := fileDigest(filepath.Join(root, manifest.ConfigEvidence), maxSnapshotSize)
	if err != nil {
		return err
	}
	if configSHA != manifest.ConfigSHA256 {
		return fmt.Errorf("verify: config snapshot не совпадает со своим sha256 в run manifest")
	}
	workflowSHA, err := fileDigest(filepath.Join(root, manifest.ResolvedWorkflow), maxSnapshotSize)
	if err != nil {
		return err
	}
	if workflowSHA != manifest.ResolvedWorkflowSHA256 {
		return fmt.Errorf("verify: workflow snapshot не совпадает со своим sha256 в run manifest")
	}

	if err := evidence.VerifyAnchor(root); err != nil {
		return fmt.Errorf("verify: anchor: %w", err)
	}
	events, err := evidence.VerifyEventLog(filepath.Join(root, "events.jsonl"), runID)
	if err != nil {
		return fmt.Errorf("verify: event log: %w", err)
	}

	manifestPeers := make(map[string]struct{}, len(records))
	manifestByEvent := make(map[string]string)
	for _, event := range events {
		if event.Type != "attempt_finished" || !safeAttemptID(event.AttemptID) {
			continue
		}
		digest, ok := event.Data["manifest_sha256"].(string)
		if !ok || digest == "" {
			continue
		}
		manifestByEvent[event.AttemptID] = digest
	}
	attemptCount := 0
	for _, record := range records {
		if record.Type != RecordAttemptManifest {
			continue
		}
		attemptCount++
		id := filepath.Base(filepath.Dir(filepath.FromSlash(record.Path)))
		expected, claimed := manifestByEvent[id]
		if !claimed {
			return fmt.Errorf("verify: attempt %s нет manifest_sha256 в event chain (evidens непокрыт)", id)
		}
		if record.SHA256 != expected {
			return fmt.Errorf("verify: attempt %s manifest не совпадает с event chain", id)
		}
		manifestPeers[id] = struct{}{}
	}
	if len(manifestByEvent) != attemptCount {
		return fmt.Errorf("verify: число attempt manifests (%d) не совпадает с attempt_finished событиями (%d)",
			attemptCount, len(manifestByEvent))
	}
	foundDirs, err := os.ReadDir(filepath.Join(root, "attempts"))
	if err != nil {
		return err
	}
	for _, entry := range foundDirs {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if _, ok := manifestPeers[entry.Name()]; !ok {
			return fmt.Errorf("verify: лишняя attempt-директория %q не покрыта event chain", entry.Name())
		}
	}

	attestationData, err := safeio.ReadRegularFile(filepath.Join(root, "attestation.json"), maxAttestationSize)
	if err != nil {
		return fmt.Errorf("verify: attestation: %w", err)
	}
	statement, err := attest.Parse(attestationData)
	if err != nil {
		return fmt.Errorf("verify: attestation parse: %w", err)
	}
	predicate := &statement.Predicate
	if predicate.RunID != runID || predicate.Run.EvidenceSchemaVersion != evidence.SchemaVersion {
		return fmt.Errorf("verify: attestation run identity/schema mismatch")
	}
	eventBytes, err := safeio.ReadRegularFile(filepath.Join(root, "events.jsonl"), maxEventLogSize)
	if err != nil {
		return err
	}
	if predicate.Run.EventLogSHA256 != sha256Bytes(eventBytes) {
		return fmt.Errorf("verify: attestation event_log_sha256 не совпадает с событиями")
	}
	if predicate.Run.ConfigSHA256 != manifest.ConfigSHA256 {
		return fmt.Errorf("verify: attestation config_sha256 не совпадает с run manifest")
	}
	if predicate.Spec.ResolvedWorkflowSHA256 != manifest.ResolvedWorkflowSHA256 {
		return fmt.Errorf("verify: attestation resolved_workflow_sha256 не совпадает с run manifest")
	}
	if predicate.Run.AttemptCount != attemptCount {
		return fmt.Errorf("verify: attestation attempt_count %d != %d", predicate.Run.AttemptCount, attemptCount)
	}
	if predicate.Provenance == nil || predicate.Provenance.SchemaVersion != provenance.SchemaVersion {
		return fmt.Errorf("verify: attestation должна нести provenance manifest v1 (V0-2)")
	}
	if predicate.Provenance.RunID != runID {
		return fmt.Errorf("verify: attestation provenance run_id не совпадает")
	}
	return nil
}

func fileDigest(path string, maxBytes int64) (string, error) {
	data, err := safeio.ReadRegularFile(path, maxBytes)
	if err != nil {
		return "", err
	}
	return sha256Bytes(data), nil
}

func safeAttemptID(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value
}

// PublishVerified пишет verified-запись в state/exports/<runID>.json
// (контракт V0-0/V0-4): только после полной проверки bundle. Эта запись
// открывает право `gc --prune-runs` удалить immutable evidence run'а.
func PublishVerified(aiTeamRoot, runID, bundle string, bundleSHA string, exportedAt time.Time) error {
	if aiTeamRoot == "" || runID == "" || runID == "." || runID == ".." || filepath.Base(runID) != runID {
		return fmt.Errorf("недопустимый run_id %q", runID)
	}
	exportsDir := filepath.Join(aiTeamRoot, "state", "exports")
	if err := os.MkdirAll(exportsDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(exportsDir, runID+".json")
	if err := safeio.RejectSymlink(path); err != nil {
		return err
	}
	record := retention.ExportRecord{
		SchemaVersion: retention.ExportSchema,
		RunID:         runID,
		Verified:      true,
		Bundle:        bundle,
		BundleSHA256:  bundleSHA,
		ExportedAt:    exportedAt,
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(exportsDir, ".tmp-export-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
