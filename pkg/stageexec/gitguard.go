package stageexec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxGitGuardEntries bounds the repository walk that looks for nested git
// metadata. A tree larger than this is refused rather than half-checked.
const maxGitGuardEntries = 500000

// checkRepoGitMetadata refuses a repository whose git metadata could make a
// container reach or alter anything outside the read-only mount. The mount
// itself is read-only (so `.git` and every nested gitfile cannot be written),
// but a nested `.git` symlink or a gitfile pointing out of the tree is a
// pointer someone planted: tools in the container would follow it. A gitfile
// (the top-level one of a linked worktree, or a submodule's inside a linked
// worktree) whose target is outside the mount is allowed: the target does not
// resolve in the container. A gitfile whose target is inside the mount must
// name an existing path reachable without a symlink; a missing target is
// refused. Entries below a node_modules directory are not counted against the
// limit (they are still inspected, so an existing nested .git there is
// checked). Residual risks: a container with a writable mount can create a
// new nested .git/hooks that the host would run later, and HFS+/NTFS short
// names (GIT~1) are not recognised as .git.
func checkRepoGitMetadata(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	topGit := filepath.Join(root, ".git")
	if info, err := os.Lstat(topGit); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New(".git в корне репозитория — символическая ссылка")
	}
	visited := 0
	return filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if !hasNodeModules(rel) {
			visited++
		}
		if visited > maxGitGuardEntries {
			return fmt.Errorf("в репозитории больше %d записей: проверка git-метаданных невозможна", maxGitGuardEntries)
		}
		if !strings.EqualFold(entry.Name(), ".git") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("вложенный %s — символическая ссылка", rel)
		}
		if entry.IsDir() {
			return filepath.SkipDir
		}
		if p == topGit {
			return nil
		}
		return checkNestedGitfile(root, p, rel)
	})
}

func checkNestedGitfile(root, p, rel string) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("gitfile %s: %w", rel, err)
	}
	if len(data) > 4096 {
		return fmt.Errorf("gitfile %s слишком велик", rel)
	}
	line := strings.TrimSpace(string(data))
	target, ok := strings.CutPrefix(line, "gitdir:")
	if !ok || strings.ContainsAny(line, "\n\x00") {
		return fmt.Errorf("gitfile %s не в формате «gitdir: <путь>»", rel)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("gitfile %s: пустой gitdir", rel)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(p), filepath.FromSlash(target))
	}
	target = filepath.Clean(target)
	inside, err := filepath.Rel(root, target)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return nil // outside the mount: it does not resolve in the container
	}
	current := root
	for _, part := range strings.Split(inside, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("gitfile %s указывает на недоступный путь внутри репозитория %s: %w", rel, inside, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("gitfile %s указывает внутрь репозитория через символическую ссылку %s", rel, inside)
		}
	}
	return nil
}

// hasNodeModules reports whether a path has a node_modules element.
func hasNodeModules(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "node_modules" {
			return true
		}
	}
	return false
}

// hasGitElement reports whether a slash-separated path names git metadata.
func hasGitElement(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}
