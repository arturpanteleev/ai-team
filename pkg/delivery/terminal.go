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
	TerminalRecordSchemaVersion            = 1
	deliveryRecordMaxSize                  = 1 << 20
	controllerDeliveryReceiptSchemaVersion = 1
)

// controllerDeliveryReceipt is a durable proof that this controller process
// observed the result of DeliveryService.Execute. It lives in a separate
// sandbox-hidden store because the worker API can submit TerminalRecord
// values, but cannot submit receipts.
type controllerDeliveryReceipt struct {
	SchemaVersion int            `json:"schema_version"`
	RunID         string         `json:"run_id"`
	Record        TerminalRecord `json:"record"`
	ReceiptSHA256 string         `json:"receipt_sha256,omitempty"`
}

func (r controllerDeliveryReceipt) selfDigest() (string, error) {
	r.ReceiptSHA256 = ""
	data, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (r controllerDeliveryReceipt) validate() error {
	if r.SchemaVersion != controllerDeliveryReceiptSchemaVersion || r.RunID == "" || r.Record.RunID != r.RunID {
		return errors.New("controller delivery receipt identity/schema mismatch")
	}
	if err := r.Record.Validate(); err != nil {
		return fmt.Errorf("controller delivery receipt record: %w", err)
	}
	recordDigest, err := r.Record.selfDigest()
	if err != nil {
		return err
	}
	if r.Record.RecordSHA256 != recordDigest {
		return errors.New("controller delivery receipt record digest mismatch")
	}
	got, err := r.selfDigest()
	if err != nil {
		return err
	}
	if r.ReceiptSHA256 == "" || r.ReceiptSHA256 != got {
		return errors.New("controller delivery receipt digest mismatch")
	}
	return nil
}

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
	} else if ok && sameTerminalRecord(*existing, record) {
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

// WriteControllerTerminalRecord stores a worker-submitted record outside the
// run evidence directory. The caller must bind runID to an authenticated
// per-run controller API scope; record contents remain worker-supplied.
func WriteControllerTerminalRecord(targetDir, runID string, record TerminalRecord) error {
	if err := validateTerminalRunID(runID); err != nil {
		return fmt.Errorf("terminal delivery run id: %w", err)
	}
	if record.RunID != runID {
		return fmt.Errorf("terminal delivery record run mismatch: scope=%q record=%q", runID, record.RunID)
	}
	if err := record.Validate(); err != nil {
		return err
	}
	dir, err := safeio.EnsureDir(targetDir, ".ai-team", "state", "delivery")
	if err != nil {
		return err
	}
	finalPath := filepath.Join(dir, runID+".json")
	if existing, ok, err := ReadTerminalRecordAtPath(finalPath); err != nil {
		return err
	} else if ok {
		if sameTerminalRecord(*existing, record) {
			return nil
		}
		return fmt.Errorf("terminal delivery record already exists for run %s; conflicting overwrite rejected", runID)
	}
	digest, err := record.selfDigest()
	if err != nil {
		return err
	}
	record.RecordSHA256 = digest
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".tmp-delivery-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpPath, 0600); err != nil {
		return err
	}
	if err = os.Link(tmpPath, finalPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, ok, readErr := ReadTerminalRecordAtPath(finalPath)
			if readErr != nil {
				return readErr
			}
			if ok && sameTerminalRecord(*existing, record) {
				return nil
			}
			return fmt.Errorf("terminal delivery record already exists for run %s; conflicting overwrite rejected", runID)
		}
		return err
	}
	if dirFile, openErr := os.Open(dir); openErr == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

// ReadControllerTerminalRecord reads the controller-owned record. Missing is
// distinct from corruption so callers can support the legacy runDir file.
func ReadControllerTerminalRecord(targetDir, runID string) (*TerminalRecord, bool, error) {
	if err := validateTerminalRunID(runID); err != nil {
		return nil, false, fmt.Errorf("terminal delivery run id: %w", err)
	}
	path := filepath.Join(targetDir, ".ai-team", "state", "delivery", runID+".json")
	record, ok, err := ReadTerminalRecordAtPath(path)
	if err != nil || !ok {
		return record, ok, err
	}
	if record.RunID != runID {
		return nil, false, fmt.Errorf("terminal delivery record identity mismatch: requested=%q stored=%q", runID, record.RunID)
	}
	return record, true, nil
}

// WriteControllerDeliveryReceipt records the exact TerminalRecord produced
// after a successful trusted DeliveryService.Execute. The separate receipt
// store has no worker API writer and is hidden by the Linux worker sandbox.
func WriteControllerDeliveryReceipt(targetDir string, record TerminalRecord) error {
	return writeControllerDeliveryReceipt(targetDir, record, syncControllerDeliveryReceiptDirectory)
}

