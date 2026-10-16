package delivery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Недоверие к state-файлу и граничные случаи повторной доставки.

func deliverOnce(t *testing.T, repo, feature, task, content string) (Plan, Result) {
	t.Helper()
	writeFile(t, filepath.Join(repo, "a.go"), content)
	plan, err := BuildPlan(context.Background(), repo, feature, task, []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: feature, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	return plan, result
}

func countCalls(t *testing.T, calls, prefix string) int {
	t.Helper()
	data, err := os.ReadFile(calls)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == prefix {
			count++
		}
	}
	return count
}

func replanAfterEdit(t *testing.T, repo, content string) error {
	t.Helper()
	writeFile(t, filepath.Join(repo, "a.go"), content)
	_, err := BuildPlan(context.Background(), repo, "feat", "next", []string{"a.go"}, testVerification(t, repo))
	return err
}

func statePathFor(repo string) string {
	return filepath.Join(repo, ".ai-team", "delivery", "feat.json")
}

func readState(t *testing.T, repo string) *state {
	t.Helper()
	data, err := os.ReadFile(statePathFor(repo))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := parseState(data)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func expectRefusal(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ожидался отказ с %q, got %v", want, err)
	}
}

// Агент делает произвольный commit X и подделывает state (валидный plan, hash,
// commit_sha, pushed, pr_url) без ключа контроллера: X не принимается.
func TestRedeliveryRejectsForgedState(t *testing.T) {
	repo, _ := setupRepository(t)
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// agent\n")
	plan, err := BuildPlan(context.Background(), repo, "feat", "change", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo, "switch", "-c", "ai-team/feat")
	git(t, repo, "add", "a.go")
	git(t, repo, "commit", "-m", plan.CommitMessage)
	sha := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	hash, err := plan.Hash()
	if err != nil {
		t.Fatal(err)
	}
	forged := &state{SchemaVersion: deliveryStateSchemaVersion, PlanHash: hash, Plan: plan, CommitSHA: sha, CommitVerified: true, Pushed: true, PRURL: "https://example.test/pull/1"}
	if err := os.MkdirAll(filepath.Dir(statePathFor(repo)), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(mac string) {
		forged.MAC = mac
		data, marshalErr := json.Marshal(forged)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := os.WriteFile(statePathFor(repo), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("")
	expectRefusal(t, replanAfterEdit(t, repo, "package a\n// agent\n// more\n"), "не подписан")
	write(strings.Repeat("ab", 32))
	expectRefusal(t, replanAfterEdit(t, repo, "package a\n// agent\n// more\n"), "подпись delivery state неверна")

	// Подпись чужим ключом тоже отвергается.
	original := os.Getenv(stateKeyDirEnv)
	t.Setenv(stateKeyDirEnv, t.TempDir())
	forged.MAC = ""
	if err := writeState(statePathFor(repo), forged); err != nil {
		t.Fatal(err)
	}
	t.Setenv(stateKeyDirEnv, original)
	expectRefusal(t, replanAfterEdit(t, repo, "package a\n// agent\n// more\n"), "подпись delivery state неверна")
}

func TestRedeliveryRejectsStateWithoutPR(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	deliverOnce(t, repo, "feat", "change", "package a\n// first\n")
	loaded := readState(t, repo)
	loaded.PRURL = ""
	if err := writeState(statePathFor(repo), loaded); err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, replanAfterEdit(t, repo, "package a\n// first\n// second\n"), "не является завершённой доставкой")
}

func TestRedeliveryRejectsHeadBeyondRecordedCommit(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	deliverOnce(t, repo, "feat", "change", "package a\n// first\n")
	git(t, repo, "commit", "--allow-empty", "-m", "agent commit on top")
	expectRefusal(t, replanAfterEdit(t, repo, "package a\n// first\n// second\n"), "не созданные контроллером")
}

func TestRedeliveryRejectsCommitSwappedUnderValidState(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	first, _ := deliverOnce(t, repo, "feat", "change", "package a\n// first\n")
	// Ветка пересобрана агентом: чужой commit, затем commit с тем же
	// сообщением. SHA не совпадёт с записанным в подписанном state.
	git(t, repo, "reset", "--hard", "main")
	git(t, repo, "commit", "--allow-empty", "-m", "agent first")
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// first\n")
	git(t, repo, "commit", "-am", first.CommitMessage)
	expectRefusal(t, replanAfterEdit(t, repo, "package a\n// first\n// second\n"), "не созданные контроллером")
}

func TestRedeliveryRejectsRunOutsidePlannedBranch(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	deliverOnce(t, repo, "feat", "change", "package a\n// first\n")
	git(t, repo, "switch", "-c", "work")
	expectRefusal(t, replanAfterEdit(t, repo, "package a\n// first\n// second\n"), "должна быть protected base")
	git(t, repo, "switch", "--detach")
	expectRefusal(t, replanAfterEdit(t, repo, "package a\n// first\n// second\n"), "не созданные контроллером")
}

// Ушедший вперёд base даёт отдельное понятное сообщение про rebase и не
// путается с чужим commit.
func TestRedeliveryBaseAdvancedAsksForRebase(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	deliverOnce(t, repo, "feat", "change", "package a\n// first\n")
	git(t, repo, "switch", "main")
	writeFile(t, filepath.Join(repo, "unrelated.txt"), "moved\n")
	git(t, repo, "commit", "-am", "base moved")
	git(t, repo, "switch", "ai-team/feat")
	err := replanAfterEdit(t, repo, "package a\n// first\n// second\n")
	expectRefusal(t, err, "требуется rebase")
	if strings.Contains(err.Error(), "не созданные контроллером") {
		t.Fatalf("base ушла вперёд не должна выглядеть как чужой commit: %v", err)
	}
}

func TestRedeliveryRejectsTipUnrelatedToBase(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	deliverOnce(t, repo, "feat", "change", "package a\n// first\n")
	git(t, repo, "switch", "--orphan", "other")
	git(t, repo, "commit", "--allow-empty", "-m", "unrelated root")
	git(t, repo, "branch", "-f", "main", "other")
	git(t, repo, "switch", "ai-team/feat")
	if err := replanAfterEdit(t, repo, "package a\n// first\n// second\n"); err == nil {
		t.Fatal("tip, не происходящий от base, должен отвергаться")
	}
}

func TestVerifyControllerDeliveredTipCreatesNothing(t *testing.T) {
	repo, _ := setupRepository(t)
	head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	if err := verifyControllerDeliveredTip(context.Background(), repo, "feat", "ai-team/feat", "ai-team/feat", "main", head); err == nil {
		t.Fatal("без state проверка должна отказывать")
	}
	if _, err := os.Stat(filepath.Join(repo, ".ai-team")); !os.IsNotExist(err) {
		t.Fatalf("проверка не должна создавать каталоги: %v", err)
	}
}

// Повторная доставка: push без force, PR не создаётся второй раз, а
// не-fast-forward push (ветку на remote изменили) завершается ошибкой.
func TestRedeliveryPushIsFastForwardOnlyAndReusesPR(t *testing.T) {
	repo, remote := setupRepository(t)
	calls := installFakeGH(t)
	_, first := deliverOnce(t, repo, "feat", "change", "package a\n// first\n")
	_, second := deliverOnce(t, repo, "feat", "fix review", "package a\n// first\n// after review\n")
	if got := countCalls(t, calls, "pr create"); got != 1 {
		t.Fatalf("gh pr create вызван %d раз, ожидался 1", got)
	}
	pushed := false
	for _, step := range second.Steps {
		if step.Step != "push" {
			continue
		}
		pushed = true
		joined := " " + strings.Join(step.Command, " ")
		if strings.Contains(joined, "--force") || strings.Contains(joined, " +") || strings.Contains(joined, " -f") {
			t.Fatalf("push не должен быть force: %s", joined)
		}
	}
	if !pushed {
		t.Fatal("шаг push не найден")
	}
	if second.PRURL != first.PRURL {
		t.Fatalf("PR должен быть тем же: %s / %s", first.PRURL, second.PRURL)
	}

	// Кто-то переписал ветку на remote: третья доставка не должна затереть её.
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", remote, other)
	git(t, other, "config", "user.name", "Other")
	git(t, other, "config", "user.email", "other@example.test")
	git(t, other, "switch", "-c", "tmp", "origin/main")
	writeFile(t, filepath.Join(other, "x.txt"), "x\n")
	git(t, other, "add", "x.txt")
	git(t, other, "commit", "-m", "diverged")
	git(t, other, "push", "--force", "origin", "tmp:refs/heads/ai-team/feat")
	diverged := strings.Fields(git(t, repo, "ls-remote", "origin", "refs/heads/ai-team/feat"))[0]

	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// third\n")
	plan, err := BuildPlan(context.Background(), repo, "feat", "third", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "feat", Plan: plan}); err == nil {
		t.Fatal("non-fast-forward push должен завершаться ошибкой")
	}
	if now := strings.Fields(git(t, repo, "ls-remote", "origin", "refs/heads/ai-team/feat"))[0]; now != diverged {
		t.Fatalf("remote branch была перезаписана: %s -> %s", diverged, now)
	}
	if got := countCalls(t, calls, "pr create"); got != 1 {
		t.Fatalf("gh pr create вызван %d раз после отказа, ожидался 1", got)
	}
}
