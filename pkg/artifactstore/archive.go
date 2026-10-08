package artifactstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const ManifestVersion = 1
const maxManifestBytes = 16 << 20

type Manifest struct {
	SchemaVersion int             `json:"schema_version"`
	RunID         string          `json:"run_id"`
	CreatedAt     time.Time       `json:"created_at"`
	Entries       []ManifestEntry `json:"entries"`
}

type ManifestEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

type RunArchive struct {
	runRoot string
	store   *LocalCAS
}

func NewRunArchive(runRoot string, store *LocalCAS) (*RunArchive, error) {
	if store == nil {
		return nil, errors.New("artifact store обязателен")
	}
	absolute, err := filepath.Abs(runRoot)
	if err != nil {
		return nil, err
	}
	return &RunArchive{runRoot: filepath.Clean(absolute), store: store}, nil
}

func (a *RunArchive) Archive(runID string) error {
	if !safeID(runID) {
		return errors.New("archive run: invalid run ID")
	}
	root := filepath.Join(a.runRoot, runID)
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("archive run: unsafe run root")
	}
	manifest := Manifest{SchemaVersion: ManifestVersion, RunID: runID, CreatedAt: time.Now().UTC()}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive run: symlink запрещён: %s", path)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archive run: non-regular entry запрещён: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if filepath.ToSlash(relative) == "events.jsonl" {
			// The target-side copy is worker-visible and may be stale or forged.
			// The canonical source is selected and archived below.
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		blob, putErr := a.store.Put(file)
		closeErr := file.Close()
		if putErr != nil {
			return putErr
		}
		if closeErr != nil {
			return closeErr
		}
		manifest.Entries = append(manifest.Entries, ManifestEntry{
			Path: filepath.ToSlash(relative), SHA256: blob.SHA256, Size: blob.Size,
			Mode: uint32(info.Mode().Perm()),
		})
		return nil
	})
	if err != nil {
		return err
	}
	if eventBytes, eventErr := evidence.ReadEventLogBytesForRunDir(root, runID); eventErr == nil {
		blob, putErr := a.store.Put(bytes.NewReader(eventBytes))
		if putErr != nil {
			return putErr
		}
		manifest.Entries = append(manifest.Entries, ManifestEntry{
			Path: "events.jsonl", SHA256: blob.SHA256, Size: blob.Size, Mode: 0600,
		})
	} else if errors.Is(eventErr, os.ErrNotExist) {
		if err := reservedEventLogMissingError(a.runRoot, runID, eventErr); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("archive run event log: %w", eventErr)
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Path < manifest.Entries[j].Path })
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return a.store.WriteManifest(runID, append(data, '\n'))
}

func reservedEventLogMissingError(runRoot, runID string, eventErr error) error {
	if filepath.Base(runRoot) != "runs" || filepath.Base(filepath.Dir(runRoot)) != ".ai-team" {
		return nil
	}
	target := filepath.Dir(filepath.Dir(runRoot))
	store := evidence.ControllerEventStore{TargetDir: target}
	reserved, authorityErr := store.IsReserved(runID)
	if reserved {
		return fmt.Errorf("archive reserved run event log: %w", eventErr)
	}
	if authorityErr == nil {
		return nil
	}
	if authorityErr != nil && !errors.Is(authorityErr, os.ErrNotExist) {
		return fmt.Errorf("archive run event authority: %w", authorityErr)
	}
	// IsReserved can return ENOENT both for an absent authority tree and for a
	// reserved marker whose canonical log is missing. Inspect the run-specific
	// marker and data directory to distinguish those cases without trusting the
	// worker-visible mirror.
	for _, path := range []string{
		filepath.Join(target, ".ai-team", "state", "runs", "event-log-reservations", runID+".json"),
		filepath.Join(target, ".ai-team", "state", "events", runID),
	} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("archive reserved run event log: %w", eventErr)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect archive event authority: %w", err)
		}
	}
	if authorityErr != nil {
		return fmt.Errorf("archive run event authority: %w", authorityErr)
	}
	return nil
}

func (a *RunArchive) Restore(runID, destination string) error {
	data, err := a.store.ReadManifest(runID, maxManifestBytes)
	if err != nil {
		return err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("artifact manifest trailing JSON")
	}
	if manifest.SchemaVersion != ManifestVersion || manifest.RunID != runID {
		return errors.New("artifact manifest identity mismatch")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err := safeio.EnsureDir(filepath.Dir(absolute), filepath.Base(absolute)); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, entry := range manifest.Entries {
		cleanPath := filepath.Clean(filepath.FromSlash(entry.Path))
		if entry.Path == "" || filepath.IsAbs(entry.Path) || filepath.ToSlash(cleanPath) != entry.Path ||
			cleanPath == ".." || filepath.Dir(cleanPath) == ".." || seen[entry.Path] {
			return fmt.Errorf("artifact manifest unsafe path %q", entry.Path)
		}
		seen[entry.Path] = true
		target := filepath.Join(absolute, cleanPath)
		relative, err := filepath.Rel(absolute, target)
		if err != nil || relative == ".." || filepath.IsAbs(relative) {
			return fmt.Errorf("artifact restore escapes destination: %s", entry.Path)
		}
		if parent := filepath.Dir(cleanPath); parent != "." {
			components := []string{}
			for current := parent; current != "."; {
				components = append([]string{filepath.Base(current)}, components...)
				current = filepath.Dir(current)
			}
			if _, err := safeio.EnsureDir(absolute, components...); err != nil {
				return err
			}
		}
		temporary, err := os.CreateTemp(filepath.Dir(target), ".restore-*.tmp")
		if err != nil {
			return err
		}
		tempPath := temporary.Name()
		size, copyErr := a.store.WriteTo(entry.SHA256, temporary)
		if closeErr := temporary.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil || size != entry.Size {
			// Аварийные пути до rename: недописанный temp-файл убирается как
			// уборка мусора, значимая ошибка возвращается следующей строкой.
			// Если убрать не удалось, сделать с этим всё равно нечего.
			_ = os.Remove(tempPath)
			return errors.Join(copyErr, fmt.Errorf("artifact restore size mismatch: %s", entry.Path))
		}
		if err := os.Chmod(tempPath, os.FileMode(entry.Mode)); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
		if err := os.Rename(tempPath, target); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
	}
	return nil
}
