package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/worker"
)

func TestCheckControlRootDistinguishesUninitializedFromUnsafe(t *testing.T) {
	t.Run("не инициализирован", func(t *testing.T) {
		target := t.TempDir()
		err := checkControlRoot(target)
		if err == nil || !strings.Contains(err.Error(), "не инициализирован") {
			t.Fatalf("ожидалось сообщение про неинициализированный проект, получено: %v", err)
		}
		if strings.Contains(err.Error(), "небезопасный") {
			t.Fatalf("сообщение о неинициализированном проекте не должно упоминать небезопасность: %v", err)
		}
	})

	t.Run("небезопасен (symlink)", func(t *testing.T) {
		target := t.TempDir()
		realDir := filepath.Join(target, "elsewhere")
		if err := os.Mkdir(realDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realDir, filepath.Join(target, ".ai-team")); err != nil {
			t.Fatal(err)
		}
		err := checkControlRoot(target)
		if err == nil || !strings.Contains(err.Error(), "небезопасный") {
			t.Fatalf("ожидалось сообщение про небезопасный control root, получено: %v", err)
		}
		if strings.Contains(err.Error(), "не инициализирован") {
			t.Fatalf("сообщение о небезопасном control root не должно звучать как «не инициализирован»: %v", err)
		}
	})

	t.Run("валидный control root", func(t *testing.T) {
		target := t.TempDir()
		if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := checkControlRoot(target); err != nil {
			t.Fatalf("валидный control root не должен возвращать ошибку: %v", err)
		}
	})
}

func TestConfiguredWorkerProcessOptions(t *testing.T) {
	t.Run("default has no sandbox override", func(t *testing.T) {
		t.Setenv(worker.WorkerSandboxEnvVar, "")
		options, err := configuredWorkerProcessOptions()
		if err != nil || len(options) != 0 {
			t.Fatalf("default worker options = %d, %v; want none", len(options), err)
		}
	})

	t.Run("unknown sandbox fails closed", func(t *testing.T) {
		t.Setenv(worker.WorkerSandboxEnvVar, "unknown")
		if _, err := configuredWorkerProcessOptions(); err == nil || !strings.Contains(err.Error(), worker.WorkerSandboxEnvVar) {
			t.Fatalf("unknown sandbox selector must fail closed, got %v", err)
		}
	})

	t.Run("bubblewrap selector", func(t *testing.T) {
		t.Setenv(worker.WorkerSandboxEnvVar, "bubblewrap")
		options, err := configuredWorkerProcessOptions()
		if err != nil || len(options) != 1 {
			t.Fatalf("bubblewrap worker options = %d, %v; want one option", len(options), err)
		}
		target := t.TempDir()
		_, engineErr := worker.NewProcessEngine([]string{"worker"}, target, filepath.Join(target, "controller.db"), options...)
		_, bwrapErr := exec.LookPath("bwrap")
		if runtime.GOOS != "linux" || bwrapErr != nil {
			if engineErr == nil {
				t.Fatal("unsupported or unavailable bubblewrap selector must fail closed")
			}
			return
		}
		if engineErr != nil {
			t.Fatalf("available Linux bubblewrap option rejected: %v", engineErr)
		}
	})
}

func TestLocalWebTokenIsPersistedWithPrivatePermissions(t *testing.T) {
	target := t.TempDir()
	token, path, err := loadOrCreateLocalWebToken(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 || path != filepath.Join(target, localWebTokenRelativePath) {
		t.Fatalf("unexpected local token or path: token length=%d path=%q", len(token), path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("token permissions = %04o, want 0600", info.Mode().Perm())
	}
	second, secondPath, err := loadOrCreateLocalWebToken(target)
	if err != nil {
		t.Fatal(err)
	}
	if second != token || secondPath != path {
		t.Fatal("local token was not stable across restarts")
	}
	var output bytes.Buffer
	if err := printLocalWebToken(&output, token, path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), token) || !strings.Contains(output.String(), path) {
		t.Fatalf("local token output omitted its credential or file path: %q", output.String())
	}
}

func TestLocalWebTokenRejectsSymlinkAndLoosePermissions(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		target := t.TempDir()
		controlDir := filepath.Join(target, ".ai-team")
		if err := os.Mkdir(controlDir, 0o755); err != nil {
			t.Fatal(err)
		}
		victim := filepath.Join(target, "victim")
		if err := os.WriteFile(victim, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(controlDir, "web.token")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadOrCreateLocalWebToken(target); err == nil {
			t.Fatal("symlink token file must be rejected")
		}
		data, err := os.ReadFile(victim)
		if err != nil || string(data) != "secret\n" {
			t.Fatalf("symlink target was modified: data=%q err=%v", data, err)
		}
	})

	t.Run("loose permissions", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not expose POSIX permission bits")
		}
		target := t.TempDir()
		controlDir := filepath.Join(target, ".ai-team")
		if err := os.Mkdir(controlDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(controlDir, "web.token"), []byte(strings.Repeat("x", 43)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadOrCreateLocalWebToken(target); err == nil || !strings.Contains(err.Error(), "0600") {
			t.Fatalf("loose permissions must fail closed, got %v", err)
		}
	})
}

