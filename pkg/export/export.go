// Package export реализует самодостаточный deterministic portable bundle
// терминального run (V0-4): whitelisted typed records и digests без raw
// logs/stdout, перепроверяемые без исходного repo и .ai-team. Bundle хранит
// зеркало run evidence (run.json, config/workflow snapshots, hash-chained
// event log, anchor, attestation v1, attempt manifests и artifacts), а также
// наличные delivery/containment receipts, плюс index.json с sha256 каждого
// record. Экспорт публикует verified-запись в state/exports (V0-0), что
// открывает право gc на prune этой evidence.
package export

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/dsse"
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
	RecordArtifact         = "attempt_artifact"
	RecordDelivery         = "delivery"
	RecordContainment      = "containment"
)

// validRecordType — допускаемый whitelisted тип record bundle'а. Неизвестные
// типы отклоняются fail-closed, чтобы произвольный index.json не мог объявить
// любые файлы частью доказательного bundle.
func validRecordType(t string) bool {
	switch t {
	case RecordRunManifest, RecordConfigSnapshot, RecordWorkflowSnapshot,
		RecordEventLog, RecordAnchor, RecordAttestation, RecordAttemptManifest,
		RecordArtifact, RecordDelivery, RecordContainment:
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
	if err := VerifyEvidence(runDir); err != nil {
		return nil, fmt.Errorf("export: run evidence verification: %w", err)
	}
	files := []struct{ kind, rel string }{
		{RecordRunManifest, "run.json"},
		{RecordConfigSnapshot, fromSlash(manifest.ConfigEvidence)},
		{RecordWorkflowSnapshot, fromSlash(manifest.ResolvedWorkflow)},
		{RecordEventLog, "events.jsonl"},
		{RecordAnchor, "anchor.json"},
		{RecordAttestation, "attestation.json"},
	}
	if _, err := os.Lstat(filepath.Join(runDir, "containment.json")); err == nil {
		files = append(files, struct{ kind, rel string }{RecordContainment, "containment.json"})
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("export: containment receipt: %w", err)
	}
	if _, found, err := readDeliveryRecord(runDir, manifest, true); err != nil {
		return nil, fmt.Errorf("export: delivery record: %w", err)
	} else if found {
		files = append(files, struct{ kind, rel string }{RecordDelivery, "delivery.json"})
	}
	if _, found, err := readContainmentReceipt(runDir, manifest, true); err != nil {
		return nil, fmt.Errorf("export: containment receipt: %w", err)
	} else if found {
		alreadyIncluded := false
		for _, file := range files {
			alreadyIncluded = alreadyIncluded || file.kind == RecordContainment
		}
		if !alreadyIncluded {
			files = append(files, struct{ kind, rel string }{RecordContainment, "containment.json"})
		}
	}
	records := make([]Record, 0, len(files)+16)
	for _, file := range files {
		if err := safePath(file.rel); err != nil {
			return nil, fmt.Errorf("export: небезопасный путь evidence %q: %w", file.rel, err)
		}
		var sum string
		if file.kind == RecordEventLog {
			data, readErr := evidence.ReadEventLogBytesForRunDir(runDir, manifest.RunID)
			if readErr != nil {
				return nil, fmt.Errorf("export: event log: %w", readErr)
			}
			sum, err = writeRecordBytes(data, file.rel, outDir, file.kind)
		} else if file.kind == RecordAttestation {
			data, sourceErr := runAttestationData(runDir, manifest.RunID)
			if sourceErr != nil {
				return nil, sourceErr
			}
			sum, err = writeRecordBytes(data, file.rel, outDir, file.kind)
		} else if file.kind == RecordDelivery {
			record, _, readErr := readDeliveryRecord(runDir, manifest, true)
			if readErr != nil {
				return nil, fmt.Errorf("export: delivery record: %w", readErr)
			}
			data, marshalErr := json.MarshalIndent(record, "", "  ")
			if marshalErr != nil {
				return nil, marshalErr
			}
			data = append(data, '\n')
			sum, err = writeRecordBytes(data, file.rel, outDir, file.kind)
		} else if file.kind == RecordContainment {
			receipt, _, readErr := readContainmentReceipt(runDir, manifest, true)
			if readErr != nil {
				return nil, fmt.Errorf("export: containment receipt: %w", readErr)
			}
			data, marshalErr := json.MarshalIndent(receipt, "", "  ")
			if marshalErr != nil {
				return nil, marshalErr
			}
			data = append(data, '\n')
			sum, err = writeRecordBytes(data, file.rel, outDir, file.kind)
		} else {
			sum, err = copyRecord(runDir, file.rel, outDir, file.kind)
		}
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
		data, _, readErr := evidence.ReadAttemptManifest(nil, runDir, manifest.RunID, id)
		if readErr != nil {
			return nil, fmt.Errorf("export attempt manifest %s: %w", id, readErr)
		}
		sum, err := writeRecordBytes(data, rel, outDir, RecordAttemptManifest)
		if err != nil {
			return nil, err
		}
		records = append(records, Record{Type: RecordAttemptManifest, Path: filepath.ToSlash(rel), SHA256: sum})
		var attempt evidence.AttemptManifest
		if err := json.Unmarshal(data, &attempt); err != nil {
			return nil, fmt.Errorf("export attempt manifest %s decode: %w", id, err)
		}
		for _, group := range []struct {
			area      string
			artifacts []evidence.ArtifactRecord
		}{
			{area: "inputs", artifacts: attempt.Inputs},
			{area: "artifacts", artifacts: attempt.Outputs},
		} {
			for _, artifact := range group.artifacts {
				artifactPath, pathErr := safeArtifactPath(artifact.EvidencePath, id, group.area)
				if pathErr != nil {
					return nil, fmt.Errorf("export: unsafe attempt artifact path %q: %w", artifact.EvidencePath, pathErr)
				}
				artifactSum, err := copyArtifactRecord(runDir, artifactPath, outDir)
				if err != nil {
					return nil, fmt.Errorf("export attempt artifact %s: %w", artifactPath, err)
				}
				records = append(records, Record{Type: RecordArtifact, Path: artifactPath, SHA256: artifactSum})
			}
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	index := &Index{SchemaVersion: BundleSchema, Type: BundleType, RunID: manifest.RunID, Records: records}
	data, err := indexBytes(index)
	if err != nil {
		return nil, err
	}
	// index.json пишется теми же read-only правами, что и records: манифест
	// не исключение (PDD-25).
	if err := os.WriteFile(filepath.Join(outDir, indexFileName), data, safeio.ReadOnlyFileMode); err != nil {
		return nil, err
	}
	return index, nil
}

func runAttestationData(runDir, runID string) ([]byte, error) {
	targetDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Clean(runDir))))
	data, err := (attest.ControllerStore{TargetDir: targetDir}).Read(runID)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("export: controller attestation: %w", err)
	}
	data, err = safeio.ReadRegularFile(filepath.Join(runDir, "attestation.json"), maxAttestationSize)
	if err != nil {
		return nil, fmt.Errorf("export: attestation: %w", err)
	}
	statement, err := attest.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("export: legacy attestation invalid: %w", err)
	}
	if statement.Predicate.RunID != runID {
		return nil, fmt.Errorf("export: legacy attestation run mismatch: %s != %s", statement.Predicate.RunID, runID)
	}
	return data, nil
}

