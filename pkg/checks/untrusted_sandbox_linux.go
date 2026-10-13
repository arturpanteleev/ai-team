//go:build linux

package checks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sandboxWorkspace is where the PR checkout appears inside bubblewrap. It lives
// under the private /run tmpfs because the read-only host root cannot gain new
// top-level mount points.
const sandboxWorkspace = "/run/ai-team/workspace"

type bubblewrapSandbox struct {
	bwrap          string
	tmp            string
	root           string
	target         string
	modCache       string
	protectedPaths []string
}

// NewUntrustedCommandSandbox creates a Linux sandbox for executing project
// checks over untrusted PR contents. Failure to locate bubblewrap is a hard
// error; callers must block rather than run commands without confinement.
func NewUntrustedCommandSandbox(targetDir, trustedRepoDir string) (CommandSandbox, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("Linux untrusted checks require bubblewrap: %w", err)
	}
	// Use a fixed path so TMPDIR cannot place sandbox scratch files in an
	// otherwise-visible controller directory. The sandbox later replaces /tmp.
	root, err := os.MkdirTemp("/tmp", "ai-team-pr-check-sandbox-")
	if err != nil {
		return nil, fmt.Errorf("prepare private sandbox directory: %w", err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("secure private sandbox directory: %w", err)
	}
	target, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("resolve PR checkout for sandbox: %w", err)
	}
	if !filepath.IsAbs(target) || target == string(filepath.Separator) {
		_ = os.RemoveAll(root)
		return nil, errors.New("sandbox checkout must be a non-root absolute path")
	}
	if info, statErr := os.Stat(target); statErr != nil || !info.IsDir() {
		_ = os.RemoveAll(root)
		if statErr == nil {
			statErr = errors.New("not a directory")
		}
		return nil, fmt.Errorf("inspect PR checkout for sandbox: %w", statErr)
	}
	protectedPaths, err := trustedRepositoryPaths(trustedRepoDir)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	for _, path := range protectedPaths {
		if path == string(filepath.Separator) {
			_ = os.RemoveAll(root)
			return nil, errors.New("refusing to mask the filesystem root as a trusted repository")
		}
	}
	tmp := filepath.Join(root, "tmp")
	if err := os.Mkdir(tmp, 0700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("prepare private sandbox tmp: %w", err)
	}
	for _, name := range []string{"home", "gopath", "gocache", "modcache"} {
		if err := os.Mkdir(filepath.Join(tmp, name), 0700); err != nil {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("prepare private sandbox %s: %w", name, err)
		}
	}
	// Go's module cache is useful build input, but remains read-only and outside
	// the controller's HOME. No other host-home content is exposed.
	modCache := os.Getenv("GOMODCACHE")
	if modCache == "" {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			modCache = filepath.Join(home, "go", "pkg", "mod")
		}
	}
	if info, statErr := os.Stat(modCache); statErr == nil && info.IsDir() {
		modCache, err = filepath.EvalSymlinks(modCache)
		if err != nil {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("resolve Go module cache: %w", err)
		}
	} else {
		modCache = ""
	}
	for _, protected := range protectedPaths {
		if pathsOverlap(modCache, protected) {
			modCache = ""
			break
		}
	}
	if pathsOverlap(modCache, "/tmp") {
		modCache = ""
	}
	return &bubblewrapSandbox{
		bwrap:          bwrap,
		tmp:            tmp,
		root:           root,
		target:         target,
		modCache:       modCache,
		protectedPaths: protectedPaths,
	}, nil
}

func (s *bubblewrapSandbox) Enforced() bool { return s != nil && s.bwrap != "" && s.root != "" }

func (s *bubblewrapSandbox) Close() error {
	if s == nil || s.root == "" {
		return nil
	}
	return os.RemoveAll(s.root)
}

