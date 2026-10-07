package evidence

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// MaxAttemptManifestSize bounds attempt manifests read through source-backed
// APIs and written by PublishAttempt. Legacy standalone replay streams the
// filesystem digest and can still verify larger pre-existing manifests.
const MaxAttemptManifestSize = 8 << 20

const maxAttemptManifestSize = MaxAttemptManifestSize

// AttemptManifestSource provides the bytes for an attempt manifest. Keeping
// the source explicit lets replay, resume, and verification share the same
// manifest identity checks while allowing a controller-owned backend. Reads
// through this interface are limited to MaxAttemptManifestSize bytes.
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
	if source == nil {
		source = filesystemAttemptManifestSource{}
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

func validateAttemptManifestIdentity(runID, attemptID string) error {
	if err := ValidateRunID(runID); err != nil {
		return err
	}
	if attemptID == "" || filepath.Base(attemptID) != attemptID || filepath.Clean(attemptID) != attemptID {
		return fmt.Errorf("invalid attempt id %q", attemptID)
	}
	return nil
}
