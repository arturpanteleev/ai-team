package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// runverify.go (QS-05/QS-23/QS-24) — проверки, применимые только к live run
// evidence: архивные артефакты попыток, terminal delivery record, containment
// receipt и candidate identity. В portable bundle этих файлов нет по
// построению (bundle несёт typed records без raw-артефактов), поэтому
// verifyCore вызывает их только для каталога run.

const (
	maxContainmentSize = 64 << 10
	maxCandidateSize   = 8 << 20
)

// verifyAttemptEvidence сверяет один attempt manifest с файлами на диске:
// каждый record inputs/outputs обязан указывать на существующий артефакт с тем
// же типом, размером и sha256, а внутри attempts/<id> не должно быть файлов,
// не покрытых ни одним record. Манифест к этому моменту уже сверен с
// manifest_sha256 из цепочки событий, поэтому его записи — доверенный эталон.
func verifyAttemptEvidence(root, manifestRel string) error {
	attemptID := filepath.Base(filepath.Dir(filepath.FromSlash(manifestRel)))
	data, err := safeio.ReadRegularFile(filepath.Join(root, filepath.FromSlash(manifestRel)), maxAttemptManifest)
	if err != nil {
		return fmt.Errorf("verify: attempt %s manifest: %w", attemptID, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest evidence.AttemptManifest
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("verify: attempt %s manifest decode: %w", attemptID, err)
	}
	if manifest.AttemptID != attemptID {
		return fmt.Errorf("verify: attempt %s manifest объявляет attempt_id %q", attemptID, manifest.AttemptID)
	}
	if manifest.SchemaVersion != evidence.SchemaVersion {
		return fmt.Errorf("verify: attempt %s manifest schema %d не поддерживается", attemptID, manifest.SchemaVersion)
	}

	files := map[string]bool{}
	dirs := []string{}
	records := make([]evidence.ArtifactRecord, 0, len(manifest.Inputs)+len(manifest.Outputs))
	records = append(records, manifest.Inputs...)
	records = append(records, manifest.Outputs...)
	for _, record := range records {
		rel, err := attemptArtifactPath(attemptID, record.EvidencePath)
		if err != nil {
			return fmt.Errorf("verify: attempt %s artifact %s: %w", attemptID, record.Name, err)
		}
		artifactType, size, digest, digestErr := evidence.ArtifactDigest(filepath.Join(root, rel))
		if digestErr != nil {
			return fmt.Errorf("verify: attempt %s artifact %s (%s) недоступен: %w",
				attemptID, record.Name, record.EvidencePath, digestErr)
		}
		if artifactType != record.Type || size != record.Size || digest != record.SHA256 {
			return fmt.Errorf("verify: attempt %s artifact %s (%s) не совпадает с манифестом "+
				"(тип %s/%s, размер %d/%d, sha256 %s/%s) — evidence подменён",
				attemptID, record.Name, record.EvidencePath,
				artifactType, record.Type, size, record.Size, digest, record.SHA256)
		}
		switch artifactType {
		case "directory":
			dirs = append(dirs, filepath.ToSlash(rel))
		default:
			files[filepath.ToSlash(rel)] = true
		}
	}
	return ensureAttemptFilesCovered(root, attemptID, files, dirs)
}

// attemptArtifactPath проверяет, что evidence_path указывает внутрь каталога
// именно этой попытки (записи манифеста — недоверенный ввод для файловых
// операций, даже будучи сверенными с цепочкой).
func attemptArtifactPath(attemptID, evidencePath string) (string, error) {
	rel := filepath.FromSlash(strings.TrimSpace(evidencePath))
	if err := safePath(rel); err != nil {
		return "", err
	}
	clean := filepath.Clean(rel)
	prefix := filepath.Join("attempts", attemptID) + string(filepath.Separator)
	if !strings.HasPrefix(clean, prefix) {
		return "", fmt.Errorf("evidence_path %q вне каталога попытки", evidencePath)
	}
	return clean, nil
}

// ensureAttemptFilesCovered требует, чтобы каждый файл внутри attempts/<id>
// был покрыт record'ом манифеста: иначе подброшенный в evidence файл остаётся
// незамеченным ровно так же, как раньше оставалась незамеченной подмена.
func ensureAttemptFilesCovered(root, attemptID string, files map[string]bool, dirs []string) error {
	attemptDir := filepath.Join(root, "attempts", attemptID)
	return filepath.WalkDir(attemptDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		slashRel := filepath.ToSlash(rel)
		if slashRel == filepath.ToSlash(filepath.Join("attempts", attemptID, "manifest.json")) {
			return nil
		}
		if files[slashRel] {
			return nil
		}
		for _, dir := range dirs {
			if strings.HasPrefix(slashRel, dir+"/") {
				return nil
			}
		}
		return fmt.Errorf("verify: attempt %s содержит файл %q, не покрытый манифестом", attemptID, slashRel)
	})
}

