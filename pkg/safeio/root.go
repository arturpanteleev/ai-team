package safeio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PinnedRoot is a filesystem root resolved once through EvalSymlinks. It makes
// the "check, then launch" window explicit: a container or process must only
// ever see the directory that was actually inspected.
type PinnedRoot struct {
	shown string      // cleaned absolute path supplied by the caller
	real  string      // EvalSymlinks(shown)
	info  os.FileInfo // identity of the real directory, to catch same-path replacement
}

// PinRoot resolves path into a PinnedRoot. A path whose final component is a
// symlink is refused: the launcher would follow the link and mount a different
// tree than the one that was checked. Symlinks in ancestor directories are
// resolved into Real, which callers must use for walks and bind-mount sources
// so that exactly the checked tree is handed to the program.
func PinRoot(path string) (PinnedRoot, error) {
	shown, err := filepath.Abs(path)
	if err != nil {
		return PinnedRoot{}, err
	}
	shown = filepath.Clean(shown)
	info, err := os.Lstat(shown)
	if err != nil {
		return PinnedRoot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return PinnedRoot{}, fmt.Errorf("root %s is a symlink", shown)
	}
	real, err := filepath.EvalSymlinks(shown)
	if err != nil {
		return PinnedRoot{}, err
	}
	info, err = os.Stat(real)
	if err != nil {
		return PinnedRoot{}, err
	}
	if !info.IsDir() {
		return PinnedRoot{}, fmt.Errorf("root %s is not a directory", shown)
	}
	return PinnedRoot{shown: shown, real: real, info: info}, nil
}

// Real returns the symlink-free absolute path of the pinned root.
func (p PinnedRoot) Real() string { return p.real }

// Shown returns the cleaned absolute path supplied by the caller.
func (p PinnedRoot) Shown() string { return p.shown }

// Verify fails if the root no longer names the same real directory or if its
// final component became a symlink. Call it immediately before launching a
// process or container; on error the launch must not proceed (fail closed).
func (p PinnedRoot) Verify() error {
	info, err := os.Lstat(p.shown)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("root %s became a symlink", p.shown)
	}
	now, err := filepath.EvalSymlinks(p.shown)
	if err != nil {
		return err
	}
	if now != p.real {
		return fmt.Errorf("root %s now resolves to %s, checked %s", p.shown, now, p.real)
	}
	cur, err := os.Stat(now)
	if err != nil {
		return err
	}
	if !os.SameFile(p.info, cur) {
		return fmt.Errorf("root %s was replaced after the check", p.shown)
	}
	return nil
}

// IsGitMetadataName reports whether name names git metadata. Besides case
// variants of ".git" it recognises the 8.3 short names ("GIT~1", ".GIT~2",
// "git~12") that HFS+ and NTFS can expose for the same directory: those are
// the same metadata under a different spelling and must not slip past a
// check that only compares the name with ".git".
func IsGitMetadataName(name string) bool {
	if strings.EqualFold(name, ".git") {
		return true
	}
	name = strings.TrimPrefix(name, ".")
	if len(name) < 5 || !strings.EqualFold(name[:3], "git") || name[3] != '~' {
		return false
	}
	for i := 4; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}
