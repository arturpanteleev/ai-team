// Package candidate управляет отдельным Git worktree одного run.
package candidate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const metadataVersion = 1
const maxCommandOutput = 64 << 10

type Metadata struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	ControlTarget string    `json:"control_target"`
	Worktree      string    `json:"worktree"`
	BaseCommit    string    `json:"base_commit"`
	BaseTree      string    `json:"base_tree"`
	CreatedAt     time.Time `json:"created_at"`
}

type Identity struct {
	RunID           string `json:"run_id"`
	BaseCommit      string `json:"base_commit"`
	BaseTree        string `json:"base_tree"`
	WorkspaceSHA256 string `json:"workspace_sha256"`
}

type Manager struct {
	metadata Metadata
}

// MetadataStore lets cloud workers access candidate identity through their
// controller API while preserving the local filesystem default.
type MetadataStore interface {
	Create(Metadata) error
	Read(controlTarget, runID string) (Metadata, error)
}

// AbsenceMarkerStore is a controller-owned proof that a run was admitted on a
// target without a Git repository. Workers may read the proof through their
// scoped controller API, but only the controller writes it before worker
// execution begins.
type AbsenceMarkerStore interface {
	MarkAbsent(controlTarget, runID string) error
	ReadAbsent(controlTarget, runID string) error
}

// GitAdmissionStore records controller-side proof that Start admitted a run
// while its target was a Git repository. It prevents a pre-existing absence
// marker from being reinterpreted as non-Git admission during recovery.
type GitAdmissionStore interface {
	MarkGitAdmission(controlTarget, runID string) error
	ReadGitAdmission(controlTarget, runID string) error
}

type FileMetadataStore struct{}

func (FileMetadataStore) Create(metadata Metadata) error {
	if err := rejectAbsenceMarker(metadata.ControlTarget, metadata.RunID); err != nil {
		return err
	}
	return saveMetadata(metadata)
}
func (FileMetadataStore) Read(controlTarget, runID string) (Metadata, error) {
	metadata, err := readMetadata(controlTarget, runID)
	if err != nil {
		return Metadata{}, err
	}
	if err := rejectAbsenceMarker(controlTarget, runID); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}
func (FileMetadataStore) MarkAbsent(controlTarget, runID string) error {
	return markAbsent(controlTarget, runID)
}
func (FileMetadataStore) ReadAbsent(controlTarget, runID string) error {
	return readAbsent(controlTarget, runID)
}
func (FileMetadataStore) MarkGitAdmission(controlTarget, runID string) error {
	return markGitAdmission(controlTarget, runID)
}
func (FileMetadataStore) ReadGitAdmission(controlTarget, runID string) error {
	return readGitAdmission(controlTarget, runID)
}