func writeRecordBytes(data []byte, rel, outDir, kind string) (string, error) {
	if int64(len(data)) > sizeLimitFor(kind, rel) {
		return "", fmt.Errorf("export: %s %s exceeds size limit", kind, rel)
	}
	destination := filepath.Join(outDir, rel)
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(destination, data, safeio.ReadOnlyFileMode); err != nil {
		return "", err
	}
	return sha256Bytes(data), nil
}

// SignBundle подписывает детерминированный BundleDigest собранного bundle
// (outDir с index.json) ed25519-ключом priv и пишет dsse.json
// (SignaturePayloadType). Вызывается после Build.
func SignBundle(outDir string, priv ed25519.PrivateKey) error {
	data, err := safeio.ReadRegularFile(filepath.Join(outDir, indexFileName), maxIndexSize)
	if err != nil {
		return fmt.Errorf("export sign: index.json: %w", err)
	}
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		return fmt.Errorf("export sign: index.json decode: %w", err)
	}
	if index.Type != BundleType || index.RunID == "" {
		return fmt.Errorf("export sign: index.json не является run bundle")
	}
	digest, err := BundleDigest(&index)
	if err != nil {
		return fmt.Errorf("export sign: BundleDigest: %w", err)
	}
	if err := dsse.SignBundleFile(outDir, priv, dsse.SignaturePayloadType, []byte(digest)); err != nil {
		return fmt.Errorf("export sign: %w", err)
	}
	return nil
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

