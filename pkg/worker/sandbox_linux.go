//go:build linux

package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func checkBubblewrapAvailable() error {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("Linux worker isolation requires bubblewrap (bwrap): %w", err)
	}
	return nil
}

func bubblewrapWorkerCommand(ctx context.Context, worker *exec.Cmd, target, dbPath string, agentPaths, environment []string) (*exec.Cmd, error) {
	canonicalTarget, err := resolveBubblewrapTarget(target)
	if err != nil {
		return nil, err
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap unavailable: %w", err)
	}
	env := envMap(environment)
	home, temp := env["HOME"], env["TMPDIR"]
	if home == "" || temp == "" || !filepath.IsAbs(home) || !filepath.IsAbs(temp) {
		return nil, errors.New("worker HOME and TMPDIR must be absolute for bubblewrap")
	}
	args := []string{
		"--die-with-parent", "--new-session",
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--ro-bind", "/", "/",
		"--dev", "/dev", "--proc", "/proc",
		"--bind", canonicalTarget, canonicalTarget,
		"--bind", home, home,
		"--bind", temp, temp,
	}
	for _, path := range agentPaths {
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return nil, fmt.Errorf("agent registry path %q: %w", path, resolveErr)
		}
		args = append(args, "--ro-bind", resolved, resolved)
	}
	// Lifecycle checkpoints and legacy file approvals are controller-owned.
	// Their directories are overlaid with private tmpfs mounts after the target
	// bind, so neither target access nor the controller's host paths expose them.
	lifecycleDir := filepath.Join(canonicalTarget, ".ai-team", "state", "runs")
	if err := appendPrivateDirectoryMount(&args, lifecycleDir, true); err != nil {
		return nil, err
	}
	legacyApprovalsDir := filepath.Join(canonicalTarget, ".ai-team", "state", "approvals")
	if err := appendPrivateDirectoryMount(&args, legacyApprovalsDir, false); err != nil {
		return nil, err
	}
	resolvedDB, err := resolveExistingPath(dbPath)
	if err != nil {
		return nil, fmt.Errorf("controller database path: %w", err)
	}
	// SQLite may use WAL, SHM, or rollback journal sidecars. Mounting /dev/null
	// over each exact path prevents file contents from reaching the worker;
	// workers receive controller operations through the scoped API instead.
	protectedPaths := make([]string, 0, 4)
	for _, path := range []string{resolvedDB, resolvedDB + "-wal", resolvedDB + "-shm", resolvedDB + "-journal"} {
		canonical, checkErr := resolveAndCheckDatabasePath(path)
		if checkErr != nil {
			return nil, checkErr
		}
		protectedPaths = append(protectedPaths, canonical)
	}
	for _, path := range protectedPaths {
		args = append(args, "--ro-bind", "/dev/null", path)
	}
	args = append(args, "--chdir", canonicalTarget, "--")
	args = append(args, worker.Path)
	args = append(args, worker.Args[1:]...)
	return exec.CommandContext(ctx, bwrap, args...), nil
}

// resolveBubblewrapTarget canonicalizes the writable workspace bind. Binding
// the host filesystem root read-write would undo the preceding read-only root
// mount, so reject every spelling/symlink that resolves to it.
func resolveBubblewrapTarget(target string) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve worker target %q: %w", target, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve worker target %q: %w", target, err)
	}
	resolved = filepath.Clean(resolved)
	if resolved == string(filepath.Separator) {
		return "", errors.New("worker target must not resolve to the filesystem root")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect worker target %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("worker target %q must be a directory", resolved)
	}
	return resolved, nil
}

// resolveAndCheckDatabasePath canonicalizes an existing DB/sidecar before it
// is masked. A second hard link would leave the same bytes reachable at an
// unmasked pathname, so refuse to launch when an existing file has aliases.
func resolveAndCheckDatabasePath(path string) (string, error) {
	resolved, err := resolveExistingPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve controller database path %q: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return resolved, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect controller database path %q: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("controller database path %q must be a regular file", resolved)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("cannot verify hard-link count for controller database path %q", resolved)
	}
	if stat.Nlink > 1 {
		return "", fmt.Errorf("controller database path %q has %d hard links; refusing bubblewrap launch because an alias could bypass masking", resolved, stat.Nlink)
	}
	return resolved, nil
}

func appendPrivateDirectoryMount(args *[]string, path string, required bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect private worker path %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("private worker path %q must be a real directory", path)
	}
	if err := verifyPrivateDirectory(path); err != nil {
		return err
	}
	*args = append(*args, "--tmpfs", path)
	return nil
}

// verifyPrivateDirectory rejects aliases that would remain readable elsewhere
// after the directory itself is hidden by a tmpfs mount.
func verifyPrivateDirectory(path string) error {
	return filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect private worker path %q: %w", current, err)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("private worker path %q must contain only regular files and directories", current)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot verify hard-link count for private worker path %q", current)
		}
		if stat.Nlink > 1 {
			return fmt.Errorf("private worker path %q has %d hard links; refusing bubblewrap launch because an alias could bypass masking", current, stat.Nlink)
		}
		return nil
	})
}

func envMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func resolveExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("resolve database parent: %w", err)
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}