// DetectGitRepository determines whether target belongs to a Git repository.
// A failed rev-parse is considered a non-Git target only when no .git marker
// exists on the target's ancestor chain; a damaged or inaccessible repository
// therefore fails closed instead of being recorded as candidate absence.
func DetectGitRepository(ctx context.Context, target string) (bool, error) {
	canonical, err := canonicalDirectory(target)
	if err != nil {
		return false, err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return false, fmt.Errorf("detect candidate repository: git executable unavailable: %w", err)
	}
	command := exec.CommandContext(ctx, "git", "-C", canonical, "rev-parse", "--show-toplevel")
	command.Env = make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	if err := command.Run(); err == nil {
		return true, nil
	} else {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		for current := canonical; ; current = filepath.Dir(current) {
			marker := filepath.Join(current, ".git")
			if _, statErr := os.Lstat(marker); statErr == nil {
				return false, fmt.Errorf("detect candidate repository: .git marker exists but Git cannot resolve target: %w", err)
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return false, fmt.Errorf("inspect Git marker %s: %w", marker, statErr)
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
		}
		return false, nil
	}
}

func Create(ctx context.Context, controlTarget, runID string) (*Manager, bool, error) {
	return CreateWithMetadataStore(ctx, controlTarget, runID, FileMetadataStore{})
}

func CreateWithMetadataStore(ctx context.Context, controlTarget, runID string, store MetadataStore) (*Manager, bool, error) {
	if store == nil {
		return nil, false, errors.New("candidate metadata store is required")
	}
	target, err := canonicalDirectory(controlTarget)
	if err != nil {
		return nil, false, err
	}
	if !safeID(runID) {
		return nil, false, fmt.Errorf("candidate: invalid run id")
	}
	if _, err := git(ctx, target, "rev-parse", "--show-toplevel"); err != nil {
		return nil, false, nil
	}
	if status, err := git(ctx, target, "status", "--porcelain=v1", "--untracked-files=all", "--", ".", ":(exclude).ai-team"); err != nil {
		return nil, true, fmt.Errorf("candidate baseline status: %w", err)
	} else if strings.TrimSpace(status) != "" {
		return nil, true, fmt.Errorf("candidate требует clean git workspace")
	}
	baseCommit, err := git(ctx, target, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, true, fmt.Errorf("candidate baseline commit: %w", err)
	}
	baseTree, err := git(ctx, target, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return nil, true, fmt.Errorf("candidate baseline tree: %w", err)
	}
	root, err := safeio.EnsureDir(target, ".ai-team", "worktrees")
	if err != nil {
		return nil, true, err
	}
	worktree := filepath.Join(root, runID)
	if _, err := os.Lstat(worktree); err == nil {
		// A worker can die after `git worktree add` and before publishing the
		// candidate metadata. Under the run's workspace lock, recover that exact
		// detached worktree only when it still points at the current baseline.
		manager, recoverErr := recoverCreatedWorktree(ctx, target, runID, worktree, strings.TrimSpace(baseCommit), strings.TrimSpace(baseTree), store)
		return manager, true, recoverErr
	} else if !os.IsNotExist(err) {
		return nil, true, err
	}
	if _, err := git(ctx, target, "worktree", "add", "--detach", worktree, strings.TrimSpace(baseCommit)); err != nil {
		return nil, true, fmt.Errorf("candidate worktree add: %w", err)
	}
	metadata := Metadata{
		SchemaVersion: metadataVersion, RunID: runID, ControlTarget: target,
		Worktree: worktree, BaseCommit: strings.TrimSpace(baseCommit),
		BaseTree: strings.TrimSpace(baseTree), CreatedAt: time.Now().UTC(),
	}
	manager := &Manager{metadata: metadata}
	if err := manager.ensureArtifactRoot(); err != nil {
		return nil, true, err
	}
	if err := manager.save(store); err != nil {
		return nil, true, err
	}
	return manager, true, nil
}

func recoverCreatedWorktree(ctx context.Context, target, runID, worktree, baseCommit, baseTree string, store MetadataStore) (*Manager, error) {
	if _, err := store.Read(target, runID); err == nil {
		return LoadWithMetadataStore(ctx, target, runID, store)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	actualRoot, err := canonicalDirectory(worktree)
	if err != nil || actualRoot != worktree {
		return nil, fmt.Errorf("candidate recovery: existing worktree is unavailable or unsafe")
	}
	common, err := git(ctx, actualRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("candidate recovery: repository identity: %w", err)
	}
	actualCommon, err := filepath.EvalSymlinks(strings.TrimSpace(common))
	if err != nil {
		return nil, err
	}
	expectedCommon, err := filepath.EvalSymlinks(filepath.Join(target, ".git"))
	if err != nil || actualCommon != expectedCommon {
		return nil, fmt.Errorf("candidate recovery: worktree belongs to another repository")
	}
	head, err := git(ctx, actualRoot, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(head) != baseCommit {
		return nil, fmt.Errorf("candidate recovery: detached HEAD differs from admitted baseline")
	}
	manager := &Manager{metadata: Metadata{
		SchemaVersion: metadataVersion, RunID: runID, ControlTarget: target,
		Worktree: worktree, BaseCommit: baseCommit, BaseTree: baseTree,
		CreatedAt: time.Now().UTC(),
	}}
	if err := manager.ensureArtifactRoot(); err != nil {
		return nil, err
	}
	if err := manager.save(store); err != nil {
		return nil, err
	}
	return manager, nil
}

func Load(ctx context.Context, controlTarget, runID string) (*Manager, error) {
	return LoadWithMetadataStore(ctx, controlTarget, runID, FileMetadataStore{})
}

func LoadWithMetadataStore(ctx context.Context, controlTarget, runID string, store MetadataStore) (*Manager, error) {
	if store == nil {
		return nil, errors.New("candidate metadata store is required")
	}
	target, err := canonicalDirectory(controlTarget)
	if err != nil {
		return nil, err
	}
	if !safeID(runID) {
		return nil, fmt.Errorf("candidate: invalid run id")
	}
	metadata, err := store.Read(target, runID)
	if err != nil {
		return nil, err
	}
	if err := ValidateMetadata(target, runID, metadata); err != nil {
		return nil, err
	}
	manager := &Manager{metadata: metadata}
	if err := manager.verify(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

// ValidateMetadata enforces target/run-bound candidate identity.
func ValidateMetadata(controlTarget, runID string, metadata Metadata) error {
	target, err := canonicalDirectory(controlTarget)
	if err != nil {
		return err
	}
	if !safeID(runID) || metadata.SchemaVersion != metadataVersion || metadata.RunID != runID ||
		metadata.ControlTarget != target || metadata.Worktree != filepath.Join(target, ".ai-team", "worktrees", runID) ||
		metadata.BaseCommit == "" || metadata.BaseTree == "" || metadata.CreatedAt.IsZero() {
		return fmt.Errorf("candidate metadata identity mismatch")
	}
	return nil
}

func (m *Manager) Root() string       { return m.metadata.Worktree }
func (m *Manager) Metadata() Metadata { return m.metadata }

func (m *Manager) Identity() (Identity, error) {
	digest, err := checks.WorkspaceDigest(m.metadata.Worktree)
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		RunID: m.metadata.RunID, BaseCommit: m.metadata.BaseCommit,
		BaseTree: m.metadata.BaseTree, WorkspaceSHA256: digest,
	}, nil
}

func (m *Manager) verify(ctx context.Context) error {
	worktree, err := canonicalDirectory(m.metadata.Worktree)
	if err != nil || worktree != m.metadata.Worktree {
		return fmt.Errorf("candidate worktree unavailable or unsafe")
	}
	common, err := git(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("candidate repository identity: %w", err)
	}
	expectedCommon, err := filepath.EvalSymlinks(filepath.Join(m.metadata.ControlTarget, ".git"))
	if err != nil {
		return err
	}
	actualCommon, err := filepath.EvalSymlinks(strings.TrimSpace(common))
	if err != nil || actualCommon != expectedCommon {
		return fmt.Errorf("candidate repository identity mismatch")
	}
	if _, err := git(ctx, worktree, "merge-base", "--is-ancestor", m.metadata.BaseCommit, "HEAD"); err != nil {
		return fmt.Errorf("candidate HEAD не является потомком baseline")
	}
	tree, err := git(ctx, m.metadata.ControlTarget, "rev-parse", "--verify", m.metadata.BaseCommit+"^{tree}")
	if err != nil || strings.TrimSpace(tree) != m.metadata.BaseTree {
		return fmt.Errorf("candidate baseline tree identity mismatch")
	}
	return m.ensureArtifactRoot()
}

func (m *Manager) ensureArtifactRoot() error {
	for _, parts := range [][]string{
		{".ai-team"}, {".ai-team", "artifacts"}, {".ai-team", "artifacts", "tasks"},
	} {
		if _, err := safeio.EnsureDir(m.metadata.Worktree, parts...); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) save(store MetadataStore) error { return store.Create(m.metadata) }

func saveMetadata(metadata Metadata) error {
	if err := ValidateMetadata(metadata.ControlTarget, metadata.RunID, metadata); err != nil {
		return err
	}
	if stored, err := readMetadata(metadata.ControlTarget, metadata.RunID); err == nil {
		if stored == metadata {
			return nil
		}
		return fmt.Errorf("candidate metadata conflicts with existing run identity")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir, err := safeio.EnsureDir(metadata.ControlTarget, ".ai-team", "state", "candidates")
	if err != nil {
		return err
	}
	destination := filepath.Join(dir, metadata.RunID+".json")
	if err := safeio.RejectSymlink(destination); err != nil {
		return err
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".candidate-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	defer func() { _ = os.Remove(tempPath) }() // temp-файл удаляется, только если rename не состоялся; ENOENT после успеха — норма.
	if err := temporary.Chmod(0600); err != nil {
		// аварийный путь: значимая ошибка уже возвращается, Close только освобождает дескриптор.
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
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
	if err := os.Link(tempPath, destination); err != nil {
		if stored, readErr := readMetadata(metadata.ControlTarget, metadata.RunID); readErr == nil && stored == metadata {
			return nil
		}
		return fmt.Errorf("persist candidate metadata without replacing an existing identity: %w", err)
	}
	return nil
}

const absenceMarkerVersion = 1

type absenceMarker struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	ControlTarget string    `json:"control_target"`
	CreatedAt     time.Time `json:"created_at"`
}

func markAbsent(controlTarget, runID string) error {
	target, err := canonicalDirectory(controlTarget)
	if err != nil {
		return err
	}
	if !safeID(runID) {
		return fmt.Errorf("candidate: invalid run id")
	}
	if err := readGitAdmission(target, runID); err == nil {
		return errors.New("candidate absence conflicts with prior Git admission")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check Git admission before candidate absence: %w", err)
	}
	if _, err := readMetadata(target, runID); err == nil {
		return errors.New("candidate absence conflicts with existing candidate metadata")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check candidate metadata before absence marker: %w", err)
	}
	dir, err := safeio.EnsureDir(target, ".ai-team", "state", "candidates")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, runID+".absent.json")
	if _, err := readAbsenceMarker(target, runID); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("existing candidate absence marker is invalid: %w", err)
	}
	marker := absenceMarker{SchemaVersion: absenceMarkerVersion, RunID: runID, ControlTarget: target, CreatedAt: time.Now().UTC()}
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".candidate-absence-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
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
	if err := os.Link(temporaryPath, path); err != nil {
		if _, readErr := readAbsenceMarker(target, runID); readErr == nil {
			return nil
		}
		return fmt.Errorf("persist controller candidate absence marker without replacement: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open candidate absence directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync candidate absence directory: %w", err)
	}
	return nil
}

func readAbsent(controlTarget, runID string) error {
	target, err := canonicalDirectory(controlTarget)
	if err != nil {
		return err
	}
	if !safeID(runID) {
		return fmt.Errorf("candidate: invalid run id")
	}
	if err := readGitAdmission(target, runID); err == nil {
		return errors.New("candidate absence conflicts with prior Git admission")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("candidate Git admission state is invalid: %w", err)
	}
	if _, err := readMetadata(target, runID); err == nil {
		return errors.New("candidate absence conflicts with existing candidate metadata")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("candidate metadata state is invalid: %w", err)
	}
	_, err = readAbsenceMarker(target, runID)
	return err
}

type gitAdmissionMarker struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	ControlTarget string    `json:"control_target"`
	CreatedAt     time.Time `json:"created_at"`
}

func markGitAdmission(controlTarget, runID string) error {
	target, err := canonicalDirectory(controlTarget)
	if err != nil {
		return err
	}
	if !safeID(runID) {
		return fmt.Errorf("candidate: invalid run id")
	}
	if err := readGitAdmission(target, runID); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("existing Git admission marker is invalid: %w", err)
	}
	dir, err := safeio.EnsureDir(target, ".ai-team", "state", "candidates")
	if err != nil {
		return err
	}
	marker := gitAdmissionMarker{SchemaVersion: 1, RunID: runID, ControlTarget: target, CreatedAt: time.Now().UTC()}
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".candidate-git-admission-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
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
	path := filepath.Join(dir, runID+".git-admitted.json")
	if err := os.Link(temporaryPath, path); err != nil {
		if readErr := readGitAdmission(target, runID); readErr == nil {
			return nil
		}
		return fmt.Errorf("persist Git admission marker without replacement: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open candidate admission directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func readGitAdmission(controlTarget, runID string) error {
	target, err := canonicalDirectory(controlTarget)
	if err != nil {
		return err
	}
	if !safeID(runID) {
		return fmt.Errorf("candidate: invalid run id")
	}
	data, err := safeio.ReadRegularFile(filepath.Join(target, ".ai-team", "state", "candidates", runID+".git-admitted.json"), 16<<10)
	if err != nil {
		return err
	}
	var marker gitAdmissionMarker
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return fmt.Errorf("candidate Git admission marker: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("candidate Git admission marker contains trailing JSON")
	}
	if marker.SchemaVersion != 1 || marker.RunID != runID || marker.ControlTarget != target || marker.CreatedAt.IsZero() {
		return errors.New("candidate Git admission marker identity mismatch")
	}
	return nil
}

func rejectAbsenceMarker(controlTarget, runID string) error {
	target, err := canonicalDirectory(controlTarget)
	if err != nil {
		return err
	}
	if _, err := readAbsenceMarker(target, runID); err == nil {
		return errors.New("candidate metadata conflicts with candidate absence marker")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("candidate absence marker state is invalid: %w", err)
	}
	return nil
}

func readAbsenceMarker(target, runID string) (absenceMarker, error) {
	if !safeID(runID) {
		return absenceMarker{}, fmt.Errorf("candidate: invalid run id")
	}
	data, err := safeio.ReadRegularFile(filepath.Join(target, ".ai-team", "state", "candidates", runID+".absent.json"), 16<<10)
	if err != nil {
		return absenceMarker{}, err
	}
	var marker absenceMarker
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return absenceMarker{}, fmt.Errorf("candidate absence marker: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return absenceMarker{}, errors.New("candidate absence marker contains trailing JSON")
	}
	canonical, err := canonicalDirectory(target)
	if err != nil {
		return absenceMarker{}, err
	}
	if marker.SchemaVersion != absenceMarkerVersion || marker.RunID != runID || marker.ControlTarget != canonical || marker.CreatedAt.IsZero() {
		return absenceMarker{}, errors.New("candidate absence marker identity mismatch")
	}
	return marker, nil
}

func readMetadata(controlTarget, runID string) (Metadata, error) {
	target, err := canonicalDirectory(controlTarget)
	if err != nil {
		return Metadata{}, err
	}
	if !safeID(runID) {
		return Metadata{}, fmt.Errorf("candidate: invalid run id")
	}
	data, err := safeio.ReadRegularFile(filepath.Join(target, ".ai-team", "state", "candidates", runID+".json"), 64<<10)
	if err != nil {
		return Metadata{}, err
	}
	var metadata Metadata
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return Metadata{}, fmt.Errorf("candidate metadata: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Metadata{}, fmt.Errorf("candidate metadata contains trailing JSON")
	}
	if err := ValidateMetadata(target, runID, metadata); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func canonicalDirectory(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%s должен быть каталогом без symlink", absolute)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

// CanonicalTargetDir resolves a target path (including a final symlink) to the
// absolute directory used as the worker API namespace. Candidate metadata
// itself is still validated against the resolved path before it is stored.
func CanonicalTargetDir(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s должен быть каталогом", canonical)
	}
	return filepath.Clean(canonical), nil
}

func safeID(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value &&
		!strings.ContainsAny(value, `/\`)
}

func git(ctx context.Context, target string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", target}, args...)...)
	var output limitedBuffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if ctx.Err() != nil {
		return output.String(), ctx.Err()
	}
	if err != nil {
		return output.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
	}
	return strings.TrimSpace(output.String()), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(value []byte) (int, error) {
	remaining := maxCommandOutput - b.Len()
	if remaining > len(value) {
		remaining = len(value)
	}
	if remaining > 0 {
		_, _ = b.Buffer.Write(value[:remaining])
	}
	return len(value), nil
}