// safeArtifactPath validates the portable, attempt-scoped path recorded in an
// attempt manifest. Requiring canonical slash-separated paths prevents a
// manifest from naming an artifact outside its input/output namespace or
// aliasing the same file through alternate path spellings.
func safeArtifactPath(rel, attemptID, area string) (string, error) {
	if !safeAttemptID(attemptID) || strings.ContainsAny(rel, "\\\x00") {
		return "", fmt.Errorf("invalid attempt or path")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	canonical := filepath.ToSlash(clean)
	if filepath.IsAbs(clean) || canonical != rel || canonical == "." ||
		canonical == ".." || strings.HasPrefix(canonical, "../") {
		return "", fmt.Errorf("path is not canonical and relative")
	}
	prefix := filepath.ToSlash(filepath.Join("attempts", attemptID, area)) + "/"
	if !strings.HasPrefix(canonical, prefix) || len(canonical) == len(prefix) {
		return "", fmt.Errorf("path is outside attempts/%s/%s", attemptID, area)
	}
	return canonical, nil
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
	if err := os.WriteFile(destination, data, safeio.ReadOnlyFileMode); err != nil {
		return "", err
	}
	return sha256Bytes(data), nil
}

func copyArtifactRecord(runDir, rel, outDir string) (string, error) {
	if err := safePath(filepath.FromSlash(rel)); err != nil {
		return "", err
	}
	source := filepath.Join(runDir, filepath.FromSlash(rel))
	artifactType, _, digest, err := evidence.ArtifactDigest(source)
	if err != nil {
		return "", err
	}
	destination := filepath.Join(outDir, filepath.FromSlash(rel))
	if artifactType == "file" {
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return "", err
		}
		if err := copyRegularFile(source, destination); err != nil {
			return "", err
		}
		return digest, nil
	}
	if artifactType != "directory" {
		return "", fmt.Errorf("unsupported artifact type %q", artifactType)
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		return "", err
	}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		outPath := filepath.Join(destination, relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link in evidence artifact %s", path)
		}
		if entry.IsDir() {
			return os.MkdirAll(outPath, 0755)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported evidence artifact entry %s", path)
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			return err
		}
		return copyRegularFile(path, outPath)
	})
	return digest, err
}

func copyRegularFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact source %s is not a regular file", source)
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, safeio.ReadOnlyFileMode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}

func readDeliveryRecord(runDir string, manifest *evidence.RunManifest, liveRun bool) (*delivery.TerminalRecord, bool, error) {
	if !liveRun {
		return delivery.ReadTerminalRecord(runDir)
	}
	targetDir := manifest.TargetDir
	if targetDir == "" {
		targetDir = filepath.Dir(filepath.Dir(filepath.Dir(filepath.Clean(runDir))))
	}
	return delivery.ReadTerminalRecordForRun(targetDir, runDir, manifest.RunID)
}

