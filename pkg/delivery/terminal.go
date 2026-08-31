package delivery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// terminal.go (V0-9): post-terminal delivery record — фиксирует результат
// отложенного (deferred) commit/push/PR. Пишется контроллером в
// {RunDir}/delivery.json ПОСЛЕ terminal finalize, когда attestation digest и
// runtime identity уже детерминированы; эти же значения попадают в commit
// trailer'ы. Attempt manifest внутри run не меняется (immutable); delivery.json
// — канонический источник результата доставки для FindDelivered/export.

const (
	TerminalRecordSchemaVersion = 1
	deliveryRecordMaxSize       = 1 << 20
)

// TerminalRecord — каноническая запись отложенной доставки run.
type TerminalRecord struct {
	SchemaVersion     int       `json:"schema_version"`
	RunID             string    `json:"run_id"`
	Feature           string    `json:"feature"`
	PlanHash          string    `json:"plan_hash"`
	CommitSHA         string    `json:"commit_sha,omitempty"`
	PRURL             string    `json:"pr_url,omitempty"`
	Trailers          []string  `json:"trailers,omitempty"`
	AttestationSHA256 string    `json:"attestation_sha256,omitempty"`
	RuntimeIdentity   string    `json:"runtime_identity,omitempty"`
	PerformedAt       time.Time `json:"performed_at"`
	// RecordSHA256 — self-integrity digest (sha256 canonical bytes record без
	// этого поля). Делает record tamper-evident: любой ad-hoc правдоподобный
	// edit фиксируется при повторном чтении.
	RecordSHA256 string `json:"record_sha256,omitempty"`
}

// selfDigest вычисляет sha256 canonical-байт record без поля record_sha256.
// Десериализованный time.Time с идентичным UTC-временем мёршится детерминированно.
func (r TerminalRecord) selfDigest() (string, error) {
	r.RecordSHA256 = ""
	data, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (r TerminalRecord) Validate() error {
	if r.SchemaVersion != TerminalRecordSchemaVersion {
		return fmt.Errorf("terminal delivery record schema %d не поддерживается", r.SchemaVersion)
	}
	if r.RunID == "" || r.Feature == "" {
		return errors.New("terminal delivery record: пустые run_id/feature")
	}
	if r.PlanHash == "" || !hex64(r.PlanHash) {
		return fmt.Errorf("terminal delivery record: некорректный plan_hash %q", r.PlanHash)
	}
	if err := ValidateTrailers(r.Trailers); err != nil {
		return err
	}
	if r.CommitSHA != "" && !gitHashPattern.MatchString(r.CommitSHA) {
		return fmt.Errorf("terminal delivery record: некорректный commit_sha %q", r.CommitSHA)
	}
	if r.CommitSHA == "" && r.PRURL == "" {
		return errors.New("terminal delivery record: пустые commit_sha и pr_url")
	}
	for _, trailer := range r.Trailers {
		key, value, ok := strings.Cut(trailer, ": ")
		if !ok {
			continue
		}
		switch key {
		case TrailerRunID:
			if value != r.RunID {
				return fmt.Errorf("terminal delivery record: trailer %q не согласован с run_id", trailer)
			}
		case TrailerRuntime:
			if r.RuntimeIdentity != "" && value != r.RuntimeIdentity {
				return fmt.Errorf("terminal delivery record: trailer %q не согласован с runtime_identity", trailer)
			}
		case TrailerAttestation:
			if r.AttestationSHA256 != "" && value != r.AttestationSHA256 {
				return fmt.Errorf("terminal delivery record: trailer %q не согласован с attestation_sha256", trailer)
			}
		}
	}
	if r.RecordSHA256 != "" {
		got, err := r.selfDigest()
		if err != nil {
			return err
		}
		if got != r.RecordSHA256 {
			return fmt.Errorf("terminal delivery record: record_sha256 mismatch (%s != %s) — record изменён", got, r.RecordSHA256)
		}
	}
	return nil
}

func hex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' {
			continue
		}
		return false
	}
	return true
}

// WriteTerminalRecord сериализует canonical JSON record в {runDir}/delivery.json
// (temp + fsync + rename, как у anchor; с self-integrity record_sha256).
// Перезапись уже существующего осмысленного record — ошибка (запись однократная).
func WriteTerminalRecord(runDir string, record TerminalRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	finalPath := filepath.Join(runDir, "delivery.json")
	if existing, ok, err := ReadTerminalRecord(runDir); err != nil {
		return err
	} else if ok && existing.CommitSHA == record.CommitSHA && existing.PlanHash == record.PlanHash {
		return nil // идемпотентный повтор (retry) с тем же результатом
	} else if ok {
		return fmt.Errorf("terminal delivery record уже записан (%s): перезапись запрещена", existing.CommitSHA)
	}
	digest, err := record.selfDigest()
	if err != nil {
		return err
	}
	record.RecordSHA256 = digest
	if err := record.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmpFile, err := os.CreateTemp(runDir, ".tmp-delivery-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, finalPath)
}

// ReadTerminalRecord читает и строго валидирует record (fail-closed).
func ReadTerminalRecord(runDir string) (*TerminalRecord, bool, error) {
	data, err := safeio.ReadRegularFile(filepath.Join(runDir, "delivery.json"), deliveryRecordMaxSize)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record TerminalRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, false, fmt.Errorf("terminal delivery record decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, false, errors.New("terminal delivery record: trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("terminal delivery record: trailing data: %w", err)
	}
	if err := record.Validate(); err != nil {
		return nil, false, err
	}
	return &record, true, nil
}

// ReadTerminalRecordFile — то же, но для явного пути записи (используется
// export/verify-контрактом; путь обязан быть в {runDir}/delivery.json).
func ReadTerminalRecordFile(path string) (*TerminalRecord, bool, error) {
	if filepath.Base(path) != "delivery.json" {
		return nil, false, fmt.Errorf("terminal delivery record: неожиданное имя %q", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record TerminalRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, false, fmt.Errorf("terminal delivery record decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, false, errors.New("terminal delivery record: trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("terminal delivery record: trailing data: %w", err)
	}
	if err := record.Validate(); err != nil {
		return nil, false, err
	}
	return &record, true, nil
}
