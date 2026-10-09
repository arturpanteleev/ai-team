package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
)

const attemptManifestReservationName = "reservation.json"

// ControllerAttemptManifestStore keeps canonical attempt records outside the
// worker-visible run tree. A reservation makes the controller copy
// authoritative for that run; absence is distinct from a damaged reservation.
type ControllerAttemptManifestStore struct{ TargetDir string }

type attemptManifestReservation struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
}

var attemptManifestWriteMu sync.Mutex

func (s ControllerAttemptManifestStore) root(create bool) (string, error) {
	if s.TargetDir == "" || !filepath.IsAbs(s.TargetDir) || filepath.Clean(s.TargetDir) != s.TargetDir {
		return "", errors.New("attempt manifest target must be absolute and clean")
	}
	if create {
		return safeio.EnsureDir(s.TargetDir, ".ai-team", "state", "attempt-manifests")
	}
	return safeio.ExistingDir(s.TargetDir, ".ai-team", "state", "attempt-manifests")
}

func (s ControllerAttemptManifestStore) runDir(runID string, create bool) (string, error) {
	if err := ValidateRunID(runID); err != nil {
		return "", err
	}
	root, err := s.root(create)
	if err != nil {
		return "", err
	}
	if create {
		return safeio.EnsureDir(root, runID)
	}
	return safeio.ExistingDir(root, runID)
}

// Reserve creates the durable authority marker before a worker is spawned.
// Calling it again is safe only for the same intact reservation.
func (s ControllerAttemptManifestStore) Reserve(runID string) error {
	dir, err := s.runDir(runID, true)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, attemptManifestReservationName)
	want, _ := json.Marshal(attemptManifestReservation{SchemaVersion: SchemaVersion, RunID: runID})
	if existing, readErr := safeio.ReadRegularFile(path, 1024); readErr == nil {
		if bytes.Equal(existing, want) {
			return nil
		}
		return errors.New("attempt manifest reservation conflicts with requested run")
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read attempt manifest reservation: %w", readErr)
	}
	if err := safeio.WriteRegularFileNoFollow(path, want, 0600); err != nil {
		return fmt.Errorf("reserve controller attempt manifests: %w", err)
	}
	return nil
}

// IsReserved reports false only when the store, run directory, or marker is
// absent. Any malformed or unsafe existing path is an error and must fail
// closed rather than selecting target-side manifests.
func (s ControllerAttemptManifestStore) IsReserved(runID string) (bool, error) {
	dir, err := s.runDir(runID, false)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data, err := safeio.ReadRegularFile(filepath.Join(dir, attemptManifestReservationName), 1024)
	if os.IsNotExist(err) {
		return false, errors.New("attempt manifest reservation marker is missing from an existing run store")
	}
	if err != nil {
		return false, fmt.Errorf("read attempt manifest reservation: %w", err)
	}
	var reservation attemptManifestReservation
	if err := strictjson.Unmarshal(data, 1024, &reservation); err != nil || reservation.SchemaVersion != SchemaVersion || reservation.RunID != runID {
		return false, errors.New("attempt manifest reservation is corrupt or has mismatched identity")
	}
	return true, nil
}

// Write persists one typed manifest. Exact retries are idempotent; any other
// write to the same identity is rejected.
func (s ControllerAttemptManifestStore) Write(runID string, manifest AttemptManifest) error {
	attemptManifestWriteMu.Lock()
	defer attemptManifestWriteMu.Unlock()
	reserved, err := s.IsReserved(runID)
	if err != nil || !reserved {
		if err == nil {
			err = errors.New("attempt manifest run is not reserved")
		}
		return err
	}
	if manifest.RunID != runID || manifest.SchemaVersion != SchemaVersion {
		return errors.New("attempt manifest run or schema identity mismatch")
	}
	if err := validateControllerAttemptManifest(manifest); err != nil {
		return err
	}
	dir, err := s.runDir(runID, false)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxAttemptManifestSize {
		return fmt.Errorf("attempt manifest exceeds maximum size of %d bytes", MaxAttemptManifestSize)
	}
	path := filepath.Join(dir, manifest.AttemptID+".json")
	if old, readErr := safeio.ReadRegularFile(path, MaxAttemptManifestSize); readErr == nil {
		if bytes.Equal(old, data) {
			return nil
		}
		return errors.New("conflicting attempt manifest overwrite rejected")
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read stored attempt manifest: %w", readErr)
	}
	if err := safeio.WriteRegularFileNoFollow(path, data, 0444); err != nil {
		return err
	}
	return nil
}