func (s *bubblewrapSandbox) Command(ctx context.Context, targetDir, workingDir, executable string, args ...string) (*exec.Cmd, error) {
	if s == nil || s.bwrap == "" || s.root == "" || s.target == "" {
		return nil, errors.New("bubblewrap sandbox is not initialized")
	}
	target, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox checkout: %w", err)
	}
	if target != s.target {
		return nil, errors.New("sandbox command target differs from initialized PR checkout")
	}
	worktree, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox working directory: %w", err)
	}
	relativeWorktree, err := filepath.Rel(s.target, worktree)
	if err != nil || relativeWorktree == ".." || strings.HasPrefix(relativeWorktree, ".."+string(filepath.Separator)) {
		return nil, errors.New("sandbox working directory escaped the PR checkout")
	}
	info, err := os.Stat(worktree)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return nil, fmt.Errorf("inspect sandbox working directory: %w", err)
	}
	privateTmp := filepath.Join(s.root, "tmp")
	guestWorkingDir := filepath.Join(sandboxWorkspace, relativeWorktree)
	guestExecutable, err := mapSandboxPath(s.target, executable)
	if err != nil {
		return nil, err
	}

	argv := []string{
		"--die-with-parent", "--new-session", "--unshare-user", "--unshare-pid",
		"--unshare-ipc", "--unshare-uts", "--unshare-net",
		"--ro-bind", "/", "/", "--tmpfs", "/run", "--dev", "/dev", "--proc", "/proc",
		"--bind", s.target, sandboxWorkspace,
	}
	argv = append(argv, "--bind", privateTmp, "/tmp")
	if s.modCache != "" {
		argv = append(argv, "--ro-bind", s.modCache, "/tmp/modcache")
	}
	// Hide controller state and custom scratch directories. The isolated
	// candidate checkout has its own .git directory and does not need the
	// controller's common gitdir.
	privatePaths := sandboxMaskedPaths(s.protectedPaths)
	if err := checkSandboxToolVisible(s.target, executable, append(append([]string(nil), privatePaths...), "/tmp")); err != nil {
		return nil, err
	}
	for _, privatePath := range privatePaths {
		if privatePath == "/tmp" || strings.HasPrefix(privatePath, "/tmp"+string(filepath.Separator)) {
			// The private /tmp mount below hides every host path here.
			continue
		}
		if info, statErr := os.Stat(privatePath); statErr == nil && info.IsDir() {
			argv = append(argv, "--tmpfs", privatePath)
		}
	}
	argv = append(argv, "--chdir", guestWorkingDir, "--clearenv")
	environment := sanitizedCheckEnvironment(os.Environ())
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		if key == "HOME" || key == "TMP" || key == "TMPDIR" || key == "TEMP" || key == "PWD" || key == "GOPATH" || key == "GOCACHE" || key == "GOMODCACHE" || key == "GOTMPDIR" {
			continue
		}
		argv = append(argv, "--setenv", key, value)
	}
	argv = append(argv,
		"--setenv", "PWD", guestWorkingDir,
		"--setenv", "HOME", "/tmp/home",
		"--setenv", "TMP", "/tmp",
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "TEMP", "/tmp",
		"--setenv", "GOPATH", "/tmp/gopath",
		"--setenv", "GOCACHE", "/tmp/gocache",
		"--setenv", "GOMODCACHE", "/tmp/modcache",
	)
	argv = append(argv, "--", guestExecutable)
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, s.bwrap, argv...)
	cmd.Dir = s.target
	cmd.Env = sanitizedCheckEnvironment(os.Environ())
	return cmd, nil
}

func pathsOverlap(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	within := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
	}
	return within(left, right) || within(right, left)
}

func mapSandboxPath(target, value string) (string, error) {
	resolved, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox executable: %w", err)
	}
	relative, err := filepath.Rel(target, resolved)
	if err != nil {
		return "", fmt.Errorf("map sandbox executable: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return resolved, nil
	}
	if relative == "." {
		return sandboxWorkspace, nil
	}
	return filepath.Join(sandboxWorkspace, relative), nil
}
