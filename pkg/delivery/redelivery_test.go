package delivery

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Повторный проход реализации после возврата из ревью: второй commit встаёт
// поверх первого на той же ветке, PR обновляется, а не создаётся заново.
func TestRedeliveryAfterReviewLoopbackStacksCommitOnSameBranch(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// first\n")
	first, err := BuildPlan(context.Background(), repo, "feat", "change", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	firstResult, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "feat", Plan: first})
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// first\n// after review\n")
	second, err := BuildPlan(context.Background(), repo, "feat", "fix review", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatalf("повторный plan после возврата из ревью: %v", err)
	}
	if second.BaselineHead != firstResult.CommitSHA || second.BaseBranch != "main" || second.Branch != first.Branch {
		t.Fatalf("baseline повторного plan должен быть tip прошлой доставки: %+v", second)
	}
	secondResult, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "feat", Plan: second})
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.CommitSHA == firstResult.CommitSHA || secondResult.PRURL != firstResult.PRURL {
		t.Fatalf("ожидался новый commit в том же PR: %+v / %+v", firstResult, secondResult)
	}
	if parent := strings.TrimSpace(git(t, repo, "rev-parse", secondResult.CommitSHA+"^")); parent != firstResult.CommitSHA {
		t.Fatalf("parent второго commit %s, ожидался %s", parent, firstResult.CommitSHA)
	}
	remoteTip := strings.Fields(git(t, repo, "ls-remote", "origin", "refs/heads/ai-team/feat"))[0]
	if remoteTip != secondResult.CommitSHA {
		t.Fatalf("remote branch %s не обновлена до %s", remoteTip, secondResult.CommitSHA)
	}
}

// Инвариант не ослаблен: чужой commit поверх base по-прежнему отвергается.
func TestRedeliveryRejectsForeignCommitsOnBranch(t *testing.T) {
	repo, _ := setupRepository(t)
	git(t, repo, "switch", "-c", "ai-team/feat")
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// manual\n")
	git(t, repo, "commit", "-am", "manual commit")
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// manual\n// more\n")
	_, err := BuildPlan(context.Background(), repo, "feat", "change", []string{"a.go"}, testVerification(t, repo))
	if err == nil || !strings.Contains(err.Error(), "не созданные контроллером") {
		t.Fatalf("ожидался отказ для чужого commit, got %v", err)
	}
}