func writeControllerDeliveryReceipt(targetDir string, record TerminalRecord, syncDirectory func(string) error) error {
	if syncDirectory == nil {
		return errors.New("controller delivery receipt directory sync is required")
	}
	if err := validateTerminalRunID(record.RunID); err != nil {
		return fmt.Errorf("controller delivery receipt run id: %w", err)
	}
	if err := record.Validate(); err != nil {
		return err
	}
	recordDigest, err := record.selfDigest()
	if err != nil {
		return err
	}
	record.RecordSHA256 = recordDigest
	receipt := controllerDeliveryReceipt{
		SchemaVersion: controllerDeliveryReceiptSchemaVersion,
		RunID:         record.RunID,
		Record:        record,
	}
	receipt.ReceiptSHA256, err = receipt.selfDigest()
	if err != nil {
		return err
	}
	if err := receipt.validate(); err != nil {
		return err
	}
	dir, err := safeio.EnsureDir(targetDir, ".ai-team", "state", "delivery-receipts")
	if err != nil {
		return err
	}
	finalPath := filepath.Join(dir, record.RunID+".json")
	if existing, found, readErr := readControllerDeliveryReceiptAtPath(finalPath); readErr != nil {
		return readErr
	} else if found {
		if sameControllerDeliveryReceipt(*existing, receipt) {
			return syncDirectory(dir)
		}
		return fmt.Errorf("controller delivery receipt already exists for run %s; conflicting overwrite rejected", record.RunID)
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".tmp-delivery-receipt-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpPath, 0600); err != nil {
		return err
	}
	if err = os.Link(tmpPath, finalPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, found, readErr := readControllerDeliveryReceiptAtPath(finalPath)
			if readErr != nil {
				return readErr
			}
			if found && sameControllerDeliveryReceipt(*existing, receipt) {
				return syncDirectory(dir)
			}
			return fmt.Errorf("controller delivery receipt already exists for run %s; conflicting overwrite rejected", record.RunID)
		}
		return err
	}
	return syncDirectory(dir)
}

func syncControllerDeliveryReceiptDirectory(dir string) error {
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open controller delivery receipt directory for sync: %w", err)
	}
	syncErr := dirFile.Sync()
	closeErr := dirFile.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(
			wrapOptionalError("sync controller delivery receipt directory", syncErr),
			wrapOptionalError("close controller delivery receipt directory", closeErr),
		)
	}
	return nil
}

func wrapOptionalError(context string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", context, err)
}

// ReadControllerDeliveryReceipt reads only the controller-origin retry proof.
// Legacy run-local records and worker-submitted controller records are not
// accepted as evidence that Execute completed.
func ReadControllerDeliveryReceipt(targetDir, runID string) (*TerminalRecord, bool, error) {
	if err := validateTerminalRunID(runID); err != nil {
		return nil, false, fmt.Errorf("controller delivery receipt run id: %w", err)
	}
	path := filepath.Join(targetDir, ".ai-team", "state", "delivery-receipts", runID+".json")
	receipt, found, err := readControllerDeliveryReceiptAtPath(path)
	if err != nil || !found {
		return nil, found, err
	}
	if receipt.RunID != runID {
		return nil, false, fmt.Errorf("controller delivery receipt identity mismatch: requested=%q stored=%q", runID, receipt.RunID)
	}
	return &receipt.Record, true, nil
}

func readControllerDeliveryReceiptAtPath(path string) (*controllerDeliveryReceipt, bool, error) {
	data, err := safeio.ReadRegularFile(path, 2*deliveryRecordMaxSize)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt controllerDeliveryReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return nil, false, fmt.Errorf("controller delivery receipt decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, false, errors.New("controller delivery receipt: trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("controller delivery receipt: trailing data: %w", err)
	}
	if err := receipt.validate(); err != nil {
		return nil, false, err
	}
	return &receipt, true, nil
}

func sameControllerDeliveryReceipt(a, b controllerDeliveryReceipt) bool {
	return a.RunID == b.RunID && a.ReceiptSHA256 != "" && a.ReceiptSHA256 == b.ReceiptSHA256
}

// ReadTerminalRecordForRun prefers the controller-origin receipt. If no receipt
// exists, it preserves compatibility with worker API records and the historical
// evidence-local delivery.json for older consumers.
func ReadTerminalRecordForRun(targetDir, runDir, runID string) (*TerminalRecord, bool, error) {
	record, ok, err := ReadControllerDeliveryReceipt(targetDir, runID)
	if err != nil || ok {
		return record, ok, err
	}
	record, ok, err = ReadControllerTerminalRecord(targetDir, runID)
	if err != nil || ok {
		return record, ok, err
	}
	localRecord, localOK, localErr := ReadTerminalRecord(runDir)
	if localErr != nil {
		return nil, false, localErr
	}
	if ok {
		if localOK && !sameTerminalRecord(*record, *localRecord) {
			return nil, false, fmt.Errorf("terminal delivery record conflict between controller state and run evidence for run %s", runID)
		}
		return record, true, nil
	}
	if !localOK {
		return nil, false, nil
	}
	if localRecord.RunID != runID {
		return nil, false, fmt.Errorf("legacy terminal delivery record identity mismatch: requested=%q stored=%q", runID, localRecord.RunID)
	}
	return localRecord, true, nil
}

func ReadTerminalRecordAtPath(path string) (*TerminalRecord, bool, error) {
	data, err := safeio.ReadRegularFile(path, deliveryRecordMaxSize)
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

func sameTerminalRecord(a, b TerminalRecord) bool {
	aDigest, aErr := a.selfDigest()
	bDigest, bErr := b.selfDigest()
	return aErr == nil && bErr == nil && aDigest == bDigest
}

func validateTerminalRunID(runID string) error {
	if runID == "" || runID == "." || runID == ".." || len(runID) > 255 || filepath.Base(runID) != runID || filepath.Clean(runID) != runID || strings.ContainsAny(runID, "/\\\x00") {
		return fmt.Errorf("invalid run id %q", runID)
	}
	for _, r := range runID {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid run id %q", runID)
		}
	}
	return nil
}