func readContainmentReceipt(runDir string, manifest *evidence.RunManifest, liveRun bool) (*containment.Receipt, bool, error) {
	data, err := safeio.ReadRegularFile(filepath.Join(runDir, "containment.json"), maxSnapshotSize)
	if err == nil {
		var receipt containment.Receipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			return nil, false, fmt.Errorf("containment.json: %w", err)
		}
		if err := receipt.Validate(); err != nil {
			return nil, false, err
		}
		return &receipt, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if !liveRun || manifest.TargetDir == "" {
		return nil, false, nil
	}
	receipt, err := (containment.ControllerReceiptStore{TargetDir: manifest.TargetDir}).Read(manifest.RunID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &receipt, true, nil
}

func verifyAttemptArtifacts(root, runID string, records []Record, liveRun bool) error {
	indexedArtifacts := make(map[string]string)
	for _, record := range records {
		if record.Type == RecordArtifact {
			path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(record.Path)))
			if _, exists := indexedArtifacts[path]; exists {
				return fmt.Errorf("verify: duplicate attempt artifact record %s", path)
			}
			indexedArtifacts[path] = record.SHA256
		}
	}
	verifiedArtifacts := make(map[string]bool)
	for _, record := range records {
		if record.Type != RecordAttemptManifest {
			continue
		}
		attemptID := filepath.Base(filepath.Dir(filepath.FromSlash(record.Path)))
		_, manifest, err := evidence.ReadAttemptManifest(nil, root, runID, attemptID)
		if err != nil {
			return fmt.Errorf("verify: attempt %s manifest: %w", attemptID, err)
		}
		if manifest.RunID != runID || manifest.AttemptID != attemptID {
			return fmt.Errorf("verify: attempt %s identity mismatch", attemptID)
		}
		for _, group := range []struct {
			name    string
			records []evidence.ArtifactRecord
		}{
			{name: "input", records: manifest.Inputs},
			{name: "output", records: manifest.Outputs},
		} {
			for _, artifact := range group.records {
				area := "inputs"
				if group.name == "output" {
					area = "artifacts"
				}
				path, pathErr := safeArtifactPath(artifact.EvidencePath, attemptID, area)
				if pathErr != nil {
					return fmt.Errorf("verify: attempt %s %s path is unsafe: %q", attemptID, group.name, artifact.EvidencePath)
				}
				if verifiedArtifacts[path] {
					return fmt.Errorf("verify: attempt %s duplicate %s path %s", attemptID, group.name, path)
				}
				verifiedArtifacts[path] = true
				artifactType, size, digest, err := evidence.ArtifactDigest(filepath.Join(root, filepath.FromSlash(path)))
				if err != nil {
					return fmt.Errorf("verify: attempt %s %s %s: %w", attemptID, group.name, path, err)
				}
				if artifactType != artifact.Type || size != artifact.Size || digest != artifact.SHA256 {
					return fmt.Errorf("verify: attempt %s %s %s digest/size mismatch", attemptID, group.name, path)
				}
				if indexed, ok := indexedArtifacts[path]; !liveRun && (!ok || indexed != digest) {
					return fmt.Errorf("verify: attempt %s artifact %s is missing from bundle records", attemptID, path)
				}
			}
		}
	}
	if !liveRun && len(verifiedArtifacts) != len(indexedArtifacts) {
		return fmt.Errorf("verify: bundle has unreferenced attempt artifact records")
	}
	return nil
}

