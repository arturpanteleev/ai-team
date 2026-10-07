package evidence

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// MaxAttemptManifestSize bounds attempt manifests accepted by source-backed
// APIs and written by PublishAttempt. Injected sources return a []byte before
// the adapter enforces this limit, so their allocation is not bounded here.
// Legacy standalone replay streams the filesystem digest and can still verify
// larger pre-existing manifests.
const MaxAttemptManifestSize = 8 << 20

const maxAttemptManifestSize = MaxAttemptManifestSize

// AttemptManifestSource provides the bytes for an attempt manifest. Keeping
// the source explicit lets replay, resume, and verification share the same
// manifest identity checks while allowing a controller-owned backend. The
// adapter rejects returned payloads larger than MaxAttemptManifestSize, after
// the source has produced the []byte; this does not bound a custom source's
// allocation. The filesystem source enforces the limit while reading.
type AttemptManifestSource interface {
	ReadAttemptManifest(runDir, runID, attemptID string) ([]byte, error)
}

type filesystemAttemptManifestSource struct{}

// FilesystemAttemptManifestSource is the compatible default reader for
// manifests stored under <runDir>/attempts/<attemptID>/manifest.json.
func FilesystemAttemptManifestSource() AttemptManifestSource {
	return filesystemAttemptManifestSource{}
}

func (filesystemAttemptManifestSource) ReadAttemptManifest(runDir, runID, attemptID string) ([]byte, error) {
	if err := validateAttemptManifestIdentity(runID, attemptID); err != nil {
		return nil, err
	}
	return safeio.ReadRegularFile(filepath.Join(runDir, "attempts", attemptID, "manifest.json"), maxAttemptManifestSize)
}

func readAttemptManifest(source AttemptManifestSource, runDir, runID, attemptID string) ([]byte, AttemptManifest, error) {
	data, err := readAttemptManifestBytes(source, runDir, runID, attemptID)
	if err != nil {
		return nil, AttemptManifest{}, err
	}
	var manifest AttemptManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, AttemptManifest{}, fmt.Errorf("attempt manifest %s повреждён: %w", attemptID, err)
	}
	return data, manifest, nil
}

func readAttemptManifestBytes(source AttemptManifestSource, runDir, runID, attemptID string) ([]byte, error) {
	if err := validateAttemptManifestIdentity(runID, attemptID); err != nil {
		return nil, err
	}
	if source == nil {
		var err error
		source, err = defaultAttemptManifestSource(runDir, runID)
		if err != nil {
			return nil, err
		}
	}
	data, err := source.ReadAttemptManifest(runDir, runID, attemptID)
	if err != nil {
		return nil, err
	}
	if len(data) > maxAttemptManifestSize {
		return nil, fmt.Errorf("attempt manifest %s exceeds maximum size of %d bytes", attemptID, maxAttemptManifestSize)
	}
	return data, nil
}

// attemptManifestDigest preserves standalone filesystem replay's historical
// streaming behavior. Controller-backed sources return bytes, so those reads
// stay bounded by maxAttemptManifestSize.
func attemptManifestDigest(source AttemptManifestSource, runDir, runID, attemptID string) (string, int64, error) {
	if source == nil {
		if err := validateAttemptManifestIdentity(runID, attemptID); err != nil {
			return "", 0, err
		}
		resolved, err := defaultAttemptManifestSource(runDir, runID)
		if err != nil {
			return "", 0, err
		}
		if _, isController := resolved.(ControllerAttemptManifestStore); isController {
			data, readErr := resolved.ReadAttemptManifest(runDir, runID, attemptID)
			if readErr != nil {
				return "", 0, readErr
			}
			return sha256Bytes(data), int64(len(data)), nil
		}
		path := filepath.Join(runDir, "attempts", attemptID, "manifest.json")
		artifactType, size, digest, err := ArtifactDigest(path)
		if err != nil {
			return "", 0, err
		}
		if artifactType != "file" {
			return "", 0, fmt.Errorf("attempt manifest %s is not a file", attemptID)
		}
		return digest, size, nil
	}
	data, err := readAttemptManifestBytes(source, runDir, runID, attemptID)
	if err != nil {
		return "", 0, err
	}
	return sha256Bytes(data), int64(len(data)), nil
}

// ReadAttemptManifest returns a validated typed manifest from the selected
// source. A nil source resolves controller reservation automatically and
// otherwise keeps legacy filesystem behavior.
func ReadAttemptManifest(source AttemptManifestSource, runDir, runID, attemptID string) ([]byte, AttemptManifest, error) {
	return readAttemptManifest(source, runDir, runID, attemptID)
}

// AttemptManifestDigest calculates the digest over the exact canonical record
// selected for this run, including controller-owned records when reserved.
func AttemptManifestDigest(source AttemptManifestSource, runDir, runID, attemptID string) (string, int64, error) {
	return attemptManifestDigest(source, runDir, runID, attemptID)
}

func defaultAttemptManifestSource(runDir, runID string) (AttemptManifestSource, error) {
	clean := filepath.Clean(runDir)
	if filepath.Base(clean) != runID || filepath.Base(filepath.Dir(clean)) != "runs" || filepath.Base(filepath.Dir(filepath.Dir(clean))) != ".ai-team" {
		return filesystemAttemptManifestSource{}, nil
	}
	target := filepath.Dir(filepath.Dir(filepath.Dir(clean)))
	store := ControllerAttemptManifestStore{TargetDir: target}
	reserved, err := store.IsReserved(runID)
	if err != nil {
		return nil, err
	}
	if reserved {
		return store, nil
	}
	return filesystemAttemptManifestSource{}, nil
}

func validateAttemptManifestIdentity(runID, attemptID string) error {
	if err := ValidateRunID(runID); err != nil {
		return err
	}
	if attemptID == "" || attemptID == "." || attemptID == ".." || filepath.Base(attemptID) != attemptID || filepath.Clean(attemptID) != attemptID || strings.ContainsAny(attemptID, "/\\\x00") {
		return fmt.Errorf("invalid attempt id %q", attemptID)
	}
	return nil
}
