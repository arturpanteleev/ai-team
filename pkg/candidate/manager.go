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

func Create(ctx context.Context, controlTarget, runID string) (*Manager, bool, error) {
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
		return nil, true, fmt.Errorf("candidate worktree %s уже существует", worktree)
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
	if err := manager.save(); err != nil {
		return nil, true, err
	}
	return manager, true, nil
}

func Load(ctx context.Context, controlTarget, runID string) (*Manager, error) {
	target, err := canonicalDirectory(controlTarget)
	if err != nil {
		return nil, err
	}
	if !safeID(runID) {
		return nil, fmt.Errorf("candidate: invalid run id")
	}
	data, err := safeio.ReadRegularFile(
		filepath.Join(target, ".ai-team", "state", "candidates", runID+".json"), 64<<10,
	)
	if err != nil {
		return nil, err
	}
	var metadata Metadata
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return nil, fmt.Errorf("candidate metadata: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("candidate metadata содержит trailing JSON")
	}
	if metadata.SchemaVersion != metadataVersion || metadata.RunID != runID ||
		metadata.ControlTarget != target || metadata.Worktree != filepath.Join(target, ".ai-team", "worktrees", runID) {
		return nil, fmt.Errorf("candidate metadata identity mismatch")
	}
	manager := &Manager{metadata: metadata}
	if err := manager.verify(ctx); err != nil {
		return nil, err
	}
	return manager, nil
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

func (m *Manager) save() error {
	dir, err := safeio.EnsureDir(m.metadata.ControlTarget, ".ai-team", "state", "candidates")
	if err != nil {
		return err
	}
	destination := filepath.Join(dir, m.metadata.RunID+".json")
	if err := safeio.RejectSymlink(destination); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.metadata, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".candidate-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	defer os.Remove(tempPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
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
	return os.Rename(tempPath, destination)
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