// verifyDeliveryRecord проверяет terminal delivery record (QS-23). Проверяемо
// локально: self-integrity digest (обязателен), run identity, plan_hash против
// delivery_deferred события в hash-цепочке и attestation_sha256 против
// фактического attestation.json. Сам факт коммита/PR (commit_sha, pr_url) —
// внешнее утверждение о состоянии git-remote: локальная evidence его
// подтвердить не может, и verify этого не заявляет.
func verifyDeliveryRecord(root, runID string, manifest *evidence.RunManifest,
	events []evidence.Event, statement *attest.Statement) error {
	record, present, err := delivery.ReadTerminalRecord(root)
	if err != nil {
		return fmt.Errorf("verify: delivery record: %w", err)
	}
	if !present {
		return nil
	}
	if record.RunID != runID {
		return fmt.Errorf("verify: delivery record объявляет run_id %q вместо %q", record.RunID, runID)
	}
	deferredPlan := ""
	for _, event := range events {
		if event.Type != "delivery_deferred" {
			continue
		}
		if hash, ok := event.Data["plan_hash"].(string); ok && hash != "" {
			deferredPlan = hash
			break
		}
	}
	if deferredPlan == "" {
		return errors.New("verify: delivery record есть, а delivery_deferred события в цепочке нет")
	}
	if record.PlanHash != deferredPlan {
		return fmt.Errorf("verify: delivery record plan_hash %s не совпадает с delivery_deferred событием %s",
			record.PlanHash, deferredPlan)
	}
	if record.AttestationSHA256 != "" {
		// Digest берётся от canonical-сериализации statement (ровно так его
		// считал контроллер), а не от байтов файла на диске.
		digest, err := attest.Digest(statement)
		if err != nil {
			return fmt.Errorf("verify: delivery record attestation: %w", err)
		}
		if record.AttestationSHA256 != digest {
			return errors.New("verify: delivery record attestation_sha256 не совпадает с attestation.json")
		}
	}
	if record.RuntimeIdentity != "" {
		// runtime identity = sha256 raw provenance-байт run.json, а run.json
		// теперь зафиксирован anchor'ом: это привязка record к цепочке.
		if len(manifest.Provenance) == 0 {
			return errors.New("verify: delivery record объявляет runtime_identity, а provenance в run.json отсутствует")
		}
		if sha256Bytes(manifest.Provenance) != record.RuntimeIdentity {
			return errors.New("verify: delivery record runtime_identity не совпадает с provenance в run.json")
		}
	}
	return nil
}

// verifyContainmentReceipt проверяет containment receipt (QS-24). Receipt
// детерминированно выводится из профиля исполнения, поэтому verify
// пересчитывает канонический receipt и сравнивает: любой переворот уровня или
// флага ловится. Имя самого профиля привязать к цепочке нечем — receipt
// пишется после terminal anchor, — и verify этого не заявляет.
func verifyContainmentReceipt(root string) error {
	data, err := safeio.ReadRegularFile(filepath.Join(root, "containment.json"), maxContainmentSize)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // legacy run без receipt — UNAVAILABLE, а не ошибка
	}
	if err != nil {
		return fmt.Errorf("verify: containment receipt: %w", err)
	}
	var receipt containment.Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return fmt.Errorf("verify: containment receipt: %w", err)
	}
	if !receipt.Equal(containment.CanonicalReceipt(receipt.Profile)) {
		return fmt.Errorf("verify: containment receipt не совпадает с каноническим для профиля %q — receipt подменён",
			receipt.Profile)
	}
	return nil
}

// candidateEvidenceDocument — минимальная проекция {RunDir}/candidate.json,
// нужная для перекрёстной сверки с attestation subject.
type candidateEvidenceDocument struct {
	RunID           string `json:"run_id"`
	WorkspaceSHA256 string `json:"workspace_sha256"`
}

// verifyCandidateIdentity сверяет candidate.json с candidate subject
// attestation'а. Это перекрёстная согласованность двух файлов, а не привязка к
// hash-цепочке: candidate.json переписывается после каждой попытки и
// digest'ом в цепочке не фиксируется. Частичная подделка (правка одного файла
// из двух) ловится, согласованная — нет, и verify этого не заявляет.
func verifyCandidateIdentity(root, runID string, statement *attest.Statement) error {
	data, err := safeio.ReadRegularFile(filepath.Join(root, "candidate.json"), maxCandidateSize)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // run вне Git — candidate evidence не создаётся
	}
	if err != nil {
		return fmt.Errorf("verify: candidate evidence: %w", err)
	}
	var document candidateEvidenceDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("verify: candidate evidence decode: %w", err)
	}
	if document.RunID != runID {
		return fmt.Errorf("verify: candidate evidence объявляет run_id %q вместо %q", document.RunID, runID)
	}
	for _, subject := range statement.Subject {
		if subject.Name != "candidate" {
			continue
		}
		if digest := subject.Digest["sha256"]; digest != "" && digest != document.WorkspaceSHA256 {
			return fmt.Errorf("verify: candidate.json workspace_sha256 не совпадает с candidate subject attestation'а")
		}
	}
	return nil
}

// verifyRunDirectory — полный набор проверок, требующих файлов live run:
// артефакты попыток, delivery record, containment receipt, candidate identity.
func verifyRunDirectory(root, runID string, manifest *evidence.RunManifest, records []Record,
	events []evidence.Event, statement *attest.Statement) error {
	attemptManifests := make([]string, 0, len(records))
	for _, record := range records {
		if record.Type == RecordAttemptManifest {
			attemptManifests = append(attemptManifests, record.Path)
		}
	}
	sort.Strings(attemptManifests)
	for _, rel := range attemptManifests {
		if err := verifyAttemptEvidence(root, rel); err != nil {
			return err
		}
	}
	if err := verifyDeliveryRecord(root, runID, manifest, events, statement); err != nil {
		return err
	}
	if err := verifyContainmentReceipt(root); err != nil {
		return err
	}
	return verifyCandidateIdentity(root, runID, statement)
}