func (s ControllerAttemptManifestStore) ReadAttemptManifest(_ string, runID, attemptID string) ([]byte, error) {
	if err := validateAttemptManifestIdentity(runID, attemptID); err != nil {
		return nil, err
	}
	reserved, err := s.IsReserved(runID)
	if err != nil {
		return nil, err
	}
	if !reserved {
		return nil, errors.New("attempt manifest run is not reserved")
	}
	dir, err := s.runDir(runID, false)
	if err != nil {
		return nil, err
	}
	data, err := safeio.ReadRegularFile(filepath.Join(dir, attemptID+".json"), MaxAttemptManifestSize)
	if err != nil {
		return nil, err
	}
	var manifest AttemptManifest
	if err := strictjson.Unmarshal(data, MaxAttemptManifestSize, &manifest); err != nil {
		return nil, fmt.Errorf("stored attempt manifest is corrupt: %w", err)
	}
	if err := validateControllerAttemptManifest(manifest); err != nil || manifest.RunID != runID || manifest.AttemptID != attemptID {
		return nil, errors.New("stored attempt manifest identity/schema mismatch")
	}
	return data, nil
}

func validateControllerAttemptManifest(manifest AttemptManifest) error {
	if err := validateAttemptManifestIdentity(manifest.RunID, manifest.AttemptID); err != nil {
		return err
	}
	if manifest.SchemaVersion != SchemaVersion || strings.TrimSpace(manifest.Stage) == "" || manifest.StageIndex < 1 || manifest.StartedAt.IsZero() || manifest.FinishedAt.IsZero() {
		return errors.New("attempt manifest required schema or identity fields are invalid")
	}
	if manifest.Usage != nil && (manifest.Usage.TokensInput < 0 || manifest.Usage.TokensOutput < 0 ||
		(!manifest.Usage.Attested && (manifest.Usage.TokensInput != 0 || manifest.Usage.TokensOutput != 0))) {
		return errors.New("attempt manifest usage is invalid")
	}
	for _, records := range [][]ArtifactRecord{manifest.Inputs, manifest.Outputs} {
		for _, record := range records {
			rel := filepath.Clean(filepath.FromSlash(record.EvidencePath))
			if record.Name == "" || record.EvidencePath == "" || filepath.IsAbs(rel) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.ToSlash(rel) != record.EvidencePath {
				return fmt.Errorf("attempt manifest artifact path is invalid: %q", record.EvidencePath)
			}
			prefix := filepath.ToSlash(filepath.Join("attempts", manifest.AttemptID)) + "/"
			if !strings.HasPrefix(record.EvidencePath, prefix) {
				return fmt.Errorf("attempt manifest artifact path is outside attempt: %q", record.EvidencePath)
			}
		}
	}
	return nil
}

// ReservedAttemptManifestSource selects the controller store when a valid
// reservation exists and preserves the filesystem layout for legacy runs.
// A present but damaged marker returns an error and never falls back.
type ReservedAttemptManifestSource struct{ TargetDir string }

func (s ReservedAttemptManifestSource) ReadAttemptManifest(runDir, runID, attemptID string) ([]byte, error) {
	store := ControllerAttemptManifestStore(s)
	reserved, err := store.IsReserved(runID)
	if err != nil {
		return nil, err
	}
	if reserved {
		return store.ReadAttemptManifest(runDir, runID, attemptID)
	}
	return FilesystemAttemptManifestSource().ReadAttemptManifest(runDir, runID, attemptID)
}
