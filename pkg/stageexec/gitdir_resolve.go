package stageexec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveGitdir resolves the directory a gitfile names the way the kernel
// would, one component at a time: ".." applies to the real (symlink-free)
// directory reached so far, never to the lexical text. It reports whether any
// symlink was followed and whether the final path exists. A path that cannot
// be resolved unambiguously (a ".." after a component that does not exist, a
// symlink that cannot be evaluated) is an error: the caller must refuse it.
func resolveGitdir(fileDir, target string) (real string, viaSymlink, exists bool, err error) {
	cur := fileDir
	if filepath.IsAbs(target) {
		cur = string(filepath.Separator)
		if vol := filepath.VolumeName(target); vol != "" {
			cur = vol + string(filepath.Separator)
		}
	}
	if cur, err = filepath.EvalSymlinks(cur); err != nil {
		return "", false, false, err
	}
	parts := strings.FieldsFunc(target, func(r rune) bool { return r == '/' || r == filepath.Separator })
	exists = true
	for i, part := range parts {
		switch part {
		case ".":
			continue
		case "..":
			if !exists {
				return "", false, false, errors.New(`".." after a path component that does not exist`)
			}
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		if !exists {
			cur = next
			continue
		}
		info, lerr := os.Lstat(next)
		if lerr != nil {
			if !errors.Is(lerr, os.ErrNotExist) {
				return "", false, false, lerr
			}
			for _, rest := range parts[i+1:] {
				if rest == ".." {
					return "", false, false, errors.New(`".." after a path component that does not exist`)
				}
			}
			exists = false
			cur = next
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			viaSymlink = true
			resolved, rerr := filepath.EvalSymlinks(next)
			if rerr != nil {
				return "", false, false, fmt.Errorf("symlink %s: %w", part, rerr)
			}
			next = resolved
		}
		cur = next
	}
	return filepath.Clean(cur), viaSymlink, exists, nil
}
