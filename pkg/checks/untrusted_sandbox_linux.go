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

type bubblewrapSandbox struct {
	bwrap    string
	tmp      string
	root     string
	modCache string
}

// NewUntrustedCommandSandbox creates a Linux sandbox for executing project
// checks over untrusted PR contents. Failure to locate bubblewrap is a hard
// error; callers must block rather than run commands without confinement.
func NewUntrustedCommandSandbox() (CommandSandbox, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("Linux untrusted checks require bubblewrap: %w", err)
	}
	root, err := os.MkdirTemp("", "ai-team-pr-check-sandbox-")
	if err != nil {
		return nil, fmt.Errorf("prepare private sandbox directory: %w", err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("secure private sandbox directory: %w", err)
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
	return &bubblewrapSandbox{
		bwrap:    bwrap,
		tmp:      tmp,
		root:     root,
		modCache: modCache,
	}, nil
}

func (s *bubblewrapSandbox) Enforced() bool { return s != nil && s.bwrap != "" && s.root != "" }

func (s *bubblewrapSandbox) Close() error {
	if s == nil || s.root == "" {
		return nil
	}
	return os.RemoveAll(s.root)
}

func (s *bubblewrapSandbox) Command(ctx context.Context, workingDir, executable string, args ...string) (*exec.Cmd, error) {
	if s == nil || s.bwrap == "" || s.root == "" {
		return nil, errors.New("bubblewrap sandbox is not initialized")
	}
	worktree, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox working directory: %w", err)
	}
	if !filepath.IsAbs(worktree) || worktree == string(filepath.Separator) {
		return nil, errors.New("sandbox working directory must be a non-root absolute path")
	}
	info, err := os.Stat(worktree)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return nil, fmt.Errorf("inspect sandbox working directory: %w", err)
	}
	privateTmp := filepath.Join(s.root, "tmp")

	argv := []string{
		"--die-with-parent", "--new-session", "--unshare-user", "--unshare-pid",
		"--unshare-ipc", "--unshare-uts", "--unshare-net",
		"--ro-bind", "/", "/", "--tmpfs", "/run", "--dev", "/dev", "--proc", "/proc",
	}
	for _, privatePath := range []string{"/home", "/root", "/Users"} {
		if info, statErr := os.Stat(privatePath); statErr == nil && info.IsDir() {
			argv = append(argv, "--tmpfs", privatePath)
		}
	}
	argv = append(argv, "--bind", worktree, worktree, "--bind", privateTmp, "/tmp")
	if s.modCache != "" {
		argv = append(argv, "--ro-bind", s.modCache, "/tmp/modcache")
	}
	argv = append(argv, "--chdir", worktree, "--clearenv")
	environment := sanitizedCheckEnvironment(os.Environ())
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		if key == "HOME" || key == "TMP" || key == "TMPDIR" || key == "TEMP" || key == "GOPATH" || key == "GOCACHE" || key == "GOMODCACHE" || key == "GOTMPDIR" {
			continue
		}
		argv = append(argv, "--setenv", key, value)
	}
	argv = append(argv,
		"--setenv", "HOME", "/tmp/home",
		"--setenv", "TMP", "/tmp",
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "TEMP", "/tmp",
		"--setenv", "GOPATH", "/tmp/gopath",
		"--setenv", "GOCACHE", "/tmp/gocache",
		"--setenv", "GOMODCACHE", "/tmp/modcache",
		"--", executable,
	)
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, s.bwrap, argv...)
	cmd.Dir = worktree
	cmd.Env = sanitizedCheckEnvironment(os.Environ())
	return cmd, nil
}