func TestConfiguredAgentRegistryPathsNormalizesPlugins(t *testing.T) {
	configDir := setUserConfigDirForTest(t)
	userAgents := filepath.Join(configDir, "ai-team", "agents")
	if err := os.MkdirAll(userAgents, 0755); err != nil {
		t.Fatal(err)
	}
	pluginPath := filepath.Join("relative", "plugins")
	t.Setenv("AI_TEAM_AGENT_PATH", pluginPath+string(os.PathListSeparator))
	paths := configuredAgentRegistryPaths()
	if len(paths) != 2 {
		t.Fatalf("agent registry paths = %v; want plugin and user config paths", paths)
	}
	wantPlugin, err := filepath.Abs(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if paths[0] != wantPlugin {
		t.Fatalf("plugin path = %q, want %q", paths[0], wantPlugin)
	}
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(userConfigDir, "ai-team", "agents"); paths[1] != want {
		t.Fatalf("user agent path = %q, want %q", paths[1], want)
	}
}

func TestConfiguredAgentRegistryPathsOptionalUserRegistry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
	}{
		{name: "missing is omitted"},
		{name: "existing is included", existing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := setUserConfigDirForTest(t)
			userAgents := filepath.Join(configDir, "ai-team", "agents")
			if tc.existing {
				if err := os.MkdirAll(userAgents, 0755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("AI_TEAM_AGENT_PATH", "")

			paths := configuredAgentRegistryPaths()
			if tc.existing {
				if len(paths) != 1 || paths[0] != userAgents {
					t.Fatalf("agent registry paths = %v; want existing user registry %q", paths, userAgents)
				}
				return
			}
			if len(paths) != 0 {
				t.Fatalf("agent registry paths = %v; absent optional user registry should be omitted", paths)
			}
		})
	}
}

func TestConfiguredAgentRegistryPathsRetainsMissingExplicitPlugin(t *testing.T) {
	configDir := setUserConfigDirForTest(t)
	missingPlugin := filepath.Join(t.TempDir(), "missing-plugin-agents")
	t.Setenv("AI_TEAM_AGENT_PATH", missingPlugin)
	paths := configuredAgentRegistryPaths()
	want, err := filepath.Abs(missingPlugin)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("agent registry paths = %v; explicit missing plugin must be preserved as %q (user config %q)", paths, want, configDir)
	}
}

func setUserConfigDirForTest(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", base)
		return base
	case "darwin":
		t.Setenv("HOME", base)
		return filepath.Join(base, "Library", "Application Support")
	case "plan9":
		t.Setenv("home", base)
		return filepath.Join(base, "lib")
	default:
		t.Setenv("XDG_CONFIG_HOME", base)
		return base
	}
}

func TestEnsureControlIgnoredUsesLocalGitExclude(t *testing.T) {
	target := t.TempDir()
	runGitTest(t, target, "init", "-b", "main")
	originalGitignore := []byte("vendor/\n")
	if err := os.WriteFile(filepath.Join(target, ".gitignore"), originalGitignore, 0644); err != nil {
		t.Fatal(err)
	}

	excludePath, err := ensureControlIgnored(target, false)
	if err != nil {
		t.Fatalf("ensure local exclude: %v", err)
	}
	if excludePath == "" {
		t.Fatal("ожидался разрешённый Git exclude path")
	}
	if _, err := ensureControlIgnored(target, false); err != nil {
		t.Fatalf("повторный ensure local exclude: %v", err)
	}

	gitignore, err := os.ReadFile(filepath.Join(target, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gitignore) != string(originalGitignore) {
		t.Fatalf(".gitignore изменён по умолчанию:\n%s", gitignore)
	}
	exclude, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(exclude), ".ai-team/"); count != 1 {
		t.Fatalf("правило должно быть записано один раз, получено %d:\n%s", count, exclude)
	}
	runGitTest(t, target, "check-ignore", "--no-index", ".ai-team/config.yaml")
}

func TestEnsureControlIgnoredCanWriteGitignore(t *testing.T) {
	target := t.TempDir()
	path, err := ensureControlIgnored(target, true)
	if err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if path != filepath.Join(target, ".gitignore") {
		t.Fatalf("неожиданный путь: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ".ai-team/") {
		t.Fatalf("правило отсутствует:\n%s", data)
	}
}

func TestEnsureControlIgnoredSupportsLinkedWorktree(t *testing.T) {
	repository := t.TempDir()
	runGitTest(t, repository, "init", "-b", "main")
	runGitTest(t, repository, "config", "user.name", "AI Team Test")
	runGitTest(t, repository, "config", "user.email", "ai-team@example.test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "README.md")
	runGitTest(t, repository, "commit", "-m", "initial")

	worktree := filepath.Join(t.TempDir(), "linked")
	runGitTest(t, repository, "worktree", "add", "-b", "feature", worktree)
	path, err := ensureControlIgnored(worktree, false)
	if err != nil {
		t.Fatalf("ensure linked worktree exclude: %v", err)
	}
	if path == "" {
		t.Fatal("ожидался exclude path linked worktree")
	}
	runGitTest(t, worktree, "check-ignore", "--no-index", ".ai-team/config.yaml")
}

func TestAppendIgnoreRuleRejectsSymlink(t *testing.T) {
	target := t.TempDir()
	actual := filepath.Join(target, "actual")
	if err := os.WriteFile(actual, []byte("keep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(target, "exclude")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}

	if err := appendIgnoreRule(link); err == nil {
		t.Fatal("ожидался отказ для symlink")
	}
	data, err := os.ReadFile(actual)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("данные за symlink изменены: %q", data)
	}
}

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