func verifyContainment(root string, manifest *evidence.RunManifest, records []Record, liveRun bool) error {
	receipt, found, err := readContainmentReceipt(root, manifest, liveRun)
	if err != nil {
		return fmt.Errorf("verify: containment receipt: %w", err)
	}
	var indexed *Record
	for i := range records {
		if records[i].Type == RecordContainment {
			indexed = &records[i]
			break
		}
	}
	if found != (indexed != nil) {
		return errors.New("verify: containment receipt record/index mismatch")
	}
	if !found {
		return nil
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if sha256Bytes(append(data, '\n')) != indexed.SHA256 {
		return errors.New("verify: containment receipt digest mismatch")
	}
	return nil
}

func verifyDelivery(root string, manifest *evidence.RunManifest, events []evidence.Event, records []Record, liveRun bool) error {
	record, found, err := readDeliveryRecord(root, manifest, liveRun)
	if err != nil {
		return fmt.Errorf("verify: delivery record: %w", err)
	}
	var indexed *Record
	for i := range records {
		if records[i].Type == RecordDelivery {
			indexed = &records[i]
			break
		}
	}
	if found != (indexed != nil) {
		return errors.New("verify: delivery record/index mismatch")
	}
	if !found {
		anchorData, readErr := safeio.ReadRegularFile(filepath.Join(root, "anchor.json"), maxAnchorSize)
		if readErr != nil {
			return fmt.Errorf("verify: delivery anchor: %w", readErr)
		}
		var anchor evidence.Anchor
		if err := json.Unmarshal(anchorData, &anchor); err != nil {
			return fmt.Errorf("verify: delivery anchor decode: %w", err)
		}
		if anchor.DeliveryRecordSHA256 != "" {
			return errors.New("verify: anchor contains a delivery digest but no delivery record is present")
		}
		return nil
	}
	if record.RunID != manifest.RunID || record.Feature != manifest.Feature {
		return errors.New("verify: delivery record run/feature identity mismatch")
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if sha256Bytes(append(data, '\n')) != indexed.SHA256 {
		return errors.New("verify: delivery record digest mismatch")
	}
	anchorData, err := safeio.ReadRegularFile(filepath.Join(root, "anchor.json"), maxAnchorSize)
	if err != nil {
		return fmt.Errorf("verify: delivery anchor: %w", err)
	}
	var anchor evidence.Anchor
	if err := json.Unmarshal(anchorData, &anchor); err != nil {
		return fmt.Errorf("verify: delivery anchor decode: %w", err)
	}
	if anchor.DeliveryRecordSHA256 == "" || sha256Bytes(append(data, '\n')) != anchor.DeliveryRecordSHA256 {
		return errors.New("verify: delivery record does not match the terminal anchor")
	}
	deferred := false
	for _, event := range events {
		if event.Type != "delivery_deferred" {
			continue
		}
		planHash, _ := event.Data["plan_hash"].(string)
		if planHash == record.PlanHash {
			deferred = true
			break
		}
	}
	if !deferred {
		return errors.New("verify: delivery record has no matching delivery_deferred event")
	}
	return nil
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
//
// Опционально (variadic keyVerify) проверяется DSSE-подпись bundle (P1-5):
// если задан открытый ключ — подпись обязана присутствовать и совпадать
// (fail-closed). Если dsse.json присутствует, а ключ пуст — сообщение о
// неподтверждённой подписи (не ошибка, но требование при verify с ключом).
func VerifyBundle(bundleDir string, keyVerify ...ed25519.PublicKey) error {
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
		path := filepath.Join(bundleDir, filepath.FromSlash(record.Path))
		var sum string
		if record.Type == RecordArtifact {
			_, _, sum, err = evidence.ArtifactDigest(path)
			if err != nil {
				return fmt.Errorf("verify: record %s %s: %w", record.Type, record.Path, err)
			}
		} else {
			data, readErr := safeio.ReadRegularFile(path, sizeLimitFor(record.Type, record.Path))
			if readErr != nil {
				return fmt.Errorf("verify: record %s %s: %w", record.Type, record.Path, readErr)
			}
			sum = sha256Bytes(data)
		}
		if sum != record.SHA256 {
			return fmt.Errorf("verify: record %s %s не совпадает со своим sha256 (evidence подменён)", record.Type, record.Path)
		}
	}
	if err := ensureNoExtraneousFiles(bundleDir, &index); err != nil {
		return err
	}
	if err := verifyCore(bundleDir, index.RunID, index.Records, false); err != nil {
		return err
	}
	digest, err := BundleDigest(&index)
	if err != nil {
		return err
	}
	if err := verifySignature(bundleDir, digest, keyVerify...); err != nil {
		return err
	}
	return nil
}

// verifySignature проверяет DSSE-подпись (dsse.json) против digest. Правила
// (P1-5, fail-closed): ключ задан → подпись обязана быть и совпадать;
// ключ пуст и подписи нет → pass (integrity-only); ключ пуст, подпись есть →
// pass, но подпись остаётся непроверенной (без ключа проверить нельзя).
func verifySignature(bundleDir, digest string, keyVerify ...ed25519.PublicKey) error {
	env, present, err := dsse.ReadEnvelopeFile(bundleDir)
	if err != nil {
		return fmt.Errorf("verify: dsse.json: %w", err)
	}
	keyGiven := len(keyVerify) > 0 && len(keyVerify[0]) > 0
	if !present {
		if keyGiven {
			return errors.New("verify: задан --verify-key, но dsse.json (подпись bundle) отсутствует")
		}
		return nil
	}
	if !keyGiven {
		return nil
	}
	payloadType := dsse.SignaturePayloadType
	want := []byte(digest)
	if env.PayloadType != payloadType || string(env.Payload) != string(want) {
		return errors.New("verify: подпись bundle не соответствует digest (payload подменён)")
	}
	if err := dsse.Verify(keyVerify[0], env.PayloadType, env.Payload, env.Signature); err != nil {
		return fmt.Errorf("verify: подпись bundle недействительна: %w", err)
	}
	return nil
}

// ensureNoExtraneousFiles — bundle не должен содержать файлов вне index.json:
// любой лишний файл вне whitelisted records — повод для подозрения, отклоняется.
// Единственное исключение — dsse.json (DSSE-подпись bundle, P1-5): он пишется
// рядом с index.json и не входит в records.
func ensureNoExtraneousFiles(bundleDir string, index *Index) error {
	seen := make(map[string]bool, len(index.Records))
	artifactDirs := make([]string, 0)
	for _, record := range index.Records {
		rel := filepath.FromSlash(record.Path)
		seen[rel] = true
		if record.Type == RecordArtifact {
			if info, err := os.Stat(filepath.Join(bundleDir, rel)); err == nil && info.IsDir() {
				artifactDirs = append(artifactDirs, rel)
			}
		}
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
		if rel == indexFileName || rel == dsse.EnvelopeFileName || seen[rel] {
			return nil
		}
		for _, artifactDir := range artifactDirs {
			if strings.HasPrefix(rel, artifactDir+string(filepath.Separator)) {
				return nil
			}
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
	return verifyCore(runDir, manifest.RunID, records, true)
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
		data, attempt, readErr := evidence.ReadAttemptManifest(nil, runDir, manifest.RunID, id)
		if readErr != nil {
			return nil, readErr
		}
		sum := sha256Bytes(data)
		records = append(records, Record{Type: RecordAttemptManifest, Path: rel, SHA256: sum})
		for _, group := range []struct {
			area      string
			artifacts []evidence.ArtifactRecord
		}{
			{area: "inputs", artifacts: attempt.Inputs},
			{area: "artifacts", artifacts: attempt.Outputs},
		} {
			for _, artifact := range group.artifacts {
				path, pathErr := safeArtifactPath(artifact.EvidencePath, id, group.area)
				if pathErr != nil {
					return nil, fmt.Errorf("verify: unsafe attempt artifact path %q", artifact.EvidencePath)
				}
				_, _, artifactSHA, artifactErr := evidence.ArtifactDigest(filepath.Join(runDir, filepath.FromSlash(path)))
				if artifactErr != nil {
					return nil, fmt.Errorf("verify: attempt %s artifact %s: %w", id, path, artifactErr)
				}
				records = append(records, Record{Type: RecordArtifact, Path: path, SHA256: artifactSHA})
			}
		}
	}
	if receipt, found, err := readContainmentReceipt(runDir, manifest, true); err != nil {
		return nil, fmt.Errorf("verify: containment receipt: %w", err)
	} else if found {
		data, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			return nil, err
		}
		records = append(records, Record{Type: RecordContainment, Path: "containment.json", SHA256: sha256Bytes(append(data, '\n'))})
	}
	if record, found, err := readDeliveryRecord(runDir, manifest, true); err != nil {
		return nil, fmt.Errorf("verify: delivery record: %w", err)
	} else if found {
		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return nil, err
		}
		records = append(records, Record{Type: RecordDelivery, Path: "delivery.json", SHA256: sha256Bytes(append(data, '\n'))})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	return records, nil
}

// verifyCore — semantic-свёртка evidence против каталога с run layout:
// 1) run manifest identity; 2) hash-цепочка + anchor; 3) attempt manifests
// ↔ events; 4) attestation v1 ↔ run/spec/events/attempts/provenance.
func verifyCore(root, runID string, records []Record, liveRun bool) error {
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

	var anchorErr error
	if liveRun {
		anchorErr = evidence.VerifyAnchor(root)
	} else {
		anchorErr = evidence.VerifyBundleAnchor(root,
			evidence.NewFileEventLog(filepath.Join(root, "events.jsonl")),
			evidence.FilesystemAttemptManifestSource(), manifest.TargetDir)
	}
	if anchorErr != nil {
		return fmt.Errorf("verify: anchor: %w", anchorErr)
	}
	var eventBytes []byte
	if liveRun {
		eventBytes, err = evidence.ReadEventLogBytesForRunDir(root, runID)
	} else {
		eventBytes, err = safeio.ReadRegularFile(filepath.Join(root, "events.jsonl"), maxEventLogSize)
	}
	if err != nil {
		return fmt.Errorf("verify: event log: %w", err)
	}
	events, err := evidence.VerifyEventLogBytes(eventBytes, runID)
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
	if err := verifyAttemptArtifacts(root, runID, records, liveRun); err != nil {
		return err
	}
	if err := verifyContainment(root, manifest, records, liveRun); err != nil {
		return err
	}
	if err := verifyDelivery(root, manifest, events, records, liveRun); err != nil {
		return err
	}

	var attestationData []byte
	if liveRun && filepath.Base(filepath.Dir(root)) == "runs" && filepath.Base(filepath.Dir(filepath.Dir(root))) == ".ai-team" {
		attestationData, err = runAttestationData(root, runID)
	} else {
		attestationData, err = safeio.ReadRegularFile(filepath.Join(root, "attestation.json"), maxAttestationSize)
	}
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
	if predicate.Run.ControllerExecutableSHA != manifest.Controller.ExecutableSHA256 {
		return errors.New("verify: attestation controller_executable_sha256 does not match run manifest")
	}
	if predicate.Provenance == nil || predicate.Provenance.SchemaVersion != provenance.SchemaVersion {
		return fmt.Errorf("verify: attestation должна нести provenance manifest v1 (V0-2)")
	}
	if predicate.Provenance.RunID != runID {
		return fmt.Errorf("verify: attestation provenance run_id не совпадает")
	}
	var manifestProvenance provenance.Manifest
	provenanceDecoder := json.NewDecoder(bytes.NewReader(manifest.Provenance))
	provenanceDecoder.DisallowUnknownFields()
	if err := provenanceDecoder.Decode(&manifestProvenance); err != nil {
		return fmt.Errorf("verify: run manifest provenance: %w", err)
	}
	attestedProvenanceBytes, err := json.Marshal(predicate.Provenance)
	if err != nil {
		return fmt.Errorf("verify: marshal attestation provenance: %w", err)
	}
	manifestProvenanceBytes, err := json.Marshal(manifestProvenance)
	if err != nil {
		return fmt.Errorf("verify: marshal run manifest provenance: %w", err)
	}
	if !bytes.Equal(attestedProvenanceBytes, manifestProvenanceBytes) {
		return errors.New("verify: attestation provenance does not match run manifest")
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
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, "/\\\x00")
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
