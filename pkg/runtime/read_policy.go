package runtime

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
)

// exactDeniedReadPaths validates controller-supplied per-stage exclusions.
// Paths are exact regular-file locations, not user-authored glob patterns.
func exactDeniedReadPaths(task *Task) ([]string, error) {
	if task == nil || len(task.DeniedReadPaths) == 0 {
		return nil, nil
	}
	return validateExactDeniedReadPaths(task.DeniedReadPaths)
}

func validateExactDeniedReadPaths(paths []string) ([]string, error) {
	seen := make(map[string]bool, len(paths))
	validated := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("stage read-deny path must be absolute and clean: %q", path)
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		validated = append(validated, path)
	}
	sort.Strings(validated)
	return validated, nil
}

func claudeReadDenyRule(path string) (string, error) {
	// Claude permission rules use a glob-like path expression inside
	// Read(...). These controller paths are literal filenames, so reject the
	// grammar characters rather than accidentally widening an exclusion.
	for _, character := range []rune{'*', '?', '(', ')'} {
		for _, actual := range path {
			if actual == character {
				return "", errors.New("stage read-deny path cannot be represented exactly by Claude permissions")
			}
		}
	}
	return "Read(//" + filepath.ToSlash(path)[1:] + ")", nil
}
