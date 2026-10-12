package delivery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const stageDeliveryReceiptSchemaVersion = 1

// StageDeliveryRequest contains only identities that the controller verifies
// against its pinned event log, attempt manifest, run manifest and approval
// store. It intentionally has no path, plan body, URL, or effect parameters.
type StageDeliveryRequest struct {
	RunID                 string `json:"run_id"`
	AttemptID             string `json:"attempt_id"`
	AttemptManifestSHA256 string `json:"attempt_manifest_sha256"`
	PlanHash              string `json:"plan_hash"`
	RuntimeIdentity       string `json:"runtime_identity"`
}

// StageDeliveryReceipt records a controller-observed delivery result for one
// immutable attempt and approved plan. Terminal whole-run attestation remains
// separate and is never written into this attempt's manifest.
type StageDeliveryReceipt struct {
	SchemaVersion         int       `json:"schema_version"`
	RunID                 string    `json:"run_id"`
	AttemptID             string    `json:"attempt_id"`
	AttemptManifestSHA256 string    `json:"attempt_manifest_sha256"`
	PlanHash              string    `json:"plan_hash"`
	RuntimeIdentity       string    `json:"runtime_identity"`
	ApprovalIdentity      string    `json:"approval_identity"`
	Feature               string    `json:"feature"`
	CommitSHA             string    `json:"commit_sha,omitempty"`
	PRURL                 string    `json:"pr_url"`
	PerformedAt           time.Time `json:"performed_at"`
	ReceiptSHA256         string    `json:"receipt_sha256,omitempty"`
}

func (r StageDeliveryReceipt) selfDigest() (string, error) {
	r.ReceiptSHA256 = ""
	data, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (r StageDeliveryReceipt) Validate() error {
	if r.SchemaVersion != stageDeliveryReceiptSchemaVersion {
		return fmt.Errorf("stage delivery receipt schema %d is unsupported", r.SchemaVersion)
	}
	if err := validateTerminalRunID(r.RunID); err != nil {
		return err
	}
	if !safeStageAttemptID(r.AttemptID) || !hex64(r.AttemptManifestSHA256) || !hex64(r.PlanHash) || !hex64(r.RuntimeIdentity) {
		return errors.New("stage delivery receipt has invalid attempt or digest identity")
	}
	if strings.TrimSpace(r.ApprovalIdentity) == "" || strings.TrimSpace(r.Feature) == "" || r.PerformedAt.IsZero() {
		return errors.New("stage delivery receipt is missing approval, feature or timestamp")
	}
	if r.CommitSHA != "" && !gitHashPattern.MatchString(r.CommitSHA) {
		return fmt.Errorf("stage delivery receipt has invalid commit SHA %q", r.CommitSHA)
	}
	parsed, err := url.Parse(strings.TrimSpace(r.PRURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("stage delivery receipt has invalid PR URL %q", r.PRURL)
	}
	got, err := r.selfDigest()
	if err != nil {
		return err
	}
	if r.ReceiptSHA256 == "" || r.ReceiptSHA256 != got {
		return errors.New("stage delivery receipt digest mismatch")
	}
	return nil
}

func safeStageAttemptID(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value &&
		filepath.Clean(value) == value && !strings.ContainsAny(value, "/\\\x00")
}

// SealStageDeliveryReceipt returns the immutable receipt with its self-digest.
func SealStageDeliveryReceipt(receipt StageDeliveryReceipt) (StageDeliveryReceipt, error) {
	receipt.SchemaVersion = stageDeliveryReceiptSchemaVersion
	digest, err := receipt.selfDigest()
	if err != nil {
		return StageDeliveryReceipt{}, err
	}
	receipt.ReceiptSHA256 = digest
	if err := receipt.Validate(); err != nil {
		return StageDeliveryReceipt{}, err
	}
	return receipt, nil
}

func stageReceiptPath(targetDir string, request StageDeliveryRequest) (string, error) {
	if err := validateTerminalRunID(request.RunID); err != nil {
		return "", err
	}
	if !safeStageAttemptID(request.AttemptID) || !hex64(request.AttemptManifestSHA256) ||
		!hex64(request.PlanHash) || !hex64(request.RuntimeIdentity) {
		return "", errors.New("stage delivery receipt key is invalid")
	}
	keyMaterial := strings.Join([]string{request.RunID, request.AttemptID, request.AttemptManifestSHA256, request.PlanHash, request.RuntimeIdentity}, "\x00")
	key := sha256.Sum256([]byte(keyMaterial))
	return filepath.Join(targetDir, ".ai-team", "state", "stage-delivery-receipts", request.RunID, hex.EncodeToString(key[:])+".json"), nil
}

// ReadControllerStageDeliveryReceipt reads a controller-owned receipt for the
// exact request key. Worker event or attempt files are not receipt authority.
func ReadControllerStageDeliveryReceipt(targetDir string, request StageDeliveryRequest) (*StageDeliveryReceipt, bool, error) {
	path, err := stageReceiptPath(targetDir, request)
	if err != nil {
		return nil, false, err
	}
	data, err := safeio.ReadRegularFile(path, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt StageDeliveryReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return nil, false, fmt.Errorf("stage delivery receipt decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, false, errors.New("stage delivery receipt contains trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("stage delivery receipt trailing data: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return nil, false, err
	}
	if receipt.RunID != request.RunID || receipt.AttemptID != request.AttemptID ||
		receipt.AttemptManifestSHA256 != request.AttemptManifestSHA256 || receipt.PlanHash != request.PlanHash ||
		receipt.RuntimeIdentity != request.RuntimeIdentity {
		return nil, false, errors.New("stage delivery receipt identity differs from request")
	}
	return &receipt, true, nil
}

// WriteControllerStageDeliveryReceipt durably records a result observed by the
// trusted controller. Retrying the exact receipt is idempotent; a conflicting
// receipt for the same key is rejected.
func WriteControllerStageDeliveryReceipt(targetDir string, receipt StageDeliveryReceipt) error {
	sealed, err := SealStageDeliveryReceipt(receipt)
	if err != nil {
		return err
	}
	request := StageDeliveryRequest{
		RunID: sealed.RunID, AttemptID: sealed.AttemptID,
		AttemptManifestSHA256: sealed.AttemptManifestSHA256,
		PlanHash:              sealed.PlanHash, RuntimeIdentity: sealed.RuntimeIdentity,
	}
	path, err := stageReceiptPath(targetDir, request)
	if err != nil {
		return err
	}
	if existing, found, err := ReadControllerStageDeliveryReceipt(targetDir, request); err != nil {
		return err
	} else if found {
		if *existing == sealed {
			return nil
		}
		return errors.New("conflicting controller stage delivery receipt already exists")
	}
	if err := safeio.EnsureDirPath(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".stage-delivery-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
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
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			if existing, found, readErr := ReadControllerStageDeliveryReceipt(targetDir, request); readErr == nil && found && *existing == sealed {
				return syncStageReceiptDirectory(filepath.Dir(path))
			}
			return errors.New("conflicting controller stage delivery receipt already exists")
		}
		return err
	}
	return syncStageReceiptDirectory(filepath.Dir(path))
}

func syncStageReceiptDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(syncErr, closeErr)
	}
	return nil
}
