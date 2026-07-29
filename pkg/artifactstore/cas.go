// Package artifactstore хранит immutable run artifacts в content-addressed storage.
package artifactstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

type Blob struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type BlobStore interface {
	Put(io.Reader) (Blob, error)
	WriteTo(sha256 string, destination io.Writer) (int64, error)
}

type LocalCAS struct {
	root string
}

func NewLocalCAS(root string) (*LocalCAS, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := safeio.EnsureDir(filepath.Dir(absolute), filepath.Base(absolute)); err != nil {
		return nil, err
	}
	if _, err := safeio.EnsureDir(absolute, "blobs"); err != nil {
		return nil, err
	}
	if _, err := safeio.EnsureDir(absolute, "manifests"); err != nil {
		return nil, err
	}
	return &LocalCAS{root: filepath.Clean(absolute)}, nil
}

func (s *LocalCAS) Put(source io.Reader) (Blob, error) {
	temporary, err := os.CreateTemp(filepath.Join(s.root, "blobs"), ".blob-*.tmp")
	if err != nil {
		return Blob{}, err
	}
	tempPath := temporary.Name()
	defer os.Remove(tempPath)
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), source)
	if err != nil {
		temporary.Close()
		return Blob{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return Blob{}, err
	}
	if err := temporary.Close(); err != nil {
		return Blob{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	dir, err := safeio.EnsureDir(s.root, "blobs", digest[:2])
	if err != nil {
		return Blob{}, err
	}
	destination := filepath.Join(dir, digest[2:])
	if _, err := os.Lstat(destination); err == nil {
		if verifyErr := s.verify(digest); verifyErr != nil {
			return Blob{}, verifyErr
		}
		return Blob{SHA256: digest, Size: size}, nil
	} else if !os.IsNotExist(err) {
		return Blob{}, err
	}
	if err := os.Chmod(tempPath, 0444); err != nil {
		return Blob{}, err
	}
	if err := os.Rename(tempPath, destination); err != nil {
		if _, statErr := os.Stat(destination); statErr == nil {
			if verifyErr := s.verify(digest); verifyErr != nil {
				return Blob{}, verifyErr
			}
			return Blob{SHA256: digest, Size: size}, nil
		}
		return Blob{}, err
	}
	return Blob{SHA256: digest, Size: size}, nil
}

func (s *LocalCAS) WriteTo(digest string, destination io.Writer) (int64, error) {
	path, err := s.blobPath(digest)
	if err != nil {
		return 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(destination, hash), file)
	if err != nil {
		return 0, err
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != digest {
		return 0, fmt.Errorf("artifact blob corruption: expected=%s actual=%s", digest, actual)
	}
	return size, nil
}

func (s *LocalCAS) WriteManifest(runID string, value []byte) error {
	if !safeID(runID) {
		return errors.New("artifact manifest: invalid run ID")
	}
	blob, err := s.Put(bytes.NewReader(value))
	if err != nil {
		return err
	}
	reference, err := json.MarshalIndent(struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		ManifestSHA   string `json:"manifest_sha256"`
		ManifestSize  int64  `json:"manifest_size"`
	}{SchemaVersion: 1, RunID: runID, ManifestSHA: blob.SHA256, ManifestSize: blob.Size}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(s.root, "manifests")
	temporary, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	path := temporary.Name()
	defer os.Remove(path)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(reference, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(dir, runID+".json"))
}

func (s *LocalCAS) ReadManifest(runID string, limit int64) ([]byte, error) {
	if !safeID(runID) {
		return nil, errors.New("artifact manifest: invalid run ID")
	}
	data, err := safeio.ReadRegularFile(filepath.Join(s.root, "manifests", runID+".json"), 64<<10)
	if err != nil {
		return nil, err
	}
	var reference struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		ManifestSHA   string `json:"manifest_sha256"`
		ManifestSize  int64  `json:"manifest_size"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("artifact manifest reference trailing JSON")
	}
	if reference.SchemaVersion != 1 || reference.RunID != runID ||
		reference.ManifestSize < 0 || reference.ManifestSize > limit {
		return nil, errors.New("artifact manifest reference invalid")
	}
	destination := &boundedBuffer{remaining: limit}
	size, err := s.WriteTo(reference.ManifestSHA, destination)
	if err != nil {
		return nil, err
	}
	if size != reference.ManifestSize || size > limit {
		return nil, errors.New("artifact manifest size mismatch")
	}
	return destination.Bytes(), nil
}

type boundedBuffer struct {
	bytes.Buffer
	remaining int64
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	if int64(len(value)) > b.remaining {
		return 0, errors.New("artifact manifest превышает limit")
	}
	count, err := b.Buffer.Write(value)
	b.remaining -= int64(count)
	return count, err
}

func (s *LocalCAS) verify(digest string) error {
	_, err := s.WriteTo(digest, io.Discard)
	return err
}

func (s *LocalCAS) blobPath(digest string) (string, error) {
	if len(digest) != 64 {
		return "", errors.New("artifact blob: invalid SHA-256")
	}
	if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
		return "", errors.New("artifact blob: invalid SHA-256")
	}
	return filepath.Join(s.root, "blobs", digest[:2], digest[2:]), nil
}

func safeID(value string) bool {
	return value != "" && filepath.Base(value) == value && !strings.ContainsAny(value, `/\`)
}
