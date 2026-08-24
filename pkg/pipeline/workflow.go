package pipeline

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/checks"
)

type filesystemSnapshot struct {
	Fingerprint string
	Files       map[string]string
}

type gitMetadataSnapshot struct {
	Fingerprint string
	Head        string
	Dirty       map[string]bool
	Tracked     map[string]bool
}

// captureWorkspaceSnapshot provides the same per-attempt attribution when the
// target is not a git repository. Controller-owned metadata and dependency
// directories from checks.DefaultIgnoreDirs are excluded; all other regular
// files and symlinks are hashed.
func captureWorkspaceSnapshot(root string) (filesystemSnapshot, error) {
	return captureFilesystemSnapshot(root, checks.DefaultIgnoreDirs())
}

// captureArtifactSnapshot attributes changes inside the controller's artifact
// namespace. Unlike a source snapshot it excludes nothing: agents may only
// touch the exact artifact paths declared for the current stage.
func captureArtifactSnapshot(root string) (filesystemSnapshot, error) {
	return captureFilesystemSnapshot(root, nil)
}

// captureFilesystemSnapshot adapts checks.WorkspaceFileDigests — the single
// tree-walking implementation shared with checks.WorkspaceDigest.
func captureFilesystemSnapshot(root string, ignoredDirs map[string]bool) (filesystemSnapshot, error) {
	files, fingerprint, err := checks.WorkspaceFileDigests(root, ignoredDirs)
	if err != nil {
		return filesystemSnapshot{}, err
	}
	return filesystemSnapshot{Fingerprint: fingerprint, Files: files}, nil
}

// captureGitMetadataSnapshot binds an attempt to HEAD, the current symbolic
// ref and the complete Git index. Workspace bytes are intentionally captured
// separately by captureWorkspaceSnapshot so ignored files and nested targets
// cannot disappear from mutation attribution.
func captureGitMetadataSnapshot(dir string) (snapshot gitMetadataSnapshot, available bool, err error) {
	rootCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	rootCmd.Dir = dir
	rootOut, rootErr := rootCmd.Output()
	if rootErr != nil {
		if findGitMetadata(dir) == "" {
			return gitMetadataSnapshot{}, false, nil
		}
		return gitMetadataSnapshot{}, false, fmt.Errorf("git rev-parse: %w", rootErr)
	}
	root := strings.TrimSpace(string(rootOut))
	if root == "" {
		return gitMetadataSnapshot{}, false, fmt.Errorf("git rev-parse вернул пустой корень")
	}
	target, err := filepath.Abs(dir)
	if err != nil {
		return gitMetadataSnapshot{}, true, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return gitMetadataSnapshot{}, true, err
	}
	if root != target {
		return gitMetadataSnapshot{}, true, fmt.Errorf("target %s должен совпадать с git root %s; subdirectory target не имеет filesystem isolation", target, root)
	}
	targetRel, err := filepath.Rel(root, target)
	if err != nil || targetRel == ".." || strings.HasPrefix(targetRel, ".."+string(filepath.Separator)) {
		return gitMetadataSnapshot{}, true, fmt.Errorf("target %s находится вне git root %s", target, root)
	}
	pathspec := filepath.ToSlash(targetRel)
	if pathspec == "" {
		pathspec = "."
	}

	h := sha256.New()
	writePart := func(label string, data []byte) {
		fmt.Fprintf(h, "%s\x00%d\x00", label, len(data))
		_, _ = h.Write(data)
	}

	head, headErr := gitOutput(root, "rev-parse", "--verify", "HEAD")
	if headErr != nil {
		head = []byte("UNBORN")
	}
	writePart("head", head)
	snapshot.Head = strings.TrimSpace(string(head))
	symbolicHead, _ := gitOutput(root, "symbolic-ref", "-q", "HEAD")
	writePart("symbolic-head", symbolicHead)
	index, indexErr := gitOutput(root, "ls-files", "--stage", "-z")
	if indexErr != nil {
		return gitMetadataSnapshot{}, true, fmt.Errorf("git index: %w", indexErr)
	}
	writePart("index", index)
	tracked, trackedErr := gitOutput(root, "ls-files", "-z", "--", pathspec)
	if trackedErr != nil {
		return gitMetadataSnapshot{}, true, fmt.Errorf("git tracked paths: %w", trackedErr)
	}
	snapshot.Tracked = make(map[string]bool)
	for _, repoRelative := range splitNUL(tracked) {
		relative, relErr := filepath.Rel(target, filepath.Join(root, filepath.FromSlash(repoRelative)))
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			snapshot.Tracked[filepath.ToSlash(relative)] = true
		}
	}

	snapshot.Dirty = make(map[string]bool)
	dirtyCommands := [][]string{
		{"diff", "--name-only", "-z", "--", pathspec},
		{"diff", "--cached", "--name-only", "-z", "--", pathspec},
		{"ls-files", "--others", "--exclude-standard", "-z", "--", pathspec},
	}
	for _, args := range dirtyCommands {
		out, commandErr := gitOutput(root, args...)
		if commandErr != nil {
			return gitMetadataSnapshot{}, true, fmt.Errorf("git dirty paths: %w", commandErr)
		}
		for _, repoRelative := range splitNUL(out) {
			relative, relErr := filepath.Rel(target, filepath.Join(root, filepath.FromSlash(repoRelative)))
			if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			snapshot.Dirty[filepath.ToSlash(relative)] = true
		}
	}

	snapshot.Fingerprint = fmt.Sprintf("%x", h.Sum(nil))
	return snapshot, true, nil
}

func changedSnapshotPaths(before, after filesystemSnapshot) []string {
	seen := make(map[string]bool, len(before.Files)+len(after.Files))
	for path := range before.Files {
		seen[path] = true
	}
	for path := range after.Files {
		seen[path] = true
	}
	var changed []string
	for path := range seen {
		if before.Files[path] != after.Files[path] {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}

func gitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func splitNUL(data []byte) []string {
	var result []string
	for _, part := range strings.Split(string(data), "\x00") {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func findGitMetadata(dir string) string {
	current, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(current, ".git")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func gitDiffOutput(dir string) string {
	cmd := exec.Command("git", "--no-pager", "diff")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(не удалось получить diff: %v)", err)
	}
	return string(out)
}

// findLoopbackTarget ищет точную цель loopback среди агентов ДО индекса before.
