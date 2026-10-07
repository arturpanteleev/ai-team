package pipeline

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/provenance"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

func loadResumeCandidate(ctx context.Context, target, runID string, store candidate.MetadataStore) (*candidate.Manager, error) {
	var manager *candidate.Manager
	var err error
	if store == nil {
		manager, err = candidate.Load(ctx, target, runID)
	} else {
		manager, err = candidate.LoadWithMetadataStore(ctx, target, runID, store)
	}
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return manager, err
	}
	if store != nil {
		absenceStore, ok := store.(candidate.AbsenceMarkerStore)
		if !ok {
			return nil, fmt.Errorf("candidate metadata is required to resume run %s: %w", runID, err)
		}
		if absenceErr := absenceStore.ReadAbsent(target, runID); absenceErr == nil {
			return nil, nil
		} else {
			return nil, fmt.Errorf("candidate metadata or controller absence marker is required to resume run %s: %w", runID, errors.Join(err, absenceErr))
		}
	}
	// Local CLI keeps compatibility with older non-Git runs. This uses mutable
	// target files and is not a trust boundary; controller-backed workers never
	// enter this branch.
	knownCandidate, identityErr := runHasCandidate(target, runID)
	if identityErr != nil {
		return nil, fmt.Errorf("candidate metadata is missing and local run provenance cannot establish candidate absence: %w", identityErr)
	}
	if knownCandidate {
		return nil, fmt.Errorf("candidate metadata is missing for candidate-backed run %s", runID)
	}
	return nil, nil
}

// runHasCandidate is only a local CLI compatibility path for legacy runs. The
// run manifest lives under the mutable target and must never authorize a
// controller-backed worker to skip missing controller metadata.
func runHasCandidate(target, runID string) (bool, error) {
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	data, err := safeio.ReadRegularFile(filepath.Join(runDir, "run.json"), 1<<20)
	if err != nil {
		return false, err
	}
	var manifest evidence.RunManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false, fmt.Errorf("decode run manifest: %w", err)
	}
	if manifest.SchemaVersion != evidence.SchemaVersion || manifest.RunID != runID || manifest.TargetDir != target {
		return false, errors.New("run manifest identity mismatch")
	}
	if len(manifest.Provenance) == 0 {
		return false, errors.New("run manifest has no provenance")
	}
	var p provenance.Manifest
	if err := json.Unmarshal(manifest.Provenance, &p); err != nil {
		return false, fmt.Errorf("decode provenance manifest: %w", err)
	}
	if p.SchemaVersion != provenance.SchemaVersion || p.RunID != runID {
		return false, errors.New("provenance identity mismatch")
	}
	digest, found := p.Find(provenance.KindCandidate, "")
	if !found || digest.Type != "sha256" {
		return false, errors.New("provenance has no valid candidate identity")
	}
	if digest.Value == provenance.UnknownValue {
		return false, nil
	}
	decoded, err := hex.DecodeString(digest.Value)
	if err != nil || len(decoded) != 32 {
		return false, errors.New("provenance candidate digest is malformed")
	}
	return true, nil
}
