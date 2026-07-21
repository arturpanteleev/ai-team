package pipeline

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
// target is not a git repository. Controller-owned .ai-team and .git metadata
// are excluded; all other regular files and symlinks are hashed.
func captureWorkspaceSnapshot(root string) (filesystemSnapshot, error) {
	return captureFilesystemSnapshot(root, map[string]bool{".ai-team": true, ".git": true})
}

// captureArtifactSnapshot attributes changes inside the controller's artifact
// namespace. Unlike a source snapshot it excludes nothing: agents may only
// touch the exact artifact paths declared for the current stage.
func captureArtifactSnapshot(root string) (filesystemSnapshot, error) {
	return captureFilesystemSnapshot(root, nil)
}

func captureFilesystemSnapshot(root string, ignoredTopLevel map[string]bool) (filesystemSnapshot, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return filesystemSnapshot{}, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return filesystemSnapshot{}, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return filesystemSnapshot{}, fmt.Errorf("snapshot root %s должен быть обычным каталогом без symlink", root)
	}
	snapshot := filesystemSnapshot{Files: make(map[string]string)}
	err = filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(root, filePath)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		first := strings.SplitN(relative, "/", 2)[0]
		if entry.IsDir() && ignoredTopLevel[first] {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		digest, hashErr := hashWorkspacePath(root, filepath.FromSlash(relative))
		if hashErr != nil {
			return hashErr
		}
		snapshot.Files[relative] = digest
		return nil
	})
	if err != nil {
		return filesystemSnapshot{}, err
	}
	paths := make([]string, 0, len(snapshot.Files))
	for relative := range snapshot.Files {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		fmt.Fprintf(hash, "%s\x00%s\x00", relative, snapshot.Files[relative])
	}
	snapshot.Fingerprint = fmt.Sprintf("%x", hash.Sum(nil))
	return snapshot, nil
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

func hashWorkspacePath(root, rel string) (string, error) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err != nil {
		return "", fmt.Errorf("git snapshot %s: %w", rel, err)
	}

	h := sha256.New()
	fmt.Fprintf(h, "mode\x00%d\x00", info.Mode())
	if info.Mode()&os.ModeSymlink != 0 {
		target, linkErr := os.Readlink(full)
		if linkErr != nil {
			return "", fmt.Errorf("git snapshot symlink %s: %w", rel, linkErr)
		}
		_, _ = io.WriteString(h, target)
		return fmt.Sprintf("%x", h.Sum(nil)), nil
	}
	if info.IsDir() {
		submoduleHead, submoduleErr := gitOutput(full, "rev-parse", "--verify", "HEAD")
		if submoduleErr == nil {
			_, _ = h.Write(submoduleHead)
		}
		return fmt.Sprintf("%x", h.Sum(nil)), nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Sprintf("%x", h.Sum(nil)), nil
	}
	f, openErr := os.Open(full)
	if openErr != nil {
		return "", fmt.Errorf("git snapshot open %s: %w", rel, openErr)
	}
	if _, copyErr := io.Copy(h, f); copyErr != nil {
		_ = f.Close()
		return "", fmt.Errorf("git snapshot read %s: %w", rel, copyErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		return "", fmt.Errorf("git snapshot close %s: %w", rel, closeErr)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
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
func findLoopbackTarget(names []string, before int, target string) int {
	for i := before - 1; i >= 0; i-- {
		if names[i] == target {
			return i
		}
	}
	return -1
}
