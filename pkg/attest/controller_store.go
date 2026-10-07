package attest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const controllerAttestationLimit = 1 << 20

// ControllerStore persists worker-supplied statements outside the worker-visible
// run evidence tree. It validates syntax and run identity, not statement truth.
type ControllerStore struct{ TargetDir string }

func (s ControllerStore) Path(runID string) (string, error) {
	if err := evidence.ValidateRunID(runID); err != nil {
		return "", err
	}
	if s.TargetDir == "" {
		return "", errors.New("attestation controller target is empty")
	}
	dir, err := safeio.EnsureDir(s.TargetDir, ".ai-team", "state", "attestation")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, runID+".json"), nil
}

func (s ControllerStore) Write(runID string, raw []byte) (retErr error) {
	path, err := s.Path(runID)
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > controllerAttestationLimit {
		return errors.New("attestation controller record size is invalid")
	}
	statement, err := Parse(raw)
	if err != nil {
		return err
	}
	if statement.Predicate.RunID != runID {
		return errors.New("attestation controller run mismatch")
	}
	canonical, err := json.MarshalIndent(statement, "", "  ")
	if err != nil {
		return err
	}
	canonical = append(canonical, '\n')
	if len(canonical) > controllerAttestationLimit {
		return errors.New("attestation controller record size is invalid")
	}
	if existing, readErr := safeio.ReadRegularFile(path, controllerAttestationLimit); readErr == nil {
		if bytes.Equal(existing, canonical) {
			return syncControllerAttestationDir(filepath.Dir(path))
		}
		return fmt.Errorf("controller attestation already exists for run %s; conflicting overwrite rejected", runID)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read controller attestation: %w", readErr)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-attestation-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("remove temporary controller attestation: %w", err))
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(canonical); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Link is no-replace: concurrent submissions cannot replace a statement.
	if err := os.Link(tmpPath, path); err != nil {
		if existing, readErr := safeio.ReadRegularFile(path, controllerAttestationLimit); readErr == nil && bytes.Equal(existing, canonical) {
			return syncControllerAttestationDir(dir)
		}
		return fmt.Errorf("persist controller attestation: %w", err)
	}
	return syncControllerAttestationDir(dir)
}

func syncControllerAttestationDir(dir string) error {
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open controller attestation directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync controller attestation directory: %w", err)
	}
	return nil
}

func (s ControllerStore) Read(runID string) ([]byte, error) {
	path, err := s.Path(runID)
	if err != nil {
		return nil, err
	}
	data, err := safeio.ReadRegularFile(path, controllerAttestationLimit)
	if err != nil {
		return nil, err
	}
	statement, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if statement.Predicate.RunID != runID {
		return nil, errors.New("controller attestation run mismatch")
	}
	return data, nil
}
