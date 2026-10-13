package checks

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// trustedRepositoryPaths returns the controller checkout and every gitdir
// that may contain its configuration. Linked worktrees store their .git data
// outside the checkout, so the common directory must be found explicitly.
func trustedRepositoryPaths(repository string) ([]string, error) {
	root, err := canonicalDirectory(repository)
	if err != nil {
		return nil, fmt.Errorf("resolve trusted repository: %w", err)
	}
	paths := []string{root}
	gitEntry := filepath.Join(root, ".git")
	entryInfo, err := os.Lstat(gitEntry)
	if err != nil {
		return nil, fmt.Errorf("inspect trusted repository git metadata: %w", err)
	}
	adminDir := ""
	if entryInfo.IsDir() {
		adminDir, err = canonicalDirectory(gitEntry)
	} else {
		file, openErr := os.Open(gitEntry)
		if openErr != nil {
			return nil, fmt.Errorf("open trusted repository gitdir pointer: %w", openErr)
		}
		scanner := bufio.NewScanner(file)
		if !scanner.Scan() {
			_ = file.Close()
			return nil, fmt.Errorf("read trusted repository gitdir pointer: %w", scanner.Err())
		}
		line := strings.TrimSpace(scanner.Text())
		closeErr := file.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("close trusted repository gitdir pointer: %w", closeErr)
		}
		const prefix = "gitdir: "
		if !strings.HasPrefix(line, prefix) || strings.TrimSpace(strings.TrimPrefix(line, prefix)) == "" {
			return nil, fmt.Errorf("invalid trusted repository gitdir pointer")
		}
		adminDir = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if !filepath.IsAbs(adminDir) {
			adminDir = filepath.Join(root, adminDir)
		}
		adminDir, err = canonicalDirectory(adminDir)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve trusted repository gitdir: %w", err)
	}
	paths = append(paths, adminDir)
	if commonPath, commonErr := readCommonGitDir(adminDir); commonErr != nil {
		return nil, commonErr
	} else if commonPath != "" {
		paths = append(paths, commonPath)
		if filepath.Base(commonPath) == ".git" {
			// A linked checkout's common .git directory belongs to the parent
			// repository. Hide its working files as well as its Git config.
			parentRepo, parentErr := canonicalDirectory(filepath.Dir(commonPath))
			if parentErr != nil {
				return nil, fmt.Errorf("resolve parent repository for common gitdir: %w", parentErr)
			}
			paths = append(paths, parentRepo)
		}
	}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if value := strings.TrimSpace(os.Getenv(key)); filepath.IsAbs(value) {
			if canonical, pathErr := canonicalDirectory(value); pathErr == nil {
				paths = append(paths, canonical)
			}
		}
	}
	return compactPaths(paths), nil
}

func readCommonGitDir(adminDir string) (string, error) {
	path := filepath.Join(adminDir, "commondir")
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read trusted repository common gitdir: %w", err)
	}
	commonDir := strings.TrimSpace(string(contents))
	if commonDir == "" {
		return "", fmt.Errorf("trusted repository has an empty common gitdir")
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(adminDir, commonDir)
	}
	resolved, err := canonicalDirectory(commonDir)
	if err != nil {
		return "", fmt.Errorf("resolve trusted repository common gitdir: %w", err)
	}
	return resolved, nil
}

func canonicalDirectory(path string) (string, error) {
	resolved, err := canonicalExistingPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return resolved, nil
}

func canonicalExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// compactPaths removes duplicates and descendants already hidden by a parent.
func compactPaths(paths []string) []string {
	sorted := append([]string(nil), paths...)
	sort.Slice(sorted, func(i, j int) bool {
		if len(sorted[i]) == len(sorted[j]) {
			return sorted[i] < sorted[j]
		}
		return len(sorted[i]) < len(sorted[j])
	})
	result := make([]string, 0, len(sorted))
	for _, path := range sorted {
		path = filepath.Clean(path)
		covered := false
		for _, existing := range result {
			relative, err := filepath.Rel(existing, path)
			if err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, path)
		}
	}
	return result
}

// sandboxMaskedPaths lists host directories replaced by empty tmpfs mounts in
// the untrusted-check sandbox: every home root, the controller's own HOME and
// the trusted repository paths. Host /tmp is also replaced (by the private
// scratch directory) and is added by visibleSandboxTool's callers.
func sandboxMaskedPaths(protectedPaths []string) []string {
	privatePaths := []string{"/home", "/root", "/Users"}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && filepath.IsAbs(home) {
		privatePaths = append(privatePaths, filepath.Clean(home))
	}
	privatePaths = append(privatePaths, protectedPaths...)
	return compactPaths(privatePaths)
}

// checkSandboxToolVisible fails with an actionable error when a check tool
// resolved on the host lives in a directory the sandbox hides (for example
// ~/go/bin under a masked home). Without it, bubblewrap reports only a bare
// "No such file or directory" for the tool. Tools inside the PR checkout are
// mapped into the sandbox and always visible.
func checkSandboxToolVisible(target, executable string, hidden []string) error {
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve sandbox executable: %w", err)
	}
	if pathWithin(target, absolute) {
		return nil
	}
	candidates := []string{absolute}
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil && resolved != absolute {
		candidates = append(candidates, resolved)
	}
	for _, candidate := range candidates {
		for _, masked := range hidden {
			if masked != "" && pathWithin(masked, candidate) {
				return fmt.Errorf("check tool %s resolves to %s, which is hidden inside the untrusted-check sandbox (%s is masked); install the tool outside home and temporary directories (for example /usr/local/bin) or invoke it from the repository", executable, candidate, masked)
			}
		}
	}
	return nil
}

// pathWithin reports whether child equals parent or lies below it.
func pathWithin(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
