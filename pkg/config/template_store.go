package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const MaxTemplateYAMLBytes = 1 << 20

var templateVersionPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var taskTemplateIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type TemplateVersion struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type runTemplatePin struct {
	TaskID  string `json:"task_id"`
	Version string `json:"version"`
}

// TemplateStore keeps the project's single active template, immutable
// published versions, and immutable task-level pins. A run record references
// a task pin so recovery and continuation can resolve the same YAML.
type TemplateStore struct {
	target string
	mu     sync.Mutex
}

func NewTemplateStore(target string) (*TemplateStore, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, errors.New("template target должен быть доступным каталогом")
	}
	return &TemplateStore{target: filepath.Clean(abs)}, nil
}

func (s *TemplateStore) currentPath() string {
	return filepath.Join(s.target, ".ai-team", "config.yaml")
}

func (s *TemplateStore) ReadCurrent() ([]byte, string, error) {
	if _, err := safeio.ExistingDir(s.target, ".ai-team"); err != nil {
		return nil, "", err
	}
	data, err := safeio.ReadRegularFile(s.currentPath(), MaxTemplateYAMLBytes)
	if err != nil {
		return nil, "", err
	}
	return data, TemplateVersionID(data), nil
}

func TemplateVersionID(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Publish atomically replaces the project's one active template. The expected
// digest is an optimistic concurrency guard against overwriting a newer edit.
func (s *TemplateStore) Publish(data []byte, expectedVersion string) (string, error) {
	if len(data) == 0 || len(data) > MaxTemplateYAMLBytes {
		return "", fmt.Errorf("template YAML должен занимать от 1 до %d bytes", MaxTemplateYAMLBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, currentVersion, err := s.ReadCurrent()
	if err != nil {
		return "", err
	}
	if expectedVersion == "" || expectedVersion != currentVersion {
		return "", fmt.Errorf("активный шаблон изменился: ожидалась версия %q, текущая %q", expectedVersion, currentVersion)
	}
	version := TemplateVersionID(data)
	if err := s.writeImmutableVersion(version, data); err != nil {
		return "", err
	}
	if string(current) == string(data) {
		return version, nil
	}
	if err := atomicReplaceRegular(s.currentPath(), data, 0644); err != nil {
		return "", err
	}
	return version, nil
}

// PinCurrentForRun links a run to a task-level immutable pin. Repeated calls
// for an existing task reuse its original version even when the project has
// since published another one. A new task captures the current active YAML.
func (s *TemplateStore) PinCurrentForRun(runID, taskID string) (string, error) {
	if !taskTemplateIDPattern.MatchString(runID) || !taskTemplateIDPattern.MatchString(taskID) {
		return "", errors.New("template pin: недопустимый run или task id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, found, err := s.readRunPin(runID); err != nil {
		return "", err
	} else if found {
		if existing.TaskID != taskID {
			return "", errors.New("template pin: run уже привязан к другой задаче")
		}
		return existing.Version, nil
	}
	if _, taskVersion, found, err := s.readTaskPin(taskID); err != nil {
		return "", err
	} else if found {
		if err := s.writeRunPin(runID, taskID, taskVersion); err != nil {
			return "", err
		}
		return taskVersion, nil
	}
	data, _, err := s.ReadCurrent()
	if err != nil {
		return "", err
	}
	version := TemplateVersionID(data)
	if err := s.writeImmutableVersion(version, data); err != nil {
		return "", err
	}
	if err := s.writeTaskPin(taskID, data); err != nil {
		return "", err
	}
	if err := s.writeRunPin(runID, taskID, version); err != nil {
		return "", err
	}
	return version, nil
}

// PinDataForRun is used when an already-resolved configuration is available
// (for example, a CLI run without a project config file). It is idempotent for
// the same task and refuses to retarget an existing run.
func (s *TemplateStore) PinDataForRun(runID, taskID string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxTemplateYAMLBytes ||
		!taskTemplateIDPattern.MatchString(runID) || !taskTemplateIDPattern.MatchString(taskID) {
		return "", errors.New("template pin: invalid run, task, or YAML size")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, found, err := s.readRunPin(runID); err != nil {
		return "", err
	} else if found {
		if existing.TaskID != taskID {
			return "", errors.New("template pin: run уже привязан к другой задаче")
		}
		return existing.Version, nil
	}
	if _, taskVersion, found, err := s.readTaskPin(taskID); err != nil {
		return "", err
	} else if found {
		if err := s.writeRunPin(runID, taskID, taskVersion); err != nil {
			return "", err
		}
		return taskVersion, nil
	}
	version := TemplateVersionID(data)
	if err := s.writeImmutableVersion(version, data); err != nil {
		return "", err
	}
	if err := s.writeTaskPin(taskID, data); err != nil {
		return "", err
	}
	if err := s.writeRunPin(runID, taskID, version); err != nil {
		return "", err
	}
	return version, nil
}

func (s *TemplateStore) ReadPinnedRun(runID string) ([]byte, string, bool, error) {
	if !taskTemplateIDPattern.MatchString(runID) {
		return nil, "", false, errors.New("template pin: недопустимый run id")
	}
	pin, found, err := s.readRunPin(runID)
	if err != nil || !found {
		return nil, "", found, err
	}
	data, err := safeio.ReadRegularFile(s.taskPinPath(pin.TaskID), MaxTemplateYAMLBytes)
	if err != nil {
		return nil, "", false, fmt.Errorf("template task pin %s: %w", pin.TaskID, err)
	}
	if TemplateVersionID(data) != pin.Version {
		return nil, "", false, errors.New("template task pin digest mismatch")
	}
	return data, pin.Version, true, nil
}

func (s *TemplateStore) ListVersions() ([]TemplateVersion, error) {
	directory, err := safeio.ExistingDir(s.target, ".ai-team", "templates", "versions")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	versions := make([]TemplateVersion, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".yaml")
		if !strings.HasSuffix(entry.Name(), ".yaml") || !templateVersionPattern.MatchString(id) || entry.IsDir() {
			continue
		}
		data, readErr := safeio.ReadRegularFile(filepath.Join(directory, entry.Name()), MaxTemplateYAMLBytes)
		if readErr != nil || TemplateVersionID(data) != id {
			return nil, fmt.Errorf("published template version %s повреждена", id)
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil, statErr
		}
		versions = append(versions, TemplateVersion{ID: id, CreatedAt: info.ModTime().UTC()})
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].CreatedAt.Equal(versions[j].CreatedAt) {
			return versions[i].ID > versions[j].ID
		}
		return versions[i].CreatedAt.After(versions[j].CreatedAt)
	})
	return versions, nil
}

func (s *TemplateStore) writeImmutableVersion(version string, data []byte) error {
	path := filepath.Join(s.target, ".ai-team", "templates", "versions", version+".yaml")
	if err := safeio.WriteRegularFileNoFollow(path, data, 0444); err != nil {
		if existing, readErr := safeio.ReadRegularFile(path, MaxTemplateYAMLBytes); readErr == nil && string(existing) == string(data) {
			return nil
		}
		return err
	}
	return nil
}

func (s *TemplateStore) writeTaskPin(taskID string, data []byte) error {
	path := s.taskPinPath(taskID)
	if err := safeio.WriteRegularFileNoFollow(path, data, 0444); err != nil {
		if existing, readErr := safeio.ReadRegularFile(path, MaxTemplateYAMLBytes); readErr == nil && string(existing) == string(data) {
			return nil
		}
		return err
	}
	return nil
}

func (s *TemplateStore) readRunPin(runID string) (runTemplatePin, bool, error) {
	directory, err := safeio.ExistingDir(s.target, ".ai-team", "run-template-pins")
	if errors.Is(err, os.ErrNotExist) {
		return runTemplatePin{}, false, nil
	}
	if err != nil {
		return runTemplatePin{}, false, err
	}
	data, err := safeio.ReadRegularFile(filepath.Join(directory, runID+".json"), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return runTemplatePin{}, false, nil
	}
	if err != nil {
		return runTemplatePin{}, false, err
	}
	var pin runTemplatePin
	if err := json.Unmarshal(data, &pin); err != nil || !taskTemplateIDPattern.MatchString(pin.TaskID) || !templateVersionPattern.MatchString(pin.Version) {
		return runTemplatePin{}, false, errors.New("template run pin повреждён")
	}
	return pin, true, nil
}

func (s *TemplateStore) readTaskPin(taskID string) ([]byte, string, bool, error) {
	directory, err := safeio.ExistingDir(s.target, ".ai-team", "task-template-pins")
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	data, err := safeio.ReadRegularFile(filepath.Join(directory, taskID+".yaml"), MaxTemplateYAMLBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	return data, TemplateVersionID(data), true, nil
}

func (s *TemplateStore) writeRunPin(runID, taskID, version string) error {
	encoded, err := json.Marshal(runTemplatePin{TaskID: taskID, Version: version})
	if err != nil {
		return err
	}
	path := s.runPinPath(runID)
	if err := safeio.WriteRegularFileNoFollow(path, encoded, 0444); err != nil {
		if existing, found, readErr := s.readRunPin(runID); readErr == nil && found &&
			existing.TaskID == taskID && existing.Version == version {
			return nil
		}
		return err
	}
	return nil
}

func (s *TemplateStore) runPinPath(runID string) string {
	return filepath.Join(s.target, ".ai-team", "run-template-pins", runID+".json")
}

func (s *TemplateStore) taskPinPath(taskID string) string {
	return filepath.Join(s.target, ".ai-team", "task-template-pins", taskID+".yaml")
}

func atomicReplaceRegular(path string, data []byte, mode os.FileMode) (err error) {
	if err := safeio.RejectSymlink(path); err != nil {
		return err
	}
	if err := safeio.EnsureDirPath(filepath.Dir(path)); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".template-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := safeio.RejectSymlink(path); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
