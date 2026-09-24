package delivery

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// hookNames — весь набор git hooks, который способен сработать в пути
// доставки: commit (pre-commit, prepare-commit-msg, commit-msg, post-commit),
// смена ветки (post-checkout), обновление ссылок (reference-transaction,
// post-index-change) и push (pre-push).
var hookNames = []string{
	"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit",
	"post-checkout", "reference-transaction", "post-index-change", "pre-push",
}

// plantHooks подсаживает в репозиторий hooks, которые при исполнении
// дописывают своё имя в файл ВНЕ рабочего дерева: запись внутрь worktree
// сломала бы workspace digest и тест падал бы по другой причине.
func plantHooks(t *testing.T, repo, hooksDir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	marker := filepath.Join(t.TempDir(), "fired.log")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range hookNames {
		script := "#!/bin/sh\necho " + name + " >> " + marker + "\nexit 0\n"
		path := filepath.Join(hooksDir, name)
		writeFile(t, path, script)
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return marker
}

func firedHooks(t *testing.T, marker string) []string {
	t.Helper()
	data, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var fired []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line = strings.TrimSpace(line); line != "" && !seen[line] {
			seen[line] = true
			fired = append(fired, line)
		}
	}
	sort.Strings(fired)
	return fired
}

// TestControllerNeverExecutesRepositoryHooks — воспроизведение QS-04: агент
// кладёт .git/hooks/pre-push, человек одобряет план, контроллер делает push и
// исполняет подсаженный код. Доставка обязана пройти полностью и не запустить
// ни один hook репозитория.
func TestControllerNeverExecutesRepositoryHooks(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	marker := plantHooks(t, repo, filepath.Join(repo, ".git", "hooks"))

	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// hooks must not run\n")
	plan, err := BuildPlan(context.Background(), repo, "hooks", "hooks", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "hooks", Plan: plan})
	if err != nil {
		t.Fatalf("доставка должна пройти при подсаженных hooks: %v", err)
	}
	if result.CommitSHA == "" || result.PRURL == "" {
		t.Fatalf("доставка не доведена до конца: %+v", result)
	}
	if fired := firedHooks(t, marker); len(fired) > 0 {
		t.Fatalf("контроллер исполнил hooks репозитория: %v", fired)
	}
}

// TestControllerNeverExecutesRelocatedHooks закрывает обход через
// core.hooksPath: агент может не трогать .git/hooks, а перенести каталог
// hooks куда угодно одной строкой в .git/config.
func TestControllerNeverExecutesRelocatedHooks(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	relocated := filepath.Join(t.TempDir(), "relocated-hooks")
	marker := plantHooks(t, repo, relocated)
	git(t, repo, "config", "core.hooksPath", relocated)

	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// relocated hooks must not run\n")
	plan, err := BuildPlan(context.Background(), repo, "relocated", "relocated", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "relocated", Plan: plan}); err != nil {
		t.Fatalf("доставка должна пройти при перенесённом каталоге hooks: %v", err)
	}
	if fired := firedHooks(t, marker); len(fired) > 0 {
		t.Fatalf("контроллер исполнил hooks из core.hooksPath: %v", fired)
	}
}

// TestControllerNeverExecutesFsmonitor закрывает core.fsmonitor: это не hook
// в смысле .git/hooks, а произвольная команда, которую git запускает при
// каждом обновлении индекса (add, diff --cached, ls-files, check-attr).
func TestControllerNeverExecutesFsmonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	repo, _ := setupRepository(t)
	installFakeGH(t)
	marker := filepath.Join(t.TempDir(), "fired.log")
	probe := filepath.Join(t.TempDir(), "fsmonitor.sh")
	writeFile(t, probe, "#!/bin/sh\necho core.fsmonitor >> "+marker+"\nexit 1\n")
	if err := os.Chmod(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "core.fsmonitor", probe)

	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// fsmonitor must not run\n")
	plan, err := BuildPlan(context.Background(), repo, "fsmonitor", "fsmonitor", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "fsmonitor", Plan: plan}); err != nil {
		t.Fatalf("доставка должна пройти при подсаженном core.fsmonitor: %v", err)
	}
	if fired := firedHooks(t, marker); len(fired) > 0 {
		t.Fatalf("контроллер исполнил core.fsmonitor: %v", fired)
	}
}

// TestControllerRefusesExtTransport закрывает ext:: remote helper: git
// исполняет shell-команду прямо из remote url, если .git/config разрешает
// транспорт через protocol.ext.allow=always.
func TestControllerRefusesExtTransport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	repo, _ := setupRepository(t)
	installFakeGH(t)
	marker := filepath.Join(t.TempDir(), "fired.log")
	helper := filepath.Join(t.TempDir(), "ext.sh")
	writeFile(t, helper, "#!/bin/sh\necho ext-transport >> "+marker+"\nexit 1\n")
	if err := os.Chmod(helper, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "protocol.ext.allow", "always")
	git(t, repo, "remote", "set-url", "origin", "ext::"+helper)

	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// ext transport must not run\n")
	plan, err := BuildPlan(context.Background(), repo, "ext", "ext", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	// Push обязан упасть: ext-транспорт запрещён, легального remote нет.
	if _, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "ext", Plan: plan}); err == nil {
		t.Fatal("доставка через ext:: remote должна быть отклонена")
	}
	if fired := firedHooks(t, marker); len(fired) > 0 {
		t.Fatalf("контроллер исполнил ext:: remote helper: %v", fired)
	}
}

