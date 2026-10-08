package metrics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/runid"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// FileUsageEnvelopeStore stores controller-owned usage summaries outside the
// worker-visible run evidence directory.
type FileUsageEnvelopeStore struct{}

const usageReservationSchemaVersion = 1

type usageReservation struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	TargetDir     string    `json:"target_dir"`
	ReservedAt    time.Time `json:"reserved_at"`
}

func UsageEnvelopePath(target, runID string) (string, error) {
	if err := validateUsageRunID(runID); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(absolute), ".ai-team", "state", "usage", runID+".json"), nil
}

// PrepareUsageEnvelopeStore establishes the controller-owned location before
// launching a cloud worker, allowing readers to distinguish cloud state from
// legacy local evidence even if a run fails before writing its summary.
func PrepareUsageEnvelopeStore(target string) (string, error) {
	return safeio.EnsureDir(target, ".ai-team", "state", "usage")
}

// Reserve establishes controller authority for one bubblewrap run before its
// worker starts. The reservation is immutable and idempotent across recovery.
func (FileUsageEnvelopeStore) Reserve(target, runID string) error {
	if err := validateUsageRunID(runID); err != nil {
		return err
	}
	canonicalTarget, err := canonicalUsageTarget(target)
	if err != nil {
		return err
	}
	root, err := PrepareUsageEnvelopeStore(canonicalTarget)
	if err != nil {
		return err
	}
	reservation := usageReservation{SchemaVersion: usageReservationSchemaVersion, RunID: runID, TargetDir: canonicalTarget, ReservedAt: time.Now().UTC()}
	data, err := json.MarshalIndent(reservation, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(root, runID+".reserved.json")
	if err := safeio.RejectSymlink(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			_, checkErr := UsageEnvelopeReservation(target, runID)
			return checkErr
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	closeErr := f.Close()
	dir, openErr := os.Open(root)
	if openErr != nil {
		return errors.Join(closeErr, openErr)
	}
	return errors.Join(closeErr, syncAndCloseDirectory(dir))
}

// UsageEnvelopeReservation reports whether this exact target/run has a valid
// controller reservation. A corrupt present reservation is an error.
func UsageEnvelopeReservation(target, runID string) (bool, error) {
	path, err := UsageEnvelopePath(target, runID)
	if err != nil {
		return false, err
	}
	reservationPath := strings.TrimSuffix(path, ".json") + ".reserved.json"
	data, err := safeio.ReadRegularFile(reservationPath, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var reservation usageReservation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reservation); err != nil {
		return false, fmt.Errorf("corrupt usage reservation: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false, errors.New("corrupt usage reservation: trailing data")
	}
	canonicalTarget, err := canonicalUsageTarget(target)
	if err != nil {
		return false, err
	}
	if reservation.SchemaVersion != usageReservationSchemaVersion || reservation.RunID != runID || reservation.TargetDir != canonicalTarget || reservation.ReservedAt.IsZero() {
		return false, errors.New("corrupt usage reservation: schema or run/target identity mismatch")
	}
	return true, nil
}

// Write persists one immutable envelope. An exact retry is idempotent; a
// different envelope for the same run is rejected without replacing state.
func (FileUsageEnvelopeStore) Write(target, runID string, envelope UsageEnvelope) error {
	return writeUsageEnvelope(target, runID, envelope, false)
}

// Replace atomically advances the summary on a later resume/recovery
// invocation. A single invocation must still limit itself to one value.
func (FileUsageEnvelopeStore) Replace(target, runID string, envelope UsageEnvelope) error {
	return writeUsageEnvelope(target, runID, envelope, true)
}

func writeUsageEnvelope(target, runID string, envelope UsageEnvelope, allowReplace bool) error {
	if err := ValidateUsageEnvelope(runID, envelope); err != nil {
		return err
	}
	reserved, err := UsageEnvelopeReservation(target, runID)
	if err != nil {
		return err
	}
	if !reserved {
		return errors.New("usage envelope has no controller reservation")
	}
	root, err := PrepareUsageEnvelopeStore(target)
	if err != nil {
		return err
	}
	path := filepath.Join(root, runID+".json")
	if err := safeio.RejectSymlink(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".usage-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, path); err != nil {
		stored, readErr := ReadUsageEnvelope(target, runID)
		if readErr == nil {
			storedData, marshalErr := json.MarshalIndent(stored, "", "  ")
			if marshalErr == nil && bytes.Equal(append(storedData, '\n'), data) {
				return nil
			}
		}
		if allowReplace && readErr == nil {
			if err := os.Rename(tempPath, path); err != nil {
				return fmt.Errorf("replace usage envelope for resumed run: %w", err)
			}
			dir, err := os.Open(filepath.Dir(path))
			if err != nil {
				return err
			}
			return syncAndCloseDirectory(dir)
		}
		return fmt.Errorf("persist usage envelope without replacing existing run state: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return syncAndCloseDirectory(dir)
}

func syncAndCloseDirectory(dir *os.File) error {
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}

// ReadUsageEnvelope loads and validates the authoritative controller summary.
func ReadUsageEnvelope(target, runID string) (UsageEnvelope, error) {
	reserved, err := UsageEnvelopeReservation(target, runID)
	if err != nil {
		return UsageEnvelope{}, err
	}
	if !reserved {
		return UsageEnvelope{}, os.ErrNotExist
	}
	path, err := UsageEnvelopePath(target, runID)
	if err != nil {
		return UsageEnvelope{}, err
	}
	data, err := safeio.ReadRegularFile(path, 8<<20)
	if err != nil {
		return UsageEnvelope{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope UsageEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return UsageEnvelope{}, fmt.Errorf("corrupt usage envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return UsageEnvelope{}, errors.New("corrupt usage envelope: trailing data")
	}
	if err := ValidateUsageEnvelope(runID, envelope); err != nil {
		return UsageEnvelope{}, fmt.Errorf("corrupt usage envelope: %w", err)
	}
	return envelope, nil
}

func ValidateUsageEnvelope(runID string, envelope UsageEnvelope) error {
	if err := validateUsageRunID(runID); err != nil {
		return err
	}
	if envelope.SchemaVersion != SchemaVersion || envelope.RunID != runID {
		return errors.New("usage envelope schema or run identity mismatch")
	}
	if strings.TrimSpace(envelope.Feature) == "" || strings.TrimSpace(envelope.Outcome) == "" || envelope.StartedAt.IsZero() || envelope.FinishedAt.IsZero() || envelope.FinishedAt.Before(envelope.StartedAt) {
		return errors.New("usage envelope has invalid feature, outcome, or timestamps")
	}
	if envelope.TotalDurationMS < 0 || envelope.LoopbackCycles < 0 || envelope.TokensInput < 0 || envelope.TokensOutput < 0 || math.IsNaN(envelope.CostUSD) || math.IsInf(envelope.CostUSD, 0) || envelope.CostUSD < 0 {
		return errors.New("usage envelope contains invalid aggregate values")
	}
	if envelope.TokensUnknown && envelope.UsageReported {
		return errors.New("usage envelope token attestation flags conflict")
	}
	for _, stage := range envelope.Stages {
		if strings.TrimSpace(stage.Stage) == "" || stage.Attempts < 0 || stage.DurationMS < 0 {
			return errors.New("usage envelope contains an invalid stage")
		}
	}
	return nil
}

func validateUsageRunID(runID string) error {
	return runid.Validate(runID)
}

func canonicalUsageTarget(target string) (string, error) {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("usage target must be an existing directory")
	}
	return filepath.Clean(canonical), nil
}
