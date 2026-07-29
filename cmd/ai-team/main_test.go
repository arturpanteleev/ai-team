package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