func TestGitSafeArgsPrecedeSubcommand(t *testing.T) {
	args := hardenedGitArgs("push", "-u", "origin", "ai-team/feat")
	if args[len(args)-4] != "push" {
		t.Fatalf("подкоманда должна остаться последней группой: %v", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-c core.hooksPath=/dev/null") {
		t.Fatalf("hooks не отключены: %v", args)
	}
	for index := 0; index < len(args)-4; index += 2 {
		if args[index] != "-c" {
			t.Fatalf("hardening должен состоять только из -c пар: %v", args)
		}
	}
}

// TestGitExecutionSurfaceIsRecorded — вторая половина QS-04 в той части,
// которую можно закрыть честно: подсаженный hook больше не исполняется, но и
// невидимым он быть не должен. `.git` исключён из workspace digest, поэтому
// единственное место, где факт может быть зафиксирован, — манифест доставки.
func TestGitExecutionSurfaceIsRecorded(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	plantHooks(t, repo, filepath.Join(repo, ".git", "hooks"))
	git(t, repo, "config", "credential.helper", "!f() { echo password=hunter2; }; f")

	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// surface\n")
	plan, err := BuildPlan(context.Background(), repo, "surface", "surface", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "surface", Plan: plan})
	if err != nil {
		t.Fatalf("доставка должна пройти: %v", err)
	}
	var reason string
	var found bool
	for _, step := range result.Steps {
		if step.Step == "inspect_git_execution_surface" {
			reason, found = step.Reason, true
		}
	}
	if !found {
		t.Fatal("шаг inspect_git_execution_surface отсутствует в манифесте")
	}
	for _, expected := range []string{"pre-push", "credential.helper"} {
		if !strings.Contains(reason, expected) {
			t.Fatalf("манифест не фиксирует %q: %s", expected, reason)
		}
	}
	// Значения таких ключей содержат секреты и в evidence попадать не должны.
	if strings.Contains(reason, "hunter2") {
		t.Fatalf("значение config-ключа утекло в манифест: %s", reason)
	}
}

func TestRepoScopedExecutionKeysSelectsRepositoryScopeOnly(t *testing.T) {
	record := func(scope, origin, key, value string) string {
		return scope + "\x00" + origin + "\x00" + key + "\n" + value + "\x00"
	}
	output := record("system", "file:/etc/gitconfig", "credential.helper", "osxkeychain") +
		record("global", "file:/home/u/.gitconfig", "core.sshCommand", "ssh -i /home/u/.ssh/id") +
		record("local", "file:.git/config", "remote.origin.url", "git@example.test:x.git") +
		record("local", "file:.git/config", "core.hooksPath", "/tmp/evil") +
		// include.path раскрывается git'ом, scope остаётся local — ключ обязан
		// быть виден, иначе обход состоит из одной строки в .git/config.
		record("local", "file:.git/included", "core.fsmonitor", "/tmp/evil.sh") +
		record("worktree", "file:.git/config.worktree", "filter.Evil.CLEAN", "/tmp/evil.sh") +
		record("local", "file:.git/config", "branch.main.remote", "origin")

	keys := repoScopedExecutionKeys(output)
	want := []string{"core.fsmonitor", "core.hookspath", "filter.evil.clean"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("ожидались %v, получены %v", want, keys)
	}
}

func TestExecutionConfigKeyClassification(t *testing.T) {
	dangerous := []string{
		"core.hooksPath", "core.fsmonitor", "core.sshCommand", "core.pager",
		"gpg.program", "gpg.ssh.program", "credential.helper",
		"credential.https://example.test.helper", "filter.lfs.process",
		"diff.external", "diff.jpg.textconv", "merge.ours.driver",
		"url.https://token@host/.insteadOf", "protocol.ext.allow",
		"remote.origin.uploadpack",
	}
	for _, key := range dangerous {
		if !executionConfigKey(key) {
			t.Fatalf("ключ %q должен считаться исполняющим команду", key)
		}
	}
	safe := []string{
		"remote.origin.url", "branch.main.merge", "core.bare", "user.email",
		"commit.gpgSign", "diff.colorMoved", "filter.lfs.required",
		"alias.st", "http.proxy",
	}
	for _, key := range safe {
		if executionConfigKey(key) {
			t.Fatalf("ключ %q не исполняет команду и не должен попадать в список", key)
		}
	}
}

func TestPresentHookNamesIgnoresSamplesAndNonExecutable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pre-push.sample"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(dir, "README"), "not a hook\n")
	writeFile(t, filepath.Join(dir, "pre-push"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	if names := presentHookNames(dir); len(names) != 1 || names[0] != "pre-push" {
		t.Fatalf("ожидался только исполняемый pre-push, получено %v", names)
	}
}
